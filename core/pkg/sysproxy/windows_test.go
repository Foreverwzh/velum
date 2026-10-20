package sysproxy_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

type fakeWin struct {
	cur    sysproxy.WinSettings
	writes int
	err    error
}

func (f *fakeWin) Read() (sysproxy.WinSettings, error) { return f.cur, f.err }
func (f *fakeWin) Write(s sysproxy.WinSettings) error {
	if f.err != nil {
		return f.err
	}
	f.cur = s
	f.writes++
	return nil
}

var winPlan = compiler.SysProxyPlan{Enabled: true, Host: "127.0.0.1", Port: 7899,
	Exceptions: []compiler.Exception{{Pattern: "localhost"}, {Pattern: "*.corp.example"}, {Pattern: "10.0.0.0/8"}}}

func TestWinApplyRestore(t *testing.T) {
	orig := sysproxy.WinSettings{Flags: sysproxy.WinProxyDirect | sysproxy.WinProxyAutoURL | sysproxy.WinProxyAutoDetect,
		AutoConfigURL: "http://wpad/proxy.pac", Server: "corp:8080", Bypass: "<local>"}
	b := &fakeWin{cur: orig}
	m := sysproxy.NewWindows(filepath.Join(t.TempDir(), "snap.json"), b)
	if err := m.Apply(winPlan); err != nil {
		t.Fatal(err)
	}
	want := sysproxy.WinSettings{Flags: sysproxy.WinProxyDirect | sysproxy.WinProxyManual, Server: "127.0.0.1:7899", Bypass: "localhost;*.corp.example;10.*"}
	if b.cur != want {
		t.Fatalf("接管后：%+v", b.cur)
	}
	if !m.HasSnapshot() {
		t.Fatal("应有快照")
	}
	// 再次 Apply（豁免变化）不覆盖最初的快照
	if err := m.Apply(winPlan); err != nil {
		t.Fatal(err)
	}
	if e, _ := m.Effective(); !e.HTTP.Enabled || e.HTTP.Server != "127.0.0.1" || e.HTTP.Port != 7899 || e.Overridden("127.0.0.1", 7899) {
		t.Fatalf("effective: %+v", e)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if b.cur != orig || m.HasSnapshot() {
		t.Fatalf("还原后：%+v snapshot=%v", b.cur, m.HasSnapshot())
	}
	if err := m.Restore(); err != nil { // 无快照时无操作
		t.Fatal(err)
	}
}

func TestWinHeal(t *testing.T) {
	orig := sysproxy.WinSettings{Flags: sysproxy.WinProxyDirect}
	b := &fakeWin{cur: orig}
	path := filepath.Join(t.TempDir(), "snap.json")
	m := sysproxy.NewWindows(path, b)
	if r, err := m.Heal(); r != sysproxy.HealNothing || err != nil {
		t.Fatal(r, err)
	}
	// 崩溃后仍指向 Velum：还原
	_ = m.Apply(winPlan)
	if r, err := m.Heal(); r != sysproxy.HealRestored || err != nil || b.cur != orig {
		t.Fatal(r, err, b.cur)
	}
	// 用户已改为其他代理：丢弃快照，不覆盖
	_ = m.Apply(winPlan)
	b.cur = sysproxy.WinSettings{Flags: sysproxy.WinProxyManual | sysproxy.WinProxyDirect, Server: "other:1080"}
	if r, err := m.Heal(); r != sysproxy.HealDiscarded || err != nil || b.cur.Server != "other:1080" || m.HasSnapshot() {
		t.Fatal(r, err, b.cur)
	}
}

func TestWinApplyReadFailureKeepsNothing(t *testing.T) {
	b := &fakeWin{err: errors.New("没有已登录的用户")}
	m := sysproxy.NewWindows(filepath.Join(t.TempDir(), "snap.json"), b)
	if err := m.Apply(winPlan); err == nil || m.HasSnapshot() {
		t.Fatal("读取失败时不应写快照", err)
	}
}

func TestWinBypass(t *testing.T) {
	got := sysproxy.WinBypass([]string{"localhost", "*.local", "169.254/16", "192.168.0.0/16", "172.16.0.0/12",
		"10.1.2.3/32", "10.1.2.0/24", "fd00::/8", "2.0.0.0/7", "0.0.0.0/0", "localhost", " "})
	want := []string{"localhost", "*.local", "169.254.*", "192.168.*"}
	for i := 16; i <= 31; i++ {
		want = append(want, "172."+itoa(i)+".*")
	}
	want = append(want, "10.1.2.3", "10.1.2.*", "2.*", "3.*") // fd00::/8、0.0.0.0/0 忽略
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %v\nwant %v", got, want)
	}
}

func itoa(i int) string { return string(rune('0'+i/10)) + string(rune('0'+i%10)) }

// 接管期间监听地址变化：快照中的 Velum 地址随之更新，崩溃后自愈仍能识别并还原。
func TestWinRetargetThenHeal(t *testing.T) {
	orig := sysproxy.WinSettings{Flags: sysproxy.WinProxyDirect}
	b := &fakeWin{cur: orig}
	m := sysproxy.NewWindows(filepath.Join(t.TempDir(), "snap.json"), b)
	_ = m.Apply(winPlan)
	moved := winPlan
	moved.Port = 7900
	if err := m.Apply(moved); err != nil {
		t.Fatal(err)
	}
	if b.cur.Server != "127.0.0.1:7900" {
		t.Fatalf("%+v", b.cur)
	}
	if r, err := m.Heal(); r != sysproxy.HealRestored || err != nil || b.cur != orig {
		t.Fatal(r, err, b.cur)
	}
}

func TestParseWinServer(t *testing.T) {
	for _, c := range []struct {
		in   string
		host string
		port int
	}{
		{"127.0.0.1:7899", "127.0.0.1", 7899},
		{"[::1]:7899", "::1", 7899},
		{"http://127.0.0.1:7899/", "127.0.0.1", 7899},
		{"https=10.0.0.1:443;http=10.0.0.1:8080", "10.0.0.1", 8080},
		{"socks=10.0.0.1:1080", "", 0},
		{"garbage", "", 0},
	} {
		if h, p := sysproxy.ParseWinServer(c.in); h != c.host || p != c.port {
			t.Errorf("%q → %q %d", c.in, h, p)
		}
	}
	// IPv6 监听地址写入时加方括号，读回一致
	b := &fakeWin{}
	m := sysproxy.NewWindows(filepath.Join(t.TempDir(), "snap.json"), b)
	_ = m.Apply(compiler.SysProxyPlan{Host: "::1", Port: 7899})
	if e, _ := m.Effective(); b.cur.Server != "[::1]:7899" || e.Overridden("::1", 7899) {
		t.Fatalf("%q %+v", b.cur.Server, e)
	}
}

// 接管时开着的本机代理工具已退出：还原时关闭手动代理，保留其余设置（豁免、PAC）。
func TestWinRestoreDropsDeadLocalProxy(t *testing.T) {
	orig := sysproxy.WinSettings{Flags: sysproxy.WinProxyDirect | sysproxy.WinProxyManual, Server: "127.0.0.1:7890", Bypass: "localhost;<local>"}
	b := &fakeWin{cur: orig}
	m := sysproxy.NewWindows(filepath.Join(t.TempDir(), "snap.json"), b)
	m.Listening = func(string, int) bool { return false }
	if err := m.Apply(winPlan); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	want := orig
	want.Flags = sysproxy.WinProxyDirect
	if b.cur != want {
		t.Fatalf("还原后：%+v", b.cur)
	}
	// 仍在监听时照常还原
	b.cur = orig
	m.Listening = func(string, int) bool { return true }
	_ = m.Apply(winPlan)
	if err := m.Restore(); err != nil || b.cur != orig {
		t.Fatalf("还原后：%+v %v", b.cur, err)
	}
}
