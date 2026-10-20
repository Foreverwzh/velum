// Package explain 实现 Velum 的诊断能力（ARCHITECTURE §4.4）。
//
// match.go 是 VelumIR 语义的参考求值器：静态推演（vlu why）直接使用它，
// 测试中再用它与 sing-box 匹配器逐条比对，保证渲染器不改变语义。
package explain

import (
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// Meta 描述一个待推演的连接。
type Meta struct {
	Domain      string
	IP          netip.Addr   // 目标是 IP 时
	ResolvedIPs []netip.Addr // 经 resolve 动作得到的地址
	Port        uint16
	Network     string // tcp / udp，空为 tcp
	ProcessPath string
	SourceIP    netip.Addr
	Protocol    string // 嗅探协议
	// Unresolved 表示推演已经过 resolve 动作、但没有解析结果：此后的 IP 条件无法判定，应报告为未知而非未命中。
	Unresolved bool
}

func (m Meta) network() string {
	if m.Network == "" {
		return "tcp"
	}
	return m.Network
}

func (m Meta) ips() []netip.Addr {
	if m.IP.IsValid() {
		return []netip.Addr{m.IP}
	}
	return m.ResolvedIPs
}

// Sets 提供规则集内容；远程规则集（GeoIP / GeoSite）静态推演时无法求值。
type Sets map[string]ir.RuleSet

var regexCache sync.Map

func compiled(expr string) *regexp.Regexp {
	if v, ok := regexCache.Load(expr); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		re = regexp.MustCompile(`\A\z.`) // 永不匹配
	}
	regexCache.Store(expr, re)
	return re
}

// MatchDomainSuffix 实现 IR 后缀语义："x" 匹配根域及子域；".x" 只匹配子域。
func MatchDomainSuffix(domain, suffix string) bool {
	if strings.HasPrefix(suffix, ".") {
		return strings.HasSuffix(domain, suffix)
	}
	return domain == suffix || strings.HasSuffix(domain, "."+suffix)
}

// CondResult 说明一个 Cond 的求值结果，供 Explain 输出「为何没命中」。
type CondResult struct {
	Matched bool
	Reason  string // 未命中原因或命中依据
	Unknown bool   // 依赖无法静态求值的远程规则集
}

func EvalCond(c *ir.Cond, m Meta, sets Sets) CondResult {
	domain := strings.ToLower(m.Domain)
	// 地址类：任一命中即可
	hasAddr := len(c.Domain)+len(c.DomainSuffix)+len(c.DomainKeyword)+len(c.DomainRegex)+len(c.IPCIDR) > 0 || c.IPPrivate
	if hasAddr {
		hit := ""
		if domain != "" {
			for _, d := range c.Domain {
				if domain == d {
					hit = "domain=" + d
				}
			}
			for _, s := range c.DomainSuffix {
				if hit == "" && MatchDomainSuffix(domain, s) {
					hit = "domain_suffix=" + s
				}
			}
			for _, k := range c.DomainKeyword {
				if hit == "" && strings.Contains(domain, k) {
					hit = "domain_keyword=" + k
				}
			}
			for _, r := range c.DomainRegex {
				if hit == "" && compiled(r).MatchString(domain) {
					hit = "domain_regex=" + r
				}
			}
		}
		for _, ip := range m.ips() {
			for _, cidr := range c.IPCIDR {
				if hit == "" {
					if p, err := netip.ParsePrefix(cidr); err == nil && p.Contains(ip.Unmap()) {
						hit = "ip_cidr=" + cidr
					}
				}
			}
			if hit == "" && c.IPPrivate && isPrivate(ip) {
				hit = "ip_private"
			}
		}
		if hit == "" {
			reason := "域名 / IP 均未命中"
			if (len(c.IPCIDR) > 0 || c.IPPrivate) && len(m.ips()) == 0 && domain != "" {
				if m.Unresolved {
					return CondResult{Unknown: true, Reason: "IP 条件需要解析结果（可用 --resolve 指定）"}
				}
				reason = "目标是域名且尚未解析，IP 条件无法匹配"
			}
			return CondResult{Reason: reason}
		}
		if r := evalOthers(c, m, sets); !r.Matched {
			return r
		}
		return CondResult{Matched: true, Reason: hit}
	}
	return evalOthers(c, m, sets)
}

func evalOthers(c *ir.Cond, m Meta, sets Sets) CondResult {
	if len(c.Port) > 0 && !containsInt(c.Port, int(m.Port)) {
		return CondResult{Reason: "端口不匹配"}
	}
	if len(c.Network) > 0 && !containsStr(c.Network, m.network()) {
		return CondResult{Reason: "网络类型不匹配"}
	}
	if len(c.Protocol) > 0 && !containsStr(c.Protocol, m.Protocol) {
		return CondResult{Reason: "嗅探协议不匹配"}
	}
	// 进程名 / 路径区分大小写，与 sing-box 一致。
	if len(c.ProcessName) > 0 && (m.ProcessPath == "" || !containsExact(c.ProcessName, filepath.Base(m.ProcessPath))) {
		return CondResult{Reason: "进程名不匹配"}
	}
	if len(c.ProcessPath) > 0 && !containsExact(c.ProcessPath, m.ProcessPath) {
		return CondResult{Reason: "进程路径不匹配"}
	}
	if len(c.SourceIPCIDR) > 0 {
		ok := false
		for _, cidr := range c.SourceIPCIDR {
			if p, err := netip.ParsePrefix(cidr); err == nil && m.SourceIP.IsValid() && p.Contains(m.SourceIP) {
				ok = true
			}
		}
		if !ok {
			return CondResult{Reason: "源地址不匹配"}
		}
	}
	if len(c.RuleSet) > 0 {
		unknown, unloaded := false, false
		for _, t := range c.RuleSet {
			rs, ok := sets[t]
			if !ok || (rs.Kind != ir.RuleSetInline && len(rs.Rules) == 0) {
				unloaded = true // 远程规则集尚未下载 / 未加载
				continue
			}
			for i := range rs.Rules {
				r := EvalCond(&rs.Rules[i], m, sets)
				if r.Matched {
					return CondResult{Matched: true, Reason: "rule_set=" + t + " (" + r.Reason + ")"}
				}
				unknown = unknown || r.Unknown
			}
		}
		switch {
		case unloaded:
			return CondResult{Unknown: true, Reason: "依赖尚未加载的远程规则集，静态推演无法求值"}
		case unknown:
			return CondResult{Unknown: true, Reason: "规则集中的 IP 条目需要解析结果（可用 --resolve 指定）"}
		}
		return CondResult{Reason: "规则集内无条目命中"}
	}
	return CondResult{Matched: true, Reason: "条件满足"}
}

// EvalMatch 对 Match（含逻辑组合）求值。空 Match 视为无条件命中。
func EvalMatch(mt ir.Match, m Meta, sets Sets) CondResult {
	if mt.Cond != nil {
		return EvalCond(mt.Cond, m, sets)
	}
	if mt.Logical == "" {
		return CondResult{Matched: true, Reason: "无条件"}
	}
	var v bool
	unknown := false
	if mt.Logical == "and" {
		v = true
		for _, ch := range mt.Children {
			r := EvalMatch(ch, m, sets)
			unknown = unknown || r.Unknown
			if !r.Matched {
				v = false
				break
			}
		}
	} else {
		for _, ch := range mt.Children {
			r := EvalMatch(ch, m, sets)
			unknown = unknown || r.Unknown
			if r.Matched {
				v = true
				break
			}
		}
	}
	if mt.Invert {
		v = !v
	}
	if v {
		return CondResult{Matched: true, Reason: "逻辑组合（" + mt.Logical + "）满足"}
	}
	return CondResult{Unknown: unknown, Reason: "逻辑组合（" + mt.Logical + "）不满足"}
}

// isPrivate 与 sing-box ip_is_private 一致（!N.IsPublicAddr）：不含 CGNAT 100.64.0.0/10。
func isPrivate(ip netip.Addr) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified()
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsExact(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
