package singbox

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	sblog "github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/Foreverwzh/velum/core/pkg/engine"
)

// 对端连上了、应用却已放弃（延迟握手）：不是到对端的失败，不能显示成错误。真正的错误照旧记录。
func TestCanceledHandshakeIsNotAnError(t *testing.T) {
	e := New(Options{History: 10})
	w := &logWriter{e: e}
	run := func(id uint32, msg string) engine.Connection {
		t.Helper()
		e.mu.Lock()
		c := &engine.Connection{ID: id}
		e.live[id] = c
		e.mu.Unlock()
		w.WriteMessage(sblog.LevelError, msg)
		e.finish(c, &meter{start: time.Now()}, time.Now())
		return *c
	}
	got := run(1234, "[1234 0ms] connection: report handshake success: connection refused")
	if !got.Canceled || len(got.Errors) != 0 {
		t.Fatalf("应用取消不算错误：%+v", got)
	}
	got = run(5678, "[5678 0ms] connection: open connection to x:80: dial tcp: i/o timeout")
	if got.Canceled || len(got.Errors) != 1 {
		t.Fatalf("真正的错误要保留：%+v", got)
	}
}

// 嗅探出的域名在目标地址里：连接记录要显示成域名，而不是带端口的目标地址。
func TestConnDomain(t *testing.T) {
	if got := connDomain(adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("storage3.jd.com", 80)}); got != "storage3.jd.com" {
		t.Errorf("目标地址是域名：%q", got)
	}
	if got := connDomain(adapter.InboundContext{Domain: "a.example", Destination: M.ParseSocksaddrHostPort("1.2.3.4", 80)}); got != "a.example" {
		t.Errorf("优先用 Domain：%q", got)
	}
	if got := connDomain(adapter.InboundContext{Destination: M.ParseSocksaddrHostPort("1.2.3.4", 80)}); got != "" {
		t.Errorf("目标是 IP 时没有域名：%q", got)
	}
}

// 连上对端的耗时：报告握手成功时记下；应用已放弃时（转发给入站失败）也要留下，才能看出它等了多久。
func TestConnectTimeRecorded(t *testing.T) {
	e := New(Options{History: 10})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	m := &meter{start: time.Now().Add(-120 * time.Millisecond)}
	c := &engine.Connection{ID: 9, ConnectMS: -1, FirstByteMS: -1}
	e.mu.Lock()
	e.live[9] = c
	e.mu.Unlock()
	tc := &trackedConn{Conn: refusingConn{a}, e: e, c: c, m: m}
	if err := N.ReportConnHandshakeSuccess(tc, b); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("应当转给入站并返回它的错误：%v", err)
	}
	e.finish(c, m, time.Now())
	if c.ConnectMS < 120 || c.ConnectMS > 5000 {
		t.Fatalf("连接耗时不对：%d", c.ConnectMS)
	}
	// 没走到这一步的连接
	if got := (&meter{}).connectMS(); got != -1 {
		t.Fatalf("没连上应为 -1：%d", got)
	}
}

// refusingConn 模拟 gVisor 延迟握手时应用已经放弃。
type refusingConn struct{ net.Conn }

func (refusingConn) HandshakeSuccess() error { return syscall.ECONNREFUSED }
