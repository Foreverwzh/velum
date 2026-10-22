package source_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestFetchLogHidesURL(t *testing.T) {
	f := source.New(t.TempDir(), "")
	var logs []string
	f.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	if _, _, err := f.Get("subscription", "http://127.0.0.1:9/sub?token=secret", time.Hour); err == nil {
		t.Fatal("应拉取失败")
	}
	if len(logs) != 1 || strings.Contains(logs[0], "secret") || !strings.Contains(logs[0], "127.0.0.1:9") {
		t.Fatalf("失败日志应只含主机名：%q", logs)
	}
}

// 引擎运行时先经 Velum 的本地端口拉取（按用户规则走），它不通时直连；本机 / 局域网地址始终直连。
func TestFetchViaVelum(t *testing.T) {
	var viaHits atomic.Int32
	via := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaHits.Add(1)
		_, _ = w.Write([]byte("via " + r.URL.String()))
	}))
	defer via.Close()
	viaURL, _ := url.Parse(via.URL)

	f := source.New(t.TempDir(), "test")
	f.Via = viaURL
	if b, _, err := f.Get("providers", "http://rules.example/a.list", 0); err != nil || string(b) != "via http://rules.example/a.list" {
		t.Fatalf("应经 Velum 拉取：%s %v", b, err)
	}

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("direct")) }))
	defer direct.Close()
	if b, _, _ := f.Get("providers", direct.URL, 0); string(b) != "direct" || viaHits.Load() != 1 {
		t.Fatalf("本机地址应直连：%s（经 Velum %d 次）", b, viaHits.Load())
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead, _ := url.Parse("http://" + ln.Addr().String())
	ln.Close()
	f.Via = dead
	f.Client = source.NewClient(func(*http.Request) (*url.URL, error) { return viaURL, nil }) // 用 via 服务器模拟「直连」到外网
	if b, _, err := f.Get("providers", "http://rules.example/b.list", 0); err != nil || !strings.HasPrefix(string(b), "via ") {
		t.Fatalf("Velum 端口不通时应直连：%s %v", b, err)
	}
}
