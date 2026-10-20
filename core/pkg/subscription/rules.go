package subscription

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"gopkg.in/yaml.v3"
)

// Entry 是一条 Clash 原子规则（统一为 classical 表示）。
// Type 额外包含 Velum 内部类型：DOMAIN-SUBONLY（仅子域）、DOMAIN-SINGLE（单级子域）。
type Entry struct {
	Type      string
	Value     string
	NoResolve bool
	Raw       string
	Line      int
}

func IsIPType(t string) bool { return t == "IP-CIDR" || t == "IP-CIDR6" }

// ParseProvider 读取 rule-provider 内容（text / yaml；domain / ipcidr / classical）。
func ParseProvider(data []byte, behavior, format string) []Entry {
	type line struct {
		text string
		no   int
	}
	var lines []line
	if format == "yaml" || (format == "" && bytes.Contains(data, []byte("payload:"))) {
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err == nil && len(doc.Content) > 0 {
			if p := mapGet(doc.Content[0], "payload"); p != nil {
				for _, it := range p.Content {
					lines = append(lines, line{it.Value, it.Line})
				}
			}
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		n := 0
		for sc.Scan() {
			n++
			lines = append(lines, line{sc.Text(), n})
		}
	}
	var out []Entry
	for _, ln := range lines {
		l := strings.TrimSpace(ln.text)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//") {
			continue
		}
		l = strings.Trim(l, "'\"")
		var e Entry
		switch behavior {
		case "domain":
			e = domainTextEntry(l)
		case "ipcidr":
			t := "IP-CIDR"
			if strings.Contains(l, ":") {
				t = "IP-CIDR6"
			}
			e = Entry{Type: t, Value: l, Raw: l}
		default:
			e = parseClassical(l)
		}
		e.Line = ln.no
		out = append(out, e)
	}
	return out
}

func parseClassical(l string) Entry {
	parts := strings.Split(l, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	e := Entry{Type: strings.ToUpper(parts[0]), Raw: l}
	if len(parts) > 1 {
		e.Value = parts[1]
	}
	if len(parts) > 2 { // 防御：格式不规范的行（无逗号）不能导致越界
		for _, p := range parts[2:] {
			if strings.EqualFold(p, "no-resolve") {
				e.NoResolve = true
			}
		}
	}
	return e
}

// Clash text 域名格式：+.x 根域 + 子域；.x 仅子域；*.x 单级子域；含 * / ? 的其他写法按通配；否则精确。
func domainTextEntry(l string) Entry {
	switch {
	case strings.HasPrefix(l, "+."):
		return Entry{Type: "DOMAIN-SUFFIX", Value: l[2:], Raw: l}
	case strings.HasPrefix(l, "*.") && !strings.ContainsAny(l[2:], "*?"):
		return Entry{Type: "DOMAIN-SINGLE", Value: l[2:], Raw: l}
	case strings.HasPrefix(l, "."):
		return Entry{Type: "DOMAIN-SUBONLY", Value: l[1:], Raw: l}
	case strings.ContainsAny(l, "*?"):
		return Entry{Type: "DOMAIN-WILDCARD", Value: l, Raw: l}
	default:
		return Entry{Type: "DOMAIN", Value: l, Raw: l}
	}
}

// EntriesResult 是一组 Entry 转换为 IR Cond 的结果。
type EntriesResult struct {
	Conds        []ir.Cond
	Counts       map[string]int
	Unsupported  map[string]int
	Degraded     map[string]int
	NeedsResolve bool
}

// ConvertEntries 把 Entry 列表转换为若干 Cond（彼此为「或」）。
// 地址类条件合并为一个 Cond；进程 / 端口 / 源 IP / 网络各自独立成 Cond，
// 因为同一个 Cond 内这些类别与地址类是「与」关系（见 ir 包语义约定）。
func ConvertEntries(entries []Entry) EntriesResult {
	res := EntriesResult{Counts: map[string]int{}, Unsupported: map[string]int{}, Degraded: map[string]int{}}
	var addr ir.Cond
	var procName, procPath, srcIP, network []string
	var ports []int
	hasAddr := false
	for _, e := range entries {
		res.Counts[e.Type]++
		v := strings.ToLower(e.Value)
		switch e.Type {
		case "DOMAIN":
			addr.Domain = append(addr.Domain, v)
			hasAddr = true
		case "DOMAIN-SUFFIX":
			addr.DomainSuffix = append(addr.DomainSuffix, strings.TrimPrefix(v, "."))
			hasAddr = true
		case "DOMAIN-SUBONLY":
			addr.DomainSuffix = append(addr.DomainSuffix, "."+v)
			hasAddr = true
		case "DOMAIN-SINGLE":
			addr.DomainRegex = append(addr.DomainRegex, `^[^.]+\.`+regexp.QuoteMeta(v)+`$`)
			res.Degraded[e.Type]++
			hasAddr = true
		case "DOMAIN-KEYWORD":
			addr.DomainKeyword = append(addr.DomainKeyword, v)
			hasAddr = true
		case "DOMAIN-WILDCARD":
			addr.DomainRegex = append(addr.DomainRegex, GlobToRegex(v))
			res.Degraded[e.Type]++
			hasAddr = true
		case "DOMAIN-REGEX":
			addr.DomainRegex = append(addr.DomainRegex, e.Value)
			hasAddr = true
		case "IP-CIDR", "IP-CIDR6":
			addr.IPCIDR = append(addr.IPCIDR, e.Value)
			hasAddr = true
			if !e.NoResolve {
				res.NeedsResolve = true
			}
		case "PROCESS-NAME":
			procName = append(procName, e.Value)
		case "PROCESS-PATH":
			procPath = append(procPath, e.Value)
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
	// 规则集内各条目统一按 no-resolve 处理，是否需要解析由 NeedsResolve 在引用处决定。
	addr.NoResolve = true
	if hasAddr {
		res.Conds = append(res.Conds, addr)
	}
	if len(procName) > 0 {
		res.Conds = append(res.Conds, ir.Cond{ProcessName: procName})
	}
	if len(procPath) > 0 {
		res.Conds = append(res.Conds, ir.Cond{ProcessPath: procPath})
	}
	if len(ports) > 0 {
		res.Conds = append(res.Conds, ir.Cond{Port: ports})
	}
	if len(srcIP) > 0 {
		res.Conds = append(res.Conds, ir.Cond{SourceIPCIDR: srcIP})
	}
	if len(network) > 0 {
		res.Conds = append(res.Conds, ir.Cond{Network: network})
	}
	return res
}

// GlobToRegex：Surge / Stash DOMAIN-WILDCARD，* 匹配任意字符（含点），? 匹配单个字符。
func GlobToRegex(g string) string {
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

// ---------- 逻辑规则 ----------

// RuleNode 是内联规则的语法树。
type RuleNode struct {
	Leaf     *Entry
	Mode     string // and / or
	Invert   bool
	Children []*RuleNode
	Raw      string
}

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

// ParseRuleNode 解析不含 target 的规则体，如 "DOMAIN,a.com" 或 "AND,((DOMAIN,a),(NETWORK,UDP))"。
func ParseRuleNode(body string) (*RuleNode, error) {
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
			c, err := ParseRuleNode(a)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
		}
		return n, nil
	default:
		e := parseClassical(body)
		return &RuleNode{Leaf: &e, Raw: body}, nil
	}
}

// SplitRuleLine 把整行规则拆成规则体、target 与附加参数（如 no-resolve）。
func SplitRuleLine(line string) (body, target string, params []string) {
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

// ToMatch 把语法树转换为 IR Match；不支持的叶子返回错误说明。
func (n *RuleNode) ToMatch() (ir.Match, []string, error) {
	if n.Leaf != nil {
		r := ConvertEntries([]Entry{*n.Leaf})
		if len(r.Conds) != 1 || len(r.Unsupported) > 0 {
			return ir.Match{}, nil, fmt.Errorf("规则类型 %s 无对应", n.Leaf.Type)
		}
		c := r.Conds[0]
		c.NoResolve = n.Leaf.NoResolve || !c.HasIP()
		var notes []string
		if r.Degraded[n.Leaf.Type] > 0 {
			notes = append(notes, "通配符转正则")
		}
		return ir.Leaf(c), notes, nil
	}
	m := ir.Match{Logical: n.Mode, Invert: n.Invert}
	var notes []string
	for _, c := range n.Children {
		cm, ns, err := c.ToMatch()
		if err != nil {
			return ir.Match{}, nil, err
		}
		notes = append(notes, ns...)
		m.Children = append(m.Children, cm)
	}
	return m, notes, nil
}

// NeedsResolve 报告语法树中是否有未标 no-resolve 的 IP 条件。
func (n *RuleNode) NeedsResolve() bool {
	if n.Leaf != nil {
		return IsIPType(n.Leaf.Type) && !n.Leaf.NoResolve
	}
	for _, c := range n.Children {
		if c.NeedsResolve() {
			return true
		}
	}
	return false
}
