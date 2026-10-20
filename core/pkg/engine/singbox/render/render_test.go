package render_test

import (
	"context"
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/race"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/include"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	sjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

var update = flag.Bool("update", false, "更新 golden 文件")

func repoTestdata(t *testing.T) string {
	t.Helper()
	d, _ := filepath.Abs("../../../../testdata")
	return d
}

func dnsRegistry() *dns.TransportRegistry {
	r := include.DNSTransportRegistry()
	race.Register(r)
	return r
}

func boxContext() context.Context {
	return box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		dnsRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
}

func buildPlan(t *testing.T, mode string) *compiler.Plan {
	t.Helper()
	td := repoTestdata(t)
	data, err := os.ReadFile(filepath.Join(td, "coverage/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := subscription.Parse(data, subscription.Options{File: "coverage/config.yaml", BaseDirs: []string{filepath.Join(td, "coverage")}})
	if err != nil {
		t.Fatal(err)
	}
	ov, err := overlay.Load(filepath.Join(td, "overlay.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ov.File = "overlay.yaml"
	plan, err := compiler.Compile(sub, ov, compiler.Options{Mode: mode, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: "darwin",
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")},
		Env: &compiler.Environment{SplitVPNs: []compiler.EnvVPN{{Iface: "utun7", Routes: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")},
			Domains: []string{"corp.example"}, Nameservers: []string{"172.20.0.53"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// 渲染产物必须能被 sing-box 自身加载（需构建标签 with_wireguard,with_utls,with_quic,with_gvisor）。
func TestRenderLoadsInSingBox(t *testing.T) {
	for _, mode := range []string{compiler.ModeProxy, compiler.ModeSysProxy, compiler.ModeTUN} {
		plan := buildPlan(t, mode)
		r, err := render.Render(plan, render.Options{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := boxContext()
		opts, err := sjson.UnmarshalExtendedContext[option.Options](ctx, r.JSON)
		if err != nil {
			t.Fatalf("%s: decode: %v", mode, err)
		}
		instance, err := box.New(box.Options{Context: ctx, Options: opts})
		if err != nil {
			if strings.Contains(err.Error(), "rebuild with -tags") {
				t.Fatalf("缺少构建标签，请用 make test（-tags with_wireguard,with_utls,with_quic,with_gvisor）：%v", err)
			}
			t.Fatalf("%s: box.New: %v", mode, err)
		}
		instance.Close()
		if len(r.RuleSources) == 0 {
			t.Fatal("RuleSources 为空")
		}
	}
}

func TestRenderGolden(t *testing.T) {
	plan := buildPlan(t, compiler.ModeSysProxy)
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(repoTestdata(t), "golden/coverage.singbox.json")
	if *update {
		_ = os.MkdirAll(filepath.Dir(golden), 0o755)
		if err := os.WriteFile(golden, r.JSON, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("缺少 golden，用 -update 生成：%v", err)
	}
	if string(want) != string(r.JSON) {
		t.Fatalf("渲染结果与 golden 不一致；确认变更后用 go test -update 更新：%s", golden)
	}
}

func metadata(m explain.Meta) *adapter.InboundContext {
	md := &adapter.InboundContext{Network: "tcp", Protocol: m.Protocol}
	if m.Network != "" {
		md.Network = m.Network
	}
	port := m.Port       // 0 表示未知端口，与 explain.Meta 语义一致
	md.Domain = m.Domain // 目标为 IP 时也可能带有嗅探出的域名
	switch {
	case m.IP.IsValid():
		md.Destination = M.SocksaddrFrom(m.IP, port)
		if m.IP.Is4() {
			md.IPVersion = 4
		} else {
			md.IPVersion = 6
		}
	case m.Domain != "":
		md.Destination = M.ParseSocksaddrHostPort(m.Domain, port)
	default:
		md.Destination = M.ParseSocksaddrHostPort("probe.invalid", port)
	}
	md.DestinationAddresses = m.ResolvedIPs
	if m.ProcessPath != "" {
		md.ProcessInfo = &adapter.ConnectionOwner{ProcessPath: m.ProcessPath}
	}
	if m.SourceIP.IsValid() {
		md.Source = M.SocksaddrFrom(m.SourceIP, 50000)
	}
	return md
}

func headless(t *testing.T, ctx context.Context, m ir.Match) adapter.HeadlessRule {
	t.Helper()
	data, _ := json.Marshal(render.MatchJSON(m))
	opt, err := sjson.UnmarshalExtendedContext[option.HeadlessRule](ctx, data)
	if err != nil {
		t.Fatalf("decode headless %s: %v", data, err)
	}
	hr, err := rule.NewHeadlessRule(ctx, opt)
	if err != nil {
		t.Fatalf("new headless %s: %v", data, err)
	}
	return hr
}

type matcher interface {
	Match(*adapter.InboundContext) bool
}

// routeRule 按真正的路由规则构造（ip_is_private 等字段只允许出现在路由规则中）。
func routeRule(t *testing.T, ctx context.Context, m ir.Match) matcher {
	t.Helper()
	o := render.MatchJSON(m)
	o["action"], o["outbound"] = "route", "DIRECT"
	data, _ := json.Marshal(o)
	opt, err := sjson.UnmarshalExtendedContext[option.Rule](ctx, data)
	if err != nil {
		t.Fatalf("decode rule %s: %v", data, err)
	}
	r, err := rule.NewRule(ctx, sblog.NewNOPFactory().NewLogger("test"), opt, false)
	if err != nil {
		t.Fatalf("new rule %s: %v", data, err)
	}
	return r
}

func hasRuleSet(m ir.Match) bool {
	if m.Cond != nil {
		return len(m.Cond.RuleSet) > 0
	}
	for _, ch := range m.Children {
		if hasRuleSet(ch) {
			return true
		}
	}
	return false
}

// 语义等价：IR 参考求值器（即 vlu why 静态推演）与 sing-box 匹配器对同一探测给出相同结论。
func TestSemanticEquivalence(t *testing.T) {
	plan := buildPlan(t, compiler.ModeSysProxy)
	ctx := boxContext()
	sets := explain.Sets{}
	for _, rs := range plan.IR.RuleSets {
		sets[rs.Tag] = rs
	}
	probes, mismatches := 0, 0
	check := func(what string, m ir.Match, asRoute bool) {
		var hr matcher
		if asRoute {
			hr = routeRule(t, ctx, m)
		} else {
			hr = headless(t, ctx, m)
		}
		for _, p := range explain.ProbesForMatch(m) {
			probes++
			want := explain.EvalMatch(m, p, sets).Matched
			got := hr.Match(metadata(p))
			if want != got {
				mismatches++
				if mismatches <= 20 {
					t.Errorf("%s probe %+v: IR=%v sing-box=%v", what, p, want, got)
				}
			}
		}
	}
	for _, r := range plan.IR.Rules {
		if (r.Match.Cond == nil && r.Match.Logical == "") || hasRuleSet(r.Match) {
			continue
		}
		check("rule "+r.Source.Text, r.Match, true)
	}
	for _, rs := range plan.IR.RuleSets {
		for i := range rs.Rules {
			check("ruleset "+rs.Tag, ir.Leaf(rs.Rules[i]), false)
		}
	}
	t.Logf("probes=%d mismatches=%d", probes, mismatches)
	if probes < 100 {
		t.Fatalf("探测太少（%d），检查探测生成", probes)
	}
}

// 本机真实配置端到端（不进 CI）：VELUM_TEST_STASH=1 make test
func TestLocalStashEndToEnd(t *testing.T) {
	if os.Getenv("VELUM_TEST_STASH") == "" {
		t.Skip("设置 VELUM_TEST_STASH=1 以使用本机 Stash 配置")
	}
	home, _ := os.UserHomeDir()
	core := filepath.Join(home, "Library/Application Support/Stash/Core")
	data, err := os.ReadFile(filepath.Join(core, "config.yaml"))
	if err != nil {
		t.Skip(err)
	}
	sub, err := subscription.Parse(data, subscription.Options{File: "stash/config.yaml", BaseDirs: []string{filepath.Join(core, "resources")}})
	if err != nil {
		t.Fatal(err)
	}
	ov, _ := overlay.Load(filepath.Join(repoTestdata(t), "overlay.yaml"))
	plan, err := compiler.Compile(sub, ov, compiler.Options{Mode: compiler.ModeSysProxy, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: "darwin"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := boxContext()
	opts, err := sjson.UnmarshalExtendedContext[option.Options](ctx, r.JSON)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	instance.Close()
	sets := explain.Sets{}
	entries := 0
	for _, rs := range plan.IR.RuleSets {
		sets[rs.Tag] = rs
	}
	probes, mismatches := 0, 0
	for _, rs := range plan.IR.RuleSets {
		for i := range rs.Rules {
			c := &rs.Rules[i]
			entries += len(c.Domain) + len(c.DomainSuffix) + len(c.DomainKeyword) + len(c.DomainRegex) + len(c.IPCIDR) + len(c.ProcessName)
			hr := headless(t, ctx, ir.Leaf(*c))
			for _, p := range explain.ProbesForCond(c) {
				probes++
				if want, got := explain.EvalCond(c, p, sets).Matched, hr.Match(metadata(p)); want != got {
					mismatches++
					t.Logf("%s probe %+v: IR=%v sing-box=%v", rs.Tag, p, want, got)
				}
			}
		}
	}
	t.Logf("nodes=%d groups=%d rules=%d rulesets=%d entries=%d warnings=%d probes=%d mismatches=%d json=%dKB",
		len(plan.IR.Nodes), len(plan.IR.Groups), len(plan.IR.Rules), len(plan.IR.RuleSets), entries, len(plan.Warnings), probes, mismatches, len(r.JSON)/1024)
	if mismatches > 0 {
		t.Fatalf("%d 处语义不一致", mismatches)
	}
}

// Windows：内核自建 wintun（固定网卡名），开启 auto_route，路由取自 RoutePlan。
func TestRenderWindowsTUN(t *testing.T) {
	plan := buildPlan(t, compiler.ModeTUN)
	plan.Options.Platform = "windows"
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := sjson.UnmarshalExtendedContext[option.Options](boxContext(), r.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var tun *option.TunInboundOptions
	for _, in := range opts.Inbounds {
		if o, ok := in.Options.(*option.TunInboundOptions); ok {
			tun = o
		}
	}
	if tun == nil {
		t.Fatal("缺少 tun 入站")
	}
	if tun.InterfaceName != render.WindowsTUNName || !tun.AutoRoute {
		t.Fatalf("interface_name=%q auto_route=%v", tun.InterfaceName, tun.AutoRoute)
	}
	if len(tun.RouteAddress) != len(plan.Route.Routes) || len(tun.RouteAddress) == 0 {
		t.Fatalf("route_address=%v，RoutePlan=%v", tun.RouteAddress, plan.Route.Routes)
	}
	for i, p := range tun.RouteAddress {
		if p.String() != plan.Route.Routes[i] {
			t.Fatalf("route_address[%d]=%s，期望 %s", i, p, plan.Route.Routes[i])
		}
	}

	// macOS 不变：不开 auto_route，由 helper 管路由
	r, _ = render.Render(buildPlan(t, compiler.ModeTUN), render.Options{})
	if !strings.Contains(string(r.JSON), `"auto_route": false`) || strings.Contains(string(r.JSON), "route_address") {
		t.Fatal("macOS 的 tun 入站不应开启 auto_route")
	}
}

func TestRenderAndroidTUN(t *testing.T) {
	plan := buildPlan(t, compiler.ModeTUN)
	plan.Options.Platform = "android"
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := sjson.UnmarshalExtendedContext[option.Options](boxContext(), r.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Route.AutoDetectInterface {
		t.Fatal("Android 不应开启 auto_detect_interface（netlink 受限，且 VpnService 已排除自身）")
	}
	for _, in := range opts.Inbounds {
		if o, ok := in.Options.(*option.TunInboundOptions); ok {
			if o.AutoRoute {
				t.Fatal("Android 的路由由 VpnService 设置，不应开启 auto_route")
			}
			if o.Stack != "gvisor" {
				t.Fatalf("Android 应使用 gvisor 栈（VpnService 排除了自身，system 栈回包不经 TUN），实际 %q", o.Stack)
			}
		}
	}
}

// respect-rules 会把 DNS 服务器的 detour 设为策略组（selector / urltest）：sing-box 必须能加载。
func TestRenderDNSDetourToGroup(t *testing.T) {
	plan := buildPlan(t, compiler.ModeProxy)
	group := ""
	for _, g := range plan.IR.Groups {
		group = g.Tag
		break
	}
	n := 0
	for i := range plan.IR.DNS.Servers {
		if s := &plan.IR.DNS.Servers[i]; s.Type == "tls" || s.Type == "https" || s.Type == "udp" {
			s.Detour = group
			n++
		}
	}
	if group == "" || n == 0 {
		t.Skip("样本中没有策略组或远程 DNS")
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := boxContext()
	opts, err := sjson.UnmarshalExtendedContext[option.Options](ctx, r.JSON)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		t.Fatalf("detour 到策略组 %q：%v", group, err)
	}
	instance.Close()
}
