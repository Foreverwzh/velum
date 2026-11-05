//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

const exeSuffix = ""

// linuxServiceUnit 是 velumd service install 写入的 systemd 单元。
const linuxServiceUnit = "/etc/systemd/system/velum.service"

// detached 让 velumd 脱离终端会话（关闭终端不影响它）。
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// startService：Linux 上已安装系统服务时经 systemd 启动；其他情况 velumd 由 vlu / App 直接启动。
func startService(string) (bool, error) {
	if runtime.GOOS != "linux" {
		return false, nil
	}
	if _, err := os.Stat(linuxServiceUnit); err != nil {
		return false, nil
	}
	if out, err := exec.Command("systemctl", "start", "velum").CombinedOutput(); err != nil {
		return true, fmt.Errorf("启动 velum 服务失败（可能需要 sudo systemctl start velum）：%v\n%s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// localSysProxy 返回本地系统代理控制器（velumd 不在时用于自愈）；不支持的平台返回 nil。
func localSysProxy(paths config.Paths) interface {
	Heal() (sysproxy.HealResult, error)
} {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return sysproxy.New(paths.SysProxySnapshot)
}
