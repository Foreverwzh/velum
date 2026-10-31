// Package mobilebind 是移动端 App 调用 Go 核心的入口，经 gomobile bind 导出（Android：AAR）。
//
// App 进程内运行完整的 daemon；界面经 HTTP over UDS（Socket 返回的路径）访问，接口与桌面端完全相同。
// Go 做不到的事（建立 VPN、读取当前网络）由 App 实现 Host 接口提供。
//
// 导出的 API 只能使用 gomobile 支持的类型（string、数值、bool、[]byte、error、接口），结构化数据以 JSON 传递。
package mobilebind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/logfile"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

// Host 由 App 实现，供 Go 回调。调用发生在 Go 的线程上，实现可以阻塞（但不应超过数秒）。
type Host interface {
	// OpenTun 建立 VPN 并返回 TUN 的文件描述符（所有权交给 Go，由 Go 关闭）。req 为 TunRequest 的 JSON。
	OpenTun(req string) (int32, error)
	// CloseTun 关闭 VPN（FD 已由 Go 关闭），App 停止前台服务。
	CloseTun()
	// Network 返回当前物理网络（Network 的 JSON，不含 VPN）；没有可用网络时返回空字符串。
	Network() string
	// FindOwner 查询 VPN 中一条连接所属的应用（Owner 的 JSON）；找不到时返回空字符串。
	// protocol 为 IP 协议号（6 = TCP，17 = UDP）。
	FindOwner(protocol int32, srcAddr string, srcPort int32, dstAddr string, dstPort int32) (string, error)
}

// Owner 是连接所属的应用。
type Owner struct {
	UID      int32    `json:"uid"`
	Packages []string `json:"packages"`
}

// TunRequest 是建立 VPN 所需的参数，均来自编译器的 RoutePlan。
type TunRequest struct {
	MTU    uint32   `json:"mtu"`
	Inet4  []string `json:"inet4"`         // 地址（CIDR）
	Inet6  []string `json:"inet6"`         // 地址（CIDR）
	Routes []string `json:"routes"`        // 经 VPN 的路由（CIDR，已扣除排除网段）
	DNS    []string `json:"dns,omitempty"` // 非空时把 VPN 的 DNS 设为这些地址（由 Velum 劫持处理）
	// IncludeApps 非空时只有这些应用经 VPN；否则 ExcludeApps 中的应用不经 VPN（Velum 自身总是不经 VPN）。
	IncludeApps []string `json:"include_apps,omitempty"`
	ExcludeApps []string `json:"exclude_apps,omitempty"`
}

// Network 是 App 报告的当前默认网络。
type Network struct {
	Iface   string   `json:"iface"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// Socket 返回 home 下 daemon 本地接口的 socket 路径。
func Socket(home string) string { return config.PathsFor(home).Socket }

// Version 返回核心版本。
func Version() string { return daemon.Version }

type instance struct {
	d    *daemon.Daemon
	done chan struct{}
	lf   io.Closer
}

var (
	mu  sync.Mutex
	cur *instance
)

// Start 在 home 目录启动 daemon，本地接口就绪后返回。已在运行时直接返回。
func Start(home string, host Host) error {
	if host == nil {
		return errors.New("host 不能为空")
	}
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		return nil
	}
	paths := config.PathsFor(home)
	if err := config.Ensure(paths); err != nil {
		return err
	}
	lf, err := logfile.Open(paths.Log, 10<<20, 3)
	if err != nil {
		return err
	}
	logger := log.New(lf, "[velumd] ", log.LstdFlags|log.Lmicroseconds)
	daemon.RuleSetLoader = singbox.LoadRuleSetFile
	d := daemon.New(paths, singbox.New(singbox.Options{Log: lf}), logger)
	d.TUNOpener = opener{host}
	d.NetSnapshot = func() (netsense.Snapshot, error) { return netSnapshot(host) }

	inst := &instance{d: d, done: make(chan struct{}), lf: lf}
	errc := make(chan error, 1)
	go func() {
		defer close(inst.done)
		errc <- d.Run(context.Background())
	}()
	// 等待首次加载完成（本地接口先就绪，随后构建配置、启动引擎）
	deadline := time.Now().Add(15 * time.Second)
	c := rpc.NewClient(paths.Socket)
	for st, err := c.Status(); err != nil || st.Starting; st, err = c.Status() {
		select {
		case err := <-errc:
			lf.Close()
			if err == nil {
				err = errors.New("daemon 意外退出")
			}
			return err
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			d.Shutdown()
			return errors.New("等待 daemon 就绪超时")
		}
	}
	cur = inst
	return nil
}

// Stop 停止 daemon（关闭 VPN），等待其退出。
func Stop() {
	mu.Lock()
	inst := cur
	cur = nil
	mu.Unlock()
	if inst == nil {
		return
	}
	inst.d.Shutdown()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
	}
	_ = inst.lf.Close()
}

// TunRevoked 由 App 在系统撤销 VPN 时调用（用户在系统设置中断开、被其他 VPN 取代）：关闭 TUN 开关，
// 与用户在界面上关闭等效（保存到 state.json）。异步执行，立即返回。
func TunRevoked() { switchTUN("off", "VPN 被系统撤销") }

// TunRequested 由 App 在系统直接拉起 VPN 服务时调用（「始终开启的 VPN」、开机自动连接）：打开 TUN 开关。
// 异步执行，立即返回；核心尚未就绪时等待其就绪。
func TunRequested() { switchTUN("on", "系统请求连接 VPN") }

func switchTUN(v, why string) {
	go func() {
		var inst *instance
		for i := 0; i < 300; i++ { // App 进程刚启动时核心可能尚未就绪，最多等 30 秒
			mu.Lock()
			inst = cur
			mu.Unlock()
			if inst != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if inst == nil {
			return
		}
		if _, err := rpc.NewClient(inst.d.Paths.Socket).Switch(rpc.SwitchRequest{TUN: v}); err != nil {
			inst.d.Logger.Printf("%s，切换 TUN 失败：%v", why, err)
		} else {
			inst.d.Logger.Printf("%s，TUN 已切换为 %s", why, v)
		}
	}()
}

// opener 经 App 的 VpnService 建立 VPN（对应 macOS 上的 velum-helper）。
type opener struct{ host Host }

func (o opener) OpenTUN(r engine.TUNRequest) (int, string, io.Closer, error) {
	req, _ := json.Marshal(TunRequest{MTU: r.MTU, Inet4: r.Inet4, Inet6: r.Inet6, Routes: r.Routes, DNS: r.SystemDNS,
		IncludeApps: r.IncludeApps, ExcludeApps: r.ExcludeApps})
	fd, err := o.host.OpenTun(string(req))
	if err != nil {
		return -1, "", nil, fmt.Errorf("建立 VPN：%w", err)
	}
	if fd < 0 {
		return -1, "", nil, errors.New("建立 VPN：未获得 TUN 文件描述符（可能未授权 VPN）")
	}
	return int(fd), tunName(int(fd)), closerFunc(o.host.CloseTun), nil
}

// FindOwner 实现 engine.OwnerFinder：内核经它识别 VPN 中连接所属的应用。
func (o opener) FindOwner(network string, src, dst netip.AddrPort) (engine.Owner, bool, error) {
	proto := int32(6)
	if network == "udp" {
		proto = 17
	}
	js, err := o.host.FindOwner(proto, src.Addr().String(), int32(src.Port()), dst.Addr().String(), int32(dst.Port()))
	if err != nil || js == "" {
		return engine.Owner{}, false, err
	}
	var w Owner
	if err := json.Unmarshal([]byte(js), &w); err != nil {
		return engine.Owner{}, false, fmt.Errorf("解析连接归属：%w", err)
	}
	return engine.Owner{UID: w.UID, Packages: w.Packages}, true, nil
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

// snapshot 把 App 报告的默认网络转换为 NetworkSense 快照。Android 同时只有一个 VPN（即 Velum），
// 因此快照里只有物理网络：用于获取其 DNS，以及在网络切换（Wi-Fi ↔ 蜂窝）时重新加载。
func snapshot(js string) (netsense.Snapshot, error) {
	var s netsense.Snapshot
	if js == "" {
		return s, nil
	}
	var n Network
	if err := json.Unmarshal([]byte(js), &n); err != nil {
		return s, fmt.Errorf("解析网络状态：%w", err)
	}
	s.PrimaryInterface = n.Iface
	if n.Iface != "" {
		s.Routes = []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: n.Iface, Gateway: n.Gateway}}
	}
	s.DHCPNameservers = n.DNS
	return s, nil
}

// LogPath 返回 home 下的日志文件路径（供 App 的「导出日志」使用）。
func LogPath(home string) string { return filepath.Clean(config.PathsFor(home).Log) }
