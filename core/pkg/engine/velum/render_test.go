package velum

import (
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

func TestRenderConvertsAndWarns(t *testing.T) {
	c := &ir.Config{
		Nodes: []ir.Node{{Tag: "hk", Protocol: "anytls", Server: "a.example", Port: 443, Options: map[string]any{"password": "x"},
			TLS: &ir.TLS{ServerName: "a.example"}}, {Tag: "vm", Protocol: "vmess", Server: "b", Port: 1}},
		Groups: []ir.Group{{Tag: "Proxy", Type: "select", Members: []string{"hk", "vm"}, Default: "hk",
			Policy: &ir.NetPolicy{SSID: map[string]string{"家": "DIRECT"}, Default: "hk"}}},
		RuleSets: []ir.RuleSet{
			{Tag: "ai", Kind: ir.RuleSetInline, Rules: []ir.Cond{{DomainSuffix: []string{"openai.com"}, DomainRegex: []string{`^x\d+`}}}},
			{Tag: "geoip-cn", Kind: ir.RuleSetRemote, Format: "binary"},
		},
		Rules: []ir.Rule{
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"cn"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}},
			{Match: ir.Leaf(ir.Cond{RuleSet: []string{"ai"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: "Proxy"}},
			{Match: ir.Match{Logical: "and", Children: []ir.Match{ir.Leaf(ir.Cond{Port: []int{443}})}}, Action: ir.Action{Kind: ir.ActionReject}},
			{Match: ir.Leaf(ir.Cond{RuleSet: []string{"geoip-cn"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}},
			{Match: ir.Leaf(ir.Cond{ProcessName: []string{"x"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: "Proxy"}},
			{Match: ir.Leaf(ir.Cond{IPCIDR: []string{"10.0.0.0/8"}}), Action: ir.Action{Kind: ir.ActionDrop}},
		},
		Final: ir.Action{Kind: ir.ActionRoute, Outbound: "Proxy"},
		DNS:   ir.DNS{Servers: []ir.DNSServer{{Tag: "u", Type: "udp", Address: "223.5.5.5"}, {Tag: "q", Type: "quic", Address: "1.1.1.1"}}, FakeIP: &ir.FakeIP{Inet4Range: "198.18.0.0/15"}},
	}
	r := Render(&compiler.Plan{IR: c})
	if got := r.RuleIndex; len(got) != 4 || got[0] != 0 || got[1] != 1 || got[2] != 2 || got[3] != 5 {
		t.Fatalf("应当保留第 1、2、3（逻辑规则）、6 条，实际 %v", got)
	}
	if m := r.Config.Rules[2].Match; m == nil || m.Logical != "and" || len(m.Children) != 1 || m.Children[0].Cond.Port[0] != 443 {
		t.Errorf("逻辑规则应当转换：%+v", m)
	}
	if a := r.Config.Rules[3].Action; a.Kind != "reject" {
		t.Errorf("drop 应当按 reject：%+v", a)
	}
	if r.Config.Final.Target != "Proxy" || r.Config.Groups[0].Policy == nil || r.Config.Groups[0].Selected != "hk" {
		t.Errorf("final / 策略组转换不对：%+v %+v", r.Config.Final, r.Config.Groups[0])
	}
	if len(r.RuleSets) != 1 || r.RuleSets[0].DomainRegex[0] != `^x\d+` {
		t.Errorf("内联规则集应当连正则一起转换：%+v", r.RuleSets)
	}
	if len(r.Config.DNS.Servers) != 1 || r.Config.DNS.Servers[0].Tag != "u" || r.Config.DNS.Servers[0].Type != "udp" {
		t.Errorf("DNS：%+v", r.Config.DNS)
	}
	all := ""
	for _, w := range r.Warnings {
		all += w.Kind + ":" + w.Subject + ":" + w.Reason + "\n"
	}
	for _, want := range []string{"geoip-cn", "process", "drop 按 reject", "vmess"} {
		if !strings.Contains(all, want) {
			t.Errorf("缺少告警 %q；全部告警：\n%s", want, all)
		}
	}
}

// 解析点对齐：第一个 resolve 动作之前的 IP 规则标成 no-resolve（sing-box 只在 resolve 处解析）。
func TestRenderMarksIPRulesBeforeResolve(t *testing.T) {
	ipRule := func(cidr string) ir.Rule {
		return ir.Rule{Match: ir.Leaf(ir.Cond{IPCIDR: []string{cidr}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}}
	}
	c := &ir.Config{
		Rules: []ir.Rule{
			ipRule("10.0.0.0/8"),
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"a.com"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}},
			{Action: ir.Action{Kind: ir.ActionResolve}},
			ipRule("1.0.0.0/8"),
		},
		Final: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect},
	}
	r := Render(&compiler.Plan{IR: c})
	if len(r.Config.Rules) != 3 || !r.Config.Rules[0].Cond.NoResolve || r.Config.Rules[2].Cond.NoResolve {
		t.Fatalf("resolve 之前的 IP 规则应标 no-resolve、之后的不标：%+v", r.Config.Rules)
	}
	// 没有 resolve：所有 IP 规则都标
	c.Rules = []ir.Rule{ipRule("1.0.0.0/8")}
	if r := Render(&compiler.Plan{IR: c}); !r.Config.Rules[0].Cond.NoResolve {
		t.Fatal("没有 resolve 时 IP 规则对域名连接不生效，应标 no-resolve")
	}
}

func TestRenderDNS(t *testing.T) {
	d := ir.DNS{
		Servers: []ir.DNSServer{
			{Tag: "doh", Type: "https", Address: "doh.pub", Resolver: "boot"},
			{Tag: "boot", Type: "udp", Address: "223.5.5.5"},
			{Tag: "sys", Type: "physical"},
			{Tag: "q", Type: "quic", Address: "x"},
			{Tag: "r", Type: "race", Members: []string{"doh", "q"}, Fallback: []string{"sys"}},
			{Tag: "dead", Type: "race", Members: []string{"q"}},
		},
		Rules: []ir.DNSRule{
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"corp.example"}}), Server: "sys"},
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"x.example"}}), Server: "dead"},
			{Match: ir.Leaf(ir.Cond{Domain: []string{"pin.example"}}), Answer: []string{"10.0.0.1"}},
			{Server: "sys", PreferredBy: "sys", QueryType: []string{"A"}},
		},
		Final: "r", Strategy: "ipv4_only", FakeIP: &ir.FakeIP{Inet4Range: "198.18.0.0/16"},
	}
	out := dns(d, nil, nil, func(string, string, string, ir.Source) {})
	tags := []string{}
	for _, s := range out.Servers {
		tags = append(tags, s.Tag+":"+s.Type)
	}
	if strings.Join(tags, ",") != "doh:https,boot:udp,sys:local,r:race" {
		t.Errorf("服务器：%v", tags)
	}
	if r := out.Servers[3]; len(r.Members) != 1 || r.Members[0] != "doh" || r.Fallback[0] != "sys" {
		t.Errorf("race 成员应当去掉不可用的：%+v", r)
	}
	if len(out.Rules) != 3 || out.Rules[1].Answer[0] != "10.0.0.1" || out.Rules[2].PreferredBy != "sys" {
		t.Errorf("规则：%+v", out.Rules)
	}
	if out.Final != "r" || out.Strategy != "ipv4_only" || out.FakeIP.Inet4Range != "198.18.0.0/16" {
		t.Errorf("final / strategy / fakeip：%+v", out)
	}
}

// sniff：第一条无条件 sniff 打开引擎的嗅探，不产生告警，也不占规则下标。
func TestRenderSniff(t *testing.T) {
	c := &ir.Config{
		Rules: []ir.Rule{
			{Action: ir.Action{Kind: ir.ActionSniff}},
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"cn"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}},
		},
		Final: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect},
	}
	r := Render(&compiler.Plan{IR: c})
	if !r.Config.Sniff || r.Config.SniffAfter != 0 || len(r.Warnings) != 0 || len(r.RuleIndex) != 1 || r.RuleIndex[0] != 1 {
		t.Fatalf("sniff=%v 告警 %+v 下标 %v", r.Config.Sniff, r.Warnings, r.RuleIndex)
	}
	c.Rules[0], c.Rules[1] = c.Rules[1], c.Rules[0]
	r = Render(&compiler.Plan{IR: c})
	if !r.Config.Sniff || r.Config.SniffAfter != 1 || len(r.Warnings) != 0 {
		t.Fatalf("sniff 之前的规则数应为 1：sniff=%v after=%d 告警 %+v", r.Config.Sniff, r.Config.SniffAfter, r.Warnings)
	}
	if r = Render(&compiler.Plan{IR: &ir.Config{Final: c.Final}}); r.Config.Sniff {
		t.Fatal("没有 sniff 规则时不应打开")
	}
}

// hijack-dns：protocol: dns 的接管规则打开引擎的接管，记下之前的规则数；带其他条件的告警。
func TestRenderHijackDNS(t *testing.T) {
	direct := ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	c := &ir.Config{
		Rules: []ir.Rule{
			{Match: ir.Leaf(ir.Cond{IPCIDR: []string{"::/0"}, Network: []string{"tcp"}}), Action: ir.Action{Kind: ir.ActionReject}},
			{Action: ir.Action{Kind: ir.ActionSniff}},
			{Match: ir.Leaf(ir.Cond{Protocol: []string{"dns"}}), Action: ir.Action{Kind: ir.ActionHijackDNS}},
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"cn"}}), Action: direct},
		},
		Final: direct,
	}
	r := Render(&compiler.Plan{IR: c})
	if !r.Config.HijackDNS || r.Config.HijackDNSAfter != 1 || r.Config.SniffAfter != 1 || len(r.Warnings) != 0 {
		t.Fatalf("hijack=%v after=%d sniffAfter=%d 告警 %+v", r.Config.HijackDNS, r.Config.HijackDNSAfter, r.Config.SniffAfter, r.Warnings)
	}
	c.Rules[2].Match = ir.Leaf(ir.Cond{Protocol: []string{"dns"}, Port: []int{53}})
	r = Render(&compiler.Plan{IR: c})
	if r.Config.HijackDNS || len(r.Warnings) != 1 {
		t.Fatalf("带其他条件的应当告警且不打开：hijack=%v 告警 %+v", r.Config.HijackDNS, r.Warnings)
	}
}

// DNS 服务器的 detour：出站存在时传给引擎，不存在时告警并改为直连。
func TestRenderDNSDetour(t *testing.T) {
	d := ir.DNS{Servers: []ir.DNSServer{
		{Tag: "via", Type: "udp", Address: "8.8.8.8", Detour: "Proxy"},
		{Tag: "gone", Type: "udp", Address: "1.1.1.1", Detour: "Missing"},
	}}
	var warns []string
	out := dns(d, nil, map[string]bool{"Proxy": true}, func(kind, subject, reason string, _ ir.Source) {
		warns = append(warns, subject+":"+reason)
	})
	if out.Servers[0].Detour != "Proxy" || out.Servers[1].Detour != "" {
		t.Fatalf("detour：%+v", out.Servers)
	}
	if len(warns) != 1 || !strings.HasPrefix(warns[0], "gone:") {
		t.Fatalf("告警：%v", warns)
	}
}

// 逻辑规则：转换成 match 树；解析点之前的 IP 叶子标 no-resolve；有不支持的叶子时整条跳过。
func TestRenderLogicalRules(t *testing.T) {
	direct := ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	c := &ir.Config{
		Rules: []ir.Rule{
			{Match: ir.Match{Logical: "and", Children: []ir.Match{
				ir.Leaf(ir.Cond{IPCIDR: []string{"::/0"}, Network: []string{"tcp"}}),
				{Logical: "or", Invert: true, Children: []ir.Match{ir.Leaf(ir.Cond{IPCIDR: []string{"fd00::/8"}})}},
			}}, Action: ir.Action{Kind: ir.ActionReject}},
			{Match: ir.Match{Logical: "or", Children: []ir.Match{
				ir.Leaf(ir.Cond{DomainSuffix: []string{"a.com"}}),
				ir.Leaf(ir.Cond{ProcessName: []string{"x"}}),
			}}, Action: direct},
			{Action: ir.Action{Kind: ir.ActionResolve}},
			{Match: ir.Leaf(ir.Cond{IPCIDR: []string{"10.0.0.0/8"}}), Action: direct},
		},
		DNS: ir.DNS{Servers: []ir.DNSServer{{Tag: "u", Type: "udp", Address: "1.1.1.1"}},
			Rules: []ir.DNSRule{{Match: ir.Match{Logical: "and", Children: []ir.Match{ir.Leaf(ir.Cond{DomainSuffix: []string{"corp.example"}})}}, Server: "u"}}},
		Final: direct,
	}
	r := Render(&compiler.Plan{IR: c})
	if len(r.RuleIndex) != 2 || r.RuleIndex[0] != 0 || r.RuleIndex[1] != 3 {
		t.Fatalf("规则下标：%v（含进程条件的逻辑规则应当跳过）", r.RuleIndex)
	}
	m := r.Config.Rules[0].Match
	if m == nil || !m.Children[0].Cond.NoResolve || !m.Children[1].Invert || !m.Children[1].Children[0].Cond.NoResolve {
		t.Fatalf("解析点之前的 IP 叶子应当 no-resolve：%+v", m)
	}
	if r.Config.Rules[1].Cond.NoResolve {
		t.Fatal("解析点之后的规则不应 no-resolve")
	}
	if d := r.Config.DNS.Rules; len(d) != 1 || d[0].Match == nil || d[0].Match.Children[0].Cond.DomainSuffix[0] != "corp.example" {
		t.Fatalf("DNS 逻辑规则：%+v", d)
	}
}

// hosts 覆盖：直连 + 改写地址 → redirect。
func TestRenderHostsOverride(t *testing.T) {
	c := &ir.Config{Rules: []ir.Rule{{Match: ir.Leaf(ir.Cond{Domain: []string{"dev.test"}}),
		Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect, OverrideAddress: "10.0.0.5"}}},
		Final: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}}
	r := Render(&compiler.Plan{IR: c})
	if a := r.Config.Rules[0].Action; a.Kind != "redirect" || a.Target != "10.0.0.5" || len(r.Warnings) != 0 {
		t.Fatalf("动作：%+v，告警：%+v", a, r.Warnings)
	}
}
