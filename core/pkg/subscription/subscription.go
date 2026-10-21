// Package subscription 把 Clash / Stash / mihomo YAML 转换为 VelumIR。
//
// 原则：可以降级，但绝不静默丢弃——每个降级或不支持的项都会产生 ir.Warning。
package subscription

import (
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
	"gopkg.in/yaml.v3"
)

type obj = map[string]any

// Options 控制转换时如何读取外部文件（rule-providers / proxy-providers）。
type Options struct {
	// File 是订阅文件名，写入 Provenance。
	File string
	// BaseDirs 用于解析 provider 的相对路径，按顺序尝试。
	BaseDirs []string
	// ReadFile 读取 provider 内容；为空时使用 os.ReadFile。生产环境由 Velum 注入「拉取 + 缓存」实现。
	ReadFile func(path string, url string) ([]byte, error)
	// GeoRuleSetURL 是 GEOIP / GEOSITE 规则集（.srs）的下载地址模板，{kind} 为 geoip / geosite，{code} 为代码（如 cn）。
	// 为空时使用 DefaultGeoRuleSetURL；Velum 会把实际取值写入用户可编辑的配置。
	GeoRuleSetURL string
}

// DefaultGeoRuleSetURL 是 sing-box 官方规则集仓库地址（部分网络环境不可达，可在配置中替换为镜像）。
const DefaultGeoRuleSetURL = "https://raw.githubusercontent.com/SagerNet/sing-{kind}/rule-set/{kind}-{code}.srs"

type converter struct {
	opts          Options
	cfg           *ir.Config
	tags          map[string]bool
	nodeIndex     map[string]int    // tag -> cfg.Nodes 下标（relay 克隆用）
	nodeType      map[string]string // tag -> Clash type
	proxyNodes    []string
	providerNodes map[string][]string
	ruleSets      map[string]*ir.RuleSet
	prefetched    map[[2]string]readResult // (path, url) → 预先并发读取的结果
}

type readResult struct {
	data []byte
	err  error
}

// prefetchLimit 是并发读取 provider 的上限。订阅常带几十个远程 provider，逐个拉取（每个最长 30 秒）
// 在网络不佳时会累计到数分钟。
const prefetchLimit = 8

// Parse 转换一份 Clash YAML。
func Parse(data []byte, opts Options) (*ir.Config, error) {
	if opts.ReadFile == nil {
		opts.ReadFile = func(p, _ string) ([]byte, error) { return userfs.ReadFile(p) }
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("subscription is not a YAML mapping")
	}
	doc := root.Content[0]
	c := &converter{opts: opts, cfg: &ir.Config{}, tags: map[string]bool{ir.OutboundDirect: true, ir.OutboundReject: true},
		nodeIndex: map[string]int{}, nodeType: map[string]string{}, providerNodes: map[string][]string{}, ruleSets: map[string]*ir.RuleSet{}}
	c.global(doc)
	c.prefetch(doc)
	c.proxies(mapGet(doc, "proxies"))
	c.proxyProviders(mapGet(doc, "proxy-providers"))
	c.groups(mapGet(doc, "proxy-groups"))
	c.ruleProviders(mapGet(doc, "rule-providers"))
	c.rules(mapGet(doc, "rules"))
	c.dns(mapGet(doc, "dns"))
	sort.SliceStable(c.cfg.RuleSets, func(i, j int) bool { return c.cfg.RuleSets[i].Tag < c.cfg.RuleSets[j].Tag })
	return c.cfg, nil
}

func (c *converter) src(n *yaml.Node, text string) ir.Source {
	s := ir.Source{Kind: ir.SourceSubscription, File: c.opts.File, Text: text}
	if n != nil {
		s.Line = n.Line
	}
	return s
}

func (c *converter) warn(cat, kind, level, reason, subject string, src ir.Source) {
	c.cfg.Warnings = append(c.cfg.Warnings, ir.Warning{Category: cat, Kind: kind, Level: level, Reason: reason, Subject: subject, Source: src})
}

// ---------- 全局 ----------

func (c *converter) global(doc *yaml.Node) {
	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i].Value, doc.Content[i+1]
		switch k {
		case "proxies", "proxy-groups", "rules", "rule-providers", "dns", "proxy-providers":
		case "mode":
			if !strings.EqualFold(v.Value, "rule") {
				c.warn("global", "mode="+v.Value, ir.Degraded, "非 rule 模式由 Velum 运行时切换实现", "", c.src(v, k))
			}
		case "ipv6":
			c.cfg.IPv6 = v.Value == "true"
			c.cfg.IPv6Disabled = v.Value == "false"
		default:
			c.warn("global", k, ir.Degraded, "顶层字段未映射", "", c.src(doc.Content[i], k))
		}
	}
}

// ---------- 节点 ----------

func (c *converter) addNode(n ir.Node, typ string) string {
	tag := n.Tag
	for i := 2; c.tags[tag]; i++ {
		tag = fmt.Sprintf("%s #%d", n.Tag, i)
	}
	n.Tag = tag
	c.tags[tag] = true
	c.nodeType[tag] = typ
	c.nodeIndex[tag] = len(c.cfg.Nodes)
	c.cfg.Nodes = append(c.cfg.Nodes, n)
	return tag
}

func (c *converter) convertAndAdd(item *yaml.Node, src ir.Source) (string, bool) {
	var p obj
	if err := item.Decode(&p); err != nil {
		c.warn("node", "?", ir.Unsupported, "无法解析", "", src)
		return "", false
	}
	main, extra, ok := c.convertProxy(p, src)
	if !ok {
		return "", false
	}
	for _, e := range extra {
		main.Detour = c.addNode(e, "shadowtls") // tag 可能因重名被改写
	}
	return c.addNode(main, str(p, "type")), true
}

func (c *converter) proxies(seq *yaml.Node) {
	if seq == nil {
		return
	}
	for _, item := range seq.Content {
		if tag, ok := c.convertAndAdd(item, c.src(item, "")); ok {
			c.proxyNodes = append(c.proxyNodes, tag)
		}
	}
}

func (c *converter) proxyProviders(m *yaml.Node) {
	if m == nil {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		name, node := m.Content[i].Value, m.Content[i+1]
		var pp obj
		_ = node.Decode(&pp)
		src := c.src(m.Content[i], "proxy-provider "+name)
		data, err := c.read(orDefault(str(pp, "path"), "./proxy_providers/"+name+".yaml"), str(pp, "url"))
		if err != nil {
			c.warn("provider", str(pp, "type"), ir.Unsupported, "无法读取 provider 内容", name, src)
			continue
		}
		var pdoc yaml.Node
		if err := yaml.Unmarshal(data, &pdoc); err != nil || len(pdoc.Content) == 0 {
			c.warn("provider", str(pp, "type"), ir.Unsupported, "provider 内容无法解析", name, src)
			continue
		}
		include, err1 := compileFilters(str(pp, "filter"))
		exclude, err2 := compileFilters(str(pp, "exclude-filter"))
		if err1 != nil || err2 != nil {
			c.warn("provider", "filter", ir.Degraded, "filter 正则含 Go RE2 不支持的语法，已忽略过滤", name, src)
		}
		if _, ok := pp["override"]; ok {
			c.warn("provider", "override", ir.Degraded, "override 未实现", name, src)
		}
		if seq := mapGet(pdoc.Content[0], "proxies"); seq != nil {
			for _, item := range seq.Content {
				var p obj
				_ = item.Decode(&p)
				if !matchFilters(str(p, "name"), include, exclude) {
					continue
				}
				isrc := src
				isrc.Line = item.Line
				isrc.File = name
				if tag, ok := c.convertAndAdd(item, isrc); ok {
					c.providerNodes[name] = append(c.providerNodes[name], tag)
				}
			}
		}
	}
}

func (c *converter) read(path, rawURL string) ([]byte, error) {
	path = c.resolve(path)
	if r, ok := c.prefetched[[2]string{path, rawURL}]; ok {
		return r.data, r.err
	}
	return c.opts.ReadFile(path, rawURL)
}

// resolve 把 provider 的相对路径按 BaseDirs 解析为已存在的文件。
func (c *converter) resolve(path string) string {
	if !filepath.IsAbs(path) {
		for _, d := range c.opts.BaseDirs {
			if _, err := userfs.Stat(filepath.Join(d, path)); err == nil {
				return filepath.Join(d, path)
			}
		}
	}
	return path
}

// prefetch 并发读取所有带 url 的 provider（结果供 read 使用），使总耗时接近最慢的一个，而不是逐个累加。
func (c *converter) prefetch(doc *yaml.Node) {
	var keys [][2]string
	for _, sec := range []struct{ key, defDir string }{{"proxy-providers", "./proxy_providers/"}, {"rule-providers", ""}} {
		m := mapGet(doc, sec.key)
		if m == nil {
			continue
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			var p obj
			_ = m.Content[i+1].Decode(&p)
			if str(p, "url") == "" {
				continue // 只有本地 path：读文件很快，无需并发
			}
			path := str(p, "path")
			if sec.defDir != "" {
				path = orDefault(path, sec.defDir+m.Content[i].Value+".yaml")
			}
			keys = append(keys, [2]string{c.resolve(path), str(p, "url")})
		}
	}
	if len(keys) < 2 {
		return
	}
	results := make([]readResult, len(keys))
	sem := make(chan struct{}, prefetchLimit)
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := c.opts.ReadFile(k[0], k[1])
			results[i] = readResult{data, err}
		}()
	}
	wg.Wait()
	c.prefetched = make(map[[2]string]readResult, len(keys))
	for i, k := range keys {
		c.prefetched[k] = results[i]
	}
}

// ---------- 策略组 ----------

// mihomo 的 exclude-type 使用内部适配器类型名，而非 YAML 中的 type。
var mihomoAdapterType = map[string]string{
	"ss": "Shadowsocks", "ssr": "ShadowsocksR", "vmess": "Vmess", "vless": "Vless", "trojan": "Trojan",
	"hysteria": "Hysteria", "hysteria2": "Hysteria2", "tuic": "Tuic", "socks5": "Socks5", "http": "Http",
	"wireguard": "WireGuard", "snell": "Snell", "anytls": "AnyTLS", "ssh": "Ssh",
}

var knownGroupFields = map[string]bool{"name": true, "type": true, "proxies": true, "url": true, "benchmark-url": true, "interval": true,
	"tolerance": true, "lazy": true, "timeout": true, "benchmark-timeout": true, "hidden": true, "icon": true, "ssid-policy": true,
	"use": true, "filter": true, "exclude-filter": true, "include-all": true, "include-all-proxies": true, "include-all-providers": true,
	"exclude-type": true, "strategy": true, "max-failed-times": true, "expected-status": true, "disable-udp": true}

func (c *converter) groups(seq *yaml.Node) {
	if seq == nil {
		return
	}
	type pending struct {
		g    obj
		node *yaml.Node
	}
	var gs []pending
	for _, item := range seq.Content {
		var g obj
		if err := item.Decode(&g); err == nil {
			c.tags[str(g, "name")] = true // 先登记，允许前向引用
			gs = append(gs, pending{g, item})
		}
	}
	for _, p := range gs {
		if !c.group(p.g, c.src(p.node, "proxy-group "+str(p.g, "name"))) {
			delete(c.tags, str(p.g, "name"))
		}
	}
	// 移除对已删除组的引用
	for i := range c.cfg.Groups {
		g := &c.cfg.Groups[i]
		var kept []string
		for _, m := range g.Members {
			if c.tags[m] {
				kept = append(kept, m)
			}
		}
		g.Members = kept
	}
}

func (c *converter) group(g obj, src ir.Source) bool {
	name, typ := str(g, "name"), str(g, "type")
	var members []string
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			members = append(members, t)
		}
	}
	for _, m := range strList(g["proxies"]) {
		t := mapTarget(m)
		if !c.tags[t] {
			c.warn("group", typ, ir.Degraded, "成员引用的节点 / 组不可用，已移除", name+" → "+m, src)
			continue
		}
		add(t)
	}
	var dynamic []string
	for _, pn := range strList(g["use"]) {
		dynamic = append(dynamic, c.providerNodes[pn]...)
	}
	allProviders := func() {
		for _, pn := range sortedKeys(c.providerNodes) {
			dynamic = append(dynamic, c.providerNodes[pn]...)
		}
	}
	if b, _ := boolv(g, "include-all"); b {
		dynamic = append(dynamic, c.proxyNodes...)
		allProviders()
	}
	if b, _ := boolv(g, "include-all-proxies"); b {
		dynamic = append(dynamic, c.proxyNodes...)
	}
	if b, _ := boolv(g, "include-all-providers"); b {
		allProviders()
	}
	if g["use"] != nil || g["include-all"] != nil || g["include-all-proxies"] != nil || g["include-all-providers"] != nil {
		include, err1 := compileFilters(str(g, "filter"))
		exclude, err2 := compileFilters(str(g, "exclude-filter"))
		if err1 != nil || err2 != nil {
			c.warn("group", "filter", ir.Degraded, "filter 正则含 Go RE2 不支持的语法，已忽略过滤", name, src)
		}
		exTypes := map[string]bool{}
		for _, t := range strings.Split(str(g, "exclude-type"), "|") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				exTypes[t] = true
			}
		}
		for _, t := range dynamic {
			if matchFilters(t, include, exclude) && !exTypes[strings.ToLower(mihomoAdapterType[c.nodeType[t]])] {
				add(t)
			}
		}
	}
	if _, ok := g["ssid-policy"]; ok {
		c.warn("group", "ssid-policy", ir.Degraded, "Stash 私有（按 Wi-Fi 切换），将由 NetworkSense 场景触发实现", name, src)
	}
	for _, k := range sortedKeys(g) {
		if !knownGroupFields[k] {
			c.warn("group", typ, ir.Degraded, "字段 "+k+" 未映射", name, src)
		}
	}
	if len(members) == 0 {
		c.warn("group", typ, ir.Unsupported, "无可用成员", name, src)
		return false
	}
	out := ir.Group{Tag: name, Members: members, OriginalType: typ, Source: src}
	switch typ {
	case "select":
		out.Type = ir.GroupSelect
	case "url-test", "fallback", "load-balance":
		out.Type = ir.GroupURLTest
		out.URL = str(g, "url", "benchmark-url")
		if v, ok := intv(g, "interval"); ok {
			out.Interval = time.Duration(v) * time.Second
		}
		if v, ok := intv(g, "tolerance"); ok {
			out.Tolerance = v
		}
		switch typ {
		case "fallback":
			c.warn("group", typ, ir.Degraded, "降级为 urltest：按延迟而非顺序选择", name, src)
		case "load-balance":
			c.warn("group", typ, ir.Degraded, "降级为 urltest：无负载均衡（strategy="+orDefault(str(g, "strategy"), "consistent-hashing")+"）", name, src)
		}
	case "relay":
		return c.relay(name, members, src)
	default:
		c.warn("group", typ, ir.Unsupported, "类型无对应", name, src)
		return false
	}
	c.cfg.Groups = append(c.cfg.Groups, out)
	return true
}

// relay [A, B, C]：本机 → A → B → C → 目标。克隆 B、C 并挂 detour：B'.Detour = A，C'.Detour = B'，C' 以组名为 tag。
func (c *converter) relay(name string, members []string, src ir.Source) bool {
	if len(members) < 2 {
		c.warn("group", "relay", ir.Unsupported, "relay 至少需要两跳", name, src)
		return false
	}
	var clones []ir.Node
	prev := members[0]
	for i, m := range members[1:] {
		idx, ok := c.nodeIndex[m]
		if !ok || c.cfg.Nodes[idx].Protocol == "wireguard" {
			c.warn("group", "relay", ir.Unsupported, "非首跳成员是策略组或 endpoint，无法挂 detour", name, src)
			return false
		}
		clone := c.cfg.Nodes[idx]
		clone.Options = copyMap(clone.Options)
		clone.Tag = fmt.Sprintf("%s/hop%d", name, i+2)
		if i == len(members)-2 {
			clone.Tag = name
		}
		clone.Detour = prev
		clone.Source = src
		clones = append(clones, clone)
		prev = clone.Tag
	}
	for _, n := range clones {
		c.nodeIndex[n.Tag] = len(c.cfg.Nodes)
		c.cfg.Nodes = append(c.cfg.Nodes, n)
	}
	return true
}

// ---------- 规则集 ----------

func (c *converter) ruleProviders(m *yaml.Node) {
	if m == nil {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		name, node := m.Content[i].Value, m.Content[i+1]
		var rp obj
		_ = node.Decode(&rp)
		src := c.src(m.Content[i], "rule-provider "+name)
		behavior := str(rp, "behavior")
		data, err := c.read(str(rp, "path"), str(rp, "url"))
		if err != nil {
			c.warn("ruleset", behavior, ir.Unsupported, "无法读取规则集内容", name, src)
			continue
		}
		res := ConvertEntries(ParseProvider(data, behavior, str(rp, "format")))
		for t, n := range res.Unsupported {
			c.warn("ruleset", t, ir.Unsupported, fmt.Sprintf("规则类型无对应（%d 条）", n), name, src)
		}
		for t, n := range res.Degraded {
			c.warn("ruleset", t, ir.Degraded, fmt.Sprintf("通配符转正则（%d 条）", n), name, src)
		}
		rs := &ir.RuleSet{Tag: "rs:" + name, Kind: ir.RuleSetInline, Rules: res.Conds, NeedsResolve: res.NeedsResolve, Source: src}
		c.ruleSets[name] = rs
		c.cfg.RuleSets = append(c.cfg.RuleSets, *rs)
	}
}

func (c *converter) remoteSet(kind, code string, src ir.Source) string {
	code = strings.ToLower(code)
	tag := kind + "-" + code
	for _, rs := range c.cfg.RuleSets {
		if rs.Tag == tag {
			return tag
		}
	}
	c.cfg.RuleSets = append(c.cfg.RuleSets, ir.RuleSet{Tag: tag, Kind: ir.RuleSetRemote, Format: "binary",
		URL:          strings.NewReplacer("{kind}", kind, "{code}", code).Replace(orDefault(c.opts.GeoRuleSetURL, DefaultGeoRuleSetURL)),
		NeedsResolve: kind == "geoip", Source: src})
	return tag
}

// ---------- 规则 ----------

func (c *converter) rules(seq *yaml.Node) {
	c.cfg.Final = ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	if seq == nil {
		return
	}
	for _, item := range seq.Content {
		line := strings.TrimSpace(item.Value)
		src := c.src(item, line)
		typ := strings.ToUpper(strings.TrimSpace(strings.SplitN(line, ",", 2)[0]))
		if typ == "MATCH" || typ == "FINAL" {
			parts := strings.Split(line, ",")
			if len(parts) > 1 {
				c.cfg.Final = c.target(strings.TrimSpace(parts[1]), line, src)
			}
			continue
		}
		if typ == "SUB-RULE" || typ == "SCRIPT" {
			c.warn("rule", typ, ir.Unsupported, "子规则 / 脚本规则无对应", line, src)
			continue
		}
		body, target, params := SplitRuleLine(line)
		if target == "" {
			c.warn("rule", typ, ir.Unsupported, "格式无法解析", line, src)
			continue
		}
		noResolve := false
		for _, p := range params {
			if strings.EqualFold(strings.TrimSpace(p), "no-resolve") {
				noResolve = true
			}
		}
		val := ""
		if parts := strings.Split(body, ","); len(parts) > 1 {
			val = strings.TrimSpace(parts[1])
		}
		var m ir.Match
		switch typ {
		case "RULE-SET":
			rs := c.ruleSets[val]
			if rs == nil {
				c.warn("rule", typ, ir.Unsupported, "引用的规则集不可用", line, src)
				continue
			}
			m = ir.Leaf(ir.Cond{RuleSet: []string{rs.Tag}, NoResolve: noResolve})
		case "GEOIP":
			if strings.EqualFold(val, "LAN") {
				m = ir.Leaf(ir.Cond{IPPrivate: true, NoResolve: noResolve})
			} else {
				m = ir.Leaf(ir.Cond{RuleSet: []string{c.remoteSet("geoip", val, src)}, NoResolve: noResolve})
			}
		case "GEOSITE":
			m = ir.Leaf(ir.Cond{RuleSet: []string{c.remoteSet("geosite", val, src)}, NoResolve: true})
		default:
			node, err := ParseRuleNode(body)
			if err != nil {
				c.warn("rule", typ, ir.Unsupported, "无法解析："+err.Error(), line, src)
				continue
			}
			if node.Leaf != nil {
				node.Leaf.NoResolve = noResolve
			}
			mm, notes, err := node.ToMatch()
			if err != nil {
				c.warn("rule", typ, ir.Unsupported, err.Error(), line, src)
				continue
			}
			for _, n := range notes {
				c.warn("rule", typ, ir.Degraded, n, line, src)
			}
			m = mm
		}
		c.cfg.Rules = append(c.cfg.Rules, ir.Rule{Match: m, Action: c.target(target, line, src), Source: src})
	}
}

func mapTarget(t string) string {
	switch strings.ToUpper(t) {
	case "DIRECT":
		return ir.OutboundDirect
	case "REJECT", "REJECT-DROP", "REJECT-TINYGIF", "REJECT-NO-DROP":
		return ir.OutboundReject
	}
	return t
}

func (c *converter) target(t, line string, src ir.Source) ir.Action {
	switch strings.ToUpper(t) {
	case "DIRECT":
		return ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	case "REJECT", "REJECT-TINYGIF", "REJECT-NO-DROP":
		return ir.Action{Kind: ir.ActionReject}
	case "REJECT-DROP":
		return ir.Action{Kind: ir.ActionDrop}
	}
	if !c.tags[t] {
		c.warn("rule", "target", ir.Degraded, "目标策略「"+t+"」不存在，回落 DIRECT", line, src)
		return ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}
	}
	return ir.Action{Kind: ir.ActionRoute, Outbound: t}
}

// ---------- DNS ----------

var knownDNSFields = map[string]bool{"enable": true, "ipv6": true, "listen": true, "enhanced-mode": true, "fake-ip-range": true, "respect-rules": true,
	"use-hosts": true, "use-system-hosts": true, "fake-ip-filter": true, "default-nameserver": true, "proxy-server-nameserver": true,
	"nameserver": true, "nameserver-policy": true, "fallback": true, "fallback-filter": true}

func (c *converter) dns(node *yaml.Node) {
	if node == nil {
		return
	}
	var d obj
	_ = node.Decode(&d)
	src := c.src(node, "dns")
	field := func(k string) ir.Source {
		if n := mapGetKey(node, k); n != nil {
			return c.src(n, "dns."+k)
		}
		return src
	}
	for _, k := range sortedKeys(d) {
		if !knownDNSFields[k] {
			c.warn("dns", k, ir.Degraded, "字段未映射", "", field(k))
		}
	}
	D := &c.cfg.DNS
	n := 0
	bootstrap := ""
	for _, s := range strList(d["default-nameserver"]) {
		if sv, ok := dnsServer(s, fmt.Sprintf("bootstrap-%d", n), ""); ok {
			n++
			sv.Source = field("default-nameserver")
			D.Servers = append(D.Servers, sv)
			if bootstrap == "" {
				bootstrap = sv.Tag
			}
		}
	}
	// respect-rules：nameserver / nameserver-policy 的连接按路由规则走（mihomo 要求同时配置 proxy-server-nameserver，
	// 用它解析节点域名，否则「连节点要先解析、解析又要经节点」会形成循环）
	respect, _ := boolv(d, "respect-rules")
	if respect && len(strList(d["proxy-server-nameserver"])) == 0 {
		respect = false
		c.warn("dns", "respect-rules", ir.Degraded, "未配置 proxy-server-nameserver，respect-rules 不生效（否则节点域名的解析会依赖节点本身）", "", field("respect-rules"))
	}
	add := func(list []string, prefix, key string) []string {
		var tags []string
		for i, s := range list {
			// mihomo 写法：地址#出站名，该服务器固定经此出站
			addr, detour, _ := strings.Cut(s, "#")
			sv, ok := dnsServer(addr, fmt.Sprintf("%s-%d", prefix, i), bootstrap)
			if !ok {
				c.warn("dns", "server", ir.Unsupported, "无法解析的 DNS 地址格式", s, field(key))
				continue
			}
			sv.Source = field(key)
			switch detour = strings.TrimSpace(detour); {
			case detour == "" || strings.EqualFold(detour, ir.OutboundDirect):
			case c.tags[detour]:
				sv.Detour = detour
			default:
				c.warn("dns", "server", ir.Degraded, "指定的出站 "+detour+" 不存在，改为按默认方式连接", s, field(key))
			}
			if respect && (key == "nameserver" || key == "nameserver-policy") && sv.Detour == "" && sv.Type != "local" && sv.Type != "dhcp" {
				sv.RespectRules = true
			}
			D.Servers = append(D.Servers, sv)
			tags = append(tags, sv.Tag)
		}
		return tags
	}
	// race：同一位置写了多个上游时，与 mihomo 一样并发查询、取最先成功的应答（如 1.1.1.1 + doh.pub，国内直连时 doh.pub 先返回）
	race := func(tags []string, tag, key string) string {
		if len(tags) <= 1 {
			return strings.Join(tags, "")
		}
		D.Servers = append(D.Servers, ir.DNSServer{Tag: tag, Type: "race", Members: tags, Source: field(key)})
		return tag
	}
	main := race(add(strList(d["nameserver"]), "ns", "nameserver"), "ns", "nameserver")
	if main != "" {
		D.Final = main
	}
	if ps := race(add(strList(d["proxy-server-nameserver"]), "psns", "proxy-server-nameserver"), "psns", "proxy-server-nameserver"); ps != "" {
		D.OutboundResolver = ps
	}
	if len(strList(d["fallback"])) > 0 || d["fallback-filter"] != nil {
		c.warn("dns", "fallback", ir.Unsupported, "fallback / fallback-filter 无对应", "", field("fallback"))
	}
	if polNode := mapGet(node, "nameserver-policy"); polNode != nil {
		for i := 0; i+1 < len(polNode.Content); i += 2 {
			k := polNode.Content[i].Value
			var v any
			_ = polNode.Content[i+1].Decode(&v)
			vals := strList(v)
			psrc := c.src(polNode.Content[i], "nameserver-policy "+k)
			if len(vals) == 0 {
				continue
			}
			ptag := race(add(vals, fmt.Sprintf("policy-%d", i/2), "nameserver-policy"), fmt.Sprintf("policy-%d", i/2), "nameserver-policy")
			if ptag == "" {
				continue
			}
			conds, ok := c.domainPatterns(strings.Split(k, ","), psrc)
			if !ok {
				c.warn("dns", "nameserver-policy", ir.Unsupported, "域名模式无法转换", k, psrc)
				continue
			}
			for _, cond := range conds {
				D.Rules = append(D.Rules, ir.DNSRule{Match: ir.Leaf(cond), Server: ptag, Source: psrc})
			}
		}
	}
	if str(d, "enhanced-mode") == "fake-ip" {
		r := orDefault(str(d, "fake-ip-range"), "198.18.0.1/16")
		if pf, err := netip.ParsePrefix(r); err == nil {
			r = pf.Masked().String()
		}
		D.FakeIP = &ir.FakeIP{Inet4Range: r}
		if f := strList(d["fake-ip-filter"]); len(f) > 0 && main != "" {
			fsrc := field("fake-ip-filter")
			if conds, ok := c.domainPatterns(f, fsrc); ok {
				for _, cond := range conds {
					D.Rules = append(D.Rules, ir.DNSRule{Match: ir.Leaf(cond), Server: main, Source: fsrc})
				}
			} else {
				c.warn("dns", "fake-ip-filter", ir.Degraded, "部分模式无法转换", "", fsrc)
			}
		}
	}
	if b, ok := boolv(d, "use-system-hosts"); ok && !b {
		D.NoSystemHosts = true
	}
	if b, ok := boolv(d, "ipv6"); ok && !b {
		D.Strategy = "ipv4_only"
	}
}

func dnsServer(s, tag, resolver string) (ir.DNSServer, bool) {
	s = strings.TrimSpace(s)
	if s == "system" || s == "system://" {
		return ir.DNSServer{Tag: tag, Type: "local"}, true
	}
	if strings.HasPrefix(s, "dhcp://") {
		return ir.DNSServer{Tag: tag, Type: "dhcp"}, true
	}
	if !strings.Contains(s, "://") {
		s = "udp://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return ir.DNSServer{}, false
	}
	typ := map[string]string{"udp": "udp", "tcp": "tcp", "tls": "tls", "https": "https", "quic": "quic", "h3": "h3"}[u.Scheme]
	if typ == "" {
		return ir.DNSServer{}, false
	}
	sv := ir.DNSServer{Tag: tag, Type: typ, Address: u.Hostname()}
	if p := u.Port(); p != "" {
		sv.Port, _ = strconv.Atoi(p)
	}
	if (typ == "https" || typ == "h3") && u.Path != "" && u.Path != "/dns-query" {
		sv.Path = u.Path
	}
	if _, err := netip.ParseAddr(u.Hostname()); err != nil {
		sv.Resolver = resolver
	}
	return sv, true
}

// domainPatterns 把 Clash 域名模式（+.x / *.x / .x / x / geosite:x / rule-set:x）转换为 Cond 列表（彼此为「或」）。
// 规则集引用单独成一个 Cond：sing-box 中 rule_set 与同一规则内其他地址条件的组合语义取决于
// 规则集内部结构（可合并时为「或」，否则为「与」），因此 IR 约定二者不混放。
func (c *converter) domainPatterns(patterns []string, src ir.Source) ([]ir.Cond, bool) {
	var dom, sets ir.Cond
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case strings.HasPrefix(p, "geosite:"):
			sets.RuleSet = append(sets.RuleSet, c.remoteSet("geosite", strings.TrimPrefix(p, "geosite:"), src))
		case strings.HasPrefix(p, "rule-set:"):
			rs := c.ruleSets[strings.TrimPrefix(p, "rule-set:")]
			if rs == nil {
				return nil, false
			}
			sets.RuleSet = append(sets.RuleSet, rs.Tag)
		case strings.HasPrefix(p, "+."):
			dom.DomainSuffix = append(dom.DomainSuffix, p[2:])
		case strings.HasPrefix(p, "*.") && !strings.ContainsAny(p[2:], "*?"):
			dom.DomainRegex = append(dom.DomainRegex, `^[^.]+\.`+regexp.QuoteMeta(p[2:])+`$`)
		case strings.HasPrefix(p, "."):
			dom.DomainSuffix = append(dom.DomainSuffix, p)
		case strings.ContainsAny(p, "*?"):
			dom.DomainRegex = append(dom.DomainRegex, GlobToRegex(p))
		default:
			dom.Domain = append(dom.Domain, p)
		}
	}
	var out []ir.Cond
	if len(dom.Domain)+len(dom.DomainSuffix)+len(dom.DomainRegex) > 0 {
		dom.NoResolve = true
		out = append(out, dom)
	}
	if len(sets.RuleSet) > 0 {
		sets.NoResolve = true
		out = append(out, sets)
	}
	return out, len(out) > 0
}

// ---------- helpers ----------

func compileFilters(s string) ([]*regexp.Regexp, error) {
	if s == "" {
		return nil, nil
	}
	var out []*regexp.Regexp
	for _, part := range strings.Split(s, "`") { // mihomo 支持用反引号分隔多个正则
		re, err := regexp.Compile(part)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

func matchFilters(name string, include, exclude []*regexp.Regexp) bool {
	if len(include) > 0 {
		ok := false
		for _, re := range include {
			if re.MatchString(name) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, re := range exclude {
		if re.MatchString(name) {
			return false
		}
	}
	return true
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapGetKey(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return nil
}

func str(m obj, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprint(v)
		}
	}
	return ""
}

func intv(m obj, k string) (int, bool) {
	switch t := m[k].(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

func boolv(m obj, k string) (bool, bool) {
	switch t := m[k].(type) {
	case bool:
		return t, true
	case string:
		b, err := strconv.ParseBool(t)
		return b, err == nil
	}
	return false, false
}

func sub(m obj, k string) obj {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

func strList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
