//go:build darwin

package helper

import (
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
