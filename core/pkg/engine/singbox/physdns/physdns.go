// Package physdns 是 Velum 注册到 sing-box 的 DNS 传输：询问物理网络（当前 Wi-Fi / 有线 / 蜂窝）的 DNS。
//
// 地址在运行时由引擎更新（Servers.Set），不写进内核配置：换 Wi-Fi、换网时物理网络的 DNS 常会变化，
// 写进配置就要重启内核，所有连接（聊天软件的长连接等）随之断开。网络下发了多个 DNS 时并发询问，取最先可用的应答，
// 其中一个不可用不影响解析。
package physdns

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Foreverwzh/velum/core/pkg/i18n"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

// Type 是渲染到内核配置中的 DNS 服务器类型。
const Type = "velum-physical"

// Options 没有字段：地址来自 context 中的 Servers。
type Options struct{}

// Servers 保存物理网络的 DNS 地址。由引擎持有，跨内核重启保留；内核里的传输在下次查询时按新地址重建。
type Servers struct {
	mu    sync.Mutex
	addrs []string
	ver   atomic.Uint64
}

// Set 更新地址（IP，或 IP:端口），返回是否有变化。
func (s *Servers) Set(addrs []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Equal(s.addrs, addrs) {
		return false
	}
	s.addrs = slices.Clone(addrs)
	s.ver.Add(1)
	return true
}

// Get 返回当前地址。
func (s *Servers) Get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.addrs)
}

// With 把 Servers 放进内核的 context，供传输读取。
func With(ctx context.Context, s *Servers) context.Context {
	return service.ContextWith[*Servers](ctx, s)
}

// Register 把传输注册到 sing-box 的 DNS 传输表。
func Register(registry *dns.TransportRegistry) {
	dns.RegisterTransport[Options](registry, Type, New)
}

type state struct {
	ver   uint64
	addrs []string
	ups   []adapter.DNSTransport
	scope *adapter.Scope
}

type Transport struct {
	dns.TransportAdapter
	ctx     context.Context
	logger  log.ContextLogger
	dialer  N.Dialer
	servers *Servers

	mu     sync.Mutex // 重建 state
	state  atomic.Pointer[state]
	closed bool
}

var _ adapter.DNSTransport = (*Transport)(nil)

func New(ctx context.Context, logger log.ContextLogger, tag string, _ Options) (adapter.DNSTransport, error) {
	servers := service.FromContext[*Servers](ctx)
	if servers == nil {
		return nil, i18n.New("physdns: 缺少物理网络 DNS 的地址来源")
	}
	// 与 local / dhcp 相同的直连拨号器：TUN 模式下绑定物理网卡，查询不会进隧道绕回 Velum
	d, err := dns.NewLocalDialer(ctx, option.LocalDNSServerOptions{})
	if err != nil {
		return nil, err
	}
	return &Transport{TransportAdapter: dns.NewTransportAdapter(Type, tag, nil), ctx: ctx, logger: logger, dialer: d, servers: servers}, nil
}

func (t *Transport) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateStart && scope != nil {
		scope.Add(t.Close)
	}
	return nil
}

// Close 关闭当前的上游连接；之后不再查询。
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if st := t.state.Swap(nil); st != nil && st.scope != nil {
		return st.scope.Close()
	}
	return nil
}

// Reset 在网络变化时调用：丢弃上游的连接。
func (t *Transport) Reset() {
	if st := t.state.Load(); st != nil {
		for _, u := range st.ups {
			u.Reset()
		}
	}
}

// current 返回与 Servers 当前地址对应的上游；地址变化后重建。
func (t *Transport) current() (*state, error) {
	ver := t.servers.ver.Load()
	if st := t.state.Load(); st != nil && st.ver == ver {
		return st, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, i18n.New("physdns: 已关闭")
	}
	ver = t.servers.ver.Load()
	if st := t.state.Load(); st != nil && st.ver == ver {
		return st, nil
	}
	addrs := t.servers.Get()
	st := &state{ver: ver, addrs: addrs, scope: adapter.NewScope(t.ctx, t.logger)}
	for _, a := range addrs {
		sa, err := serverAddr(a)
		if err != nil {
			continue
		}
		u := transport.NewUDPRaw(t.logger, dns.NewTransportAdapter(C.DNSTypeUDP, "", nil), t.dialer, sa)
		if err := u.Start(adapter.StartStateStart, st.scope); err != nil {
			continue
		}
		st.ups = append(st.ups, u)
	}
	if old := t.state.Swap(st); old != nil && old.scope != nil {
		_ = old.scope.Close()
	}
	return st, nil
}

func serverAddr(s string) (M.Socksaddr, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return M.SocksaddrFromNetIP(ap), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return M.Socksaddr{}, err
	}
	return M.SocksaddrFrom(a, 53), nil
}

type result struct {
	msg *mDNS.Msg
	err error
}

func usable(r result) bool {
	return r.err == nil && r.msg != nil && r.msg.Rcode != mDNS.RcodeServerFailure && r.msg.Rcode != mDNS.RcodeRefused
}

// Exchange 并发询问物理网络的各个 DNS，返回第一个可用的应答；都不可用时返回第一个应答或各自的错误。
func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	st, err := t.current()
	if err != nil {
		return nil, err
	}
	switch len(st.ups) {
	case 0:
		return nil, i18n.New("未获取到物理网络的 DNS")
	case 1:
		return st.ups[0].Exchange(ctx, message)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan result, len(st.ups))
	for _, u := range st.ups {
		go func() {
			resp, err := u.Exchange(ctx, message.Copy())
			ch <- result{resp, err}
		}()
	}
	var errs []error
	var answered *mDNS.Msg
	for range st.ups {
		r := <-ch
		if usable(r) {
			return r.msg, nil
		}
		if r.err != nil {
			errs = append(errs, r.err)
		} else if answered == nil {
			answered = r.msg
		}
	}
	if answered != nil {
		return answered, nil
	}
	return nil, errors.Join(errs...)
}

func (t *Transport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(t.Exchange(ctx, message))
}
