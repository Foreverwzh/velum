package compiler_test

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

const sub = `
mode: rule
dns:
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  nameserver: [223.5.5.5]
  fake-ip-filter: ["+.lan"]
proxies:
  - {name: n1, type: ss, server: s.example.com, port: 8388, cipher: aes-128-gcm, password: x}
proxy-groups:
  - {name: Proxy, type: select, proxies: [n1, DIRECT]}
rules:
  - DOMAIN-SUFFIX,google.com,Proxy
  - IP-CIDR,8.8.8.0/24,Proxy,no-resolve
  - IP-CIDR,1.1.1.0/24,Proxy
  - MATCH,Proxy
`

const ov = `
dev_bypass:
  domains: [corp.example.com, .svc.test, =exact.test]
  cidrs: [10.12.0.0/16]
  hosts:
    staging.corp.example.com: 10.12.34.56
  processes: [SomeVPNClient]
system_proxy:
  bypass: [localhost, 192.168.0.0/16, "*.corp.example.com"]
`

func compile(t *testing.T, mode, platform string) *compiler.Plan {
	t.Helper()
	s, err := subscription.Parse([]byte(sub), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := overlay.Parse([]byte(ov))
	if err != nil {
		t.Fatal(err)
	}
	o.File = "overlay.yaml"
	p, err := compiler.Compile(s, o, compiler.Options{Mode: mode, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: platform,
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func exceptions(p *compiler.Plan) map[string]ir.Source {
	m := map[string]ir.Source{}
	for _, e := range p.SysProxy.Exceptions {
		m[e.Pattern] = e.Source
	}
	return m
}

// P0：hosts 覆盖的主机绝不能被系统代理豁免覆盖，否则浏览器直连走系统 DNS，覆盖失效。
func TestHostsNotCoveredBySysProxyExceptions(t *testing.T) {
	p := compile(t, compiler.ModeSysProxy, "darwin")
	ex := exceptions(p)
	if _, ok := ex["*.corp.example.com"]; ok {
		t.Fatal("*.corp.example.com（无论来自 dev_bypass 还是 system_proxy.bypass）会覆盖 hosts 主机 staging.corp.example.com，不应写入豁免")
	}
	if _, ok := ex["corp.example.com"]; !ok {
		t.Fatal("根域 corp.example.com 不含 hosts 主机，应写入豁免")
	}
	for _, want := range []string{"*.svc.test", "exact.test", "10.12.0.0/16", "localhost", "192.168.0.0/16"} { // 后两项来自 system_proxy.bypass
		if _, ok := ex[want]; !ok {
			t.Errorf("缺少豁免 %s", want)
		}
	}
	if _, ok := ex["svc.test"]; ok {
		t.Error(".svc.test 只匹配子域，不应豁免根域")
	}
	if src := ex["corp.example.com"]; src.Kind != ir.SourceOverlay || src.Line == 0 {
		t.Errorf("豁免缺少 overlay 来源：%+v", src)
	}
	found := false
	for _, w := range p.Warnings {
		if w.Category == "overlay" && strings.Contains(w.Reason, "staging.corp.example.com") {
			found = true
		}
	}
	if !found {
		t.Error("应对被跳过的豁免给出告警")
	}
}

func TestOverlayRulesPrecedeSubscriptionAndHostsOverride(t *testing.T) {
	p := compile(t, compiler.ModeSysProxy, "darwin")
	rules := p.IR.Rules
	if rules[0].Action.Kind != ir.ActionSniff {
		t.Fatalf("首条应为 sniff，实际 %+v", rules[0].Action)
	}
	firstSub := -1
	var hostRule *ir.Rule
	for i := range rules {
		if rules[i].Source.Kind == ir.SourceSubscription && firstSub < 0 {
			firstSub = i
		}
		if rules[i].Action.OverrideAddress == "10.12.34.56" {
			hostRule = &rules[i]
		}
		if rules[i].Source.Kind == ir.SourceOverlay && firstSub >= 0 {
			t.Fatalf("overlay 规则 %d 出现在订阅规则之后", i)
		}
	}
	if hostRule == nil || hostRule.Action.Outbound != ir.OutboundDirect {
		t.Fatal("缺少 hosts 覆盖规则")
	}
	answered := false
	for _, r := range p.IR.DNS.Rules {
		if len(r.Answer) == 1 && r.Answer[0] == "staging.corp.example.com. IN A 10.12.34.56" {
			answered = true
		}
	}
	if !answered {
		t.Fatal("DNS 层缺少 hosts 直接应答")
	}
}

func TestResolveInsertedBeforeFirstResolvingIPRule(t *testing.T) {
	p := compile(t, compiler.ModeSysProxy, "darwin")
	var order []string
	for _, r := range p.IR.Rules {
		switch {
		case r.Action.Kind == ir.ActionResolve:
			order = append(order, "resolve")
		case r.Source.Kind == ir.SourceSubscription:
			order = append(order, r.Source.Text)
		}
	}
	want := []string{"DOMAIN-SUFFIX,google.com,Proxy", "IP-CIDR,8.8.8.0/24,Proxy,no-resolve", "resolve", "IP-CIDR,1.1.1.0/24,Proxy"}
	if strings.Join(order, " | ") != strings.Join(want, " | ") {
		t.Fatalf("顺序错误：\n got  %v\n want %v", order, want)
	}
	if p.IR.Final.Outbound != "Proxy" {
		t.Fatalf("final=%+v", p.IR.Final)
	}
}

func TestFakeIPOnlyInTUN(t *testing.T) {
	if p := compile(t, compiler.ModeSysProxy, "darwin"); p.IR.DNS.FakeIP != nil {
		t.Fatal("系统代理模式不应启用 fake-ip")
	}
	if p := compile(t, compiler.ModeTUN, "darwin"); p.IR.DNS.FakeIP == nil {
		t.Fatal("TUN 模式应保留 fake-ip")
	}
}

func TestProcessesDroppedOnIOS(t *testing.T) {
	for _, r := range compile(t, compiler.ModeTUN, "ios").IR.Rules {
		if r.Match.Cond != nil && len(r.Match.Cond.ProcessName) > 0 {
			t.Fatal("iOS 不应生成进程规则")
		}
	}
}

func TestSysProxyPlan(t *testing.T) {
	p := compile(t, compiler.ModeSysProxy, "darwin")
	if !p.SysProxy.Enabled || p.SysProxy.Host != "127.0.0.1" || p.SysProxy.Port != 7899 {
		t.Fatalf("%+v", p.SysProxy)
	}
	if compile(t, compiler.ModeProxy, "darwin").SysProxy.Enabled {
		t.Fatal("proxy 模式不应接管系统代理")
	}
}

// 编译器不注入任何策略：订阅与 Overlay 均为空时，只有机制性规则（嗅探），豁免为空。
func TestNoBuiltinPolicy(t *testing.T) {
	p, err := compiler.Compile(nil, nil, compiler.Options{Mode: compiler.ModeSysProxy, Listen: netip.MustParseAddrPort("127.0.0.1:7899")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.IR.Rules {
		if r.Action.Kind != ir.ActionSniff {
			t.Errorf("编译器注入了策略规则：%+v（来源 %+v）", r, r.Source)
		}
	}
	if len(p.SysProxy.Exceptions) != 0 {
		t.Errorf("编译器注入了系统代理豁免：%+v", p.SysProxy.Exceptions)
	}
	if p.IR.Final.Kind != ir.ActionRoute || p.IR.Final.Outbound != ir.OutboundDirect {
		t.Errorf("final=%+v", p.IR.Final)
	}
}

func TestGeoRuleSetURLConfigurable(t *testing.T) {
	s, err := subscription.Parse([]byte("rules:\n  - GEOIP,CN,DIRECT\n"), subscription.Options{GeoRuleSetURL: "https://mirror.example/{kind}/{code}.srs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.RuleSets) != 1 || s.RuleSets[0].URL != "https://mirror.example/geoip/cn.srs" {
		t.Fatalf("%+v", s.RuleSets)
	}
}

func TestBuildRoutePlan(t *testing.T) {
	addr := netip.MustParsePrefix("172.19.0.1/30")
	// 全局：0/1 + 128/1，扣除排除网段，并包含 TUN 自身网段
	rp, err := compiler.BuildRoutePlan(compiler.TUNOptions{Address: addr, Exclude: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		[]netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	has := func(r string) bool {
		for _, x := range rp.Routes {
			if x == r {
				return true
			}
		}
		return false
	}
	covers := func(ip string) bool {
		for _, x := range rp.Routes {
			if netip.MustParsePrefix(x).Contains(netip.MustParseAddr(ip)) {
				return true
			}
		}
		return false
	}
	if has("0.0.0.0/0") || has("0.0.0.0/1") || !has("128.0.0.0/2") || !covers("172.19.0.2") || !covers("8.8.8.8") || covers("10.1.1.1") {
		t.Fatalf("routes=%v", rp.Routes)
	}
	for _, r := range rp.Routes {
		p := netip.MustParsePrefix(r)
		for _, ex := range []string{"10.0.0.0/8", "192.168.0.0/16"} {
			if p.Overlaps(netip.MustParsePrefix(ex)) {
				t.Fatalf("%s 与排除网段 %s 重叠", r, ex)
			}
		}
	}
	if rp.MTU != 9000 || len(rp.Exclude) != 2 {
		t.Fatalf("%+v", rp)
	}
	// 指定网段
	rp, _ = compiler.BuildRoutePlan(compiler.TUNOptions{Address: addr, Include: []netip.Prefix{netip.MustParsePrefix("223.6.6.6/32")}}, nil)
	if strings.Join(rp.Routes, ",") != "172.19.0.0/30,223.6.6.6/32" {
		t.Fatalf("routes=%v", rp.Routes)
	}
	if _, err := compiler.BuildRoutePlan(compiler.TUNOptions{}, nil); err == nil {
		t.Fatal("缺少地址应报错")
	}
}

func TestTUNModePlan(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	o, _ := overlay.Parse([]byte(ov))
	p, err := compiler.Compile(s, o, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"),
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Route.Enabled || p.SysProxy.Enabled || p.IR.DNS.FakeIP == nil {
		t.Fatalf("route=%+v sysproxy=%v fakeip=%v", p.Route.Enabled, p.SysProxy.Enabled, p.IR.DNS.FakeIP)
	}
	hijack := false
	for _, r := range p.IR.Rules {
		if r.Action.Kind == ir.ActionHijackDNS {
			hijack = true
		}
	}
	if !hijack {
		t.Fatal("TUN 模式应接管 DNS 查询")
	}
	for _, r := range p.Route.Routes { // overlay 的 cidrs 自动排除
		if netip.MustParsePrefix(r).Overlaps(netip.MustParsePrefix("10.12.0.0/16")) {
			t.Fatalf("overlay cidr 未排除：%s", r)
		}
	}
}

func TestEnvironmentSplitVPN(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	env := &compiler.Environment{SplitVPNs: []compiler.EnvVPN{{Iface: "utun7",
		Routes: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}, Domains: []string{"corp.example"}, Nameservers: []string{"172.20.0.53"}}},
		SystemDNSDomains: []string{"corp.example", "intra.example"}}
	p, err := compiler.Compile(s, nil, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Env: env,
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
	if err != nil {
		t.Fatal(err)
	}
	var vpnNode *ir.Node
	for i := range p.IR.Nodes {
		if p.IR.Nodes[i].Tag == compiler.VPNOutbound("utun7") {
			vpnNode = &p.IR.Nodes[i]
		}
	}
	if vpnNode == nil || vpnNode.Protocol != "direct" || vpnNode.Dial.BindInterface != "utun7" {
		t.Fatalf("应生成绑定 utun7 的直连出站：%+v", vpnNode)
	}
	firstSub, cidrRule, domRule := -1, -1, -1
	for i, r := range p.IR.Rules {
		if r.Source.Kind == ir.SourceSubscription && firstSub < 0 {
			firstSub = i
		}
		if r.Action.Outbound == compiler.VPNOutbound("utun7") {
			if r.Match.Cond.HasIP() {
				cidrRule = i
			} else {
				domRule = i
			}
		}
	}
	if cidrRule < 0 || domRule < 0 || cidrRule > firstSub || domRule > firstSub {
		t.Fatalf("VPN 规则应优先于订阅：cidr=%d dom=%d sub=%d", cidrRule, domRule, firstSub)
	}
	// 系统 DNS 分流优先于订阅的 DNS 规则，交给系统解析器
	sysIdx, subIdx := -1, -1
	for i, r := range p.IR.DNS.Rules {
		if r.Server == compiler.SystemResolverDNS && r.Match.Cond != nil && strings.Join(r.Match.Cond.DomainSuffix, ",") == "corp.example,intra.example" {
			sysIdx = i
		}
		if r.Source.Kind == ir.SourceSubscription && subIdx < 0 {
			subIdx = i
		}
	}
	if sysIdx < 0 || (subIdx >= 0 && sysIdx > subIdx) {
		t.Fatalf("系统 DNS 分流规则缺失或晚于订阅规则：sys=%d sub=%d", sysIdx, subIdx)
	}
	for _, r := range p.Route.Routes {
		if netip.MustParsePrefix(r).Overlaps(netip.MustParsePrefix("172.20.0.0/16")) {
			t.Fatalf("VPN 网段应从 TUN 中排除：%s", r)
		}
	}
	ex := exceptions(p)
	for _, want := range []string{"172.20.0.0/16", "corp.example", "*.corp.example"} {
		if _, ok := ex[want]; !ok {
			t.Errorf("缺少系统代理豁免 %s", want)
		}
	}
}

func TestBuildRoutePlanIPv6(t *testing.T) {
	rp, err := compiler.BuildRoutePlan(compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"),
		Address6: netip.MustParsePrefix("fdfe:dcba:9876::1/126"), Exclude: []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	covers := func(ip string) bool {
		for _, r := range rp.Routes {
			if netip.MustParsePrefix(r).Contains(netip.MustParseAddr(ip)) {
				return true
			}
		}
		return false
	}
	if !covers("2400::1") || !covers("fdfe:dcba:9876::2") || covers("2001:db8::1") || rp.Address6 != "fdfe:dcba:9876::1/126" {
		t.Fatalf("routes=%v", rp.Routes)
	}
	for _, r := range rp.Routes {
		if r == "::/0" {
			t.Fatal("不应出现 ::/0")
		}
	}
	// 没有 IPv6 地址：IPv6 包含网段被忽略
	rp, _ = compiler.BuildRoutePlan(compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"), Include: []netip.Prefix{netip.MustParsePrefix("2400::/16")}}, nil)
	if strings.Join(rp.Routes, ",") != "172.19.0.0/30" {
		t.Fatalf("routes=%v", rp.Routes)
	}
}

func TestDNSTakeoverRewritesLocal(t *testing.T) {
	s, _ := subscription.Parse([]byte("dns:\n  nameserver: [system]\nrules:\n  - MATCH,DIRECT\n"), subscription.Options{})
	mk := func(phys []string) *compiler.Plan {
		p, err := compiler.Compile(s, nil, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"),
			TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"), DNSHijack: true},
			Env: &compiler.Environment{PhysicalDNS: phys, SystemDNSDomains: []string{"corp.example"}}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := mk([]string{"192.168.1.1"})
	if strings.Join(p.Route.SystemDNS, ",") != "172.19.0.2" {
		t.Fatalf("SystemDNS=%v", p.Route.SystemDNS)
	}
	for _, sv := range p.IR.DNS.Servers {
		switch {
		case sv.Tag == compiler.SystemResolverDNS && sv.Type != "local":
			t.Error("系统 DNS 分流仍应用系统解析器")
		case sv.Tag != compiler.SystemResolverDNS && sv.Type == "local":
			t.Errorf("接管后 %s 不应再用系统解析器（会绕回 Velum）", sv.Tag)
		}
	}
	if p := mk(nil); len(p.Route.SystemDNS) != 0 {
		t.Fatal("没有物理 DNS 时不应接管")
	}
}

// Windows：VPN 域名直接经该 VPN 询问它的 DNS（系统解析器可能绕回 TUN 的 DNS）；macOS 不生成。
func TestEnvironmentSplitVPNDNSWindows(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	env := &compiler.Environment{SplitVPNs: []compiler.EnvVPN{{Iface: "Corp VPN",
		Routes: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}, Domains: []string{"corp.example"}, Nameservers: []string{"172.20.0.53", "172.20.0.54"}}}}
	for _, platform := range []string{"windows", "darwin"} {
		p, err := compiler.Compile(s, nil, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Env: env,
			Platform: platform, TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
		if err != nil {
			t.Fatal(err)
		}
		ob := compiler.VPNOutbound("Corp VPN")
		var srv *ir.DNSServer
		for i := range p.IR.DNS.Servers {
			if p.IR.DNS.Servers[i].Detour == ob {
				srv = &p.IR.DNS.Servers[i]
			}
		}
		ruleIdx, subIdx := -1, -1
		for i, r := range p.IR.DNS.Rules {
			if srv != nil && r.Server == srv.Tag && strings.Join(r.Match.Cond.DomainSuffix, ",") == "corp.example" {
				ruleIdx = i
			}
			if r.Source.Kind == ir.SourceSubscription && subIdx < 0 {
				subIdx = i
			}
		}
		if platform == "darwin" {
			if srv != nil {
				t.Fatalf("macOS 不应为 VPN 生成 DNS 服务器：%+v", srv)
			}
			continue
		}
		if srv == nil || srv.Type != "udp" || srv.Address != "172.20.0.53" {
			t.Fatalf("缺少经 VPN 的 DNS 服务器：%+v", srv)
		}
		if ruleIdx < 0 || (subIdx >= 0 && ruleIdx > subIdx) {
			t.Fatalf("VPN 域名的 DNS 规则应优先于订阅：rule=%d sub=%d", ruleIdx, subIdx)
		}
	}
}

// Android 没有 /etc/resolv.conf：不论是否开启 TUN，系统解析器都改为平台层报告的网络 DNS。
func TestAndroidUsesPhysicalDNS(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	for _, mode := range []string{compiler.ModeProxy, compiler.ModeTUN} {
		mk := func(phys []string) *compiler.Plan {
			p, err := compiler.Compile(s, nil, compiler.Options{Mode: mode, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: "android",
				TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}, Env: &compiler.Environment{PhysicalDNS: phys}})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		for _, sv := range mk([]string{"10.0.2.3"}).IR.DNS.Servers {
			if sv.Type == "local" {
				t.Errorf("%s：%s 仍使用系统解析器", mode, sv.Tag)
			}
			if sv.Tag == compiler.LocalDNS && sv.Address != "10.0.2.3" {
				t.Errorf("%s：系统 DNS 应改为 10.0.2.3，实际 %+v", mode, sv)
			}
		}
		found := false
		for _, w := range mk(nil).Warnings {
			found = found || w.Kind == "dns"
		}
		if !found {
			t.Errorf("%s：没有网络 DNS 时应告警", mode)
		}
	}
}

// 按应用分流只在 Android 上进入 RoutePlan，其他平台告警。
func TestTUNApps(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	for _, platform := range []string{"android", "darwin"} {
		p, err := compiler.Compile(s, nil, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: platform,
			Env: &compiler.Environment{PhysicalDNS: []string{"10.0.2.3"}},
			TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"), ExcludeApps: []string{"com.bank.app"}}})
		if err != nil {
			t.Fatal(err)
		}
		warned := false
		for _, w := range p.Warnings {
			warned = warned || w.Kind == "tun.exclude_apps"
		}
		if platform == "android" {
			if strings.Join(p.Route.ExcludeApps, ",") != "com.bank.app" || warned {
				t.Fatalf("android：exclude=%v warned=%v", p.Route.ExcludeApps, warned)
			}
		} else if len(p.Route.ExcludeApps) != 0 || !warned {
			t.Fatalf("%s：exclude=%v warned=%v", platform, p.Route.ExcludeApps, warned)
		}
	}
}

// 没有任何排除网段时，两个 /1 会被合并成 /0：应拆回 /1，始终不替换默认路由（helper 拒绝 /0）。
func TestBuildRoutePlanNoExclude(t *testing.T) {
	rp, err := compiler.BuildRoutePlan(compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"),
		Address6: netip.MustParsePrefix("fdfe:dcba:9876::1/126")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rp.Routes, ","); got != "0.0.0.0/1,128.0.0.0/1,::/1,8000::/1" {
		t.Fatalf("routes=%s", got)
	}
}

// 系统 DNS 分流（如企业 VPN 写入 /etc/resolver：corp-net.example → 127.0.0.1、10.8.3.129）：直接询问这些服务器，
// 不交给系统解析器（macOS 上 sing-box 的系统解析器按默认 DNS 查询，接管后即 Velum 自己，会回环）。
func TestSystemDNSSplitResolvers(t *testing.T) {
	s, _ := subscription.Parse([]byte(sub), subscription.Options{})
	mk := func(ns []string) *compiler.Plan {
		p, err := compiler.Compile(s, nil, compiler.Options{Mode: compiler.ModeTUN, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: "darwin",
			TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"), DNSHijack: true},
			Env: &compiler.Environment{PhysicalDNS: []string{"192.168.3.1"},
				SplitVPNs:        []compiler.EnvVPN{{Iface: "utun8", Routes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}},
				SystemDNSDomains: []string{"corp-net.example", "corp.example"},
				SystemResolvers:  []compiler.EnvResolver{{Domain: "corp-net.example", Nameservers: ns}, {Domain: "corp.example", Nameservers: ns}}}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	byTag := func(p *compiler.Plan) map[string]ir.DNSServer {
		m := map[string]ir.DNSServer{}
		for _, sv := range p.IR.DNS.Servers {
			m[sv.Tag] = sv
		}
		return m
	}
	ruleServer := func(p *compiler.Plan, domain string) string {
		for _, r := range p.IR.DNS.Rules {
			if r.Match.Cond != nil && strings.Contains(strings.Join(r.Match.Cond.DomainSuffix, ","), domain) {
				return r.Server
			}
		}
		return ""
	}

	p := mk([]string{"127.0.0.1", "10.8.3.129"})
	srv := byTag(p)[ruleServer(p, "corp-net.example")]
	if srv.Type != "udp" || srv.Address != "10.8.3.129" || srv.Detour != compiler.VPNOutbound("utun8") {
		t.Fatalf("有其他服务器时跳过回环地址，经 VPN 询问 10.8.3.129：%+v", srv)
	}
	if ruleServer(p, "corp.example") != srv.Tag {
		t.Fatal("服务器相同的域名应共用一个 DNS 服务器")
	}
	for _, sv := range p.IR.DNS.Servers {
		if sv.Tag == compiler.SystemResolverDNS {
			t.Fatal("有服务器信息时不应再交给系统解析器")
		}
	}
	// 由 VPN 网段内服务器解析的域名属于该 VPN：优先于订阅规则经 VPN 出站，并加入系统代理豁免
	vpnRule, subRule := -1, -1
	for i, r := range p.IR.Rules {
		if r.Match.Cond != nil && slices.Contains(r.Match.Cond.DomainSuffix, "corp-net.example") && r.Action.Outbound == compiler.VPNOutbound("utun8") && vpnRule < 0 {
			vpnRule = i
		}
		if r.Source.Kind == ir.SourceSubscription && subRule < 0 {
			subRule = i
		}
	}
	if vpnRule < 0 || (subRule >= 0 && vpnRule > subRule) {
		t.Fatalf("corp-net.example 应经 VPN 出站且优先于订阅：vpn=%d sub=%d", vpnRule, subRule)
	}
	bypass := false
	for _, e := range p.SysProxy.Exceptions {
		bypass = bypass || e.Pattern == "*.corp-net.example"
	}
	if !bypass {
		t.Fatal("VPN 域名应加入系统代理豁免")
	}

	p = mk([]string{"127.0.0.1"})
	srv = byTag(p)[ruleServer(p, "corp-net.example")]
	if srv.Address != "127.0.0.1" || srv.Detour != compiler.VPNOutbound("lo0") {
		t.Fatalf("只有回环地址时经 lo0 询问：%+v", srv)
	}
	found := false
	for _, n := range p.IR.Nodes {
		found = found || (n.Tag == compiler.VPNOutbound("lo0") && n.Dial.BindInterface == "lo0")
	}
	if !found {
		t.Fatal("缺少绑定 lo0 的直连出站")
	}

	p = mk([]string{"10.8.3.129", "10.8.3.130"})
	if srv = byTag(p)[ruleServer(p, "corp-net.example")]; srv.Type != "race" || len(srv.Members) != 2 {
		t.Fatalf("多个服务器应竞速：%+v", srv)
	}
}

// 订阅显式 ipv6: false：TUN 下拒绝连向 IPv6 地址的连接（排在订阅规则之前），应用立即回落到 IPv4。
func TestIPv6DisabledRejectsInTUN(t *testing.T) {
	has := func(text string, mode string) (bool, bool) {
		s, err := subscription.Parse([]byte(text), subscription.Options{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := compiler.Compile(s, nil, compiler.Options{Mode: mode, Listen: netip.MustParseAddrPort("127.0.0.1:7899"), Platform: "darwin",
			TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
		if err != nil {
			t.Fatal(err)
		}
		idx, sub := -1, -1
		for i, r := range p.IR.Rules {
			if r.Action.Kind == ir.ActionReject && r.Match.Cond != nil && slices.Contains(r.Match.Cond.IPCIDR, "::/0") && idx < 0 {
				idx = i
			}
			if r.Source.Kind == ir.SourceSubscription && r.Source.Line > 0 && sub < 0 {
				sub = i
			}
		}
		return idx >= 0, idx >= 0 && (sub < 0 || idx < sub)
	}
	off := "ipv6: false\n" + sub
	if ok, before := has(off, compiler.ModeTUN); !ok || !before {
		t.Fatalf("TUN + ipv6: false 应拒绝 IPv6 且优先于订阅规则：%v %v", ok, before)
	}
	if ok, _ := has(off, compiler.ModeProxy); ok {
		t.Fatal("非 TUN 模式不需要（应用经代理传入域名）")
	}
	if ok, _ := has(sub, compiler.ModeTUN); ok {
		t.Fatal("未写 ipv6 时不应拒绝（mihomo 默认开启 IPv6）")
	}
}
