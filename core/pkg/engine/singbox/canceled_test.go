package singbox

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	sblog "github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"

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
