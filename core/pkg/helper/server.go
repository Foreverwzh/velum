//go:build darwin

package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// System 抽象 helper 的特权系统调用（darwin 实现见 system_darwin.go；测试中替换）。
type System interface {
	OpenTun(mtu uint32, inet4, inet6 []netip.Prefix) (fd int, name string, err error)
	AddRoute(dst netip.Prefix, gw netip.Addr) error
	DeleteRoute(dst netip.Prefix, gw netip.Addr) error
	CloseTun(fd int) error
	// SetDNS 设置系统 DNS 并返回还原函数。
	SetDNS(servers []string) (restore func() error, err error)
	// EnsureScopedDefaults 确保物理网卡有作用域默认路由（IPv4 / IPv6），返回补上的路由说明与撤销函数（未补时为 nil）。
	//
	// velumd 把自己的出站连接绑定到物理网卡（IP_BOUND_IF）以免绕回 TUN；macOS 对绑定网卡的连接只查该网卡作用域内的路由，
	// 主网卡的默认路由若是非作用域的（如其他 VPN 退出后），绑定的连接一律「网络不可达」。
	EnsureScopedDefaults() (added []string, undo func() error, err error)
	// WatchRoutes 在路由表或网卡变化时调用 changed，阻塞直到出错。
	WatchRoutes(changed func()) error
}

// ErrRouteExists 表示目标路由已存在（通常是其他 TUN / VPN 已占用）。
var ErrRouteExists = errors.New("route exists")

type Server struct {
	Sys      System
	AllowUID int
	Logger   *log.Logger
	// PeerCred 返回对端 uid / pid；默认用 LOCAL_PEERCRED，测试可替换。
	PeerCred func(*net.UnixConn) (uid, pid int, err error)
	// RouteSettle 是路由变化后等待其稳定再补作用域默认路由的时间（默认 0.3 秒）。
	RouteSettle time.Duration

	mu       sync.Mutex
	sessions map[*session]struct{}
}

type session struct {
	srv        *Server
	peer       string
	mu         sync.Mutex
	tunFd      int
	name       string
	gw4, gw6   netip.Addr
	routes     []netip.Prefix
	restoreDNS func() error
	undoScoped func() error // 删除本会话补上的作用域默认路由
	closed     bool
}

func (x *session) gateway(p netip.Prefix) (netip.Addr, error) {
	if p.Addr().Is4() {
		if !x.gw4.IsValid() {
			return netip.Addr{}, errors.New("TUN 未配置 IPv4 地址")
		}
		return x.gw4, nil
	}
	if !x.gw6.IsValid() {
		return netip.Addr{}, errors.New("TUN 未配置 IPv6 地址")
	}
	return x.gw6, nil
}

// Serve 处理连接，直到 ln 关闭。
func (s *Server) Serve(ln *net.UnixListener) error {
	if s.Logger == nil {
		s.Logger = log.New(os.Stderr, "[helper] ", log.LstdFlags)
	}
	if s.PeerCred == nil {
		s.PeerCred = peerCred
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[*session]struct{}{}
	}
	s.mu.Unlock()
	if s.Sys != nil {
		go s.watchRoutes()
	}
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

// Shutdown 清理所有会话（删除路由、关闭 utun）。
func (s *Server) Shutdown(reason string) {
	s.mu.Lock()
	ss := make([]*session, 0, len(s.sessions))
	for x := range s.sessions {
		ss = append(ss, x)
	}
	s.mu.Unlock()
	for _, x := range ss {
		x.cleanup(reason)
	}
}

// watchRoutes 在网络变化（换 Wi-Fi、睡眠唤醒、网卡重连）后为打开着 TUN 的会话重新补作用域默认路由：
// 网卡断开时系统会删掉它的全部路由（包括打开 TUN 时补上的），重连后新的默认路由又是非作用域的，
// velumd 绑定物理网卡的连接会一律「网络不可达」。
func (s *Server) watchRoutes() {
	kick := make(chan struct{}, 1)
	go func() {
		err := s.Sys.WatchRoutes(func() {
			select {
			case kick <- struct{}{}:
			default:
			}
		})
		s.Logger.Printf("停止监听路由变化：%v", err)
	}()
	settle := s.RouteSettle
	if settle <= 0 {
		settle = 300 * time.Millisecond
	}
	for range kick {
		time.Sleep(settle) // 等这一波变化结束（DHCP 拿到地址、系统装好默认路由）
		select {
		case <-kick:
		default:
		}
		s.mu.Lock()
		ss := make([]*session, 0, len(s.sessions))
		for x := range s.sessions {
			ss = append(ss, x)
		}
		s.mu.Unlock()
		for _, x := range ss {
			x.reensureScoped()
		}
	}
}

// reensureScoped 再次补作用域默认路由（已存在时不重复添加），新补的与之前的一起在会话结束时删除。
func (x *session) reensureScoped() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed || x.tunFd < 0 {
		return
	}
	added, undo, err := x.srv.Sys.EnsureScopedDefaults()
	if undo != nil {
		if prev := x.undoScoped; prev != nil {
			x.undoScoped = func() error { return errors.Join(undo(), prev()) }
		} else {
			x.undoScoped = undo
		}
		x.srv.Logger.Printf("%s 网络变化后补物理网卡作用域默认路由 %v", x.peer, added)
	}
	if err != nil {
		x.srv.Logger.Printf("%s 网络变化后补物理网卡作用域默认路由失败：%v", x.peer, err)
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	uid, pid, err := s.PeerCred(conn)
	if err != nil {
		s.Logger.Printf("peer cred: %v", err)
		return
	}
	peer := fmt.Sprintf("pid=%d uid=%d", pid, uid)
	if uid != s.AllowUID {
		s.Logger.Printf("拒绝 %s：uid 不在白名单", peer)
		_ = send(conn, Response{Error: "unauthorized"}, -1)
		return
	}
	// TODO(M2)：校验对端代码签名（SecCodeCopyGuestWithAttributes + SecRequirement）。
	x := &session{srv: s, peer: peer, tunFd: -1}
	s.mu.Lock()
	s.sessions[x] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, x)
		s.mu.Unlock()
	}()
	s.Logger.Printf("接受 %s", peer)
	dec := json.NewDecoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			x.cleanup(fmt.Sprintf("对端断开（%v）", err)) // 含 kill -9
			return
		}
		resp, fd := x.dispatch(req)
		if err := send(conn, resp, fd); err != nil {
			x.cleanup("发送失败：" + err.Error())
			return
		}
	}
}

func (x *session) dispatch(req Request) (Response, int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	switch req.Op {
	case OpPing:
		return Response{OK: true, Version: Version}, -1
	case OpOpenTun:
		if x.tunFd >= 0 {
			return Response{Error: "本会话已打开 TUN"}, -1
		}
		var v4, v6 []netip.Prefix
		for _, a := range req.Inet4 {
			p, err := netip.ParsePrefix(a)
			if err != nil || !p.Addr().Is4() {
				return Response{Error: fmt.Sprintf("TUN 地址无效：%q", a)}, -1
			}
			v4 = append(v4, p)
		}
		for _, a := range req.Inet6 {
			p, err := netip.ParsePrefix(a)
			if err != nil || !p.Addr().Is6() {
				return Response{Error: fmt.Sprintf("TUN IPv6 地址无效：%q", a)}, -1
			}
			v6 = append(v6, p)
		}
		if len(v4) == 0 {
			return Response{Error: "缺少 TUN 地址"}, -1
		}
		fd, name, err := x.srv.Sys.OpenTun(req.MTU, v4, v6)
		if err != nil {
			return Response{Error: "创建 utun：" + err.Error()}, -1
		}
		x.tunFd, x.name, x.gw4 = fd, name, v4[0].Addr()
		if len(v6) > 0 {
			x.gw6 = v6[0].Addr()
		}
		if err := x.addRoutesLocked(req.Routes); err != nil {
			x.cleanupLocked("打开 TUN 时添加路由失败")
			return Response{Error: err.Error()}, -1
		}
		x.srv.Logger.Printf("%s 打开 %s，路由 %v", x.peer, name, x.routes)
		// 失败不致命（如离线时没有物理网卡默认路由）；已补上的部分照常在会话结束时删除
		added, undo, err := x.srv.Sys.EnsureScopedDefaults()
		if undo != nil {
			x.undoScoped = undo
			x.srv.Logger.Printf("%s 补物理网卡作用域默认路由 %v", x.peer, added)
		}
		if err != nil {
			x.srv.Logger.Printf("%s 补物理网卡作用域默认路由失败：%v", x.peer, err)
		}
		return Response{OK: true, Name: name, Routes: x.routeStrings()}, fd
	case OpSetDNS:
		if x.tunFd < 0 {
			return Response{Error: "尚未打开 TUN"}, -1
		}
		if x.restoreDNS != nil {
			return Response{Error: "本会话已接管系统 DNS"}, -1
		}
		for _, d := range req.DNS {
			if _, err := netip.ParseAddr(d); err != nil {
				return Response{Error: fmt.Sprintf("DNS 地址无效：%q", d)}, -1
			}
		}
		if len(req.DNS) == 0 {
			return Response{Error: "缺少 DNS 地址"}, -1
		}
		restore, err := x.srv.Sys.SetDNS(req.DNS)
		if err != nil {
			return Response{Error: "设置系统 DNS：" + err.Error()}, -1
		}
		x.restoreDNS = restore
		x.srv.Logger.Printf("%s 接管系统 DNS → %v", x.peer, req.DNS)
		return Response{OK: true, Name: x.name}, -1
	case OpClose:
		x.cleanupLocked("velumd 关闭会话")
		return Response{OK: true}, -1
	case OpAddRoutes, OpDelRoutes:
		if x.tunFd < 0 {
			return Response{Error: "尚未打开 TUN"}, -1
		}
		var err error
		if req.Op == OpAddRoutes {
			err = x.addRoutesLocked(req.Routes)
		} else {
			err = x.delRoutesLocked(req.Routes)
		}
		if err != nil {
			return Response{Error: err.Error(), Routes: x.routeStrings()}, -1
		}
		return Response{OK: true, Name: x.name, Routes: x.routeStrings()}, -1
	}
	return Response{Error: "未知操作 " + req.Op}, -1
}

func (x *session) addRoutesLocked(routes []string) error {
	for _, r := range routes {
		p, err := ValidateRoute(r)
		if err != nil {
			return err
		}
		if x.has(p) {
			continue
		}
		gw, err := x.gateway(p)
		if err != nil {
			return fmt.Errorf("添加路由 %s：%w", p, err)
		}
		if err := x.srv.Sys.AddRoute(p, gw); err != nil {
			if errors.Is(err, ErrRouteExists) {
				return fmt.Errorf("路由 %s 已存在（可能已被其他 TUN / VPN 占用），不覆盖", p)
			}
			return fmt.Errorf("添加路由 %s：%w", p, err)
		}
		x.routes = append(x.routes, p)
	}
	return nil
}

func (x *session) delRoutesLocked(routes []string) error {
	for _, r := range routes {
		p, err := netip.ParsePrefix(r)
		if err != nil {
			return err
		}
		p = p.Masked()
		idx := -1
		for i, q := range x.routes {
			if q == p {
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("路由 %s 不属于本会话", p)
		}
		gw, _ := x.gateway(p)
		if err := x.srv.Sys.DeleteRoute(p, gw); err != nil {
			return fmt.Errorf("删除路由 %s：%w", p, err)
		}
		x.routes = append(x.routes[:idx], x.routes[idx+1:]...)
	}
	return nil
}

func (x *session) has(p netip.Prefix) bool {
	for _, q := range x.routes {
		if q == p {
			return true
		}
	}
	return false
}

func (x *session) routeStrings() []string {
	var out []string
	for _, r := range x.routes {
		out = append(out, r.String())
	}
	return out
}

func (x *session) cleanup(reason string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.cleanupLocked(reason)
}

func (x *session) cleanupLocked(reason string) {
	if x.closed {
		return
	}
	x.closed = true
	start := time.Now()
	var failed []string
	// 先还原 DNS：TUN 关闭后指向 TUN 的系统 DNS 将不可用
	if x.restoreDNS != nil {
		if err := x.restoreDNS(); err != nil {
			failed = append(failed, "还原系统 DNS："+err.Error())
		} else {
			x.srv.Logger.Printf("已还原系统 DNS")
		}
		x.restoreDNS = nil
	}
	for i := len(x.routes) - 1; i >= 0; i-- {
		gw, _ := x.gateway(x.routes[i])
		if err := x.srv.Sys.DeleteRoute(x.routes[i], gw); err != nil {
			failed = append(failed, x.routes[i].String()+": "+err.Error())
		}
	}
	if x.undoScoped != nil {
		if err := x.undoScoped(); err != nil {
			failed = append(failed, "删除作用域默认路由："+err.Error())
		}
		x.undoScoped = nil
	}
	removed := x.routeStrings()
	x.routes = nil
	if x.tunFd >= 0 {
		_ = x.srv.Sys.CloseTun(x.tunFd)
		x.tunFd = -1
	}
	if x.name != "" || len(removed) > 0 {
		x.srv.Logger.Printf("清理 %s（%s）：删除路由 %v，关闭 %s，用时 %s", x.peer, reason, removed, x.name, time.Since(start))
	}
	if len(failed) > 0 {
		x.srv.Logger.Printf("清理时部分路由删除失败：%s", strings.Join(failed, "; "))
	}
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
