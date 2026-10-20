package mobilebind

import (
	"net/netip"
	"os"
	"runtime"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
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
