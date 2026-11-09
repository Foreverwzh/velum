package subscription

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"gopkg.in/yaml.v3"
)

// sing-box JSON 配置 → VelumIR。
//
// 字段名本来就是 sing-box 的写法：节点的协议参数原样放进 Node.Options（渲染时原样写回），
// 只把 IR 有专门字段的部分（服务器、拨号、TLS、传输层）拆出来。入站、日志、实验性功能由 Velum 自己管理。

// IsSingBox 报告配置是否为 sing-box 格式（顶层有 outbounds 或 endpoints，没有 Clash 的 proxies）。
func IsSingBox(doc *yaml.Node) bool {
	if mapGet(doc, "proxies") != nil || mapGet(doc, "proxy-groups") != nil || mapGet(doc, "proxy-providers") != nil {
		return false
	}
	return mapGet(doc, "outbounds") != nil || mapGet(doc, "endpoints") != nil
}

// sbConv 保存 sing-box 转换期间的状态。
type sbConv struct {
	*converter
	alias   map[string]string // direct / block / dns 类型出站的 tag → IR 内置出站（DIRECT / REJECT / dns）
	first   string            // 第一个出站：sing-box 没写 route.final 时的默认出站
	fakeIP  string            // fakeip DNS 服务器的 tag
	dnsTags map[string]bool
}

const sbHijackDNS = "\x00dns" // dns 类型出站（旧写法的 dns-out）

func (c *converter) parseSingBox(doc *yaml.Node) {
	s := &sbConv{converter: c, alias: map[string]string{}, dnsTags: map[string]bool{}}
	c.cfg.Final = ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	route := mapGet(doc, "route")
	for i := 0; i+1 < len(doc.Content); i += 2 {
		switch k := doc.Content[i].Value; k {
		case "outbounds", "endpoints", "route", "dns", "log":
		case "inbounds", "experimental", "ntp", "services", "certificate":
			c.warn("global", k, ir.Degraded, i18n.T("由 Velum 管理，订阅中的设置已忽略"), "", c.src(doc.Content[i], k))
		default:
			c.warn("global", k, ir.Degraded, i18n.T("顶层字段未映射"), "", c.src(doc.Content[i], k))
		}
	}
	s.tun(mapGet(doc, "inbounds"))
	s.prefetchRuleSets(mapGet(route, "rule_set"))
	s.outbounds(mapGet(doc, "outbounds"), mapGet(doc, "endpoints"))
	s.ruleSetList(mapGet(route, "rule_set"))
	s.route(route)
	s.dns(mapGet(doc, "dns"), route)
	sort.SliceStable(c.cfg.RuleSets, func(i, j int) bool { return c.cfg.RuleSets[i].Tag < c.cfg.RuleSets[j].Tag })
}

// tun 只取 IPv6：TUN 入站有 IPv6 地址表示订阅打算接管 IPv6。
func (s *sbConv) tun(seq *yaml.Node) {
	for _, in := range items(seq) {
		if str(in.o, "type") != "tun" {
			continue
		}
		for _, a := range strList(in.o["address"]) {
			if strings.Contains(a, ":") {
				s.cfg.IPv6 = true
			}
		}
	}
}

type item struct {
	o    obj
	node *yaml.Node
}

func items(seq *yaml.Node) []item {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	var out []item
	for _, n := range seq.Content {
		var o obj
		if err := n.Decode(&o); err == nil && o != nil {
			out = append(out, item{o, n})
		}
	}
	return out
}

// ---------- 出站 ----------

var sbProxyTypes = map[string]bool{"shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "anytls": true,
	"hysteria": true, "hysteria2": true, "tuic": true, "socks": true, "http": true, "shadowtls": true, "wireguard": true}

// sbDialFields 是拨号字段：有 IR 对应的拆出来，其余原样透传。
var sbNodeSkip = map[string]bool{"type": true, "tag": true, "server": true, "server_port": true, "server_ports": true, "detour": true,
	"tcp_fast_open": true, "tcp_multi_path": true, "bind_interface": true, "domain_strategy": true, "connect_timeout": true, "tls": true, "transport": true}

func (s *sbConv) outbounds(seq, endpoints *yaml.Node) {
	all := append(items(seq), items(endpoints)...)
	// 先登记全部 tag，允许前向引用（节点的 detour、组的成员）
	for _, it := range all {
		tag, typ := str(it.o, "tag"), str(it.o, "type")
		switch typ {
		case "direct":
			s.alias[tag] = ir.OutboundDirect
		case "block":
			s.alias[tag] = ir.OutboundReject
		case "dns":
			s.alias[tag] = sbHijackDNS
		default:
			s.tags[tag] = true
		}
		if s.first == "" && tag != "" {
			s.first = tag
		}
	}
	var groups []item
	for _, it := range all {
		tag, typ := str(it.o, "tag"), str(it.o, "type")
		src := s.src(it.node, "outbound "+tag+" ("+typ+")")
		switch {
		case typ == "direct" || typ == "block" || typ == "dns":
			for _, k := range sortedKeys(it.o) {
				if k != "type" && k != "tag" {
					s.warn("node", typ, ir.Degraded, i18n.T("字段 ")+k+i18n.T(" 未映射"), tag, src)
				}
			}
		case typ == "selector" || typ == "urltest":
			groups = append(groups, it)
		case sbProxyTypes[typ]:
			n, ok := s.node(it.o, src)
			if !ok {
				delete(s.tags, tag)
				continue
			}
			delete(s.tags, tag) // addNode 会重新登记
			s.addNode(n, typ)
			s.proxyNodes = append(s.proxyNodes, n.Tag)
		default:
			s.warn("node", typ, ir.Unsupported, i18n.T("协议无对应"), tag, src)
			delete(s.tags, tag)
		}
	}
	// detour 指向不存在的出站：节点不可用
	for i := 0; i < len(s.cfg.Nodes); i++ {
		n := &s.cfg.Nodes[i]
		if n.Detour == "" {
			continue
		}
		if a, ok := s.alias[n.Detour]; ok && a == ir.OutboundDirect {
			n.Detour = ""
			continue
		}
		if !s.tags[n.Detour] {
			s.warn("node", n.Protocol, ir.Unsupported, i18n.T("detour 指向的出站不可用"), n.Tag+" → "+n.Detour, n.Source)
			delete(s.tags, n.Tag)
			s.cfg.Nodes = append(s.cfg.Nodes[:i], s.cfg.Nodes[i+1:]...)
			i--
		}
	}
	s.nodeIndex = map[string]int{}
	for i, n := range s.cfg.Nodes {
		s.nodeIndex[n.Tag] = i
	}
	s.groups(groups)
}

func (s *sbConv) node(o obj, src ir.Source) (ir.Node, bool) {
	typ, tag := str(o, "type"), str(o, "tag")
	port, _ := intv(o, "server_port")
	n := ir.Node{Tag: tag, Protocol: typ, Server: str(o, "server"), Port: port, Detour: str(o, "detour"), Options: map[string]any{}, Source: src}
	if typ == "wireguard" {
		return s.wireguard(o, n)
	}
	for k, v := range o {
		if !sbNodeSkip[k] {
			n.Options[k] = v
		}
	}
	n.Ports = strList(o["server_ports"])
	n.Dial.TCPFastOpen, _ = boolv(o, "tcp_fast_open")
	n.Dial.MPTCP, _ = boolv(o, "tcp_multi_path")
	n.Dial.BindInterface = str(o, "bind_interface")
	n.Dial.DomainStrategy = str(o, "domain_strategy")
	if d, err := time.ParseDuration(str(o, "connect_timeout")); err == nil {
		n.Dial.ConnectTimeout = d
	}
	// TLS / 传输层：IR 能完整表达时拆出来，否则原样透传（渲染器不会覆盖 Options 中的同名键）
	if t := sub(o, "tls"); t != nil {
		if b, ok := boolv(t, "enabled"); !ok || b {
			if it, ok := sbTLS(t); ok {
				n.TLS = it
			} else {
				n.Options["tls"] = t
			}
		}
	}
	if t := sub(o, "transport"); t != nil {
		if it, ok := sbTransport(t); ok {
			n.Transport = it
		} else {
			n.Options["transport"] = t
		}
	}
	return n, true
}

// wireguard 把旧的 wireguard 出站或新的 endpoint 转为 IR 节点（渲染器统一写成 endpoint）。
func (s *sbConv) wireguard(o obj, n ir.Node) (ir.Node, bool) {
	opt := n.Options
	opt["private_key"] = str(o, "private_key")
	if v, ok := intv(o, "mtu"); ok {
		opt["mtu"] = v
	}
	addr := strList(o["address"])
	if len(addr) == 0 {
		addr = strList(o["local_address"]) // 旧写法
	}
	for i, a := range addr {
		addr[i] = hostPrefix(a)
	}
	opt["address"] = addr
	peer := o
	if peers, ok := o["peers"].([]any); ok {
		if len(peers) == 0 {
			s.warn("node", "wireguard", ir.Unsupported, i18n.T("没有 peer"), n.Tag, n.Source)
			return n, false
		}
		if len(peers) > 1 {
			s.warn("node", "wireguard", ir.Degraded, i18n.T("只使用第一个 peer"), n.Tag, n.Source)
		}
		peer, _ = peers[0].(map[string]any)
		n.Server = str(peer, "address")
		n.Port, _ = intv(peer, "port")
		opt["public_key"] = str(peer, "public_key")
	} else {
		opt["public_key"] = str(o, "peer_public_key")
	}
	if v := str(peer, "pre_shared_key"); v != "" {
		opt["pre_shared_key"] = v
	}
	if ips := strList(peer["allowed_ips"]); len(ips) > 0 {
		opt["allowed_ips"] = ips
	} else {
		opt["allowed_ips"] = []string{"0.0.0.0/0", "::/0"}
	}
	if r, ok := peer["reserved"]; ok {
		opt["reserved"] = r
	}
	if v, ok := intv(peer, "persistent_keepalive_interval"); ok {
		opt["persistent_keepalive_interval"] = v
	}
	return n, true
}

func sbTLS(t obj) (*ir.TLS, bool) {
	for k := range t {
		switch k {
		case "enabled", "server_name", "insecure", "alpn", "utls", "reality":
		default:
			return nil, false
		}
	}
	out := &ir.TLS{ServerName: str(t, "server_name"), ALPN: strList(t["alpn"])}
	out.Insecure, _ = boolv(t, "insecure")
	if u := sub(t, "utls"); u != nil {
		if b, ok := boolv(u, "enabled"); !ok || b {
			out.Fingerprint = orDefault(str(u, "fingerprint"), "chrome")
		}
	}
	if r := sub(t, "reality"); r != nil {
		if b, ok := boolv(r, "enabled"); !ok || b {
			out.Reality = &ir.Reality{PublicKey: str(r, "public_key"), ShortID: str(r, "short_id")}
		}
	}
	return out, true
}

func sbTransport(t obj) (*ir.Transport, bool) {
	known := map[string]map[string]bool{
		"ws":   {"type": true, "path": true, "headers": true, "max_early_data": true, "early_data_header_name": true},
		"grpc": {"type": true, "service_name": true},
		"http": {"type": true, "host": true, "path": true, "method": true},
	}[str(t, "type")]
	if known == nil {
		return nil, false
	}
	for k := range t {
		if !known[k] {
			return nil, false
		}
	}
	out := &ir.Transport{Type: str(t, "type"), Path: str(t, "path"), ServiceName: str(t, "service_name"), Method: str(t, "method"),
		Host: strList(t["host"]), EarlyDataHeader: str(t, "early_data_header_name")}
	out.MaxEarlyData, _ = intv(t, "max_early_data")
	if h := sub(t, "headers"); h != nil {
		out.Headers = map[string]string{}
		for k, v := range h {
			if l := strList(v); len(l) > 0 {
				out.Headers[k] = l[0]
			}
		}
	}
	return out, true
}

// ---------- 策略组 ----------

func (s *sbConv) groups(gs []item) {
	for _, it := range gs {
		s.tags[str(it.o, "tag")] = true
	}
	var kept []ir.Group
	for _, it := range gs {
		tag, typ := str(it.o, "tag"), str(it.o, "type")
		src := s.src(it.node, "outbound "+tag+" ("+typ+")")
		g := ir.Group{Tag: tag, OriginalType: typ, Source: src}
		seen := map[string]bool{}
		for _, m := range strList(it.o["outbounds"]) {
			t := s.member(m)
			if t == "" || !s.tags[t] {
				s.warn("group", typ, ir.Degraded, i18n.T("成员引用的节点 / 组不可用，已移除"), tag+" → "+m, src)
				continue
			}
			if !seen[t] {
				seen[t] = true
				g.Members = append(g.Members, t)
			}
		}
		switch typ {
		case "selector":
			g.Type = ir.GroupSelect
			if d := s.member(str(it.o, "default")); seen[d] {
				g.Default = d
			}
		case "urltest":
			g.Type = ir.GroupURLTest
			g.URL = str(it.o, "url")
			if d, err := time.ParseDuration(str(it.o, "interval")); err == nil {
				g.Interval = d
			}
			g.Tolerance, _ = intv(it.o, "tolerance")
		}
		for _, k := range sortedKeys(it.o) {
			switch k {
			case "type", "tag", "outbounds", "default", "url", "interval", "tolerance", "idle_timeout", "interrupt_exist_connections":
			default:
				s.warn("group", typ, ir.Degraded, i18n.T("字段 ")+k+i18n.T(" 未映射"), tag, src)
			}
		}
		if len(g.Members) == 0 {
			s.warn("group", typ, ir.Unsupported, i18n.T("无可用成员"), tag, src)
			delete(s.tags, tag)
			continue
		}
		kept = append(kept, g)
	}
	// 移除对已删除组的引用（被删的组可能排在引用它的组后面）
	for i := range kept {
		var ms []string
		for _, m := range kept[i].Members {
			if s.tags[m] {
				ms = append(ms, m)
			}
		}
		kept[i].Members = ms
		if kept[i].Default != "" && !s.tags[kept[i].Default] {
			kept[i].Default = ""
		}
	}
	s.cfg.Groups = append(s.cfg.Groups, kept...)
}

// member 把出站 tag 换成 IR 的名字（direct / block 类型的出站对应内置的 DIRECT / REJECT）。
func (s *sbConv) member(t string) string {
	if a, ok := s.alias[t]; ok {
		if a == sbHijackDNS {
			return ""
		}
		return a
	}
	return t
}

// ---------- 规则集 ----------

// srsMagic 是 sing-box 二进制规则集（.srs）的文件头。
var srsMagic = []byte("SRS")

var sbIPName = regexp.MustCompile(`(?i)(^|[^a-z])(geo)?ip([^a-z]|$)|geoip`)

func (s *sbConv) prefetchRuleSets(seq *yaml.Node) {
	var keys [][2]string
	for _, it := range items(seq) {
		if str(it.o, "type") == "remote" && str(it.o, "format") != "binary" && !strings.HasSuffix(str(it.o, "url"), ".srs") {
			keys = append(keys, [2]string{s.resolve(""), str(it.o, "url")})
		}
	}
	s.prefetchKeys(keys)
}

func (s *sbConv) ruleSetList(seq *yaml.Node) {
	for _, it := range items(seq) {
		tag, typ := str(it.o, "tag"), str(it.o, "type")
		src := s.src(it.node, "rule_set "+tag)
		rs := &ir.RuleSet{Tag: "rs:" + tag, Source: src}
		format := str(it.o, "format")
		var data []byte
		switch typ {
		case "inline":
			raw, _ := json.Marshal(obj{"rules": it.o["rules"]})
			data, format = raw, "source"
		case "remote":
			url := str(it.o, "url")
			if format == "" {
				format = "source"
				if strings.HasSuffix(url, ".srs") {
					format = "binary"
				}
			}
			rs.Kind, rs.URL, rs.Format = ir.RuleSetRemote, url, format
			if format == "source" {
				data, _ = s.read("", url) // 读不到时交给运行前的下载（ResolveRemoteRuleSets）
				if bytes.HasPrefix(data, srsMagic) {
					// 声明为 source、实际是二进制（有的订阅服务按 target 统一返回 .srs）：按内容为准
					rs.Format, data = "binary", nil
					s.warn("ruleset", "format", ir.Degraded, i18n.T("声明为 source 格式，实际内容是二进制，已按二进制加载"), tag, src)
				}
			}
		case "local":
			path := s.resolve(str(it.o, "path"))
			if format == "" && strings.HasSuffix(path, ".srs") {
				format = "binary"
			}
			if format == "binary" {
				rs.Kind, rs.Path, rs.Format = ir.RuleSetFile, path, format
				break
			}
			var err error
			if data, err = s.read(path, ""); err != nil {
				s.warn("ruleset", typ, ir.Unsupported, i18n.T("无法读取规则集内容"), tag, src)
				continue
			}
			format = "source"
		default:
			s.warn("ruleset", typ, ir.Unsupported, i18n.T("类型无对应"), tag, src)
			continue
		}
		if data != nil {
			if conds, hasIP, ok := sbHeadless(data); ok {
				// 只含普通规则：转为 IR 条件，可以静态推演（vlu why），也知道是否需要先解析域名
				rs.Kind, rs.Rules, rs.NeedsResolve, rs.Path, rs.Format = ir.RuleSetInline, conds, hasIP, "", ""
			} else if rs.Kind == "" {
				s.warn("ruleset", typ, ir.Unsupported, i18n.T("规则集内容无法识别"), tag, src)
				continue
			} else {
				rs.NeedsResolve = hasIP
			}
		} else {
			// 二进制规则集（或内容暂时读不到）：按名称推断是否含 IP 规则
			rs.NeedsResolve = sbIPName.MatchString(tag) || sbIPName.MatchString(rs.URL)
		}
		s.ruleSets[tag] = rs
		s.cfg.RuleSets = append(s.cfg.RuleSets, *rs)
	}
}

// sbHeadless 解析 source 格式规则集。ok 为 false 表示含 IR 无法表达的规则（逻辑规则、未知字段），
// 此时 hasIP 仍尽量给出（供推断是否需要先解析域名）。
func sbHeadless(data []byte) (conds []ir.Cond, hasIP, ok bool) {
	var doc struct {
		Rules []obj `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, false
	}
	ok = true
	for _, r := range doc.Rules {
		c, rest := sbCond(r)
		if c.HasIP() {
			hasIP = true
		}
		if len(rest) > 0 || len(c.RuleSet) > 0 || len(c.Protocol) > 0 {
			ok = false
			if _, isLogical := r["rules"]; isLogical && strings.Contains(fmt.Sprint(r), "ip_cidr") {
				hasIP = true
			}
			continue
		}
		c.NoResolve = true // 是否需要先解析由引用处（RuleSet.NeedsResolve）决定
		conds = append(conds, c)
	}
	return conds, hasIP, ok
}

// sbCond 把 sing-box 规则的匹配字段转为 Cond；rest 为无法转换的字段名。
func sbCond(r obj) (c ir.Cond, rest []string) {
	for _, k := range sortedKeys(r) {
		v := r[k]
		switch k {
		case "domain":
			c.Domain = strList(v)
		case "domain_suffix":
			c.DomainSuffix = strList(v)
		case "domain_keyword":
			c.DomainKeyword = strList(v)
		case "domain_regex":
			c.DomainRegex = strList(v)
		case "ip_cidr":
			c.IPCIDR = strList(v)
		case "ip_is_private":
			c.IPPrivate, _ = v.(bool)
		case "source_ip_cidr":
			c.SourceIPCIDR = strList(v)
		case "port":
			for _, p := range anyList(v) {
				if n, ok := intv(obj{"p": p}, "p"); ok {
					c.Port = append(c.Port, n)
				}
			}
		case "network":
			c.Network = strList(v)
		case "process_name":
			c.ProcessName = strList(v)
		case "process_path":
			c.ProcessPath = strList(v)
		case "rule_set":
			c.RuleSet = strList(v)
		case "protocol":
			c.Protocol = strList(v)
		default:
			rest = append(rest, k)
		}
	}
	return c, rest
}

func anyList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

// ---------- 路由 ----------

// sbRuleSkip 是规则中描述动作（而非匹配条件）的字段。
var sbRuleSkip = map[string]bool{"action": true, "outbound": true, "method": true, "no_drop": true, "invert": true,
	"type": true, "mode": true, "rules": true, "server": true, "timeout": true, "strategy": true, "sniffer": true, "disable_cache": true,
	"rewrite_ttl": true, "client_subnet": true, "override_address": true, "override_port": true}

func (s *sbConv) route(route *yaml.Node) {
	var r obj
	if route != nil {
		_ = route.Decode(&r)
	}
	for _, k := range sortedKeys(r) {
		switch k {
		case "rules", "rule_set", "final", "default_domain_resolver", "auto_detect_interface", "default_interface", "default_mark",
			"override_android_vpn", "find_process", "geoip", "geosite":
		default:
			s.warn("rule", k, ir.Degraded, i18n.T("字段未映射"), "", s.src(mapGetKey(route, k), "route."+k))
		}
	}
	for _, it := range items(mapGet(route, "rules")) {
		text, _ := json.Marshal(it.o)
		src := s.src(it.node, string(text))
		act, ok := s.action(it.o, src)
		if !ok {
			continue
		}
		m, ok := s.match(it.o, "rule", src)
		if !ok {
			continue
		}
		if m.Cond == nil && m.Logical == "" && act.Kind == ir.ActionRoute {
			s.warn("rule", "route", ir.Degraded, i18n.T("无条件的路由规则视为最终出站"), "", src)
			s.cfg.Final = act
			continue
		}
		s.cfg.Rules = append(s.cfg.Rules, ir.Rule{Match: m, Action: act, Source: src})
	}
	final := orDefault(str(r, "final"), s.first)
	if final != "" {
		src := s.src(mapGetKey(route, "final"), "route.final "+final)
		if act, ok := s.target(final, src); ok && act.Kind != ir.ActionHijackDNS {
			s.cfg.Final = act
		}
	}
}

// action 返回规则的动作；ok 为 false 表示跳过该规则（已告警或由 Velum 自行处理）。
func (s *sbConv) action(r obj, src ir.Source) (ir.Action, bool) {
	switch a := orDefault(str(r, "action"), "route"); a {
	case "route":
		act, ok := s.target(str(r, "outbound"), src)
		if ok && act.Kind == ir.ActionHijackDNS {
			return act, false // 旧写法的 dns-out：Velum 自己接管 DNS
		}
		return act, ok
	case "reject":
		if str(r, "method") == "drop" {
			return ir.Action{Kind: ir.ActionDrop}, true
		}
		return ir.Action{Kind: ir.ActionReject}, true
	case "sniff", "hijack-dns":
		return ir.Action{}, false // Velum 自己嗅探、接管 DNS 查询
	case "resolve":
		return ir.Action{Kind: ir.ActionResolve}, true
	default:
		s.warn("rule", a, ir.Unsupported, i18n.T("动作无对应"), "", src)
		return ir.Action{}, false
	}
}

func (s *sbConv) target(t string, src ir.Source) (ir.Action, bool) {
	if a, ok := s.alias[t]; ok {
		switch a {
		case ir.OutboundReject:
			return ir.Action{Kind: ir.ActionReject}, true
		case sbHijackDNS:
			return ir.Action{Kind: ir.ActionHijackDNS}, true
		}
		return ir.Action{Kind: ir.ActionRoute, Outbound: a}, true
	}
	if !s.tags[t] {
		s.warn("rule", "target", ir.Degraded, i18n.T("目标策略「")+t+i18n.T("」不存在，回落 DIRECT"), "", src)
		return ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}, true
	}
	return ir.Action{Kind: ir.ActionRoute, Outbound: t}, true
}

// match 把规则（default 或 logical）的条件转为 Match；ok 为 false 表示含无法表达的条件，整条跳过（已告警）。
// 少一个条件会让规则匹配得更宽，所以不做部分转换。
func (s *sbConv) match(r obj, cat string, src ir.Source) (ir.Match, bool) {
	invert, _ := boolv(r, "invert")
	if str(r, "type") == "logical" {
		m := ir.Match{Logical: str(r, "mode"), Invert: invert}
		if m.Logical != "and" && m.Logical != "or" {
			s.warn(cat, "logical", ir.Unsupported, i18n.T("逻辑规则的 mode 无效"), "", src)
			return m, false
		}
		for _, ch := range anyList(r["rules"]) {
			co, _ := ch.(map[string]any)
			cm, ok := s.match(co, cat, src)
			if !ok {
				return m, false
			}
			m.Children = append(m.Children, cm)
		}
		return m, true
	}
	cond := obj{}
	for k, v := range r {
		if !sbRuleSkip[k] && !(cat == "dns" && k == "query_type") {
			cond[k] = v
		}
	}
	c, rest := sbCond(cond)
	if len(rest) > 0 {
		s.warn(cat, strings.Join(rest, "+"), ir.Unsupported, i18n.T("匹配条件无对应，整条规则已跳过"), "", src)
		return ir.Match{}, false
	}
	var sets []string
	for _, t := range c.RuleSet {
		rs := s.ruleSets[t]
		if rs == nil {
			s.warn(cat, "rule_set", ir.Unsupported, i18n.T("引用的规则集不可用"), t, src)
			return ir.Match{}, false
		}
		sets = append(sets, rs.Tag)
	}
	c.RuleSet = nil
	if len(sets) == 0 {
		if isEmptyCond(c) {
			return ir.Match{Invert: invert}, true
		}
		return ir.Match{Cond: &c, Invert: invert}, true
	}
	// sing-box 里规则集与同一规则内的地址条件为「或」；IR 不允许二者放在同一个 Cond，拆成 or
	addr := ir.Cond{Domain: c.Domain, DomainSuffix: c.DomainSuffix, DomainKeyword: c.DomainKeyword, DomainRegex: c.DomainRegex,
		IPCIDR: c.IPCIDR, IPPrivate: c.IPPrivate}
	other := c
	other.Domain, other.DomainSuffix, other.DomainKeyword, other.DomainRegex, other.IPCIDR, other.IPPrivate = nil, nil, nil, nil, nil, false
	withSets := other
	withSets.RuleSet = sets
	if isEmptyCond(addr) {
		return ir.Match{Cond: &withSets, Invert: invert}, true
	}
	a := other
	a.Domain, a.DomainSuffix, a.DomainKeyword, a.DomainRegex, a.IPCIDR, a.IPPrivate = addr.Domain, addr.DomainSuffix, addr.DomainKeyword, addr.DomainRegex, addr.IPCIDR, addr.IPPrivate
	return ir.Match{Logical: "or", Invert: invert, Children: []ir.Match{ir.Leaf(a), ir.Leaf(withSets)}}, true
}

func isEmptyCond(c ir.Cond) bool {
	b, _ := json.Marshal(c)
	return string(b) == "{}"
}

// ---------- DNS ----------

var sbDNSIgnore = map[string]bool{"servers": true, "rules": true, "final": true, "strategy": true, "reverse_mapping": true,
	"independent_cache": true, "disable_cache": true, "disable_expire": true, "cache_capacity": true, "fakeip": true}

func (s *sbConv) dns(node, route *yaml.Node) {
	D := &s.cfg.DNS
	if r := mapGet(route, "default_domain_resolver"); r != nil {
		var v any
		_ = r.Decode(&v)
		D.OutboundResolver = resolverTag(v)
	}
	if node == nil {
		return
	}
	var d obj
	_ = node.Decode(&d)
	for _, k := range sortedKeys(d) {
		if !sbDNSIgnore[k] {
			s.warn("dns", k, ir.Degraded, i18n.T("字段未映射"), "", s.src(mapGetKey(node, k), "dns."+k))
		}
	}
	for _, it := range items(mapGet(node, "servers")) {
		s.dnsServer(it)
	}
	if fk := sub(d, "fakeip"); fk != nil && D.FakeIP == nil { // 旧写法：dns.fakeip + address: fakeip 的服务器
		if b, _ := boolv(fk, "enabled"); b {
			D.FakeIP = &ir.FakeIP{Inet4Range: orDefault(str(fk, "inet4_range"), "198.18.0.0/15"), Inet6Range: str(fk, "inet6_range")}
		}
	}
	for _, it := range items(mapGet(node, "rules")) {
		text, _ := json.Marshal(it.o)
		src := s.src(it.node, string(text))
		// 旧写法：outbound: any 的规则指定解析节点域名用的服务器
		if ob := strList(it.o["outbound"]); len(ob) > 0 {
			if len(ob) == 1 && ob[0] == "any" && D.OutboundResolver == "" && s.dnsTags[str(it.o, "server")] {
				D.OutboundResolver = str(it.o, "server")
			} else if !(len(ob) == 1 && ob[0] == "any") {
				s.warn("dns", "outbound", ir.Unsupported, i18n.T("按出站选择 DNS 服务器无对应，整条规则已跳过"), "", src)
			}
			continue
		}
		if a := orDefault(str(it.o, "action"), "route"); a != "route" {
			s.warn("dns", a, ir.Unsupported, i18n.T("动作无对应"), "", src)
			continue
		}
		server := str(it.o, "server")
		if server == s.fakeIP && server != "" {
			continue // fake-ip 由 Velum 统一追加在规则末尾（没被其他规则接走的域名）
		}
		if !s.dnsTags[server] {
			s.warn("dns", "server", ir.Unsupported, i18n.T("引用的 DNS 服务器不可用"), server, src)
			continue
		}
		m, ok := s.match(it.o, "dns", src)
		if !ok {
			continue
		}
		D.Rules = append(D.Rules, ir.DNSRule{Match: m, QueryType: strList(it.o["query_type"]), Server: server, Source: src})
	}
	if f := str(d, "final"); s.dnsTags[f] {
		D.Final = f
	} else {
		for _, sv := range D.Servers { // sing-box 默认用第一个服务器
			D.Final = sv.Tag
			break
		}
	}
	if st := str(d, "strategy"); st != "" {
		D.Strategy = st
	}
	if D.OutboundResolver != "" && !s.dnsTags[D.OutboundResolver] {
		D.OutboundResolver = ""
	}
}

func resolverTag(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m, "server")
	}
	s, _ := v.(string)
	return s
}

func (s *sbConv) dnsServer(it item) {
	tag, typ := str(it.o, "tag"), str(it.o, "type")
	src := s.src(it.node, "dns server "+tag)
	D := &s.cfg.DNS
	var sv ir.DNSServer
	if typ == "" { // 旧写法：address 为 URL
		addr := str(it.o, "address")
		switch {
		case addr == "fakeip":
			typ = "fakeip"
		case addr == "local":
			sv = ir.DNSServer{Tag: tag, Type: "local"}
		case strings.HasPrefix(addr, "rcode://"):
			s.warn("dns", "rcode", ir.Unsupported, i18n.T("类型无对应"), tag, src)
			return
		default:
			var ok bool
			if sv, ok = dnsServer(addr, tag, str(it.o, "address_resolver")); !ok {
				s.warn("dns", "server", ir.Unsupported, i18n.T("无法解析的 DNS 地址格式"), addr, src)
				return
			}
		}
	} else {
		switch typ {
		case "local", "dhcp":
			sv = ir.DNSServer{Tag: tag, Type: typ}
		case "udp", "tcp", "tls", "https", "quic", "h3":
			sv = ir.DNSServer{Tag: tag, Type: typ, Address: str(it.o, "server"), Path: str(it.o, "path"), Resolver: resolverTag(it.o["domain_resolver"])}
			sv.Port, _ = intv(it.o, "server_port")
			if sv.Path == "/dns-query" {
				sv.Path = ""
			}
		case "fakeip":
		default:
			s.warn("dns", typ, ir.Unsupported, i18n.T("类型无对应"), tag, src)
			return
		}
	}
	if typ == "fakeip" {
		s.fakeIP = tag
		D.FakeIP = &ir.FakeIP{Inet4Range: orDefault(str(it.o, "inet4_range"), "198.18.0.0/15"), Inet6Range: str(it.o, "inet6_range")}
		return
	}
	if dt := str(it.o, "detour"); dt != "" {
		switch t := s.member(dt); {
		case t == ir.OutboundDirect:
		case t != "" && s.tags[t]:
			sv.Detour = t
		default:
			s.warn("dns", "server", ir.Degraded, i18n.T("指定的出站 ")+dt+i18n.T(" 不存在，改为按默认方式连接"), tag, src)
		}
	}
	sv.Source = src
	s.dnsTags[tag] = true
	D.Servers = append(D.Servers, sv)
}
