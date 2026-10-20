package main

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type provider struct {
	Name     string
	Behavior string
	Format   string
	Path     string // 本地缓存文件
	Entries  []Entry
	Result   ruleSetResult
	OutFile  string
	Missing  bool
}

type inlineRule struct {
	Node     *RuleNode
	Headless obj
}

type Converted struct {
	Config    obj
	Providers []*provider
	Inline    []inlineRule
	tags      map[string]bool

	nodeObj       map[string]obj    // 可克隆的普通出站（relay 用）
	nodeType      map[string]string // tag -> Clash 协议类型
	proxyNodes    []string          // proxies 段节点（顺序）
	providerNodes map[string][]string
}

func (c *Converted) addNode(cp *convertedProxy, typ string, outbounds, endpoints *[]any) string {
	tag := cp.tag
	for i := 2; c.tags[tag]; i++ {
		tag = fmt.Sprintf("%s #%d", cp.tag, i)
	}
	if cp.endpoint != nil {
		cp.endpoint["tag"] = tag
		*endpoints = append(*endpoints, cp.endpoint)
	} else {
		cp.outbound["tag"] = tag
		*outbounds = append(*outbounds, cp.outbound)
		c.nodeObj[tag] = cp.outbound
	}
	for _, e := range cp.extra {
		*outbounds = append(*outbounds, e)
	}
	c.tags[tag] = true
	c.nodeType[tag] = typ
	return tag
}

func resolvePath(p string, dirs ...string) string {
	if filepath.IsAbs(p) {
		return p
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, p)); err == nil {
			return filepath.Join(d, p)
		}
	}
	return filepath.Join(dirs[0], p)
}

func convert(doc obj, cfgDir, baseDir, outDir string, fs *Findings) (*Converted, error) {
	c := &Converted{tags: map[string]bool{"direct": true, "block": true},
		nodeObj: map[string]obj{}, nodeType: map[string]string{}, providerNodes: map[string][]string{}}
	outbounds := []any{obj{"type": "direct", "tag": "direct"}, obj{"type": "block", "tag": "block"}}
	var endpoints []any

	// ---- 全局 ----
	for _, k := range sortedKeys(doc) {
		switch k {
		case "proxies", "proxy-groups", "rules", "rule-providers", "dns", "proxy-providers":
		case "mode":
			if m := str(doc, "mode"); !strings.EqualFold(m, "rule") {
				fs.add("全局", "mode="+m, Degraded, "非 rule 模式由 Velum 切换 final 实现", "")
			} else {
				fs.add("全局", "mode", Supported, "", "")
			}
		case "ipv6":
			fs.add("全局", "ipv6", Supported, "映射为 DNS strategy", "")
		default:
			fs.add("全局", k, Degraded, "顶层字段未映射", "")
		}
	}

	// ---- 节点 ----
	for _, x := range asList(doc["proxies"]) {
		p, ok := x.(map[string]any)
		if !ok {
			continue
		}
		cp, ok := convertProxy(p, fs)
		if !ok {
			continue
		}
		c.proxyNodes = append(c.proxyNodes, c.addNode(cp, str(p, "type"), &outbounds, &endpoints))
	}

	// ---- proxy-providers：Velum 拉取后在编译期展开为普通节点 ----
	pps, _ := doc["proxy-providers"].(map[string]any)
	for _, pname := range sortedKeys(pps) {
		pp, _ := pps[pname].(map[string]any)
		path := resolvePath(orDefault(str(pp, "path"), "./proxy_providers/"+pname+".yaml"), cfgDir, baseDir)
		data, err := os.ReadFile(path)
		if err != nil {
			fs.add("节点源", str(pp, "type"), Unsupported, "本地无缓存文件（生产版由 Velum 拉取）", pname)
			continue
		}
		var pdoc obj
		if err := yaml.Unmarshal(data, &pdoc); err != nil {
			fs.add("节点源", str(pp, "type"), Unsupported, "provider 内容无法解析", pname)
			continue
		}
		filter, ferr := compileFilters(str(pp, "filter"))
		exclude, eerr := compileFilters(str(pp, "exclude-filter"))
		if ferr != nil || eerr != nil {
			fs.add("节点源", "filter", Degraded, "filter 正则含 Go RE2 不支持的语法，已忽略过滤", pname)
		}
		if sub(pp, "override") != nil {
			fs.add("节点源", "override", Degraded, "override 未实现", pname)
		}
		n := 0
		for _, x := range asList(pdoc["proxies"]) {
			p, ok := x.(map[string]any)
			if !ok || !matchFilters(str(p, "name"), filter, exclude) {
				continue
			}
			cp, ok := convertProxy(p, fs)
			if !ok {
				continue
			}
			c.providerNodes[pname] = append(c.providerNodes[pname], c.addNode(cp, str(p, "type"), &outbounds, &endpoints))
			n++
		}
		fs.add("节点源", str(pp, "type"), Supported, fmt.Sprintf("编译期展开为 %d 个节点", n), pname)
	}

	// ---- 策略组（先登记名字，允许前向引用）----
	groups := asList(doc["proxy-groups"])
	for _, x := range groups {
		if g, ok := x.(map[string]any); ok {
			c.tags[str(g, "name")] = true
		}
	}
	for _, x := range groups {
		g, ok := x.(map[string]any)
		if !ok {
			continue
		}
		if os := c.convertGroup(g, fs); len(os) > 0 {
			outbounds = append(outbounds, os...)
		} else {
			delete(c.tags, str(g, "name"))
		}
	}

	// ---- 规则集 ----
	rpDir := filepath.Join(outDir, "rules")
	_ = os.MkdirAll(rpDir, 0o755)
	var ruleSets []any
	provByName := map[string]*provider{}
	rps, _ := doc["rule-providers"].(map[string]any)
	for _, name := range sortedKeys(rps) {
		rp, _ := rps[name].(map[string]any)
		pv := &provider{Name: name, Behavior: str(rp, "behavior"), Format: str(rp, "format")}
		if pth := str(rp, "path"); pth != "" {
			if !filepath.IsAbs(pth) {
				pth = filepath.Join(baseDir, pth)
			}
			pv.Path = pth
		}
		data, err := os.ReadFile(pv.Path)
		if err != nil {
			pv.Missing = true
			fs.add("规则集", pv.Behavior, Unsupported, "本地无缓存文件（生产版由 Velum 拉取）", name)
		} else {
			pv.Entries = parseProvider(data, pv.Behavior, pv.Format)
			pv.Result = convertEntries(pv.Entries)
			pv.OutFile = filepath.Join(rpDir, fmt.Sprintf("rs-%03d.json", len(c.Providers)))
			if err := writeJSON(pv.OutFile, obj{"version": 3, "rules": pv.Result.Rules}); err != nil {
				return nil, err
			}
			ruleSets = append(ruleSets, obj{"type": "local", "tag": "rs:" + name, "format": "source", "path": pv.OutFile})
			st := Supported
			reason := ""
			if len(pv.Result.Unsupported) > 0 {
				st, reason = Degraded, fmt.Sprintf("含不支持的规则类型 %v", pv.Result.Unsupported)
			}
			fs.add("规则集", pv.Behavior+"/"+orDefault(pv.Format, "yaml"), st, reason, name)
		}
		provByName[name] = pv
		c.Providers = append(c.Providers, pv)
	}

	// ---- 规则 ----
	routeRules := []any{obj{"action": "sniff"}, obj{"protocol": "dns", "action": "hijack-dns"}}
	resolved := false
	final := "direct"
	remoteSets := map[string]bool{}
	for _, x := range asList(doc["rules"]) {
		line := strings.TrimSpace(fmt.Sprint(x))
		typ := strings.ToUpper(strings.TrimSpace(strings.SplitN(line, ",", 2)[0]))
		if typ == "MATCH" || typ == "FINAL" {
			parts := strings.Split(line, ",")
			if len(parts) > 1 {
				final = mapTarget(strings.TrimSpace(parts[1]), c.tags)
			}
			fs.add("规则", typ, Supported, "映射为 route.final", "")
			continue
		}
		if typ == "SUB-RULE" || typ == "SCRIPT" {
			fs.add("规则", typ, Unsupported, "子规则 / 脚本规则无对应", line)
			continue
		}
		body, target, params := splitRuleLine(line)
		if target == "" {
			fs.add("规则", typ, Unsupported, "格式无法解析", line)
			continue
		}
		val := ""
		if parts := strings.Split(body, ","); len(parts) > 1 {
			val = strings.TrimSpace(parts[1])
		}
		noResolve := false
		for _, p := range params {
			if strings.EqualFold(strings.TrimSpace(p), "no-resolve") {
				noResolve = true
			}
		}
		cond := obj{}
		needResolve := false
		st, reason := Supported, ""
		switch typ {
		case "RULE-SET":
			pv := provByName[val]
			if pv == nil || pv.Missing {
				fs.add("规则", typ, Unsupported, "引用的规则集不可用", line)
				continue
			}
			cond["rule_set"] = []string{"rs:" + val}
			needResolve = !noResolve && (pv.Behavior == "ipcidr" || pv.Result.NeedsResolve)
		case "GEOIP":
			if strings.EqualFold(val, "LAN") {
				cond["ip_is_private"] = true
			} else {
				tag := "geoip-" + strings.ToLower(val)
				cond["rule_set"] = []string{tag}
				remoteSets[tag] = true
				reason = "GeoIP 转为远程 rule_set（.srs）"
			}
			needResolve = !noResolve
		case "GEOSITE":
			tag := "geosite-" + strings.ToLower(val)
			cond["rule_set"] = []string{tag}
			remoteSets[tag] = true
			reason = "GeoSite 转为远程 rule_set（.srs）"
		default:
			node, err := parseRuleNode(body)
			if err != nil {
				fs.add("规则", typ, Unsupported, "无法解析："+err.Error(), line)
				continue
			}
			if node.Leaf != nil {
				node.Leaf.NoResolve = noResolve
			}
			h, notes := node.toHeadless()
			if h == nil {
				fs.add("规则", typ, Unsupported, strings.Join(notes, "；"), line)
				continue
			}
			for k, v := range h {
				cond[k] = v
			}
			needResolve = node.hasIPLeafNeedingResolve()
			if len(notes) > 0 {
				st, reason = Degraded, strings.Join(notes, "；")
			}
			c.Inline = append(c.Inline, inlineRule{node, h})
		}
		if needResolve && !resolved {
			routeRules = append(routeRules, obj{"action": "resolve"})
			resolved = true
			fs.add("规则", "resolve 插入", Supported, "首条需解析的 IP 规则前插入 resolve，模拟 Clash 按需解析", line)
		}
		applyTarget(cond, target, c.tags, fs, line)
		routeRules = append(routeRules, cond)
		fs.add("规则", typ, st, reason, line)
	}
	dns, resolver := convertDNS(sub(doc, "dns"), func(tag string) { remoteSets[tag] = true }, fs)
	for _, tag := range sortedKeys(remoteSets) {
		kind, code, _ := strings.Cut(tag, "-")
		ruleSets = append(ruleSets, obj{"type": "remote", "tag": tag, "format": "binary",
			"url":             fmt.Sprintf("https://raw.githubusercontent.com/SagerNet/sing-%s/rule-set/%s-%s.srs", kind, kind, code),
			"download_detour": "direct"})
	}

	route := obj{"rules": routeRules, "rule_set": ruleSets, "final": final, "auto_detect_interface": true}
	if resolver != "" {
		route["default_domain_resolver"] = resolver
	}
	cfg := obj{
		"log":       obj{"level": "warn"},
		"dns":       dns,
		"inbounds":  []any{obj{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 17890}},
		"outbounds": outbounds,
		"route":     route,
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}
	c.Config = cfg
	return c, nil
}

func (c *Converted) convertGroup(g obj, fs *Findings) []any {
	name, typ := str(g, "name"), str(g, "type")
	var members []string
	seen := map[string]bool{}
	addMember := func(t string) {
		if !seen[t] {
			seen[t] = true
			members = append(members, t)
		}
	}
	for _, m := range strList(g["proxies"]) {
		t := mapTarget(m, c.tags)
		if !c.tags[t] {
			fs.add("策略组", typ, Degraded, "成员引用的节点 / 组已被丢弃", name)
			continue
		}
		addMember(t)
	}
	// 动态成员：use / include-all*，再经 filter / exclude-filter / exclude-type 过滤。
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
	hasDynamic := g["use"] != nil || g["include-all"] != nil || g["include-all-proxies"] != nil || g["include-all-providers"] != nil
	if hasDynamic {
		filter, ferr := compileFilters(str(g, "filter"))
		exclude, eerr := compileFilters(str(g, "exclude-filter"))
		if ferr != nil || eerr != nil {
			fs.add("策略组", "filter", Degraded, "filter 正则含 Go RE2 不支持的语法，已忽略过滤", name)
		}
		exTypes := map[string]bool{}
		for _, t := range strings.Split(str(g, "exclude-type"), "|") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				exTypes[t] = true
			}
		}
		n := 0
		for _, t := range dynamic {
			if !matchFilters(t, filter, exclude) || exTypes[strings.ToLower(mihomoAdapterType[c.nodeType[t]])] {
				continue
			}
			addMember(t)
			n++
		}
		fs.add("策略组", typ+"+动态成员", Supported, fmt.Sprintf("编译期展开 %d 个成员（订阅更新后重新编译）", n), name)
	}
	if _, ok := g["ssid-policy"]; ok {
		fs.add("策略组", "ssid-policy", Degraded, "Stash 私有：按 Wi-Fi 切换，需由 NetworkSense 场景触发实现，sing-box 无对应", name)
	}
	known := map[string]bool{"name": true, "type": true, "proxies": true, "url": true, "benchmark-url": true, "interval": true,
		"tolerance": true, "lazy": true, "timeout": true, "benchmark-timeout": true, "hidden": true, "icon": true, "ssid-policy": true,
		"use": true, "filter": true, "exclude-filter": true, "include-all": true, "include-all-proxies": true, "include-all-providers": true,
		"exclude-type": true, "strategy": true, "max-failed-times": true, "expected-status": true, "disable-udp": true}
	for _, k := range sortedKeys(g) {
		if !known[k] {
			fs.add("策略组", typ, Degraded, "字段 "+k+" 未映射", name)
		}
	}
	if len(members) == 0 {
		fs.add("策略组", typ, Unsupported, "无可用成员", name)
		return nil
	}
	o := obj{"tag": name, "outbounds": members}
	switch typ {
	case "select":
		o["type"] = "selector"
		fs.add("策略组", typ, Supported, "", name)
	case "url-test", "fallback", "load-balance":
		o["type"] = "urltest"
		if u := str(g, "url", "benchmark-url"); u != "" {
			o["url"] = u
		}
		if n, ok := intv(g, "interval"); ok {
			o["interval"] = fmt.Sprintf("%ds", n)
		}
		if n, ok := intv(g, "tolerance"); ok {
			o["tolerance"] = n
		}
		switch typ {
		case "url-test":
			fs.add("策略组", typ, Supported, "", name)
		case "fallback":
			fs.add("策略组", typ, Degraded, "降级为 urltest：按延迟而非顺序选择", name)
		default:
			fs.add("策略组", typ, Degraded, "降级为 urltest：无负载均衡（strategy="+orDefault(str(g, "strategy"), "consistent-hashing")+"）", name)
		}
	case "relay":
		return c.convertRelay(name, members, fs)
	default:
		fs.add("策略组", typ, Unsupported, "类型无对应", name)
		return nil
	}
	return []any{o}
}

// convertRelay：relay [A, B, C] 表示 本机 → A → B → C → 目标。
// sing-box 用 detour 表达：克隆 B、C，令 B'.detour = A，C'.detour = B'，C' 即为该组。
// 第一跳可以是任意出站（含策略组）；后续各跳必须是可克隆的普通节点。
func (c *Converted) convertRelay(name string, members []string, fs *Findings) []any {
	if len(members) < 2 {
		fs.add("策略组", "relay", Unsupported, "relay 至少需要两跳", name)
		return nil
	}
	var out []any
	prev := members[0]
	for i, m := range members[1:] {
		base, ok := c.nodeObj[m]
		if !ok {
			fs.add("策略组", "relay", Unsupported, "非首跳成员是策略组或 endpoint，无法挂 detour", name)
			return nil
		}
		clone := obj{}
		for k, v := range base {
			clone[k] = v
		}
		tag := fmt.Sprintf("%s/hop%d", name, i+2)
		if i == len(members)-2 {
			tag = name
		}
		clone["tag"] = tag
		clone["detour"] = prev
		out = append(out, clone)
		prev = tag
	}
	fs.add("策略组", "relay", Supported, "改写为 detour 链（克隆后续各跳节点）", name)
	return out
}

// mihomoAdapterType：exclude-type 使用 mihomo 内部适配器类型名，而非 YAML 中的 type。
var mihomoAdapterType = map[string]string{
	"ss": "Shadowsocks", "ssr": "ShadowsocksR", "vmess": "Vmess", "vless": "Vless", "trojan": "Trojan",
	"hysteria": "Hysteria", "hysteria2": "Hysteria2", "tuic": "Tuic", "socks5": "Socks5", "http": "Http",
	"wireguard": "WireGuard", "snell": "Snell", "anytls": "AnyTLS", "ssh": "Ssh",
}

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

func mapTarget(t string, tags map[string]bool) string {
	switch strings.ToUpper(t) {
	case "DIRECT":
		return "direct"
	case "REJECT", "REJECT-DROP", "REJECT-TINYGIF", "REJECT-NO-DROP":
		return "block"
	}
	return t
}

func applyTarget(cond obj, target string, tags map[string]bool, fs *Findings, line string) {
	switch strings.ToUpper(target) {
	case "DIRECT":
		cond["outbound"] = "direct"
	case "REJECT", "REJECT-TINYGIF", "REJECT-NO-DROP":
		cond["action"] = "reject"
	case "REJECT-DROP":
		cond["action"] = "reject"
		cond["method"] = "drop"
	default:
		if !tags[target] {
			fs.add("规则", "target", Degraded, "目标策略不存在，回落 direct", line)
			cond["outbound"] = "direct"
			return
		}
		cond["outbound"] = target
	}
}

// ---------- DNS ----------

func convertDNS(d obj, addSet func(string), fs *Findings) (obj, string) {
	if d == nil {
		return obj{}, ""
	}
	var servers []any
	var rules []any
	n := 0
	known := map[string]bool{"enable": true, "ipv6": true, "listen": true, "enhanced-mode": true, "fake-ip-range": true, "respect-rules": true,
		"use-hosts": true, "use-system-hosts": true, "fake-ip-filter": true, "default-nameserver": true, "proxy-server-nameserver": true,
		"nameserver": true, "nameserver-policy": true, "fallback": true, "fallback-filter": true}
	for _, k := range sortedKeys(d) {
		if !known[k] {
			fs.add("DNS", k, Degraded, "字段未映射", "")
		}
	}
	// bootstrap：用于解析 DoH / DoT 的服务器域名
	bootstrap := ""
	for _, s := range strList(d["default-nameserver"]) {
		sv, ok := dnsServer(s, fmt.Sprintf("bootstrap-%d", n), "")
		if ok {
			n++
			servers = append(servers, sv)
			if bootstrap == "" {
				bootstrap = sv["tag"].(string)
			}
		}
	}
	fs.add("DNS", "default-nameserver", Supported, "映射为 DoH/DoT 的 domain_resolver", "")
	add := func(list []string, prefix string) []string {
		var tags []string
		for i, s := range list {
			sv, ok := dnsServer(s, fmt.Sprintf("%s-%d", prefix, i), bootstrap)
			if !ok {
				fs.add("DNS", "server", Unsupported, "无法解析的 DNS 地址格式", s)
				continue
			}
			servers = append(servers, sv)
			tags = append(tags, sv["tag"].(string))
		}
		return tags
	}
	main := add(strList(d["nameserver"]), "ns")
	if len(main) > 1 {
		fs.add("DNS", "nameserver", Degraded, "多上游并发竞速无对应，仅使用首个", "")
	} else {
		fs.add("DNS", "nameserver", Supported, "", "")
	}
	resolver := ""
	if ps := add(strList(d["proxy-server-nameserver"]), "psns"); len(ps) > 0 {
		resolver = ps[0]
		fs.add("DNS", "proxy-server-nameserver", Supported, "映射为 route.default_domain_resolver", "")
	}
	if len(strList(d["fallback"])) > 0 || d["fallback-filter"] != nil {
		fs.add("DNS", "fallback/fallback-filter", Unsupported, "无对应，需改写为 DNS 规则", "")
	}
	// nameserver-policy
	if pol, ok := d["nameserver-policy"].(map[string]any); ok {
		i := 0
		for _, k := range sortedKeys(pol) {
			vals := strList(pol[k])
			if len(vals) == 0 {
				continue
			}
			tags := add(vals[:1], fmt.Sprintf("policy-%d", i))
			i++
			if len(tags) == 0 {
				continue
			}
			r, ok := domainPatternRule(strings.Split(k, ","), addSet)
			if !ok {
				fs.add("DNS", "nameserver-policy", Unsupported, "域名模式无法转换", k)
				continue
			}
			r["server"] = tags[0]
			rules = append(rules, r)
		}
		fs.add("DNS", "nameserver-policy", Supported, "映射为 DNS 规则", "")
	}
	// fake-ip
	if str(d, "enhanced-mode") == "fake-ip" {
		rangeV4 := orDefault(str(d, "fake-ip-range"), "198.18.0.1/16")
		pf, err := netip.ParsePrefix(rangeV4)
		if err == nil {
			rangeV4 = pf.Masked().String()
		}
		servers = append(servers, obj{"type": "fakeip", "tag": "fakeip", "inet4_range": rangeV4})
		if f := strList(d["fake-ip-filter"]); len(f) > 0 && len(main) > 0 {
			r, ok := domainPatternRule(f, addSet)
			if ok {
				r["server"] = main[0]
				rules = append(rules, r)
				fs.add("DNS", "fake-ip-filter", Supported, "映射为优先于 fakeip 的 DNS 规则", "")
			} else {
				fs.add("DNS", "fake-ip-filter", Degraded, "部分模式无法转换", "")
			}
		}
		rules = append(rules, obj{"query_type": []string{"A", "AAAA"}, "server": "fakeip"})
		fs.add("DNS", "enhanced-mode=fake-ip", Supported, "", "")
	}
	if b, _ := boolv(d, "respect-rules"); b {
		fs.add("DNS", "respect-rules", Degraded, "DNS 查询按路由规则走代理：sing-box 只能按服务器设置 detour", "")
	}
	if b, _ := boolv(d, "use-system-hosts"); b {
		fs.add("DNS", "use-system-hosts", Degraded, "交由 Velum DNSPlan 统一处理 hosts（Overlay）", "")
	}
	if _, ok := d["listen"]; ok {
		fs.add("DNS", "listen", Supported, "由 Velum 自行决定 DNS 入口（TUN 劫持）", "")
	}
	strategy := ""
	if b, ok := boolv(d, "ipv6"); ok && !b {
		strategy = "ipv4_only"
	}
	out := obj{"servers": servers, "rules": rules}
	if len(main) > 0 {
		out["final"] = main[0]
	}
	if strategy != "" {
		out["strategy"] = strategy
	}
	return out, resolver
}

func dnsServer(s, tag, resolver string) (obj, bool) {
	s = strings.TrimSpace(s)
	if s == "system" || s == "system://" {
		return obj{"type": "local", "tag": tag}, true
	}
	if s == "dhcp://system" || strings.HasPrefix(s, "dhcp://") {
		return obj{"type": "dhcp", "tag": tag}, true
	}
	if !strings.Contains(s, "://") {
		s = "udp://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	typ := map[string]string{"udp": "udp", "tcp": "tcp", "tls": "tls", "https": "https", "quic": "quic", "h3": "h3"}[u.Scheme]
	if typ == "" {
		return nil, false
	}
	o := obj{"type": typ, "tag": tag, "server": u.Hostname()}
	if p := u.Port(); p != "" {
		var port int
		fmt.Sscan(p, &port)
		o["server_port"] = port
	}
	if typ == "https" || typ == "h3" {
		if u.Path != "" && u.Path != "/dns-query" {
			o["path"] = u.Path
		}
	}
	if _, err := netip.ParseAddr(u.Hostname()); err != nil && resolver != "" {
		o["domain_resolver"] = resolver
	}
	return o, true
}

func domainPatternRule(patterns []string, addSet func(string)) (obj, bool) {
	r := obj{}
	ok := true
	appendTo := func(k, v string) { r[k] = append(asStrings(r[k]), v) }
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case strings.HasPrefix(p, "geosite:"):
			tag := "geosite-" + strings.ToLower(strings.TrimPrefix(p, "geosite:"))
			addSet(tag)
			appendTo("rule_set", tag)
		case strings.HasPrefix(p, "rule-set:"):
			appendTo("rule_set", "rs:"+strings.TrimPrefix(p, "rule-set:"))
		case strings.HasPrefix(p, "+."):
			appendTo("domain_suffix", p[2:])
		case strings.HasPrefix(p, "*.") && !strings.Contains(p[2:], "*"):
			appendTo("domain_regex", `^[^.]+\.`+strings.ReplaceAll(p[2:], ".", `\.`)+`$`)
		case strings.HasPrefix(p, "."):
			appendTo("domain_suffix", p)
		case strings.ContainsAny(p, "*?"):
			appendTo("domain_regex", globToRegex(p))
		default:
			appendTo("domain", p)
		}
	}
	return r, ok && len(r) > 0
}

func asStrings(v any) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	return nil
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
