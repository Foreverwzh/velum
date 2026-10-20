package singbox_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/option"
)

func freePort(t *testing.T) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return netip.MustParseAddrPort(ln.Addr().String())
}

// 端到端：真实 sing-box 引擎 + 本地目标，不访问外网。验证动态实录的规则下标、Provenance、出站与 REJECT 记录。
func TestEngineLiveRecords(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello velum")) }))
	defer target.Close()

	sub, err := subscription.Parse([]byte(`
rules:
  - DOMAIN-SUFFIX,blocked.test,REJECT
  - IP-CIDR,127.0.0.1/32,DIRECT,no-resolve
  - MATCH,REJECT
`), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	events, cancel := eng.Subscribe()
	defer cancel()

	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, _ = client.Get("http://blocked.test/")

	got := map[string]engine.Connection{}
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case c := <-events:
			got[c.Chain[len(c.Chain)-1]] = c
		case <-deadline:
			t.Fatalf("只收到 %d 条记录：%+v", len(got), got)
		}
	}
	direct := got[ir.OutboundDirect]
	if direct.Provenance.Text != "IP-CIDR,127.0.0.1/32,DIRECT,no-resolve" || direct.Provenance.Line != 4 {
		t.Errorf("DIRECT 记录来源错误：%+v", direct.Provenance)
	}
	if direct.Download == 0 || direct.FirstByteMS < 0 {
		t.Errorf("DIRECT 记录缺少字节数 / 首包：%+v", direct)
	}
	if plan.IR.Rules[direct.RuleIndex].Source != direct.Provenance {
		t.Errorf("规则下标与来源不对应：%d", direct.RuleIndex)
	}
	reject := got[ir.OutboundReject]
	if reject.Provenance.Text != "DOMAIN-SUFFIX,blocked.test,REJECT" || reject.Domain != "blocked.test" {
		t.Errorf("REJECT 记录错误：%+v", reject)
	}
	if len(reject.Errors) == 0 || !strings.Contains(reject.Errors[0], "block") {
		t.Errorf("REJECT 记录应关联拦截日志：%+v", reject.Errors)
	}
	if n := len(eng.Connections(10)); n < 2 {
		t.Errorf("历史记录 %d 条", n)
	}
}

func TestEngineRestartAndStop(t *testing.T) {
	listen := freePort(t)
	plan, _ := compiler.Compile(nil, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: listen})
	r, _ := render.Render(plan, render.Options{})
	eng := singbox.New(singbox.Options{})
	cfg := engine.Config{JSON: r.JSON, RuleSources: r.RuleSources}
	if err := eng.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background(), cfg); err == nil {
		t.Fatal("重复 Start 应报错")
	}
	if err := eng.Restart(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !eng.Running() {
		t.Fatal("Restart 后应在运行")
	}
	if err := eng.Stop(); err != nil || eng.Running() {
		t.Fatalf("Stop: %v running=%v", err, eng.Running())
	}
	if c, err := net.DialTimeout("tcp", listen.String(), 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("Stop 后端口仍在监听")
	}
}

// 回归：规则直接指向 DIRECT 的域名，必须按 DNS 规则解析（overlay / nameserver-policy），
// 不能被 default_domain_resolver 绕过（sing-box ≥1.12 语义）。用 DNS 规则的预置应答指向本地测试服务验证。
func TestDirectDomainResolvesThroughDNSRules(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))

	sub, _ := subscription.Parse([]byte("rules:\n  - DOMAIN-SUFFIX,dev.test,DIRECT\n  - MATCH,REJECT\n"), subscription.Options{})
	listen := freePort(t)
	plan, err := compiler.Compile(sub, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	plan.IR.DNS.Rules = append([]ir.DNSRule{{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"dev.test"}}), Answer: []string{"svc.dev.test. IN A 127.0.0.1"}}}, plan.IR.DNS.Rules...)
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Get("http://svc.dev.test:" + port + "/")
	if err != nil {
		t.Fatalf("经 DNS 规则解析失败：%v", err)
	}
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	cs := eng.Connections(5)
	if len(cs) == 0 || cs[0].Provenance.Text != "DOMAIN-SUFFIX,dev.test,DIRECT" || len(cs[0].Resolved) == 0 || cs[0].Resolved[0] != "127.0.0.1" {
		t.Fatalf("记录：%+v", cs)
	}
	if plan.IR.Rules[cs[0].RuleIndex].Source.Text != "DOMAIN-SUFFIX,dev.test,DIRECT" {
		t.Fatalf("RuleIndex 应为 IR 下标：%d", cs[0].RuleIndex)
	}
}

// Velum 下载后的规则集以本地 .srs 文件交给内核；下载失败的规则集降级为空的内联集。两者都必须能正常启动。
func TestEngineStartsWithLocalAndEmptyRuleSets(t *testing.T) {
	dir := t.TempDir()
	srsPath := filepath.Join(dir, "geoip-cn.srs")
	var buf bytes.Buffer
	if err := srs.Write(&buf, option.PlainRuleSet{Rules: []option.HeadlessRule{{Type: "default", DefaultOptions: option.DefaultHeadlessRule{IPCIDR: []string{"1.0.1.0/24"}}}}}, 3); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(srsPath, buf.Bytes(), 0o600)
	sub, _ := subscription.Parse([]byte("rules:\n  - GEOIP,CN,DIRECT\n  - GEOSITE,missing,REJECT\n  - MATCH,DIRECT\n"), subscription.Options{})
	for i := range sub.RuleSets {
		switch sub.RuleSets[i].Tag {
		case "geoip-cn":
			sub.RuleSets[i].Kind, sub.RuleSets[i].Path = ir.RuleSetFile, srsPath
		default:
			sub.RuleSets[i].Kind, sub.RuleSets[i].Rules = ir.RuleSetInline, nil
		}
	}
	plan, _ := compiler.Compile(sub, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: freePort(t)})
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatalf("启动失败：%v\n%s", err, r.JSON)
	}
	_ = eng.Stop()
}

func TestRuleSetFileRoundTripAndResolve(t *testing.T) {
	dir := t.TempDir()
	conds := []ir.Cond{{DomainSuffix: []string{"cn", ".sub.example"}, Domain: []string{"exact.example"}, IPCIDR: []string{"1.0.1.0/24"}}, {Port: []int{8443}}}
	data, err := singbox.EncodeRuleSet(conds)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.srs")
	_ = os.WriteFile(p, data, 0o600)
	got, err := singbox.LoadRuleSetFile(p, "binary")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if !got[0].NoResolve || len(got[0].IPCIDR) != 1 || len(got[0].Domain) != 1 || len(got[0].DomainSuffix) != 2 || got[1].Port[0] != 8443 {
		t.Fatalf("还原结果：%+v", got)
	}
	src := filepath.Join(dir, "x.json")
	_ = os.WriteFile(src, []byte(`{"version":3,"rules":[{"type":"logical","mode":"and","rules":[{"domain":["a"]},{"port":[1]}]}]}`), 0o600)
	if _, err := singbox.LoadRuleSetFile(src, "source"); err == nil {
		t.Fatal("包含逻辑规则时应报错")
	}

	// 引擎 DNS 解析：经 DNS 规则（此处为预置应答）
	sub, _ := subscription.Parse([]byte("rules:\n  - MATCH,DIRECT\n"), subscription.Options{})
	plan, _ := compiler.Compile(sub, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: freePort(t)})
	plan.IR.DNS.Rules = append([]ir.DNSRule{{Match: ir.Leaf(ir.Cond{Domain: []string{"svc.dev.test"}}), Answer: []string{"svc.dev.test. IN A 10.9.8.7"}}}, plan.IR.DNS.Rules...)
	r, _ := render.Render(plan, render.Options{})
	eng := singbox.New(singbox.Options{})
	if _, err := eng.Resolve(context.Background(), "svc.dev.test"); err == nil {
		t.Fatal("引擎未运行时应报错")
	}
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	addrs, err := eng.Resolve(context.Background(), "svc.dev.test")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "10.9.8.7" {
		t.Fatalf("%v %v", addrs, err)
	}
}

func TestEngineSelectMember(t *testing.T) {
	sub, _ := subscription.Parse([]byte("proxy-groups:\n  - {name: G, type: select, proxies: [DIRECT, REJECT]}\nrules:\n  - MATCH,G\n"), subscription.Options{})
	plan, _ := compiler.Compile(sub, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: freePort(t), Selections: map[string]string{"G": "REJECT"}})
	r, _ := render.Render(plan, render.Options{})
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	if got := eng.Selected("G"); got != "REJECT" {
		t.Fatalf("应按 Selections 设为默认：%s", got)
	}
	if err := eng.SelectMember("G", "DIRECT"); err != nil || eng.Selected("G") != "DIRECT" {
		t.Fatalf("%v %s", err, eng.Selected("G"))
	}
	if err := eng.SelectMember("G", "nope"); err == nil {
		t.Fatal("非成员应报错")
	}
	if err := eng.SelectMember("DIRECT", "x"); err == nil {
		t.Fatal("非 selector 应报错")
	}
}
