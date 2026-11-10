package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/source"
)

// renderVelum 直接由 Plan 渲染：规则来源取自 Plan 的规则（不经 sing-box 配置），final 有来源，告警转成 ir.Warning。
func TestRenderVelumFromPlan(t *testing.T) {
	src := ir.Source{Kind: ir.SourceSubscription, Text: "DOMAIN-SUFFIX,cn,DIRECT"}
	p := &compiler.Plan{IR: &ir.Config{
		Rules: []ir.Rule{
			{Action: ir.Action{Kind: ir.ActionSniff}},
			{Match: ir.Leaf(ir.Cond{DomainSuffix: []string{"cn"}}), Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}, Source: src},
			{Match: ir.Leaf(ir.Cond{ProcessName: []string{"x"}}), Action: ir.Action{Kind: ir.ActionReject}},
		},
		Final: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect},
	}}
	v, warns, err := renderVelum(p, &source.Fetcher{})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		RuleIndex   []int       `json:"rule_index"`
		RuleSources []ir.Source `json:"rule_sources"`
		FinalSource ir.Source   `json:"final_source"`
	}
	if err := json.Unmarshal(v.Meta, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.RuleIndex) != 1 || m.RuleIndex[0] != 1 || m.RuleSources[0] != src || m.FinalSource.Text != "route.final" {
		t.Fatalf("meta：%+v", m)
	}
	var cfg map[string]any
	if err := json.Unmarshal(v.IR, &cfg); err != nil || cfg["sniff"] != true {
		t.Fatalf("IR：%s %v", v.IR, err)
	}
	if len(warns) == 0 || warns[0].Category != "velum-engine" {
		t.Fatalf("进程规则应当产出告警：%+v", warns)
	}
}
