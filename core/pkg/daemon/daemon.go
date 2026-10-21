// Package daemon 实现 velumd：加载配置、编译、运行引擎，并通过 Unix socket 提供本地接口。
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/helper"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/source"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
)

var Version = "0.1.0-dev"

type state struct {
	cfg              *config.Config
	wantSys, wantTUN bool   // 用户要求（配置文件 + 运行时切换）
	sysOn, tunOn     bool   // 实际生效（可能因 NetworkSense 暂停而不同）
	paused           string // 暂停原因
	stale            bool   // 使用了过期缓存（启动时），需要在后台更新
	retryViaProxy    bool   // 有远程规则集直连下载失败且无缓存，引擎起来后应经 Velum 代理重试
	plan             *compiler.Plan
	rendered         *render.Rendered
	sub              rpc.SubscriptionStatus
	loadedAt         time.Time
}

// SysProxy 是系统代理控制器（sysproxy.Manager 实现）；为 nil 表示不支持 / 不接管（如测试）。
type SysProxy interface {
	Apply(plan compiler.SysProxyPlan) error
	Restore() error
	Heal() (sysproxy.HealResult, error)
	HasSnapshot() bool
}

type Daemon struct {
	Paths    config.Paths
	Engine   engine.Engine
	Logger   *log.Logger
	SysProxy SysProxy
	// TUNUnavailable 非空时 TUN 不可用（如 Windows 上未以服务运行），开启 TUN 会显示为暂停并给出此原因。
	TUNUnavailable string
	// AllowSIDs 是 Windows 上允许访问本地接口的用户（以服务运行时为安装用户）；为空表示当前用户。
	AllowSIDs []string
	// TUNOpener 覆盖默认的 HelperOpener（测试用）。
	TUNOpener engine.TUNOpener
	// EffectiveProxy 读取系统实际生效的代理，用于发现「写入了但被其他 VPN 覆盖」；为 nil 时不检查。
	EffectiveProxy func() (sysproxy.Effective, error)

	sysOverride, tunOverride *bool // 界面 / vlu 设置的开关（持久化到 state.json，覆盖 config.yaml）；nil 表示按 config.yaml

	// NetSnapshot 采集网络状态（nil 表示不启用 NetworkSense，如测试）。
	NetSnapshot func() (netsense.Snapshot, error)
	// CaptiveCheck 覆盖 Captive 探测（测试用）。
	CaptiveCheck func(ctx context.Context, probes []netsense.Probe, path netsense.PhysicalPath) (netsense.CaptiveState, string)

	rsCache    map[string]ruleSetCacheEntry // 静态推演用的本地规则集（path → 内容）
	dnsHosts   map[string]dnsHostEntry      // respect-rules 推演用：DNS 服务器域名 → 地址（经 default-nameserver 解析）
	selections map[string]string            // 用户为 select 组选择的成员（持久化到 state.json）

	env           netsense.Assessment
	physDNS       []string // 物理网络（DHCP）的 DNS，用于接管系统 DNS 后避免回环
	captive       netsense.CaptiveState
	captiveDetail string

	// 实际生效代理的缓存：读取可能较慢（Windows 服务需在用户会话中启动子进程），状态查询频繁
	effMu  sync.Mutex
	effAt  time.Time
	eff    sysproxy.Effective
	effErr error

	mu        sync.RWMutex
	st        *state
	startedAt time.Time
	starting  bool // 首次加载配置、启动引擎尚未完成
	lastErr   string
	reloadMu  sync.Mutex // 串行化重新加载（定时更新、网络变化、界面操作可能同时触发）
	runCtx    context.Context
	stop      chan struct{}
	stopOnce  sync.Once
}

func New(paths config.Paths, eng engine.Engine, logger *log.Logger) *Daemon {
	return &Daemon{Paths: paths, Engine: eng, Logger: logger, stop: make(chan struct{})}
}

// build 读取配置并完成「获取订阅 → 转换 → 合并 Overlay → 编译 → 渲染」，不影响正在运行的引擎。
// force 为 true 时忽略缓存 TTL，强制重新拉取订阅、provider 与规则集（vlu update）。
// stale 为 true 时有缓存就用（即使已过期），用于启动时立即可用。
func (d *Daemon) build(force, stale bool) (*state, error) {
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return nil, err
	}
	f := source.New(d.Paths.Cache, cfg.Subscription.UserAgent)
	f.PreferLocal = cfg.Subscription.PreferLocalProviders
	f.Log = d.Logger.Printf
	if d.Engine.Running() {
		f.Fallback = &url.URL{Scheme: "http", Host: cfg.ListenAddr().String()}
	}
	subTTL, rsTTL := cfg.Subscription.UpdateInterval, cfg.Subscription.RuleSetUpdateInterval
	if force {
		f.TTL, subTTL, rsTTL = 0, 0, 0
	}
	f.Stale = stale && !force
	st := &state{cfg: cfg, loadedAt: time.Now()}
	defer func() { st.stale = f.UsedStale() }()

	var data []byte
	baseDirs := append([]string(nil), cfg.Subscription.ProviderDirs...)
	switch {
	case cfg.Subscription.File != "":
		data, err = userfs.ReadFile(cfg.Subscription.File)
		if err != nil {
			return nil, fmt.Errorf("读取订阅文件：%w", err)
		}
		st.sub = rpc.SubscriptionStatus{Source: cfg.Subscription.File, Origin: "file", FetchedAt: modTime(cfg.Subscription.File)}
		baseDirs = append(baseDirs, filepath.Dir(cfg.Subscription.File))
	case cfg.Subscription.URL != "":
		var res source.Result
		data, res, err = f.Get("subscription", cfg.Subscription.URL, subTTL)
		if err != nil {
			return nil, fmt.Errorf("拉取订阅：%w", err)
		}
		st.sub = rpc.SubscriptionStatus{Source: redact(cfg.Subscription.URL), Origin: res.Origin, FetchedAt: res.FetchedAt, FetchErr: res.Err}
	}
	var sub *ir.Config
	if data != nil {
		sub, err = subscription.Parse(data, subscription.Options{File: filepath.Base(st.sub.Source), BaseDirs: baseDirs,
			ReadFile: f.ProviderReader(), GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL})
		if err != nil {
			return nil, fmt.Errorf("解析订阅：%w", err)
		}
	}
	var rsWarnings []ir.Warning
	if sub != nil {
		rsWarnings = f.ResolveRemoteRuleSets(sub, rsTTL)
		for _, w := range rsWarnings {
			if w.Level == ir.Unsupported && f.Fallback == nil {
				st.retryViaProxy = true
			}
		}
	}
	ov, err := overlay.Load(cfg.Overlay)
	if err != nil {
		return nil, err
	}
	opts := compiler.Options{Mode: compiler.ModeProxy, Listen: cfg.ListenAddr(), Platform: runtime.GOOS}
	d.mu.RLock()
	st.wantSys, st.wantTUN = cfg.WantSystemProxy(), cfg.WantTUN()
	if d.sysOverride != nil {
		st.wantSys = *d.sysOverride
	}
	if d.tunOverride != nil {
		st.wantTUN = *d.tunOverride
	}
	env, captive := d.env, d.captive
	d.mu.RUnlock()
	// NetworkSense：按环境决定实际生效的开关，并提供分流型 VPN 信息
	st.sysOn, st.tunOn = st.wantSys, st.wantTUN
	ns := cfg.NetworkSense
	var helperProblem string
	if st.tunOn && d.TUNOpener == nil && runtime.GOOS == "darwin" {
		helperProblem = checkHelper(cfg.TUN.HelperSocket)
	}
	switch {
	case ns.Enabled && ns.CaptivePortal && captive == netsense.Captive && (st.sysOn || st.tunOn):
		st.sysOn, st.tunOn = false, false
		st.paused = "连接的网络需要网页认证（Captive Portal），已暂停接管；认证完成后自动恢复"
	case st.tunOn && d.TUNUnavailable != "":
		st.tunOn = false
		st.paused = d.TUNUnavailable
	case helperProblem != "":
		// TUN 组件缺失或是旧版本（如刚更新 Velum）：只暂停 TUN，系统代理与本地端口照常工作
		st.tunOn = false
		st.paused = helperProblem
	case ns.Enabled && ns.YieldToFullVPN && st.tunOn && env.FullTunnel() != nil:
		st.tunOn = false // 系统代理不受影响：遵守系统代理的应用仍经 Velum
		st.paused = fmt.Sprintf("检测到 %s 全局接管（占据主路由），已暂停 TUN 让路", env.FullTunnel().Iface)
	}
	if ns.Enabled {
		e := &compiler.Environment{}
		if ns.VPNCoexist {
			for _, v := range env.Split() {
				e.SplitVPNs = append(e.SplitVPNs, compiler.EnvVPN{Iface: v.Iface, Routes: v.Routes, Domains: v.Domains, Nameservers: v.Nameservers})
			}
		}
		if ns.SystemSplitDNS {
			e.SystemDNSDomains = env.SplitDNSDomains
			for _, r := range env.SplitDNS {
				e.SystemResolvers = append(e.SystemResolvers, compiler.EnvResolver{Domain: r.Domain, Nameservers: r.Nameservers})
			}
		}
		d.mu.RLock()
		e.PhysicalDNS = d.physDNS
		d.mu.RUnlock()
		opts.Env = e
	} else if runtime.GOOS == "android" {
		// Android 没有 /etc/resolv.conf，系统 DNS 只能用平台层报告的当前网络 DNS（见 netMonitor）
		d.mu.RLock()
		opts.Env = &compiler.Environment{PhysicalDNS: d.physDNS}
		d.mu.RUnlock()
	}
	opts.SystemProxy = st.sysOn
	if st.tunOn {
		opts.Mode = compiler.ModeTUN
		if opts.TUN.Address, opts.TUN.Address6, opts.TUN.MTU, opts.TUN.Include, opts.TUN.Exclude, err = cfg.TUNOptions(); err != nil {
			return nil, err
		}
		opts.TUN.DNSHijack = cfg.TUN.DNSHijack
		opts.TUN.IncludeApps, opts.TUN.ExcludeApps = cfg.TUN.IncludeApps, cfg.TUN.ExcludeApps
	}
	d.mu.RLock()
	opts.Selections = copyStrMap(d.selections)
	d.mu.RUnlock()
	st.plan, err = compiler.Compile(sub, ov, opts)
	if err != nil {
		return nil, err
	}
	st.plan.Warnings = append(st.plan.Warnings, rsWarnings...)
	d.applyRespectRules(st)
	st.rendered, err = render.Render(st.plan, render.Options{LogLevel: cfg.LogLevel})
	if err != nil {
		return nil, err
	}
	st.sub.Nodes, st.sub.Groups, st.sub.Rules, st.sub.RuleSets = len(st.plan.IR.Nodes), len(st.plan.IR.Groups), len(st.plan.IR.Rules), len(st.plan.IR.RuleSets)
	return st, nil
}

func (d *Daemon) engineConfig(st *state) engine.Config {
	c := engine.Config{JSON: st.rendered.JSON, RuleSources: st.rendered.RuleSources, RuleIndex: st.rendered.RuleIndex, LogLevel: st.cfg.LogLevel,
		CachePath: filepath.Join(d.Paths.Cache, "singbox.db")}
	if st.plan.Route.Enabled {
		opener := d.TUNOpener
		if opener == nil && runtime.GOOS == "darwin" { // Windows 上由内核自建 wintun
			opener = HelperOpener{Socket: st.cfg.TUN.HelperSocket}
		}
		c.TUN = &engine.TUNConfig{Routes: st.plan.Route.Routes, SystemDNS: st.plan.Route.SystemDNS,
			IncludeApps: st.plan.Route.IncludeApps, ExcludeApps: st.plan.Route.ExcludeApps, Opener: opener}
	}
	return c
}

// checkHelper 检查 velum-helper 是否可用，不可用时返回给用户看的原因。
func checkHelper(sock string) string {
	c, err := helper.Dial(orDefault(sock, helper.DefaultSocket))
	switch {
	case err == nil:
		_ = c.Close()
		return ""
	case errors.Is(err, helper.ErrVersionMismatch):
		return "TUN 组件需要更新（Velum 已更新，组件还是旧版本），已暂停 TUN；在 TUN 开关或「系统组件」中更新即可"
	default:
		return fmt.Sprintf("TUN 组件不可用（%v），已暂停 TUN；在「系统组件」中重新安装", err)
	}
}

// HelperOpener 经 velum-helper 打开 TUN：每次打开建立一个 helper 会话，关闭会话即由 helper 清理路由与 utun。
type HelperOpener struct{ Socket string }

func (o HelperOpener) OpenTUN(req engine.TUNRequest) (int, string, io.Closer, error) {
	c, err := helper.Dial(orDefault(o.Socket, helper.DefaultSocket))
	if err != nil {
		return -1, "", nil, err
	}
	fd, name, err := c.OpenTun(req.MTU, req.Inet4, req.Inet6, req.Routes)
	if err != nil {
		c.Close()
		return -1, "", nil, err
	}
	if len(req.SystemDNS) > 0 {
		if err := c.SetDNS(req.SystemDNS...); err != nil {
			c.Close() // helper 随即清理路由与 utun
			return -1, "", nil, err
		}
	}
	return fd, name, c, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Reload 重新构建；失败时保留当前运行的配置。
func (d *Daemon) Reload(ctx context.Context) error { return d.reload(ctx, false) }

// Update 强制重新拉取订阅、provider 与规则集后重新加载。
func (d *Daemon) Update(ctx context.Context) error { return d.reload(ctx, true) }

func (d *Daemon) reload(ctx context.Context, force bool) error {
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()
	st, err := d.build(force, false)
	if err != nil {
		d.setErr(err)
		return err
	}
	d.mu.RLock()
	old := d.st
	d.mu.RUnlock()
	// RoutePlan 也要比较：macOS / Android 的路由、系统 DNS、按应用分流由平台层（helper / VpnService）执行，不在内核配置里
	if old != nil && d.Engine.Running() && bytes.Equal(old.rendered.JSON, st.rendered.JSON) && reflect.DeepEqual(old.plan.Route, st.plan.Route) {
		// 内核配置未变化：不重启引擎（避免无谓断开连接），只更新状态（告警、拉取时间等）
		d.mu.Lock()
		d.st, d.lastErr = st, ""
		d.mu.Unlock()
		d.syncSysProxy(st)
		d.retryMissing(st)
		return nil
	}
	if err := d.Engine.Restart(ctx, d.engineConfig(st)); err != nil {
		d.setErr(err)
		// 新配置无法启动：尝试恢复旧配置
		if old != nil {
			if rerr := d.Engine.Restart(ctx, d.engineConfig(old)); rerr != nil {
				d.Logger.Printf("恢复旧配置失败：%v", rerr)
			}
		}
		return err
	}
	d.mu.Lock()
	d.st, d.lastErr = st, ""
	d.mu.Unlock()
	d.syncSysProxy(st)
	d.Logger.Printf("配置已加载：%d 节点 / %d 策略组 / %d 规则，%d 条告警", st.sub.Nodes, st.sub.Groups, st.sub.Rules, len(st.plan.Warnings))
	d.retryMissing(st)
	return nil
}

// retryMissing：引擎没起来时（首次设置订阅、启动）直连下载失败的规则集，在引擎起来后于后台经 Velum 代理重试一次，
// 避免它们一直缺失，也避免之后每次切换开关都同步重试、卡住界面。
func (d *Daemon) retryMissing(st *state) {
	d.mu.RLock()
	ctx := d.runCtx
	d.mu.RUnlock()
	if !st.retryViaProxy || !d.Engine.Running() || ctx == nil {
		return
	}
	go func() {
		d.Logger.Printf("有规则集直连下载失败，经 Velum 代理重试")
		if err := d.Reload(ctx); err != nil {
			d.Logger.Printf("经代理重试规则集失败：%v", err)
		}
	}()
}

func (d *Daemon) setErr(err error) {
	d.mu.Lock()
	d.lastErr = err.Error()
	d.mu.Unlock()
	d.Logger.Printf("错误：%v", err)
}

// Run 启动引擎与本地接口，阻塞到 Shutdown 或 ctx 结束。
func (d *Daemon) Run(ctx context.Context) error {
	if err := config.Ensure(d.Paths); err != nil {
		return err
	}
	if ipc.Alive(d.Paths.Socket) {
		return fmt.Errorf("velumd 已在运行（%s）", d.Paths.Socket)
	}
	ln, err := ipc.Listen(d.Paths.Socket, ipc.ListenOptions{AllowSIDs: d.AllowSIDs})
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.startedAt, d.starting, d.runCtx = time.Now(), true, ctx
	d.mu.Unlock()
	// 先开始响应本地接口：首次加载（拉取订阅、规则集，启动引擎）可能需要十几秒，期间界面应显示「正在启动」而不是「无法连接」
	srv := &http.Server{Handler: d.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	d.loadState()
	if d.SysProxy != nil {
		// 上次未正常退出（如 kill -9）时，系统代理可能仍指向已不存在的端口：先自愈。
		switch res, herr := d.SysProxy.Heal(); {
		case herr != nil:
			d.Logger.Printf("系统代理自愈失败：%v", herr)
		case res == sysproxy.HealRestored:
			d.Logger.Printf("检测到上次未正常退出：系统代理已按快照还原")
		case res == sysproxy.HealDiscarded:
			d.Logger.Printf("检测到上次未正常退出，但系统代理已被修改：丢弃快照，保留当前设置")
		}
	}
	d.senseInitial()
	// 有缓存就先用缓存启动（即使已过期）：拉取 30 多个 provider 可能要十几秒，且此时 Velum 自己的代理还没起来
	st, err := d.build(false, true)
	if err != nil {
		d.setErr(err) // 配置有误时仍启动 RPC，便于 vlu status 查看原因；修正后 vlu reload
	} else if err := d.Engine.Start(ctx, d.engineConfig(st)); err != nil {
		d.setErr(err)
	} else {
		d.mu.Lock()
		d.st = st
		d.mu.Unlock()
		d.syncSysProxy(st)
		d.Logger.Printf("velumd %s 已启动：listen=%s 系统代理=%v TUN=%v，%d 节点 / %d 规则", Version, st.cfg.Listen, st.sysOn, st.tunOn, st.sub.Nodes, st.sub.Rules)
		if st.stale || st.retryViaProxy {
			// 过期的订阅 / provider / 规则集在后台更新；内容未变化时不重启引擎
			go func() {
				if err := d.Reload(ctx); err != nil {
					d.Logger.Printf("后台更新过期缓存失败：%v", err)
				}
			}()
		}
	}
	d.mu.Lock()
	d.starting = false
	d.mu.Unlock()
	go d.updater(ctx)
	go d.netMonitor(ctx)
	select {
	case <-ctx.Done():
	case <-d.stop:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	// 先还原系统代理再停引擎，避免系统代理指向已关闭的端口
	if d.SysProxy != nil && d.SysProxy.HasSnapshot() {
		if rerr := d.SysProxy.Restore(); rerr != nil {
			d.Logger.Printf("还原系统代理失败：%v", rerr)
		} else {
			d.Logger.Printf("系统代理已还原")
		}
	}
	err = d.Engine.Stop()
	d.Logger.Printf("velumd 已退出")
	return err
}

func (d *Daemon) Shutdown() { d.stopOnce.Do(func() { close(d.stop) }) }

// netMonitor 定期采集网络状态；环境（外部 VPN / Captive）变化时重新编译。
func (d *Daemon) netMonitor(ctx context.Context) {
	if d.NetSnapshot == nil {
		return
	}
	var tracker probeTracker
	for {
		d.mu.RLock()
		st := d.st
		d.mu.RUnlock()
		interval := 5 * time.Second
		var ns config.NetworkSense
		if st != nil {
			ns = st.cfg.NetworkSense
			if ns.Interval > 0 {
				interval = ns.Interval
			}
		}
		switch {
		case st != nil && ns.Enabled:
			d.senseOnce(ctx, ns, &tracker)
		case st != nil && runtime.GOOS == "android":
			// 关闭了 NetworkSense 仍需跟踪当前网络的 DNS（没有 resolv.conf），但不做 Captive 探测等其他感知
			d.senseOnce(ctx, config.NetworkSense{}, &tracker)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-time.After(interval):
		}
	}
}

// probeTracker 决定何时做 Captive 探测：网络身份或环境变化、非在线状态、在线时每 5 分钟复查。
type probeTracker struct {
	networkID string
	key       string
	lastProbe time.Time
}

const captiveRecheck = 5 * time.Minute

func (d *Daemon) senseOnce(ctx context.Context, ns config.NetworkSense, tr *probeTracker) {
	snap, err := d.NetSnapshot()
	if err != nil {
		d.Logger.Printf("NetworkSense 采集失败：%v", err)
		return
	}
	var own []string
	if t, ok := d.Engine.(interface{ TUNName() string }); ok && t.TUNName() != "" {
		own = append(own, t.TUNName())
	}
	a := netsense.Analyze(snap, own...)
	d.mu.RLock()
	prevKey, prevCaptive := d.env.Key(), d.captive
	phys := snap.PhysicalDNS(d.ownDNS()...)
	physChanged := strings.Join(phys, ",") != strings.Join(d.physDNS, ",") && len(phys) > 0
	d.mu.RUnlock()
	captive, detail := prevCaptive, ""
	id := snap.NetworkID()
	changed := id != tr.networkID || a.Key() != tr.key
	tr.networkID, tr.key = id, a.Key()
	if ns.CaptivePortal && (changed || prevCaptive != netsense.Online || time.Since(tr.lastProbe) > captiveRecheck) {
		tr.lastProbe = time.Now()
		var probes []netsense.Probe
		for _, p := range ns.CaptiveProbes {
			probes = append(probes, netsense.ParseProbe(p))
		}
		check := d.CaptiveCheck
		if check == nil {
			check = netsense.CheckCaptive
		}
		d.mu.RLock()
		own := d.ownDNS()
		d.mu.RUnlock()
		captive, detail = check(ctx, probes, physicalPath(snap, own...))
	}
	if a.Key() == prevKey && captive == prevCaptive && !physChanged {
		return
	}
	d.mu.Lock()
	d.env, d.captive = a, captive
	if len(phys) > 0 {
		d.physDNS = phys
	}
	if detail != "" {
		d.captiveDetail = detail
	}
	d.mu.Unlock()
	d.Logger.Printf("网络环境变化：主接口 %s，外部 VPN %d 个，Captive=%s", a.PrimaryInterface, len(a.VPNs), captive)
	if err := d.Reload(ctx); err != nil {
		d.Logger.Printf("按网络环境重新加载失败：%v", err)
	}
}

// physicalPath 让 Captive 探测绕过 TUN 与 fake-ip：绑定物理接口，并使用物理网络的 DNS（优先 DHCP 下发值）。
// 不直接用接口的作用域 DNS：接管系统 DNS 后它也会变成 Velum 自己的地址，绑定物理接口后访问不到，探测会误判为离线。
func physicalPath(s netsense.Snapshot, own ...string) netsense.PhysicalPath {
	var p netsense.PhysicalPath
	if name := s.PhysicalInterface(); name != "" {
		if i, err := net.InterfaceByName(name); err == nil {
			p.IfIndex = i.Index
		}
		p.Nameservers = s.PhysicalDNS(own...)
	}
	return p
}

// senseInitial 在首次构建前同步分析一次环境（不做 Captive 探测），使启动时即可正确让路 / 共存。
func (d *Daemon) senseInitial() {
	if d.NetSnapshot == nil {
		return
	}
	snap, err := d.NetSnapshot()
	if err != nil {
		return
	}
	a := netsense.Analyze(snap)
	d.mu.Lock()
	d.env, d.physDNS = a, snap.PhysicalDNS(d.ownDNS()...)
	d.mu.Unlock()
}

// ownDNS 返回 Velum 接管系统 DNS 时使用的地址（需从物理 DNS 中剔除）。
func (d *Daemon) ownDNS() []string {
	if d.st != nil && len(d.st.plan.Route.SystemDNS) > 0 {
		return d.st.plan.Route.SystemDNS
	}
	return nil
}

// updater 定期 reload：拉取带 TTL，到期才真正请求；内核配置未变化时不重启引擎。
func (d *Daemon) updater(ctx context.Context) {
	for {
		interval := time.Hour
		d.mu.RLock()
		if st := d.st; st != nil {
			interval = minPositive(st.cfg.Subscription.UpdateInterval, st.cfg.Subscription.RuleSetUpdateInterval)
		}
		d.mu.RUnlock()
		if interval <= 0 {
			interval = time.Hour
		}
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-time.After(interval):
			if err := d.Reload(ctx); err == nil {
				d.Logger.Printf("定时更新完成")
			}
		}
	}
}

func minPositive(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0 || a < b:
		return a
	}
	return b
}

// syncSysProxy 让系统代理与当前计划一致：接管模式下写入（豁免变化也随之更新），否则按快照还原。
func (d *Daemon) syncSysProxy(st *state) {
	defer d.invalidateEffective()
	if d.SysProxy == nil {
		if st.plan.SysProxy.Enabled {
			d.setErr(errors.New("当前平台不支持接管系统代理"))
		}
		return
	}
	if st.plan.SysProxy.Enabled {
		if err := d.SysProxy.Apply(st.plan.SysProxy); err != nil {
			d.setErr(fmt.Errorf("接管系统代理：%w", err))
			return
		}
		d.Logger.Printf("已接管系统代理 → %s:%d（%d 条豁免）", st.plan.SysProxy.Host, st.plan.SysProxy.Port, len(st.plan.SysProxy.Exceptions))
		return
	}
	if d.SysProxy.HasSnapshot() {
		if err := d.SysProxy.Restore(); err != nil {
			d.setErr(fmt.Errorf("还原系统代理：%w", err))
			return
		}
		d.Logger.Printf("系统代理已还原")
	}
}

// ---------------- 本地接口 ----------------

func (d *Daemon) handler() (h http.Handler) {
	mux := http.NewServeMux()
	defer func() { h = d.whileStarting(mux) }()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.status()) })
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Reload(r.Context()); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, d.status())
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Update(r.Context()); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, d.status())
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		go func() { time.Sleep(100 * time.Millisecond); d.Shutdown() }()
	})
	mux.HandleFunc("POST /v1/why", d.why)
	mux.HandleFunc("POST /v1/groups/select", d.selectMember)
	mux.HandleFunc("POST /v1/groups/test", d.testGroup)
	mux.HandleFunc("GET /v1/overlay", d.getOverlay)
	mux.HandleFunc("PUT /v1/overlay", d.putOverlay)
	mux.HandleFunc("GET /v1/overlay/items", d.getOverlayItems)
	mux.HandleFunc("POST /v1/overlay/items", d.editOverlayItem)
	mux.HandleFunc("GET /v1/subscription", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(d.Paths)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, rpc.SubscriptionConfig{URL: cfg.Subscription.URL, File: cfg.Subscription.File, UserAgent: cfg.Subscription.UserAgent})
	})
	mux.HandleFunc("PUT /v1/subscription", d.putSubscription)
	mux.HandleFunc("GET /v1/tun/apps", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(d.Paths)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, rpc.TUNApps{IncludeApps: nonNil(cfg.TUN.IncludeApps), ExcludeApps: nonNil(cfg.TUN.ExcludeApps)})
	})
	mux.HandleFunc("PUT /v1/tun/apps", d.putTUNApps)
	mux.HandleFunc("GET /v1/helper", func(w http.ResponseWriter, r *http.Request) {
		if runtime.GOOS == "windows" { // Windows 没有 helper：TUN 组件即 velumd 服务本身
			hs := rpc.HelperStatus{Socket: "service", Version: Version, Reachable: d.TUNUnavailable == "", Error: d.TUNUnavailable}
			writeJSON(w, hs)
			return
		}
		sock := helper.DefaultSocket
		if cfg, err := config.Load(d.Paths); err == nil && cfg.TUN.HelperSocket != "" {
			sock = cfg.TUN.HelperSocket
		}
		hs := rpc.HelperStatus{Socket: sock, Version: helper.Version}
		if c, err := helper.Dial(sock); err != nil {
			hs.Error = err.Error()
			hs.Outdated = errors.Is(err, helper.ErrVersionMismatch)
		} else {
			hs.Reachable = true
			_ = c.Close()
		}
		writeJSON(w, hs)
	})
	mux.HandleFunc("POST /v1/repair", d.repair)
	mux.HandleFunc("POST /v1/switches", d.setSwitches)
	mux.HandleFunc("GET /v1/connections", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		host := strings.ToLower(r.URL.Query().Get("host"))
		var out []engine.Connection
		for _, c := range d.Engine.Connections(0) {
			if host == "" || matchHost(c, host) {
				out = append(out, c)
				if len(out) >= limit {
					break
				}
			}
		}
		writeJSON(w, rpc.ConnectionsResponse{Connections: out})
	})
	mux.HandleFunc("GET /v1/connections/stream", d.stream)
	mux.HandleFunc("GET /v1/warnings", func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		defer d.mu.RUnlock()
		var ws []ir.Warning
		if d.st != nil {
			ws = append(ws, d.st.plan.Warnings...)
			ws = append(ws, d.st.rendered.Warnings...)
		}
		writeJSON(w, rpc.WarningsResponse{Warnings: ws})
	})
	return mux
}

// effectiveTTL 是实际生效代理的缓存时间。
const effectiveTTL = 10 * time.Second

func (d *Daemon) effective() (sysproxy.Effective, error) {
	d.effMu.Lock()
	defer d.effMu.Unlock()
	if d.effAt.IsZero() || time.Since(d.effAt) >= effectiveTTL {
		d.eff, d.effErr = d.EffectiveProxy()
		d.effAt = time.Now()
	}
	return d.eff, d.effErr
}

func (d *Daemon) invalidateEffective() {
	d.effMu.Lock()
	d.effAt = time.Time{}
	d.effMu.Unlock()
}

func (d *Daemon) status() rpc.Status {
	// 在持锁之外读取实际生效的代理，避免慢操作阻塞重新加载、切换等写操作
	var eff *sysproxy.Effective
	if d.SysProxy != nil && d.EffectiveProxy != nil && d.SysProxy.HasSnapshot() {
		if e, err := d.effective(); err == nil {
			eff = &e
		}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := rpc.Status{PID: os.Getpid(), Version: Version, Running: d.Engine.Running(), Home: d.Paths.Home, StartedAt: d.startedAt, Starting: d.starting, LastError: d.lastErr,
		Overridden: d.sysOverride != nil || d.tunOverride != nil}
	if d.SysProxy != nil {
		s.SysProxy = rpc.SysProxyStatus{Supported: true, Active: d.SysProxy.HasSnapshot()}
		if s.SysProxy.Active && d.st != nil && eff != nil {
			p := d.st.plan.SysProxy
			s.SysProxy.Overridden = eff.Overridden(p.Host, int(p.Port))
			s.SysProxy.PrimaryInterface = eff.PrimaryInterface
			if eff.HTTP.Enabled {
				s.SysProxy.Effective = fmt.Sprintf("%s:%d", eff.HTTP.Server, eff.HTTP.Port)
			}
		}
	}
	if d.NetSnapshot != nil {
		s.Network = &rpc.NetworkStatus{PrimaryInterface: d.env.PrimaryInterface, VPNs: d.env.VPNs, SplitDNSDomains: d.env.SplitDNSDomains,
			Captive: string(d.captive), CaptiveDetail: d.captiveDetail}
	}
	if st := d.st; st != nil {
		s.Listen, s.LoadedAt, s.Subscription = st.cfg.Listen, st.loadedAt, st.sub
		s.SystemProxyOn, s.TUNOn, s.WantSystemProxy, s.WantTUN = st.sysOn, st.tunOn, st.wantSys, st.wantTUN
		s.Mode = modeName(st.sysOn, st.tunOn)
		if s.Network == nil && st.paused != "" { // 未启用 NetworkSense 时也要显示暂停原因（如 TUN 不可用）
			s.Network = &rpc.NetworkStatus{}
		}
		if s.Network != nil {
			s.Network.Paused = st.paused
		}
		s.Warnings = len(st.plan.Warnings) + len(st.rendered.Warnings)
		if st.plan.Route.Enabled {
			s.TUN = &rpc.TUNStatus{Address: st.plan.Route.Address, Address6: st.plan.Route.Address6, Routes: st.plan.Route.Routes,
				Exclude: st.plan.Route.Exclude, SystemDNS: st.plan.Route.SystemDNS, IncludeApps: st.plan.Route.IncludeApps, ExcludeApps: st.plan.Route.ExcludeApps}
			if t, ok := d.Engine.(interface{ TUNName() string }); ok {
				s.TUN.Name = t.TUNName()
			}
		}
		delayer, _ := d.Engine.(interface{ Delay(string) int })
		for _, g := range st.plan.IR.Groups {
			gs := rpc.GroupStatus{Tag: g.Tag, Type: g.Type, Selected: d.Engine.Selected(g.Tag), Members: g.Members}
			if delayer != nil {
				for _, m := range g.Members {
					if v := delayer.Delay(m); v > 0 {
						if gs.Delays == nil {
							gs.Delays = map[string]int{}
						}
						gs.Delays[m] = v
					}
				}
			}
			s.Groups = append(s.Groups, gs)
		}
	}
	return s
}

func (d *Daemon) why(w http.ResponseWriter, r *http.Request) {
	var req rpc.WhyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := explain.ParseTarget(req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	for _, s := range req.Resolved {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("--resolve 地址无效：%s", s))
			return
		}
		t.ResolvedIPs = append(t.ResolvedIPs, a)
	}
	t.ProcessPath, t.Network = req.Process, req.Network
	d.mu.RLock()
	st := d.st
	d.mu.RUnlock()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("配置尚未成功加载，见 vlu status"))
		return
	}
	var resp rpc.WhyResponse
	switch {
	case len(t.ResolvedIPs) > 0:
		resp.ResolvedBy = "user"
	case t.Domain != "" && !req.NoResolve:
		// 用运行中内核的 DNS 路由器解析：与真实连接使用同一套 DNS 规则（而非系统 DNS / fake-ip）
		if rs, ok := d.Engine.(interface {
			Resolve(context.Context, string) ([]netip.Addr, error)
		}); ok && d.Engine.Running() {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			addrs, err := rs.Resolve(ctx, t.Domain)
			cancel()
			if err != nil {
				resp.ResolveErr = err.Error()
			} else {
				t.ResolvedIPs, resp.ResolvedBy = addrs, "engine"
			}
		}
	}
	for _, a := range t.ResolvedIPs {
		resp.Resolved = append(resp.Resolved, a.String())
	}
	loaded, errs := d.loadRuleSets(st)
	if len(errs) > 0 {
		resp.RuleSetErrors = errs
	}
	resp.Report = explain.WhyWith(st.plan, t, loaded)
	if dec := resp.Report.Decision; dec.Action == ir.ActionRoute {
		for cur, i := dec.Outbound, 0; cur != "" && i < 8; i++ {
			resp.Chain = append(resp.Chain, cur)
			cur = d.Engine.Selected(cur)
		}
	}
	writeJSON(w, resp)
}

type ruleSetCacheEntry struct {
	mod   time.Time
	conds []ir.Cond
	err   string
}

// RuleSetLoader 把本地规则集文件读为 IR 条件（由 velumd 注入 sing-box 实现；为 nil 时不加载）。
var RuleSetLoader func(path, format string) ([]ir.Cond, error)

// loadRuleSets 加载计划中的本地文件规则集（GeoIP / GeoSite 等），按路径与修改时间缓存。
func (d *Daemon) loadRuleSets(st *state) (map[string][]ir.Cond, map[string]string) {
	if RuleSetLoader == nil {
		return nil, nil
	}
	loaded, errs := map[string][]ir.Cond{}, map[string]string{}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rsCache == nil {
		d.rsCache = map[string]ruleSetCacheEntry{}
	}
	for _, rs := range st.plan.IR.RuleSets {
		if rs.Kind != ir.RuleSetFile {
			continue
		}
		info, err := os.Stat(rs.Path)
		if err != nil {
			errs[rs.Tag] = err.Error()
			continue
		}
		e, ok := d.rsCache[rs.Path]
		if !ok || !e.mod.Equal(info.ModTime()) {
			conds, err := RuleSetLoader(rs.Path, rs.Format)
			e = ruleSetCacheEntry{mod: info.ModTime(), conds: conds}
			if err != nil {
				e.err = err.Error()
			}
			d.rsCache[rs.Path] = e
		}
		if e.err != "" {
			errs[rs.Tag] = e.err
			continue
		}
		loaded[rs.Tag] = e.conds
	}
	return loaded, errs
}

// ---------------- 订阅 ----------------

// putSubscription 设置订阅地址：拉取 → 解析（必须有节点）→ 只改 config.yaml 中对应的行 → 重新加载；失败时改回原文件。
// 地址为空表示清除订阅。
func (d *Daemon) putSubscription(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	cfg, err := config.Load(d.Paths)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if req.URL != "" {
		if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("订阅地址应以 http:// 或 https:// 开头"))
			return
		}
		ua := cfg.Subscription.UserAgent
		if req.UserAgent != "" {
			ua = req.UserAgent
		}
		f := source.New(d.Paths.Cache, ua)
		f.TTL, f.Log = 0, d.Logger.Printf
		if d.Engine.Running() {
			f.Fallback = &url.URL{Scheme: "http", Host: cfg.ListenAddr().String()}
		}
		start := time.Now()
		defer func() { d.Logger.Printf("设置订阅：用时 %.1fs", time.Since(start).Seconds()) }()
		data, res, err := f.Get("subscription", req.URL, 0)
		if err == nil && res.Err != "" {
			err = errors.New(res.Err) // 拉取失败、回落到了旧缓存：校验时不算成功
		}
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("拉取订阅失败：%w", err))
			return
		}
		sub, err := subscription.Parse(data, subscription.Options{File: "subscription", ReadFile: f.ProviderReader(), GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL})
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("订阅内容无法识别（需要 Clash / Stash / mihomo 格式）：%w", err))
			return
		}
		if len(sub.Nodes) == 0 {
			writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("订阅中没有可用节点"))
			return
		}
	}
	fields := map[string]string{"url": req.URL, "file": ""}
	if req.UserAgent != "" {
		fields["user_agent"] = req.UserAgent
	}
	old, err := userfs.ReadFile(d.Paths.Config)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	data, err := config.SetFields(old, "subscription", fields, []string{"url", "file", "user_agent"})
	if err == nil {
		err = userfs.WriteFileAtomic(d.Paths.Config, data, 0o600)
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := d.Reload(r.Context()); err != nil {
		_ = userfs.WriteFileAtomic(d.Paths.Config, old, 0o600)
		_ = d.Reload(r.Context())
		writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("应用失败，已改回原配置：%w", err))
		return
	}
	writeJSON(w, d.status())
}

// putTUNApps 设置按应用分流（Android）：写入 config.yaml 的 tun 节，重新加载失败时改回原配置。
func (d *Daemon) putTUNApps(w http.ResponseWriter, r *http.Request) {
	var req rpc.TUNApps
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	clean := func(xs []string) []string {
		out := []string{}
		seen := map[string]bool{}
		for _, x := range xs {
			if x = strings.TrimSpace(x); x != "" && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	old, err := userfs.ReadFile(d.Paths.Config)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	data, err := config.SetLists(old, "tun", map[string][]string{"include_apps": clean(req.IncludeApps), "exclude_apps": clean(req.ExcludeApps)},
		[]string{"include_apps", "exclude_apps"})
	if err == nil {
		err = userfs.WriteFileAtomic(d.Paths.Config, data, 0o600)
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := d.Reload(r.Context()); err != nil {
		_ = userfs.WriteFileAtomic(d.Paths.Config, old, 0o600)
		_ = d.Reload(r.Context())
		writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("应用失败，已改回原配置：%w", err))
		return
	}
	writeJSON(w, d.status())
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// ---------------- 开关 ----------------

func modeName(sys, tun bool) string {
	switch {
	case sys && tun:
		return "tun+sysproxy"
	case tun:
		return "tun"
	case sys:
		return "sysproxy"
	}
	return "proxy"
}

// setSwitches 切换系统代理 / TUN：保存到 state.json（重启后保持），不改 config.yaml；失败时恢复原状态。
func (d *Daemon) setSwitches(w http.ResponseWriter, r *http.Request) {
	var req rpc.SwitchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parse := func(v string, cur *bool) (*bool, error) {
		switch v {
		case "":
			return cur, nil
		case "config":
			return nil, nil
		case "on", "off":
			b := v == "on"
			return &b, nil
		}
		return nil, fmt.Errorf("开关取值无效：%q（可选 on / off / config）", v)
	}
	d.mu.Lock()
	prevSys, prevTUN := d.sysOverride, d.tunOverride
	sys, err1 := parse(req.SystemProxy, d.sysOverride)
	tun, err2 := parse(req.TUN, d.tunOverride)
	if err1 != nil || err2 != nil {
		d.mu.Unlock()
		writeErr(w, http.StatusBadRequest, errors.Join(err1, err2))
		return
	}
	d.sysOverride, d.tunOverride = sys, tun
	d.mu.Unlock()
	if err := d.Reload(r.Context()); err != nil {
		d.mu.Lock()
		d.sysOverride, d.tunOverride = prevSys, prevTUN
		d.mu.Unlock()
		_ = d.Reload(r.Context())
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	d.saveState()
	writeJSON(w, d.status())
}

// ---------------- 策略组 ----------------

func (d *Daemon) selectMember(w http.ResponseWriter, r *http.Request) {
	var req rpc.SelectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	d.mu.RLock()
	st := d.st
	d.mu.RUnlock()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("配置尚未成功加载"))
		return
	}
	valid := false
	for _, g := range st.plan.IR.Groups {
		if g.Tag == req.Group {
			if g.Type != ir.GroupSelect {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("策略组 %q 为自动测速组，不能手动选择", req.Group))
				return
			}
			for _, m := range g.Members {
				valid = valid || m == req.Member
			}
		}
	}
	if !valid {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q 不是策略组 %q 的成员", req.Member, req.Group))
		return
	}
	if sel, ok := d.Engine.(interface{ SelectMember(string, string) error }); ok && d.Engine.Running() {
		if err := sel.SelectMember(req.Group, req.Member); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	d.mu.Lock()
	if d.selections == nil {
		d.selections = map[string]string{}
	}
	d.selections[req.Group] = req.Member
	d.mu.Unlock()
	d.saveState()
	writeJSON(w, d.status())
}

func (d *Daemon) testGroup(w http.ResponseWriter, r *http.Request) {
	var req rpc.TestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, ok := d.Engine.(interface {
		TestGroup(context.Context, string) (map[string]int, error)
	})
	if !ok || !d.Engine.Running() {
		writeErr(w, http.StatusServiceUnavailable, errors.New("引擎未运行"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := t.TestGroup(ctx, req.Group)
	if err != nil && len(res) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, rpc.TestResponse{Delays: res})
}

// stateFile 是用户在界面 / vlu 中做出的选择，重启后保持；config.yaml 只由用户手动编辑。
type stateFile struct {
	Selections  map[string]string `json:"selections,omitempty"`
	SystemProxy *bool             `json:"system_proxy,omitempty"` // 为空表示按 config.yaml
	TUN         *bool             `json:"tun,omitempty"`
}

func (d *Daemon) loadState() {
	data, err := os.ReadFile(d.Paths.State)
	if err != nil {
		return
	}
	var s stateFile
	if json.Unmarshal(data, &s) == nil {
		d.mu.Lock()
		d.selections, d.sysOverride, d.tunOverride = s.Selections, s.SystemProxy, s.TUN
		d.mu.Unlock()
	}
}

func (d *Daemon) saveState() {
	d.mu.RLock()
	s := stateFile{Selections: copyStrMap(d.selections), SystemProxy: d.sysOverride, TUN: d.tunOverride}
	d.mu.RUnlock()
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := writeFileAtomic(d.Paths.State, data); err != nil {
		d.Logger.Printf("保存状态失败：%v", err)
	}
}

func copyStrMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------- overlay ----------------

func (d *Daemon) overlayPath() (string, error) {
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return "", err
	}
	return cfg.Overlay, nil
}

func (d *Daemon) getOverlay(w http.ResponseWriter, r *http.Request) {
	path, err := d.overlayPath()
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	data, err := userfs.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, rpc.OverlayDoc{Path: path, Content: string(data)})
}

// putOverlay 校验后写入 overlay 并重新加载；加载失败时改回原内容。
func (d *Daemon) putOverlay(w http.ResponseWriter, r *http.Request) {
	var doc rpc.OverlayDoc
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := overlay.Parse([]byte(doc.Content)); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("overlay 校验失败：%w", err))
		return
	}
	path, err := d.overlayPath()
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	old, _ := userfs.ReadFile(path)
	if code, err := d.applyOverlay(r.Context(), path, old, []byte(doc.Content)); err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, d.status())
}

// applyOverlay 写入新的 overlay 并重新加载；加载失败时改回 old。
func (d *Daemon) applyOverlay(ctx context.Context, path string, old, data []byte) (int, error) {
	if err := userfs.WriteFileAtomic(path, data, 0o600); err != nil {
		return http.StatusInternalServerError, err
	}
	if err := d.Reload(ctx); err != nil {
		_ = userfs.WriteFileAtomic(path, old, 0o600)
		_ = d.Reload(ctx)
		return http.StatusUnprocessableEntity, fmt.Errorf("应用失败，已改回原内容：%w", err)
	}
	return 0, nil
}

// getOverlayItems 返回结构化的 overlay 条目与系统 hosts 文件条目，供界面以列表编辑。
func (d *Daemon) getOverlayItems(w http.ResponseWriter, r *http.Request) {
	path, err := d.overlayPath()
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	ov, err := overlay.Load(path)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	resp := rpc.OverlayItems{Path: path, Entries: []rpc.OverlayEntry{}}
	for _, e := range ov.Entries() {
		resp.Entries = append(resp.Entries, rpc.OverlayEntry(e))
	}
	resp.SystemHosts, resp.SystemHostsTotal = explain.SystemHostsEntries(200)
	d.mu.RLock()
	if st := d.st; st != nil {
		resp.SystemHostsOff = st.plan.IR.DNS.NoSystemHosts
	}
	d.mu.RUnlock()
	writeJSON(w, resp)
}

// editOverlayItem 添加或删除一个 overlay 条目（保留文件中的其余内容与注释），随后重新加载。
func (d *Daemon) editOverlayItem(w http.ResponseWriter, r *http.Request) {
	var req rpc.OverlayEdit
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path, err := d.overlayPath()
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	old, err := userfs.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	e := overlay.Entry(req.Entry)
	var data []byte
	switch req.Op {
	case "add":
		data, _, err = overlay.Add(old, e)
	case "remove":
		data, err = overlay.Remove(old, e)
	default:
		err = fmt.Errorf("未知操作 %q", req.Op)
	}
	if err == nil {
		_, err = overlay.Parse(data)
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if code, err := d.applyOverlay(r.Context(), path, old, data); err != nil {
		writeErr(w, code, err)
		return
	}
	writeJSON(w, d.status())
}

// ---------------- 一键修复 ----------------

// FlushDNSCache 清空系统 DNS 缓存（平台相关，由 velumd 注入；为 nil 时跳过）。
var FlushDNSCache func() error

func (d *Daemon) repair(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var steps []rpc.RepairStep
	step := func(name string, err error, detail string) {
		s := rpc.RepairStep{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			s.Detail = err.Error()
		}
		steps = append(steps, s)
	}
	if FlushDNSCache != nil {
		step("清空系统 DNS 缓存", FlushDNSCache(), "")
	}
	d.mu.RLock()
	st := d.st
	d.mu.RUnlock()
	if d.NetSnapshot != nil && st != nil && st.cfg.NetworkSense.Enabled {
		d.senseOnce(ctx, st.cfg.NetworkSense, &probeTracker{}) // 全新 tracker：强制重新探测
		d.mu.RLock()
		detail := fmt.Sprintf("主接口 %s，外部 VPN %d 个，连通性 %s", d.env.PrimaryInterface, len(d.env.VPNs), d.captive)
		d.mu.RUnlock()
		step("重新检测网络环境", nil, detail)
	}
	err := d.Update(ctx)
	step("重新拉取订阅与规则集并加载", err, "")
	d.mu.RLock()
	st = d.st
	d.mu.RUnlock()
	if st != nil && st.plan.SysProxy.Enabled && d.SysProxy != nil {
		step("校正系统代理", d.SysProxy.Apply(st.plan.SysProxy), "")
	}
	if t, ok := d.Engine.(interface {
		TestGroup(context.Context, string) (map[string]int, error)
	}); ok && st != nil && d.Engine.Running() {
		ok, total := 0, 0
		for _, g := range st.plan.IR.Groups {
			if g.Type != ir.GroupURLTest {
				continue
			}
			tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			res, _ := t.TestGroup(tctx, g.Tag)
			cancel()
			for _, v := range res {
				total++
				if v > 0 {
					ok++
				}
			}
		}
		step("节点测速", nil, fmt.Sprintf("%d / %d 个节点可用", ok, total))
	}
	writeJSON(w, rpc.RepairResponse{Steps: steps})
}

func (d *Daemon) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := d.Engine.Subscribe()
	defer cancel()
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-d.stop:
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(c)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

func matchHost(c engine.Connection, host string) bool {
	if strings.EqualFold(c.Domain, host) {
		return true
	}
	h, _, err := net.SplitHostPort(c.Destination)
	if err != nil {
		h = c.Destination
	}
	return strings.EqualFold(strings.Trim(h, "[]"), host)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(rpc.ErrorResponse{Error: err.Error()})
}

// redact 去掉订阅 URL 中的查询参数与凭据，只保留 scheme://host/path。
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

func modTime(p string) time.Time {
	if st, err := userfs.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// whileStarting 在首次启动完成前拒绝写操作（重新加载、切换开关等），避免与首次加载并发；只读接口照常响应。
func (d *Daemon) whileStarting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		starting := d.starting
		d.mu.RUnlock()
		if starting && r.Method != http.MethodGet {
			writeErr(w, http.StatusServiceUnavailable, errors.New("Velum 正在启动，请稍候再试"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
