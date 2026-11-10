// Package mobilebind 是移动端 App 调用 Go 核心的入口，经 gomobile bind 导出（Android：AAR）。
//
// App 进程内运行完整的 daemon；界面经 HTTP over UDS（Socket 返回的路径）访问，接口与桌面端完全相同。
// Go 做不到的事（建立 VPN、读取当前网络）由 App 实现 Host 接口提供。
//
// 导出的 API 只能使用 gomobile 支持的类型（string、数值、bool、[]byte、error、接口），结构化数据以 JSON 传递。
package mobilebind

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
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

// Socket 返回 daemon 本地接口的地址：SetSocket 指定的地址，否则 home 下的 socket 路径。
func Socket(home string) string {
	mu.Lock()
	defer mu.Unlock()
	if socket != "" {
		return socket
	}
	return config.PathsFor(home).Socket
}

// socket 是 SetSocket 指定的本地接口地址。
var socket string

// SetSocket 指定本地接口地址，在 Start 之前调用。macOS 系统扩展用回环 TCP + 令牌（tcp://127.0.0.1:端口?token=…）：
// 扩展以 root 运行在沙盒里，用户进程连不上它能创建的 socket（docs/MN-PLAN.md Q2）。为空时使用 home 下的 socket。
func SetSocket(addr string) {
	mu.Lock()
	defer mu.Unlock()
	socket = addr
}

// SetLocale 设置核心的语言（错误、告警、修复步骤等），在 Start 之前调用。
func SetLocale(l string) { i18n.SetLocale(l) }

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
		return i18n.New("host 不能为空")
	}
	return start(home, func(paths config.Paths, lf io.Writer, logger *log.Logger) *daemon.Daemon {
		d := daemon.New(paths, singbox.New(singbox.Options{Log: lf}), logger)
		d.Render = render.Renderer
		d.TUNOpener = opener{host}
		d.NetSnapshot = func() (netsense.Snapshot, error) { return netSnapshot(host) }
		if proxyHost != nil {
			d.SysProxy = &hostProxy{h: proxyHost}
		}
		return d
	})
}

// StartSplit 是 iOS 的入口：daemon 在 App 进程，内核在隧道扩展里（core/iostunnel）。
// 本地接口用回环 TCP + 令牌：App Group 路径可能超过 Unix socket 的长度限制。界面经 Call 访问。
func StartSplit(home string, host TunnelHost) error {
	if host == nil {
		return i18n.New("host 不能为空")
	}
	mu.Lock()
	if socket == "" {
		a, err := loopbackAddr()
		if err != nil {
			mu.Unlock()
			return err
		}
		socket = a
	}
	mu.Unlock()
	return start(home, func(paths config.Paths, lf io.Writer, logger *log.Logger) *daemon.Daemon {
		se := newSplitEngine(home, host, singbox.New(singbox.Options{Log: lf}))
		se.onAdopt = TunRequested
		d := daemon.New(paths, se, logger)
		d.Render = render.Renderer
		d.SystemConnectivity = true
		d.NetSnapshot = func() (netsense.Snapshot, error) { return snapshot(host.Network()) }
		return d
	})
}

// Call 发送一次本地接口请求，返回响应正文。接口报错（如校验失败）时 error 是给用户看的原因。
func Call(method, path string, body []byte) ([]byte, error) {
	mu.Lock()
	inst := cur
	mu.Unlock()
	if inst == nil {
		return nil, i18n.New("核心未运行")
	}
	return inst.call(method, path, body)
}

// call 在进程内调用本地接口，不经过端口：App 挂起时系统会回收监听端口。
func (i *instance) call(method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, "http://velumd"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	w := &recorder{header: http.Header{}, code: http.StatusOK}
	i.d.Handler().ServeHTTP(w, req)
	if w.code != http.StatusOK {
		var e rpc.ErrorResponse
		_ = json.Unmarshal(w.body.Bytes(), &e)
		if e.Error == "" {
			e.Error = http.StatusText(w.code)
		}
		return nil, errors.New(e.Error)
	}
	return w.body.Bytes(), nil
}

type recorder struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(code int)        { r.code = code }

func loopbackAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ipc.TCPAddr(l.Addr().String(), hex.EncodeToString(b)), nil
}

func start(home string, build func(config.Paths, io.Writer, *log.Logger) *daemon.Daemon) error {
	mu.Lock()
	defer mu.Unlock()
	if cur != nil {
		return nil
	}
	paths := config.PathsFor(home)
	if socket != "" {
		paths.Socket = socket
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	lf, err := logfile.Open(paths.Log, 10<<20, 3)
	if err != nil {
		return err
	}
	logger := log.New(lf, "[velumd] ", log.LstdFlags|log.Lmicroseconds)
	daemon.RuleSetLoader = singbox.LoadRuleSetFile
	d := build(paths, lf, logger)

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
				err = i18n.New("daemon 意外退出")
			}
			return err
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			d.Shutdown()
			return i18n.New("等待 daemon 就绪超时")
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
func TunRevoked() { switchTUN("off", i18n.T("VPN 被系统撤销")) }

// TunRequested 由 App 在系统直接拉起 VPN 服务时调用（「始终开启的 VPN」、开机自动连接）：打开 TUN 开关。
// 异步执行，立即返回；核心尚未就绪时等待其就绪。
func TunRequested() { switchTUN("on", i18n.T("系统请求连接 VPN")) }

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
		body, _ := json.Marshal(rpc.SwitchRequest{TUN: v})
		if _, err := inst.call(http.MethodPost, "/v1/switches", body); err != nil {
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
		return -1, "", nil, i18n.Errorf("建立 VPN：%w", err)
	}
	if fd < 0 {
		return -1, "", nil, i18n.New("建立 VPN：未获得 TUN 文件描述符（可能未授权 VPN）")
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
		return engine.Owner{}, false, i18n.Errorf("解析连接归属：%w", err)
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
		return s, i18n.Errorf("解析网络状态：%w", err)
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
