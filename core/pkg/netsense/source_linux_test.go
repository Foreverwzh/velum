//go:build linux && !android

package netsense

import (
	"net"
	"strings"
	"testing"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"
)

func TestParseNameservers(t *testing.T) {
	got := parseNameservers("# 注释\nnameserver 127.0.0.53\nnameserver 192.168.3.1\noptions edns0\nnameserver 2001:4860:4860::8888\nnameserver bad\nnameserver fe80::1%2\nsearch lan\n")
	if strings.Join(got, ",") != "192.168.3.1,2001:4860:4860::8888" {
		t.Fatalf("parseNameservers = %v", got)
	}
}

func TestParseResolvectl(t *testing.T) {
	dns := "Global:\nLink 3 (docker0):\nLink 2 (eth0): 192.168.3.1 fe80::1%2\nLink 9 (wg0): 10.77.0.2\n"
	dom := "Global:\nLink 3 (docker0): nothing.test\nLink 2 (eth0): ~. lan\nLink 9 (wg0): ~corp.test\n"
	got := parseResolvectl(dns, dom)
	byDomain := map[string]Resolver{}
	for _, r := range got {
		byDomain[r.Domain] = r
	}
	if len(got) != 2 {
		t.Fatalf("应只有两个域名：%+v", got)
	}
	if r := byDomain["lan"]; r.Iface != "eth0" || strings.Join(r.Nameservers, ",") != "192.168.3.1" {
		t.Errorf("lan：%+v", r)
	}
	if r := byDomain["corp.test"]; r.Iface != "wg0" || strings.Join(r.Nameservers, ",") != "10.77.0.2" {
		t.Errorf("corp.test：%+v", r)
	}
}

func TestConvertRoutes(t *testing.T) {
	names := map[int]string{1: "eth0", 2: "velum0", 3: "wg0", 4: "tailscale0"}
	p := func(c string) *net.IPNet {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	in := []netlink.Route{
		{LinkIndex: 1, Table: unix.RT_TABLE_MAIN, Type: unix.RTN_UNICAST, Family: netlink.FAMILY_V4}, // 默认路由
		{LinkIndex: 1, Table: 12345, Type: unix.RTN_UNICAST, Dst: p("8.8.8.0/24")},                   // auto_redirect 的随机表
		{LinkIndex: 2, Table: 2022, Type: unix.RTN_UNICAST, Dst: p("0.0.0.0/5")},                     // Velum 自己
		{LinkIndex: 3, Table: unix.RT_TABLE_MAIN, Type: unix.RTN_UNICAST, Dst: p("198.51.100.0/24")},
		{LinkIndex: 4, Table: 52, Type: unix.RTN_UNICAST, Dst: p("100.101.1.1/32")},
		{LinkIndex: 4, Table: 52, Type: unix.RTN_UNICAST, Dst: p("100.101.1.2/32")},
		{LinkIndex: 4, Table: 52, Type: unix.RTN_THROW, Dst: p("127.0.0.0/8")},
		{LinkIndex: 3, Table: unix.RT_TABLE_LOCAL, Type: unix.RTN_LOCAL, Dst: p("10.77.0.1/32")},
		{LinkIndex: 3, Table: unix.RT_TABLE_MAIN, Type: unix.RTN_UNICAST, Dst: p("fe80::/64")},
		{LinkIndex: 3, Table: unix.RT_TABLE_MAIN, Type: unix.RTN_UNICAST, Dst: p("2001:db8::/32")},
	}
	var got []string
	for _, r := range convertRoutes(in, names, "velum0", []string{"wg0", "tailscale0"}) {
		got = append(got, r.Iface+" "+r.Dst.String())
	}
	want := "eth0 0.0.0.0/0;wg0 198.51.100.0/24;tailscale0 100.64.0.0/10;wg0 2001:db8::/32"
	if strings.Join(got, ";") != want {
		t.Fatalf("得到 %v，应为 %s", got, want)
	}
}
