//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Listen 在 Named Pipe 上监听。ACL：SYSTEM、Administrators、AllowSIDs（为空时为当前进程的用户）完全控制，其余拒绝。
func Listen(addr string, opts ListenOptions) (net.Listener, error) {
	sids := opts.AllowSIDs
	if len(sids) == 0 {
		u, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return nil, fmt.Errorf("读取当前用户：%w", err)
		}
		sids = []string{u.User.Sid.String()}
	}
	sddl, err := PipeSDDL(sids)
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: sddl, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
}

// Dial 连接 Named Pipe，并确认服务端可信：Pipe 名字是全局的，任何进程都可以抢先创建同名 Pipe 冒充 velumd。
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := verifyServer(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("%s：%w", addr, err)
	}
	return c, nil
}

// verifyServer 检查 Pipe 对象的所有者：SYSTEM / Administrators（Velum 服务）或当前用户（以用户身份运行的 velumd）。
// 普通用户无法创建所有者为 SYSTEM / Administrators 的对象；同一用户的进程本就能控制该用户的 velumd，不构成提权。
func verifyServer(c net.Conn) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法检查 Pipe 的所有者")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("读取 Pipe 所有者：%w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("读取 Pipe 所有者：%w", err)
	}
	if TrustedOwner(owner) {
		return nil
	}
	return fmt.Errorf("Pipe 的所有者 %s 不可信（可能被其他程序冒充）", owner)
}

// TrustedOwner 判断 sid 是否为 SYSTEM、Administrators 或当前用户。
func TrustedOwner(sid *windows.SID) bool {
	if sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && sid.Equals(u.User.Sid)
}

// PipeSDDL 生成 Pipe 的安全描述符：受保护的 DACL（不继承），只列出允许的主体。
func PipeSDDL(sids []string) (string, error) {
	var b strings.Builder
	b.WriteString("D:P(A;;GA;;;SY)(A;;GA;;;BA)")
	for _, s := range sids {
		if _, err := windows.StringToSid(s); err != nil {
			return "", fmt.Errorf("无效的 SID %q：%w", s, err)
		}
		fmt.Fprintf(&b, "(A;;GA;;;%s)", s)
	}
	return b.String(), nil
}
