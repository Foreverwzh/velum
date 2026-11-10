package mobilebind

import (
	"context"
	"encoding/json"
	"net/netip"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
)

// velumLocal 是 App 内的 velum-engine 本地引擎（没开隧道时测速、解析、切换节点、经代理拉取）。
// 配置写到 tunnelproto.LocalDir，由 Swift 编译并经 velum-ffi 的本地模式启动；消息与隧道相同（tunnelproto）。
type velumLocal struct {
	home string
	host TunnelHost

	mu      sync.Mutex
	running bool
	hash    string
	state   *tunnelproto.State
	stateAt time.Time
}

func newVelumLocal(home string, host TunnelHost) *velumLocal {
	return &velumLocal{home: home, host: host}
}

func (v *velumLocal) Restart(_ context.Context, cfg engine.Config) error {
	if cfg.Velum == nil {
		return i18n.New("本地引擎需要 velum-engine 的配置")
	}
	hash, err := tunnelproto.SaveLocal(v.home, cfg)
	if err != nil {
		return err
	}
	v.mu.Lock()
	same := v.running && hash == v.hash
	v.mu.Unlock()
	if same {
		return nil // 配置没变：不打断本地引擎上的连接
	}
	if err := v.host.LocalStart(); err != nil {
		return err
	}
	v.mu.Lock()
	v.running, v.hash, v.state = true, hash, nil
	v.mu.Unlock()
	return nil
}

// Pause 在隧道接管时停掉本地引擎（隧道开着时 App 的请求都由隧道处理）。
func (v *velumLocal) Pause() error { return v.Stop() }

func (v *velumLocal) Stop() error {
	v.host.LocalStop()
	v.mu.Lock()
	v.running, v.hash, v.state = false, "", nil
	v.mu.Unlock()
	return nil
}

func (v *velumLocal) Running() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.running
}

func (v *velumLocal) call(r tunnelproto.Request) (*tunnelproto.Response, error) {
	if !v.Running() {
		return nil, i18n.New("本地引擎未运行")
	}
	b, _ := json.Marshal(r)
	out, err := v.host.LocalCall(string(b))
	if err != nil {
		return nil, err
	}
	var resp tunnelproto.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return &resp, i18n.Errorf("本地引擎：%s", resp.Error)
	}
	return &resp, nil
}

// cached 是最近一次的状态：界面刷新一次要查每个策略组、每个成员，逐项发消息不现实。
func (v *velumLocal) cached() *tunnelproto.State {
	v.mu.Lock()
	if v.state != nil && time.Since(v.stateAt) < stateTTL {
		s := v.state
		v.mu.Unlock()
		return s
	}
	v.mu.Unlock()
	resp, err := v.call(tunnelproto.Request{Op: tunnelproto.OpState})
	if err != nil || resp.State == nil {
		return &tunnelproto.State{}
	}
	v.mu.Lock()
	v.state, v.stateAt = resp.State, time.Now()
	v.mu.Unlock()
	return resp.State
}

func (v *velumLocal) Connections(limit int) []engine.Connection {
	resp, err := v.call(tunnelproto.Request{Op: tunnelproto.OpConns, Limit: limit})
	if err != nil {
		return nil
	}
	return resp.Connections
}

// Subscribe：本地引擎不推送连接事件（界面按需查询 Connections）。
func (v *velumLocal) Subscribe() (<-chan engine.Connection, func()) {
	return make(chan engine.Connection), func() {}
}

func (v *velumLocal) Selected(tag string) string { return v.cached().Selected[tag] }

func (v *velumLocal) Delay(tag string) int { return v.cached().Delays[tag] }

func (v *velumLocal) SelectMember(group, member string) error {
	_, err := v.call(tunnelproto.Request{Op: tunnelproto.OpSelect, Group: group, Member: member})
	v.mu.Lock()
	v.state = nil
	v.mu.Unlock()
	return err
}

func (v *velumLocal) TestGroup(_ context.Context, group string) (map[string]int, error) {
	resp, err := v.call(tunnelproto.Request{Op: tunnelproto.OpTest, Group: group})
	v.mu.Lock()
	v.state = nil
	v.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return resp.Delays, nil
}

func (v *velumLocal) Traffic() engine.Traffic { return v.cached().Traffic }

func (v *velumLocal) Resolve(_ context.Context, domain string) ([]netip.Addr, error) {
	resp, err := v.call(tunnelproto.Request{Op: tunnelproto.OpResolve, Domain: domain})
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range resp.Addrs {
		if ip, err := netip.ParseAddr(a); err == nil {
			out = append(out, ip)
		}
	}
	return out, nil
}
