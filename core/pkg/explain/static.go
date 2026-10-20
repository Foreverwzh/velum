package explain

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// Target 是 vlu why 的推演对象。
type Target struct {
	Raw         string       `json:"raw"`
	Domain      string       `json:"domain,omitempty"`
	IP          netip.Addr   `json:"ip,omitempty"`
	Port        uint16       `json:"port"`
	Network     string       `json:"network,omitempty"`
	ProcessPath string       `json:"process,omitempty"`
	ResolvedIPs []netip.Addr `json:"resolved,omitempty"` // 可选：用户提供的解析结果，用于推演 resolve 之后的 IP 规则
}

// ParseTarget 接受 URL（https://a.com/x）、host:port、域名或 IP。
func ParseTarget(s string) (Target, error) {
	t := Target{Raw: s}
	host, port := s, ""
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return t, err
		}
		host, port = u.Hostname(), u.Port()
		if port == "" {
			switch u.Scheme {
			case "http", "ws":
				port = "80"
			default:
				port = "443"
			}
		}
	} else if h, p, err := net.SplitHostPort(s); err == nil {
		host, port = h, p
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "" {
		return t, fmt.Errorf("无法解析目标 %q", s)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		t.IP = ip
	} else {
		t.Domain = host
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return t, fmt.Errorf("端口无效：%s", port)
		}
		t.Port = uint16(n)
	} else {
		t.Port = 443
	}
	return t, nil
}

func (t Target) meta() Meta {
	return Meta{Domain: t.Domain, IP: t.IP, Port: t.Port, Network: t.Network, ProcessPath: t.ProcessPath}
}

// Host 返回用于展示的主机名。
func (t Target) Host() string {
	if t.Domain != "" {
		return t.Domain
	}
	return t.IP.String()
}

type SysProxyStep struct {
	Enabled  bool       `json:"enabled"`
	Bypassed bool       `json:"bypassed"`
	Pattern  string     `json:"pattern,omitempty"`
	Source   *ir.Source `json:"source,omitempty"`
}

type DNSStep struct {
	Applicable bool       `json:"applicable"` // 目标是域名时才需要 DNS
	Server     string     `json:"server,omitempty"`
	Answer     []string   `json:"answer,omitempty"` // hosts 覆盖
	Source     *ir.Source `json:"source,omitempty"`
}

type RuleTrace struct {
	Index   int       `json:"index"`
	Action  string    `json:"action"`
	Matched bool      `json:"matched"`
	Unknown bool      `json:"unknown,omitempty"`
	Reason  string    `json:"reason"`
	Source  ir.Source `json:"source"`
}

type Decision struct {
	RuleIndex       int       `json:"rule_index"` // -1 = final
	Action          string    `json:"action"`
	Outbound        string    `json:"outbound,omitempty"`
	OverrideAddress string    `json:"override_address,omitempty"`
	Source          ir.Source `json:"source"`
	// Uncertain 表示此前有无法静态求值的规则（远程规则集），实际结果可能不同。
	Uncertain bool `json:"uncertain,omitempty"`
}

type Report struct {
	Target   Target       `json:"target"`
	SysProxy SysProxyStep `json:"sys_proxy"`
	DNS      DNSStep      `json:"dns"`
	Rules    []RuleTrace  `json:"rules"`
	Decision Decision     `json:"decision"`
	Notes    []string     `json:"notes,omitempty"`
}

// Why 在 CompiledPlan 上静态推演一个目标会怎么走（ARCHITECTURE §4.4 静态推演）。
func Why(p *compiler.Plan, t Target) Report { return WhyWith(p, t, nil) }

// WhyWith 同 Why，loaded 提供已加载的本地规则集内容（tag → 条件），用于求值 GeoIP / GeoSite 等文件规则集。
func WhyWith(p *compiler.Plan, t Target, loaded map[string][]ir.Cond) Report {
	r := Report{Target: t, SysProxy: SysProxyStep{Enabled: p.SysProxy.Enabled}}
	host := t.Host()

	// ① 系统代理豁免
	for _, ex := range p.SysProxy.Exceptions {
		if MatchSysProxyException(ex.Pattern, host, t.IP) {
			src := ex.Source
			r.SysProxy.Bypassed, r.SysProxy.Pattern, r.SysProxy.Source = true, ex.Pattern, &src
			break
		}
	}
	if r.SysProxy.Bypassed {
		if p.SysProxy.Enabled {
			r.Notes = append(r.Notes, "命中系统代理豁免：遵守系统代理的应用会直接连接（使用系统 DNS），请求不会经过 Velum；以下为经过 Velum 时的推演")
		} else {
			r.Notes = append(r.Notes, "当前未接管系统代理；若接管，该目标会被豁免")
		}
	}

	sets := Sets{}
	for _, rs := range p.IR.RuleSets {
		if conds, ok := loaded[rs.Tag]; ok {
			rs.Rules = conds
		}
		sets[rs.Tag] = rs
	}

	// ② DNS
	if t.Domain != "" {
		r.DNS.Applicable = true
		m := Meta{Domain: t.Domain}
		for _, dr := range p.IR.DNS.Rules {
			if res := EvalMatch(dr.Match, m, sets); res.Matched {
				src := dr.Source
				r.DNS.Source = &src
				if len(dr.Answer) > 0 {
					r.DNS.Answer = dr.Answer
				} else {
					r.DNS.Server = dr.Server
				}
				break
			}
		}
		if r.DNS.Source == nil {
			r.DNS.Server = p.IR.DNS.Final
		}
	}

	// ③ 规则
	m := t.meta()
	uncertain := false
	for i, rule := range p.IR.Rules {
		tr := RuleTrace{Index: i, Action: rule.Action.Kind, Source: rule.Source}
		switch rule.Action.Kind {
		case ir.ActionSniff:
			continue // 对推演无影响（目标为 IP 时内核可能嗅探出域名）
		case ir.ActionHijackDNS:
			continue
		case ir.ActionResolve:
			if m.Domain != "" && !m.IP.IsValid() {
				if len(t.ResolvedIPs) > 0 {
					m.ResolvedIPs = t.ResolvedIPs
					tr.Reason = "解析域名（使用提供的地址 " + joinAddrs(t.ResolvedIPs) + "）"
				} else {
					m.Unresolved = true
					tr.Reason = "此处解析域名；未提供解析结果，后续 IP 条件标为未知（可用 --resolve 指定）"
					r.Notes = append(r.Notes, "推演经过 resolve：后续 IP 规则需要解析结果才能准确判断")
				}
				tr.Matched = true
				r.Rules = append(r.Rules, tr)
			}
			continue
		}
		res := EvalMatch(rule.Match, m, sets)
		tr.Matched, tr.Unknown, tr.Reason = res.Matched, res.Unknown, res.Reason
		r.Rules = append(r.Rules, tr)
		if res.Unknown {
			uncertain = true
			continue
		}
		if res.Matched {
			r.Decision = Decision{RuleIndex: i, Action: rule.Action.Kind, Outbound: rule.Action.Outbound,
				OverrideAddress: rule.Action.OverrideAddress, Source: rule.Source, Uncertain: uncertain}
			return finish(r)
		}
	}
	r.Decision = Decision{RuleIndex: -1, Action: p.IR.Final.Kind, Outbound: p.IR.Final.Outbound,
		Source: ir.Source{Kind: ir.SourceVelum, Text: "route.final（订阅的 MATCH）"}, Uncertain: uncertain}
	return finish(r)
}

func finish(r Report) Report {
	if r.Decision.Uncertain {
		r.Notes = append(r.Notes, "此前有规则无法静态判定（规则集未加载或缺少解析结果），实际结果以 vlu why --live 为准")
	}
	return r
}

// MatchSysProxyException 按 macOS / Windows 系统代理豁免语义匹配：
// "*.x" 通配主机名；CIDR 仅匹配以 IP 访问的目标；其余为不区分大小写的精确匹配。
func MatchSysProxyException(pattern, host string, ip netip.Addr) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pf, err := netip.ParsePrefix(pattern); err == nil {
		return ip.IsValid() && pf.Contains(ip)
	}
	if strings.ContainsAny(pattern, "*?") {
		ok, _ := path.Match(pattern, host) // * 可跨越 '.'，因此 *.x 匹配任意层级子域
		return ok
	}
	return pattern == host
}

func joinAddrs(as []netip.Addr) string {
	var ss []string
	for _, a := range as {
		ss = append(ss, a.String())
	}
	return strings.Join(ss, ", ")
}
