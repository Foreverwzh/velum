package explain_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

func plan(t *testing.T, mode string) *compiler.Plan {
	t.Helper()
	td, _ := filepath.Abs("../../testdata")
	data, err := os.ReadFile(filepath.Join(td, "coverage/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := subscription.Parse(data, subscription.Options{File: "config.yaml", BaseDirs: []string{filepath.Join(td, "coverage")}})
	if err != nil {
		t.Fatal(err)
	}
	ov, err := overlay.Load(filepath.Join(td, "overlay.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := compiler.Compile(sub, ov, compiler.Options{Mode: mode, Listen: netip.MustParseAddrPort("127.0.0.1:7899")})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func why(t *testing.T, p *compiler.Plan, target string) explain.Report {
	t.Helper()
	tg, err := explain.ParseTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	return explain.Why(p, tg)
}

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]string{
		"https://A.Example.com/x": "a.example.com:443", "http://a.com": "a.com:80", "a.com:8443": "a.com:8443",
		"1.2.3.4": "1.2.3.4:443", "[2001:db8::1]:53": "2001:db8::1:53", "a.com": "a.com:443",
	} {
		tg, err := explain.ParseTarget(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := tg.Host() + ":" + itoa(int(tg.Port)); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func itoa(i int) string { return netip.AddrPortFrom(netip.IPv4Unspecified(), uint16(i)).String()[8:] }

func TestWhyHostsOverride(t *testing.T) {
	r := why(t, plan(t, compiler.ModeSysProxy), "http://staging.corp.example.com/")
	if len(r.DNS.Answer) != 1 || r.Decision.OverrideAddress != "10.12.34.56" || r.Decision.Outbound != ir.OutboundDirect {
		t.Fatalf("%+v", r.Decision)
	}
	if r.SysProxy.Bypassed {
		t.Fatal("hosts 主机不应命中系统代理豁免")
	}
	if r.Decision.Source.Kind != ir.SourceOverlay || r.Decision.Source.Line == 0 {
		t.Fatalf("来源：%+v", r.Decision.Source)
	}
}

func TestWhyDevBypassDomain(t *testing.T) {
	p := plan(t, compiler.ModeSysProxy)
	r := why(t, p, "https://a.svc.example.test/")
	if !r.SysProxy.Bypassed || r.Decision.Outbound != ir.OutboundDirect || r.DNS.Server != compiler.LocalDNS {
		t.Fatalf("sysproxy=%+v dns=%+v decision=%+v", r.SysProxy, r.DNS, r.Decision)
	}
	// corp.example.com 下有 hosts 覆盖：*.corp.example.com 不写入豁免，但仍经规则直连
	r = why(t, p, "https://api.corp.example.com/")
	if r.SysProxy.Bypassed || r.Decision.Outbound != ir.OutboundDirect {
		t.Fatalf("sysproxy=%+v decision=%+v", r.SysProxy, r.Decision)
	}
}

func TestWhySubscriptionRuleAndMisses(t *testing.T) {
	r := why(t, plan(t, compiler.ModeProxy), "https://api.openai.com/")
	if r.Decision.Outbound != "Proxy" || r.Decision.Source.Text != "DOMAIN-KEYWORD,openai,Proxy" {
		t.Fatalf("%+v", r.Decision)
	}
	misses := 0
	for _, tr := range r.Rules {
		if !tr.Matched && !tr.Unknown && tr.Reason == "" {
			t.Errorf("未命中规则缺少原因：%+v", tr)
		}
		if !tr.Matched {
			misses++
		}
	}
	if misses == 0 {
		t.Fatal("应记录此前未命中的规则")
	}
}

func TestWhyLogicalAndReject(t *testing.T) {
	p := plan(t, compiler.ModeProxy)
	if r := why(t, p, "https://ad.example.com/"); r.Decision.Action != ir.ActionReject {
		t.Fatalf("%+v", r.Decision)
	}
	tg, _ := explain.ParseTarget("https://www.youtube.com/")
	tg.Network = "udp"
	if r := explain.Why(p, tg); r.Decision.Action != ir.ActionReject || r.Decision.Source.Text != "AND,((DOMAIN-SUFFIX,youtube.com),(NETWORK,UDP)),REJECT" {
		t.Fatalf("UDP youtube: %+v", r.Decision)
	}
}

func TestWhyRemoteRuleSetIsUncertain(t *testing.T) {
	// .cn 域名越过 NOT,((DOMAIN-SUFFIX,cn)) 规则，进入 GEOSITE 远程规则集
	r := why(t, plan(t, compiler.ModeProxy), "https://unknown-site.example.cn/")
	if !r.Decision.Uncertain {
		t.Fatalf("经过 GEOSITE / GEOIP 远程规则集，结论应标记为不确定：%+v", r.Decision)
	}
}

func TestMatchSysProxyException(t *testing.T) {
	cases := []struct {
		pat, host string
		want      bool
	}{
		{"*.a.com", "x.y.a.com", true}, {"*.a.com", "a.com", false}, {"a.com", "a.com", true},
		{"a.com", "x.a.com", false}, {"10.0.0.0/8", "10.1.2.3", true}, {"10.0.0.0/8", "a.com", false},
	}
	for _, c := range cases {
		ip, _ := netip.ParseAddr(c.host)
		if got := explain.MatchSysProxyException(c.pat, c.host, ip); got != c.want {
			t.Errorf("%s vs %s: %v", c.pat, c.host, got)
		}
	}
}

func TestWhyWithLoadedRuleSetsAndResolution(t *testing.T) {
	sub, _ := subscription.Parse([]byte("rules:\n  - GEOIP,CN,DIRECT\n  - MATCH,Proxy\nproxies:\n  - {name: Proxy, type: socks5, server: 192.0.2.1, port: 1080}\n"), subscription.Options{})
	p, err := compiler.Compile(sub, nil, compiler.Options{Mode: compiler.ModeProxy, Listen: netip.MustParseAddrPort("127.0.0.1:7899")})
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.IR.RuleSets { // 模拟 daemon 已下载为本地文件
		p.IR.RuleSets[i].Kind, p.IR.RuleSets[i].Path = ir.RuleSetFile, "/tmp/geoip-cn.srs"
	}
	loaded := map[string][]ir.Cond{"geoip-cn": {{IPCIDR: []string{"1.0.1.0/24"}, NoResolve: true}}}
	tg, _ := explain.ParseTarget("https://cn.example/")

	// 未加载规则集：未知
	if r := explain.Why(p, tg); !r.Decision.Uncertain {
		t.Fatalf("未加载时应标记不确定：%+v", r.Decision)
	}
	// 已加载但没有解析结果：IP 条件未知，而不是未命中
	r := explain.WhyWith(p, tg, loaded)
	if !r.Decision.Uncertain || r.Decision.Outbound != "Proxy" {
		t.Fatalf("缺少解析结果时应走到 final 且标记不确定：%+v", r.Decision)
	}
	// 有解析结果：确定命中 / 未命中
	tg.ResolvedIPs = []netip.Addr{netip.MustParseAddr("1.0.1.9")}
	if r := explain.WhyWith(p, tg, loaded); r.Decision.Uncertain || r.Decision.Outbound != ir.OutboundDirect {
		t.Fatalf("解析到 CN 地址应确定命中 GEOIP：%+v", r.Decision)
	}
	tg.ResolvedIPs = []netip.Addr{netip.MustParseAddr("8.8.8.8")}
	if r := explain.WhyWith(p, tg, loaded); r.Decision.Uncertain || r.Decision.Outbound != "Proxy" {
		t.Fatalf("解析到非 CN 地址应确定未命中：%+v", r.Decision)
	}
}
