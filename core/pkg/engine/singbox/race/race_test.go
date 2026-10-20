package race

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

type fakeUpstream struct {
	dns.TransportAdapter
	delay time.Duration
	err   error
	id    uint16 // 应答中的 Rcode 用于区分来源
}

func (f *fakeUpstream) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (f *fakeUpstream) Reset()                                         {}
func (f *fakeUpstream) Exchange(ctx context.Context, m *mDNS.Msg) (*mDNS.Msg, error) {
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	r := new(mDNS.Msg)
	r.SetReply(m)
	r.Id = f.id
	return r, nil
}
func (f *fakeUpstream) ExchangeAsync(ctx context.Context, m *mDNS.Msg, cb func(*mDNS.Msg, error)) {
	cb(f.Exchange(ctx, m))
}

type fakeManager struct {
	adapter.DNSTransportManager
	m map[string]adapter.DNSTransport
}

func (f fakeManager) Transport(tag string) (adapter.DNSTransport, bool) {
	t, ok := f.m[tag]
	return t, ok
}

func newRace(t *testing.T, ups map[string]*fakeUpstream, order ...string) adapter.DNSTransport {
	t.Helper()
	m := map[string]adapter.DNSTransport{}
	for k, v := range ups {
		v.TransportAdapter = dns.NewTransportAdapter("fake", k, nil)
		m[k] = v
	}
	ctx := service.ContextWith[adapter.DNSTransportManager](context.Background(), fakeManager{m: m})
	tr, err := New(ctx, nil, "ns", Options{Servers: order})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(adapter.StartStateStart, nil); err != nil {
		t.Fatal(err)
	}
	return tr
}

func query() *mDNS.Msg {
	m := new(mDNS.Msg)
	m.SetQuestion("www.qq.com.", mDNS.TypeA)
	return m
}

// 慢的上游（如境外 DoT）不拖慢应答：取最先成功的。
func TestRaceFastestWins(t *testing.T) {
	tr := newRace(t, map[string]*fakeUpstream{"slow": {delay: 2 * time.Second, id: 1}, "fast": {delay: 10 * time.Millisecond, id: 2}}, "slow", "fast")
	start := time.Now()
	r, err := tr.Exchange(context.Background(), query())
	if err != nil || r.Id != 2 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("用时 %v，应取最快的上游", d)
	}
}

// 失败的上游被跳过；全部失败时返回错误。
func TestRaceSkipsFailures(t *testing.T) {
	tr := newRace(t, map[string]*fakeUpstream{"bad": {err: errors.New("unreachable"), id: 1}, "ok": {delay: 50 * time.Millisecond, id: 2}}, "bad", "ok")
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 2 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	tr = newRace(t, map[string]*fakeUpstream{"a": {err: errors.New("a down")}, "b": {err: errors.New("b down")}}, "a", "b")
	if _, err := tr.Exchange(context.Background(), query()); err == nil {
		t.Fatal("全部失败时应返回错误")
	}
}

func TestRaceUnknownUpstream(t *testing.T) {
	ctx := service.ContextWith[adapter.DNSTransportManager](context.Background(), fakeManager{m: map[string]adapter.DNSTransport{}})
	tr, _ := New(ctx, nil, "ns", Options{Servers: []string{"missing"}})
	if err := tr.Start(adapter.StartStateStart, nil); err == nil {
		t.Fatal("上游不存在时应报错")
	}
}
