package mobilebind

import (
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

type fakeHost struct{}

func (fakeHost) OpenTun(string) (int32, error) { return -1, nil }
func (fakeHost) CloseTun()                     {}
func (fakeHost) Network() string               { return `{"iface":"wlan0","gateway":"10.0.2.2","dns":["10.0.2.3"]}` }
func (fakeHost) FindOwner(proto int32, src string, sport int32, dst string, dport int32) (string, error) {
	if proto == 6 && src == "172.19.0.1" && sport == 40000 && dst == "198.18.0.1" && dport == 443 {
		return `{"uid":10100,"packages":["com.example.app"]}`, nil
	}
	return "", nil
}

func TestStartStop(t *testing.T) {
	if !fullEngine {
		t.Skip("velumonly 构建不支持 Start")
	}
	if runtime.GOOS == "windows" {
		t.Skip("mobilebind 只用于移动端；Windows 上本地接口为 Named Pipe")
	}
	home, err := os.MkdirTemp("/tmp", "velum-mb-") // UDS 路径长度有限，不用 t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	if err := Start(home, fakeHost{}); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, fakeHost{}); err != nil { // 重复启动直接返回
		t.Fatal(err)
	}
	st, err := rpc.NewClient(Socket(home)).Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Home != home {
		t.Fatalf("home=%s", st.Home)
	}
	Stop()
	if _, err := os.Stat(LogPath(home)); err != nil {
		t.Fatalf("日志文件：%v", err)
	}
	if _, err := rpc.NewClient(Socket(home)).Status(); err == nil {
		t.Fatal("Stop 后本地接口仍可访问")
	}
}

// macOS 系统扩展：本地接口为回环 TCP + 令牌，令牌错误的客户端连不上。
func TestStartTCP(t *testing.T) {
	if !fullEngine {
		t.Skip("velumonly 构建不支持 Start")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hp := ln.Addr().String()
	ln.Close()
	addr := ipc.TCPAddr(hp, "t0ken")
	SetSocket(addr)
	defer SetSocket("")
	home := t.TempDir()
	if err := Start(home, fakeHost{}); err != nil {
		t.Fatal(err)
	}
	defer Stop()
	if Socket(home) != addr {
		t.Fatalf("Socket=%s", Socket(home))
	}
	if _, err := rpc.NewClient(addr).Status(); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.NewClient(ipc.TCPAddr(hp, "wrong")).Status(); err == nil {
		t.Fatal("令牌错误时不应能访问")
	}
}

func TestSnapshot(t *testing.T) {
	s, err := snapshot(fakeHost{}.Network())
	if err != nil {
		t.Fatal(err)
	}
	if s.PhysicalInterface() != "wlan0" || len(s.PhysicalDNS()) != 1 || s.PhysicalDNS()[0] != "10.0.2.3" {
		t.Fatalf("快照：%+v", s)
	}
	if s, err := snapshot(""); err != nil || s.PrimaryInterface != "" {
		t.Fatalf("无网络：%+v %v", s, err)
	}
	if _, err := snapshot("{"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestFindOwner(t *testing.T) {
	o := opener{fakeHost{}}
	w, ok, err := o.FindOwner("tcp", netip.MustParseAddrPort("172.19.0.1:40000"), netip.MustParseAddrPort("198.18.0.1:443"))
	if err != nil || !ok || w.UID != 10100 || len(w.Packages) != 1 || w.Packages[0] != "com.example.app" {
		t.Fatalf("owner=%+v ok=%v err=%v", w, ok, err)
	}
	if _, ok, _ := o.FindOwner("udp", netip.MustParseAddrPort("172.19.0.1:40000"), netip.MustParseAddrPort("198.18.0.1:443")); ok {
		t.Fatal("UDP 不应命中")
	}
	var _ engine.OwnerFinder = o
}

type fakeProxyHost struct {
	mu   sync.Mutex
	reqs []string
}

func (f *fakeProxyHost) SetProxy(req string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return nil
}

func (f *fakeProxyHost) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// macOS 系统扩展：系统代理交给宿主放进隧道参数；打开时下发地址，关闭时清除，相同设置不重复下发。
func TestSystemProxyViaHost(t *testing.T) {
	if !fullEngine {
		t.Skip("velumonly 构建不支持 Start")
	}
	if runtime.GOOS == "windows" {
		t.Skip("mobilebind 只用于移动端与 macOS 系统扩展")
	}
	ph := &fakeProxyHost{}
	SetProxyHost(ph)
	defer SetProxyHost(nil)
	home, err := os.MkdirTemp("/tmp", "velum-mb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	ln, err := net.Listen("tcp", "127.0.0.1:0") // 入口端口不与本机正在运行的 Velum 冲突
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	ln.Close()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("listen: "+listen+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Start(home, fakeHost{}); err != nil {
		t.Fatal(err)
	}
	defer Stop()
	c := rpc.NewClient(Socket(home))
	if _, err := c.Switch(rpc.SwitchRequest{SystemProxy: "on"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reload(); err != nil { // 设置未变：不再下发
		t.Fatal(err)
	}
	if _, err := c.Switch(rpc.SwitchRequest{SystemProxy: "off"}); err != nil {
		t.Fatal(err)
	}
	got := ph.calls()
	if len(got) != 2 || !strings.Contains(got[0], `"host":"127.0.0.1"`) || got[1] != "" {
		t.Fatalf("宿主收到的系统代理设置：%q", got)
	}
	var req ProxyRequest
	if err := json.Unmarshal([]byte(got[0]), &req); err != nil || req.Port == 0 {
		t.Fatalf("%q %v", got[0], err)
	}
}
