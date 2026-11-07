package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/config"
)

// 开启网页界面必须有密码：回环地址也能被本机其他用户访问。
func TestWebRequiresPassword(t *testing.T) {
	load := func(web string) error {
		home := t.TempDir()
		paths := config.PathsFor(home)
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("listen: 127.0.0.1:7899\n"+web), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := config.Load(paths)
		return err
	}
	if err := load("web:\n  enabled: true\n  listen: 127.0.0.1:9090\n"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("没有密码应当拒绝：%v", err)
	}
	if err := load("web:\n  enabled: true\n  listen: nope\n  password: x\n"); err == nil {
		t.Error("地址无效应当拒绝")
	}
	if err := load("web:\n  enabled: true\n  listen: 127.0.0.1:7899\n  password: x\n"); err == nil {
		t.Error("不能与 listen 同端口")
	}
	if err := load("web:\n  enabled: true\n  listen: 127.0.0.1:9090\n  password: x\n"); err != nil {
		t.Errorf("有效配置：%v", err)
	}
}
