// Package engine 定义数据面引擎接口（ARCHITECTURE §2.3）。本包不依赖任何内核实现。
package engine

import (
	"context"
	"io"
	"net/netip"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// Config 是一次启动所需的全部输入：渲染好的内核配置，以及内核规则下标到 IR 规则的映射。
type Config struct {
	JSON []byte
	// RuleSources[i] / RuleIndex[i] 对应内核 route.rules[i] 的来源与 IR 规则下标
	// （渲染器可能为一条 IR 规则生成多条内核规则，如前置 resolve）。
	RuleSources []ir.Source
	RuleIndex   []int
	LogLevel    string // 写入日志文件的最低级别
	// CachePath 是内核缓存文件路径（每次启动前清空）；为空时使用临时目录。
	// 注意：sing-box 在设置了 PlatformLogWriter 时会强制启用缓存文件，默认写到工作目录下的 cache.db。
	CachePath string
	// TUN 非空且 Opener 非空时由 Opener（velum-helper）创建 utun，配置中的 tun 入站经平台接口取得其 FD；
	// Opener 为空（Windows）时由内核按配置自建 TUN 与路由。
	TUN *TUNConfig
}

type TUNConfig struct {
	Routes    []string // 经 TUN 的路由，由 helper 设置
	SystemDNS []string // 非空时把系统 DNS 指向这些地址
	// IncludeApps / ExcludeApps 是只经 / 不经 TUN 的应用（Android 包名，由 VpnService 执行；其他平台忽略）。
	IncludeApps, ExcludeApps []string
	Opener                   TUNOpener
}

// TUNRequest 是打开 TUN 所需的参数：地址与 MTU 来自内核的 tun 入站，其余来自 TUNConfig。
type TUNRequest struct {
	MTU                      uint32
	Inet4, Inet6             []string // 地址（CIDR）
	Routes                   []string
	SystemDNS                []string
	IncludeApps, ExcludeApps []string
}

// TUNOpener 创建并配置 TUN（macOS：velum-helper 创建 utun；Android：VpnService），返回其 FD、名称，
// 以及释放它的 Closer（关闭后平台还原系统 DNS、删除路由并销毁 TUN）。
type TUNOpener interface {
	OpenTUN(req TUNRequest) (fd int, name string, release io.Closer, err error)
}

// OwnerFinder 是平台提供的连接归属查询，TUNOpener 可以选择实现（Android：VPN 应用可调用 getConnectionOwnerUid）。
// 实现后，内核用它识别 TUN 中每条连接所属的应用，用于进程规则与连接记录。
type OwnerFinder interface {
	// FindOwner 查询连接所属的应用；network 为 tcp / udp。找不到时返回 ok=false。
	FindOwner(network string, src, dst netip.AddrPort) (owner Owner, ok bool, err error)
}

// Owner 是连接所属的应用。
type Owner struct {
	UID      int32
	Packages []string // Android 包名（共享 UID 时可能有多个）
}

// Connection 是一条连接的动态实录（§4.4）。
type Connection struct {
	ID          uint32     `json:"id"`
	Start       time.Time  `json:"start"`
	End         *time.Time `json:"end,omitempty"` // nil 表示仍在进行
	Network     string     `json:"network"`
	Inbound     string     `json:"inbound"`
	Source      string     `json:"source"`
	Process     string     `json:"process,omitempty"`
	Domain      string     `json:"domain,omitempty"`
	Destination string     `json:"destination"`
	Resolved    []string   `json:"resolved,omitempty"`
	Sniffed     string     `json:"sniffed,omitempty"`
	RuleIndex   int        `json:"rule_index"` // IR 规则下标；-1 表示 final
	Rule        string     `json:"rule"`       // 内核对规则的描述
	Provenance  ir.Source  `json:"provenance"`
	Chain       []string   `json:"chain"` // 策略组展开后的出站链
	Upload      int64      `json:"upload"`
	Download    int64      `json:"download"`
	FirstByteMS int64      `json:"first_byte_ms"` // -1 表示没有收到下行数据
	DurationMS  int64      `json:"duration_ms"`
	Errors      []string   `json:"errors,omitempty"`
	// Untracked 为 true 表示连接未经过 tracker（如被 reject 动作拦截），信息来自日志。
	Untracked bool `json:"untracked,omitempty"`
}

// Traffic 是经过引擎的流量统计。
type Traffic struct {
	Up     int64 `json:"up"`   // 引擎启动以来的上行总字节数
	Down   int64 `json:"down"` // 下行总字节数
	Active int   `json:"active"`
}

// Engine 是数据面引擎。
type Engine interface {
	Start(ctx context.Context, cfg Config) error
	// Restart 以新配置重启引擎，会断开所有现有连接。
	Restart(ctx context.Context, cfg Config) error
	Stop() error
	Running() bool

	// Connections 返回最近的连接记录（含进行中），新的在前。
	Connections(limit int) []Connection
	// Subscribe 订阅连接结束事件；返回的函数用于取消订阅。
	Subscribe() (<-chan Connection, func())
	// Selected 返回策略组当前选中的成员；非策略组返回空。
	Selected(tag string) string
}
