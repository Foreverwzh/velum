package explain

import (
	"net/netip"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// ProbesForCond 为一个 Cond 生成探测：每个条件的正例与「相似但不应命中」的反例。
// 期望结果不在这里给出，由调用方用参考求值器（EvalCond）计算。
func ProbesForCond(c *ir.Cond) []Meta {
	var ps []Meta
	for _, d := range c.Domain {
		ps = append(ps, Meta{Domain: d}, Meta{Domain: "zz" + d}, Meta{Domain: "a." + d})
	}
	for _, s := range c.DomainSuffix {
		bare := strings.TrimPrefix(s, ".")
		ps = append(ps, Meta{Domain: bare}, Meta{Domain: "a.b." + bare}, Meta{Domain: "zz" + bare})
	}
	for _, k := range c.DomainKeyword {
		ps = append(ps, Meta{Domain: "x" + k + "y.com"}, Meta{Domain: strings.Replace(k, k[:1], "#", 1) + ".com"})
	}
	for _, cidr := range c.IPCIDR {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		first, last := p.Masked().Addr(), lastAddr(p)
		ps = append(ps, Meta{IP: first}, Meta{IP: last})
		if n := last.Next(); n.IsValid() {
			ps = append(ps, Meta{IP: n})
		}
		// 域名目标 + 已解析地址
		ps = append(ps, Meta{Domain: "resolved.example", ResolvedIPs: []netip.Addr{first}}, Meta{Domain: "unresolved.example"})
	}
	if c.IPPrivate {
		for _, s := range []string{"10.1.2.3", "192.168.1.1", "127.0.0.1", "100.64.0.1", "8.8.8.8", "fd00::1"} {
			ps = append(ps, Meta{IP: netip.MustParseAddr(s)})
		}
	}
	for _, port := range c.Port {
		ps = append(ps, Meta{Port: uint16(port)}, Meta{Port: uint16(port + 1)})
	}
	for _, n := range c.Network {
		other := "udp"
		if n == "udp" {
			other = "tcp"
		}
		ps = append(ps, Meta{Network: n}, Meta{Network: other})
	}
	for _, pn := range c.ProcessName {
		ps = append(ps, Meta{ProcessPath: "/Applications/" + pn}, Meta{ProcessPath: "/Applications/" + pn + "x"})
	}
	for _, pp := range c.ProcessPath {
		ps = append(ps, Meta{ProcessPath: pp}, Meta{ProcessPath: pp + "x"})
	}
	for _, s := range c.SourceIPCIDR {
		if p, err := netip.ParsePrefix(s); err == nil {
			ps = append(ps, Meta{SourceIP: p.Masked().Addr()}, Meta{SourceIP: lastAddr(p).Next()})
		}
	}
	for _, pr := range c.Protocol {
		ps = append(ps, Meta{Protocol: pr}, Meta{Protocol: "other"})
	}
	return ps
}

// ProbesForMatch 为逻辑组合生成探测：对子节点探测做笛卡尔合并（每个子节点最多取 4 个）。
func ProbesForMatch(m ir.Match) []Meta {
	if m.Cond != nil {
		return ProbesForCond(m.Cond)
	}
	acc := []Meta{{}}
	for _, ch := range m.Children {
		cs := ProbesForMatch(ch)
		if len(cs) > 4 {
			cs = cs[:4]
		}
		var next []Meta
		for _, a := range acc {
			for _, b := range cs {
				if mm, ok := mergeMeta(a, b); ok {
					next = append(next, mm)
				}
			}
			next = append(next, a)
		}
		acc = next
	}
	return acc
}

func mergeMeta(a, b Meta) (Meta, bool) {
	out := a
	pick := func(x, y string, dst *string) bool {
		if y == "" {
			return true
		}
		if x != "" && x != y {
			return false
		}
		*dst = y
		return true
	}
	if !pick(a.Domain, b.Domain, &out.Domain) || !pick(a.Network, b.Network, &out.Network) ||
		!pick(a.ProcessPath, b.ProcessPath, &out.ProcessPath) || !pick(a.Protocol, b.Protocol, &out.Protocol) {
		return a, false
	}
	if b.IP.IsValid() {
		if a.IP.IsValid() && a.IP != b.IP {
			return a, false
		}
		out.IP = b.IP
	}
	if len(b.ResolvedIPs) > 0 {
		out.ResolvedIPs = b.ResolvedIPs
	}
	if b.Port != 0 {
		if a.Port != 0 && a.Port != b.Port {
			return a, false
		}
		out.Port = b.Port
	}
	if b.SourceIP.IsValid() {
		out.SourceIP = b.SourceIP
	}
	return out, true
}

func lastAddr(p netip.Prefix) netip.Addr {
	p = p.Masked()
	a := p.Addr().AsSlice()
	for i := range a {
		for b := 0; b < 8; b++ {
			if i*8+b >= p.Bits() {
				a[i] |= 1 << (7 - b)
			}
		}
	}
	r, _ := netip.AddrFromSlice(a)
	return r
}
