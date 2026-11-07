package main

import (
	"context"
	"fmt"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"os"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/webui"
)

// asService：systemd 单元（见 service_linux.go）设置了 VELUM_SERVICE=1。
func asService() bool { return os.Getenv("VELUM_SERVICE") == "1" }

// setupPlatform：以服务运行时本地接口开放给 velum 组，并拒绝无认证的对外监听。
// TUN 只在服务模式下提供：崩溃清理由 systemd 单元负责，试运行的回退也依赖 systemd（docs/ML-PLAN.md）。
func setupPlatform(d *daemon.Daemon, _ config.Paths) {
	d.WebAssets = webui.Assets()
	if !asService() {
		d.TUNUnavailable = i18n.T("Linux 上 TUN 需要以系统服务运行：sudo velumd service install")
		return
	}
	d.SocketMode, d.LoopbackListenOnly = 0o660, true
	d.TUNTrials, d.TUNRevertMarker = true, revertMarker
	d.ServerMode = true
	d.TUNGuard = setConnGuard
	d.SystemDNS = netsense.SystemUpstreamDNS
	d.NetSnapshot = netsense.LinuxSnapshot(render.LinuxTUNName)
	d.TUNAdvisories = firewallAdvisories
}

// preparePaths：以服务运行时，数据放到 /var/lib/velum，本地接口放到 /run/velum。
func preparePaths(paths config.Paths, _ []string) (config.Paths, error) {
	if !asService() {
		return paths, nil
	}
	paths = paths.WithData(config.ServiceDataDir)
	paths.Socket = config.ServiceSocket
	return paths, nil
}

func platformCommand(name string, args []string) bool {
	if name != "service" {
		return false
	}
	if err := serviceCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, "velumd service:", err)
		os.Exit(1)
	}
	return true
}

func runDaemon(ctx context.Context, d *daemon.Daemon) error { return d.Run(ctx) }
