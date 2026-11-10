//go:build velumonly

package mobilebind

import (
	"io"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
)

// 上架构建（velumonly）：不含 sing-box。隧道与 App 内本地内核都是 velum-engine（本地内核经 TunnelHost 交给 Swift）；
// daemon 不渲染 sing-box 配置，只生成 velum-engine 的。

var renderer func(*compiler.Plan, string) (*engine.Rendered, error)

// 远程 .srs 规则集由 sing-box 解析：这个构建里不可用（GEOIP / GEOSITE 走 velum 的 GeoSource）。
func setupRuleSets() {}

// fullEngine 表示支持桌面式入口 Start（App 内运行完整内核）。
const fullEngine = false

func newFullEngine(io.Writer) (engine.Engine, error) {
	return nil, i18n.New("这个构建只支持 iOS 的分离模式（StartSplit）")
}

func newLocalEngine(home string, host TunnelHost, _ io.Writer) localEngine {
	return newVelumLocal(home, host)
}
