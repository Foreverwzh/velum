// m0-live：M0「动态实录」验证（ARCHITECTURE §4.4）。
//
// 在进程内通过 sing-box 公开的 Router.AppendTracker 拿到每条连接的
// 「实际命中规则（序号）+ 实际出站链 + 进程 / 域名 / 解析结果 / 嗅探协议」，
// 包装 conn 统计字节数与首包耗时，并经 PlatformLogWriter 按连接 ID 关联错误日志。
// 只开本地 mixed 入站（127.0.0.1:17891），不需要 root，不碰 TUN / 系统代理。
package main

import (
	"context"
	stdjson "encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
)

var (
	listen    = flag.String("listen", "127.0.0.1:17891", "mixed 入站")
	stashAddr = flag.String("stash", "127.0.0.1:7890", "上游 HTTP 代理（Stash）")
	recPath   = flag.String("records", "/tmp/velum-live/records.jsonl", "连接记录输出")
)

// provenance 模拟 Velum CompiledPlan 中每条路由规则的来源（与 route.rules 一一对应）。
var provenance = []string{
	"velum:builtin sniff",
	"subscription: DOMAIN-SUFFIX,ads.example.com,REJECT",
	"subscription: DOMAIN-SUFFIX,blocked.example.com,REJECT（编译为 block 出站）",
	"overlay: dev_bypass.cidrs 223.6.6.6/32",
	"subscription: RULE-SET,domestic → DOMAIN-SUFFIX,baidu.com",
	"velum:resolve（首条需解析的 IP 规则前插入）",
	"subscription: IP-CIDR,9.9.9.9/32,Proxy",
	"test: IP-CIDR,10.255.255.0/24 → 短超时直连（绑定 en0，绕开 Stash TUN）",
}

func renderConfig() string {
	host, port, _ := net.SplitHostPort(*listen)
	sh, sp, _ := net.SplitHostPort(*stashAddr)
	return fmt.Sprintf(`{
  "log": {"level": "debug", "output": "/dev/null"},
  "dns": {"servers": [{"type": "local", "tag": "local"}]},
  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": %q, "listen_port": %s}],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "direct", "tag": "direct-short", "connect_timeout": "2s", "bind_interface": "en0"},
    {"type": "block", "tag": "block"},
    {"type": "http", "tag": "stash", "server": %q, "server_port": %s},
    {"type": "urltest", "tag": "Auto", "outbounds": ["stash"], "interval": "10m"},
    {"type": "selector", "tag": "Proxy", "outbounds": ["Auto", "direct"], "default": "Auto"}
  ],
  "route": {
    "default_domain_resolver": "local",
    "final": "Proxy",
    "rules": [
      {"action": "sniff"},
      {"domain_suffix": ["ads.example.com"], "action": "reject"},
      {"domain_suffix": ["blocked.example.com"], "outbound": "block"},
      {"ip_cidr": ["223.6.6.6/32"], "outbound": "direct"},
      {"domain_suffix": ["baidu.com"], "outbound": "direct"},
      {"action": "resolve"},
      {"ip_cidr": ["9.9.9.9/32"], "outbound": "Proxy"},
      {"ip_cidr": ["10.255.255.0/24"], "outbound": "direct-short"}
    ]
  }
}`, host, port, sh, sp)
}

// ---------------- 连接记录 ----------------

type Record struct {
	ID          uint32   `json:"id"`
	Start       string   `json:"start"`
	Network     string   `json:"network"`
	Inbound     string   `json:"inbound"`
	Source      string   `json:"source"`
	Process     string   `json:"process,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	Destination string   `json:"destination"`
	Resolved    []string `json:"resolved,omitempty"`
	Sniffed     string   `json:"sniffed,omitempty"`
	RuleIndex   int      `json:"rule_index"` // -1 = final
	Rule        string   `json:"rule"`
	Provenance  string   `json:"provenance"`
	Chain       []string `json:"chain"`
	Upload      int64    `json:"upload"`
	Download    int64    `json:"download"`
	FirstByteMS int64    `json:"first_byte_ms"` // 路由完成 → 首个下行字节
	DurationMS  int64    `json:"duration_ms"`
	Errors      []string `json:"errors,omitempty"`
}

type tracker struct {
	router   adapter.Router
	outbound adapter.OutboundManager
	mu       sync.Mutex
	live     map[uint32]*Record
	errs     map[uint32][]string // 日志里按连接 ID 收集到的错误
	out      *os.File
}

func (t *tracker) RoutedConnection(ctx context.Context, conn net.Conn, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) net.Conn {
	rec := t.newRecord(ctx, "tcp", md, rule, ob)
	return &trackedConn{Conn: conn, t: t, rec: rec, start: time.Now()}
}

func (t *tracker) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) N.PacketConn {
	rec := t.newRecord(ctx, "udp", md, rule, ob)
	t.finish(rec, time.Now()) // spike：UDP 只记录路由结论
	return conn
}

func (t *tracker) RoutedFlow(ctx context.Context, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) tun.FlowTracker {
	return nil
}

func (t *tracker) newRecord(ctx context.Context, network string, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) *Record {
	rec := &Record{Start: time.Now().Format("15:04:05.000"), Network: network, Inbound: md.Inbound,
		Source: md.Source.String(), Domain: md.Domain, Destination: md.Destination.String(), Sniffed: md.Protocol, RuleIndex: -1}
	if id, ok := sblog.IDFromContext(ctx); ok {
		rec.ID = id.ID
	}
	if md.ProcessInfo != nil {
		rec.Process = md.ProcessInfo.ProcessPath
	}
	for _, a := range md.DestinationAddresses {
		rec.Resolved = append(rec.Resolved, a.String())
	}
	if rule != nil {
		rec.Rule = rule.String()
		for i, r := range t.router.Rules() {
			if r == rule {
				rec.RuleIndex = i
				break
			}
		}
		if rec.RuleIndex >= 0 && rec.RuleIndex < len(provenance) {
			rec.Provenance = provenance[rec.RuleIndex]
		}
	} else {
		rec.Rule, rec.Provenance = "final", "route.final"
	}
	// 展开策略组：Proxy → Auto → stash
	cur := ob
	for i := 0; cur != nil && i < 8; i++ {
		rec.Chain = append(rec.Chain, cur.Tag())
		g, ok := cur.(adapter.OutboundGroup)
		if !ok {
			break
		}
		cur, _ = t.outbound.Outbound(g.Now())
	}
	t.mu.Lock()
	t.live[rec.ID] = rec
	t.mu.Unlock()
	return rec
}

func (t *tracker) finish(rec *Record, start time.Time) { t.finishAt(rec, start, time.Now()) }

func (t *tracker) finishAt(rec *Record, start, end time.Time) {
	t.mu.Lock()
	rec.DurationMS = end.Sub(start).Milliseconds()
	rec.Errors = append(rec.Errors, t.errs[rec.ID]...)
	delete(t.live, rec.ID)
	delete(t.errs, rec.ID)
	data, _ := stdjson.Marshal(rec)
	t.out.Write(append(data, '\n'))
	t.mu.Unlock()
}

type trackedConn struct {
	net.Conn
	t         *tracker
	rec       *Record
	start     time.Time
	up, down  atomic.Int64
	firstByte atomic.Int64
	once      sync.Once
}

// 入站侧 conn：Read = 客户端上行，Write = 远端下行。
func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.up.Add(int64(n))
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	if c.firstByte.Load() == 0 && len(p) > 0 {
		c.firstByte.CompareAndSwap(0, time.Since(c.start).Milliseconds()+1)
	}
	n, err := c.Conn.Write(p)
	c.down.Add(int64(n))
	return n, err
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.rec.Upload, c.rec.Download = c.up.Load(), c.down.Load()
		if fb := c.firstByte.Load(); fb > 0 {
			c.rec.FirstByteMS = fb - 1
		} else {
			c.rec.FirstByteMS = -1
		}
		end := time.Now()
		// sing-box 拨号失败时先关闭连接、后输出错误日志；若在此同步等待会阻塞打日志的协程，
		// 因此异步延迟落盘，让同一连接 ID 的错误日志先归集到记录上。
		go func() {
			time.Sleep(100 * time.Millisecond)
			c.t.finishAt(c.rec, c.start, end)
		}()
	})
	return err
}

func (c *trackedConn) Upstream() any { return c.Conn }

// ---------------- 日志关联 ----------------

var (
	logIDRe = regexp.MustCompile(`\[(\d+) [0-9.]+[mµn]?s\]`)
	// sing-box 平台日志默认带 ANSI 颜色，且颜色码包住了连接 ID，需要先剥离。
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

type logWriter struct{ t *tracker }

func (w *logWriter) WriteMessage(level sblog.Level, msg string) {
	if os.Getenv("LIVE_DEBUG_LOG") != "" {
		fmt.Fprintf(os.Stderr, "PLATFORM[%s] %q\n", sblog.FormatLevel(level), msg)
	}
	msg = ansiRe.ReplaceAllString(msg, "")
	m := logIDRe.FindStringSubmatch(msg)
	if m == nil {
		return
	}
	id64, _ := strconv.ParseUint(m[1], 10, 32)
	id := uint32(id64)
	text := strings.TrimSpace(msg[strings.Index(msg, m[0])+len(m[0]):])
	w.t.mu.Lock()
	defer w.t.mu.Unlock()
	if _, live := w.t.live[id]; live {
		if level <= sblog.LevelWarn {
			w.t.errs[id] = append(w.t.errs[id], text)
		}
		return
	}
	// 未进入 tracker 的连接：只关心 reject 结论与 warn / error
	if level > sblog.LevelWarn && !strings.Contains(text, "reject") {
		return
	}
	// 未经 tracker 的连接（如 reject 动作）：单独记一条
	data, _ := stdjson.Marshal(map[string]any{"id": id, "untracked": true, "level": sblog.FormatLevel(level), "log": text})
	w.t.out.Write(append(data, '\n'))
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	_ = os.MkdirAll("/tmp/velum-live", 0o700)
	out, err := os.OpenFile(*recPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(renderConfig()))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	t := &tracker{live: map[uint32]*Record{}, errs: map[uint32][]string{}, out: out}
	instance, err := box.New(box.Options{Context: ctx, Options: opts, PlatformLogWriter: &logWriter{t}})
	if err != nil {
		log.Fatalf("box.New: %v", err)
	}
	t.router, t.outbound = instance.Router(), instance.Outbound()
	t.router.AppendTracker(t)
	if err := instance.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}
	log.Printf("LIVE STARTED mixed=%s records=%s rules=%d", *listen, *recPath, len(t.router.Rules()))
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	instance.Close()
}
