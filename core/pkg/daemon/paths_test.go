package daemon_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/Foreverwzh/velum/core/pkg/config"
)

var pipeSeq atomic.Int64

// testPaths 让每个测试使用独立的本地接口：Windows 上 Named Pipe 是全局名字，不随目录区分。
func testPaths(home string) config.Paths {
	p := config.PathsFor(home)
	if runtime.GOOS == "windows" {
		p.Socket = fmt.Sprintf(`\\.\pipe\velumd-test-%s-%d`, filepath.Base(home), pipeSeq.Add(1))
	}
	return p
}

// tmpBase：Unix socket 路径有长度上限（macOS 104 字节），系统临时目录可能太长，因此用 /tmp。
func tmpBase() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return "/tmp"
}
