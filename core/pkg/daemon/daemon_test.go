package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

// fakeEngine 记录调用，不启动内核。
type fakeEngine struct {
	mu       sync.Mutex
	running  bool
	restarts int
	last     engine.Config
	gate     chan struct{} // 非 nil 时 Start 等它关闭（模拟首次加载耗时）
}

func (f *fakeEngine) Start(_ context.Context, c engine.Config) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running, f.last = true, c
	return nil
}
func (f *fakeEngine) Restart(_ context.Context, c engine.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running, f.last = true, c
	f.restarts++
	return nil
}
func (f *fakeEngine) Stop() error                         { f.mu.Lock(); f.running = false; f.mu.Unlock(); return nil }
func (f *fakeEngine) Running() bool                       { f.mu.Lock(); defer f.mu.Unlock(); return f.running }
func (f *fakeEngine) Connections(int) []engine.Connection { return nil }
func (f *fakeEngine) Subscribe() (<-chan engine.Connection, func()) {
	return make(chan engine.Connection), func() {}
}
func (f *fakeEngine) Selected(tag string) string {
	if tag == "Proxy" {
		return "Auto"
	}
	return ""
}

func setup(t *testing.T) (config.Paths, *fakeEngine, *rpc.Client, func()) {
	t.Helper()
	return setupEngine(t, &fakeEngine{})
}

func setupEngine(t *testing.T, eng *fakeEngine) (config.Paths, *fakeEngine, *rpc.Client, func()) {
	t.Helper()
	// Unix socket 路径有长度上限，使用短的临时目录
	home, err := os.MkdirTemp(tmpBase(), "velum-test-")
	if err != nil {
		t.Fatal(err)
	}
	td, _ := filepath.Abs("../../testdata")
	paths := testPaths(home)
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(config.Template, `  file: ""`, `  file: '`+filepath.Join(td, "coverage/config.yaml")+`'`, 1)
	cfg = strings.Replace(cfg, "listen: 127.0.0.1:7899", "listen: 127.0.0.1:17999", 1)
	cfg = strings.Replace(cfg, "prefer_local_providers: false", "prefer_local_providers: true", 1)           // 测试不出网
	cfg = strings.Replace(cfg, subscription.DefaultGeoRuleSetURL, "http://127.0.0.1:9/{kind}-{code}.srs", 1) // 规则集下载立即失败，走降级
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	ov, _ := os.ReadFile(filepath.Join(td, "overlay.yaml"))
	_ = os.WriteFile(paths.Overlay, ov, 0o600)

	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if st, err := c.Status(); err == nil && (!st.Starting || eng.gate != nil) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return paths, eng, c, func() {
		_ = c.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("velumd 未在 3 秒内退出")
		}
		_ = os.RemoveAll(home)
	}
}

func TestDaemonLifecycle(t *testing.T) {
	paths, eng, c, cleanup := setup(t)
	defer cleanup()

	st, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Listen != "127.0.0.1:17999" || st.Subscription.Nodes == 0 || st.LastError != "" {
		t.Fatalf("status: %+v", st)
	}
	if len(eng.last.RuleSources) == 0 {
		t.Fatal("引擎未收到 RuleSources")
	}

	w, err := c.Why(rpc.WhyRequest{Target: "https://api.openai.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Report.Decision.Outbound != "Proxy" || strings.Join(w.Chain, ">") != "Proxy>Auto" {
		t.Fatalf("why: %+v chain=%v", w.Report.Decision, w.Chain)
	}

	// 写坏 overlay：reload 失败，旧配置继续生效，错误可见
	good, _ := os.ReadFile(paths.Overlay)
	_ = os.WriteFile(paths.Overlay, []byte("dev_bypass:\n  cidrs: [bad]\n"), 0o600)
	if _, err := c.Reload(); err == nil || !strings.Contains(err.Error(), "invalid cidr") {
		t.Fatalf("reload 应失败：%v", err)
	}
	if eng.restarts != 0 {
		t.Fatal("构建失败时不应重启引擎")
	}
	st, _ = c.Status()
	if st.LastError == "" || !st.Running {
		t.Fatalf("status: %+v", st)
	}
	if _, err := c.Why(rpc.WhyRequest{Target: "https://api.openai.com/"}); err != nil {
		t.Fatalf("旧配置下 why 应仍可用：%v", err)
	}

	// 修复后 reload 成功、错误清除；内核配置与运行中的一致，不应重启引擎
	_ = os.WriteFile(paths.Overlay, good, 0o600)
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if st, _ = c.Status(); st.LastError != "" || eng.restarts != 0 {
		t.Fatalf("配置未变化时不应重启引擎：%+v restarts=%d", st, eng.restarts)
	}
	// 配置有变化：重启引擎
	_ = os.WriteFile(paths.Overlay, []byte(strings.Replace(string(good), "192.0.2.7", "192.0.2.8", 1)), 0o600)
	if _, err := c.Reload(); err != nil || eng.restarts != 1 {
		t.Fatalf("配置变化应重启引擎：%v restarts=%d", err, eng.restarts)
	}
	// update：强制拉取后加载（本地文件订阅，结果不变 → 不重启）
	if st, err := c.Update(); err != nil || st.LastError != "" || eng.restarts != 1 {
		t.Fatalf("update: %v %+v restarts=%d", err, st, eng.restarts)
	}

	ws, err := c.Warnings()
	if err != nil || len(ws.Warnings) == 0 {
		t.Fatalf("warnings: %v %d", err, len(ws.Warnings))
	}
	degradedSets := 0
	for _, w := range ws.Warnings {
		if w.Category == "ruleset" && strings.Contains(w.Reason, "空集") {
			degradedSets++
		}
	}
	if degradedSets == 0 {
		t.Fatal("规则集下载失败应降级为空集并告警，而不是阻断启动")
	}
}

func TestDaemonRejectsSecondInstance(t *testing.T) {
	paths, _, _, cleanup := setup(t)
	defer cleanup()
	d2 := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	if err := d2.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "已在运行") {
		t.Fatalf("第二个实例应拒绝启动：%v", err)
	}
}

func TestEmptyConfigStillServes(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	var st *rpc.Status
	var err error
	for i := 0; i < 100; i++ {
		if st, err = c.Status(); err == nil && !st.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || !st.Running || st.Subscription.Source != "" {
		t.Fatalf("未配置订阅时应以空配置运行：%v %+v", err, st)
	}
	if _, err := os.Stat(paths.Config); err != nil {
		t.Fatal("首次运行应生成 config.yaml")
	}
}

// fakeSysProxy 记录调用，不触碰系统设置。
type fakeSysProxy struct {
	mu       sync.Mutex
	active   bool
	applied  []compiler.SysProxyPlan
	restores int
	heals    int
	healRes  sysproxy.HealResult
}

func (f *fakeSysProxy) Apply(p compiler.SysProxyPlan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = true
	f.applied = append(f.applied, p)
	return nil
}
func (f *fakeSysProxy) Restore() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active {
		f.restores++
	}
	f.active = false
	return nil
}
func (f *fakeSysProxy) Heal() (sysproxy.HealResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heals++
	return f.healRes, nil
}
func (f *fakeSysProxy) HasSnapshot() bool                  { f.mu.Lock(); defer f.mu.Unlock(); return f.active }
func (f *fakeSysProxy) ClearOwn(string, int) (bool, error) { return false, nil }

func TestDaemonSysProxyLifecycle(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	_ = os.WriteFile(paths.Overlay, []byte("system_proxy:\n  bypass: [localhost]\n"), 0o600)
	sp := &fakeSysProxy{healRes: sysproxy.HealRestored}
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	d.SysProxy = sp
	// 模拟主网络服务被其他 VPN 接管：实际生效的仍是旧代理
	d.EffectiveProxy = func() (sysproxy.Effective, error) {
		return sysproxy.Effective{HTTP: sysproxy.Proxy{Enabled: true, Server: "127.0.0.1", Port: 7890}, PrimaryInterface: "utun9"}, nil
	}
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sp.heals != 1 {
		t.Fatalf("启动时应先自愈：heals=%d", sp.heals)
	}
	if len(sp.applied) != 0 {
		t.Fatal("mode: proxy 时不应接管系统代理")
	}
	st, err := c.Switch(rpc.SwitchRequest{SystemProxy: "on"})
	if err != nil || !st.SysProxy.Active || !st.Overridden || st.Mode != "sysproxy" {
		t.Fatalf("sysproxy on: %v %+v", err, st)
	}
	if !st.SysProxy.Overridden || st.SysProxy.Effective != "127.0.0.1:7890" || st.SysProxy.PrimaryInterface != "utun9" {
		t.Fatalf("应报告被覆盖：%+v", st.SysProxy)
	}
	if p := sp.applied[0]; p.Port != 7899 || len(p.Exceptions) != 1 || p.Exceptions[0].Pattern != "localhost" {
		t.Fatalf("接管计划：%+v", p)
	}
	if _, err := c.Reload(); err != nil || len(sp.applied) != 2 {
		t.Fatalf("reload 后应以新计划重新写入：%v applied=%d", err, len(sp.applied))
	}
	if st, _ := c.Switch(rpc.SwitchRequest{SystemProxy: "off"}); st.SysProxy.Active || sp.restores != 1 {
		t.Fatalf("sysproxy off 应还原：%+v restores=%d", st.SysProxy, sp.restores)
	}
	_, _ = c.Switch(rpc.SwitchRequest{SystemProxy: "on"})
	_ = c.Stop()
	<-done
	if sp.active || sp.restores != 2 {
		t.Fatalf("退出时应还原系统代理：active=%v restores=%d", sp.active, sp.restores)
	}
}

// 系统代理已是当前设置（仍在接管、实际生效的仍指向 Velum）时，重新加载不再重复写入；设置变化或被改走时才写。
func TestDaemonSysProxySkipsUnchanged(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	sp := &fakeSysProxy{}
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	d.SysProxy = sp
	var mu sync.Mutex
	effPort := 7899
	d.EffectiveProxy = func() (sysproxy.Effective, error) {
		mu.Lock()
		defer mu.Unlock()
		return sysproxy.Effective{HTTP: sysproxy.Proxy{Enabled: true, Server: "127.0.0.1", Port: effPort}}, nil
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	applied := func() int { sp.mu.Lock(); defer sp.mu.Unlock(); return len(sp.applied) }
	if _, err := c.Switch(rpc.SwitchRequest{SystemProxy: "on"}); err != nil || applied() != 1 {
		t.Fatalf("开启应写入：%v applied=%d", err, applied())
	}
	if _, err := c.Reload(); err != nil || applied() != 1 {
		t.Fatalf("设置未变化时不应重复写入：applied=%d", applied())
	}
	_ = os.WriteFile(paths.Overlay, []byte("system_proxy:\n  bypass: [localhost]\n"), 0o600)
	if _, err := c.Reload(); err != nil || applied() != 2 {
		t.Fatalf("豁免变化后应重新写入：applied=%d", applied())
	}
	mu.Lock()
	effPort = 7890 // 被其他代理工具改走
	mu.Unlock()
	if _, err := c.Reload(); err != nil || applied() != 3 {
		t.Fatalf("被改走后应重新写入：applied=%d", applied())
	}
}

type fakeOpener struct {
	calls [][]string
	dns   [][]string
}

func (f *fakeOpener) OpenTUN(req engine.TUNRequest) (int, string, io.Closer, error) {
	f.calls = append(f.calls, req.Routes)
	f.dns = append(f.dns, req.SystemDNS)
	return -1, "utun-fake", io.NopCloser(nil), nil
}

func TestDaemonTUNMode(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	cfg := strings.Replace(config.Template, "route_include: []", "route_include: [223.6.6.6/32]", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	op := &fakeOpener{}
	d.TUNOpener = op
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := c.Switch(rpc.SwitchRequest{TUN: "on"})
	if err != nil || st.TUN == nil || st.Mode != "tun" {
		t.Fatalf("%v %+v", err, st)
	}
	want := "172.19.0.0/30,223.6.6.6/32,fdfe:dcba:9876::/126" // route_include 只含 IPv4，IPv6 只有 TUN 自身网段
	if strings.Join(st.TUN.Routes, ",") != want {
		t.Fatalf("routes=%v", st.TUN.Routes)
	}
	if len(st.TUN.SystemDNS) != 0 {
		t.Fatal("未获取物理 DNS 时不应接管系统 DNS")
	}
	if eng.last.TUN == nil || eng.last.TUN.Opener != op || strings.Join(eng.last.TUN.Routes, ",") != want {
		t.Fatalf("引擎未收到 TUN 配置：%+v", eng.last.TUN)
	}
	if !strings.Contains(string(eng.last.JSON), `"type": "tun"`) {
		t.Fatal("渲染结果缺少 tun 入站")
	}
	// 只改路由：内核配置（JSON）不变，但路由由平台层设置，仍需重启引擎重新打开 TUN
	cfg = strings.Replace(config.Template, "route_include: []", "route_include: [223.5.5.5/32]", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(eng.last.TUN.Routes, ","); !strings.Contains(got, "223.5.5.5/32") {
		t.Fatalf("修改路由后引擎未重启：%s", got)
	}
	if st, _ = c.Switch(rpc.SwitchRequest{TUN: "off"}); st.TUN != nil || eng.last.TUN != nil {
		t.Fatal("关闭 TUN 后不应再有 TUN 配置")
	}
}

// TUN 不可用（Windows 未以服务运行）：开启 TUN 时显示暂停原因，不交给引擎；/v1/helper 报告不可用。
func TestDaemonTUNUnavailable(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	d.TUNOpener = &fakeOpener{}
	d.TUNUnavailable = "TUN 需要安装 Velum 服务"
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := c.Switch(rpc.SwitchRequest{TUN: "on"})
	if err != nil || !st.WantTUN || st.TUNOn || st.Network == nil || st.Network.Paused != d.TUNUnavailable || eng.last.TUN != nil {
		t.Fatalf("%v %+v", err, st)
	}
}

func TestDaemonNetworkSense(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	cfg := strings.Replace(config.Template, "tun:\n  enabled: false", "tun:\n  enabled: true", 1)
	cfg = strings.Replace(cfg, "route_include: []", "route_include: [198.51.100.0/24]", 1)
	cfg = strings.Replace(cfg, "interval: 5s", "interval: 30ms", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)

	var mu sync.Mutex
	snap := netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}}}
	captive := netsense.Online
	set := func(s netsense.Snapshot, c netsense.CaptiveState) { mu.Lock(); snap, captive = s, c; mu.Unlock() }

	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	d.TUNOpener = &fakeOpener{}
	d.NetSnapshot = func() (netsense.Snapshot, error) { mu.Lock(); defer mu.Unlock(); return snap, nil }
	d.CaptiveCheck = func(context.Context, []netsense.Probe, netsense.PhysicalPath) (netsense.CaptiveState, string) {
		mu.Lock()
		defer mu.Unlock()
		return captive, "probe"
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	until := func(what string, ok func(*rpc.Status) bool) *rpc.Status {
		t.Helper()
		var st *rpc.Status
		for i := 0; i < 200; i++ {
			if s, err := c.Status(); err == nil && !s.Starting {
				st = s
				if ok(s) {
					return s
				}
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatalf("%s：最后状态 %+v network=%+v", what, st, st.Network)
		return nil
	}

	until("初始：TUN 启用", func(s *rpc.Status) bool { return s.TUN != nil && s.Network != nil && s.Network.Captive == "online" })

	// 其他工具全局接管 → TUN 让路
	set(netsense.Snapshot{PrimaryInterface: "utun4", Routes: []netsense.Route{
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "utun4"}, {Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}}}, netsense.Online)
	st := until("全局 VPN → 让路", func(s *rpc.Status) bool { return s.TUN == nil && strings.Contains(s.Network.Paused, "utun4") })
	if st.Mode != "proxy" || eng.last.TUN != nil {
		t.Fatalf("让路时不应启用 TUN：mode=%s", st.Mode)
	}

	// 分流型 VPN → 恢复 TUN，VPN 网段从 TUN 排除并走该 VPN
	set(netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}, {Dst: netip.MustParsePrefix("198.51.100.128/25"), Iface: "utun7"}},
		Resolvers: []netsense.Resolver{{Domain: "corp.example", Nameservers: []string{"198.51.100.200"}, Iface: "utun7"}}}, netsense.Online)
	st = until("分流 VPN → 共存", func(s *rpc.Status) bool { return s.TUN != nil && len(s.Network.VPNs) == 1 && s.Network.Paused == "" })
	if strings.Join(st.TUN.Routes, ",") != "172.19.0.0/30,198.51.100.0/25,fdfe:dcba:9876::/126" {
		t.Fatalf("VPN 网段应从 TUN 排除：%v", st.TUN.Routes)
	}
	if !strings.Contains(string(eng.last.JSON), `"bind_interface": "utun7"`) {
		t.Fatal("应生成绑定 utun7 的直连出站")
	}

	// Captive Portal → 暂停；认证后恢复
	set(netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}}}, netsense.Captive)
	until("Captive → 暂停", func(s *rpc.Status) bool {
		return s.TUN == nil && s.Network.Captive == "captive" && strings.Contains(s.Network.Paused, "网页认证")
	})
	set(netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}}}, netsense.Online)
	until("认证后恢复", func(s *rpc.Status) bool { return s.TUN != nil && s.Network.Paused == "" })
}

// TUN 未开启，系统 DNS 却仍指向 Velum 的 TUN 内 DNS（上次没还原干净，或被其他代理工具写回）：报告为残留。
func TestDaemonStaleOwnDNS(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	d.NetSnapshot = func() (netsense.Snapshot, error) {
		return netsense.Snapshot{PrimaryInterface: "en0",
			Routes:    []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}},
			Resolvers: []netsense.Resolver{{Nameservers: []string{"172.19.0.2"}, Iface: "en0"}}}, nil
	}
	d.CaptiveCheck = func(context.Context, []netsense.Probe, netsense.PhysicalPath) (netsense.CaptiveState, string) {
		return netsense.Online, ""
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting && s.Network != nil && strings.Join(s.Network.StaleDNS, ",") == "172.19.0.2" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("应报告残留的 Velum TUN 内 DNS")
}

// 一键修复：发现残留的系统 DNS 时交给 helper 改回自动获取，之后重新检测，不再报告冲突。
func TestRepairResetsStaleDNS(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	var mu sync.Mutex
	dns := []string{"172.19.0.2"}
	var asked []string
	lag := 0 // 还原后系统解析器配置晚一次才更新（scutil --dns 的实际表现）
	d.NetSnapshot = func() (netsense.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		cur := dns
		if lag > 0 {
			lag--
			cur = []string{"172.19.0.2"}
		}
		return netsense.Snapshot{PrimaryInterface: "en0",
			Routes:    []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}},
			Resolvers: []netsense.Resolver{{Nameservers: cur, Iface: "en0"}}}, nil
	}
	d.ResetDNS = func(servers []string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked, dns, lag = servers, []string{"192.168.1.1"}, 1
		return []string{"Wi-Fi"}, nil
	}
	d.CaptiveCheck = func(context.Context, []netsense.Probe, netsense.PhysicalPath) (netsense.CaptiveState, string) {
		return netsense.Online, ""
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, err := c.Repair()
	if err != nil {
		t.Fatal(err)
	}
	var reset, conflict *rpc.RepairStep
	for i := range r.Steps {
		switch r.Steps[i].Name {
		case "还原系统 DNS":
			reset = &r.Steps[i]
		case "检查其他代理工具":
			conflict = &r.Steps[i]
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if reset == nil || !reset.OK || strings.Join(asked, ",") != "172.19.0.2" {
		t.Fatalf("应还原残留 DNS：%+v asked=%v", r.Steps, asked)
	}
	if conflict == nil || !conflict.OK {
		t.Fatalf("还原后应不再报告冲突：%+v", conflict)
	}
}

// 接管系统 DNS：有物理 DNS 时，TUN 内的 DNS 地址交给 helper；原本经系统解析器的 DNS 改用物理 DNS 防回环。
func TestDaemonTUNSystemDNS(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	cfg := strings.Replace(config.Template, "tun:\n  enabled: false", "tun:\n  enabled: true", 1)
	cfg = strings.Replace(cfg, "route_include: []", "route_include: [198.51.100.0/24]", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	op := &fakeOpener{}
	d.TUNOpener = op
	d.NetSnapshot = func() (netsense.Snapshot, error) {
		return netsense.Snapshot{PrimaryInterface: "en0", DHCPNameservers: []string{"192.168.1.1"},
			Routes:    []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}},
			Resolvers: []netsense.Resolver{{Domain: "corp.example", Nameservers: []string{"10.0.0.53"}}}}, nil
	}
	d.CaptiveCheck = func(context.Context, []netsense.Probe, netsense.PhysicalPath) (netsense.CaptiveState, string) {
		return netsense.Online, ""
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	var st *rpc.Status
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting && s.TUN != nil {
			st = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st == nil || strings.Join(st.TUN.SystemDNS, ",") != "172.19.0.2" || strings.Join(eng.last.TUN.SystemDNS, ",") != "172.19.0.2" {
		t.Fatalf("应接管系统 DNS 到 172.19.0.2：%+v", st)
	}
	js := string(eng.last.JSON)
	if !strings.Contains(js, `"tag": "velum-local"`) || !strings.Contains(js, `"server": "192.168.1.1"`) {
		t.Fatal("velum-local 应改为直接询问物理 DNS 192.168.1.1，避免绕回 Velum")
	}
	// 系统 DNS 分流的域名直接询问为它配置的服务器；交给系统解析器会按默认 DNS（即 Velum）查询而回环
	if !strings.Contains(js, `"tag": "velum-system-0-0"`) || !strings.Contains(js, `"server": "10.0.0.53"`) || strings.Contains(js, `"tag": "velum-system",`) {
		t.Fatal("系统 DNS 分流的域名应直接询问 10.0.0.53，不再交给系统解析器")
	}
}

type resolvingEngine struct{ fakeEngine }

func (r *resolvingEngine) Resolve(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("1.0.1.9")}, nil
}

// vlu why：规则集文件由 RuleSetLoader 加载并缓存；没有 --resolve 时向引擎查询解析结果。
func TestDaemonWhyLoadsRuleSetsAndResolves(t *testing.T) {
	srs, err := singbox.EncodeRuleSet([]ir.Cond{{IPCIDR: []string{"1.0.1.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(srs) }))
	defer srv.Close()
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	subFile := filepath.Join(home, "sub.yaml")
	_ = os.WriteFile(subFile, []byte("proxies:\n  - {name: Proxy, type: socks5, server: 192.0.2.1, port: 1080}\nrules:\n  - GEOIP,CN,DIRECT\n  - MATCH,Proxy\n"), 0o600)
	cfg := strings.Replace(config.Template, `  file: ""`, `  file: '`+subFile+`'`, 1)
	cfg = strings.Replace(cfg, subscription.DefaultGeoRuleSetURL, srv.URL+"/{kind}-{code}.srs", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)

	daemon.RuleSetLoader = singbox.LoadRuleSetFile
	defer func() { daemon.RuleSetLoader = nil }()
	eng := &resolvingEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w, err := c.Why(rpc.WhyRequest{Target: "https://cn.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ResolvedBy != "engine" || strings.Join(w.Resolved, ",") != "1.0.1.9" || len(w.RuleSetErrors) != 0 {
		t.Fatalf("%+v", w)
	}
	if dec := w.Report.Decision; dec.Uncertain || dec.Outbound != ir.OutboundDirect {
		t.Fatalf("应确定命中 GEOIP,CN：%+v", dec)
	}
	w, _ = c.Why(rpc.WhyRequest{Target: "https://cn.example/", NoResolve: true})
	if !w.Report.Decision.Uncertain || w.ResolvedBy != "" {
		t.Fatalf("--no-resolve 时应标记不确定：%+v", w)
	}
}

func TestDaemonSelectionOverlayRepair(t *testing.T) {
	paths, eng, c, cleanup := setup(t)
	defer cleanup()

	// 选择持久化：写入 state.json，并渲染为 selector 的 default
	if _, err := c.Select("Media", "DIRECT"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("Auto", "socks"); err == nil {
		t.Fatal("自动测速组不能手动选择")
	}
	if _, err := c.Select("Media", "not-a-member"); err == nil {
		t.Fatal("非成员应报错")
	}
	data, _ := os.ReadFile(paths.State)
	if !strings.Contains(string(data), `"Media": "DIRECT"`) {
		t.Fatalf("state.json：%s", data)
	}
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eng.last.JSON), `"default": "DIRECT"`) {
		t.Fatal("选择应渲染为 selector 的 default，引擎重启后保持")
	}

	// overlay 读写：非法内容被拒绝且不落盘；合法内容写入并生效
	doc, err := c.Overlay()
	if err != nil || doc.Path != paths.Overlay || !strings.Contains(doc.Content, "dev_bypass") {
		t.Fatalf("%v %+v", err, doc)
	}
	if _, err := c.SaveOverlay("dev_bypass:\n  cidrs: [nope]\n"); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("应拒绝非法 overlay：%v", err)
	}
	if now, _ := os.ReadFile(paths.Overlay); string(now) != doc.Content {
		t.Fatal("校验失败时不应改动文件")
	}
	if _, err := c.SaveOverlay(doc.Content + "\n# edited\n"); err != nil {
		t.Fatal(err)
	}

	// 按条目增删：生效到计划中，保留文件中的注释；非法条目被拒绝且不落盘
	if _, err := c.EditOverlay("add", rpc.OverlayEntry{Kind: "domain", Value: "=added.example.org"}); err != nil {
		t.Fatal(err)
	}
	items, err := c.OverlayItems()
	if err != nil || !slices.ContainsFunc(items.Entries, func(e rpc.OverlayEntry) bool {
		return e.Kind == "domain" && e.Value == "=added.example.org" && e.Line > 0
	}) {
		t.Fatalf("%v %+v", err, items)
	}
	if now, _ := os.ReadFile(paths.Overlay); !strings.Contains(string(now), "# edited") {
		t.Fatalf("按条目编辑丢失了注释：\n%s", now)
	}
	if r, err := c.Why(rpc.WhyRequest{Target: "added.example.org", NoResolve: true}); err != nil || r.Report.Decision.Source.Kind != ir.SourceOverlay {
		t.Fatalf("新增条目应立即生效：%v %+v", err, r)
	}
	before, _ := os.ReadFile(paths.Overlay)
	if _, err := c.EditOverlay("add", rpc.OverlayEntry{Kind: "cidr", Value: "nope"}); err == nil {
		t.Fatal("应拒绝非法网段")
	}
	if now, _ := os.ReadFile(paths.Overlay); string(now) != string(before) {
		t.Fatal("拒绝时不应改动文件")
	}
	if _, err := c.EditOverlay("remove", rpc.OverlayEntry{Kind: "domain", Value: "=added.example.org"}); err != nil {
		t.Fatal(err)
	}

	// 一键修复：逐步报告
	rep, err := c.Repair()
	if err != nil || len(rep.Steps) == 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, s := range rep.Steps {
		if strings.Contains(s.Name, "订阅") && !s.OK {
			t.Fatalf("订阅步骤失败：%+v", s)
		}
	}
}

// 系统代理与 TUN 相互独立：可同时开启；全局型 VPN 只让 TUN 让路，系统代理不受影响；旧的 mode: 写法仍兼容。
func TestSwitchesIndependent(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	cfg := strings.Replace(config.Template, "route_include: []", "route_include: [198.51.100.0/24]", 1)
	cfg = strings.Replace(cfg, "interval: 5s", "interval: 30ms", 1)
	_ = os.WriteFile(paths.Config, []byte("mode: sysproxy\n"+cfg), 0o600) // 旧写法
	var mu sync.Mutex
	full := false
	snap := func() (netsense.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		s := netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}}}
		if full {
			s.PrimaryInterface = "utun4"
			s.Routes = append(s.Routes, netsense.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "utun4"})
		}
		return s, nil
	}
	eng := &fakeEngine{}
	sp := &fakeSysProxy{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	d.SysProxy, d.TUNOpener, d.NetSnapshot = sp, &fakeOpener{}, snap
	d.CaptiveCheck = func(context.Context, []netsense.Probe, netsense.PhysicalPath) (netsense.CaptiveState, string) {
		return netsense.Online, ""
	}
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	var st *rpc.Status
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			st = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st == nil || !st.SystemProxyOn || st.TUNOn || !sp.HasSnapshot() {
		t.Fatalf("旧写法 mode: sysproxy 应开启系统代理：%+v", st)
	}
	st, err := c.Switch(rpc.SwitchRequest{TUN: "on"})
	if err != nil || !st.SystemProxyOn || !st.TUNOn || st.Mode != "tun+sysproxy" || !sp.HasSnapshot() || eng.last.TUN == nil {
		t.Fatalf("应可同时开启：%v %+v", err, st)
	}
	mu.Lock()
	full = true
	mu.Unlock()
	for i := 0; i < 200; i++ {
		if st, _ = c.Status(); !st.TUNOn {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if st.TUNOn || !st.WantTUN || !st.SystemProxyOn || !sp.HasSnapshot() {
		t.Fatalf("全局型 VPN 只应让 TUN 让路：%+v", st)
	}
	if data, _ := os.ReadFile(paths.State); !strings.Contains(string(data), `"tun": true`) {
		t.Fatalf("开关应保存到 state.json：%s", data)
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "maybe"}); err == nil {
		t.Fatal("非法取值应报错")
	}
	if st, _ = c.Switch(rpc.SwitchRequest{SystemProxy: "config", TUN: "config"}); st.Overridden {
		t.Fatal("config 应清除界面设置")
	}
	if data, _ := os.ReadFile(paths.State); strings.Contains(string(data), `"tun"`) {
		t.Fatalf("回到 config 后 state.json 不应再有开关：%s", data)
	}
}

// 设置订阅：先拉取并解析，通过后只改 config.yaml 中对应的行；失败时不动配置。
func TestSetSubscription(t *testing.T) {
	const good = "proxies:\n  - {name: n1, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\nrules:\n  - MATCH,n1\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good":
			_, _ = io.WriteString(w, good)
		case "/html":
			_, _ = io.WriteString(w, "<html>login</html>")
		case "/empty":
			_, _ = io.WriteString(w, "proxies: []\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	orig, _ := os.ReadFile(paths.Config)

	for _, bad := range []string{"/html", "/empty", "/404", "ftp://x"} {
		u := bad
		if strings.HasPrefix(bad, "/") {
			u = srv.URL + bad
		}
		if _, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: u}); err == nil {
			t.Fatalf("%s 应失败", bad)
		} else {
			t.Logf("%s → %v", bad, err)
		}
		if now, _ := os.ReadFile(paths.Config); string(now) != string(orig) {
			t.Fatalf("%s 失败时不应改动 config.yaml", bad)
		}
	}

	st, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: srv.URL + "/good?token=secret"})
	if err != nil || st.Subscription.Nodes != 1 || strings.Contains(st.Subscription.Source, "secret") {
		t.Fatalf("%v %+v", err, st)
	}
	now, _ := os.ReadFile(paths.Config)
	a, b := strings.Split(string(orig), "\n"), strings.Split(string(now), "\n")
	changed := 0
	for i := range a {
		if i < len(b) && a[i] != b[i] {
			changed++
		}
	}
	if len(a) != len(b) || changed != 1 {
		t.Fatalf("应只改动订阅地址一行：%d 行变化", changed)
	}
	if sc, _ := c.Subscription(); sc.URL != srv.URL+"/good?token=secret" {
		t.Fatalf("%+v", sc)
	}
}

// 按应用分流的设置写入 config.yaml（去空白、去重），可读回。
func TestDaemonTUNApps(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.SetTUNApps(rpc.TUNApps{ExcludeApps: []string{" com.bank.app ", "com.bank.app", "", "com.other"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.TUNApps()
	if err != nil || strings.Join(got.ExcludeApps, ",") != "com.bank.app,com.other" || len(got.IncludeApps) != 0 {
		t.Fatalf("%v %+v", err, got)
	}
	data, _ := os.ReadFile(paths.Config)
	if !strings.Contains(string(data), "  exclude_apps: [com.bank.app, com.other]\n") || !strings.Contains(string(data), "# Android：不经 TUN 的应用") {
		t.Fatalf("config.yaml：\n%s", data)
	}
}

// dns.respect-rules：连接 DNS 服务器时按路由规则选择出站。1.1.1.1 命中走代理组的规则 → detour 为该组；
// 223.5.5.5 命中直连 → 保持直连；proxy-server-nameserver 不受影响（用于解析节点域名，避免循环）。
func TestDaemonDNSRespectRules(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	sub := filepath.Join(home, "sub.yaml")
	_ = os.WriteFile(sub, []byte(`proxies:
  - {name: a, type: socks5, server: 192.0.2.1, port: 1080}
proxy-groups:
  - {name: Proxy, type: select, proxies: [a, DIRECT]}
dns:
  respect-rules: true
  proxy-server-nameserver: [223.5.5.5]
  nameserver: [tls://1.1.1.1, 223.5.5.5]
rules:
  - IP-CIDR,1.1.1.1/32,Proxy,no-resolve
  - MATCH,DIRECT
`), 0o600)
	cfg := strings.Replace(config.Template, `  file: ""`, `  file: '`+sub+`'`, 1)
	cfg = strings.Replace(cfg, "listen: 127.0.0.1:7899", "listen: 127.0.0.1:17998", 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	eng := &fakeEngine{}
	d := daemon.New(paths, eng, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var rendered struct {
		DNS struct {
			Servers []struct {
				Tag    string `json:"tag"`
				Server string `json:"server"`
				Detour string `json:"detour"`
			} `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(eng.last.JSON, &rendered); err != nil {
		t.Fatal(err)
	}
	detour := map[string]string{}
	for _, s := range rendered.DNS.Servers {
		detour[s.Tag] = s.Detour
	}
	if detour["ns-0"] != "Proxy" || detour["ns-1"] != "" || detour["psns-0"] != "" {
		t.Fatalf("detour=%v", detour)
	}
}

// 首次加载期间本地接口已可用：状态显示正在启动，写操作被拒绝；加载完成后恢复正常。
func TestDaemonStartingState(t *testing.T) {
	eng := &fakeEngine{gate: make(chan struct{})}
	_, _, c, cleanup := setupEngine(t, eng)
	defer cleanup()
	st, err := c.Status()
	if err != nil || !st.Starting || st.Running {
		t.Fatalf("加载期间应显示正在启动：%+v %v", st, err)
	}
	if _, err := c.Reload(); err == nil || !strings.Contains(err.Error(), "正在启动") {
		t.Fatalf("加载期间应拒绝重新加载：%v", err)
	}
	close(eng.gate)
	for i := 0; i < 100; i++ {
		if st, err = c.Status(); err == nil && !st.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Starting || !st.Running {
		t.Fatalf("加载完成后应正常运行：%+v", st)
	}
}

// 订阅列表：当前订阅自动加入；添加时校验并带上流量额度；切换改写 config.yaml；正在使用的不能删除。
func TestProfiles(t *testing.T) {
	sub := func(node string) string {
		return "proxies:\n  - {name: " + node + ", type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\nrules:\n  - MATCH," + node + "\n"
	}
	used := "1024"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Header().Set("Subscription-Userinfo", "upload=0; download="+used+"; total=4096; expire=1798732800")
			w.Header().Set("Profile-Title", "Alpha")
			_, _ = io.WriteString(w, sub("a1"))
		case "/b":
			_, _ = io.WriteString(w, strings.Replace(sub("b1"), "rules:", "  - {name: b2, type: ss, server: 192.0.2.2, port: 8388, cipher: aes-128-gcm, password: p}\nrules:", 1))
		default:
			_, _ = io.WriteString(w, "<html>")
		}
	}))
	defer srv.Close()

	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 原有的单一订阅：自动加入列表，名称取订阅服务提供的 profile-title
	st, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: srv.URL + "/a?token=s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Profiles) != 1 || !st.Profiles[0].Active || st.Profiles[0].Name != "Alpha" || st.Subscription.Name != "Alpha" ||
		st.Subscription.ID != st.Profiles[0].ID || strings.Contains(st.Profiles[0].Source, "token") {
		t.Fatalf("%+v %+v", st.Subscription, st.Profiles)
	}
	if in := st.Subscription.Info; in == nil || in.Download != 1024 || in.Total != 4096 || in.Expire.Unix() != 1798732800 {
		t.Fatalf("流量额度：%+v", in)
	}
	alpha := st.Profiles[0].ID

	if _, err := c.AddProfile(rpc.AddProfileRequest{URL: srv.URL + "/bad"}); err == nil {
		t.Fatal("无法识别的订阅应拒绝")
	}
	if _, err := c.AddProfile(rpc.AddProfileRequest{URL: srv.URL + "/a?token=s"}); err == nil {
		t.Fatal("重复的订阅应拒绝")
	}
	st, err = c.AddProfile(rpc.AddProfileRequest{Name: "Beta", URL: srv.URL + "/b"})
	if err != nil || len(st.Profiles) != 2 || st.Subscription.Name != "Alpha" || st.Profiles[1].Name != "Beta" || st.Profiles[1].Active {
		t.Fatalf("添加不切换：%v %+v", err, st.Profiles)
	}
	beta := st.Profiles[1].ID

	if st, err = c.ActivateProfile(beta); err != nil || st.Subscription.ID != beta || st.Subscription.Nodes != 2 || st.Subscription.Info != nil {
		t.Fatalf("切换：%v %+v", err, st.Subscription)
	}
	if sc, _ := c.Subscription(); sc.URL != srv.URL+"/b" {
		t.Fatalf("config.yaml 应改为 Beta：%+v", sc)
	}
	if _, err := c.DeleteProfile(beta); err == nil {
		t.Fatal("正在使用的订阅不能删除")
	}

	// 未使用的订阅也能更新额度
	used = "2048"
	if st, err = c.UpdateProfile(alpha); err != nil || st.Profiles[0].Info == nil || st.Profiles[0].Info.Download != 2048 || st.Subscription.ID != beta {
		t.Fatalf("更新未使用的订阅：%v %+v", err, st.Profiles)
	}
	if st, err = c.EditProfile(alpha, rpc.EditProfileRequest{Name: "主力"}); err != nil || st.Profiles[0].Name != "主力" {
		t.Fatalf("改名：%v %+v", err, st.Profiles)
	}
	if st, err = c.DeleteProfile(alpha); err != nil || len(st.Profiles) != 1 || st.Profiles[0].ID != beta {
		t.Fatalf("删除：%v %+v", err, st.Profiles)
	}
}

// 规则集拉取很慢（如几十个托管在海外的规则集）：「更新订阅」不等它们，切换开关等操作也照常完成。
func TestUpdateDoesNotBlockReload(t *testing.T) {
	var slow atomic.Bool
	gate := make(chan struct{})
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sub":
			_, _ = io.WriteString(w, "proxies:\n  - {name: n1, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: p}\n"+
				"rule-providers:\n  r1: {type: http, behavior: classical, format: text, url: "+srvURL+"/rules, path: ./r1.txt, interval: 86400}\n"+
				"rules:\n  - RULE-SET,r1,n1\n  - MATCH,DIRECT\n")
		case "/rules":
			if slow.Load() {
				<-gate
			}
			_, _ = io.WriteString(w, "DOMAIN-SUFFIX,example.com\n")
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: srv.URL + "/sub"}); err != nil {
		t.Fatal(err)
	}

	// 「更新订阅」只等订阅本身：规则集很慢时也很快返回，规则集随后在后台重新拉取
	slow.Store(true)
	start := time.Now()
	if _, err := c.Update(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("更新订阅等了规则集 %v", took)
	}
	// 后台拉取期间，重新加载不受影响
	time.Sleep(100 * time.Millisecond)
	start = time.Now()
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("重新加载被后台更新挡住了 %v", took)
	}
	close(gate)
}
