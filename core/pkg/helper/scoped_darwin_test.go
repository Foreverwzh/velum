//go:build darwin

package helper

import (
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

// 只读：读取本机路由表的默认路由（不修改路由，不需要 root）。
func TestFindDefaultRoutes(t *testing.T) {
	for _, af := range []int{unix.AF_INET, unix.AF_INET6} {
		d, err := findDefaultRoutes(af)
		if err != nil {
			t.Fatal(err)
		}
		if d.unscoped != nil && isTunnelName(d.unscoped.ifName) {
			t.Fatalf("隧道网卡不应被当作物理网卡：%+v", d.unscoped)
		}
		t.Logf("af=%d 物理默认路由=%+v 已有作用域默认路由的网卡=%v", af, d.unscoped, d.scoped)
	}
}

func TestScopedDefaultPlan(t *testing.T) {
	gw := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	home := defaultRoute{ifIndex: 15, ifName: "en0", gw: gw("192.168.3.1")}
	office := defaultRoute{ifIndex: 15, ifName: "en0", gw: gw("10.8.0.1")}
	other := defaultRoute{ifIndex: 7, ifName: "en7", gw: gw("10.9.0.1")}
	for _, c := range []struct {
		name  string
		def   defaultRoutes
		ok    bool
		stale *defaultRoute
	}{
		{"没有默认路由（DHCP 未完成）", defaultRoutes{scoped: map[int][]defaultRoute{15: {office}}}, false, nil},
		{"尚无作用域默认路由", defaultRoutes{unscoped: &home, scoped: map[int][]defaultRoute{7: {other}}}, true, nil},
		{"已有同一网关", defaultRoutes{unscoped: &home, scoped: map[int][]defaultRoute{15: {home}}}, false, nil},
		{"睡眠后换了网络：旧网关的要替换", defaultRoutes{unscoped: &home, scoped: map[int][]defaultRoute{15: {office}}}, true, &office},
	} {
		add, stale, ok := c.def.plan()
		if ok != c.ok || (stale == nil) != (c.stale == nil) || (stale != nil && *stale != *c.stale) {
			t.Errorf("%s：ok=%v stale=%+v，期望 ok=%v stale=%+v", c.name, ok, stale, c.ok, c.stale)
		}
		if ok && add != home {
			t.Errorf("%s：应补 %+v，实际 %+v", c.name, home, add)
		}
	}
}
