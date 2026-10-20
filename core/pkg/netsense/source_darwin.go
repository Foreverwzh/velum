//go:build darwin

package netsense

import (
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/net/route"
)

// DarwinSnapshot 采集当前网络状态（只读，不需要 root）。
func DarwinSnapshot() (Snapshot, error) {
	var s Snapshot
	routes, err := darwinRoutes()
	if err != nil {
		return s, err
	}
	s.Routes = routes
	if out, err := exec.Command("/usr/sbin/scutil", "--dns").Output(); err == nil {
		s.Resolvers = ParseScutilDNS(string(out))
	}
	if phys := s.PhysicalInterface(); phys != "" {
		if out, err := exec.Command("/usr/sbin/ipconfig", "getpacket", phys).Output(); err == nil {
			s.DHCPNameservers = parseDHCPNameservers(string(out))
		}
	}
	if out, err := exec.Command("/bin/sh", "-c", "printf 'show State:/Network/Global/IPv4\\n' | /usr/sbin/scutil").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "PrimaryInterface") {
				s.PrimaryInterface = valueOf(t)
			}
		}
	}
	return s, nil
}

func darwinRoutes() ([]Route, error) {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, err
	}
	names := map[int]string{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			names[i.Index] = i.Name
		}
	}
	var out []Route
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || len(rm.Addrs) <= syscall.RTAX_DST {
			continue
		}
		dst, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok {
			continue
		}
		host := rm.Flags&syscall.RTF_HOST != 0
		bits := 32
		if !host {
			bits = 0
			if len(rm.Addrs) > syscall.RTAX_NETMASK {
				if mask, ok := rm.Addrs[syscall.RTAX_NETMASK].(*route.Inet4Addr); ok {
					ones, _ := net.IPv4Mask(mask.IP[0], mask.IP[1], mask.IP[2], mask.IP[3]).Size()
					bits = ones
				}
			}
		}
		p := netip.PrefixFrom(netip.AddrFrom4(dst.IP), bits).Masked()
		gw := ""
		if len(rm.Addrs) > syscall.RTAX_GATEWAY {
			if g, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr); ok {
				gw = netip.AddrFrom4(g.IP).String()
			}
		}
		out = append(out, Route{Dst: p, Iface: names[rm.Index], Host: host, Gateway: gw})
	}
	return out, nil
}
