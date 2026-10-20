package main

import (
	"context"
	"os/exec"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

// setupPlatform 接入 macOS 的系统代理（networksetup）、NetworkSense 采集与 DNS 缓存清理。
func setupPlatform(d *daemon.Daemon, paths config.Paths) {
	daemon.FlushDNSCache = func() error { return exec.Command("/usr/bin/dscacheutil", "-flushcache").Run() }
	sp := sysproxy.New(paths.SysProxySnapshot)
	sp.Logf = d.Logger.Printf
	d.SysProxy = sp
	d.EffectiveProxy = sysproxy.EffectiveReader
	d.NetSnapshot = netsense.DarwinSnapshot
}

func platformCommand(string, []string) bool { return false }

func preparePaths(paths config.Paths, _ []string) (config.Paths, error) { return paths, nil }

func runDaemon(ctx context.Context, d *daemon.Daemon) error { return d.Run(ctx) }
