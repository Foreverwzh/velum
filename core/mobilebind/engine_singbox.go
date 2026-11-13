package mobilebind

import (
	"io"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
)

// 内核：sing-box（Android、macOS 系统扩展）。

// renderer 是给 daemon 的 sing-box 渲染器。
var renderer func(*compiler.Plan, string) (*engine.Rendered, error) = render.Renderer

func setupRuleSets() { daemon.RuleSetLoader = singbox.LoadRuleSetFile }

func newFullEngine(lf io.Writer) (engine.Engine, error) {
	return singbox.New(singbox.Options{Log: lf}), nil
}
