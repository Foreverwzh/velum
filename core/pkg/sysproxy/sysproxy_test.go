package sysproxy_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

// fakeNetworksetup 模拟 networksetup 的状态与输出格式。
type fakeNetworksetup struct {
	order    []string
	disabled map[string]bool
	devices  map[string]string
	state    map[string]*sysproxy.Service
	calls    []string
}

func newFake() *fakeNetworksetup {
	f := &fakeNetworksetup{order: []string{"Wi-Fi", "USB LAN", "Old VPN", "Corp VPN"}, disabled: map[string]bool{"Old VPN": true},
		devices: map[string]string{"Wi-Fi": "en0", "USB LAN": "en7", "Old VPN": "en9", "Corp VPN": ""}, state: map[string]*sysproxy.Service{}}
	f.state["Wi-Fi"] = &sysproxy.Service{Name: "Wi-Fi", Web: sysproxy.Proxy{Enabled: true, Server: "127.0.0.1", Port: 7890},
		SecureWeb: sysproxy.Proxy{Enabled: true, Server: "127.0.0.1", Port: 7890}, Bypass: []string{"10.0.0.0/8", "*.local"}}
	f.state["USB LAN"] = &sysproxy.Service{Name: "USB LAN", AutoProxyURL: "http://pac.example/proxy.pac", AutoProxyOn: true, AutoDiscovery: true,
		Socks: sysproxy.Proxy{Enabled: true, Server: "10.1.1.1", Port: 1080}}
	f.state["Old VPN"] = &sysproxy.Service{Name: "Old VPN"}
	f.state["Corp VPN"] = &sysproxy.Service{Name: "Corp VPN", Web: sysproxy.Proxy{Enabled: true, Server: "10.9.9.9", Port: 8080}}
	return f
}

func yes(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func (f *fakeNetworksetup) proxy(kind string, s *sysproxy.Service) *sysproxy.Proxy {
	switch kind {
	case "webproxy":
		return &s.Web
	case "securewebproxy":
		return &s.SecureWeb
	}
	return &s.Socks
}

func (f *fakeNetworksetup) Run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	cmd := args[0]
	if cmd == "-listnetworkserviceorder" {
		out := "An asterisk (*) denotes that a network service is disabled.\n"
		for i, n := range f.order {
			idx := fmt.Sprint(i + 1)
			if f.disabled[n] {
				idx = "*"
			}
			out += fmt.Sprintf("(%s) %s\n(Hardware Port: %s, Device: %s)\n\n", idx, n, n, f.devices[n])
		}
		return out, nil
	}
	if cmd == "-listallnetworkservices" {
		out := "An asterisk (*) denotes that a network service is disabled.\n"
		for _, n := range f.order {
			if f.disabled[n] {
				out += "*"
			}
			out += n + "\n"
		}
		return out, nil
	}
	s := f.state[args[1]]
	if s == nil {
		return "", fmt.Errorf("** Error: unknown service %s", args[1])
	}
	switch {
	case cmd == "-getproxybypassdomains":
		if len(s.Bypass) == 0 {
			return "There aren't any bypass domains set on " + s.Name + ".\n", nil
		}
		return strings.Join(s.Bypass, "\n") + "\n", nil
	case cmd == "-setproxybypassdomains":
		if len(args) == 3 && args[2] == "Empty" {
			s.Bypass = nil
		} else {
			s.Bypass = append([]string(nil), args[2:]...)
		}
	case cmd == "-getautoproxyurl":
		u := s.AutoProxyURL
		if u == "" {
			u = "(null)"
		}
		return "URL: " + u + "\nEnabled: " + yes(s.AutoProxyOn) + "\n", nil
	case cmd == "-setautoproxyurl":
		s.AutoProxyURL, s.AutoProxyOn = args[2], true
	case cmd == "-setautoproxystate":
		s.AutoProxyOn = args[2] == "on"
	case cmd == "-getproxyautodiscovery":
		v := "Off"
		if s.AutoDiscovery {
			v = "On"
		}
		return "Auto Proxy Discovery: " + v + "\n", nil
	case cmd == "-setproxyautodiscovery":
		s.AutoDiscovery = args[2] == "on"
	case strings.HasPrefix(cmd, "-get"):
		p := f.proxy(strings.TrimPrefix(cmd, "-get"), s)
		return fmt.Sprintf("Enabled: %s\nServer: %s\nPort: %d\nAuthenticated Proxy Enabled: 0\n", yes(p.Enabled), p.Server, p.Port), nil
	case strings.HasSuffix(cmd, "state"):
		f.proxy(strings.TrimSuffix(strings.TrimPrefix(cmd, "-set"), "state"), s).Enabled = args[2] == "on"
	case strings.HasPrefix(cmd, "-set"):
		p := f.proxy(strings.TrimPrefix(cmd, "-set"), s)
		port, _ := strconv.Atoi(args[3])
		p.Server, p.Port, p.Enabled = args[2], port, true
	default:
		return "", fmt.Errorf("unexpected %v", args)
	}
	return "", nil
}

// clone 返回功能等价的状态：未启用的代理忽略地址字段（networksetup 无法清空地址，残留不生效）。
func (f *fakeNetworksetup) clone() map[string]sysproxy.Service {
	out := map[string]sysproxy.Service{}
	norm := func(p sysproxy.Proxy) sysproxy.Proxy {
		if !p.Enabled {
			return sysproxy.Proxy{}
		}
		return p
	}
	for k, v := range f.state {
		c := *v
		c.Bypass = append([]string(nil), v.Bypass...)
		c.Web, c.SecureWeb, c.Socks = norm(c.Web), norm(c.SecureWeb), norm(c.Socks)
		out[k] = c
	}
	return out
}

func plan() compiler.SysProxyPlan {
	return compiler.SysProxyPlan{Enabled: true, Host: "127.0.0.1", Port: 7899,
		Exceptions: []compiler.Exception{{Pattern: "localhost"}, {Pattern: "*.corp.example.com"}}}
}

func newManager(t *testing.T, f *fakeNetworksetup) *sysproxy.Manager {
	m := sysproxy.New(filepath.Join(t.TempDir(), "snapshot.json"))
	m.Runner = f
	m.Listening = func(string, int) bool { return true } // 不探测本机真实端口（测试机可能开着其他代理工具）
	return m
}

// 接管时开着的其他代理工具（127.0.0.1:7890）已退出：还原时不再写回这个失效的本机代理，改为关闭；
// 指向其他主机的代理、PAC 等照常还原。
func TestRestoreDropsDeadLocalProxy(t *testing.T) {
	f := newFake()
	orig := f.clone()
	m := newManager(t, f)
	var logs []string
	m.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	if err := m.Apply(plan()); err != nil {
		t.Fatal(err)
	}
	m.Listening = func(host string, port int) bool { return !(host == "127.0.0.1" && port == 7890) }
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	got := f.clone()
	wifi := got["Wi-Fi"]
	if wifi.Web.Enabled || wifi.SecureWeb.Enabled {
		t.Fatalf("失效的本机代理不应还原：%+v", wifi)
	}
	if strings.Join(wifi.Bypass, ",") != strings.Join(orig["Wi-Fi"].Bypass, ",") {
		t.Fatalf("豁免列表应照常还原：%v", wifi.Bypass)
	}
	if !reflect.DeepEqual(got["USB LAN"], orig["USB LAN"]) || !reflect.DeepEqual(got["Corp VPN"], orig["Corp VPN"]) {
		t.Fatal("其他服务（远程代理、PAC）应照常还原")
	}
	if len(logs) != 2 {
		t.Fatalf("应记录两条调整（HTTP 与 HTTPS），实际：%v", logs)
	}
}

func TestApplyAndRestoreRoundTrip(t *testing.T) {
	f := newFake()
	orig := f.clone()
	m := newManager(t, f)
	if err := m.Apply(plan()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Wi-Fi", "USB LAN"} {
		s := f.state[name]
		if !s.Web.Enabled || s.Web.Port != 7899 || !s.SecureWeb.Enabled || s.SecureWeb.Port != 7899 || !s.Socks.Enabled || s.Socks.Port != 7899 {
			t.Errorf("%s 未接管：%+v", name, s)
		}
		if !reflect.DeepEqual(s.Bypass, []string{"localhost", "*.corp.example.com"}) {
			t.Errorf("%s 豁免应完全来自计划：%v", name, s.Bypass)
		}
		if s.AutoProxyOn || s.AutoDiscovery {
			t.Errorf("%s 接管期间应关闭 PAC / 自动发现", name)
		}
	}
	cur := f.clone()
	if !reflect.DeepEqual(cur["Old VPN"], orig["Old VPN"]) {
		t.Error("已禁用的服务不应被修改")
	}
	if !reflect.DeepEqual(cur["Corp VPN"], orig["Corp VPN"]) {
		t.Error("VPN 类服务（无设备名）不应被修改")
	}
	// 再次 Apply（如 reload）不覆盖最初的快照
	p2 := plan()
	p2.Exceptions = nil
	if err := m.Apply(p2); err != nil {
		t.Fatal(err)
	}
	if f.state["Wi-Fi"].Bypass != nil {
		t.Errorf("无豁免时应清空：%v", f.state["Wi-Fi"].Bypass)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := f.clone(); !reflect.DeepEqual(got, orig) {
		t.Fatalf("还原不完整：\n got  %+v\n want %+v", got, orig)
	}
	if m.HasSnapshot() {
		t.Fatal("还原后应删除快照")
	}
	if err := m.Restore(); err != nil {
		t.Fatal("无快照时 Restore 应为空操作")
	}
}

func TestHealAfterCrash(t *testing.T) {
	f := newFake()
	orig := f.clone()
	m := newManager(t, f)
	_ = m.Apply(plan())
	// 模拟 velumd 被 kill -9：快照仍在，系统代理仍指向 Velum
	res, err := m.Heal()
	if err != nil || res != sysproxy.HealRestored {
		t.Fatalf("heal: %v %v", res, err)
	}
	if !reflect.DeepEqual(f.clone(), orig) {
		t.Fatal("自愈应还原到接管前")
	}
}

// 接管期间监听端口变化：快照中的 Velum 地址随之更新，崩溃后自愈仍能识别并还原。
func TestHealAfterPortChange(t *testing.T) {
	f := newFake()
	orig := f.clone()
	m := newManager(t, f)
	_ = m.Apply(plan())
	p2 := plan()
	p2.Port = 7900
	if err := m.Apply(p2); err != nil {
		t.Fatal(err)
	}
	if res, err := m.Heal(); err != nil || res != sysproxy.HealRestored || !reflect.DeepEqual(f.clone(), orig) {
		t.Fatalf("heal: %v %v", res, err)
	}
}

func TestHealDoesNotClobberUserChanges(t *testing.T) {
	f := newFake()
	m := newManager(t, f)
	_ = m.Apply(plan())
	// 用户在 Velum 崩溃后手动改了代理
	for _, s := range f.state {
		s.Web = sysproxy.Proxy{Enabled: true, Server: "192.0.2.1", Port: 3128}
	}
	userState := f.clone()
	res, err := m.Heal()
	if err != nil || res != sysproxy.HealDiscarded {
		t.Fatalf("heal: %v %v", res, err)
	}
	if !reflect.DeepEqual(f.clone(), userState) || m.HasSnapshot() {
		t.Fatal("用户已改动时应只丢弃快照，不覆盖设置")
	}
}

func TestHealNothing(t *testing.T) {
	m := newManager(t, newFake())
	if res, err := m.Heal(); err != nil || res != sysproxy.HealNothing {
		t.Fatalf("%v %v", res, err)
	}
}

func TestParseScutilProxy(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : localhost
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  SOCKSEnable : 0
}`
	e := sysproxy.Effective{HTTP: sysproxy.ParseScutilProxyForTest(out)}
	if !e.HTTP.Enabled || e.HTTP.Port != 7890 || e.HTTP.Server != "127.0.0.1" {
		t.Fatalf("%+v", e.HTTP)
	}
	if !e.Overridden("127.0.0.1", 7899) || e.Overridden("127.0.0.1", 7890) {
		t.Fatal("Overridden 判断错误")
	}
}
