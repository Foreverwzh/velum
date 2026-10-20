//go:build unix

package main

import (
	"runtime"
	"syscall"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

const exeSuffix = ""

// detached 让 velumd 脱离终端会话（关闭终端不影响它）。
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// startService：Unix 上 velumd 由 vlu / App 直接启动，没有服务。
func startService(string) (bool, error) { return false, nil }

// localSysProxy 返回本地系统代理控制器（velumd 不在时用于自愈）；不支持的平台返回 nil。
func localSysProxy(paths config.Paths) interface {
	Heal() (sysproxy.HealResult, error)
} {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return sysproxy.New(paths.SysProxySnapshot)
}
