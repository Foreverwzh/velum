package mobilebind

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
)

// TunnelHost 由 iOS App 实现：隧道在另一个进程（扩展）里，App 只能启停它、给它发消息。
// 调用发生在 Go 的线程上，实现可以阻塞。
type TunnelHost interface {
	// Network 与 Host.Network 相同。
	Network() string
	// StartTunnel 启动隧道（第一次会弹出系统的 VPN 授权），阻塞到已连接；失败时返回给用户看的原因。
	StartTunnel() error
	// StopTunnel 停止隧道，阻塞到已断开。
	StopTunnel()
	// TunnelActive 表示隧道正在运行。
	TunnelActive() bool
	// TunnelCall 发送一条消息（tunnelproto.Request 的 JSON），返回回复。
	TunnelCall(req string) (string, error)
}

// stateTTL 是引擎状态的缓存时间：一次刷新要查几百次选择和延迟，不能每次都发消息给扩展。
const stateTTL = time.Second

// splitEngine 是 iOS 上 daemon 使用的引擎。开启隧道时内核在扩展里，这里只转发；
// 没有隧道时在 App 内运行本地内核，未连接时也能测速、解析、切换节点。
type splitEngine struct {
	home  string
	host  TunnelHost
	local *singbox.Engine
	// onAdopt 在发现隧道已被系统拉起、而保存的开关是关时调用。
	onAdopt func()

	mu       sync.Mutex
	remote   bool // 当前配置要求隧道
	started  bool
	selected map[string]string
	state    *tunnelproto.State
	stateAt  time.Time
}

func newSplitEngine(home string, host TunnelHost, local *singbox.Engine) *splitEngine {
	return &splitEngine{home: home, host: host, local: local, selected: tunnelproto.LoadSelected(home)}
}

// localConfig 是 App 内本地引擎的配置。缓存文件（bbolt）运行时一直持有文件锁，而 home 在 App Group 里：
// App 挂起时还占着共享目录的文件锁，会被系统杀掉（0xdead10cc）。留空后缓存放在 App 自己的临时目录。
func localConfig(cfg engine.Config) engine.Config {
	cfg.CachePath = ""
	return cfg
}

func (e *splitEngine) Start(ctx context.Context, cfg engine.Config) error { return e.apply(ctx, cfg) }

func (e *splitEngine) Restart(ctx context.Context, cfg engine.Config) error { return e.apply(ctx, cfg) }

func (e *splitEngine) apply(ctx context.Context, cfg engine.Config) error {
	e.mu.Lock()
	first := !e.started
	e.started = true
	e.state = nil
	e.mu.Unlock()

	if cfg.TUN == nil {
		// 第一次加载时隧道已在运行（按需连接由系统拉起，App 才启动）：不停掉它，把开关同步过去。
		if first && e.host.TunnelActive() && e.onAdopt != nil {
			e.onAdopt()
		} else if e.host.TunnelActive() {
			e.host.StopTunnel()
		}
		e.setRemote(false)
		return e.local.Restart(ctx, localConfig(cfg))
	}

	if e.local.Running() {
		if err := e.local.Pause(); err != nil {
			return err
		}
	}
	active := e.host.TunnelActive()
	prev := tunnelproto.SavedHash(e.home)
	hash, err := tunnelproto.Save(e.home, cfg)
	if err != nil {
		return err
	}
	switch {
	case !active:
		if err := e.host.StartTunnel(); err != nil {
			return err
		}
	case hash != prev:
		if _, err := e.call(tunnelproto.Request{Op: tunnelproto.OpReload}); err != nil {
			return err
		}
	}
	// 配置没变且隧道已在运行（App 重新打开）：不打断现有连接。
	e.setRemote(true)
	return nil
}

func (e *splitEngine) setRemote(v bool) {
	e.mu.Lock()
	e.remote = v
	e.mu.Unlock()
}

func (e *splitEngine) isRemote() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.remote
}

// Stop 只停 App 内的本地内核。App 退出不代表用户要断开，隧道继续运行。
func (e *splitEngine) Stop() error { return e.local.Stop() }

func (e *splitEngine) Running() bool {
	if e.isRemote() {
		return e.host.TunnelActive()
	}
	return e.local.Running()
}

func (e *splitEngine) call(r tunnelproto.Request) (*tunnelproto.Response, error) {
	b, _ := json.Marshal(r)
	s, err := e.host.TunnelCall(string(b))
	if err != nil {
		return nil, err
	}
	var resp tunnelproto.Response
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return &resp, nil
}

// remoteState 返回扩展的状态（带缓存）；隧道不在或暂时查不到时为空。
func (e *splitEngine) remoteState() *tunnelproto.State {
	e.mu.Lock()
	if e.state != nil && time.Since(e.stateAt) < stateTTL {
		s := e.state
		e.mu.Unlock()
		return s
	}
	e.mu.Unlock()
	resp, err := e.call(tunnelproto.Request{Op: tunnelproto.OpState})
	if err != nil || resp.State == nil {
		return nil
	}
	e.mu.Lock()
	e.state, e.stateAt = resp.State, time.Now()
	e.mu.Unlock()
	return resp.State
}

func (e *splitEngine) Connections(limit int) []engine.Connection {
	if !e.isRemote() {
		return e.local.Connections(limit)
	}
	resp, err := e.call(tunnelproto.Request{Op: tunnelproto.OpConns, Limit: limit})
	if err != nil {
		return nil
	}
	return resp.Connections
}

// Subscribe 只有本地内核有事件。iOS 界面轮询连接列表，不依赖事件流。
func (e *splitEngine) Subscribe() (<-chan engine.Connection, func()) { return e.local.Subscribe() }

func (e *splitEngine) Selected(tag string) string {
	if !e.isRemote() {
		return e.local.Selected(tag)
	}
	if s := e.remoteState(); s != nil {
		return s.Selected[tag]
	}
	return ""
}

func (e *splitEngine) Delay(tag string) int {
	if !e.isRemote() {
		return e.local.Delay(tag)
	}
	if s := e.remoteState(); s != nil {
		return s.Delays[tag]
	}
	return 0
}

func (e *splitEngine) SelectMember(group, member string) error {
	var err error
	if e.isRemote() {
		_, err = e.call(tunnelproto.Request{Op: tunnelproto.OpSelect, Group: group, Member: member})
	} else {
		err = e.local.SelectMember(group, member)
	}
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.selected == nil {
		e.selected = map[string]string{}
	}
	e.selected[group] = member
	e.state = nil
	return tunnelproto.SaveSelected(e.home, e.selected)
}

func (e *splitEngine) TestGroup(ctx context.Context, group string) (map[string]int, error) {
	if !e.isRemote() {
		return e.local.TestGroup(ctx, group)
	}
	resp, err := e.call(tunnelproto.Request{Op: tunnelproto.OpTest, Group: group})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.state = nil
	e.mu.Unlock()
	return resp.Delays, nil
}

func (e *splitEngine) Traffic() engine.Traffic {
	if !e.isRemote() {
		return e.local.Traffic()
	}
	if s := e.remoteState(); s != nil {
		return s.Traffic
	}
	return engine.Traffic{}
}

func (e *splitEngine) Resolve(ctx context.Context, domain string) ([]netip.Addr, error) {
	if !e.isRemote() {
		return e.local.Resolve(ctx, domain)
	}
	resp, err := e.call(tunnelproto.Request{Op: tunnelproto.OpResolve, Domain: domain})
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, s := range resp.Addrs {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

// TUNName 让 daemon 知道 TUN 已生效。
func (e *splitEngine) TUNName() string {
	if !e.isRemote() {
		return ""
	}
	if s := e.remoteState(); s != nil && s.TUNName != "" {
		return s.TUNName
	}
	return "utun"
}
