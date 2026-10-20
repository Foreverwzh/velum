//go:build !darwin && !windows

package main

import (
	"context"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
)

// setupPlatform：其他平台暂不接管系统代理，也不启用 NetworkSense；TUN 没有平台实现（不路由任何流量），明确标为不可用。
func setupPlatform(d *daemon.Daemon, _ config.Paths) {
	d.TUNUnavailable = "此平台暂不支持 TUN"
}

func platformCommand(string, []string) bool { return false }

func preparePaths(paths config.Paths, _ []string) (config.Paths, error) { return paths, nil }

func runDaemon(ctx context.Context, d *daemon.Daemon) error { return d.Run(ctx) }
