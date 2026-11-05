package main

import (
	"net/netip"
	"testing"

	"github.com/sagernet/netlink"
)

func TestStaleRule(t *testing.T) {
	n := netip.MustParsePrefix("10.0.0.0/8")
	for _, c := range []struct {
		name string
		r    netlink.Rule
		want bool
	}{
		{"sing-tun 范围", netlink.Rule{Priority: 9001, Table: 2022}, true},
		{"范围之上", netlink.Rule{Priority: 9011, Table: 2022}, false},
		{"回退规则", netlink.Rule{Priority: 32768, Table: 2022, Mark: 0x2024}, true},
		{"同优先级的其他规则", netlink.Rule{Priority: 32768, Table: 100}, false},
		{"系统 main", netlink.Rule{Priority: 32766, Table: 254}, false},
		{"系统 local", netlink.Rule{Priority: 0, Table: 255}, false},
		{"随机表规则", netlink.Rule{Priority: 1, Table: 1234567}, true},
		{"别人写在优先级 1：有源地址条件", netlink.Rule{Priority: 1, Table: 1234567, Src: n}, false},
		{"别人写在优先级 1：有标记条件", netlink.Rule{Priority: 1, Table: 1234567, Mark: 1}, false},
		{"别人写在优先级 1：查 main", netlink.Rule{Priority: 1, Table: 254}, false},
		{"Tailscale", netlink.Rule{Priority: 5270, Table: 52}, false},
		{"wg-quick", netlink.Rule{Priority: 32764, Table: 51820}, false},
	} {
		if got := staleRule(c.r); got != c.want {
			t.Errorf("%s：staleRule = %v，应为 %v", c.name, got, c.want)
		}
	}
}

func TestParseNameservers(t *testing.T) {
	got := parseNameservers("# 注释\nnameserver 127.0.0.53\nnameserver 192.168.3.1\noptions edns0\nnameserver 2001:4860:4860::8888\nnameserver bad\nnameserver fe80::1%2\nsearch lan\n")
	if len(got) != 2 || got[0] != "192.168.3.1" || got[1] != "2001:4860:4860::8888" {
		t.Fatalf("parseNameservers = %v", got)
	}
}
