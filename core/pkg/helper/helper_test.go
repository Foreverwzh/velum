//go:build darwin

package helper_test

import (
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/helper"
	"golang.org/x/sys/unix"
)

// fakeSystem 记录调用；OpenTun 返回一个真实的管道 FD，用来验证 SCM_RIGHTS 传递。
type fakeSystem struct {
	dns      []string
	restored int
	resets   int
	mu       sync.Mutex
	routes   map[netip.Prefix]bool
	existing map[netip.Prefix]bool // 模拟已被其他 VPN 占用的路由
	closed   []int
	opened   int
	scoped   int           // 当前补上的作用域默认路由数
	changes  chan struct{} // 模拟路由变化（nil 时没有变化）
}

func (f *fakeSystem) WatchRoutes(changed func()) error {
	for range f.changes {
		changed()
	}
	return nil
}

func (f *fakeSystem) EnsureScopedDefaults() ([]string, func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scoped++
	return []string{"default via 192.168.1.1 ifscope en0"}, func() error { f.mu.Lock(); f.scoped--; f.mu.Unlock(); return nil }, nil
}

func newFake() *fakeSystem {
	return &fakeSystem{routes: map[netip.Prefix]bool{}, existing: map[netip.Prefix]bool{}}
}

func (f *fakeSystem) SetDNS(servers []string) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dns = servers
	return func() error { f.mu.Lock(); f.dns, f.restored = nil, f.restored+1; f.mu.Unlock(); return nil }, nil
}

func (f *fakeSystem) ResetDNS(servers []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets++
	return []string{"Wi-Fi"}, nil
}

func (f *fakeSystem) OpenTun(mtu uint32, inet4, inet6 []netip.Prefix) (int, string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return -1, "", err
	}
	_ = w.Close()
	fd, _ := unix.Dup(int(r.Fd()))
	_ = r.Close()
	f.mu.Lock()
	f.opened++
	f.mu.Unlock()
	return fd, "utun99", nil
}

func (f *fakeSystem) AddRoute(dst netip.Prefix, gw netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existing[dst] || f.routes[dst] {
		return helper.ErrRouteExists
	}
	f.routes[dst] = true
	return nil
}

func (f *fakeSystem) DeleteRoute(dst netip.Prefix, gw netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routes, dst)
	return nil
}

func (f *fakeSystem) CloseTun(fd int) error {
	f.mu.Lock()
	f.closed = append(f.closed, fd)
	f.mu.Unlock()
	return unix.Close(fd)
}

func (f *fakeSystem) snapshot() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.routes), len(f.closed)
}

func start(t *testing.T, sys helper.System, uid int) string {
	t.Helper()
	dir, _ := os.MkdirTemp("/tmp", "vh-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	srv := &helper.Server{Sys: sys, AllowUID: uid, Logger: log.New(io.Discard, "", 0), RouteSettle: 10 * time.Millisecond}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { ln.Close(); srv.Shutdown("test end") })
	return sock
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

func TestOpenTunPassesFDAndCleansUpOnDisconnect(t *testing.T) {
	sys := newFake()
	sock := start(t, sys, os.Getuid())
	c, err := helper.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	fd, name, err := c.OpenTun(9000, []string{"172.19.0.1/30"}, []string{"fdfe:dcba:9876::1/126"}, []string{"172.19.0.0/30", "198.51.100.7/32", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "utun99" || fd < 0 {
		t.Fatalf("fd=%d name=%s", fd, name)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatalf("收到的 FD 无效：%v", err)
	}
	unix.Close(fd)
	if routes, err := c.AddRoutes("203.0.113.0/24"); err != nil || len(routes) != 4 {
		t.Fatalf("add: %v %v", routes, err)
	}
	if routes, err := c.DelRoutes("203.0.113.0/24"); err != nil || len(routes) != 3 {
		t.Fatalf("del: %v %v", routes, err)
	}
	if err := c.SetDNS("172.19.0.2"); err != nil || strings.Join(sys.dns, ",") != "172.19.0.2" {
		t.Fatalf("set dns: %v %v", err, sys.dns)
	}
	if err := c.SetDNS("172.19.0.2"); err == nil {
		t.Fatal("重复接管 DNS 应报错")
	}
	if _, err := c.DelRoutes("192.0.2.0/24"); err == nil {
		t.Fatal("不应删除不属于本会话的路由")
	}
	// 断开（等同 velumd 被 kill）：路由删除、utun 关闭
	sys.mu.Lock()
	if sys.scoped != 1 {
		t.Fatalf("打开 TUN 时应补物理网卡的作用域默认路由，scoped=%d", sys.scoped)
	}
	sys.mu.Unlock()
	_ = c.Close()
	waitFor(t, func() bool { r, cl := sys.snapshot(); return r == 0 && cl == 1 })
	sys.mu.Lock()
	defer sys.mu.Unlock()
	if sys.restored != 1 || sys.dns != nil {
		t.Fatal("断开时应还原系统 DNS")
	}
	if sys.scoped != 0 {
		t.Fatal("断开时应删除补上的作用域默认路由")
	}
}

func TestRouteSafety(t *testing.T) {
	sys := newFake()
	sys.existing[netip.MustParsePrefix("0.0.0.0/1")] = true // 其他 TUN 已占用
	sock := start(t, sys, os.Getuid())
	c, err := helper.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, err := c.OpenTun(0, []string{"172.19.0.1/30"}, nil, []string{"0.0.0.0/0"}); err == nil || !strings.Contains(err.Error(), "默认路由") {
		t.Fatalf("应拒绝 /0：%v", err)
	}
	waitFor(t, func() bool { _, cl := sys.snapshot(); return cl == 1 }) // 失败的打开已清理
	c2, _ := helper.Dial(sock)
	defer c2.Close()
	fd, _, err := c2.OpenTun(0, []string{"172.19.0.1/30"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if _, err := c2.AddRoutes("0.0.0.0/1"); err == nil || !strings.Contains(err.Error(), "已被其他 TUN") {
		t.Fatalf("应拒绝覆盖已存在的路由：%v", err)
	}
	if _, err := c2.AddRoutes("::/1"); err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("未配置 IPv6 地址时应拒绝 IPv6 路由：%v", err)
	}
	for _, bad := range []string{"127.0.0.0/8", "127.0.0.1/32"} {
		if _, err := c2.AddRoutes(bad); err == nil {
			t.Fatalf("应拒绝回环 %s", bad)
		}
	}
	if routes, err := c2.AddRoutes("128.0.0.0/1"); err != nil || len(routes) != 1 {
		t.Fatalf("全局接管所需的 128.0.0.0/1 应允许：%v %v", routes, err)
	}
}

func TestRejectsOtherUID(t *testing.T) {
	sock := start(t, newFake(), os.Getuid()+1)
	_, err := helper.Dial(sock)
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("应拒绝其他 uid：%v", err)
	}
}

func TestDialErrorMentionsSudo(t *testing.T) {
	_, err := helper.Dial("/tmp/velum-helper-not-exist.sock")
	if err == nil || !strings.Contains(err.Error(), "sudo velum-helper") {
		t.Fatalf("%v", err)
	}

}

func TestValidateRouteV6(t *testing.T) {
	for _, ok := range []string{"::/1", "8000::/1", "2001:db8::/32"} {
		if _, err := helper.ValidateRoute(ok); err != nil {
			t.Errorf("%s 应允许：%v", ok, err)
		}
	}
	for _, bad := range []string{"::/0", "::1/128", "fe80::/64", "fe80::1/128"} {
		if _, err := helper.ValidateRoute(bad); err == nil {
			t.Errorf("%s 应拒绝", bad)
		}
	}
}

// Close 同步清理：返回时路由已删除、utun 已关闭，紧接着打开的新会话不会与旧会话的路由冲突。
func TestCloseCleansUpSynchronously(t *testing.T) {
	sys := newFake()
	sock := start(t, sys, os.Getuid())
	for i := 0; i < 3; i++ {
		c, err := helper.Dial(sock)
		if err != nil {
			t.Fatal(err)
		}
		fd, _, err := c.OpenTun(9000, []string{"172.19.0.1/30"}, nil, []string{"0.0.0.0/1", "128.0.0.0/1"})
		if err != nil {
			t.Fatalf("第 %d 次打开：%v", i+1, err)
		}
		unix.Close(fd)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if r, cl := sys.snapshot(); r != 0 || cl != i+1 {
			t.Fatalf("Close 返回时应已清理：routes=%d closed=%d", r, cl)
		}
	}
}

// 换网后系统删掉了打开 TUN 时补的作用域默认路由：helper 监听到路由变化后重新补上，会话结束时一并删除。
func TestScopedDefaultsReensuredOnRouteChange(t *testing.T) {
	sys := newFake()
	sys.changes = make(chan struct{})
	sock := start(t, sys, os.Getuid())
	c, err := helper.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	fd, _, err := c.OpenTun(9000, []string{"172.19.0.1/30"}, nil, []string{"0.0.0.0/1"})
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	scoped := func() int { sys.mu.Lock(); defer sys.mu.Unlock(); return sys.scoped }
	sys.changes <- struct{}{}
	waitFor(t, func() bool { return scoped() == 2 })
	_ = c.Close()
	waitFor(t, func() bool { return scoped() == 0 })
}

// reset_dns 不需要 TUN；有会话正接管系统 DNS 时拒绝，该会话结束后才允许。
func TestResetDNS(t *testing.T) {
	sys := newFake()
	sock := start(t, sys, os.Getuid())
	r, err := helper.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.ResetDNS("bogus"); err == nil {
		t.Fatal("无效地址应报错")
	}
	if svcs, err := r.ResetDNS("172.19.0.2"); err != nil || strings.Join(svcs, ",") != "Wi-Fi" {
		t.Fatalf("reset: %v %v", svcs, err)
	}

	tun, err := helper.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	fd, _, err := tun.OpenTun(9000, []string{"172.19.0.1/30"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if err := tun.SetDNS("172.19.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResetDNS("172.19.0.2"); err == nil {
		t.Fatal("TUN 接管系统 DNS 期间应拒绝")
	}
	_ = tun.Close()
	if _, err := r.ResetDNS("172.19.0.2"); err != nil {
		t.Fatalf("TUN 会话结束后应允许：%v", err)
	}
	sys.mu.Lock()
	defer sys.mu.Unlock()
	if sys.resets != 2 {
		t.Fatalf("resets=%d", sys.resets)
	}
}

// 签名要求：对端（这里就是测试进程自身，链接器 ad-hoc 签名）满足要求才接受连接。
func TestPeerCodeRequirement(t *testing.T) {
	helper.TeamID = "H3H5ULV8KR"
	defer func() { helper.TeamID = "" }()
	for req, ok := range map[string]bool{"always": true, "never": false, helper.PeerRequirement(): false} {
		sys := newFake()
		dir, _ := os.MkdirTemp("/tmp", "vh-")
		sock := filepath.Join(dir, "h.sock")
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		srv := &helper.Server{Sys: sys, AllowUID: os.Getuid(), Logger: log.New(io.Discard, "", 0), Requirement: req}
		go func() { _ = srv.Serve(ln) }()
		c, err := helper.Dial(sock)
		if ok != (err == nil) {
			t.Errorf("要求 %q：期望接受=%v，实际 err=%v", req, ok, err)
		}
		if c != nil {
			_ = c.Close()
		}
		ln.Close()
		os.RemoveAll(dir)
	}
}
