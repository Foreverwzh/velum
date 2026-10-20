package source_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/source"
)

func TestProviderReaderFallbacks(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "p.yaml")
	_ = os.WriteFile(local, []byte("local"), 0o600)
	var hits atomic.Int32
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("remote"))
	}))
	defer srv.Close()

	f := source.New(filepath.Join(dir, "cache"), "test")
	read := f.ProviderReader()
	if b, _ := read(local, ""); string(b) != "local" {
		t.Fatal("无 url 时应读本地文件")
	}
	if b, _ := read(local, srv.URL); string(b) != "remote" {
		t.Fatalf("有 url 时应拉取远程：%s", b)
	}
	if b, _ := read(local, srv.URL); string(b) != "remote" || hits.Load() != 1 {
		t.Fatalf("TTL 内应使用缓存：%s hits=%d", b, hits.Load())
	}
	f.TTL = 0
	up = false
	if b, _ := read(local, srv.URL); string(b) != "remote" {
		t.Fatalf("远程失败应回落 Velum 缓存：%s", b)
	}
	if b, err := read(local, srv.URL+"/never-cached"); string(b) != "local" || err != nil {
		t.Fatalf("无缓存且远程失败应回落 provider path：%s %v", b, err)
	}
	f.PreferLocal = true
	before := hits.Load()
	if b, _ := read(local, srv.URL); string(b) != "local" || hits.Load() != before {
		t.Fatal("PreferLocal 时不应请求远程")
	}
}

func TestResolveRemoteRuleSets(t *testing.T) {
	dir := t.TempDir()
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("srs-bytes"))
	}))
	defer srv.Close()
	f := source.New(filepath.Join(dir, "cache"), "test")
	mk := func() *ir.Config {
		return &ir.Config{RuleSets: []ir.RuleSet{
			{Tag: "geoip-cn", Kind: ir.RuleSetRemote, URL: srv.URL + "/geoip-cn.srs", Format: "binary"},
			{Tag: "rs:local", Kind: ir.RuleSetInline},
		}}
	}
	cfg := mk()
	if ws := f.ResolveRemoteRuleSets(cfg, time.Hour); len(ws) != 0 {
		t.Fatalf("不应有告警：%+v", ws)
	}
	rs := cfg.RuleSets[0]
	if rs.Kind != ir.RuleSetFile || rs.Path == "" || cfg.RuleSets[1].Kind != ir.RuleSetInline {
		t.Fatalf("%+v", cfg.RuleSets)
	}
	if b, _ := os.ReadFile(rs.Path); string(b) != "srs-bytes" {
		t.Fatalf("缓存文件内容：%q", b)
	}
	// 远程失败但有缓存：使用缓存并告警
	up = false
	cfg = mk()
	ws := f.ResolveRemoteRuleSets(cfg, 0)
	if cfg.RuleSets[0].Kind != ir.RuleSetFile || len(ws) != 1 || ws[0].Level != ir.Degraded {
		t.Fatalf("%+v %+v", cfg.RuleSets[0], ws)
	}
	// 远程失败且无缓存：降级为空集，不阻断启动
	cfg = &ir.Config{RuleSets: []ir.RuleSet{{Tag: "geosite-x", Kind: ir.RuleSetRemote, URL: srv.URL + "/never.srs"}}}
	ws = f.ResolveRemoteRuleSets(cfg, 0)
	if cfg.RuleSets[0].Kind != ir.RuleSetInline || len(cfg.RuleSets[0].Rules) != 0 || len(ws) != 1 || ws[0].Level != ir.Unsupported {
		t.Fatalf("%+v %+v", cfg.RuleSets[0], ws)
	}
}

// Stale：有缓存就用（即使已过期）并标记需要后台更新；没有缓存才拉取。
func TestStaleUsesExpiredCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("remote"))
	}))
	defer srv.Close()
	f := source.New(t.TempDir(), "test")
	if _, _, err := f.Get("providers", srv.URL, time.Hour); err != nil || hits.Load() != 1 {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(f.CachePath("providers", srv.URL), old, old)

	f.Stale = true
	b, res, err := f.Get("providers", srv.URL, time.Hour)
	if err != nil || string(b) != "remote" || res.Origin != "cache" || hits.Load() != 1 || !f.UsedStale() {
		t.Fatalf("应直接使用过期缓存：%s %+v hits=%d stale=%v", b, res, hits.Load(), f.UsedStale())
	}
	if _, _, err := f.Get("providers", srv.URL+"/new", time.Hour); err != nil || hits.Load() != 2 {
		t.Fatal("没有缓存时应拉取")
	}

	f = source.New(f.CacheDir, "test")
	if _, res, _ := f.Get("providers", srv.URL, time.Hour); res.Origin != "remote" || f.UsedStale() {
		t.Fatalf("非 Stale 时过期缓存应重新拉取：%+v", res)
	}
}
