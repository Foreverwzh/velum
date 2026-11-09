// Package race 是 Velum 注册到 sing-box 的 DNS 传输：并发向多个上游查询，取最先成功的应答；可带兜底上游。
//
// 对应 mihomo 中 nameserver 写多个上游的语义（sing-box 没有这种类型）。典型订阅写法
// `nameserver: [tls://1.1.1.1, https://doh.pub/dns-query]` 在国内直连时，只用第一个会让每次解析等待
// 境外 DoT（数百毫秒甚至超时）；竞速后由先返回的 doh.pub 应答。
//
// 兜底上游（Fallback，通常是当前网络的系统 DNS）在主上游出错、返回 SERVFAIL / REFUSED，或 FallbackDelay 内没有应答时加入竞速：
// 订阅只写了一个 DoH 时，它被限流（429）或被网络拒绝，所有域名都解析不了。主上游刚失败过（unhealthyFor 内）时，
// 兜底与主上游同时发出，不再每次先等主上游超时；主上游成功一次即恢复。
package race

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/i18n"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

// Type 是渲染到内核配置中的 DNS 服务器类型。
const Type = "velum-race"

// DefaultFallbackDelay 是主上游没有应答时，等多久再加入兜底上游（国内 DoH 通常几十毫秒内应答）。
const DefaultFallbackDelay = 500 * time.Millisecond

// unhealthyFor 是主上游失败后，兜底上游与它同时发出的时长。
const unhealthyFor = 30 * time.Second

// Options 是 race 服务器的配置：Servers 为上游 DNS 服务器的 tag，Fallback 为兜底上游的 tag。
type Options struct {
	Servers  []string `json:"servers"`
	Fallback []string `json:"fallback,omitempty"`
	// FallbackDelay 为 0 时使用 DefaultFallbackDelay。
	FallbackDelay time.Duration `json:"-"`
}

// Register 把 race 传输注册到 sing-box 的 DNS 传输表（创建 box context 时调用）。
func Register(registry *dns.TransportRegistry) {
	dns.RegisterTransport[Options](registry, Type, New)
}

type Transport struct {
	dns.TransportAdapter
	ctx           context.Context
	tags, fbTags  []string
	members, fb   []adapter.DNSTransport
	fallbackDelay time.Duration
	failedAt      atomic.Int64 // 主上游最近一次全部失败的时间（UnixNano），0 表示健康
}

var _ adapter.DNSTransport = (*Transport)(nil)

func New(ctx context.Context, _ log.ContextLogger, tag string, options Options) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, i18n.New("race: 缺少上游 servers")
	}
	delay := options.FallbackDelay
	if delay <= 0 {
		delay = DefaultFallbackDelay
	}
	// 上游与兜底上游作为依赖：先于本传输启动
	deps := append(append([]string(nil), options.Servers...), options.Fallback...)
	return &Transport{TransportAdapter: dns.NewTransportAdapter(Type, tag, deps), ctx: ctx, tags: options.Servers, fbTags: options.Fallback, fallbackDelay: delay}, nil
}

func (t *Transport) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	manager := service.FromContext[adapter.DNSTransportManager](t.ctx)
	if manager == nil {
		return i18n.New("race: 缺少 DNS 传输管理器")
	}
	lookup := func(tags []string) ([]adapter.DNSTransport, error) {
		var out []adapter.DNSTransport
		for _, tag := range tags {
			m, ok := manager.Transport(tag)
			if !ok {
				return nil, i18n.Errorf("race: 上游 DNS 服务器 %q 不存在", tag)
			}
			out = append(out, m)
		}
		return out, nil
	}
	var err error
	if t.members, err = lookup(t.tags); err != nil {
		return err
	}
	t.fb, err = lookup(t.fbTags)
	return err
}

// Reset 在网络变化时调用：换了网络，之前的失败不再说明问题。
func (t *Transport) Reset() { t.failedAt.Store(0) }

type result struct {
	msg      *mDNS.Msg
	err      error
	fallback bool
}

// usable 报告应答是否可用：SERVFAIL / REFUSED 说明这个上游当前处理不了，换别的上游可能成功。
func usable(r result) bool {
	return r.err == nil && r.msg != nil && r.msg.Rcode != mDNS.RcodeServerFailure && r.msg.Rcode != mDNS.RcodeRefused
}

// Exchange 并发查询所有上游，返回第一个可用的应答（含 NXDOMAIN 等正常应答）。主上游失败或迟迟不应答时加入兜底上游。
// 都不可用时，有应答（如 SERVFAIL）返回第一个应答，否则返回各自的错误。
func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if len(t.members) == 1 && len(t.fb) == 0 {
		return t.members[0].Exchange(ctx, message)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // 取得应答后取消其余查询
	ch := make(chan result, len(t.members)+len(t.fb))
	send := func(ups []adapter.DNSTransport, fallback bool) {
		for _, m := range ups {
			go func() {
				resp, err := m.Exchange(ctx, message.Copy())
				ch <- result{resp, err, fallback}
			}()
		}
	}
	send(t.members, false)
	pending, primaryLeft := len(t.members), len(t.members)
	fbSent := len(t.fb) == 0
	startFallback := func() {
		if !fbSent {
			fbSent = true
			send(t.fb, true)
			pending += len(t.fb)
		}
	}
	if at := t.failedAt.Load(); at != 0 && time.Since(time.Unix(0, at)) < unhealthyFor {
		startFallback()
	}
	var timer <-chan time.Time
	if !fbSent {
		tm := time.NewTimer(t.fallbackDelay)
		defer tm.Stop()
		timer = tm.C
	}
	var errs []error
	var answered *mDNS.Msg
	for pending > 0 {
		select {
		case <-timer:
			timer = nil
			startFallback()
		case r := <-ch:
			pending--
			if !r.fallback {
				primaryLeft--
			}
			if usable(r) {
				if !r.fallback {
					t.failedAt.Store(0)
				}
				return r.msg, nil
			}
			if r.err != nil {
				errs = append(errs, r.err)
			} else if answered == nil {
				answered = r.msg
			}
			if !r.fallback && primaryLeft == 0 {
				if ctx.Err() == nil { // 调用方取消不算上游失败
					t.failedAt.Store(time.Now().UnixNano())
				}
				startFallback()
			}
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
