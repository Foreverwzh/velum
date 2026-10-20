// velum-helper-spike：M0 验证用的特权 helper（需 root）。
//
// 职责（与 ARCHITECTURE §3.2 一致，生产版将用 Swift 重写）：
//   - 创建 utun、设置 MTU / IPv4 地址
//   - 增删路由（只接受具体网段，拒绝 /0 等过宽前缀，绝不覆盖已有路由）
//   - 通过 SCM_RIGHTS 把 utun FD 交给用户态 velumd
//   - velumd 连接断开（含 kill -9）时立即删路由、关 utun
//   - 超过 -ttl 自动全部清理并退出（spike 安全网）
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Foreverwzh/velum/spikes/m0-utun/internal/proto"
	"github.com/sagernet/sing-box/common/process"
	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const (
	sysprotoControl = 2
	utunOptIfname   = 2
	utunControlName = "com.apple.net.utun_control"
	minPrefixBits   = 8
)

var (
	sockPath  = flag.String("sock", "/tmp/velum-spike/helper.sock", "UDS 路径")
	allowUID  = flag.Int("allow-uid", -1, "允许连接的 uid（默认取 SUDO_UID）")
	ttl       = flag.Duration("ttl", 20*time.Minute, "到时自动清理并退出")
	sessionsM sync.Mutex
	sessions  = map[*session]struct{}{}
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.SetPrefix("[helper] ")
	if os.Geteuid() != 0 {
		log.Fatal("需要 root 运行：sudo ", os.Args[0])
	}
	if *allowUID < 0 {
		if v, err := strconv.Atoi(os.Getenv("SUDO_UID")); err == nil {
			*allowUID = v
		} else {
			log.Fatal("无法确定 -allow-uid，请显式指定")
		}
	}

	if err := os.MkdirAll(filepath.Dir(*sockPath), 0o755); err != nil {
		log.Fatal(err)
	}
	_ = os.Remove(*sockPath)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: *sockPath, Net: "unix"})
	if err != nil {
		log.Fatal(err)
	}
	// 权限放开给所有人连接，真正的鉴权在 accept 后做 peer uid 校验。
	_ = os.Chmod(*sockPath, 0o666)
	log.Printf("listening on %s, allow uid=%d, ttl=%s", *sockPath, *allowUID, *ttl)

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case s := <-sig:
			log.Printf("signal %s, cleaning up", s)
		case <-time.After(*ttl):
			log.Printf("ttl reached, cleaning up")
		}
		cancel()
		ln.Close()
	}()

	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("accept: %v", err)
			continue
		}
		go handle(ctx, conn)
	}

	sessionsM.Lock()
	for s := range sessions {
		s.cleanup("helper exiting")
	}
	sessionsM.Unlock()
	_ = os.Remove(*sockPath)
	log.Printf("bye")
}

type session struct {
	conn   *net.UnixConn
	peer   string
	mu     sync.Mutex
	tunFd  int
	name   string
	gw4    netip.Addr
	routes []netip.Prefix
	closed bool
}

func handle(ctx context.Context, conn *net.UnixConn) {
	uid, pid, err := peerCred(conn)
	if err != nil {
		log.Printf("peer cred: %v", err)
		conn.Close()
		return
	}
	peer := fmt.Sprintf("pid=%d uid=%d exe=%s", pid, uid, procName(pid))
	if uid != *allowUID {
		log.Printf("REJECT %s (uid 不在白名单)", peer)
		_ = proto.Send(conn, proto.Response{Error: "unauthorized"}, -1)
		conn.Close()
		return
	}
	// 生产版：这里还要用 SecCodeCopyGuestWithAttributes + SecRequirement 校验对端代码签名。
	log.Printf("ACCEPT %s", peer)

	s := &session{conn: conn, peer: peer, tunFd: -1}
	sessionsM.Lock()
	sessions[s] = struct{}{}
	sessionsM.Unlock()

	go func() { <-ctx.Done(); conn.Close() }()

	dec := json.NewDecoder(conn)
	for {
		var req proto.Request
		if err := dec.Decode(&req); err != nil {
			// EOF：对端正常退出或被 kill -9，立即兜底清理。
			s.cleanup(fmt.Sprintf("peer disconnected (%v)", err))
			break
		}
		resp, fd := s.dispatch(req)
		if err := proto.Send(conn, resp, fd); err != nil {
			s.cleanup(fmt.Sprintf("send: %v", err))
			break
		}
	}
	conn.Close()
	sessionsM.Lock()
	delete(sessions, s)
	sessionsM.Unlock()
}

func (s *session) dispatch(req proto.Request) (proto.Response, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Op {
	case proto.OpOpenTun:
		if s.tunFd >= 0 {
			return proto.Response{Error: "tun already opened in this session"}, -1
		}
		start := time.Now()
		fd, name, gw, err := openTun(req)
		if err != nil {
			return proto.Response{Error: err.Error()}, -1
		}
		s.tunFd, s.name, s.gw4 = fd, name, gw
		for _, r := range req.Routes {
			if err := s.addRouteLocked(r); err != nil {
				s.cleanupLocked("open_tun route failed")
				return proto.Response{Error: err.Error()}, -1
			}
		}
		log.Printf("open_tun %s mtu=%d inet4=%v routes=%v (%s)", name, req.MTU, req.Inet4, req.Routes, time.Since(start))
		return proto.Response{OK: true, Name: name}, fd
	case proto.OpFindOwner:
		src, err1 := netip.ParseAddrPort(req.Src)
		dst, err2 := netip.ParseAddrPort(req.Dst)
		if err1 != nil || err2 != nil {
			return proto.Response{Error: "bad src/dst"}, -1
		}
		start := time.Now()
		owner, err := process.FindDarwinConnectionOwner(req.Network, src, dst)
		if err != nil {
			log.Printf("find_owner %s %s->%s: %v (%s)", req.Network, src, dst, err, time.Since(start))
			return proto.Response{Error: err.Error()}, -1
		}
		log.Printf("find_owner %s %s->%s: pid=%d path=%s (%s)", req.Network, src, dst, owner.ProcessID, owner.ProcessPath, time.Since(start))
		return proto.Response{OK: true, PID: owner.ProcessID, UID: owner.UserId, Path: owner.ProcessPath}, -1
	case proto.OpAddRoute, proto.OpDelRoute:
		if s.tunFd < 0 {
			return proto.Response{Error: "tun not opened"}, -1
		}
		var err error
		for _, r := range req.Routes {
			if req.Op == proto.OpAddRoute {
				err = s.addRouteLocked(r)
			} else {
				err = s.delRouteLocked(r)
			}
			if err != nil {
				break
			}
		}
		if err != nil {
			return proto.Response{Error: err.Error()}, -1
		}
		log.Printf("%s %v via %s", req.Op, req.Routes, s.name)
		return proto.Response{OK: true, Name: s.name}, -1
	default:
		return proto.Response{Error: "unknown op " + req.Op}, -1
	}
}

func (s *session) addRouteLocked(cidr string) error {
	p, err := checkPrefix(cidr)
	if err != nil {
		return err
	}
	for _, r := range s.routes {
		if r == p {
			return nil
		}
	}
	if err := execRoute(unix.RTM_ADD, p, s.gw4); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("route %s already exists, refuse to override", p)
		}
		return fmt.Errorf("add route %s: %w", p, err)
	}
	s.routes = append(s.routes, p)
	return nil
}

func (s *session) delRouteLocked(cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return err
	}
	p = p.Masked()
	for i, r := range s.routes {
		if r == p {
			if err := execRoute(unix.RTM_DELETE, p, s.gw4); err != nil && !errors.Is(err, unix.ESRCH) {
				return fmt.Errorf("delete route %s: %w", p, err)
			}
			s.routes = append(s.routes[:i], s.routes[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("route %s not owned by this session", p)
}

func (s *session) cleanup(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(reason)
}

func (s *session) cleanupLocked(reason string) {
	if s.closed {
		return
	}
	s.closed = true
	start := time.Now()
	for i := len(s.routes) - 1; i >= 0; i-- {
		if err := execRoute(unix.RTM_DELETE, s.routes[i], s.gw4); err != nil && !errors.Is(err, unix.ESRCH) {
			log.Printf("cleanup: delete route %s: %v", s.routes[i], err)
		}
	}
	routes := s.routes
	s.routes = nil
	if s.tunFd >= 0 {
		unix.Close(s.tunFd)
		s.tunFd = -1
	}
	if s.name != "" || len(routes) > 0 {
		log.Printf("CLEANUP %s [%s]: removed routes %v, closed %s in %s", s.peer, reason, routes, s.name, time.Since(start))
	}
}

func checkPrefix(cidr string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return p, err
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return p, errors.New("spike only supports IPv4 routes")
	}
	if p.Bits() < minPrefixBits {
		return p, fmt.Errorf("prefix %s too broad (< /%d), refused", p, minPrefixBits)
	}
	if p.Overlaps(netip.MustParsePrefix("127.0.0.0/8")) {
		return p, fmt.Errorf("prefix %s overlaps loopback, refused", p)
	}
	return p, nil
}

func openTun(req proto.Request) (fd int, name string, gw4 netip.Addr, err error) {
	if len(req.Inet4) == 0 {
		return -1, "", gw4, errors.New("inet4 address required")
	}
	fd, err = unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, sysprotoControl)
	if err != nil {
		return -1, "", gw4, fmt.Errorf("socket: %w", err)
	}
	defer func() {
		if err != nil {
			unix.Close(fd)
		}
	}()
	info := &unix.CtlInfo{}
	copy(info.Name[:], utunControlName)
	if err = unix.IoctlCtlInfo(fd, info); err != nil {
		return -1, "", gw4, fmt.Errorf("ctlinfo: %w", err)
	}
	// Unit=0：由内核分配下一个空闲 utunN，不与任何现有 utun 冲突。
	if err = unix.Connect(fd, &unix.SockaddrCtl{ID: info.Id, Unit: 0}); err != nil {
		return -1, "", gw4, fmt.Errorf("connect utun: %w", err)
	}
	name, err = unix.GetsockoptString(fd, sysprotoControl, utunOptIfname)
	if err != nil {
		return -1, "", gw4, fmt.Errorf("get ifname: %w", err)
	}
	name = strings.TrimRight(name, "\x00")
	mtu := req.MTU
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
		return -1, "", gw4, fmt.Errorf("set mtu: %w", err)
	}
	for i, a := range req.Inet4 {
		p, perr := netip.ParsePrefix(a)
		if perr != nil || !p.Addr().Is4() {
			err = fmt.Errorf("bad inet4 %q", a)
			return -1, "", gw4, err
		}
		if i == 0 {
			gw4 = p.Addr()
		}
		if err = addInet4(name, p); err != nil {
			return -1, "", gw4, err
		}
	}
	return fd, name, gw4, nil
}

type ifAliasReq struct {
	Name    [unix.IFNAMSIZ]byte
	Addr    unix.RawSockaddrInet4
	Dstaddr unix.RawSockaddrInet4
	Mask    unix.RawSockaddrInet4
}

func addInet4(name string, p netip.Prefix) error {
	mask := netip.MustParseAddr(net.IP(net.CIDRMask(p.Bits(), 32)).String()).As4()
	req := ifAliasReq{
		Addr:    unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: p.Addr().As4()},
		Dstaddr: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: p.Addr().As4()},
		Mask:    unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET, Addr: mask},
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
	msg := route.RouteMessage{
		Type:    typ,
		Version: unix.RTM_VERSION,
		Flags:   unix.RTF_STATIC | unix.RTF_GATEWAY,
		Seq:     1,
		Addrs: []route.Addr{
			syscall.RTAX_DST:     &route.Inet4Addr{IP: dst.Addr().As4()},
			syscall.RTAX_NETMASK: &route.Inet4Addr{IP: netip.MustParseAddr(net.IP(net.CIDRMask(dst.Bits(), 32)).String()).As4()},
			syscall.RTAX_GATEWAY: &route.Inet4Addr{IP: gw.As4()},
		},
	}
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

func useSocket(domain, typ int, fn func(int) error) error {
	s, err := unix.Socket(domain, typ, 0)
	if err != nil {
		return err
	}
	defer unix.Close(s)
	return fn(s)
}

func peerCred(conn *net.UnixConn) (uid, pid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cerr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if cerr != nil {
			return
		}
		uid = int(cred.Uid)
		pid, cerr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err == nil {
		err = cerr
	}
	return
}

func procName(pid int) string {
	out, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}
