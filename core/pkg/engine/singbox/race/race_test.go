package race

import (
	"context"
	"errors"
	"sync/atomic"
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
	id    uint16 // 应答中的 Id 用于区分来源
	rcode int
	calls atomic.Int32 // 成功应答的次数
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
	f.calls.Add(1)
	r := new(mDNS.Msg)
	r.SetReply(m)
	r.Id = f.id
	r.Rcode = f.rcode
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
	return newRaceOpts(t, ups, Options{Servers: order})
}

func newRaceOpts(t *testing.T, ups map[string]*fakeUpstream, opts Options) adapter.DNSTransport {
	t.Helper()
	m := map[string]adapter.DNSTransport{}
	for k, v := range ups {
		v.TransportAdapter = dns.NewTransportAdapter("fake", k, nil)
		m[k] = v
	}
	ctx := service.ContextWith[adapter.DNSTransportManager](context.Background(), fakeManager{m: m})
	tr, err := New(ctx, nil, "ns", opts)
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

// 主上游出错（如 DoH 被限流 429、连接被拒）时改由兜底上游应答，并且不必等满 FallbackDelay。
func TestFallbackOnPrimaryError(t *testing.T) {
	ups := map[string]*fakeUpstream{"doh": {delay: 10 * time.Millisecond, err: errors.New("429 Too Many Requests")}, "sys": {delay: 10 * time.Millisecond, id: 9}}
	tr := newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}, FallbackDelay: time.Second})
	start := time.Now()
	r, err := tr.Exchange(context.Background(), query())
	if err != nil || r.Id != 9 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("用时 %v：主上游出错后应立即问兜底上游", d)
	}
}

// SERVFAIL / REFUSED 不算可用应答：换兜底上游；兜底也失败时仍返回该应答。
func TestFallbackOnServfail(t *testing.T) {
	ups := map[string]*fakeUpstream{"doh": {rcode: mDNS.RcodeRefused, id: 1}, "sys": {delay: 10 * time.Millisecond, id: 9}}
	tr := newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}})
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 9 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	ups = map[string]*fakeUpstream{"doh": {rcode: mDNS.RcodeServerFailure, id: 1}, "sys": {err: errors.New("down")}}
	tr = newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}})
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Rcode != mDNS.RcodeServerFailure {
		t.Fatalf("r=%v err=%v", r, err)
	}
	// NXDOMAIN 是正常应答，不问兜底上游
	ups = map[string]*fakeUpstream{"doh": {rcode: mDNS.RcodeNameError, id: 1}, "sys": {id: 9}}
	tr = newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}})
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 1 || ups["sys"].calls.Load() != 0 {
		t.Fatalf("r=%v err=%v sys=%d", r, err, ups["sys"].calls.Load())
	}
}

// 主上游没有应答（超时）时，FallbackDelay 后由兜底上游应答；主上游健康时不问兜底上游。
func TestFallbackHedgesSlowPrimary(t *testing.T) {
	ups := map[string]*fakeUpstream{"doh": {delay: 5 * time.Second, id: 1}, "sys": {delay: 10 * time.Millisecond, id: 9}}
	tr := newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}, FallbackDelay: 100 * time.Millisecond})
	start := time.Now()
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 9 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > time.Second {
		t.Fatalf("用时 %v：应在 FallbackDelay 后由兜底上游应答", d)
	}
	ups = map[string]*fakeUpstream{"doh": {delay: 10 * time.Millisecond, id: 1}, "sys": {id: 9}}
	tr = newRaceOpts(t, ups, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}})
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 1 || ups["sys"].calls.Load() != 0 {
		t.Fatalf("r=%v err=%v sys=%d", r, err, ups["sys"].calls.Load())
	}
}

// 主上游刚失败过：之后的查询同时问兜底上游，不再每次等 FallbackDelay；主上游恢复后回到只问主上游。
func TestFallbackRemembersFailure(t *testing.T) {
	doh := &fakeUpstream{err: errors.New("connection refused")}
	sys := &fakeUpstream{id: 9}
	tr := newRaceOpts(t, map[string]*fakeUpstream{"doh": doh, "sys": sys}, Options{Servers: []string{"doh"}, Fallback: []string{"sys"}, FallbackDelay: time.Second})
	if _, err := tr.Exchange(context.Background(), query()); err != nil {
		t.Fatal(err)
	}
	doh.err, doh.delay = nil, 200*time.Millisecond
	start := time.Now()
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 9 {
		t.Fatalf("r=%v err=%v", r, err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("用时 %v：主上游刚失败过，应同时问兜底上游", d)
	}
	// 网络变化后重新信任主上游
	tr.Reset()
	sys.delay = 5 * time.Millisecond
	before := sys.calls.Load()
	if r, err := tr.Exchange(context.Background(), query()); err != nil || r.Id != 0 || sys.calls.Load() != before {
		t.Fatalf("r=%v err=%v：Reset 后应先等主上游", r, err)
	}
}
