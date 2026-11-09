// Package velum 把编译好的 Plan 转成 velum-engine 的 IR（JSON）和规则集。
//
// velum-engine 是自研的数据面内核（独立仓库），用来在 iOS 上替换 sing-box。它目前只覆盖 Plan 的一部分：
// 不支持的内容一律产出告警，不悄悄丢弃——跳过一条规则会改变之后规则的命中结果，必须看得见。
// 引擎侧 IR 的定义见 velum-engine 的 crates/velum-ir，规则集格式见其 docs/adr/0002-rule-db-layout.md。
package velum

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// SchemaVersion 与 velum-engine 的 velum_ir::SCHEMA_VERSION 一致。
const SchemaVersion = 1

type Config struct {
	SchemaVersion int     `json:"schema_version"`
	Nodes         []Node  `json:"nodes"`
	Groups        []Group `json:"groups"`
	Rules         []Rule  `json:"rules"`
	Final         Action  `json:"final"`
	DNS           DNS     `json:"dns"`
	IPv6          bool    `json:"ipv6"`
}

type Node struct {
	Tag       string         `json:"tag"`
	Protocol  string         `json:"protocol"`
	Server    string         `json:"server"`
	Port      int            `json:"port"`
	TLS       *TLS           `json:"tls,omitempty"`
	Transport *Transport     `json:"transport,omitempty"`
	Options   map[string]any `json:"options"`
}

type TLS struct {
	ServerName  string   `json:"server_name"`
	ALPN        []string `json:"alpn"`
	Insecure    bool     `json:"insecure"`
	Fingerprint string   `json:"fingerprint"`
	Reality     *Reality `json:"reality,omitempty"`
}

type Reality struct {
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type Transport struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Host string `json:"host"`
}

type Group struct {
	Tag          string        `json:"tag"`
	Kind         string        `json:"kind"`
	Members      []string      `json:"members"`
	Selected     string        `json:"selected,omitempty"`
	URL          string        `json:"url,omitempty"`
	Interval     int           `json:"interval,omitempty"`
	Tolerance    int           `json:"tolerance,omitempty"`
	OriginalType string        `json:"original_type,omitempty"`
	Policy       *ir.NetPolicy `json:"policy,omitempty"`
}

type Rule struct {
	Cond   Cond   `json:"cond"`
	Action Action `json:"action"`
}

type Cond struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	IPPrivate     bool     `json:"ip_private,omitempty"`
	RuleSet       []string `json:"rule_set,omitempty"`
	Port          []int    `json:"port,omitempty"`
	Network       []string `json:"network,omitempty"`
	// NoResolve：IP 条件不触发解析（Clash no-resolve）。引擎在解析点之前不用解析出的地址匹配这类规则。
	NoResolve bool `json:"no_resolve,omitempty"`
}

// Action 的 JSON 形如 {"kind": "direct"} / {"kind": "route", "target": "Proxy"}。
type Action struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

// DNS 对应 velum-engine 的 velum_ir::Dns（语义见那里的注释，与这里渲染给 sing-box 的一致）。
type DNS struct {
	Servers  []DNSServer `json:"servers"`
	Rules    []DNSRule   `json:"rules,omitempty"`
	Final    string      `json:"final,omitempty"`
	Strategy string      `json:"strategy,omitempty"`
	FakeIP   *FakeIP     `json:"fake_ip,omitempty"`
}

type DNSServer struct {
	Tag      string   `json:"tag"`
	Type     string   `json:"type"`
	Address  string   `json:"address,omitempty"`
	Members  []string `json:"members,omitempty"`
	Fallback []string `json:"fallback,omitempty"`
	Resolver string   `json:"resolver,omitempty"`
}

type DNSRule struct {
	Cond        Cond     `json:"cond"`
	QueryType   []string `json:"query_type,omitempty"`
	Server      string   `json:"server,omitempty"`
	Answer      []string `json:"answer,omitempty"`
	PreferredBy string   `json:"preferred_by,omitempty"`
}

type FakeIP struct {
	Inet4Range string `json:"inet4_range"`
	Inet6Range string `json:"inet6_range,omitempty"`
	KeepSVCB   bool   `json:"keep_svcb,omitempty"`
}

// RuleSet 是交给引擎编译成 .vrdb 的规则集内容（velum-tools vrdb-json 的输入格式）。
type RuleSet struct {
	Name          string   `json:"name"`
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	DomainRegex   []string `json:"domain_regex,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
}

// Warning 是一项没能转换的内容。Kind 用于归类统计，Subject 指向具体对象，Source 指回订阅 / 覆写原文。
type Warning struct {
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	Reason  string    `json:"reason"`
	Source  ir.Source `json:"source"`
}

type Result struct {
	Config   Config    `json:"config"`
	RuleSets []RuleSet `json:"rule_sets"`
	Warnings []Warning `json:"warnings"`
	// RuleIndex 是引擎规则 → Plan 规则的下标（一致性测试用：两边命中同一条规则才算一致）。
	RuleIndex []int `json:"rule_index"`
	// Loaded 是由 GeoSource 取得的远程规则集内容（tag → 条件），可交给 explain.WhyWith 求值。
	Loaded map[string][]ir.Cond `json:"-"`
}

// Options 控制转换。
type Options struct {
	// Geo 为 geoip-* / geosite-* 远程规则集提供数据；为 nil 时这些规则集不可用（产出告警）。
	Geo *GeoSource
}

// Render 转换 Plan。不会因为不支持的内容失败，只产出告警。
func Render(p *compiler.Plan) *Result { return RenderWith(p, Options{}) }

// RenderWith 同 Render，可提供远程 geo 规则集的数据源。
func RenderWith(p *compiler.Plan, opt Options) *Result {
	c := p.IR
	r := &Result{Config: Config{SchemaVersion: SchemaVersion, IPv6: c.IPv6}, Loaded: map[string][]ir.Cond{}}
	warn := func(kind, subject, reason string, src ir.Source) {
		r.Warnings = append(r.Warnings, Warning{Kind: kind, Subject: subject, Reason: reason, Source: src})
	}

	nodes := map[string]bool{}
	for _, n := range c.Nodes {
		nodes[n.Tag] = true
		r.Config.Nodes = append(r.Config.Nodes, node(n, warn))
	}
	for _, g := range c.Groups {
		r.Config.Groups = append(r.Config.Groups, Group{
			Tag: g.Tag, Kind: g.Type, Members: g.Members, Selected: g.Default, URL: g.URL,
			Interval: int(g.Interval.Seconds()), Tolerance: g.Tolerance, OriginalType: g.OriginalType, Policy: g.Policy,
		})
	}

	// 规则集：内联的转成引擎格式；文件 / 远程（.srs 等）暂不支持，引用它们的条件要去掉
	usable := map[string]bool{}
	for _, rs := range c.RuleSets {
		if rs.Kind != ir.RuleSetInline {
			var conds []ir.Cond
			var err error
			if opt.Geo != nil {
				conds, err = opt.Geo.Load(rs.Tag)
			}
			switch {
			case err != nil:
				warn("rule_set", rs.Tag, "取 geo 数据失败："+err.Error(), rs.Source)
				continue
			case conds == nil:
				warn("rule_set", rs.Tag, fmt.Sprintf("%s 规则集（%s）暂不支持：引擎不读 sing-box 的 .srs，需要换成可再分发的数据源", rs.Kind, rs.Format), rs.Source)
				continue
			}
			r.Loaded[rs.Tag] = conds
			rs.Rules = conds
		}
		out := RuleSet{Name: rs.Tag}
		dropped := 0
		for _, cd := range rs.Rules {
			out.Domain = append(out.Domain, cd.Domain...)
			out.DomainSuffix = append(out.DomainSuffix, cd.DomainSuffix...)
			out.DomainKeyword = append(out.DomainKeyword, cd.DomainKeyword...)
			out.DomainRegex = append(out.DomainRegex, cd.DomainRegex...)
			out.IPCIDR = append(out.IPCIDR, cd.IPCIDR...)
			if len(cd.ProcessName)+len(cd.ProcessPath)+len(cd.Port)+len(cd.Network)+len(cd.SourceIPCIDR)+len(cd.Protocol) > 0 || cd.IPPrivate {
				dropped++
			}
		}
		if dropped > 0 {
			warn("rule_set", rs.Tag, fmt.Sprintf("%d 条条目带有规则集里不支持的条件（进程 / 端口 / 网络 / 源地址 / 协议 / 私有地址），已忽略这些条件", dropped), rs.Source)
		}
		usable[rs.Tag] = true
		r.RuleSets = append(r.RuleSets, out)
	}

	// 解析点：sing-box 只在显式的 resolve 动作处解析域名，之前的 IP 规则对域名连接看不到 IP。
	// 引擎的解析点是「第一条不带 no-resolve 的 IP 规则」，所以把第一个 resolve 之前的 IP 规则都标成 no-resolve，
	// 两边的解析点就对齐了；Plan 里没有 resolve 时，所有 IP 规则都不对域名连接生效。
	resolvedFrom := len(c.Rules)
	for i, rule := range c.Rules {
		if rule.Action.Kind == ir.ActionResolve {
			resolvedFrom = i
			if rule.Match.Cond != nil || rule.Match.Logical != "" {
				warn("rule", fmt.Sprintf("#%d", i+1), "带条件的 resolve 按无条件处理（解析点取这条规则的位置）", rule.Source)
			}
			break
		}
	}
	for i, rule := range c.Rules {
		a, ok := action(rule.Action, rule.Source, warn)
		if !ok {
			continue
		}
		if rule.Match.Cond == nil {
			if rule.Match.Logical != "" {
				warn("rule", fmt.Sprintf("#%d", i+1), fmt.Sprintf("逻辑规则（%s%s）暂不支持，已跳过", invertMark(rule.Match.Invert), rule.Match.Logical), rule.Source)
			} else {
				warn("rule", fmt.Sprintf("#%d", i+1), "无条件规则（sniff / resolve 等动作之外）暂不支持，已跳过", rule.Source)
			}
			continue
		}
		cd, ok := cond(*rule.Match.Cond, usable, fmt.Sprintf("#%d", i+1), rule.Source, warn)
		if !ok {
			continue
		}
		if i < resolvedFrom {
			cd.NoResolve = true
		}
		r.Config.Rules = append(r.Config.Rules, Rule{Cond: cd, Action: a})
		r.RuleIndex = append(r.RuleIndex, i)
	}
	if a, ok := action(c.Final, ir.Source{Kind: ir.SourceVelum, Text: "final"}, warn); ok {
		r.Config.Final = a
	} else {
		r.Config.Final = Action{Kind: "direct"}
	}

	r.Config.DNS = dns(c.DNS, usable, warn)
	return r
}

func invertMark(inv bool) string {
	if inv {
		return "not "
	}
	return ""
}

type warnFn func(kind, subject, reason string, src ir.Source)

func action(a ir.Action, src ir.Source, warn warnFn) (Action, bool) {
	switch a.Kind {
	case ir.ActionRoute:
		switch a.Outbound {
		case ir.OutboundDirect, "":
			return Action{Kind: "direct"}, true
		case ir.OutboundReject:
			return Action{Kind: "reject"}, true
		}
		if a.OverrideAddress != "" {
			warn("rule", a.Outbound, "改写目标地址（hosts 覆盖）暂不支持", src)
		}
		return Action{Kind: "route", Target: a.Outbound}, true
	case ir.ActionReject:
		return Action{Kind: "reject"}, true
	case ir.ActionDrop:
		// 引擎没有静默丢弃，近似为拒绝（应用会更快得到失败）
		warn("rule", "drop", "drop 按 reject 处理", src)
		return Action{Kind: "reject"}, true
	case ir.ActionResolve:
		// 引擎遇到可能命中的 IP 规则时自己解析，不需要这条
		return Action{}, false
	case ir.ActionSniff:
		warn("rule", "sniff", "协议嗅探暂不支持：直接用 IP 连接（非 fake-ip）时匹配不到域名规则", src)
		return Action{}, false
	case ir.ActionHijackDNS:
		warn("rule", "hijack-dns", "接管发往其他 DNS 服务器的查询暂不支持（隧道 DNS 本身由引擎应答）", src)
		return Action{}, false
	}
	warn("rule", a.Kind, "未知动作，已跳过", src)
	return Action{}, false
}

func cond(c ir.Cond, usable map[string]bool, subject string, src ir.Source, warn warnFn) (Cond, bool) {
	out := Cond{
		Domain: c.Domain, DomainSuffix: c.DomainSuffix, DomainKeyword: c.DomainKeyword, IPCIDR: c.IPCIDR,
		IPPrivate: c.IPPrivate, Network: c.Network, NoResolve: c.NoResolve,
	}
	for _, p := range c.Port {
		out.Port = append(out.Port, p)
	}
	var missing []string
	for _, n := range c.RuleSet {
		if usable[n] {
			out.RuleSet = append(out.RuleSet, n)
		} else {
			missing = append(missing, n)
		}
	}
	var unsupported []string
	if len(c.DomainRegex) > 0 {
		unsupported = append(unsupported, "domain_regex")
	}
	if len(c.ProcessName)+len(c.ProcessPath) > 0 {
		unsupported = append(unsupported, "process")
	}
	if len(c.SourceIPCIDR) > 0 {
		unsupported = append(unsupported, "source_ip_cidr")
	}
	if len(c.Protocol) > 0 {
		unsupported = append(unsupported, "protocol")
	}
	addr := len(out.Domain)+len(out.DomainSuffix)+len(out.DomainKeyword)+len(out.IPCIDR)+len(out.RuleSet) > 0 || out.IPPrivate
	hadAddr := addr || len(c.DomainRegex) > 0 || len(missing) > 0
	if len(missing) > 0 {
		sort.Strings(missing)
		warn("rule", subject, "引用的规则集不可用："+strings.Join(missing, ", "), src)
	}
	switch {
	case len(c.ProcessName)+len(c.ProcessPath)+len(c.SourceIPCIDR)+len(c.Protocol) > 0:
		// 与条件（进程 / 源地址 / 协议）无法求值：整条跳过，否则会扩大命中范围
		warn("rule", subject, "含暂不支持的条件（"+strings.Join(unsupported, ", ")+"），已跳过", src)
		return Cond{}, false
	case hadAddr && !addr:
		// 地址类条件全部不可用：剩下的端口 / 网络条件会扩大命中范围，整条跳过
		warn("rule", subject, "地址类条件都不可用（"+strings.Join(append(unsupported, missing...), ", ")+"），已跳过", src)
		return Cond{}, false
	case len(c.DomainRegex) > 0:
		warn("rule", subject, "顶层 domain_regex 暂不支持，已忽略（其余地址条件仍生效）", src)
	}
	return out, true
}

func node(n ir.Node, warn warnFn) Node {
	src := n.Source
	out := Node{Tag: n.Tag, Protocol: n.Protocol, Server: n.Server, Port: n.Port, Options: n.Options}
	if out.Options == nil {
		out.Options = map[string]any{}
	}
	if t := n.TLS; t != nil {
		out.TLS = &TLS{ServerName: t.ServerName, ALPN: t.ALPN, Insecure: t.Insecure, Fingerprint: t.Fingerprint}
		if t.Reality != nil {
			out.TLS.Reality = &Reality{PublicKey: t.Reality.PublicKey, ShortID: t.Reality.ShortID}
		}
	}
	if t := n.Transport; t != nil {
		host := ""
		if len(t.Host) > 0 {
			host = t.Host[0]
		}
		out.Transport = &Transport{Kind: t.Type, Path: t.Path, Host: host}
	}
	if len(n.Ports) > 0 {
		warn("node", n.Tag, "端口跳跃暂不支持，只用主端口", src)
	}
	if n.Detour != "" {
		warn("node", n.Tag, "链式代理（dialer-proxy）暂不支持", src)
	}
	switch n.Protocol {
	case "shadowsocks", "anytls", "wireguard":
	default:
		warn("node", n.Tag, "协议 "+n.Protocol+" 暂不支持（第二阶段）", src)
	}
	return out
}

// dns 转换 DNS 服务器与规则。引擎支持 udp / https / local（physical 同）/ race / hosts；
// 其他类型去掉并告警，引用它们的竞速成员、规则一并清理。
func dns(d ir.DNS, usable map[string]bool, warn warnFn) DNS {
	out := DNS{Final: d.Final, Strategy: d.Strategy}
	ok := map[string]bool{}
	for _, s := range d.Servers {
		switch s.Type {
		case "udp", "https", "local", "physical", "hosts":
			ok[s.Tag] = true
		case "race":
		case "fakeip":
			continue
		default:
			warn("dns", s.Tag, fmt.Sprintf("DNS 服务器类型 %s 暂不支持", s.Type), ir.Source{})
		}
	}
	keep := func(tags []string) []string {
		var r []string
		for _, t := range tags {
			if ok[t] {
				r = append(r, t)
			}
		}
		return r
	}
	// race 可能引用其他 race：按声明顺序处理，成员只认已可用的
	for _, s := range d.Servers {
		if s.Type == "race" {
			m, f := keep(s.Members), keep(s.Fallback)
			if len(m)+len(f) == 0 {
				warn("dns", s.Tag, "竞速组的成员都不可用", ir.Source{})
				continue
			}
			ok[s.Tag] = true
		}
	}
	for _, s := range d.Servers {
		if !ok[s.Tag] {
			continue
		}
		typ := s.Type
		if typ == "physical" {
			typ = "local" // 物理网络 DNS：引擎里就是平台提供的系统 DNS
		}
		if s.Detour != "" {
			warn("dns", s.Tag, "经出站 "+s.Detour+" 查询（respect-rules）暂不支持，改为直连查询", ir.Source{})
		}
		out.Servers = append(out.Servers, DNSServer{Tag: s.Tag, Type: typ, Address: s.Address,
			Members: keep(s.Members), Fallback: keep(s.Fallback), Resolver: s.Resolver})
	}
	if out.Final != "" && !ok[out.Final] {
		warn("dns", out.Final, "final 服务器不可用，引擎改用第一个服务器", ir.Source{})
		out.Final = ""
	}
	for i, r := range d.Rules {
		subject := fmt.Sprintf("dns#%d", i+1)
		if len(r.Answer) == 0 && !ok[r.Server] {
			warn("dns", subject, "服务器 "+r.Server+" 不可用，已跳过", ir.Source{})
			continue
		}
		var cd Cond
		switch {
		case r.Match.Cond != nil:
			var good bool
			cd, good = cond(*r.Match.Cond, usable, subject, ir.Source{}, warn)
			if !good {
				continue
			}
		case r.Match.Logical != "":
			warn("dns", subject, "逻辑条件的 DNS 规则暂不支持，已跳过", ir.Source{})
			continue
		}
		out.Rules = append(out.Rules, DNSRule{Cond: cd, QueryType: r.QueryType, Server: r.Server, Answer: r.Answer, PreferredBy: r.PreferredBy})
	}
	if f := d.FakeIP; f != nil {
		out.FakeIP = &FakeIP{Inet4Range: f.Inet4Range, Inet6Range: f.Inet6Range, KeepSVCB: f.KeepSVCB}
	}
	return out
}
