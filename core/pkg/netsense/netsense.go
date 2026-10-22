// Package netsense 感知网络环境（ARCHITECTURE §4.3）：其他 VPN（分流 / 全局）、Captive Portal。
//
// 只基于操作系统信号工作，不内置任何公司 / 产品名称。分析逻辑（Analyze）是纯函数，平台层只负责采集 Snapshot。
package netsense

import (
	"net/netip"
	"sort"
	"strings"
)

// Route 是一条 IPv4 路由。
type Route struct {
	Dst     netip.Prefix
	Iface   string
	Host    bool   // 主机路由（RTF_HOST）
	Gateway string // 网关地址（无则为空）
	Metric  uint32 // 路由度量（Windows：路由度量 + 接口度量；macOS 不使用）
}

// Resolver 是一条 DNS 解析器配置（scutil --dns）。
type Resolver struct {
	Domain      string // 为空表示默认解析器
	Nameservers []string
	Iface       string // if_index 对应的接口（可能为空）
	Scoped      bool   // 来自「for scoped queries」段
}

// Snapshot 是某一时刻的网络状态。
type Snapshot struct {
	PrimaryInterface string
	Routes           []Route
	Resolvers        []Resolver
	// DHCPNameservers 是物理接口经 DHCP 获得的 DNS（不受手动设置 / Velum 接管系统 DNS 影响）。
	DHCPNameservers []string
	// Tunnels 是平台层按网卡类型判定的隧道接口（Windows 的网卡名不带类型前缀）；按名称前缀的判定（IsTunnel）仍然有效。
	Tunnels []string
}

// isTunnel 判断接口是否为隧道：名称前缀（macOS utun 等）或平台层标记。
func (s Snapshot) isTunnel(name string) bool {
	if IsTunnel(name) {
		return true
	}
	for _, t := range s.Tunnels {
		if t == name {
			return true
		}
	}
	return false
}

// PhysicalNameservers 返回物理接口 iface 的作用域 DNS 服务器（不经过任何 TUN / fake-ip）。
func (s Snapshot) PhysicalNameservers(iface string) []string {
	for _, rv := range s.Resolvers {
		if rv.Scoped && rv.Iface == iface && rv.Domain == "" {
			return rv.Nameservers
		}
	}
	return nil
}

// PhysicalDNS 返回物理网络的 DNS：优先 DHCP 下发值，其次物理接口的作用域 DNS；exclude 中的地址（如 Velum 自己的 DNS）会被剔除。
func (s Snapshot) PhysicalDNS(exclude ...string) []string {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[e] = true
	}
	pick := func(xs []string) []string {
		var out []string
		for _, x := range xs {
			if !skip[x] {
				out = append(out, x)
			}
		}
		return out
	}
	if d := pick(s.DHCPNameservers); len(d) > 0 {
		return d
	}
	return pick(s.PhysicalNameservers(s.PhysicalInterface()))
}

// PhysicalInterface 返回承载非隧道默认路由的接口名（如 en0）。
func (s Snapshot) PhysicalInterface() string {
	for _, r := range s.Routes {
		if r.Dst.Bits() == 0 && r.Iface != "" && !s.isTunnel(r.Iface) {
			return r.Iface
		}
	}
	return ""
}

// NetworkID 是「当前接入的是哪个网络」的近似标识：主接口 + 默认网关 + 默认 DNS 服务器。
// 同一张网卡换连到另一个 Wi-Fi（如酒店网络）时主接口不变，但网关 / DNS 通常会变。
func (s Snapshot) NetworkID() string {
	var parts []string
	parts = append(parts, s.PrimaryInterface)
	for _, r := range s.Routes {
		if r.Dst.Bits() == 0 && !s.isTunnel(r.Iface) {
			parts = append(parts, r.Iface+"@"+r.Gateway)
		}
	}
	for _, rv := range s.Resolvers {
		if rv.Domain == "" && !rv.Scoped && !s.isTunnel(rv.Iface) {
			parts = append(parts, strings.Join(rv.Nameservers, ","))
		}
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "|")
}

// VPN 是检测到的、不属于 Velum 的隧道接口。
type VPN struct {
	Iface       string         `json:"iface"`
	Full        bool           `json:"full"`              // 占据主路由（全局型）
	Routes      []netip.Prefix `json:"routes,omitempty"`  // 声明的网段（分流型）
	Domains     []string       `json:"domains,omitempty"` // 作用域 DNS 域
	Nameservers []string       `json:"nameservers,omitempty"`
}

// Assessment 是一次分析的结论。
type Assessment struct {
	PrimaryInterface string `json:"primary_interface"`
	VPNs             []VPN  `json:"vpns,omitempty"`
	// SplitDNSDomains 是系统中声明了专用解析器的域名（/etc/resolver、VPN 下发的作用域 DNS 等）。
	SplitDNSDomains []string `json:"split_dns_domains,omitempty"`
	// StaleDNS 是系统默认 DNS 中指向代理工具虚拟地址（198.18.0.0/15）的服务器，而当前没有隧道在接管：
	// 多为其他代理工具退出时没有还原的设置，查询无人应答，域名解析会失败或很慢。
	StaleDNS []string `json:"stale_dns,omitempty"`
	// SplitDNS 是这些域名各自的 DNS 服务器（与 SplitDNSDomains 对应）。Velum 直接询问它们：
	// sing-box 的系统解析器在 macOS 上只对 .local 使用 mDNSResponder，其余按默认解析器查询，
	// 而接管系统 DNS 后默认解析器就是 Velum 自己，交给它会回环。
	SplitDNS []SplitResolver `json:"split_dns,omitempty"`
}

// SplitResolver 是一个分流域名及其 DNS 服务器。
type SplitResolver struct {
	Domain      string   `json:"domain"`
	Nameservers []string `json:"nameservers"`
}

// FullTunnel 返回占据主路由的外部 VPN（没有则为 nil）。
func (a Assessment) FullTunnel() *VPN {
	for i := range a.VPNs {
		if a.VPNs[i].Full {
			return &a.VPNs[i]
		}
	}
	return nil
}

// Split 返回分流型外部 VPN。
func (a Assessment) Split() []VPN {
	var out []VPN
	for _, v := range a.VPNs {
		if !v.Full && (len(v.Routes) > 0 || len(v.Domains) > 0) {
			out = append(out, v)
		}
	}
	return out
}

// Key 是用于判断环境是否变化的稳定表示。
func (a Assessment) Key() string {
	var b strings.Builder
	b.WriteString(a.PrimaryInterface)
	for _, v := range a.VPNs {
		b.WriteString("|" + v.Iface)
		if v.Full {
			b.WriteString("!full")
		}
		for _, r := range v.Routes {
			b.WriteString("," + r.String())
		}
		b.WriteString(";" + strings.Join(v.Domains, ",") + ";" + strings.Join(v.Nameservers, ","))
	}
	b.WriteString("|stale:" + strings.Join(a.StaleDNS, ","))
	b.WriteString("|dns:")
	for _, r := range a.SplitDNS {
		b.WriteString(r.Domain + "=" + strings.Join(r.Nameservers, ",") + ";")
	}
	return b.String()
}

var tunnelPrefixes = []string{"utun", "ppp", "ipsec", "tun", "tap", "wg", "gpd", "zt", "feth"}

// IsTunnel 判断接口名是否为隧道类接口。
func IsTunnel(name string) bool {
	for _, p := range tunnelPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

var (
	multicast = netip.MustParsePrefix("224.0.0.0/4")
	linkLocal = netip.MustParsePrefix("169.254.0.0/16")
	broadcast = netip.MustParseAddr("255.255.255.255")
)

func isDefault(p netip.Prefix) bool { return p.Bits() == 0 }

// Analyze 从快照中识别外部 VPN。own 为 Velum 自己的接口（如自身的 utun），不计入。
//
// 判定：
//   - 隧道类接口（utun / ppp / ipsec …）且声明了实际 IPv4 路由（排除组播、广播、链路本地与主机路由）才算 VPN；
//     系统自身的 utun（只有 IPv6 链路本地）因此不会被误判。
//   - 主网络接口是该隧道，且它持有默认路由（或 0/1 + 128/1）→ 全局型。
//   - 其余为分流型，携带其网段与作用域 DNS 域名。
func Analyze(s Snapshot, own ...string) Assessment {
	ownSet := map[string]bool{}
	for _, o := range own {
		ownSet[o] = true
	}
	type acc struct {
		routes  []netip.Prefix
		deflt   bool
		halfLow bool
		halfHi  bool
	}
	byIface := map[string]*acc{}
	for _, r := range s.Routes {
		if !s.isTunnel(r.Iface) || ownSet[r.Iface] {
			continue
		}
		a := byIface[r.Iface]
		if a == nil {
			a = &acc{}
			byIface[r.Iface] = a
		}
		switch {
		case isDefault(r.Dst):
			a.deflt = true
		case r.Dst.String() == "0.0.0.0/1":
			a.halfLow = true
		case r.Dst.String() == "128.0.0.0/1":
			a.halfHi = true
		case r.Host, multicast.Contains(r.Dst.Addr()), linkLocal.Contains(r.Dst.Addr()), r.Dst.Addr() == broadcast:
		default:
			a.routes = append(a.routes, r.Dst.Masked())
		}
	}
	domains := map[string][]string{}
	servers := map[string][]string{}
	var split []string
	splitNS := map[string][]string{}
	for _, rv := range s.Resolvers {
		// 系统 DNS 分流：带域名、有服务器的补充解析器（不限接口，含 /etc/resolver 与各类 VPN 下发的）
		if d := normDomain(rv.Domain); d != "" && len(rv.Nameservers) > 0 && !rv.Scoped && !ownSet[rv.Iface] {
			split = appendUnique(split, d)
			for _, ns := range rv.Nameservers {
				splitNS[d] = appendUnique(splitNS[d], ns)
			}
		}
		if rv.Iface == "" || !s.isTunnel(rv.Iface) || ownSet[rv.Iface] {
			continue
		}
		if d := normDomain(rv.Domain); d != "" {
			domains[rv.Iface] = appendUnique(domains[rv.Iface], d)
		}
		for _, ns := range rv.Nameservers {
			servers[rv.Iface] = appendUnique(servers[rv.Iface], ns)
		}
	}
	sort.Strings(split)
	out := Assessment{PrimaryInterface: s.PrimaryInterface, SplitDNSDomains: split}
	for _, d := range split {
		out.SplitDNS = append(out.SplitDNS, SplitResolver{Domain: d, Nameservers: splitNS[d]})
	}
	names := map[string]bool{}
	for n := range byIface {
		names[n] = true
	}
	for n := range domains {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		a := byIface[n]
		if a == nil {
			a = &acc{}
		}
		// 用一串网段覆盖整个 IPv4 的隧道（如代理工具的 TUN：1.0.0.0/8、2.0.0.0/7 … 128.0.0.0/1）同样是全局接管：
		// 这些网段比默认路由更具体，无论主接口是谁都会接走流量；按分流处理会把几乎所有直连都送进它。
		full := (n == s.PrimaryInterface && (a.deflt || (a.halfLow && a.halfHi))) || coverage(a.routes, a.halfLow, a.halfHi) >= 1<<31
		if !full && len(a.routes) == 0 && len(domains[n]) == 0 {
			continue // 没有声明任何东西的隧道（如系统 utun、仅持有非主默认路由的隧道）
		}
		v := VPN{Iface: n, Full: full, Domains: domains[n], Nameservers: servers[n]}
		if !full {
			v.Routes = dedupe(a.routes)
		}
		out.VPNs = append(out.VPNs, v)
	}
	if out.FullTunnel() == nil {
		for _, rv := range s.Resolvers {
			if rv.Domain != "" || rv.Scoped || ownSet[rv.Iface] {
				continue
			}
			for _, ns := range rv.Nameservers {
				if a, err := netip.ParseAddr(ns); err == nil && FakeIPRange.Contains(a) {
					out.StaleDNS = appendUnique(out.StaleDNS, ns)
				}
			}
		}
	}
	return out
}

// coverage 返回网段（与 0/1、128/1）共覆盖的 IPv4 地址数；被其他网段包含的不重复计算。
func coverage(routes []netip.Prefix, halfLow, halfHi bool) uint64 {
	ps := append([]netip.Prefix(nil), routes...)
	if halfLow {
		ps = append(ps, netip.MustParsePrefix("0.0.0.0/1"))
	}
	if halfHi {
		ps = append(ps, netip.MustParsePrefix("128.0.0.0/1"))
	}
	var n uint64
	for i, p := range ps {
		if !p.Addr().Is4() {
			continue
		}
		nested := false
		for j, q := range ps {
			if j != i && q.Bits() <= p.Bits() && q.Contains(p.Addr()) && (q.Bits() < p.Bits() || j < i) {
				nested = true
				break
			}
		}
		if !nested {
			n += 1 << (32 - p.Bits())
		}
	}
	return n
}

// FakeIPRange 是代理工具常用作虚拟地址（fake-ip、TUN 内 DNS）的保留网段（RFC 2544）。
var FakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// normDomain 规范化解析器域名；反向解析域与 local（mDNS）返回空。
func normDomain(d string) string {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if d == "" || d == "local" || strings.HasSuffix(d, ".arpa") {
		return ""
	}
	return d
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func dedupe(ps []netip.Prefix) []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, p := range ps {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ParseScutilDNS 解析 `scutil --dns` 输出。
func ParseScutilDNS(out string) []Resolver {
	var res []Resolver
	scoped := false
	var cur *Resolver
	flush := func() {
		if cur != nil {
			res = append(res, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "DNS configuration"):
			flush()
			scoped = strings.Contains(t, "scoped")
		case strings.HasPrefix(t, "resolver #"):
			flush()
			cur = &Resolver{Scoped: scoped}
		case cur == nil:
		case strings.HasPrefix(t, "domain"):
			cur.Domain = valueOf(t)
		case strings.HasPrefix(t, "nameserver["):
			cur.Nameservers = append(cur.Nameservers, valueOf(t))
		case strings.HasPrefix(t, "if_index"):
			v := valueOf(t) // "24 (utun4)"
			if i, j := strings.Index(v, "("), strings.Index(v, ")"); i >= 0 && j > i {
				cur.Iface = v[i+1 : j]
			}
		}
	}
	flush()
	return res
}

func valueOf(line string) string {
	_, v, _ := strings.Cut(line, ":")
	return strings.TrimSpace(v)
}

// parseDHCPNameservers 解析 `ipconfig getpacket <iface>` 中的 domain_name_server (ip_mult): {a, b}。
func parseDHCPNameservers(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "domain_name_server") {
			continue
		}
		i, j := strings.Index(line, "{"), strings.Index(line, "}")
		if i < 0 || j <= i {
			return nil
		}
		var ns []string
		for _, f := range strings.Split(line[i+1:j], ",") {
			if a, err := netip.ParseAddr(strings.TrimSpace(f)); err == nil {
				ns = append(ns, a.String())
			}
		}
		return ns
	}
	return nil
}
