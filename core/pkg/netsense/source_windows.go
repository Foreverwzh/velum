package netsense

import (
	"net/netip"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// WindowsSnapshot 采集当前网络状态（只读）。ignore 是 Velum 自己的网卡（wintun）。
func WindowsSnapshot(ignore ...string) (Snapshot, error) {
	ifs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagSkipAnycast|winipcfg.GAAFlagSkipMulticast)
	if err != nil {
		return Snapshot{}, err
	}
	var adapters []WinAdapter
	for _, a := range ifs {
		wa := WinAdapter{Index: a.IfIndex, Name: a.FriendlyName(), GUID: a.AdapterName(), IfType: uint32(a.IfType), Desc: a.Description(),
			Up: a.OperStatus == winipcfg.IfOperStatusUp, Metric: a.Ipv4Metric, Suffix: a.DNSSuffix()}
		for d := a.FirstDNSServerAddress; d != nil; d = d.Next {
			if ip := d.Address.IP(); ip != nil {
				if addr, ok := netip.AddrFromSlice(ip); ok && !addr.Unmap().IsLinkLocalUnicast() && !isSiteLocalV6(addr) {
					wa.DNS = append(wa.DNS, addr.Unmap().String())
				}
			}
		}
		adapters = append(adapters, wa)
	}
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return Snapshot{}, err
	}
	var routes []WinRoute
	for i := range rows {
		r := &rows[i]
		routes = append(routes, WinRoute{Dst: r.DestinationPrefix.Prefix(), IfIndex: r.InterfaceIndex, NextHop: r.NextHop.Addr(), Metric: r.Metric})
	}
	return BuildWindowsSnapshot(adapters, routes, readNRPT(), readDHCPDNS(), ignore...), nil
}

// fec0::/10（已废弃的站点本地地址）：Windows 在未配置 IPv6 DNS 时会列出 fec0:0:0:ffff::1 等占位地址。
func isSiteLocalV6(a netip.Addr) bool {
	return a.Is6() && !a.Is4In6() && a.As16()[0] == 0xfe && a.As16()[1]&0xc0 == 0xc0
}

// readNRPT 读取名称解析策略表（NRPT）：按域名指定 DNS 服务器的规则（本地配置与组策略），对应 macOS 的 /etc/resolver。
func readNRPT() []Resolver {
	var out []Resolver
	for _, path := range []string{
		`SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`,
		`SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, n := range names {
			rk, err := registry.OpenKey(registry.LOCAL_MACHINE, path+`\`+n, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			domains, _, _ := rk.GetStringsValue("Name")
			servers, _, _ := rk.GetStringValue("GenericDNSServers")
			rk.Close()
			var ns []string
			for _, s := range strings.FieldsFunc(servers, func(r rune) bool { return r == ';' || r == ' ' }) {
				ns = append(ns, s)
			}
			if len(ns) == 0 {
				continue // 只设置了 DNSSEC / DirectAccess 等选项、未指定服务器的规则
			}
			for _, d := range domains {
				if d = strings.TrimPrefix(d, "."); d != "" {
					out = append(out, Resolver{Domain: d, Nameservers: ns})
				}
			}
		}
	}
	return out
}

// readDHCPDNS 读取各网卡经 DHCP 获得的 DNS（键为小写的网卡 GUID）。
func readDHCPDNS() map[string][]string {
	const base = `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces`
	out := map[string][]string{}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return out
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, n := range names {
		ik, err := registry.OpenKey(registry.LOCAL_MACHINE, base+`\`+n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, _ := ik.GetStringValue("DhcpNameServer")
		ik.Close()
		if f := strings.Fields(strings.ReplaceAll(v, ",", " ")); len(f) > 0 {
			out[strings.ToLower(n)] = f
		}
	}
	return out
}
