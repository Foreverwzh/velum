package mobilebind

import (
	"encoding/json"

	"github.com/Foreverwzh/velum/core/pkg/netsense"
)

// netSnapshot：macOS 的扩展里可以直接读路由表与解析器，与桌面端共用完整的快照，从而识别其他 VPN 的网段与 DNS；
// 读不到物理网卡时退回 App 报告的网络。沙盒里 ipconfig 取不到 DHCP 下发的 DNS，由 App 报告的物理服务 DNS 补上。
func netSnapshot(host Host) (netsense.Snapshot, error) {
	app, err := snapshot(host.Network())
	if s, e := netsense.DarwinSnapshot(); e == nil && s.PhysicalInterface() != "" {
		if len(s.DHCPNameservers) == 0 {
			s.DHCPNameservers = app.DHCPNameservers
		}
		return s, nil
	}
	return app, err
}

// NetDiag 返回扩展内读到的网络快照摘要（诊断用）：物理网卡、主接口、路由与解析器数量、识别出的外部 VPN。
func NetDiag() string {
	s, err := netsense.DarwinSnapshot()
	if err != nil {
		return "读取失败：" + err.Error()
	}
	a := netsense.Analyze(s)
	b, _ := json.Marshal(map[string]any{
		"physical": s.PhysicalInterface(), "primary": s.PrimaryInterface, "routes": len(s.Routes),
		"resolvers": len(s.Resolvers), "dhcp": s.DHCPNameservers, "vpns": a.VPNs,
	})
	return string(b)
}
