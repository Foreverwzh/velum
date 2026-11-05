//go:build linux && !android

package netsense

import (
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
)

// cgnat 是 Tailscale 等覆盖网络常用的地址段。覆盖网络给每个对端一条 /32，对端上下线就变：
// 合并成整段，否则每次变化都要重新加载、中断连接。
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// 这些 IPv6 网段默认不进隧道（见 compiler.linuxDefaultExclude），隧道网卡上的这类路由不需要处理。
var (
	ula        = netip.MustParsePrefix("fc00::/7")
	linkLoc6   = netip.MustParsePrefix("fe80::/10")
	multicast6 = netip.MustParsePrefix("ff00::/8")
)

// LinuxSnapshot 采集 Linux 的网络状态：netlink 取网卡与路由，WireGuard 取对端地址，systemd-resolved 取按链路的 DNS。
// own 是 Velum 自己的 TUN 网卡，不计入。
func LinuxSnapshot(own string) func() (Snapshot, error) {
	dns := &linkDNSCache{}
	return func() (Snapshot, error) {
		links, err := netlink.LinkList()
		if err != nil {
			return Snapshot{}, err
		}
		names := map[int]string{}
		var s Snapshot
		for _, l := range links {
			a := l.Attrs()
			names[a.Index] = a.Name
			if a.Name != own && (l.Type() == "wireguard" || l.Type() == "tuntap") {
				s.Tunnels = append(s.Tunnels, a.Name)
			}
		}
		for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
			routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
			if err != nil {
				return Snapshot{}, err
			}
			s.Routes = append(s.Routes, convertRoutes(routes, names, own, s.Tunnels)...)
		}
		s.PrimaryInterface = s.PhysicalInterface()
		s.Bypass = wireguardEndpoints()
		s.DHCPNameservers = SystemUpstreamDNS()
		s.Resolvers = dns.get()
		return s, nil
	}
}

// convertRoutes 只保留主路由表的路由和隧道网卡的路由（覆盖网络把路由放在自己的表里）。
// Velum 自己的表、auto_redirect 的随机表都在物理网卡或自身网卡上，因此被排除。
func convertRoutes(routes []netlink.Route, names map[int]string, own string, tunnels []string) []Route {
	isTunnel := map[string]bool{}
	for _, t := range tunnels {
		isTunnel[t] = true
	}
	var out []Route
	seen := map[netip.Prefix]bool{}
	for i := range routes {
		r := &routes[i]
		name := names[r.LinkIndex]
		if name == "" || name == own || r.Type != unix.RTN_UNICAST || r.Table == unix.RT_TABLE_LOCAL {
			continue
		}
		if !isTunnel[name] && r.Table != unix.RT_TABLE_MAIN {
			continue
		}
		dst, ok := routeDst(r)
		if !ok {
			continue
		}
		if isTunnel[name] && dst.Bits() > 0 {
			if dst.Addr().Is6() && (ula.Contains(dst.Addr()) || linkLoc6.Contains(dst.Addr()) || multicast6.Contains(dst.Addr())) {
				continue
			}
			if dst.Addr().Is4() && cgnat.Contains(dst.Addr()) {
				dst = cgnat
			}
			key := dst
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		rt := Route{Dst: dst, Iface: name}
		if len(r.Gw) > 0 {
			rt.Gateway = r.Gw.String()
		}
		out = append(out, rt)
	}
	return out
}

// routeDst 返回路由的目标网段；默认路由的 Dst 为空。
func routeDst(r *netlink.Route) (netip.Prefix, bool) {
	if r.Dst == nil {
		if r.Family == netlink.FAMILY_V6 {
			return netip.PrefixFrom(netip.IPv6Unspecified(), 0), true
		}
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0), true
	}
	a, ok := netip.AddrFromSlice(r.Dst.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	bits, _ := r.Dst.Mask.Size()
	return netip.PrefixFrom(a.Unmap(), bits).Masked(), true
}

// wireguardEndpoints 返回 WireGuard 对端的公网地址：内核发出的隧道外层包不能再进 Velum 的隧道，否则 WireGuard 会被套在代理里。
func wireguardEndpoints() []netip.Prefix {
	c, err := wgctrl.New()
	if err != nil {
		return nil
	}
	defer c.Close()
	devices, err := c.Devices()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, d := range devices {
		for _, p := range d.Peers {
			if p.Endpoint == nil {
				continue
			}
			if a, ok := netip.AddrFromSlice(p.Endpoint.IP); ok {
				a = a.Unmap()
				out = append(out, netip.PrefixFrom(a, a.BitLen()))
			}
		}
	}
	return out
}

// SystemUpstreamDNS 返回系统真正的上游 DNS：systemd-resolved 的存根（127.0.0.53）之后的那些，否则是 /etc/resolv.conf。
// 回环地址不算（dnsmasq 等本地解析器）：接管后内核绑定物理网卡，访问不到回环。
// 带网卡作用域的链路本地地址（fe80::1%2）也不算：网段匹配和内核的 DNS 地址都不接受作用域。
func SystemUpstreamDNS() []string {
	for _, f := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if ns := parseNameservers(string(b)); len(ns) > 0 {
			return ns
		}
	}
	return nil
}

func parseNameservers(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		if a, err := netip.ParseAddr(f[1]); err == nil && !a.IsLoopback() && !a.IsUnspecified() && a.Zone() == "" {
			out = append(out, a.String())
		}
	}
	return out
}

// linkDNSCache 缓存 resolvectl 的结果：快照每 5 秒一次，按链路的域名很少变化，不值得每次启动两个进程。
type linkDNSCache struct {
	mu   sync.Mutex
	at   time.Time
	last []Resolver
}

func (c *linkDNSCache) get() []Resolver {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < 30*time.Second && !c.at.IsZero() {
		return c.last
	}
	c.at = time.Now()
	c.last = nil
	dns, err1 := exec.Command("resolvectl", "dns").Output()
	dom, err2 := exec.Command("resolvectl", "domain").Output()
	if err1 == nil && err2 == nil { // 没有 systemd-resolved 时没有按链路的 DNS
		c.last = parseResolvectl(string(dns), string(dom))
	}
	return c.last
}

// parseResolvectl 解析 resolvectl dns / domain 的输出（「Link 2 (eth0): a b」），得到每条链路上各域名的 DNS。
// 「~.」是默认路由标记，不是域名；没有 DNS 服务器的链路无法分流。
func parseResolvectl(dns, domain string) []Resolver {
	parse := func(out string) map[string][]string {
		m := map[string][]string{}
		for _, line := range strings.Split(out, "\n") {
			head, rest, ok := strings.Cut(line, ":")
			f := strings.Fields(head)
			if !ok || len(f) != 3 || f[0] != "Link" {
				continue
			}
			m[strings.Trim(f[2], "()")] = strings.Fields(rest)
		}
		return m
	}
	servers, domains := parse(dns), parse(domain)
	var out []Resolver
	for iface, ds := range domains {
		var ns []string
		for _, s := range servers[iface] {
			if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
				ns = append(ns, a.String())
			}
		}
		if len(ns) == 0 {
			continue
		}
		for _, d := range ds {
			if d = strings.TrimPrefix(d, "~"); d != "." && d != "" {
				out = append(out, Resolver{Domain: d, Nameservers: ns, Iface: iface})
			}
		}
	}
	return out
}
