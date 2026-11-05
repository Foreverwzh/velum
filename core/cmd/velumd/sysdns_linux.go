package main

import (
	"net/netip"
	"os"
	"strings"
)

// systemUpstreamDNS 返回系统真正的上游 DNS：systemd-resolved 的存根（127.0.0.53）之后的那些，否则是 /etc/resolv.conf。
// 回环地址不算（dnsmasq 等本地解析器）：接管后内核绑定物理网卡，访问不到回环。
// 带网卡作用域的链路本地地址（fe80::1%2）也不算：网段匹配和内核的 DNS 地址都不接受作用域。
func systemUpstreamDNS() []string {
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
