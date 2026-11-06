package mobilebind

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
)

type fakeTunnel struct {
	active        bool
	starts, stops int
	calls         []tunnelproto.Request
	startErr      error
}

func (f *fakeTunnel) Network() string { return "" }
func (f *fakeTunnel) StartTunnel() error {
	f.starts++
	if f.startErr != nil {
		return f.startErr
	}
	f.active = true
	return nil
}
func (f *fakeTunnel) StopTunnel()        { f.stops++; f.active = false }
func (f *fakeTunnel) TunnelActive() bool { return f.active }
func (f *fakeTunnel) TunnelCall(req string) (string, error) {
	var r tunnelproto.Request
	_ = json.Unmarshal([]byte(req), &r)
	f.calls = append(f.calls, r)
	b, _ := json.Marshal(tunnelproto.Response{State: &tunnelproto.State{
		Selected: map[string]string{"Proxy": "节点"}, Delays: map[string]int{"节点": 42}, TUNName: "utun9"}})
	return string(b), nil
}

func (f *fakeTunnel) count(op string) int {
	n := 0
	for _, c := range f.calls {
		if c.Op == op {
			n++
		}
	}
	return n
}

const plainConfig = `{"log":{"disabled":true},"outbounds":[{"type":"direct","tag":"DIRECT"}]}`

func tunCfg(js string) engine.Config {
	return engine.Config{JSON: []byte(js), TUN: &engine.TUNConfig{Routes: []string{"0.0.0.0/1"}}}
}

func TestSplitEngineFollowsTunnel(t *testing.T) {
	home := t.TempDir()
	f := &fakeTunnel{}
	e := newSplitEngine(home, f, singbox.New(singbox.Options{}))
	ctx := context.Background()
	local := engine.Config{JSON: []byte(plainConfig), CachePath: t.TempDir() + "/c.db"}

	// 没有隧道：本地内核
	if err := e.Start(ctx, local); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if !e.local.Running() || f.starts != 0 || !e.Running() {
		t.Fatalf("应当运行本地内核：local=%v starts=%d", e.local.Running(), f.starts)
	}

	// 开启：停本地内核，配置写到共享目录，启动隧道
	if err := e.Restart(ctx, tunCfg(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if e.local.Running() || f.starts != 1 || !e.Running() {
		t.Fatalf("应当切到隧道：local=%v starts=%d", e.local.Running(), f.starts)
	}
	if got, err := tunnelproto.Load(home, nil); err != nil || string(got.JSON) != `{"a":1}` || got.TUN.Routes[0] != "0.0.0.0/1" {
		t.Fatalf("共享目录的配置不对：%+v %v", got, err)
	}

	// 配置没变：不打断连接
	if err := e.Restart(ctx, tunCfg(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || f.count(tunnelproto.OpReload) != 0 {
		t.Fatalf("配置没变不应重启：starts=%d reload=%d", f.starts, f.count(tunnelproto.OpReload))
	}

	// 配置变了：通知扩展重新加载，不重建隧道
	if err := e.Restart(ctx, tunCfg(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || f.count(tunnelproto.OpReload) != 1 {
		t.Fatalf("配置变化应当重新加载：starts=%d reload=%d", f.starts, f.count(tunnelproto.OpReload))
	}

	// 状态批量取回并缓存
	f.calls = nil
	if e.Selected("Proxy") != "节点" || e.Delay("节点") != 42 || e.TUNName() != "utun9" {
		t.Fatalf("状态不对：%q %d %q", e.Selected("Proxy"), e.Delay("节点"), e.TUNName())
	}
	if f.count(tunnelproto.OpState) != 1 {
		t.Fatalf("多次读取应当只查询一次，实际 %d 次", f.count(tunnelproto.OpState))
	}

	// 切换节点转发给扩展，并记录下来供扩展被系统单独拉起时恢复
	if err := e.SelectMember("Proxy", "别的"); err != nil {
		t.Fatal(err)
	}
	if tunnelproto.LoadSelected(home)["Proxy"] != "别的" || f.count(tunnelproto.OpSelect) != 1 {
		t.Fatalf("选择没有转发或记录：%v", tunnelproto.LoadSelected(home))
	}

	// 关闭：停隧道，回到本地内核
	if err := e.Restart(ctx, local); err != nil {
		t.Fatal(err)
	}
	if f.stops != 1 || f.active || !e.local.Running() {
		t.Fatalf("应当回到本地内核：stops=%d active=%v", f.stops, f.active)
	}
}

// App 重新打开、隧道还在：配置没变时不能打断连接；App 退出也不能停隧道。
func TestSplitEngineAttachesAndSurvivesExit(t *testing.T) {
	home := t.TempDir()
	cfg := tunCfg(`{"a":1}`)
	if _, err := tunnelproto.Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	f := &fakeTunnel{active: true}
	e := newSplitEngine(home, f, singbox.New(singbox.Options{}))
	if err := e.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if f.starts != 0 || f.count(tunnelproto.OpReload) != 0 {
		t.Fatalf("不应打断运行中的隧道：starts=%d reload=%d", f.starts, f.count(tunnelproto.OpReload))
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if f.stops != 0 || !f.active {
		t.Fatal("App 退出不应停止隧道")
	}
}

// 按需连接由系统拉起隧道，App 才启动、保存的开关还是关：不停隧道，把开关同步过去。
func TestSplitEngineAdoptsTunnelStartedBySystem(t *testing.T) {
	f := &fakeTunnel{active: true}
	e := newSplitEngine(t.TempDir(), f, singbox.New(singbox.Options{}))
	adopted := 0
	e.onAdopt = func() { adopted++ }
	if err := e.Start(context.Background(), engine.Config{JSON: []byte(plainConfig), CachePath: t.TempDir() + "/c.db"}); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if f.stops != 0 || adopted != 1 {
		t.Fatalf("应当接管：stops=%d adopted=%d", f.stops, adopted)
	}
}

func TestSplitEngineReportsStartFailure(t *testing.T) {
	f := &fakeTunnel{startErr: context.Canceled}
	e := newSplitEngine(t.TempDir(), f, singbox.New(singbox.Options{}))
	if err := e.Start(context.Background(), tunCfg(`{}`)); err == nil {
		t.Fatal("隧道启动失败应当返回错误")
	}
	if e.Running() {
		t.Fatal("隧道没起来不应显示运行中")
	}
}
