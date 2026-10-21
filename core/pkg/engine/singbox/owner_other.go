//go:build !darwin

package singbox

import (
	"net/netip"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/process"
)

// 其他平台用 sing-box 原生的连接归属查询（Android 由 VpnService 经 OwnerFinder 提供）。
const nativeOwner = false

func findOwner(int, netip.AddrPort, netip.AddrPort) (*adapter.ConnectionOwner, error) {
	return nil, process.ErrNotFound
}
