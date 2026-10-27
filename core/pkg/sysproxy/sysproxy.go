// Package sysproxy 接管与还原系统代理：macOS 经 networksetup（Manager），Windows 经 WinINet（WinManager，见 windows.go）。
//
// 流程：Apply 前先把当前设置写入快照文件；Restore 按快照写回并删除快照；
// Heal 在启动时处理上次崩溃留下的残局：若系统代理仍指向 Velum，则按快照还原。
package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
)

// Runner 执行 networksetup（测试中替换为模拟实现）。
type Runner interface {
	Run(args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(args ...string) (string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
	s := string(out)
	if err != nil {
		return s, fmt.Errorf("networksetup %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(s))
	}
	// networksetup 在部分错误下仍返回 0，需检查输出
	if strings.Contains(s, "requires admin") || strings.Contains(s, "Error") {
		return s, fmt.Errorf("networksetup %s: %s", strings.Join(args, " "), strings.TrimSpace(s))
	}
	return s, nil
}

// DefaultRunner 使用系统 networksetup。
var DefaultRunner Runner = execRunner{}

type Proxy struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	Port    int    `json:"port"`
}

type Service struct {
	Name          string   `json:"name"`
	Web           Proxy    `json:"web"`
	SecureWeb     Proxy    `json:"secure_web"`
	Socks         Proxy    `json:"socks"`
	Bypass        []string `json:"bypass"`
	AutoProxyURL  string   `json:"auto_proxy_url"`
	AutoProxyOn   bool     `json:"auto_proxy_on"`
	AutoDiscovery bool     `json:"auto_discovery"`
}

type Snapshot struct {
	TakenAt  time.Time `json:"taken_at"`
	Host     string    `json:"host"` // Velum 接管时写入的地址，用于判断「仍是 Velum 的设置」
	Port     int       `json:"port"`
	Services []Service `json:"services,omitempty"`
	// Windows 是 Windows 上接管前的 WinINet 设置。
	Windows *WinSettings `json:"windows,omitempty"`
}

type Manager struct {
	Runner       Runner
	SnapshotPath string
	// Only 非空时只操作这些服务（诊断 / 真机测试用，限定影响范围）。
	Only []string
	// Listening 判断本机端口是否有程序监听（测试中替换）；为 nil 时用 TCP 连接探测。
	Listening func(host string, port int) bool
	// Logf 记录还原时的调整（为 nil 时不记录）。
	Logf func(format string, args ...any)
}

func New(snapshotPath string) *Manager {
	return &Manager{Runner: DefaultRunner, SnapshotPath: snapshotPath}
}

// Services 返回要接管的网络服务：已启用、且对应硬件网卡（Device 非空）的服务。
// VPN / NetworkExtension 类服务（如企业 VPN、其他代理工具）没有设备名，不写入代理设置，避免干扰它们。
func (m *Manager) Services() ([]string, error) {
	out, err := m.Runner.Run("-listnetworkserviceorder")
	if err != nil {
		return nil, err
	}
	var svcs []string
	name := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "(Hardware Port:"):
			_, dev, _ := strings.Cut(line, "Device:")
			dev = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(dev), ")"))
			if name != "" && dev != "" && m.allowed(name) {
				svcs = append(svcs, name)
			}
			name = ""
		case strings.HasPrefix(line, "(") && strings.Contains(line, ") "):
			// "(1) Wi-Fi"；已禁用的服务形如 "(*) Wi-Fi"
			idx, n, _ := strings.Cut(line, ") ")
			if strings.Contains(idx, "*") {
				name = ""
			} else {
				name = strings.TrimSpace(n)
			}
		}
	}
	return svcs, nil
}

func (m *Manager) allowed(name string) bool {
	if len(m.Only) == 0 {
		return true
	}
	for _, o := range m.Only {
		if o == name {
			return true
		}
	}
	return false
}

func parseKV(out string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv
}

func (m *Manager) getProxy(kind, svc string) (Proxy, error) {
	out, err := m.Runner.Run("-get"+kind, svc)
	if err != nil {
		return Proxy{}, err
	}
	kv := parseKV(out)
	port, _ := strconv.Atoi(kv["Port"])
	return Proxy{Enabled: kv["Enabled"] == "Yes", Server: kv["Server"], Port: port}, nil
}

func (m *Manager) readService(svc string) (Service, error) {
	s := Service{Name: svc}
	var err error
	if s.Web, err = m.getProxy("webproxy", svc); err != nil {
		return s, err
	}
	if s.SecureWeb, err = m.getProxy("securewebproxy", svc); err != nil {
		return s, err
	}
	if s.Socks, err = m.getProxy("socksfirewallproxy", svc); err != nil {
		return s, err
	}
	out, err := m.Runner.Run("-getproxybypassdomains", svc)
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "There aren't any") {
			s.Bypass = append(s.Bypass, line)
		}
	}
	out, err = m.Runner.Run("-getautoproxyurl", svc)
	if err != nil {
		return s, err
	}
	kv := parseKV(out)
	if u := kv["URL"]; u != "(null)" {
		s.AutoProxyURL = u
	}
	s.AutoProxyOn = kv["Enabled"] == "Yes"
	out, err = m.Runner.Run("-getproxyautodiscovery", svc)
	if err != nil {
		return s, err
	}
	s.AutoDiscovery = strings.Contains(out, ": On")
	return s, nil
}

// Read 读取所有已启用服务的当前代理设置。
func (m *Manager) Read() ([]Service, error) {
	svcs, err := m.Services()
	if err != nil {
		return nil, err
	}
	var out []Service
	for _, s := range svcs {
		st, err := m.readService(s)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func (m *Manager) loadSnapshot() (*Snapshot, error) { return loadSnapshot(m.SnapshotPath) }

// retarget：已接管期间 Velum 的监听地址变化时，先更新快照中的地址再写入新设置。
// 自愈靠这个地址识别「仍是 Velum 的设置」，不更新的话崩溃后会把 Velum 的设置当成用户改动，丢弃快照、留下失效的代理。
func retarget(path, host string, port int) error {
	snap, err := loadSnapshot(path)
	if err != nil {
		return err
	}
	if snap.Host == host && snap.Port == port {
		return nil
	}
	snap.Host, snap.Port = host, port
	data, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("更新快照：%w", err)
	}
	return nil
}

func loadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("快照损坏：%w", err)
	}
	return &s, nil
}

// HasSnapshot 报告是否存在未还原的快照（即 Velum 处于接管状态，或上次未正常退出）。
func (m *Manager) HasSnapshot() bool {
	_, err := os.Stat(m.SnapshotPath)
	return err == nil
}

// Apply 接管系统代理。首次接管时先保存快照；已接管时只更新设置（快照保持最初的状态）。
func (m *Manager) Apply(plan compiler.SysProxyPlan) error {
	if plan.Host == "" || plan.Port == 0 {
		return errors.New("系统代理地址无效")
	}
	if !m.HasSnapshot() {
		cur, err := m.Read()
		if err != nil {
			return fmt.Errorf("读取当前系统代理：%w", err)
		}
		snap := Snapshot{TakenAt: time.Now(), Host: plan.Host, Port: int(plan.Port), Services: cur}
		data, _ := json.MarshalIndent(snap, "", "  ")
		if err := os.WriteFile(m.SnapshotPath, data, 0o600); err != nil {
			return fmt.Errorf("写入快照：%w", err)
		}
	} else if err := retarget(m.SnapshotPath, plan.Host, int(plan.Port)); err != nil {
		return err
	}
	svcs, err := m.Services()
	if err != nil {
		return err
	}
	port := strconv.Itoa(int(plan.Port))
	var bypass []string
	for _, e := range plan.Exceptions {
		bypass = append(bypass, e.Pattern)
	}
	if len(bypass) == 0 {
		bypass = []string{"Empty"} // networksetup 用 Empty 清空豁免列表
	}
	for _, s := range svcs {
		cmds := [][]string{
			{"-setwebproxy", s, plan.Host, port},
			{"-setsecurewebproxy", s, plan.Host, port},
			// mixed 入站同时支持 SOCKS5：所有代理类型都指向 Velum，避免部分应用仍流向原代理
			{"-setsocksfirewallproxy", s, plan.Host, port},
			{"-setproxybypassdomains", s},
			// PAC / 自动发现优先于手动代理，接管期间关闭，还原时写回
			{"-setautoproxystate", s, "off"},
			{"-setproxyautodiscovery", s, "off"},
		}
		cmds[3] = append(cmds[3], bypass...)
		for _, c := range cmds {
			if _, err := m.Runner.Run(c...); err != nil {
				return err
			}
		}
	}
	return nil
}

// Restore 按快照写回并删除快照；没有快照时什么都不做。
// 限制：networksetup 无法清空代理地址字段。原本未设置过的代理，还原后为「关闭 + 残留 Velum 地址」，功能上等价。
func (m *Manager) Restore() error {
	snap, err := m.loadSnapshot()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, s := range snap.Services {
		m.dropDeadLocal(&s, snap.Host, snap.Port)
		if err := m.writeService(s); err != nil {
			return err
		}
	}
	return os.Remove(m.SnapshotPath)
}

// ClearOwn 关闭仍指向 Velum 自己（host:port）的代理，用于没有快照的情况：
// 快照已被还原或丢弃，设置却又被写成 Velum 的地址（多为其他代理工具把它当作原值记下、退出时写回），
// 不接管时没有快照可还原，Velum 退出后这些设置就指向已关闭的端口。返回是否做了修改。
func (m *Manager) ClearOwn(host string, port int) (bool, error) {
	cur, err := m.Read()
	if err != nil {
		return false, err
	}
	changed := false
	for _, s := range cur {
		own := false
		for _, p := range []*Proxy{&s.Web, &s.SecureWeb, &s.Socks} {
			if p.Enabled && p.Server == host && p.Port == port {
				p.Enabled, own = false, true
			}
		}
		if !own {
			continue
		}
		if err := m.writeService(s); err != nil {
			return changed, err
		}
		changed = true
		if m.Logf != nil {
			m.Logf("系统代理：%s 仍指向 Velum（%s:%d）但未接管，已关闭", s.Name, host, port)
		}
	}
	return changed, nil
}

// dropDeadLocal：快照里启用的代理若指向本机端口、而该端口已无程序监听（接管时开着的其他代理工具已退出），
// 还原它只会让网络不可用，因此改为关闭。指向其他主机的代理照常还原。
// 指向 Velum 自己（host:port）的代理同样关闭：那是上次没还原干净、或被其他代理工具当作原值记下又写回的设置，
// 还原时 Velum 尚未停止监听，探测不出它即将失效。
func (m *Manager) dropDeadLocal(s *Service, host string, port int) {
	for _, p := range []*Proxy{&s.Web, &s.SecureWeb, &s.Socks} {
		if !p.Enabled || !isLoopbackHost(p.Server) || p.Port <= 0 {
			continue
		}
		own := p.Server == host && p.Port == port
		if !own && m.listening(p.Server, p.Port) {
			continue
		}
		p.Enabled = false
		if m.Logf == nil {
			continue
		}
		if own {
			m.Logf("还原系统代理：%s 原先的代理 %s:%d 就是 Velum 自己（残留的接管设置），改为关闭", s.Name, p.Server, p.Port)
		} else {
			m.Logf("还原系统代理：%s 原先的代理 %s:%d 已无程序监听（原代理工具已退出），改为关闭", s.Name, p.Server, p.Port)
		}
	}
}

func (m *Manager) listening(host string, port int) bool { return listening(m.Listening, host, port) }

func listening(override func(string, int) bool, host string, port int) bool {
	if override != nil {
		return override(host, port)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(h, "[]"))
	return err == nil && a.IsLoopback()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (m *Manager) writeService(s Service) error {
	var cmds [][]string
	for _, p := range []struct {
		kind string
		px   Proxy
	}{{"webproxy", s.Web}, {"securewebproxy", s.SecureWeb}, {"socksfirewallproxy", s.Socks}} {
		if p.px.Server != "" {
			cmds = append(cmds, []string{"-set" + p.kind, s.Name, p.px.Server, strconv.Itoa(p.px.Port)})
		}
		cmds = append(cmds, []string{"-set" + p.kind + "state", s.Name, onOff(p.px.Enabled)})
	}
	bypass := s.Bypass
	if len(bypass) == 0 {
		bypass = []string{"Empty"}
	}
	cmds = append(cmds, append([]string{"-setproxybypassdomains", s.Name}, bypass...))
	if s.AutoProxyURL != "" {
		cmds = append(cmds, []string{"-setautoproxyurl", s.Name, s.AutoProxyURL})
	}
	cmds = append(cmds, []string{"-setautoproxystate", s.Name, onOff(s.AutoProxyOn)}, []string{"-setproxyautodiscovery", s.Name, onOff(s.AutoDiscovery)})
	for _, c := range cmds {
		if _, err := m.Runner.Run(c...); err != nil {
			return err
		}
	}
	return nil
}

// Effective 是系统当前实际生效的代理（scutil --proxy，即主网络服务的设置）。
type Effective struct {
	HTTP             Proxy  `json:"http"`
	PrimaryInterface string `json:"primary_interface"`
}

// EffectiveReader 读取实际生效的代理（测试中替换）。
var EffectiveReader = func() (Effective, error) {
	var e Effective
	out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return e, err
	}
	e.HTTP = parseScutilProxy(string(out))
	if out, err := exec.Command("/bin/sh", "-c", "printf 'show State:/Network/Global/IPv4\\n' | /usr/sbin/scutil").Output(); err == nil {
		e.PrimaryInterface = parseKV(string(out))["PrimaryInterface"]
	}
	return e, nil
}

func parseScutilProxy(out string) Proxy {
	kv := parseKV(out)
	port, _ := strconv.Atoi(kv["HTTPPort"])
	return Proxy{Enabled: kv["HTTPEnable"] == "1", Server: kv["HTTPProxy"], Port: port}
}

// Overridden 报告写入的设置是否被主网络服务（如其他 VPN / 代理工具的隧道）覆盖而未生效。
func (e Effective) Overridden(host string, port int) bool {
	return !(e.HTTP.Enabled && e.HTTP.Server == host && e.HTTP.Port == port)
}

// HealResult 说明启动自愈做了什么。
type HealResult string

const (
	HealNothing   HealResult = ""          // 无快照
	HealRestored  HealResult = "restored"  // 系统代理仍指向 Velum：已按快照还原
	HealDiscarded HealResult = "discarded" // 用户已改动系统代理：丢弃快照，不覆盖用户设置
)

// Heal 处理上次未正常退出留下的接管状态。
func (m *Manager) Heal() (HealResult, error) {
	snap, err := m.loadSnapshot()
	if errors.Is(err, os.ErrNotExist) {
		return HealNothing, nil
	}
	if err != nil {
		_ = os.Remove(m.SnapshotPath)
		return HealDiscarded, err
	}
	svcs, err := m.Services()
	if err != nil {
		return HealNothing, err
	}
	for _, s := range svcs { // 找到第一个仍指向 Velum 的服务即可判定
		web, err := m.getProxy("webproxy", s)
		if err != nil {
			return HealNothing, err
		}
		if web.Enabled && web.Server == snap.Host && web.Port == snap.Port {
			return HealRestored, m.Restore()
		}
	}
	return HealDiscarded, os.Remove(m.SnapshotPath)
}
