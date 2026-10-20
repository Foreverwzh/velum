// Package ipc 是 velumd 本地接口的传输层（ARCHITECTURE §3.6）：macOS 为 Unix socket（权限 0600），
// Windows 为 Named Pipe（ACL 只允许 SYSTEM、Administrators 与指定用户）。上层协议（HTTP）与平台无关。
package ipc

import (
	"context"
	"time"
)

// ListenOptions 是监听选项。
type ListenOptions struct {
	// AllowSIDs 是 Windows 上额外允许访问的用户 SID；为空时允许当前进程的用户。
	// 以服务（SYSTEM）运行时由安装参数传入安装用户的 SID。Unix 上忽略（靠文件权限）。
	AllowSIDs []string
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
