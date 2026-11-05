package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirewallAdvice(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "ufw.conf")
	if ufwEnabled(conf) {
		t.Error("没有配置文件时不算开启")
	}
	_ = os.WriteFile(conf, []byte("# ENABLED=yes 说明\nENABLED=no\n"), 0o644)
	if ufwEnabled(conf) {
		t.Error("ENABLED=no 不算开启")
	}
	_ = os.WriteFile(conf, []byte("ENABLED=yes\nLOGLEVEL=low\n"), 0o644)
	if !ufwEnabled(conf) {
		t.Error("应识别为开启")
	}
	net := t.TempDir()
	_ = os.Mkdir(filepath.Join(net, "eth0"), 0o755)
	if hasContainerBridge(net) {
		t.Error("没有网桥")
	}
	_ = os.Mkdir(filepath.Join(net, "br-abc"), 0o755)
	if !hasContainerBridge(net) {
		t.Error("应识别自定义网络的网桥")
	}
	if len(firewallAdvice(true, false))+len(firewallAdvice(false, true)) != 0 {
		t.Error("缺一个条件就不提示")
	}
	if w := firewallAdvice(true, true); len(w) != 1 || !strings.Contains(w[0].Reason, ufwContainerRule) {
		t.Errorf("应给出具体命令：%+v", w)
	}
}
