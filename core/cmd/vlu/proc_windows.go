//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
	"golang.org/x/sys/windows"
)

const exeSuffix = ".exe"

// detached 让 velumd 脱离控制台（关闭终端不影响它），也不弹出新窗口。
func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
}

// startService 在已安装 Velum 服务时启动它（服务以 SYSTEM 运行，支持 TUN）；未安装时返回 false，由调用方以当前用户启动 velumd。
func startService(bin string) (bool, error) {
	out, err := exec.Command(bin, "service", "status").Output()
	if err != nil {
		return false, nil
	}
	var st struct {
		Installed bool `json:"installed"`
	}
	if json.Unmarshal(out, &st) != nil || !st.Installed {
		return false, nil
	}
	if out, err := exec.Command(bin, "service", "start").CombinedOutput(); err != nil {
		return true, fmt.Errorf("启动 Velum 服务：%s", strings.TrimSpace(string(out)))
	}
	fmt.Println("已启动 Velum 服务")
	return true, nil
}

func localSysProxy(paths config.Paths) interface {
	Heal() (sysproxy.HealResult, error)
} {
	return sysproxy.NewWindows(paths.SysProxySnapshot, sysproxy.WinINetBackend{})
}
