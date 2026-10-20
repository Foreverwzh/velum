// Package helper 实现 velum-helper（root）与 velumd 之间的协议、服务端与客户端（ARCHITECTURE §3.2）。
//
// helper 只做特权操作：创建 utun、配置地址、增删路由，并通过 SCM_RIGHTS 把 utun FD 交给用户态 velumd；
// velumd 断开（含 kill -9）时立即删除该会话添加的路由并关闭 utun。
package helper

import (
	"errors"
	"fmt"
	"net/netip"
)

// Version 是 helper 协议版本，velumd 连接时通过 ping 核对。
const Version = "5" // 2：TUN IPv6 地址、set_dns；3：打开 TUN 时为物理网卡补作用域默认路由；4：close（同步清理）；5：网络变化后重新补作用域默认路由

// ErrVersionMismatch 表示 helper 在运行，但协议版本与 velumd 不一致（需要更新 helper）。
var ErrVersionMismatch = errors.New("helper 协议版本不匹配")

// DefaultSocket 是 helper 的默认监听地址。
const DefaultSocket = "/var/run/velum-helper.sock"

const (
	OpPing      = "ping"
	OpOpenTun   = "open_tun"
	OpAddRoutes = "add_routes"
	OpDelRoutes = "del_routes"
	// OpSetDNS 把已启用硬件网络服务的系统 DNS 设为指定地址（TUN 内的 DNS 地址）；会话断开时还原。
	OpSetDNS = "set_dns"
	// OpClose 同步清理本会话（还原 DNS、删除路由、关闭 utun）后应答。velumd 重建 TUN 前用它关闭旧会话：
	// 仅断开连接时 helper 异步清理，新会话加路由可能与旧会话的路由冲突。
	OpClose = "close"
)

type Request struct {
	Op     string   `json:"op"`
	MTU    uint32   `json:"mtu,omitempty"`
	Inet4  []string `json:"inet4,omitempty"`  // TUN 地址，如 "172.19.0.1/30"
	Inet6  []string `json:"inet6,omitempty"`  // TUN IPv6 地址，如 "fdfe:dcba:9876::1/126"
	Routes []string `json:"routes,omitempty"` // 经 TUN 的路由，如 "0.0.0.0/1"、"::/1"
	DNS    []string `json:"dns,omitempty"`    // set_dns：系统 DNS 服务器
}

type Response struct {
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Name    string   `json:"name,omitempty"`    // utun 名称
	Routes  []string `json:"routes,omitempty"`  // 当前会话持有的路由
	Version string   `json:"version,omitempty"` // ping 返回
}

// ValidateRoute 校验 helper 允许添加的路由：不允许 /0（不替换默认路由），不触碰回环与 IPv6 链路本地。
func ValidateRoute(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return p, err
	}
	p = p.Masked()
	if p.Bits() == 0 {
		return p, fmt.Errorf("拒绝 %s：不替换默认路由（全局接管请使用 0.0.0.0/1 + 128.0.0.0/1、::/1 + 8000::/1）", p)
	}
	// 只拒绝落在保留网段内的路由；0.0.0.0/1 这类宽路由虽覆盖回环，但系统中更具体的回环路由优先，不受影响
	for _, reserved := range []string{"127.0.0.0/8", "::1/128", "fe80::/10"} {
		r := netip.MustParsePrefix(reserved)
		if p.Addr().Is4() == r.Addr().Is4() && p.Bits() >= r.Bits() && r.Contains(p.Addr()) {
			return p, fmt.Errorf("拒绝 %s：属于保留网段 %s", p, r)
		}
	}
	return p, nil
}
