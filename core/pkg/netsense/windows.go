package netsense

import (
	"net/netip"
	"sort"
	"strings"
)

// WinAdapter 是 Windows 网卡（GetAdaptersAddresses）中 NetworkSense 用到的部分。
type WinAdapter struct {
	Index  uint32
	Name   string // FriendlyName（与 Go net.Interface 的名称一致）
	GUID   string // AdapterName，如 {xxxxxxxx-…}，用于读取 DHCP DNS
	IfType uint32
	Desc   string
	Up     bool
	Metric uint32 // IPv4 接口度量
	DNS    []string
	Suffix string // 连接特定的 DNS 后缀
}

// WinRoute 是 Windows 路由表（GetIpForwardTable2）中的一条 IPv4 路由。
type WinRoute struct {
	Dst     netip.Prefix
	IfIndex uint32
	NextHop netip.Addr
	Metric  uint32
}

// Windows 网卡类型（IANA ifType）中的隧道类：PPP、专有虚拟网卡（wintun 等）、隧道封装。
var winTunnelTypes = map[uint32]bool{23: true, 53: true, 131: true}

// 描述中带这些词的以太网类网卡也视为隧道（TAP 类驱动以以太网类型出现）。只用通用技术名称，不列具体产品。
var winTunnelDesc = []string{"tap-windows", "tap adapter", "wintun", "wireguard", "vpn"}

func winIsTunnel(a WinAdapter) bool {
	if winTunnelTypes[a.IfType] {
		return true
	}
	d := strings.ToLower(a.Desc)
	for _, k := range winTunnelDesc {
		if strings.Contains(d, k) {
			return true
		}
	}
	return false
}

// BuildWindowsSnapshot 由网卡、路由、NRPT（按域名指定 DNS 的策略）与 DHCP DNS 组装快照。
// ignore 是 Velum 自己的网卡（wintun），不参与主接口判定。
//
// Windows 没有「主网络服务」的概念：主接口取最优默认路由所在网卡。0/1 + 128/1 比 0/0 更具体，优先；同类比较度量（路由 + 接口）。
func BuildWindowsSnapshot(adapters []WinAdapter, routes []WinRoute, nrpt []Resolver, dhcp map[string][]string, ignore ...string) Snapshot {
	var s Snapshot
	byIndex := map[uint32]WinAdapter{}
	skip := map[string]bool{}
	for _, n := range ignore {
		skip[n] = true
	}
	for _, a := range adapters {
		byIndex[a.Index] = a
		if winIsTunnel(a) {
			s.Tunnels = append(s.Tunnels, a.Name)
		}
	}
	type cand struct {
		iface  string
		halves int // 持有 0/1、128/1 的条数
		deflt  bool
		metric uint32
	}
	cands := map[string]*cand{}
	for _, r := range routes {
		a, ok := byIndex[r.IfIndex]
		if !ok || !a.Up || !r.Dst.Addr().Is4() {
			continue
		}
		m := r.Metric + a.Metric
		rt := Route{Dst: r.Dst.Masked(), Iface: a.Name, Host: r.Dst.Bits() == 32, Metric: m}
		if r.NextHop.IsValid() && !r.NextHop.IsUnspecified() {
			rt.Gateway = r.NextHop.String()
		}
		s.Routes = append(s.Routes, rt)
		if skip[a.Name] {
			continue
		}
		dst := r.Dst.String()
		if r.Dst.Bits() != 0 && dst != "0.0.0.0/1" && dst != "128.0.0.0/1" {
			continue
		}
		c := cands[a.Name]
		if c == nil {
			c = &cand{iface: a.Name, metric: m}
			cands[a.Name] = c
		}
		if r.Dst.Bits() == 0 {
			c.deflt = true
		} else {
			c.halves++
		}
		if m < c.metric {
			c.metric = m
		}
	}
	var best *cand
	for _, c := range cands {
		full := c.halves == 2
		switch {
		case !full && !c.deflt:
		case best == nil:
			best = c
		case full != (best.halves == 2):
			if full {
				best = c
			}
		case c.metric < best.metric || (c.metric == best.metric && c.iface < best.iface):
			best = c
		}
	}
	if best != nil {
		s.PrimaryInterface = best.iface
	}
	// 默认路由按度量排序，PhysicalInterface（第一条非隧道默认路由）因此取最优的物理网卡
	sort.SliceStable(s.Routes, func(i, j int) bool {
		if di, dj := s.Routes[i].Dst.Bits() == 0, s.Routes[j].Dst.Bits() == 0; di != dj || !di {
			return di && !dj
		}
		return s.Routes[i].Metric < s.Routes[j].Metric
	})
	for _, a := range adapters {
		if !a.Up || len(a.DNS) == 0 {
			continue
		}
		s.Resolvers = append(s.Resolvers, Resolver{Iface: a.Name, Nameservers: a.DNS, Scoped: true})
		if a.Name == s.PrimaryInterface {
			s.Resolvers = append(s.Resolvers, Resolver{Iface: a.Name, Nameservers: a.DNS})
		}
		// 隧道网卡的 DNS 后缀作为该 VPN 的域名（Scoped：不交给系统解析器，避免经 TUN 的 DNS 绕回）
		if a.Suffix != "" && winIsTunnel(a) {
			s.Resolvers = append(s.Resolvers, Resolver{Domain: a.Suffix, Iface: a.Name, Nameservers: a.DNS, Scoped: true})
		}
	}
	s.Resolvers = append(s.Resolvers, nrpt...)
	if phys := s.PhysicalInterface(); phys != "" {
		for _, a := range adapters {
			if a.Name == phys {
				s.DHCPNameservers = dhcp[strings.ToLower(a.GUID)]
			}
		}
	}
	return s
}
