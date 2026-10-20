// Package singbox 用 sing-box 实现 engine.Engine。
//
// 动态实录基于 sing-box 公开的 Router.AppendTracker（M0 spikes/m0-live）：
//   - 命中规则 → Router.Rules() 下标 → engine.Config.RuleSources（Provenance）；
//   - 出站链：递归展开 adapter.OutboundGroup.Now()；
//   - 错误原因：PlatformLogWriter 按连接 ID 关联；sing-box 先关闭连接后打错误日志，故延迟落盘。
package singbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/race"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/include"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const (
	defaultHistory = 2000
	finishDelay    = 100 * time.Millisecond
)

// Options 配置引擎实现。
type Options struct {
	History int       // 保留的已结束连接数
	Log     io.Writer // 内核日志输出（已剥离颜色）；nil 表示丢弃
}

type Engine struct {
	opts Options

	lmu      sync.Mutex // 生命周期（instance）；Start 期间内核会回调日志，不能与 mu 合用
	instance *box.Box
	bctx     context.Context // 内核的 context（含 DNS 路由器等服务）
	plat     *platform       // TUN 模式下的平台接口（持有 helper 会话）
	ownTUN   string          // 内核自建 TUN（Windows）的网卡名

	mu       sync.Mutex // 连接数据与运行时引用
	router   adapter.Router
	outbound adapter.OutboundManager
	sources  []ir.Source
	irIndex  []int
	minLevel sblog.Level
	ruleIdx  map[adapter.Rule]int

	live    map[uint32]*engine.Connection
	errs    map[uint32][]string
	history []engine.Connection // 环形缓冲
	next    int
	subs    map[chan engine.Connection]struct{}
}

func New(opts Options) *Engine {
	if opts.History <= 0 {
		opts.History = defaultHistory
	}
	return &Engine{opts: opts, live: map[uint32]*engine.Connection{}, errs: map[uint32][]string{}, subs: map[chan engine.Connection]struct{}{}}
}

var _ engine.Engine = (*Engine)(nil)

func boxContext(ctx context.Context) context.Context {
	return box.Context(ctx, include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		dnsRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
}

// dnsRegistry 是内核的 DNS 传输表，另加 Velum 的 race 传输（多上游竞速）。
func dnsRegistry() *dns.TransportRegistry {
	r := include.DNSTransportRegistry()
	race.Register(r)
	return r
}

// Check 校验配置能否被内核加载（不启动）。
func Check(cfgJSON []byte) error {
	ctx := boxContext(context.Background())
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, cfgJSON)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return err
	}
	return instance.Close()
}

func (e *Engine) Start(ctx context.Context, cfg engine.Config) error {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	if e.instance != nil {
		return errors.New("engine already running")
	}
	return e.startLocked(cfg)
}

func (e *Engine) startLocked(cfg engine.Config) error {
	bctx := boxContext(context.Background())
	var plat *platform
	if cfg.TUN != nil && cfg.TUN.Opener != nil { // 没有 Opener（Windows）时由内核自建 TUN
		plat = &platform{cfg: cfg.TUN}
		bctx = service.ContextWith[adapter.PlatformInterface](bctx, plat)
	}
	opts, err := json.UnmarshalExtendedContext[option.Options](bctx, cfg.JSON)
	if err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	// 内核日志统一经 PlatformLogWriter 输出，关闭内核自身的日志文件输出。
	if opts.Log == nil {
		opts.Log = &option.LogOptions{}
	}
	opts.Log.Output = os.DevNull // 空字符串会默认写 stderr；内核日志只经 PlatformLogWriter 交给 Velum
	opts.Log.Disabled = false
	opts.Log.Level = "debug" // reject 结论等关键信息在 debug 级别；写文件时再按 minLevel 过滤
	// 设置了 PlatformLogWriter 时 sing-box 会强制启用缓存文件（默认为工作目录下的 cache.db），
	// 其中保存的 selector 选择优先于配置中的 default。Velum 以 state.json 为准，因此指定路径并在启动前清空。
	cachePath := cfg.CachePath
	if cachePath == "" {
		cachePath = filepath.Join(os.TempDir(), fmt.Sprintf("velum-singbox-%d.db", os.Getpid()))
	}
	_ = os.Remove(cachePath)
	if opts.Experimental == nil {
		opts.Experimental = &option.ExperimentalOptions{}
	}
	opts.Experimental.CacheFile = &option.CacheFileOptions{Enabled: true, Path: cachePath}
	lvl, err := sblog.ParseLevel(orDefault(cfg.LogLevel, "info"))
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.minLevel = lvl
	e.mu.Unlock()
	instance, err := box.New(box.Options{Context: bctx, Options: opts, PlatformLogWriter: &logWriter{e}})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.router, e.outbound = instance.Router(), instance.Outbound()
	e.sources, e.irIndex = cfg.RuleSources, cfg.RuleIndex
	e.ruleIdx = nil // 首次需要时按 Router.Rules() 构建
	e.mu.Unlock()
	instance.Router().AppendTracker(&tracker{e})
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		if plat != nil {
			plat.release()
		}
		return err
	}
	e.instance, e.plat, e.bctx = instance, plat, bctx
	e.ownTUN = ""
	if plat == nil {
		for _, in := range opts.Inbounds {
			if t, ok := in.Options.(*option.TunInboundOptions); ok {
				e.ownTUN = t.InterfaceName
			}
		}
	}
	return nil
}

// closeLocked 关闭内核实例，并释放 helper 会话（helper 删除路由、销毁 utun）。
func (e *Engine) closeLocked() error {
	if e.instance == nil {
		return nil
	}
	err := e.instance.Close()
	e.instance, e.ownTUN = nil, ""
	if e.plat != nil {
		e.plat.release()
		e.plat = nil
	}
	return err
}

// TUNName 返回当前 TUN 网卡名称（未启用 TUN 时为空）。
func (e *Engine) TUNName() string {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	if e.plat == nil {
		return e.ownTUN
	}
	return e.plat.tunName()
}

// ruleIndexLocked 返回规则在 route.rules 中的下标（需持有 mu）。
func (e *Engine) ruleIndexLocked(r adapter.Rule) (int, bool) {
	if e.ruleIdx == nil && e.router != nil {
		e.ruleIdx = map[adapter.Rule]int{}
		for i, x := range e.router.Rules() {
			e.ruleIdx[x] = i
		}
	}
	i, ok := e.ruleIdx[r]
	return i, ok
}

func (e *Engine) Restart(ctx context.Context, cfg engine.Config) error {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	_ = e.closeLocked()
	return e.startLocked(cfg)
}

func (e *Engine) Stop() error {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	return e.closeLocked()
}

func (e *Engine) Running() bool {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	return e.instance != nil
}

func (e *Engine) Selected(tag string) string {
	e.mu.Lock()
	om := e.outbound
	e.mu.Unlock()
	if om == nil {
		return ""
	}
	ob, ok := om.Outbound(tag)
	if !ok {
		return ""
	}
	if g, ok := ob.(adapter.OutboundGroup); ok {
		return g.Now()
	}
	return ""
}

func (e *Engine) Connections(limit int) []engine.Connection {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []engine.Connection
	for _, c := range e.live {
		out = append(out, *c)
	}
	n := len(e.history)
	for i := 0; i < n; i++ {
		idx := (e.next - 1 - i + n) % n
		out = append(out, e.history[idx])
	}
	// 进行中的连接按开始时间倒序排在前面
	sortByStartDesc(out[:len(e.live)])
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (e *Engine) Subscribe() (<-chan engine.Connection, func()) {
	ch := make(chan engine.Connection, 256)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.mu.Unlock()
	}
}

// ---------------- tracker ----------------

type tracker struct{ e *Engine }

func (t *tracker) RoutedConnection(ctx context.Context, conn net.Conn, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) net.Conn {
	c := t.e.begin(ctx, "tcp", md, rule, ob)
	return &trackedConn{Conn: conn, e: t.e, c: c, start: time.Now()}
}

func (t *tracker) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) N.PacketConn {
	c := t.e.begin(ctx, "udp", md, rule, ob)
	return &trackedPacketConn{PacketConn: conn, e: t.e, c: c, start: time.Now()}
}

func (t *tracker) RoutedFlow(context.Context, adapter.InboundContext, adapter.Rule, adapter.Outbound) tun.FlowTracker {
	return nil
}

func (e *Engine) begin(ctx context.Context, network string, md adapter.InboundContext, rule adapter.Rule, ob adapter.Outbound) *engine.Connection {
	c := &engine.Connection{Start: time.Now(), Network: network, Inbound: md.Inbound, Source: md.Source.String(),
		Domain: md.Domain, Destination: md.Destination.String(), Sniffed: md.Protocol, RuleIndex: -1, FirstByteMS: -1}
	if id, ok := sblog.IDFromContext(ctx); ok {
		c.ID = id.ID
	}
	if md.ProcessInfo != nil {
		c.Process = md.ProcessInfo.ProcessPath
	}
	for _, a := range md.DestinationAddresses {
		c.Resolved = append(c.Resolved, a.String())
	}
	e.mu.Lock()
	if rule != nil {
		c.Rule = rule.String()
		if i, ok := e.ruleIndexLocked(rule); ok {
			c.RuleIndex = e.toIR(i)
			if i < len(e.sources) {
				c.Provenance = e.sources[i]
			}
		}
	} else {
		c.Rule = "final"
		c.Provenance = ir.Source{Kind: ir.SourceVelum, Text: "route.final"}
	}
	om := e.outbound
	e.live[c.ID] = c
	e.mu.Unlock()
	for cur, i := ob, 0; cur != nil && i < 8; i++ {
		c.Chain = append(c.Chain, cur.Tag())
		g, ok := cur.(adapter.OutboundGroup)
		if !ok || om == nil {
			break
		}
		cur, _ = om.Outbound(g.Now())
	}
	return c
}

func (e *Engine) finish(c *engine.Connection, start, end time.Time, up, down, firstByte int64) {
	time.Sleep(finishDelay) // 等待同一连接的错误日志
	e.mu.Lock()
	defer e.mu.Unlock()
	c.Upload, c.Download = up, down
	if firstByte > 0 {
		c.FirstByteMS = firstByte - 1
	}
	c.DurationMS = end.Sub(start).Milliseconds()
	c.End = &end
	c.Errors = append(c.Errors, e.errs[c.ID]...)
	delete(e.errs, c.ID)
	delete(e.live, c.ID)
	e.pushLocked(*c)
}

func (e *Engine) pushLocked(c engine.Connection) {
	if len(e.history) < e.opts.History {
		e.history = append(e.history, c)
		e.next = len(e.history) % e.opts.History
	} else {
		e.history[e.next] = c
		e.next = (e.next + 1) % e.opts.History
	}
	for ch := range e.subs {
		select {
		case ch <- c:
		default: // 订阅者太慢则丢弃，不阻塞数据面
		}
	}
}

type counters struct {
	up, down, firstByte atomic.Int64
	once                sync.Once
}

type trackedConn struct {
	net.Conn
	e     *Engine
	c     *engine.Connection
	start time.Time
	counters
}

// 入站侧 conn：Read = 客户端上行；Write = 远端下行。
func (t *trackedConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	t.up.Add(int64(n))
	return n, err
}

func (t *trackedConn) Write(p []byte) (int, error) {
	if len(p) > 0 && t.firstByte.Load() == 0 {
		t.firstByte.CompareAndSwap(0, time.Since(t.start).Milliseconds()+1)
	}
	n, err := t.Conn.Write(p)
	t.down.Add(int64(n))
	return n, err
}

func (t *trackedConn) Close() error {
	err := t.Conn.Close()
	t.once.Do(func() {
		end := time.Now()
		go t.e.finish(t.c, t.start, end, t.up.Load(), t.down.Load(), t.firstByte.Load())
	})
	return err
}

func (t *trackedConn) Upstream() any { return t.Conn }

type trackedPacketConn struct {
	N.PacketConn
	e     *Engine
	c     *engine.Connection
	start time.Time
	counters
}

func (t *trackedPacketConn) Close() error {
	err := t.PacketConn.Close()
	t.once.Do(func() {
		end := time.Now()
		go t.e.finish(t.c, t.start, end, t.up.Load(), t.down.Load(), t.firstByte.Load())
	})
	return err
}

func (t *trackedPacketConn) Upstream() any { return t.PacketConn }

// ---------------- 日志 ----------------

var (
	ansiRe  = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	logIDRe = regexp.MustCompile(`\[(\d+) [0-9.]+[mµn]?s\]`)
	matchRe = regexp.MustCompile(`router: match\[(\d+)\] (.*) => (\S+)`)
)

type logWriter struct{ e *Engine }

func (w *logWriter) WriteMessage(level sblog.Level, msg string) {
	msg = ansiRe.ReplaceAllString(msg, "")
	e := w.e
	e.mu.Lock()
	minLevel := e.minLevel
	e.mu.Unlock()
	if e.opts.Log != nil && level <= minLevel {
		_, _ = io.WriteString(e.opts.Log, time.Now().Format("2006-01-02 15:04:05.000 ")+msg+"\n")
	}
	m := logIDRe.FindStringSubmatch(msg)
	if m == nil {
		return
	}
	id64, _ := strconv.ParseUint(m[1], 10, 32)
	id := uint32(id64)
	text := strings.TrimSpace(msg[strings.Index(msg, m[0])+len(m[0]):])
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, live := e.live[id]; live {
		if level <= sblog.LevelWarn {
			e.errs[id] = append(e.errs[id], text)
		}
		return
	}
	// 未经 tracker 的连接：reject 等动作。根据 match 日志还原规则下标与来源。
	if mm := matchRe.FindStringSubmatch(text); mm != nil && (mm[3] == "reject" || strings.HasPrefix(mm[3], "reject(")) {
		idx, _ := strconv.Atoi(mm[1])
		c := engine.Connection{ID: id, Start: time.Now(), RuleIndex: e.toIR(idx), Rule: mm[2], Chain: []string{mm[3]}, Untracked: true, FirstByteMS: -1}
		if idx < len(e.sources) {
			c.Provenance = e.sources[idx]
		}
		end := c.Start
		c.End = &end
		e.pushLocked(c)
	}
}

// toIR 把内核规则下标映射为 IR 规则下标（需持有 mu）。
func (e *Engine) toIR(i int) int {
	if i >= 0 && i < len(e.irIndex) {
		return e.irIndex[i]
	}
	return i
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func sortByStartDesc(cs []engine.Connection) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].Start.After(cs[j-1].Start); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}
