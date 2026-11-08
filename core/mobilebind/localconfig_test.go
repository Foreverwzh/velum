package mobilebind

import (
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
)

// 本地引擎的缓存不能放在 App Group 里：App 挂起时持有共享目录的文件锁会被系统杀掉。
func TestLocalEngineCacheStaysOutOfAppGroup(t *testing.T) {
	cfg := engine.Config{CachePath: "/group/velum/cache/singbox.db"}
	if got := localConfig(cfg).CachePath; got != "" {
		t.Errorf("本地引擎缓存路径应留空（用 App 的临时目录）：%q", got)
	}
}
