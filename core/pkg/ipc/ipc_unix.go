//go:build unix

package ipc

import (
	"context"
	"net"
	"os"
)

// Listen 在 Unix socket 上监听：删除残留文件，权限设为 0600（只有本用户可访问）。关闭监听时删除文件。
func Listen(addr string, _ ListenOptions) (net.Listener, error) {
	_ = os.Remove(addr)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(addr, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil // net.UnixListener 关闭时会删除 socket 文件
}

func Dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}
