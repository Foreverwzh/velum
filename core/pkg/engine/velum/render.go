// Package velum 把编译好的 Plan 转成 velum-engine 的 IR（JSON）和规则集。
//
// velum-engine 是自研的数据面内核（独立仓库），用来在 iOS 上替换 sing-box。它目前只覆盖 Plan 的一部分：
// 不支持的内容一律产出告警，不悄悄丢弃——跳过一条规则会改变之后规则的命中结果，必须看得见。
// 引擎侧 IR 的定义见 velum-engine 的 crates/velum-ir，规则集格式见其 docs/adr/0002-rule-db-layout.md。
package velum

import (
	"fmt"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"reflect"
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
	// Sniff：直接用 IP 发起的 TCP 连接，从首包（TLS SNI、HTTP Host）取域名匹配规则，不改目标地址。
	Sniff bool `json:"sniff"`
	// SniffAfter：嗅探之前的规则数。这些规则只按 IP 判断，命中就不嗅探（如 TCP 握手前就拒绝 IPv6）。
	SniffAfter int `json:"sniff_after"`
	// HijackDNS：发往任意地址的 DNS 查询（UDP 按内容识别，TCP 53 端口）由引擎应答，走 DNS 规则与 fake-ip。
	HijackDNS bool `json:"hijack_dns"`
	// HijackDNSAfter：接管 DNS 之前的规则数，这些规则只按 IP 判断，命中就不接管。
	HijackDNSAfter int `json:"hijack_dns_after"`
	// Listen 是本地代理入口（SOCKS5 / HTTP），只在没有 TUN 的本地模式下使用（App 进程里经代理拉取）。
	Listen string `json:"listen,omitempty"`
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
	Cond Cond `json:"cond"`
	// Match 是逻辑规则（and / or / 取反）；有它时引擎不看 Cond。
	Match  *Match `json:"match,omitempty"`
	Action Action `json:"action"`
}

// Match 是逻辑条件（同 ir.Match）：叶子（Cond），或 Logical 为 and / or 的组合。
type Match struct {
	Cond     *Cond   `json:"cond,omitempty"`
	Logical  string  `json:"logical,omitempty"`
	Invert   bool    `json:"invert,omitempty"`
	Children []Match `json:"children,omitempty"`
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
	Detour   string   `json:"detour,omitempty"`
}

type DNSRule struct {
	Cond        Cond     `json:"cond"`
	Match       *Match   `json:"match,omitempty"`
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
	if p.Options.Listen.IsValid() {
		r.Config.Listen = p.Options.Listen.String()
	}
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
				warn("rule_set", rs.Tag, i18n.T("取 geo 数据失败：%v", err), rs.Source)
				continue
			case conds == nil:
				warn("rule_set", rs.Tag, i18n.T("%s 规则集（%s）暂不支持：引擎不读 sing-box 的 .srs，需要换成可再分发的数据源", rs.Kind, rs.Format), rs.Source)
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
			warn("rule_set", rs.Tag, i18n.T("%d 条条目带有规则集里不支持的条件（进程 / 端口 / 网络 / 源地址 / 协议 / 私有地址），已忽略这些条件", dropped), rs.Source)
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
				warn("rule", fmt.Sprintf("#%d", i+1), i18n.T("带条件的 resolve 按无条件处理（解析点取这条规则的位置）"), rule.Source)
			}
			break
		}
	}
	for i, rule := range c.Rules {
		if rule.Action.Kind == ir.ActionSniff {
			// 引擎先按 IP 判断 sniff 之前的规则，没命中再嗅探
			if !r.Config.Sniff {
				r.Config.Sniff, r.Config.SniffAfter = true, len(r.Config.Rules)
			}
			if rule.Match.Cond != nil || rule.Match.Logical != "" {
				warn("rule", fmt.Sprintf("#%d", i+1), i18n.T("带条件的 sniff 按无条件处理"), rule.Source)
			}
			continue
		}
		if rule.Action.Kind == ir.ActionHijackDNS && isDNSProtocol(rule.Match) {
			if !r.Config.HijackDNS {
				r.Config.HijackDNS, r.Config.HijackDNSAfter = true, len(r.Config.Rules)
			}
			continue
		}
		a, ok := action(rule.Action, rule.Source, warn)
		if !ok {
			continue
		}
		if rule.Match.Cond == nil {
			if rule.Match.Logical == "" {
				warn("rule", fmt.Sprintf("#%d", i+1), i18n.T("无条件规则（sniff / resolve 等动作之外）暂不支持，已跳过"), rule.Source)
				continue
			}
			m, ok := matchTree(rule.Match, usable, fmt.Sprintf("#%d", i+1), rule.Source, warn, i < resolvedFrom)
			if !ok {
				continue
			}
			r.Config.Rules = append(r.Config.Rules, Rule{Match: m, Action: a})
			r.RuleIndex = append(r.RuleIndex, i)
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

	outbounds := map[string]bool{}
	for _, n := range r.Config.Nodes {
		outbounds[n.Tag] = true
	}
	for _, g := range r.Config.Groups {
		outbounds[g.Tag] = true
	}
	r.Config.DNS = dns(c.DNS, usable, outbounds, warn)
	return r
}

type warnFn func(kind, subject, reason string, src ir.Source)

func action(a ir.Action, src ir.Source, warn warnFn) (Action, bool) {
	switch a.Kind {
	case ir.ActionRoute:
		switch a.Outbound {
		case ir.OutboundDirect, "":
			if a.OverrideAddress != "" {
				// hosts 覆盖：直连并把目标改写到这个地址
				return Action{Kind: "redirect", Target: a.OverrideAddress}, true
			}
			return Action{Kind: "direct"}, true
		case ir.OutboundReject:
			return Action{Kind: "reject"}, true
		}
		if a.OverrideAddress != "" {
			warn("rule", a.Outbound, i18n.T("经代理时改写目标地址暂不支持，按原目标连接"), src)
		}
		return Action{Kind: "route", Target: a.Outbound}, true
	case ir.ActionReject:
		return Action{Kind: "reject"}, true
	case ir.ActionDrop:
		// 引擎没有静默丢弃，近似为拒绝（应用会更快得到失败）
		warn("rule", "drop", i18n.T("drop 按 reject 处理"), src)
		return Action{Kind: "reject"}, true
	case ir.ActionResolve:
		// 引擎遇到可能命中的 IP 规则时自己解析，不需要这条
		return Action{}, false
	case ir.ActionHijackDNS:
		warn("rule", "hijack-dns", i18n.T("只支持「protocol: dns → hijack-dns」，带其他条件的接管 DNS 已跳过"), src)
		return Action{}, false
	}
	warn("rule", a.Kind, i18n.T("未知动作，已跳过"), src)
	return Action{}, false
}

// matchTree 转换逻辑条件；任一叶子不可用时整条规则不可用（已告警）。noResolve 时 IP 类叶子标 no-resolve（解析点之前）。
func matchTree(m ir.Match, usable map[string]bool, subject string, src ir.Source, warn warnFn, noResolve bool) (*Match, bool) {
	if m.Logical == "" {
		if m.Cond == nil {
			warn("rule", subject, i18n.T("逻辑规则里有空条件，已跳过"), src)
			return nil, false
		}
		cd, ok := cond(*m.Cond, usable, subject, src, warn)
		if !ok {
			return nil, false
		}
		if noResolve {
			cd.NoResolve = true
		}
		return &Match{Cond: &cd, Invert: m.Invert}, true
	}
	out := &Match{Logical: m.Logical, Invert: m.Invert}
	for _, c := range m.Children {
		k, ok := matchTree(c, usable, subject, src, warn, noResolve)
		if !ok {
			return nil, false
		}
		out.Children = append(out.Children, *k)
	}
	return out, true
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
		warn("rule", subject, i18n.T("引用的规则集不可用：%s", strings.Join(missing, ", ")), src)
	}
	switch {
	case len(c.ProcessName)+len(c.ProcessPath)+len(c.SourceIPCIDR)+len(c.Protocol) > 0:
		// 与条件（进程 / 源地址 / 协议）无法求值：整条跳过，否则会扩大命中范围
		warn("rule", subject, i18n.T("含暂不支持的条件（%s），已跳过", strings.Join(unsupported, ", ")), src)
		return Cond{}, false
	case hadAddr && !addr:
		// 地址类条件全部不可用：剩下的端口 / 网络条件会扩大命中范围，整条跳过
		warn("rule", subject, i18n.T("地址类条件都不可用（%s），已跳过", strings.Join(append(unsupported, missing...), ", ")), src)
		return Cond{}, false
	case len(c.DomainRegex) > 0:
		warn("rule", subject, i18n.T("顶层 domain_regex 暂不支持，已忽略（其余地址条件仍生效）"), src)
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
		warn("node", n.Tag, i18n.T("端口跳跃暂不支持，只用主端口"), src)
	}
	if n.Detour != "" {
		warn("node", n.Tag, i18n.T("链式代理（dialer-proxy）暂不支持"), src)
	}
	switch n.Protocol {
	case "shadowsocks", "anytls", "wireguard":
	default:
		warn("node", n.Tag, i18n.T("协议 %s 暂不支持（第二阶段）", n.Protocol), src)
	}
	return out
}

// dns 转换 DNS 服务器与规则。引擎支持 udp / https / local（physical 同）/ race / hosts；
// 其他类型去掉并告警，引用它们的竞速成员、规则一并清理。
func dns(d ir.DNS, usable, outbounds map[string]bool, warn warnFn) DNS {
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
			warn("dns", s.Tag, i18n.T("DNS 服务器类型 %s 暂不支持", s.Type), ir.Source{})
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
				warn("dns", s.Tag, i18n.T("竞速组的成员都不可用"), ir.Source{})
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
		detour := s.Detour
		if detour != "" && !outbounds[detour] {
			warn("dns", s.Tag, i18n.T("经出站 %s 查询：引擎里没有这个出站，改为直连查询", detour), ir.Source{})
			detour = ""
		}
		out.Servers = append(out.Servers, DNSServer{Tag: s.Tag, Type: typ, Address: s.Address,
			Members: keep(s.Members), Fallback: keep(s.Fallback), Resolver: s.Resolver, Detour: detour})
	}
	if out.Final != "" && !ok[out.Final] {
		warn("dns", out.Final, i18n.T("final 服务器不可用，引擎改用第一个服务器"), ir.Source{})
		out.Final = ""
	}
	for i, r := range d.Rules {
		subject := fmt.Sprintf("dns#%d", i+1)
		if len(r.Answer) == 0 && !ok[r.Server] {
			warn("dns", subject, i18n.T("服务器 %s 不可用，已跳过", r.Server), ir.Source{})
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
			m, good := matchTree(r.Match, usable, subject, ir.Source{}, warn, false)
			if !good {
				continue
			}
			out.Rules = append(out.Rules, DNSRule{Match: m, QueryType: r.QueryType, Server: r.Server, Answer: r.Answer, PreferredBy: r.PreferredBy})
			continue
		}
		out.Rules = append(out.Rules, DNSRule{Cond: cd, QueryType: r.QueryType, Server: r.Server, Answer: r.Answer, PreferredBy: r.PreferredBy})
	}
	if f := d.FakeIP; f != nil {
		out.FakeIP = &FakeIP{Inet4Range: f.Inet4Range, Inet6Range: f.Inet6Range, KeepSVCB: f.KeepSVCB}
	}
	return out
}

// isDNSProtocol：条件恰好是 protocol: dns（Velum 在 TUN 模式下插入的接管 DNS 规则）。
func isDNSProtocol(m ir.Match) bool {
	if m.Cond == nil || m.Logical != "" || m.Invert {
		return false
	}
	c := *m.Cond
	if len(c.Protocol) != 1 || c.Protocol[0] != "dns" {
		return false
	}
	c.Protocol = nil
	return reflect.DeepEqual(c, ir.Cond{})
}
