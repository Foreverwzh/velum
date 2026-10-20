// Package render 把 compiler.Plan（VelumIR）渲染为 sing-box 配置 JSON。
//
// 只依赖 sing-box 的 JSON 格式，不 import sing-box 的 Go 包；升级时按版本维护本包即可。
package render

import (
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/race"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

type obj = map[string]any

type Options struct {
	LogLevel string // trace / debug / info / warn / error，默认 info
}

// Rendered 是渲染产物。RuleSources[i] / RuleIndex[i] 对应 route.rules[i] 的来源与 IR 规则下标，
// 供动态实录把命中规则映射回 Provenance。
type Rendered struct {
	JSON        []byte
	RuleSources []ir.Source
	RuleIndex   []int
	Warnings    []ir.Warning
}

const rejectTag = ir.OutboundReject

func Render(p *compiler.Plan, opts Options) (*Rendered, error) {
	c := p.IR
	out := &Rendered{}
	warn := func(kind, reason, subject string, src ir.Source) {
		out.Warnings = append(out.Warnings, ir.Warning{Category: "render", Kind: kind, Level: ir.Degraded, Reason: reason, Subject: subject, Source: src})
	}

	// ---- outbounds / endpoints ----
	outbounds := []any{
		obj{"type": "direct", "tag": ir.OutboundDirect},
		// REJECT 渲染为拦截出站而非 reject 动作：reject 动作不经过 ConnectionTracker，动态实录看不到（M0 live F1）。
		// TODO: 以 Velum 自有出站类型替换已弃用的 block 出站。
		obj{"type": "block", "tag": rejectTag},
	}
	var endpoints []any
	// 域名解析（sing-box ≥1.12：设置了 domain_resolver 的出站直接使用该服务器，不经 DNS 规则）：
	//   - 节点服务器域名 → OutboundResolver（Clash proxy-server-nameserver）；
	//   - 规则直接指向 DIRECT 时，在其前插入同条件的 resolve 动作，使解析经过 DNS 规则（overlay / nameserver-policy）；
	//   - 其余（如经策略组选到 DIRECT）→ route.default_domain_resolver（DNS final）。
	nodeResolver := orDefault(c.DNS.OutboundResolver, c.DNS.Final)
	for _, n := range c.Nodes {
		o, isEndpoint, err := node(n)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.Tag, err)
		}
		if nodeResolver != "" && n.Server != "" && !isIP(n.Server) && n.Detour == "" {
			o["domain_resolver"] = nodeResolver
		}
		if isEndpoint {
			endpoints = append(endpoints, o)
		} else {
			outbounds = append(outbounds, o)
		}
	}
	for _, g := range c.Groups {
		o := obj{"tag": g.Tag, "outbounds": g.Members}
		switch g.Type {
		case ir.GroupSelect:
			o["type"] = "selector"
			if g.Default != "" {
				o["default"] = g.Default
			}
		case ir.GroupURLTest:
			o["type"] = "urltest"
			if g.URL != "" {
				o["url"] = g.URL
			}
			if g.Interval > 0 {
				o["interval"] = g.Interval.String()
			}
			if g.Tolerance > 0 {
				o["tolerance"] = g.Tolerance
			}
		default:
			return nil, fmt.Errorf("group %q: unknown type %q", g.Tag, g.Type)
		}
		outbounds = append(outbounds, o)
	}

	// ---- route ----
	directTags := map[string]bool{ir.OutboundDirect: true}
	for _, n := range c.Nodes {
		if n.Protocol == "direct" {
			directTags[n.Tag] = true
		}
	}
	var rules []any
	emit := func(o obj, irIdx int, src ir.Source) {
		rules = append(rules, o)
		out.RuleSources = append(out.RuleSources, src)
		out.RuleIndex = append(out.RuleIndex, irIdx)
	}
	for i, r := range c.Rules {
		ro, err := routeRule(r)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Source.Text, err)
		}
		if err := checkRuleSetMixing(r.Match); err != nil {
			warn("rule_set", err.Error(), r.Source.Text, r.Source)
		}
		if routesDirect(r, directTags) {
			pre := MatchJSON(r.Match)
			pre["action"] = "resolve"
			emit(pre, i, r.Source)
		}
		emit(ro, i, r.Source)
	}
	if c.Final.Kind == ir.ActionRoute && orDefault(c.Final.Outbound, ir.OutboundDirect) == ir.OutboundDirect {
		emit(obj{"action": "resolve"}, -1, ir.Source{Kind: ir.SourceVelum, Text: "final 为 DIRECT：按 DNS 规则解析"})
	}
	if len(c.DNS.Rules) > 0 && groupsReachDirect(c) {
		warn("dns", "经策略组选择 DIRECT 的连接按默认 DNS（"+c.DNS.Final+"）解析，不应用 DNS 分流规则（为避免代理域名泄漏到直连 DNS）", "", ir.Source{Kind: ir.SourceVelum})
	}
	var ruleSets []any
	for _, rs := range c.RuleSets {
		switch rs.Kind {
		case ir.RuleSetInline:
			var hs []any
			for i := range rs.Rules {
				hs = append(hs, cond(&rs.Rules[i]))
			}
			if len(hs) == 0 {
				// sing-box 不接受空的内联规则集：用 RFC 2606 保留的 .invalid 域名占位，等价于空集
				hs = append(hs, obj{"domain": []string{"empty-rule-set.velum.invalid"}})
			}
			ruleSets = append(ruleSets, obj{"type": "inline", "tag": rs.Tag, "rules": hs})
		case ir.RuleSetFile:
			ruleSets = append(ruleSets, obj{"type": "local", "tag": rs.Tag, "format": orDefault(rs.Format, "binary"), "path": rs.Path})
		case ir.RuleSetRemote:
			// 正常流程中 daemon 会先把远程规则集下载为本地文件；此分支仅在未经下载时使用（如离线校验）
			ruleSets = append(ruleSets, obj{"type": "remote", "tag": rs.Tag, "format": orDefault(rs.Format, "binary"),
				"url": rs.URL, "download_detour": ir.OutboundDirect})
		}
	}
	// Android：VpnService 已排除 Velum 自身，出站连接自然走物理网络；且 Android 11+ 不允许应用使用 netlink 监听网卡，不能开启
	route := obj{"rules": rules, "auto_detect_interface": p.Options.Mode == compiler.ModeTUN && p.Options.Platform != "android"}
	if len(ruleSets) > 0 {
		route["rule_set"] = ruleSets
	}
	switch c.Final.Kind {
	case ir.ActionReject, ir.ActionDrop:
		route["final"] = rejectTag
	default:
		route["final"] = orDefault(c.Final.Outbound, ir.OutboundDirect)
	}
	if c.DNS.Final != "" {
		route["default_domain_resolver"] = c.DNS.Final
	}

	// ---- dns ----
	dns, err := renderDNS(&c.DNS)
	if err != nil {
		return nil, err
	}

	inbounds := []any{obj{"type": "mixed", "tag": "mixed-in", "listen": p.Options.Listen.Addr().String(), "listen_port": p.Options.Listen.Port()}}
	if p.Route.Enabled {
		addrs := []string{p.Route.Address}
		if p.Route.Address6 != "" {
			addrs = append(addrs, p.Route.Address6)
		}
		tun := obj{"type": "tun", "tag": "tun-in", "address": addrs, "mtu": p.Route.MTU, "stack": "system"}
		if p.Options.Platform == "android" {
			// Android：VpnService 排除了 Velum 自身（防回环），system 栈在本进程监听 TCP、回包经内核路由回 TUN，
			// 而本进程的流量不走 VPN，握手无法完成；改用用户态的 gVisor 栈
			tun["stack"] = "gvisor"
		}
		if p.Options.Platform == "windows" {
			// Windows：velumd 以服务运行，内核自建 wintun 并设置地址、接口 DNS 与路由（RoutePlan 已扣除排除网段）
			tun["interface_name"], tun["auto_route"], tun["route_address"] = WindowsTUNName, true, p.Route.Routes
		} else {
			// macOS / Android：TUN 由 velum-helper / VpnService 创建并配置路由，内核经平台接口取得 FD，因此不开 auto_route
			tun["auto_route"] = false
		}
		inbounds = append(inbounds, tun)
	}
	cfg := obj{
		"log":       obj{"level": orDefault(opts.LogLevel, "info")},
		"dns":       dns,
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route":     route,
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}
	out.JSON, err = json.MarshalIndent(cfg, "", "  ")
	return out, err
}

// WindowsTUNName 是 Windows 上 wintun 网卡的名称。
const WindowsTUNName = "Velum"

// ---------- nodes ----------

func node(n ir.Node) (obj, bool, error) {
	o := obj{"tag": n.Tag}
	for k, v := range n.Options {
		o[k] = v
	}
	if n.Protocol == "wireguard" {
		ep := obj{"type": "wireguard", "tag": n.Tag}
		peer := obj{"address": n.Server, "port": n.Port}
		for k, v := range n.Options {
			switch k {
			case "address", "private_key", "mtu":
				ep[k] = v
			case "public_key", "pre_shared_key", "allowed_ips", "reserved", "persistent_keepalive_interval":
				peer[k] = v
			}
		}
		ep["peers"] = []any{peer}
		if n.Detour != "" {
			ep["detour"] = n.Detour
		}
		return ep, true, nil
	}
	switch n.Protocol {
	case "shadowsocks", "vmess", "vless", "trojan", "anytls", "hysteria", "hysteria2", "tuic", "socks", "http", "snell", "shadowtls", "direct":
		o["type"] = n.Protocol
	default:
		return nil, false, fmt.Errorf("unsupported protocol %q", n.Protocol)
	}
	if n.Server != "" {
		o["server"] = n.Server
		o["server_port"] = n.Port
	}
	if len(n.Ports) > 0 {
		o["server_ports"] = n.Ports
	}
	if n.Detour != "" {
		o["detour"] = n.Detour
	}
	if n.Dial.TCPFastOpen {
		o["tcp_fast_open"] = true
	}
	if n.Dial.MPTCP {
		o["tcp_multi_path"] = true
	}
	if n.Dial.BindInterface != "" {
		o["bind_interface"] = n.Dial.BindInterface
	}
	if n.Dial.DomainStrategy != "" {
		o["domain_strategy"] = n.Dial.DomainStrategy
	}
	if n.Dial.ConnectTimeout > 0 {
		o["connect_timeout"] = n.Dial.ConnectTimeout.String()
	}
	if t := n.TLS; t != nil {
		to := obj{"enabled": true}
		if t.ServerName != "" {
			to["server_name"] = t.ServerName
		}
		if t.Insecure {
			to["insecure"] = true
		}
		if len(t.ALPN) > 0 {
			to["alpn"] = t.ALPN
		}
		if t.Fingerprint != "" {
			to["utls"] = obj{"enabled": true, "fingerprint": t.Fingerprint}
		}
		if t.Reality != nil {
			to["reality"] = obj{"enabled": true, "public_key": t.Reality.PublicKey, "short_id": t.Reality.ShortID}
		}
		o["tls"] = to
	}
	if t := n.Transport; t != nil {
		to := obj{"type": t.Type}
		switch t.Type {
		case "ws":
			to["path"] = t.Path
			if len(t.Headers) > 0 {
				to["headers"] = t.Headers
			}
			if t.MaxEarlyData > 0 {
				to["max_early_data"] = t.MaxEarlyData
				to["early_data_header_name"] = t.EarlyDataHeader
			}
		case "grpc":
			to["service_name"] = t.ServiceName
		case "http":
			if len(t.Host) > 0 {
				to["host"] = t.Host
			}
			if t.Path != "" {
				to["path"] = t.Path
			}
			if t.Method != "" {
				to["method"] = t.Method
			}
		}
		o["transport"] = to
	}
	return o, false, nil
}

// ---------- rules ----------

func cond(c *ir.Cond) obj {
	o := obj{}
	set := func(k string, v []string) {
		if len(v) > 0 {
			o[k] = v
		}
	}
	set("domain", c.Domain)
	set("domain_suffix", c.DomainSuffix)
	set("domain_keyword", c.DomainKeyword)
	set("domain_regex", c.DomainRegex)
	set("ip_cidr", c.IPCIDR)
	if c.IPPrivate {
		o["ip_is_private"] = true
	}
	set("source_ip_cidr", c.SourceIPCIDR)
	if len(c.Port) > 0 {
		o["port"] = c.Port
	}
	set("network", c.Network)
	set("process_name", c.ProcessName)
	set("process_path", c.ProcessPath)
	set("rule_set", c.RuleSet)
	set("protocol", c.Protocol)
	return o
}

// MatchJSON 把 Match 渲染为 sing-box rule 条件部分（不含动作），也供测试直接构造 headless rule。
func MatchJSON(m ir.Match) obj {
	if m.Cond != nil {
		return cond(m.Cond)
	}
	if m.Logical == "" {
		return obj{}
	}
	var children []any
	for _, ch := range m.Children {
		children = append(children, MatchJSON(ch))
	}
	o := obj{"type": "logical", "mode": m.Logical, "rules": children}
	if m.Invert {
		o["invert"] = true
	}
	return o
}

func routeRule(r ir.Rule) (obj, error) {
	o := MatchJSON(r.Match)
	if m := r.Match; m.Cond == nil && m.Logical == "" && r.Action.Kind == ir.ActionRoute {
		return nil, fmt.Errorf("route action without condition")
	}
	switch a := r.Action; a.Kind {
	case ir.ActionRoute:
		o["action"] = "route"
		o["outbound"] = a.Outbound
		if a.OverrideAddress != "" {
			o["override_address"] = a.OverrideAddress
		}
	case ir.ActionReject:
		o["action"] = "route"
		o["outbound"] = rejectTag
	case ir.ActionDrop:
		o["action"] = "reject"
		o["method"] = "drop"
	case ir.ActionSniff:
		o["action"] = "sniff"
	case ir.ActionResolve:
		o["action"] = "resolve"
	case ir.ActionHijackDNS:
		o["action"] = "hijack-dns"
	default:
		return nil, fmt.Errorf("unknown action %q", a.Kind)
	}
	return o, nil
}

// routesDirect：规则直接把连接交给直连类出站（DIRECT 或绑定接口的直连），且无目标改写，需要先按 DNS 规则解析域名。
func routesDirect(r ir.Rule, direct map[string]bool) bool {
	return r.Action.Kind == ir.ActionRoute && direct[r.Action.Outbound] && r.Action.OverrideAddress == ""
}

// groupsReachDirect：是否有策略组（直接或间接）包含 DIRECT。
func groupsReachDirect(c *ir.Config) bool {
	for _, g := range c.Groups {
		for _, m := range g.Members {
			if m == ir.OutboundDirect {
				return true
			}
		}
	}
	return false
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// checkRuleSetMixing 检查 IR 约定：含 RuleSet 的 Cond 不得含地址类条件。
func checkRuleSetMixing(m ir.Match) error {
	if c := m.Cond; c != nil {
		if len(c.RuleSet) > 0 && (len(c.Domain)+len(c.DomainSuffix)+len(c.DomainKeyword)+len(c.DomainRegex)+len(c.IPCIDR) > 0 || c.IPPrivate) {
			return fmt.Errorf("rule_set 与地址类条件混放，sing-box 语义取决于规则集结构")
		}
		return nil
	}
	for _, ch := range m.Children {
		if err := checkRuleSetMixing(ch); err != nil {
			return err
		}
	}
	return nil
}

// ---------- dns ----------

func renderDNS(d *ir.DNS) (obj, error) {
	var servers []any
	for _, s := range d.Servers {
		o := obj{"tag": s.Tag, "type": s.Type}
		switch s.Type {
		case "local", "dhcp":
		case "race":
			o["type"], o["servers"] = race.Type, s.Members
		case "udp", "tcp", "tls", "https", "quic", "h3":
			o["server"] = s.Address
			if s.Port > 0 {
				o["server_port"] = s.Port
			}
			if s.Path != "" {
				o["path"] = s.Path
			}
			if s.Resolver != "" {
				o["domain_resolver"] = s.Resolver
			}
		default:
			return nil, fmt.Errorf("dns server %q: unknown type %q", s.Tag, s.Type)
		}
		if s.Detour != "" {
			o["detour"] = s.Detour
		}
		servers = append(servers, o)
	}
	if d.FakeIP != nil {
		f := obj{"type": "fakeip", "tag": "fakeip", "inet4_range": d.FakeIP.Inet4Range}
		if d.FakeIP.Inet6Range != "" {
			f["inet6_range"] = d.FakeIP.Inet6Range
		}
		servers = append(servers, f)
	}
	var rules []any
	for _, r := range d.Rules {
		o := MatchJSON(r.Match)
		if len(r.QueryType) > 0 {
			o["query_type"] = r.QueryType
		}
		if len(r.Answer) > 0 {
			o["action"] = "predefined"
			o["answer"] = r.Answer
		} else {
			o["action"] = "route"
			o["server"] = r.Server
		}
		rules = append(rules, o)
	}
	if d.FakeIP != nil {
		rules = append(rules, obj{"query_type": []string{"A", "AAAA"}, "action": "route", "server": "fakeip"})
	}
	out := obj{"servers": servers}
	if len(rules) > 0 {
		out["rules"] = rules
	}
	if d.Final != "" {
		out["final"] = d.Final
	}
	if d.Strategy != "" {
		out["strategy"] = d.Strategy
	}
	return out, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
