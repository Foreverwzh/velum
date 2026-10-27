package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
)

// WinINet 代理类型标志（INTERNET_PER_CONN_FLAGS）。
const (
	WinProxyDirect     = 0x1 // PROXY_TYPE_DIRECT
	WinProxyManual     = 0x2 // PROXY_TYPE_PROXY
	WinProxyAutoURL    = 0x4 // PROXY_TYPE_AUTO_PROXY_URL
	WinProxyAutoDetect = 0x8 // PROXY_TYPE_AUTO_DETECT
)

// WinSettings 是 WinINet 默认连接（局域网）的代理设置。
type WinSettings struct {
	Flags         uint32 `json:"flags"`
	Server        string `json:"server"`          // "host:port" 或 "http=…;https=…"
	Bypass        string `json:"bypass"`          // 以 ; 分隔
	AutoConfigURL string `json:"auto_config_url"` // PAC 地址
}

// WinBackend 读写当前用户的 WinINet 设置。velumd 以用户身份运行时在进程内调用（WinINetBackend）；
// 以服务（SYSTEM）运行时由 UserSessionBackend 在用户会话中执行。测试中替换为模拟实现。
type WinBackend interface {
	Read() (WinSettings, error)
	Write(WinSettings) error
}

// WinManager 是 Windows 上的系统代理控制器，流程与 macOS 的 Manager 相同：接管前存快照，还原后删除，启动时自愈。
type WinManager struct {
	Backend      WinBackend
	SnapshotPath string
	// Listening / Logf 与 macOS 的 Manager 相同：还原时跳过已无程序监听的本机代理。
	Listening func(host string, port int) bool
	Logf      func(format string, args ...any)
}

func NewWindows(snapshotPath string, b WinBackend) *WinManager {
	return &WinManager{Backend: b, SnapshotPath: snapshotPath}
}

func (m *WinManager) HasSnapshot() bool {
	_, err := os.Stat(m.SnapshotPath)
	return err == nil
}

// Apply 接管系统代理：手动代理指向 Velum，接管期间关闭 PAC 与自动检测（还原时写回）。
func (m *WinManager) Apply(plan compiler.SysProxyPlan) error {
	if plan.Host == "" || plan.Port == 0 {
		return errors.New("系统代理地址无效")
	}
	if !m.HasSnapshot() {
		cur, err := m.Backend.Read()
		if err != nil {
			return fmt.Errorf("读取当前系统代理：%w", err)
		}
		snap := Snapshot{TakenAt: time.Now(), Host: plan.Host, Port: int(plan.Port), Windows: &cur}
		data, _ := json.MarshalIndent(snap, "", "  ")
		if err := os.WriteFile(m.SnapshotPath, data, 0o600); err != nil {
			return fmt.Errorf("写入快照：%w", err)
		}
	} else if err := retarget(m.SnapshotPath, plan.Host, int(plan.Port)); err != nil {
		return err
	}
	var pats []string
	for _, e := range plan.Exceptions {
		pats = append(pats, e.Pattern)
	}
	return m.Backend.Write(WinSettings{
		Flags:  WinProxyDirect | WinProxyManual,
		Server: winServer(plan.Host, int(plan.Port)),
		Bypass: strings.Join(WinBypass(pats), ";"),
	})
}

// Restore 按快照写回并删除快照；没有快照时什么都不做。
func (m *WinManager) Restore() error {
	snap, err := loadSnapshot(m.SnapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if w := snap.Windows; w != nil {
		// 接管时开着的其他代理工具已退出、或原值就是 Velum 自己：不写回这个本机代理（见 Manager.dropDeadLocal）
		if host, port := ParseWinServer(w.Server); w.Flags&WinProxyManual != 0 && isLoopbackHost(host) && port > 0 &&
			((host == snap.Host && port == snap.Port) || !listening(m.Listening, host, port)) {
			w.Flags &^= WinProxyManual
			w.Flags |= WinProxyDirect
			if m.Logf != nil {
				m.Logf("还原系统代理：原先的代理 %s 已无程序监听（原代理工具已退出），改为关闭", w.Server)
			}
		}
		if err := m.Backend.Write(*w); err != nil {
			return err
		}
	}
	return os.Remove(m.SnapshotPath)
}

// Heal 处理上次未正常退出留下的接管状态：仍指向 Velum 则还原，用户已改动则丢弃快照。
func (m *WinManager) Heal() (HealResult, error) {
	snap, err := loadSnapshot(m.SnapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return HealNothing, nil
	}
	if err != nil {
		_ = os.Remove(m.SnapshotPath)
		return HealDiscarded, err
	}
	cur, err := m.Backend.Read()
	if err != nil {
		return HealNothing, err
	}
	if host, port := ParseWinServer(cur.Server); cur.Flags&WinProxyManual != 0 && host == snap.Host && port == snap.Port {
		return HealRestored, m.Restore()
	}
	return HealDiscarded, os.Remove(m.SnapshotPath)
}

// ClearOwn 与 Manager.ClearOwn 相同：未接管时关闭仍指向 Velum 自己的手动代理。
func (m *WinManager) ClearOwn(host string, port int) (bool, error) {
	cur, err := m.Backend.Read()
	if err != nil {
		return false, err
	}
	if h, p := ParseWinServer(cur.Server); cur.Flags&WinProxyManual == 0 || h != host || p != port {
		return false, nil
	}
	cur.Flags &^= WinProxyManual
	cur.Flags |= WinProxyDirect
	if err := m.Backend.Write(cur); err != nil {
		return false, err
	}
	if m.Logf != nil {
		m.Logf("系统代理：仍指向 Velum（%s:%d）但未接管，已关闭", host, port)
	}
	return true, nil
}

// Effective 读取当前生效的 WinINet 代理（与 macOS 的 EffectiveReader 对应）。
func (m *WinManager) Effective() (Effective, error) {
	cur, err := m.Backend.Read()
	if err != nil {
		return Effective{}, err
	}
	var e Effective
	e.HTTP.Server, e.HTTP.Port = ParseWinServer(cur.Server)
	e.HTTP.Enabled = cur.Flags&WinProxyManual != 0 && e.HTTP.Server != ""
	return e, nil
}

func winServer(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// ParseWinServer 解析 WinINet 的 ProxyServer：「host:port」「[::1]:port」「http://host:port」，
// 或按协议分开的「http=host:port;https=…」（取 http）。无法解析时返回空。
func ParseWinServer(s string) (host string, port int) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "=") {
		found := false
		for _, part := range strings.Split(s, ";") {
			if k, v, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(k), "http") {
				s, found = strings.TrimSpace(v), true
				break
			}
		}
		if !found {
			return "", 0
		}
	}
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	h, p, err := net.SplitHostPort(strings.TrimSuffix(s, "/"))
	if err != nil {
		return "", 0
	}
	port, _ = strconv.Atoi(p)
	return h, port
}

// WinBypass 把豁免写法转换为 WinINet 支持的形式：
//   - 域名与通配（localhost、*.corp.example）原样保留；
//   - IPv4 网段转为通配：10.0.0.0/8 → 10.*，非整字节对齐时按下一字节展开（172.16.0.0/12 → 172.16.* … 172.31.*）；
//     也接受 macOS 的简写 169.254/16；
//   - WinINet 不支持 IPv6 网段，忽略。
func WinBypass(patterns []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			add(p)
			continue
		}
		for _, w := range cidrWildcards(p) {
			add(w)
		}
	}
	return out
}

func cidrWildcards(s string) []string {
	pf, err := netip.ParsePrefix(s)
	if err != nil { // macOS 简写：169.254/16
		addr, bits, ok := strings.Cut(s, "/")
		for ok && strings.Count(addr, ".") < 3 {
			addr += ".0"
		}
		if pf, err = netip.ParsePrefix(addr + "/" + bits); err != nil {
			return nil
		}
	}
	if !pf.Addr().Is4() {
		return nil
	}
	if pf.Bits() == 0 {
		return nil // 0.0.0.0/0 在 WinINet 中只能写成 *（所有地址都不走代理，等于关闭代理），忽略
	}
	pf = pf.Masked()
	octets := (pf.Bits() + 7) / 8    // 需要写出的整字节数
	n := 1 << (octets*8 - pf.Bits()) // 最多 128 条
	base := pf.Addr().As4()
	var out []string
	for i := 0; i < n; i++ {
		a := base
		if octets > 0 {
			a[octets-1] += byte(i)
		}
		parts := make([]string, 0, 4)
		for j := 0; j < octets; j++ {
			parts = append(parts, strconv.Itoa(int(a[j])))
		}
		if octets < 4 {
			parts = append(parts, "*")
		}
		out = append(out, strings.Join(parts, "."))
	}
	return out
}
