package singbox

import (
	"net"
	"net/netip"
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
