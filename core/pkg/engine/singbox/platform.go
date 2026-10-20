package singbox

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sync"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/logger"
)

// platform 只接管「打开 TUN」：由 velum-helper 创建 utun 并设置路由，经 SCM_RIGHTS 交回 FD
// （M0 spikes/m0-utun）。其余能力全部回落 sing-box 原生实现（网卡监听、进程查询等）。
type platform struct {
	cfg *engine.TUNConfig

	mu       sync.Mutex
	releases []io.Closer
	name     string
	myAddr   []netip.Addr
}

var _ adapter.PlatformInterface = (*platform)(nil)

func (p *platform) Initialize(adapter.NetworkManager) error     { return nil }
func (p *platform) UsePlatformAutoDetectInterfaceControl() bool { return false }
func (p *platform) AutoDetectInterfaceControl(int) error        { return nil }
func (p *platform) UsePlatformInterface() bool                  { return true }

func (p *platform) OpenInterface(options *tun.Options, _ option.TunPlatformOptions) (tun.Tun, error) {
	var inet4, inet6 []string
	for _, a := range options.Inet4Address {
		inet4 = append(inet4, a.String())
	}
	for _, a := range options.Inet6Address {
		inet6 = append(inet6, a.String())
	}
	fd, name, release, err := p.cfg.Opener.OpenTUN(engine.TUNRequest{MTU: options.MTU, Inet4: inet4, Inet6: inet6,
		Routes: p.cfg.Routes, SystemDNS: p.cfg.SystemDNS, IncludeApps: p.cfg.IncludeApps, ExcludeApps: p.cfg.ExcludeApps})
	if err != nil {
		return nil, fmt.Errorf("打开 TUN：%w", err)
	}
	p.mu.Lock()
	p.releases = append(p.releases, release)
	p.name = name
	for _, a := range options.Inet4Address {
		p.myAddr = append(p.myAddr, a.Addr())
	}
	p.mu.Unlock()
	options.Name, options.FileDescriptor = name, fd
	if options.InterfaceMonitor != nil {
		options.InterfaceMonitor.RegisterMyInterface(name)
	}
	return tun.New(*options)
}

// release 释放 helper 会话：helper 删除路由并销毁 utun。
func (p *platform) release() {
	p.mu.Lock()
	rs := p.releases
	p.releases = nil
	p.mu.Unlock()
	for _, r := range rs {
		_ = r.Close()
	}
}

func (p *platform) tunName() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

func (p *platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }
func (p *platform) UsePlatformDefaultInterfaceMonitor() bool               { return false }
func (p *platform) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return nil
}
func (p *platform) UsePlatformNetworkInterfaces() bool { return false }
func (p *platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	return nil, os.ErrInvalid
}
func (p *platform) UnderNetworkExtension() bool                     { return false }
func (p *platform) NetworkExtensionIncludeAllNetworks() bool        { return false }
func (p *platform) ClearDNSCache()                                  {}
func (p *platform) RequestPermissionForWIFIState() error            { return nil }
func (p *platform) ReadWIFIState(context.Context) adapter.WIFIState { return adapter.WIFIState{} }
func (p *platform) UsePlatformConnectionOwnerFinder() bool {
	_, ok := p.cfg.Opener.(engine.OwnerFinder)
	return ok
}

// FindConnectionOwner 经平台查询连接所属应用。包名同时写入 ProcessPath：process_name 规则按其文件名匹配，
// 因此 PROCESS-NAME / overlay processes 中的包名在 Android 上直接生效（与 mihomo 的语义一致）。
func (p *platform) FindConnectionOwner(req *adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	f, ok := p.cfg.Opener.(engine.OwnerFinder)
	if !ok {
		return nil, os.ErrInvalid
	}
	network := "tcp"
	if req.IpProtocol == 17 { // IPPROTO_UDP
		network = "udp"
	}
	src, err1 := netip.ParseAddr(req.SourceAddress)
	dst, err2 := netip.ParseAddr(req.DestinationAddress)
	if err1 != nil || err2 != nil {
		return nil, os.ErrInvalid
	}
	o, found, err := f.FindOwner(network, netip.AddrPortFrom(src, uint16(req.SourcePort)), netip.AddrPortFrom(dst, uint16(req.DestinationPort)))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	owner := &adapter.ConnectionOwner{UserId: o.UID, AndroidPackageNames: o.Packages}
	if len(o.Packages) > 0 {
		owner.ProcessPath = o.Packages[0]
	}
	return owner, nil
}
func (p *platform) UsePlatformWIFIMonitor() bool                 { return false }
func (p *platform) UsePlatformNotification() bool                { return false }
func (p *platform) SendNotification(*adapter.Notification) error { return nil }
func (p *platform) CancelNotification(string, int32) error       { return nil }
func (p *platform) MyInterfaceAddress() []netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.myAddr
}
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
