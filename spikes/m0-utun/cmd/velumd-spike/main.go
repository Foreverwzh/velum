// velumd-spike：M0 验证用的用户态核心（不需要 root）。
//
// 验证点：
//  1. 只实现 sing-box 内部 adapter.PlatformInterface 的 OpenInterface，其余能力全部回落 sing-box 原生实现；
//  2. 通过 UDS 向 helper 申请 utun，经 SCM_RIGHTS 拿到 FD 交给 sing-tun；
//  3. 路由由 helper 负责；运行期增删路由（SIGUSR1 / SIGUSR2）不重启引擎；
//  4. sing-box 原生进程归属查询（find_process）在外部 FD 模式下是否可用。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Foreverwzh/velum/spikes/m0-utun/internal/proto"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

var (
	sockPath  = flag.String("sock", "/tmp/velum-spike/helper.sock", "helper UDS 路径")
	tunAddr   = flag.String("tun-addr", "172.19.0.1/30", "utun IPv4 地址")
	directIP  = flag.String("direct-ip", "223.6.6.6/32", "测试：经 utun → 物理网卡直连")
	stashIP   = flag.String("stash-ip", "9.9.9.9/32", "测试：经 utun → Stash(HTTP 代理)")
	lateIP    = flag.String("late-ip", "149.112.112.112/32", "测试：运行期由 SIGUSR1/SIGUSR2 动态增删的路由（走 Stash）")
	phys      = flag.String("phys", "en0", "物理网卡")
	stashAddr = flag.String("stash", "127.0.0.1:7890", "Stash HTTP 代理地址")
	logLevel  = flag.String("log-level", "debug", "sing-box 日志级别")
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.SetPrefix("[velumd] ")

	hc, err := dialHelper(*sockPath)
	if err != nil {
		log.Fatalf("connect helper: %v", err)
	}
	defer hc.conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = box.Context(ctx,
		include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())

	plat := &platform{helper: hc, routes: []string{*directIP, *stashIP}}
	ctx = service.ContextWith[adapter.PlatformInterface](ctx, plat)

	cfg := renderConfig()
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(cfg))
	if err != nil {
		log.Fatalf("decode config: %v\n%s", err, cfg)
	}
	start := time.Now()
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		log.Fatalf("box.New: %v", err)
	}
	if err := instance.Start(); err != nil {
		log.Fatalf("box.Start: %v", err)
	}
	log.Printf("ENGINE STARTED in %s on %s (pid=%d)", time.Since(start), plat.tunName, os.Getpid())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)
	for s := range sig {
		switch s {
		case syscall.SIGUSR1, syscall.SIGUSR2:
			op := proto.OpAddRoute
			if s == syscall.SIGUSR2 {
				op = proto.OpDelRoute
			}
			t := time.Now()
			resp, _, err := hc.call(proto.Request{Op: op, Routes: []string{*lateIP}})
			log.Printf("DYNAMIC %s %s -> ok=%v err=%v %s (%s, engine not restarted)", op, *lateIP, resp.OK, err, resp.Error, time.Since(t))
			continue
		}
		log.Printf("signal %s, closing", s)
		break
	}
	instance.Close()
	log.Printf("engine closed")
}

func renderConfig() string {
	host, port, _ := net.SplitHostPort(*stashAddr)
	return fmt.Sprintf(`{
  "log": {"level": %q, "timestamp": true, "output": "stderr"},
  "inbounds": [{
    "type": "tun", "tag": "tun-in",
    "address": [%q], "mtu": 9000,
    "auto_route": false, "stack": "system"
  }],
  "outbounds": [
    {"type": "direct", "tag": "direct-phys", "bind_interface": %q},
    {"type": "http", "tag": "via-stash", "server": %q, "server_port": %s}
  ],
  "route": {
    "auto_detect_interface": false,
    "find_process": true,
    "rules": [
      {"action": "sniff"},
      {"ip_cidr": [%q], "outbound": "direct-phys"},
      {"ip_cidr": [%q, %q], "outbound": "via-stash"}
    ],
    "final": "via-stash"
  }
}`, *logLevel, *tunAddr, *phys, host, port, *directIP, *stashIP, *lateIP)
}

// ---------------- helper 客户端 ----------------

type helperClient struct {
	mu   sync.Mutex
	conn *net.UnixConn
}

func dialHelper(path string) (*helperClient, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return &helperClient{conn: c}, nil
}

func (h *helperClient) call(req proto.Request) (proto.Response, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := proto.Send(h.conn, req, -1); err != nil {
		return proto.Response{}, -1, err
	}
	var resp proto.Response
	fd, err := proto.Recv(h.conn, &resp)
	if err != nil {
		return resp, -1, err
	}
	if !resp.OK {
		return resp, fd, fmt.Errorf("helper: %s", resp.Error)
	}
	return resp, fd, nil
}

// ---------------- PlatformInterface ----------------

// platform 只接管「打开 TUN」这一件事；其余 UsePlatformXxx 全部返回 false，
// 让 sing-box 使用其 darwin 原生实现（网卡监听、进程查询、网卡列表等）。
type platform struct {
	helper  *helperClient
	routes  []string
	tunName string
	myAddr  []netip.Addr
}

var _ adapter.PlatformInterface = (*platform)(nil)

func (p *platform) Initialize(adapter.NetworkManager) error { return nil }

func (p *platform) UsePlatformAutoDetectInterfaceControl() bool { return false }
func (p *platform) AutoDetectInterfaceControl(int) error        { return nil }

func (p *platform) UsePlatformInterface() bool { return true }

func (p *platform) OpenInterface(options *tun.Options, _ option.TunPlatformOptions) (tun.Tun, error) {
	var inet4 []string
	for _, a := range options.Inet4Address {
		inet4 = append(inet4, a.String())
		p.myAddr = append(p.myAddr, a.Addr())
	}
	// 复用 sing-tun 自己的路由推导：darwin 上至少包含 TUN 自身网段（如 172.19.0.0/30），
	// 否则 system 栈 NAT 后的回包（→172.19.0.2）会落到默认路由上丢失。
	// 生产版里这份列表应来自 Velum 的 RoutePlan，再由 helper 执行。
	routes := append([]string(nil), p.routes...)
	if ranges, err := options.BuildAutoRouteRanges(false); err == nil {
		for _, r := range ranges {
			if r.Addr().Is4() {
				routes = append(routes, r.String())
			}
		}
	}
	t := time.Now()
	resp, fd, err := p.helper.call(proto.Request{Op: proto.OpOpenTun, MTU: options.MTU, Inet4: inet4, Routes: routes})
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, fmt.Errorf("helper did not pass fd")
	}
	log.Printf("received utun fd=%d name=%s from helper in %s", fd, resp.Name, time.Since(t))
	p.tunName = resp.Name
	options.Name = resp.Name
	options.FileDescriptor = fd
	if options.InterfaceMonitor != nil {
		options.InterfaceMonitor.RegisterMyInterface(resp.Name)
	}
	return tun.New(*options)
}

func (p *platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }

func (p *platform) UsePlatformDefaultInterfaceMonitor() bool { return false }
func (p *platform) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return nil
}

func (p *platform) UsePlatformNetworkInterfaces() bool { return false }
func (p *platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	return nil, os.ErrInvalid
}

func (p *platform) UnderNetworkExtension() bool              { return false }
func (p *platform) NetworkExtensionIncludeAllNetworks() bool { return false }
func (p *platform) ClearDNSCache()                           {}
func (p *platform) RequestPermissionForWIFIState() error     { return nil }
func (p *platform) ReadWIFIState(context.Context) adapter.WIFIState {
	return adapter.WIFIState{}
}

// macOS 上非 root 进程读 pcblist_n 只能看到自己的 socket，进程查询委托给 root helper。
func (p *platform) UsePlatformConnectionOwnerFinder() bool { return true }
func (p *platform) FindConnectionOwner(r *adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	network := "tcp"
	if r.IpProtocol == syscall.IPPROTO_UDP {
		network = "udp"
	}
	src := net.JoinHostPort(r.SourceAddress, fmt.Sprint(r.SourcePort))
	dst := net.JoinHostPort(r.DestinationAddress, fmt.Sprint(r.DestinationPort))
	t := time.Now()
	resp, _, err := p.helper.call(proto.Request{Op: proto.OpFindOwner, Network: network, Src: src, Dst: dst})
	if err != nil {
		log.Printf("OWNER %s %s->%s: %v (%s)", network, src, dst, err, time.Since(t))
		return nil, err
	}
	log.Printf("OWNER %s %s->%s: pid=%d path=%s (%s)", network, src, dst, resp.PID, resp.Path, time.Since(t))
	return &adapter.ConnectionOwner{ProcessID: resp.PID, UserId: resp.UID, ProcessPath: resp.Path}, nil
}

func (p *platform) UsePlatformWIFIMonitor() bool                              { return false }
func (p *platform) UsePlatformNotification() bool                             { return false }
func (p *platform) SendNotification(*adapter.Notification) error              { return nil }
func (p *platform) CancelNotification(string, int32) error                    { return nil }
func (p *platform) MyInterfaceAddress() []netip.Addr                          { return p.myAddr }
func (p *platform) UsePlatformNeighborResolver() bool                         { return false }
func (p *platform) StartNeighborMonitor(adapter.NeighborUpdateListener) error { return os.ErrInvalid }
func (p *platform) CloseNeighborMonitor(adapter.NeighborUpdateListener) error { return nil }
func (p *platform) UsePlatformShell() bool                                    { return false }
func (p *platform) CheckPlatformShell() error                                 { return nil }
func (p *platform) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, os.ErrInvalid
}
func (p *platform) LookupUser(string) (*adapter.PlatformUser, error) { return nil, os.ErrInvalid }
func (p *platform) LookupSFTPServer() (string, error)                { return "", os.ErrInvalid }
func (p *platform) ReadSystemSSHHostKey() ([]byte, error)            { return nil, os.ErrInvalid }
func (p *platform) TailscaleHostname() string                        { return "" }
func (p *platform) UsePlatformBridge() bool                          { return false }
func (p *platform) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, os.ErrInvalid
}
