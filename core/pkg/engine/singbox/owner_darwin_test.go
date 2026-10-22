package singbox

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// 经 libproc 查到其他进程（curl）的连接；不依赖系统连接表（签名后的 velumd 读不到）。
func TestSocketOwner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cmd := exec.Command("/usr/bin/curl", "-s", "-m", "5", "http://"+ln.Addr().String())
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	src := netip.MustParseAddrPort(c.RemoteAddr().String())
	sockets.Lock()
	sockets.at = sockets.at.AddDate(-1, 0, 0) // 让快照过期，重新列出
	sockets.Unlock()
	pid := socketOwner(portKey{syscall.IPPROTO_TCP, src.Port()})
	if pid != cmd.Process.Pid {
		t.Fatalf("pid = %d，应为 curl 的 %d", pid, cmd.Process.Pid)
	}
	if path, err := pidPath(pid); err != nil || path != "/usr/bin/curl" {
		t.Fatalf("path = %q, %v", path, err)
	}
}

// 紧接着上一次列出建立的连接也能查到（之前在最短间隔内直接放弃，密集建立的连接会漏掉归属）。
func TestSocketOwnerBurst(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for i := 0; i < 5; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		port := netip.MustParseAddrPort(c.LocalAddr().String()).Port()
		if pid := socketOwner(portKey{syscall.IPPROTO_TCP, port}); pid != os.Getpid() {
			t.Fatalf("第 %d 条连接：pid = %d，应为 %d", i+1, pid, os.Getpid())
		}
	}
}
