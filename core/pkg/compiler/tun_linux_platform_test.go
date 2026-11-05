package compiler

import (
	"net/netip"
	"slices"
	"testing"
)

// Linux 默认排除：云元数据和内网不进隧道，TUN 自身网段保留（它也在私有地址段里）。
func TestLinuxDefaultExclude(t *testing.T) {
	addr := netip.MustParsePrefix("172.19.0.1/30")
	rp, err := BuildRoutePlan(TUNOptions{Address: addr}, linuxDefaultExclude)
	if err != nil {
		t.Fatal(err)
	}
	routed := func(a string) bool {
		ip := netip.MustParseAddr(a)
		return slices.ContainsFunc(rp.Routes, func(r string) bool { return netip.MustParsePrefix(r).Contains(ip) })
	}
	for _, a := range []string{"169.254.169.254", "10.1.2.3", "172.31.255.1", "192.168.3.21"} {
		if routed(a) {
			t.Errorf("%s 不应进隧道；路由 %v", a, rp.Routes)
		}
	}
	for _, a := range []string{"8.8.8.8", "172.32.0.1", "172.15.0.1", "172.19.0.2"} {
		if !routed(a) {
			t.Errorf("%s 应进隧道；路由 %v", a, rp.Routes)
		}
	}
}

// 内网里的上游 DNS 要加回隧道，否则 systemd-resolved 的查询绕过 53 端口劫持；公网 DNS 本来就进隧道。
func TestPrivateResolversRouted(t *testing.T) {
	addr := netip.MustParsePrefix("172.19.0.1/30")
	opts := Options{Platform: "linux", TUN: TUNOptions{Address: addr},
		Env: &Environment{PhysicalDNS: []string{"192.168.3.1", "8.8.8.8", "bad"}}}
	rs := privateResolvers(opts)
	if len(rs) != 1 || rs[0].String() != "192.168.3.1/32" {
		t.Fatalf("privateResolvers = %v", rs)
	}
	opts.TUN.Force = rs
	rp, err := BuildRoutePlan(opts.TUN, linuxDefaultExclude)
	if err != nil {
		t.Fatal(err)
	}
	routed := func(a string) bool {
		ip := netip.MustParseAddr(a)
		return slices.ContainsFunc(rp.Routes, func(r string) bool { return netip.MustParsePrefix(r).Contains(ip) })
	}
	if !routed("192.168.3.1") {
		t.Errorf("上游 DNS 应进隧道：%v", rp.Routes)
	}
	if routed("192.168.3.2") {
		t.Errorf("同网段其他地址不应进隧道：%v", rp.Routes)
	}

	// 用户明确排除时尊重用户
	opts.TUN.Exclude = []netip.Prefix{netip.MustParsePrefix("192.168.3.0/24")}
	if rs := privateResolvers(opts); len(rs) != 0 {
		t.Errorf("用户排除的地址不应加回：%v", rs)
	}
	// 其他平台不受影响
	opts.Platform, opts.TUN.Exclude = "darwin", nil
	if rs := privateResolvers(opts); len(rs) != 0 {
		t.Errorf("只有 Linux 需要：%v", rs)
	}
}
