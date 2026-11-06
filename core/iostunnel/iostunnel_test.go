package iostunnel

import (
	"encoding/json"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

type noHost struct{}

func (noHost) OpenTun(string) (int32, error) { return -1, nil }
func (noHost) CloseTun()                     {}

const cfgJSON = `{"log":{"disabled":true},"outbounds":[
 {"type":"selector","tag":"Proxy","outbounds":["甲","乙"],"default":"甲"},
 {"type":"direct","tag":"甲"},{"type":"direct","tag":"乙"}]}`

func ask(t *testing.T, r tunnelproto.Request) tunnelproto.Response {
	t.Helper()
	b, _ := json.Marshal(r)
	var resp tunnelproto.Response
	if err := json.Unmarshal(Handle(b), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// 扩展只靠共享目录的文件工作；被系统单独拉起时要恢复用户的选择。
func TestTunnelLoadsConfigAndRestoresSelection(t *testing.T) {
	home := t.TempDir()
	if _, err := tunnelproto.Save(home, engine.Config{JSON: []byte(cfgJSON), TUN: &engine.TUNConfig{}}); err != nil {
		t.Fatal(err)
	}
	if err := tunnelproto.SaveSelected(home, map[string]string{"Proxy": "乙", "已删除的组": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, noHost{}); err != nil {
		t.Fatal(err)
	}
	defer Stop()

	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpState}); got.Error != "" || got.State.Selected["Proxy"] != "乙" {
		t.Fatalf("没有恢复选择：%+v", got)
	}
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpSelect, Group: "Proxy", Member: "甲"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpState}); got.State.Selected["Proxy"] != "甲" {
		t.Fatalf("切换没有生效：%+v", got.State)
	}
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpSelect, Group: "Proxy", Member: "不存在"}); got.Error == "" {
		t.Fatal("切换到不存在的成员应当报错")
	}
	// 重新加载读取新配置
	if _, err := tunnelproto.Save(home, engine.Config{JSON: []byte(cfgJSON), TUN: &engine.TUNConfig{}}); err != nil {
		t.Fatal(err)
	}
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpReload}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got := ask(t, tunnelproto.Request{Op: "不存在"}); got.Error == "" {
		t.Fatal("未知操作应当报错")
	}
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpConns, Limit: 10}); got.Error != "" {
		t.Fatal(got.Error)
	}
}

func TestTunnelWithoutConfigExplainsWhat(t *testing.T) {
	if err := Start(t.TempDir(), noHost{}); err == nil {
		Stop()
		t.Fatal("没有配置时应当报错")
	}
	Stop()
}

// 换网时扩展自己切换策略组：回家走直连，在外走节点；手动切换保持到下一次换网。
func TestTunnelSwitchesGroupByNetwork(t *testing.T) {
	home := t.TempDir()
	cfg := engine.Config{JSON: []byte(cfgJSON), TUN: &engine.TUNConfig{},
		Policies: map[string]*ir.NetPolicy{"Proxy": {SSID: map[string]string{"家": "乙"}, Cellular: "甲", Default: "甲"}}}
	if _, err := tunnelproto.Save(home, cfg); err != nil {
		t.Fatal(err)
	}
	netKind, netSSID = "", ""
	if err := Start(home, noHost{}); err != nil {
		t.Fatal(err)
	}
	defer Stop()
	selected := func() string { return ask(t, tunnelproto.Request{Op: tunnelproto.OpState}).State.Selected["Proxy"] }

	NetworkChanged(ir.NetWiFi, "家")
	if got := selected(); got != "乙" {
		t.Fatalf("在家应当走乙：%q", got)
	}
	NetworkChanged(ir.NetCellular, "")
	if got := selected(); got != "甲" {
		t.Fatalf("蜂窝应当走甲：%q", got)
	}
	// 手动切换保持
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpSelect, Group: "Proxy", Member: "乙"}); got.Error != "" {
		t.Fatal(got.Error)
	}
	NetworkChanged(ir.NetCellular, "")
	if got := selected(); got != "乙" {
		t.Fatalf("网络没变不应改回手动选择：%q", got)
	}
	// 下一次换网恢复按策略
	NetworkChanged(ir.NetWiFi, "别处")
	if got := selected(); got != "甲" {
		t.Fatalf("其他 Wi-Fi 应当走 default：%q", got)
	}
	// 重新加载后仍按当前网络
	NetworkChanged(ir.NetWiFi, "家")
	if got := ask(t, tunnelproto.Request{Op: tunnelproto.OpReload}); got.Error != "" {
		t.Fatal(got.Error)
	}
	if got := selected(); got != "乙" {
		t.Fatalf("重新加载后应当还按策略：%q", got)
	}
}
