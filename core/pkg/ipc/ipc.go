// Package ipc 是 velumd 本地接口的传输层（ARCHITECTURE §3.6）：macOS 为 Unix socket（权限 0600），
// Windows 为 Named Pipe（ACL 只允许 SYSTEM、Administrators 与指定用户）。上层协议（HTTP）与平台无关。
//
// 地址为 tcp://127.0.0.1:端口?token=… 时改用回环 TCP + 令牌（macOS 系统扩展：沙盒里的 root 进程无法提供用户可连的 socket，
// 见 docs/MN-PLAN.md Q2），由 tcp.go 实现。
package ipc

import (
	"context"
	"io/fs"
	"net"
	"time"
)

// ListenOptions 是监听选项。
type ListenOptions struct {
	// AllowSIDs 是 Windows 上额外允许访问的用户 SID；为空时允许当前进程的用户。
	// 以服务（SYSTEM）运行时由安装参数传入安装用户的 SID。Unix 上忽略（靠文件权限）。
	AllowSIDs []string
	// SocketMode 是 Unix socket 的权限；为 0 时是 0600（只有本用户可访问）。Windows 上忽略。
	SocketMode fs.FileMode
}

// Listen 在 addr 上监听本地接口。
func Listen(addr string, opts ListenOptions) (net.Listener, error) {
	if t, ok, err := parseTCP(addr); ok {
		if err != nil {
			return nil, err
		}
		return listenTCP(t)
	}
	return listenLocal(addr, opts)
}

// Dial 连接 addr 上的本地接口。
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	if t, ok, err := parseTCP(addr); ok {
		if err != nil {
			return nil, err
		}
		return dialTCP(ctx, t)
	}
	return dialLocal(ctx, addr)
}

// Alive 判断 addr 上是否已有服务在监听。
func Alive(addr string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
