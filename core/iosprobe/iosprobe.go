// Package iosprobe 是 iOS 内存 spike 的 Go 入口（spikes/ios-memory）：只运行内核，不含 daemon。
// 配置由 Mac 上预先生成，扩展里不做订阅转换。
package iosprobe

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
)

type opener struct{ fd int }

func (o opener) OpenTUN(engine.TUNRequest) (int, string, io.Closer, error) {
	return o.fd, "utun", io.NopCloser(nil), nil
}

var eng *singbox.Engine

// Start 启动内核。gogc <= 0、limitMiB <= 0 时不设置，便于对比。
func Start(cfgPath, cachePath string, fd, gogc, limitMiB int) error {
	if gogc > 0 {
		debug.SetGCPercent(gogc)
	}
	if limitMiB > 0 {
		debug.SetMemoryLimit(int64(limitMiB) << 20)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	eng = singbox.New(singbox.Options{})
	return eng.Start(context.Background(), engine.Config{JSON: b, LogLevel: "error", CachePath: cachePath,
		TUN: &engine.TUNConfig{Opener: opener{fd}}})
}

// Stop 停止内核。
func Stop() {
	if eng != nil {
		_ = eng.Stop()
		eng = nil
	}
}

// Stats 返回 Go 运行时的内存读数（MiB）。
func Stats() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("heap=%d stack=%d sys=%d goroutines=%d gc=%d", m.HeapInuse>>20, m.StackInuse>>20, m.Sys>>20, runtime.NumGoroutine(), m.NumGC)
}
