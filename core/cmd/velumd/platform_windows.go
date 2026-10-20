package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
	"github.com/Foreverwzh/velum/core/pkg/winuser"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// setupPlatform 接入 Windows 的系统代理（WinINet）、NetworkSense 采集与 DNS 缓存清理。
// 以服务（SYSTEM）运行时系统代理经用户会话子进程设置；以普通用户运行时没有权限创建 wintun，TUN 不可用。
func setupPlatform(d *daemon.Daemon, paths config.Paths) {
	daemon.FlushDNSCache = func() error {
		cmd := exec.Command("ipconfig", "/flushdns")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		return cmd.Run()
	}
	var b sysproxy.WinBackend = sysproxy.WinINetBackend{}
	if asService() {
		exe, _ := os.Executable()
		b = sysproxy.UserSessionBackend{Exe: exe, AllowSIDs: d.AllowSIDs}
	} else {
		d.TUNUnavailable = "TUN 需要安装 Velum 服务：在 App 中开启 TUN 时按提示安装（或以管理员身份运行 velumd service install）"
	}
	m := sysproxy.NewWindows(paths.SysProxySnapshot, b)
	m.Logf = d.Logger.Printf
	d.SysProxy, d.EffectiveProxy = m, m.Effective
	d.NetSnapshot = func() (netsense.Snapshot, error) { return netsense.WindowsSnapshot(render.WindowsTUNName) }
}

// preparePaths：以服务运行时，Velum 维护的数据放到 %ProgramData%\Velum（只有 SYSTEM / 管理员可写），
// 用户目录中的文件以用户身份访问，本地接口使用服务的 Pipe。
func preparePaths(paths config.Paths, allowSIDs []string) (config.Paths, error) {
	if !asService() {
		return paths, nil
	}
	if len(allowSIDs) == 0 {
		return paths, errors.New("以服务运行需要 --allow-sid（由 velumd service install 设置）")
	}
	userfs.Impersonate = func(fn func() error) error { return winuser.Impersonate(allowSIDs, fn) }
	dir, err := config.ServiceDataDir()
	if err != nil {
		return paths, err
	}
	if err := secureDataDir(dir, allowSIDs); err != nil {
		return paths, err
	}
	paths = paths.WithData(dir)
	paths.Socket = config.ServicePipe
	return paths, nil
}

func asService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// platformCommand 处理 Windows 专用子命令：service（安装 / 启停服务）与 sysproxy（服务代为设置系统代理时在用户会话中执行）。
func platformCommand(name string, args []string) bool {
	var err error
	switch name {
	case "service":
		err = serviceCommand(args)
	case "sysproxy":
		var in []byte
		if len(args) == 1 && args[0] == "write" {
			in, _ = io.ReadAll(os.Stdin)
		}
		var out []byte
		if out, err = sysproxy.RunCommand(args, in); err == nil {
			_, _ = os.Stdout.Write(out)
		}
	default:
		return false
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return true
}

func runDaemon(ctx context.Context, d *daemon.Daemon) error {
	if asService() {
		return svc.Run(serviceName, &serviceHandler{d: d})
	}
	return d.Run(ctx)
}

type serviceHandler struct{ d *daemon.Daemon }

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- h.d.Run(context.Background()) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	finish := func(err error) (bool, uint32) {
		if err != nil {
			h.d.Logger.Printf("退出：%v", err)
			return true, 1 // 非 0 退出码：SCM 按失败处理并自动重启
		}
		return false, 0
	}
	for {
		select {
		case err := <-done: // 经本地接口停止（App 退出 / vlu down）或启动失败
			st <- svc.Status{State: svc.StopPending}
			return finish(err)
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				h.d.Shutdown() // 还原系统代理、关闭 TUN
				return finish(<-done)
			}
		}
	}
}
