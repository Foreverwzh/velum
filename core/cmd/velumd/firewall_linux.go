package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// ufwContainerRule 放行容器到 auto_redirect 重定向端口的连接。端口每次启动都是随机的高位端口，只能放行整个范围。
const ufwContainerRule = "sudo ufw allow from 172.16.0.0/12 proto tcp to any port 32768:60999"

// firewallAdvisories：ufw 默认拒绝入站，而 auto_redirect 把容器的连接重定向到本机端口，这些包经过 INPUT 链，容器因此不能出站
// （spikes/ml-linux/README.md F6）。这要放宽防火墙，属于用户的安全决定，所以只提示，不自动修改。
func firewallAdvisories() []ir.Warning {
	return firewallAdvice(ufwEnabled("/etc/ufw/ufw.conf"), hasContainerBridge("/sys/class/net"))
}

func firewallAdvice(ufw, bridge bool) []ir.Warning {
	if !ufw || !bridge {
		return nil
	}
	return []ir.Warning{{Category: "global", Kind: "tun.firewall", Level: ir.Degraded,
		Reason:  "ufw 已开启且有容器网络：TUN 接管后容器可能无法出站。如需要，执行 " + ufwContainerRule + "（只放行容器到本机的高位 TCP 端口；已放行可忽略）",
		Subject: "ufw", Source: ir.Source{Kind: ir.SourceVelum, Text: "防火墙检查"}}}
}

func ufwEnabled(conf string) bool {
	b, err := os.ReadFile(conf)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "ENABLED=yes" {
			return true
		}
	}
	return false
}

// hasContainerBridge 判断是否有 Docker 的网桥（docker0、自定义网络的 br-*）。
func hasContainerBridge(sysNet string) bool {
	if _, err := os.Stat(filepath.Join(sysNet, "docker0")); err == nil {
		return true
	}
	m, _ := filepath.Glob(filepath.Join(sysNet, "br-*"))
	return len(m) > 0
}
