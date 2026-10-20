package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry 是规则集里的一条原始规则（已统一为 classical 表示）。
type Entry struct {
	Type      string // DOMAIN / DOMAIN-SUFFIX / DOMAIN-SUBONLY / DOMAIN-SINGLE / DOMAIN-KEYWORD / DOMAIN-WILDCARD / DOMAIN-REGEX / IP-CIDR / IP-CIDR6 / PROCESS-NAME / ...
	Value     string
	NoResolve bool
	Raw       string
}

func isIPType(t string) bool { return t == "IP-CIDR" || t == "IP-CIDR6" }

// parseProvider 读取 Clash rule-provider（text / yaml，domain / ipcidr / classical）。
func parseProvider(data []byte, behavior, format string) []Entry {
	var lines []string
	if format == "yaml" || (format == "" && bytes.Contains(data, []byte("payload:"))) {
		var doc struct {
			Payload []string `yaml:"payload"`
		}
		_ = yaml.Unmarshal(data, &doc)
		lines = doc.Payload
	} else {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
	}
	var out []Entry
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//") {
			continue
		}
		l = strings.Trim(l, "'\"")
		switch behavior {
		case "domain":
			out = append(out, domainTextEntry(l))
		case "ipcidr":
			t := "IP-CIDR"
			if strings.Contains(l, ":") {
				t = "IP-CIDR6"
			}
			out = append(out, Entry{Type: t, Value: l, Raw: l})
		default: // classical
			parts := strings.Split(l, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			e := Entry{Type: strings.ToUpper(parts[0]), Raw: l}
			if len(parts) > 1 {
				e.Value = parts[1]
			}
			for _, p := range parts[2:] {
				if strings.EqualFold(p, "no-resolve") {
					e.NoResolve = true
				}
			}
			out = append(out, e)
		}
	}
	return out
}

// Clash text 域名格式：+.x 根域+子域；.x 仅子域；*.x 单级子域；含 * 的其他写法按通配；否则精确。
func domainTextEntry(l string) Entry {
	switch {
	case strings.HasPrefix(l, "+."):
		return Entry{Type: "DOMAIN-SUFFIX", Value: l[2:], Raw: l}
	case strings.HasPrefix(l, "*.") && !strings.Contains(l[2:], "*"):
		return Entry{Type: "DOMAIN-SINGLE", Value: l[2:], Raw: l}
	case strings.HasPrefix(l, "."):
		return Entry{Type: "DOMAIN-SUBONLY", Value: l[1:], Raw: l}
	case strings.ContainsAny(l, "*?"):
		return Entry{Type: "DOMAIN-WILDCARD", Value: l, Raw: l}
	default:
		return Entry{Type: "DOMAIN", Value: l, Raw: l}
	}
}

// ---------- 转换为 sing-box headless rules ----------

type ruleSetResult struct {
	Rules        []obj
	Counts       map[string]int // 按 Clash 类型统计
	Unsupported  map[string]int
	Degraded     map[string]int // 类型 -> 条数（如 wildcard→regex）
	NeedsResolve bool           // 含未标 no-resolve 的 IP 规则
	HasProcess   bool
}

// mutate 仅用于自检探测框架能否发现语义错误（-mutate 参数）。
var mutate string

func convertEntries(entries []Entry) ruleSetResult {
	res := ruleSetResult{Counts: map[string]int{}, Unsupported: map[string]int{}, Degraded: map[string]int{}}
	addr := map[string][]string{}
	var procName, procPath, srcIP, network []string
	var ports []int
	for _, e := range entries {
		res.Counts[e.Type]++
		v := strings.ToLower(e.Value)
		switch e.Type {
		case "DOMAIN":
			addr["domain"] = append(addr["domain"], v)
		case "DOMAIN-SUFFIX":
			if mutate == "suffix-subonly" {
				addr["domain_suffix"] = append(addr["domain_suffix"], "."+strings.TrimPrefix(v, "."))
				continue
			}
			addr["domain_suffix"] = append(addr["domain_suffix"], strings.TrimPrefix(v, "."))
		case "DOMAIN-SUBONLY":
			addr["domain_suffix"] = append(addr["domain_suffix"], "."+v)
		case "DOMAIN-SINGLE":
			addr["domain_regex"] = append(addr["domain_regex"], `^[^.]+\.`+regexp.QuoteMeta(v)+`$`)
			res.Degraded[e.Type]++
		case "DOMAIN-KEYWORD":
			addr["domain_keyword"] = append(addr["domain_keyword"], v)
		case "DOMAIN-WILDCARD":
			addr["domain_regex"] = append(addr["domain_regex"], globToRegex(v))
			res.Degraded[e.Type]++
		case "DOMAIN-REGEX":
			addr["domain_regex"] = append(addr["domain_regex"], e.Value)
		case "IP-CIDR", "IP-CIDR6":
			addr["ip_cidr"] = append(addr["ip_cidr"], e.Value)
			if !e.NoResolve {
				res.NeedsResolve = true
			}
		case "PROCESS-NAME":
			procName = append(procName, e.Value)
			res.HasProcess = true
		case "PROCESS-PATH":
			procPath = append(procPath, e.Value)
			res.HasProcess = true
		case "DST-PORT":
			var p int
			if _, err := fmt.Sscan(e.Value, &p); err == nil {
				ports = append(ports, p)
			} else {
				res.Unsupported[e.Type]++
			}
		case "SRC-IP-CIDR":
			srcIP = append(srcIP, e.Value)
		case "NETWORK":
			network = append(network, strings.ToLower(e.Value))
		default:
			res.Unsupported[e.Type]++
		}
	}
	// 关键：sing-box 同一条 rule 内，域名类 / IP 类之间是 OR，但与 process 等其他字段是 AND。
	// 因此进程、端口、源地址必须拆成独立 rule，rule 之间为 OR。
	if len(addr) > 0 {
		r := obj{}
		for k, vs := range addr {
			r[k] = vs
		}
		res.Rules = append(res.Rules, r)
	}
	if len(procName) > 0 {
		if mutate == "merge-process" && len(res.Rules) > 0 {
			res.Rules[0]["process_name"] = procName
		} else {
			res.Rules = append(res.Rules, obj{"process_name": procName})
		}
	}
	if len(procPath) > 0 {
		res.Rules = append(res.Rules, obj{"process_path": procPath})
	}
	if len(ports) > 0 {
		res.Rules = append(res.Rules, obj{"port": ports})
	}
	if len(srcIP) > 0 {
		res.Rules = append(res.Rules, obj{"source_ip_cidr": srcIP})
	}
	if len(network) > 0 {
		res.Rules = append(res.Rules, obj{"network": network})
	}
	return res
}

// Surge / Stash DOMAIN-WILDCARD：* 匹配任意字符（含点），? 匹配单个字符。
func globToRegex(g string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range strings.ToLower(g) {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// ---------- Clash 语义参考实现（用于等价性探测） ----------

type clashMatcher struct {
	entries []Entry
	globs   []*regexp.Regexp
}

func newClashMatcher(es []Entry) *clashMatcher {
	m := &clashMatcher{entries: es, globs: make([]*regexp.Regexp, len(es))}
	for i, e := range es {
		if e.Type == "DOMAIN-WILDCARD" {
			m.globs[i] = regexp.MustCompile(globToRegex(e.Value))
		} else if e.Type == "DOMAIN-REGEX" {
			m.globs[i], _ = regexp.Compile(e.Value)
		}
	}
	return m
}

type Probe struct {
	Domain  string
	IP      netip.Addr
	Proc    string
	Port    uint16
	Network string // tcp / udp，空为 tcp
	From    string // 生成该探测的原始规则（私有）
	Kind    string // 正例 / 反例
	Type    string // 来源规则类型
}

// effectivePort 与 probeMetadata 保持一致：未指定端口时按 443。
func (p Probe) effectivePort() uint16 {
	if p.Port == 0 {
		return 443
	}
	return p.Port
}

func (m *clashMatcher) match(p Probe) bool {
	d := strings.ToLower(p.Domain)
	for i, e := range m.entries {
		v := strings.ToLower(e.Value)
		switch e.Type {
		case "DOMAIN":
			if d != "" && d == v {
				return true
			}
		case "DOMAIN-SUFFIX":
			v = strings.TrimPrefix(v, ".")
			if d != "" && (d == v || strings.HasSuffix(d, "."+v)) {
				return true
			}
		case "DOMAIN-SUBONLY":
			if d != "" && strings.HasSuffix(d, "."+v) {
				return true
			}
		case "DOMAIN-SINGLE":
			if d != "" && strings.HasSuffix(d, "."+v) && !strings.Contains(strings.TrimSuffix(d, "."+v), ".") {
				return true
			}
		case "DOMAIN-KEYWORD":
			if d != "" && strings.Contains(d, v) {
				return true
			}
		case "DOMAIN-WILDCARD", "DOMAIN-REGEX":
			if d != "" && m.globs[i] != nil && m.globs[i].MatchString(d) {
				return true
			}
		case "IP-CIDR", "IP-CIDR6":
			if p.IP.IsValid() {
				if pf, err := netip.ParsePrefix(e.Value); err == nil && pf.Contains(p.IP) {
					return true
				}
			}
		case "PROCESS-NAME":
			if p.Proc != "" && p.Proc == e.Value {
				return true
			}
		case "PROCESS-PATH":
			if p.Proc != "" && "/probe/"+p.Proc == e.Value {
				return true
			}
		case "DST-PORT":
			if fmt.Sprint(p.effectivePort()) == e.Value {
				return true
			}
		case "NETWORK":
			if strings.EqualFold(orDefault(p.Network, "tcp"), e.Value) {
				return true
			}
		}
	}
	return false
}

// genProbes 为每条规则生成正例与「相似但不应命中」的反例。期望值由 Clash 参考实现给出。
func genProbes(es []Entry) []Probe {
	var ps []Probe
	add := func(p Probe, e Entry, kind string) {
		p.From, p.Kind, p.Type = e.Raw, kind, e.Type
		ps = append(ps, p)
	}
	for _, e := range es {
		v := strings.ToLower(e.Value)
		switch e.Type {
		case "DOMAIN":
			add(Probe{Domain: v}, e, "正例")
			add(Probe{Domain: "zz" + v}, e, "反例")
			add(Probe{Domain: "a." + v}, e, "反例")
		case "DOMAIN-SUFFIX":
			v = strings.TrimPrefix(v, ".")
			add(Probe{Domain: v}, e, "正例")
			add(Probe{Domain: "a.b." + v}, e, "正例")
			add(Probe{Domain: "zz" + v}, e, "反例")
		case "DOMAIN-SUBONLY":
			add(Probe{Domain: "a." + v}, e, "正例")
			add(Probe{Domain: v}, e, "反例")
		case "DOMAIN-SINGLE":
			add(Probe{Domain: "a." + v}, e, "正例")
			add(Probe{Domain: "a.b." + v}, e, "反例")
		case "DOMAIN-KEYWORD":
			add(Probe{Domain: "x" + v + "y.com"}, e, "正例")
		case "DOMAIN-WILDCARD":
			s := strings.NewReplacer("*", "abc", "?", "x").Replace(v)
			add(Probe{Domain: s}, e, "正例")
			s2 := strings.NewReplacer("*", "a.b", "?", "x").Replace(v)
			add(Probe{Domain: s2}, e, "正例（* 跨标签）")
		case "IP-CIDR", "IP-CIDR6":
			if pf, err := netip.ParsePrefix(e.Value); err == nil {
				add(Probe{IP: pf.Masked().Addr()}, e, "正例")
				if last := lastAddr(pf); last.IsValid() {
					add(Probe{IP: last}, e, "正例")
					if n := last.Next(); n.IsValid() {
						add(Probe{IP: n}, e, "反例")
					}
				}
			}
		case "PROCESS-NAME":
			add(Probe{Proc: e.Value}, e, "正例")
			add(Probe{Proc: e.Value + "x"}, e, "反例")
		case "DST-PORT":
			var n int
			if _, err := fmt.Sscan(e.Value, &n); err == nil {
				add(Probe{Port: uint16(n)}, e, "正例")
				add(Probe{Port: uint16(n + 1)}, e, "反例")
			}
		case "NETWORK":
			add(Probe{Network: strings.ToLower(e.Value)}, e, "正例")
			other := "udp"
			if strings.EqualFold(e.Value, "udp") {
				other = "tcp"
			}
			add(Probe{Network: other}, e, "反例")
		}
	}
	return ps
}

func lastAddr(p netip.Prefix) netip.Addr {
	p = p.Masked()
	a := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range a {
		for b := 0; b < 8; b++ {
			if i*8+b >= bits {
				a[i] |= 1 << (7 - b)
			}
		}
	}
	r, _ := netip.AddrFromSlice(a)
	return r
}

func providerName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// ---------- 逻辑规则（AND / OR / NOT） ----------

// RuleNode 是内联规则的语法树：叶子为单条 Entry，内部节点为逻辑组合。
type RuleNode struct {
	Leaf     *Entry
	Mode     string // and / or
	Invert   bool
	Children []*RuleNode
	Raw      string
}

// parseLogical 解析 "((DOMAIN,a),(NETWORK,UDP))" 形式的参数，返回子规则串。
func splitLogicalArgs(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return nil, fmt.Errorf("logical payload must be wrapped by ()")
	}
	s = s[1 : len(s)-1]
	var out []string
	depth, start := 0, -1
	for i, r := range s {
		switch r {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses")
	}
	return out, nil
}

// parseRuleNode 解析不含 target 的规则体，例如 "DOMAIN,a.com" 或 "AND,((DOMAIN,a),(NETWORK,UDP))"。
func parseRuleNode(body string) (*RuleNode, error) {
	body = strings.TrimSpace(body)
	typ, rest, _ := strings.Cut(body, ",")
	typ = strings.ToUpper(strings.TrimSpace(typ))
	switch typ {
	case "AND", "OR", "NOT":
		args, err := splitLogicalArgs(rest)
		if err != nil {
			return nil, err
		}
		n := &RuleNode{Mode: strings.ToLower(typ), Raw: body}
		if typ == "NOT" {
			if len(args) != 1 {
				return nil, fmt.Errorf("NOT takes exactly one rule")
			}
			n.Mode, n.Invert = "and", true
		}
		for _, a := range args {
			c, err := parseRuleNode(a)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
		}
		return n, nil
	default:
		es := parseProvider([]byte(body), "classical", "text")
		if len(es) != 1 {
			return nil, fmt.Errorf("bad rule %q", body)
		}
		return &RuleNode{Leaf: &es[0], Raw: body}, nil
	}
}

// splitRuleLine 把整行规则拆成规则体与 target（及 no-resolve 等参数）。
func splitRuleLine(line string) (body, target string, params []string) {
	typ, rest, _ := strings.Cut(line, ",")
	switch strings.ToUpper(strings.TrimSpace(typ)) {
	case "AND", "OR", "NOT":
		depth := 0
		for i, r := range rest {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					tail := strings.Split(strings.TrimPrefix(rest[i+1:], ","), ",")
					return typ + "," + rest[:i+1], strings.TrimSpace(tail[0]), tail[1:]
				}
			}
		}
		return line, "", nil
	}
	parts := strings.Split(line, ",")
	if len(parts) < 3 {
		return line, "", nil
	}
	return parts[0] + "," + parts[1], strings.TrimSpace(parts[2]), parts[3:]
}

// toHeadless 把语法树转为 sing-box headless rule（不含 outbound / action）。
func (n *RuleNode) toHeadless() (obj, []string) {
	if n.Leaf != nil {
		r := convertEntries([]Entry{*n.Leaf})
		if len(r.Rules) != 1 {
			return nil, []string{"子规则类型 " + n.Leaf.Type + " 无对应"}
		}
		var notes []string
		if r.Degraded[n.Leaf.Type] > 0 {
			notes = append(notes, "通配符转正则")
		}
		return r.Rules[0], notes
	}
	var rules []obj
	var notes []string
	for _, c := range n.Children {
		h, ns := c.toHeadless()
		if h == nil {
			return nil, ns
		}
		notes = append(notes, ns...)
		rules = append(rules, h)
	}
	o := obj{"type": "logical", "mode": n.Mode, "rules": rules}
	if n.Invert && mutate != "drop-invert" {
		o["invert"] = true
	}
	return o, notes
}

func (n *RuleNode) eval(p Probe) bool {
	if n.Leaf != nil {
		return newClashMatcher([]Entry{*n.Leaf}).match(p)
	}
	var v bool
	if n.Mode == "and" {
		v = true
		for _, c := range n.Children {
			if !c.eval(p) {
				v = false
				break
			}
		}
	} else {
		for _, c := range n.Children {
			if c.eval(p) {
				v = true
				break
			}
		}
	}
	if n.Invert {
		return !v
	}
	return v
}

func (n *RuleNode) hasIPLeafNeedingResolve() bool {
	if n.Leaf != nil {
		return isIPType(n.Leaf.Type) && !n.Leaf.NoResolve
	}
	for _, c := range n.Children {
		if c.hasIPLeafNeedingResolve() {
			return true
		}
	}
	return false
}

// candidates 为语法树生成探测：叶子用 genProbes，组合节点做笛卡尔合并（每个子节点最多取 3 个）。
func (n *RuleNode) candidates() []Probe {
	if n.Leaf != nil {
		return genProbes([]Entry{*n.Leaf})
	}
	acc := []Probe{{}}
	for _, c := range n.Children {
		cs := c.candidates()
		if len(cs) > 3 {
			cs = cs[:3]
		}
		var next []Probe
		for _, a := range acc {
			for _, b := range cs {
				if m, ok := mergeProbe(a, b); ok {
					next = append(next, m)
				}
			}
			// 也保留「该子节点无对应字段」的组合，用于 OR / NOT 的反例
			next = append(next, a)
		}
		acc = next
	}
	for i := range acc {
		acc[i].From, acc[i].Type, acc[i].Kind = n.Raw, "LOGICAL-"+strings.ToUpper(n.Mode), "组合"
	}
	return acc
}

func mergeProbe(a, b Probe) (Probe, bool) {
	out := a
	if b.Domain != "" {
		if a.Domain != "" && a.Domain != b.Domain {
			return a, false
		}
		out.Domain = b.Domain
	}
	if b.IP.IsValid() {
		if a.IP.IsValid() && a.IP != b.IP {
			return a, false
		}
		out.IP = b.IP
	}
	if b.Proc != "" {
		if a.Proc != "" && a.Proc != b.Proc {
			return a, false
		}
		out.Proc = b.Proc
	}
	if b.Port != 0 {
		if a.Port != 0 && a.Port != b.Port {
			return a, false
		}
		out.Port = b.Port
	}
	if b.Network != "" {
		if a.Network != "" && a.Network != b.Network {
			return a, false
		}
		out.Network = b.Network
	}
	return out, true
}
