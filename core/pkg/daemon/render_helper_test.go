package daemon_test

import (
	"log"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
)

// newDaemon 同 daemon.New，并像桌面端一样注入 sing-box 渲染器（daemon 本身不依赖 sing-box）。
func newDaemon(paths config.Paths, eng engine.Engine, l *log.Logger) *daemon.Daemon {
	d := daemon.New(paths, eng, l)
	d.Render = render.Renderer
	return d
}
