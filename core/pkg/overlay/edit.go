package overlay

import (
	"bytes"
	"fmt"
	"net/netip"
	"strings"

	"gopkg.in/yaml.v3"
)

// 条目类型（界面按条目增删 overlay，不必手写 YAML）。
const (
	KindDomain  = "domain"  // dev_bypass.domains，Value 为 overlay 写法（x / .x / =x）
	KindCIDR    = "cidr"    // dev_bypass.cidrs
	KindHost    = "host"    // dev_bypass.hosts，Value 为域名，IP 为覆盖地址
	KindProcess = "process" // dev_bypass.processes
	KindBypass  = "bypass"  // system_proxy.bypass
)

// Entry 是 overlay 中的一个条目。
type Entry struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	IP    string `json:"ip,omitempty"`
	Line  int    `json:"line,omitempty"`
}

// Entries 按类型分组列出所有条目。
func (o *Overlay) Entries() []Entry {
	var out []Entry
	for _, it := range o.Domains {
		out = append(out, Entry{Kind: KindDomain, Value: it.Value, Line: it.Line})
	}
	for _, it := range o.CIDRs {
		out = append(out, Entry{Kind: KindCIDR, Value: it.Value, Line: it.Line})
	}
	for _, h := range o.Hosts {
		out = append(out, Entry{Kind: KindHost, Value: h.Domain, IP: h.IP.String(), Line: h.Line})
	}
	for _, it := range o.Processes {
		out = append(out, Entry{Kind: KindProcess, Value: it.Value, Line: it.Line})
	}
	for _, it := range o.SysProxyBypass {
		out = append(out, Entry{Kind: KindBypass, Value: it.Value, Line: it.Line})
	}
	return out
}

// normalize 校验条目并返回规范写法。
func (e Entry) normalize() (Entry, error) {
	e.Value = strings.TrimSpace(e.Value)
	if e.Value == "" {
		return e, fmt.Errorf("内容为空")
	}
	switch e.Kind {
	case KindDomain:
		name, kind := ParseDomain(e.Value)
		if !validDomain(name) {
			return e, fmt.Errorf("域名无效：%s", e.Value)
		}
		e.Value = map[DomainKind]string{DomainSuffix: "", DomainSubOnly: ".", DomainExact: "="}[kind] + name
	case KindHost:
		e.Value = strings.ToLower(e.Value)
		if !validDomain(e.Value) {
			return e, fmt.Errorf("域名无效：%s", e.Value)
		}
		a, err := netip.ParseAddr(strings.TrimSpace(e.IP))
		if err != nil {
			return e, fmt.Errorf("IP 地址无效：%s", e.IP)
		}
		e.IP = a.String()
	case KindCIDR:
		p, err := netip.ParsePrefix(e.Value)
		if err != nil {
			a, err2 := netip.ParseAddr(e.Value)
			if err2 != nil {
				return e, fmt.Errorf("网段无效：%s", e.Value)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		e.Value = p.Masked().String()
	case KindProcess, KindBypass:
	default:
		return e, fmt.Errorf("未知类型 %q", e.Kind)
	}
	return e, nil
}

func validDomain(s string) bool {
	if s == "" || strings.ContainsAny(s, " /:*") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	_, err := netip.ParseAddr(s)
	return err != nil
}

// path 返回条目所在的映射路径与键。
func (e Entry) path() (section, key string) {
	switch e.Kind {
	case KindBypass:
		return "system_proxy", "bypass"
	case KindDomain:
		return "dev_bypass", "domains"
	case KindCIDR:
		return "dev_bypass", "cidrs"
	case KindHost:
		return "dev_bypass", "hosts"
	}
	return "dev_bypass", "processes"
}

// Add 向 overlay 文本添加一个条目，保留其余内容与注释；已存在时原样返回。
// 同一域名的 hosts 条目会更新为新地址。
func Add(data []byte, e Entry) ([]byte, Entry, error) {
	e, err := e.normalize()
	if err != nil {
		return nil, e, err
	}
	doc, err := load(data)
	if err != nil {
		return nil, e, err
	}
	section, key := e.path()
	root := doc.Content[0]
	sec := ensureMap(root, section)
	if e.Kind == KindHost {
		m := ensure(sec, key, yaml.MappingNode)
		for i := 0; i+1 < len(m.Content); i += 2 {
			if strings.EqualFold(m.Content[i].Value, e.Value) {
				m.Content[i+1].Value = e.IP
				return encode(doc), e, nil
			}
		}
		m.Content = append(m.Content, scalar(e.Value), scalar(e.IP))
		return encode(doc), e, nil
	}
	seq := ensure(sec, key, yaml.SequenceNode)
	for _, n := range seq.Content {
		if same(e, n.Value) {
			return data, e, nil
		}
	}
	seq.Content = append(seq.Content, scalar(e.Value))
	return encode(doc), e, nil
}

// Remove 删除一个条目；条目所在的列表或分节变空时一并删除。找不到时返回错误。
func Remove(data []byte, e Entry) ([]byte, error) {
	e.Value = strings.TrimSpace(e.Value)
	doc, err := load(data)
	if err != nil {
		return nil, err
	}
	section, key := e.path()
	root := doc.Content[0]
	sec := get(root, section)
	n := get(sec, key)
	found := false
	switch {
	case n == nil:
	case n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if strings.EqualFold(n.Content[i].Value, e.Value) {
				n.Content = append(n.Content[:i], n.Content[i+2:]...)
				found = true
				break
			}
		}
	case n.Kind == yaml.SequenceNode:
		for i, it := range n.Content {
			if same(e, it.Value) {
				n.Content = append(n.Content[:i], n.Content[i+1:]...)
				found = true
				break
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("没有找到条目 %s", e.Value)
	}
	if len(n.Content) == 0 {
		del(sec, key)
	}
	if len(sec.Content) == 0 {
		del(root, section)
	}
	return encode(doc), nil
}

// same 比较条目与文件中的原文（域名写法、网段按规范形式比较）。
func same(e Entry, raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, e.Value) {
		return true
	}
	if n, err := (Entry{Kind: e.Kind, Value: raw, IP: e.IP}).normalize(); err == nil && e.Kind != KindProcess && e.Kind != KindBypass {
		if m, err := e.normalize(); err == nil {
			return n.Value == m.Value
		}
	}
	return false
}

func load(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		// 空文件或只有注释：保留注释作为文档头
		doc = yaml.Node{Kind: yaml.DocumentNode, HeadComment: strings.TrimRight(string(data), "\n")}
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("overlay 顶层必须是映射")
	}
	return &doc, nil
}

func encode(doc *yaml.Node) []byte {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(doc)
	_ = enc.Close()
	return b.Bytes()
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func ensure(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if n := get(m, key); n != nil {
		if n.Kind == kind {
			return n
		}
		// 空值（如 "domains:"）或 "[]"：改为所需类型
		if n.Kind == yaml.ScalarNode && (n.Value == "" || n.Tag == "!!null") || len(n.Content) == 0 {
			n.Kind, n.Tag, n.Value, n.Style = kind, "", "", 0
			return n
		}
		return n
	}
	n := &yaml.Node{Kind: kind}
	m.Content = append(m.Content, scalar(key), n)
	return n
}

func ensureMap(m *yaml.Node, key string) *yaml.Node { return ensure(m, key, yaml.MappingNode) }

func del(m *yaml.Node, key string) {
	if m == nil {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
