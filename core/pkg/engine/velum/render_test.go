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
		DNS:   ir.DNS{Servers: []ir.DNSServer{{Tag: "u", Type: "udp", Address: "223.5.5.5"}, {Tag: "d", Type: "https", Address: "1.1.1.1"}}, FakeIP: &ir.FakeIP{Inet4Range: "198.18.0.0/15"}},
	}
	r := Render(&compiler.Plan{IR: c})
	if got := r.RuleIndex; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 5 {
		t.Fatalf("应当保留第 1、2、6 条，实际 %v", got)
	}
	if a := r.Config.Rules[2].Action; a.Kind != "reject" {
		t.Errorf("drop 应当按 reject：%+v", a)
	}
	if r.Config.Final.Target != "Proxy" || r.Config.Groups[0].Policy == nil || r.Config.Groups[0].Selected != "hk" {
		t.Errorf("final / 策略组转换不对：%+v %+v", r.Config.Final, r.Config.Groups[0])
	}
	if len(r.RuleSets) != 1 || r.RuleSets[0].DomainRegex[0] != `^x\d+` {
		t.Errorf("内联规则集应当连正则一起转换：%+v", r.RuleSets)
	}
	if len(r.Config.DNS.Servers) != 1 || r.Config.DNS.Servers[0] != "udp://223.5.5.5" {
		t.Errorf("DNS：%+v", r.Config.DNS)
	}
	all := ""
	for _, w := range r.Warnings {
		all += w.Kind + ":" + w.Subject + ":" + w.Reason + "\n"
	}
	for _, want := range []string{"逻辑规则", "geoip-cn", "process", "drop 按 reject", "https", "vmess"} {
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
