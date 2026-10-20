// Package race 是 Velum 注册到 sing-box 的 DNS 传输：并发向多个上游查询，取最先成功的应答。
//
// 对应 mihomo 中 nameserver 写多个上游的语义（sing-box 没有这种类型）。典型订阅写法
// `nameserver: [tls://1.1.1.1, https://doh.pub/dns-query]` 在国内直连时，只用第一个会让每次解析等待
// 境外 DoT（数百毫秒甚至超时）；竞速后由先返回的 doh.pub 应答。
package race

import (
	"context"
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

// Type 是渲染到内核配置中的 DNS 服务器类型。
const Type = "velum-race"

// Options 是 race 服务器的配置：Servers 为上游 DNS 服务器的 tag。
type Options struct {
	Servers []string `json:"servers"`
}

// Register 把 race 传输注册到 sing-box 的 DNS 传输表（创建 box context 时调用）。
func Register(registry *dns.TransportRegistry) {
	dns.RegisterTransport[Options](registry, Type, New)
}

type Transport struct {
	dns.TransportAdapter
	ctx     context.Context
	tags    []string
	members []adapter.DNSTransport
}

var _ adapter.DNSTransport = (*Transport)(nil)

func New(ctx context.Context, _ log.ContextLogger, tag string, options Options) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, errors.New("race: 缺少上游 servers")
	}
	// 上游作为依赖：先于本传输启动
	return &Transport{TransportAdapter: dns.NewTransportAdapter(Type, tag, options.Servers), ctx: ctx, tags: options.Servers}, nil
}

func (t *Transport) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	manager := service.FromContext[adapter.DNSTransportManager](t.ctx)
	if manager == nil {
		return errors.New("race: 缺少 DNS 传输管理器")
	}
	for _, tag := range t.tags {
		m, ok := manager.Transport(tag)
		if !ok {
			return fmt.Errorf("race: 上游 DNS 服务器 %q 不存在", tag)
		}
		t.members = append(t.members, m)
	}
	return nil
}

func (t *Transport) Reset() {}

type result struct {
	msg *mDNS.Msg
	err error
}

// Exchange 并发查询所有上游，返回第一个成功的应答（含 NXDOMAIN 等正常应答）；全部失败时返回各自的错误。
func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if len(t.members) == 1 {
		return t.members[0].Exchange(ctx, message)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // 取得应答后取消其余查询
	ch := make(chan result, len(t.members))
	for _, m := range t.members {
		go func() {
			resp, err := m.Exchange(ctx, message.Copy())
			ch <- result{resp, err}
		}()
	}
	var errs []error
	for range t.members {
		r := <-ch
		if r.err == nil && r.msg != nil {
			return r.msg, nil
		}
		errs = append(errs, r.err)
	}
	return nil, errors.Join(errs...)
}

func (t *Transport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(t.Exchange(ctx, message))
}
