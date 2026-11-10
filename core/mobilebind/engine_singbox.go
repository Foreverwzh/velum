//go:build !velumonly

package mobilebind

import (
	"io"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
)

// 默认构建（对照测试期间）：App 内本地内核与桌面式入口用 sing-box，iOS 扩展可在设置里切换到 velum-engine。
// 上架构建用 velumonly 标签，见 engine_velumonly.go。

// renderer 是给 daemon 的 sing-box 渲染器。
var renderer func(*compiler.Plan, string) (*engine.Rendered, error) = render.Renderer

func setupRuleSets() { daemon.RuleSetLoader = singbox.LoadRuleSetFile }

// fullEngine 表示支持桌面式入口 Start（App 内运行完整内核）。
const fullEngine = true

func newFullEngine(lf io.Writer) (engine.Engine, error) {
	return singbox.New(singbox.Options{Log: lf}), nil
}

func newLocalEngine(_ string, _ TunnelHost, lf io.Writer) localEngine {
	return singbox.New(singbox.Options{Log: lf})
}
