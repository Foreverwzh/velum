package singbox

import (
	"context"
	"io"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func TestSetDeviceSleeping(t *testing.T) {
	e := New(Options{})
	e.SetDeviceSleeping(true) // 内核没运行时不应出错

	cfg := engine.Config{JSON: []byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`), CachePath: t.TempDir() + "/c.db"}
	if err := e.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	m := service.FromContext[pause.Manager](e.bctx)
	if m == nil {
		t.Fatal("内核 context 里没有 pause.Manager")
	}
	e.SetDeviceSleeping(true)
	if !m.IsDevicePaused() {
		t.Error("休眠后应处于暂停状态")
	}
	e.SetDeviceSleeping(false)
	if m.IsDevicePaused() {
		t.Error("唤醒后应恢复")
	}
}

// 只有 iOS 上由 Packet Tunnel 交来 TUN 时才按网络扩展处理：否则出站网卡取路由表第一条默认路由（iOS 上常是蜂窝）。
func TestUnderNetworkExtension(t *testing.T) {
	if (&platform{cfg: &engine.TUNConfig{}}).UnderNetworkExtension() {
		t.Error("没有 TUN opener 时不应按网络扩展处理")
	}
	if got := (&platform{cfg: &engine.TUNConfig{Opener: stubOpener{}}}).UnderNetworkExtension(); got != C.IsIos {
		t.Errorf("UnderNetworkExtension=%v，期望 %v", got, C.IsIos)
	}
}

type stubOpener struct{}

func (stubOpener) OpenTUN(engine.TUNRequest) (int, string, io.Closer, error) { return -1, "", nil, nil }
