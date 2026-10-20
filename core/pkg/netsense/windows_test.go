package netsense_test

import (
	"net/netip"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/netsense"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

var winAdapters = []netsense.WinAdapter{
	{Index: 5, Name: "以太网", GUID: "{AAA}", IfType: 6, Desc: "Intel(R) Ethernet", Up: true, Metric: 25, DNS: []string{"192.168.1.1"}},
	{Index: 7, Name: "WLAN", GUID: "{BBB}", IfType: 71, Desc: "Wireless", Up: true, Metric: 35, DNS: []string{"10.0.0.1"}},
	{Index: 9, Name: "Velum", IfType: 53, Desc: "Velum Tunnel", Up: true, Metric: 0, DNS: []string{"172.19.0.2"}},
	{Index: 11, Name: "Corp", IfType: 6, Desc: "TAP-Windows Adapter V9", Up: true, Metric: 1, DNS: []string{"172.20.0.53"}, Suffix: "corp.example"},
	{Index: 13, Name: "Down", IfType: 6, Up: false, DNS: []string{"8.8.8.8"}},
}

var winBase = []netsense.WinRoute{
	{Dst: pfx("0.0.0.0/0"), IfIndex: 7, NextHop: netip.MustParseAddr("10.0.0.1"), Metric: 0},
	{Dst: pfx("0.0.0.0/0"), IfIndex: 5, NextHop: netip.MustParseAddr("192.168.1.1"), Metric: 0},
	{Dst: pfx("192.168.1.0/24"), IfIndex: 5, Metric: 256},
	{Dst: pfx("0.0.0.0/1"), IfIndex: 9, Metric: 0}, // Velum 自己的 TUN
	{Dst: pfx("128.0.0.0/1"), IfIndex: 9, Metric: 0},
	{Dst: pfx("0.0.0.0/0"), IfIndex: 13, Metric: 0}, // 未连接的网卡
}

func TestWindowsSnapshotSplitVPN(t *testing.T) {
	routes := append(append([]netsense.WinRoute{}, winBase...),
		netsense.WinRoute{Dst: pfx("172.20.0.0/16"), IfIndex: 11}, netsense.WinRoute{Dst: pfx("172.20.0.5/32"), IfIndex: 11})
	nrpt := []netsense.Resolver{{Domain: "intra.example", Nameservers: []string{"10.9.9.9"}}}
	s := netsense.BuildWindowsSnapshot(winAdapters, routes, nrpt, map[string][]string{"{aaa}": {"192.168.1.254"}}, "Velum")
	// 有线（25）优于无线（35）；Velum 自己的 0/1 + 128/1 不参与
	if s.PrimaryInterface != "以太网" || s.PhysicalInterface() != "以太网" {
		t.Fatalf("primary=%q physical=%q", s.PrimaryInterface, s.PhysicalInterface())
	}
	if got := s.PhysicalDNS(); len(got) != 1 || got[0] != "192.168.1.254" {
		t.Fatalf("应优先取 DHCP DNS：%v", got)
	}
	if got := s.PhysicalNameservers("以太网"); len(got) != 1 || got[0] != "192.168.1.1" {
		t.Fatalf("物理网卡 DNS：%v", got)
	}
	a := netsense.Analyze(s, "Velum")
	if len(a.VPNs) != 1 {
		t.Fatalf("应只识别出 Corp：%+v", a.VPNs)
	}
	v := a.VPNs[0]
	if v.Iface != "Corp" || v.Full || len(v.Routes) != 1 || v.Routes[0] != pfx("172.20.0.0/16") ||
		len(v.Domains) != 1 || v.Domains[0] != "corp.example" || v.Nameservers[0] != "172.20.0.53" {
		t.Fatalf("%+v", v)
	}
	// NRPT 域名交给系统解析器；VPN 网卡的 DNS 后缀不交给系统解析器（避免绕回 TUN 的 DNS）
	if len(a.SplitDNSDomains) != 1 || a.SplitDNSDomains[0] != "intra.example" {
		t.Fatalf("split=%v", a.SplitDNSDomains)
	}
}

func TestWindowsSnapshotFullVPN(t *testing.T) {
	routes := append(append([]netsense.WinRoute{}, winBase...),
		netsense.WinRoute{Dst: pfx("0.0.0.0/1"), IfIndex: 11, Metric: 5}, netsense.WinRoute{Dst: pfx("128.0.0.0/1"), IfIndex: 11, Metric: 5})
	s := netsense.BuildWindowsSnapshot(winAdapters, routes, nil, nil, "Velum")
	if s.PrimaryInterface != "Corp" || s.PhysicalInterface() != "以太网" {
		t.Fatalf("primary=%q physical=%q", s.PrimaryInterface, s.PhysicalInterface())
	}
	a := netsense.Analyze(s, "Velum")
	if f := a.FullTunnel(); f == nil || f.Iface != "Corp" {
		t.Fatalf("应识别为全局型：%+v", a.VPNs)
	}
}
