//go:build darwin

package helper

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const (
	sysprotoControl = 2
	utunOptIfname   = 2
	utunControlName = "com.apple.net.utun_control"
)

// DarwinSystem 是真实的 macOS 实现（需要 root）。M0 spikes/m0-utun 已验证。
type DarwinSystem struct{}

func (DarwinSystem) OpenTun(mtu uint32, inet4, inet6 []netip.Prefix) (fd int, name string, err error) {
	fd, err = unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, sysprotoControl)
	if err != nil {
		return -1, "", fmt.Errorf("socket: %w", err)
	}
	defer func() {
		if err != nil {
			unix.Close(fd)
		}
	}()
	info := &unix.CtlInfo{}
	copy(info.Name[:], utunControlName)
	if err = unix.IoctlCtlInfo(fd, info); err != nil {
		return -1, "", fmt.Errorf("ctlinfo: %w", err)
	}
	// Unit=0：由内核分配空闲的 utunN，不与其他工具的 utun 冲突
	if err = unix.Connect(fd, &unix.SockaddrCtl{ID: info.Id, Unit: 0}); err != nil {
		return -1, "", fmt.Errorf("connect utun: %w", err)
	}
	name, err = unix.GetsockoptString(fd, sysprotoControl, utunOptIfname)
	if err != nil {
		return -1, "", fmt.Errorf("ifname: %w", err)
	}
	name = strings.TrimRight(name, "\x00")
	if mtu == 0 {
		mtu = 9000
	}
	err = useSocket(unix.AF_INET, unix.SOCK_DGRAM, func(s int) error {
		var ifr unix.IfreqMTU
		copy(ifr.Name[:], name)
		ifr.MTU = int32(mtu)
		return unix.IoctlSetIfreqMTU(s, &ifr)
	})
	if err != nil {
		return -1, "", fmt.Errorf("mtu: %w", err)
	}
	for _, p := range inet4 {
		if err = addInet4(name, p); err != nil {
			return -1, "", err
		}
	}
	for _, p := range inet6 {
		if err = addInet6(name, p); err != nil {
			return -1, "", err
		}
	}
	return fd, name, nil
}

// 以下常量与结构见 netinet6/in6_var.h、nd6.h（与 sing-tun 一致）。
const (
	siocAIFADDRIn6      = 2155899162
	in6IffNoDAD         = 0x0020
	in6IffSecured       = 0x0400
	nd6InfiniteLifetime = 0xFFFFFFFF
)

type addrLifetime6 struct {
	Expire    float64
	Preferred float64
	Vltime    uint32
	Pltime    uint32
}

type ifAliasReq6 struct {
	Name     [16]byte
	Addr     unix.RawSockaddrInet6
	Dstaddr  unix.RawSockaddrInet6
	Mask     unix.RawSockaddrInet6
	Flags    uint32
	Lifetime addrLifetime6
}

func addInet6(name string, p netip.Prefix) error {
	var mask [16]byte
	copy(mask[:], net.CIDRMask(p.Bits(), 128))
	req := ifAliasReq6{
		Addr:     unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: p.Addr().As16()},
		Mask:     unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: mask},
		Flags:    in6IffNoDAD | in6IffSecured,
		Lifetime: addrLifetime6{Vltime: nd6InfiniteLifetime, Pltime: nd6InfiniteLifetime},
	}
	if p.Bits() == 128 {
		req.Dstaddr = unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6, Addr: p.Addr().Next().As16()}
	}
	copy(req.Name[:], name)
	return useSocket(unix.AF_INET6, unix.SOCK_DGRAM, func(s int) error {
		if _, _, errno := unix.Syscall(syscall.SYS_IOCTL, uintptr(s), uintptr(siocAIFADDRIn6), uintptr(unsafe.Pointer(&req))); errno != 0 {
			return fmt.Errorf("SIOCAIFADDR_IN6 %s: %w", p, errno)
		}
		return nil
	})
}

func (DarwinSystem) CloseTun(fd int) error { return unix.Close(fd) }

func (DarwinSystem) AddRoute(dst netip.Prefix, gw netip.Addr) error {
	err := execRoute(unix.RTM_ADD, dst, gw)
	if errors.Is(err, unix.EEXIST) {
		return ErrRouteExists
	}
	return err
}

func (DarwinSystem) DeleteRoute(dst netip.Prefix, gw netip.Addr) error {
	err := execRoute(unix.RTM_DELETE, dst, gw)
	if errors.Is(err, unix.ESRCH) {
		return nil // 已不存在（如 utun 销毁时被内核移除）
	}
	return err
}

type ifAliasReq struct {
	Name    [unix.IFNAMSIZ]byte
	Addr    unix.RawSockaddrInet4
	Dstaddr unix.RawSockaddrInet4
	Mask    unix.RawSockaddrInet4
}

func mask4(bits int) [4]byte {
	var m [4]byte
	copy(m[:], net.CIDRMask(bits, 32))
	return m
}

func addInet4(name string, p netip.Prefix) error {
	req := ifAliasReq{
		Addr:    unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: p.Addr().As4()},
		Dstaddr: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: p.Addr().As4()},
		Mask:    unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: mask4(p.Bits())},
	}
	copy(req.Name[:], name)
	return useSocket(unix.AF_INET, unix.SOCK_DGRAM, func(s int) error {
		if _, _, errno := unix.Syscall(syscall.SYS_IOCTL, uintptr(s), uintptr(unix.SIOCAIFADDR), uintptr(unsafe.Pointer(&req))); errno != 0 {
			return fmt.Errorf("SIOCAIFADDR %s: %w", p, errno)
		}
		return nil
	})
}

func execRoute(typ int, dst netip.Prefix, gw netip.Addr) error {
	var addrs []route.Addr
	if dst.Addr().Is4() {
		addrs = []route.Addr{
			syscall.RTAX_DST:     &route.Inet4Addr{IP: dst.Addr().As4()},
			syscall.RTAX_NETMASK: &route.Inet4Addr{IP: mask4(dst.Bits())},
			syscall.RTAX_GATEWAY: &route.Inet4Addr{IP: gw.As4()},
		}
	} else {
		var m [16]byte
		copy(m[:], net.CIDRMask(dst.Bits(), 128))
		addrs = []route.Addr{
			syscall.RTAX_DST:     &route.Inet6Addr{IP: dst.Addr().As16()},
			syscall.RTAX_NETMASK: &route.Inet6Addr{IP: m},
			syscall.RTAX_GATEWAY: &route.Inet6Addr{IP: gw.As16()},
		}
	}
	msg := route.RouteMessage{Type: typ, Version: unix.RTM_VERSION, Flags: unix.RTF_STATIC | unix.RTF_GATEWAY, Seq: 1, Addrs: addrs}
	if typ == unix.RTM_ADD {
		msg.Flags |= unix.RTF_UP
	}
	b, err := msg.Marshal()
	if err != nil {
		return err
	}
	return useSocket(unix.AF_ROUTE, unix.SOCK_RAW, func(s int) error {
		_, err := unix.Write(s, b)
		return err
	})
}

// SetDNS 把已启用硬件网络服务的 DNS 设为 servers，返回按原值还原的函数（原为自动 / DHCP 时还原为 Empty）。
// 不触碰 VPN / NetworkExtension 类服务与 /etc/resolver 等补充解析器。
func (DarwinSystem) SetDNS(servers []string) (func() error, error) {
	svcs, err := sysproxy.New("").Services()
	if err != nil {
		return nil, err
	}
	orig := map[string][]string{}
	for _, svc := range svcs {
		out, err := exec.Command("/usr/sbin/networksetup", "-getdnsservers", svc).Output()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 的 DNS：%w", svc, err)
		}
		orig[svc] = restorable(string(out), servers)
	}
	set := func(svc string, vals []string) error {
		if len(vals) == 0 {
			vals = []string{"Empty"}
		}
		out, err := exec.Command("/usr/sbin/networksetup", append([]string{"-setdnsservers", svc}, vals...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("设置 %s 的 DNS：%v %s", svc, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	restore := func() error {
		var errs []string
		for svc, vals := range orig {
			if err := set(svc, vals); err != nil {
				errs = append(errs, err.Error())
			}
		}
		flushDNSCache()
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	}
	for _, svc := range svcs {
		if err := set(svc, servers); err != nil {
			_ = restore()
			return nil, err
		}
	}
	flushDNSCache()
	return restore, nil
}

// ResetDNS 把系统 DNS 含有 servers 中任一地址的硬件网络服务改回自动获取（Empty）。
func (DarwinSystem) ResetDNS(servers []string) ([]string, error) {
	svcs, err := sysproxy.New("").Services()
	if err != nil {
		return nil, err
	}
	var reset []string
	for _, svc := range svcs {
		out, err := exec.Command("/usr/sbin/networksetup", "-getdnsservers", svc).Output()
		if err != nil {
			return reset, fmt.Errorf("读取 %s 的 DNS：%w", svc, err)
		}
		if !slices.ContainsFunc(strings.Fields(string(out)), func(f string) bool { return slices.Contains(servers, f) }) {
			continue // 不含要清理的地址
		}
		if out, err := exec.Command("/usr/sbin/networksetup", "-setdnsservers", svc, "Empty").CombinedOutput(); err != nil {
			return reset, fmt.Errorf("还原 %s 的 DNS：%v %s", svc, err, strings.TrimSpace(string(out)))
		}
		reset = append(reset, svc)
	}
	if len(reset) > 0 {
		flushDNSCache()
	}
	return reset, nil
}

// restorable 解析 networksetup -getdnsservers 的输出，得到退出时要还原的 DNS。
// 其他代理工具的虚拟 DNS（198.18.0.0/15，如它的 TUN 内 DNS）不还原：那是它没退出干净的残留，
// 还原后没有程序应答，解析全部失败；去掉后为空即还原为自动获取。
// 即将设置的 own（Velum 自己的 TUN 内 DNS）同理：它出现在原值里，说明上次没还原干净，
// 或被其他代理工具当作原值记下、退出时又写了回来。
func restorable(out string, own []string) []string {
	var cur []string
	for _, line := range strings.Split(out, "\n") {
		if a, err := netip.ParseAddr(strings.TrimSpace(line)); err == nil && !netsense.FakeIPRange.Contains(a) && !slices.Contains(own, a.String()) {
			cur = append(cur, a.String())
		}
	}
	return cur
}

func flushDNSCache() {
	_ = exec.Command("/usr/bin/dscacheutil", "-flushcache").Run()
	_ = exec.Command("/usr/bin/killall", "-HUP", "mDNSResponder").Run()
}

func useSocket(domain, typ int, fn func(int) error) error {
	s, err := unix.Socket(domain, typ, 0)
	if err != nil {
		return err
	}
	defer unix.Close(s)
	return fn(s)
}

// EnsureScopedDefaults 见 System。只处理非隧道网卡上带网关的默认路由；已存在同一网关的作用域默认路由时不重复添加，
// 网关不同（旧网络留下的）则替换。
// IPv4 与 IPv6 各自独立：一个失败不影响另一个（返回已补上的部分与错误）。
func (DarwinSystem) EnsureScopedDefaults() ([]string, func() error, error) {
	var added []string
	var undo []func() error
	var errs []error
	for _, af := range []int{unix.AF_INET, unix.AF_INET6} {
		def, err := findDefaultRoutes(af)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r, stale, ok := def.plan()
		if !ok {
			continue
		}
		note := ""
		if stale != nil {
			// 换了网络但网卡没断（典型是合盖睡眠后在另一处唤醒）：旧网络的作用域默认路由还在，
			// 绑定物理网卡的连接会沿用它的旧源地址，一律 can't assign requested address
			if err := execScopedDefault(unix.RTM_DELETE, *stale); err != nil && !errors.Is(err, unix.ESRCH) {
				errs = append(errs, fmt.Errorf("删除 %s 过期的作用域默认路由（经 %s）：%w", stale.ifName, stale.gw, err))
				continue
			}
			note = fmt.Sprintf("（替换经 %s 的旧路由）", stale.gw)
		}
		if err := execScopedDefault(unix.RTM_ADD, r); err != nil {
			if !errors.Is(err, unix.EEXIST) {
				errs = append(errs, fmt.Errorf("添加 %s 作用域默认路由（经 %s）：%w", r.ifName, r.gw, err))
			}
			continue
		}
		added = append(added, fmt.Sprintf("default via %s ifscope %s%s", r.gw, r.ifName, note))
		undo = append(undo, func() error {
			err := execScopedDefault(unix.RTM_DELETE, r)
			if errors.Is(err, unix.ESRCH) {
				return nil // 网卡断开等情况下已被系统移除
			}
			return err
		})
	}
	if len(undo) == 0 {
		return nil, nil, errors.Join(errs...)
	}
	return added, func() error {
		var errs []error
		for _, u := range undo {
			errs = append(errs, u())
		}
		return errors.Join(errs...)
	}, errors.Join(errs...)
}

type defaultRoute struct {
	ifIndex int
	ifName  string
	gw      netip.Addr
}

type defaultRoutes struct {
	unscoped *defaultRoute          // 物理网卡上带网关的非作用域默认路由
	scoped   map[int][]defaultRoute // 各网卡已有的作用域默认路由
}

// plan 决定要补的作用域默认路由：没有非作用域默认路由，或该网卡已有同一网关的作用域默认路由时 ok 为 false；
// 该网卡已有的作用域默认路由网关不同时，stale 是要先删掉的那条。
func (d defaultRoutes) plan() (add defaultRoute, stale *defaultRoute, ok bool) {
	if d.unscoped == nil {
		return add, nil, false
	}
	add = *d.unscoped
	for _, r := range d.scoped[add.ifIndex] {
		if r.gw == add.gw {
			return add, nil, false
		}
	}
	if old := d.scoped[add.ifIndex]; len(old) > 0 {
		stale = &old[0]
	}
	return add, stale, true
}

// findDefaultRoutes 读取路由表中 af 的默认路由。隧道网卡（utun 等）上的默认路由不算物理网卡。
func findDefaultRoutes(af int) (defaultRoutes, error) {
	out := defaultRoutes{scoped: map[int][]defaultRoute{}}
	rib, err := route.FetchRIB(af, route.RIBTypeRoute, 0)
	if err != nil {
		return out, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return out, err
	}
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || len(rm.Addrs) <= unix.RTAX_NETMASK || rm.Flags&unix.RTF_UP == 0 || rm.Flags&unix.RTF_GATEWAY == 0 {
			continue
		}
		if !isZero(rm.Addrs[unix.RTAX_DST]) || (rm.Addrs[unix.RTAX_NETMASK] != nil && !isZero(rm.Addrs[unix.RTAX_NETMASK])) {
			continue
		}
		var gw netip.Addr
		switch a := rm.Addrs[unix.RTAX_GATEWAY].(type) {
		case *route.Inet4Addr:
			gw = netip.AddrFrom4(a.IP)
		case *route.Inet6Addr:
			gw = netip.AddrFrom16(a.IP)
		default:
			continue
		}
		ifi, err := net.InterfaceByIndex(rm.Index)
		if err != nil || isTunnelName(ifi.Name) {
			continue
		}
		r := defaultRoute{ifIndex: rm.Index, ifName: ifi.Name, gw: gw}
		if rm.Flags&unix.RTF_IFSCOPE != 0 {
			out.scoped[rm.Index] = append(out.scoped[rm.Index], r)
			continue
		}
		if out.unscoped == nil {
			out.unscoped = &r
		}
	}
	return out, nil
}

func isZero(a route.Addr) bool {
	switch a := a.(type) {
	case *route.Inet4Addr:
		return a.IP == [4]byte{}
	case *route.Inet6Addr:
		return a.IP == [16]byte{}
	}
	return false
}

func isTunnelName(n string) bool {
	for _, p := range []string{"utun", "tun", "tap", "ppp", "ipsec", "gif", "stf"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func execScopedDefault(typ int, r defaultRoute) error {
	var addrs []route.Addr
	if r.gw.Is4() {
		addrs = []route.Addr{
			syscall.RTAX_DST:     &route.Inet4Addr{},
			syscall.RTAX_NETMASK: &route.Inet4Addr{},
			syscall.RTAX_GATEWAY: &route.Inet4Addr{IP: r.gw.As4()},
		}
	} else {
		gw := &route.Inet6Addr{IP: r.gw.As16()}
		if r.gw.IsLinkLocalUnicast() {
			gw.ZoneID = r.ifIndex // 链路本地网关需带网卡作用域（如 fe80::1%en0）
		}
		addrs = []route.Addr{
			syscall.RTAX_DST:     &route.Inet6Addr{},
			syscall.RTAX_NETMASK: &route.Inet6Addr{},
			syscall.RTAX_GATEWAY: gw,
		}
	}
	msg := route.RouteMessage{Type: typ, Version: unix.RTM_VERSION, Flags: unix.RTF_STATIC | unix.RTF_GATEWAY | unix.RTF_IFSCOPE,
		Index: r.ifIndex, Seq: 1, Addrs: addrs}
	if typ == unix.RTM_ADD {
		msg.Flags |= unix.RTF_UP
	}
	b, err := msg.Marshal()
	if err != nil {
		return err
	}
	return useSocket(unix.AF_ROUTE, unix.SOCK_RAW, func(s int) error {
		_, err := unix.Write(s, b)
		return err
	})
}

// WatchRoutes 见 System。读取路由套接字（PF_ROUTE），路由增删、网卡状态或地址变化时调用 changed。
func (DarwinSystem) WatchRoutes(changed func()) error {
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	buf := make([]byte, 2048)
	for {
		n, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n < 4 {
			continue
		}
		switch buf[3] { // rt_msghdr.rtm_type
		case unix.RTM_ADD, unix.RTM_DELETE, unix.RTM_CHANGE, unix.RTM_IFINFO, unix.RTM_NEWADDR, unix.RTM_DELADDR:
			changed()
		}
	}
}
