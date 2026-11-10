package mobilebind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
)

// fakeLocal 记录本地引擎的启停与消息，按操作给回复。
type fakeLocal struct {
	fakeTunnel
	starts, stops int
	reqs          []tunnelproto.Request
}

func (f *fakeLocal) LocalStart() error { f.starts++; return nil }
func (f *fakeLocal) LocalStop()        { f.stops++ }
func (f *fakeLocal) LocalCall(req string) (string, error) {
	var r tunnelproto.Request
	_ = json.Unmarshal([]byte(req), &r)
	f.reqs = append(f.reqs, r)
	var resp tunnelproto.Response
	switch r.Op {
	case tunnelproto.OpState:
		resp.State = &tunnelproto.State{Selected: map[string]string{"Proxy": "hk"}, Delays: map[string]int{"hk": 42},
			Traffic: engine.Traffic{Up: 7}}
	case tunnelproto.OpResolve:
		resp.Addrs = []string{"10.0.0.1", "bad"}
	case tunnelproto.OpSelect:
		if r.Member == "nope" {
			resp.Error = "没有这个成员"
		}
	}
	b, _ := json.Marshal(resp)
	return string(b), nil
}

func TestVelumLocal(t *testing.T) {
	home := t.TempDir()
	h := &fakeLocal{}
	v := newVelumLocal(home, h)
	if err := v.Restart(context.Background(), engine.Config{}); err == nil {
		t.Fatal("没有 velum 配置时应当报错")
	}
	cfg := engine.Config{Velum: &engine.VelumConfig{IR: []byte(`{"schema_version":1}`), RuleSets: []byte(`[]`), Meta: []byte(`{}`)}}
	for range 2 {
		if err := v.Restart(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if h.starts != 1 || !v.Running() {
		t.Fatalf("配置没变时不应重复启动：starts=%d", h.starts)
	}
	if _, err := os.Stat(filepath.Join(tunnelproto.LocalDir(home), tunnelproto.VelumIRFile)); err != nil {
		t.Fatalf("应当写出本地配置：%v", err)
	}
	if v.Selected("Proxy") != "hk" || v.Delay("hk") != 42 || v.Traffic().Up != 7 {
		t.Fatalf("状态：%q %d %+v", v.Selected("Proxy"), v.Delay("hk"), v.Traffic())
	}
	if n := len(h.reqs); n != 1 {
		t.Fatalf("1 秒内的状态查询应当只发一次消息，实际 %d", n)
	}
	addrs, err := v.Resolve(context.Background(), "a.example")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "10.0.0.1" {
		t.Fatalf("解析：%v %v", addrs, err)
	}
	if err := v.SelectMember("Proxy", "nope"); err == nil {
		t.Fatal("引擎报错应当返回错误")
	}
	_ = v.Pause()
	if v.Running() || h.stops != 1 {
		t.Fatal("Pause 应当停掉本地引擎")
	}
	if _, err := v.Resolve(context.Background(), "a.example"); err == nil {
		t.Fatal("停止后不应再发消息")
	}
}
