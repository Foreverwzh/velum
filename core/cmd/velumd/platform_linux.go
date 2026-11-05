package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
)

// asService：systemd 单元（见 service_linux.go）设置了 VELUM_SERVICE=1。
func asService() bool { return os.Getenv("VELUM_SERVICE") == "1" }

// setupPlatform：TUN 尚未实现（docs/ML-PLAN.md L4）。以服务运行时本地接口开放给 velum 组，并拒绝无认证的对外监听。
func setupPlatform(d *daemon.Daemon, _ config.Paths) {
	d.TUNUnavailable = "Linux 暂不支持 TUN"
	if asService() {
		d.SocketMode, d.LoopbackListenOnly = 0o660, true
	}
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
