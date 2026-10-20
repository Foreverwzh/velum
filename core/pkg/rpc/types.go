// Package rpc 定义 velumd 本地接口的数据类型与客户端（HTTP over Unix socket）。
package rpc

import (
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
)

type GroupStatus struct {
	Tag      string         `json:"tag"`
	Type     string         `json:"type"`
	Selected string         `json:"selected"`
	Members  []string       `json:"members"`
	Delays   map[string]int `json:"delays,omitempty"` // 成员最近一次测速（毫秒）
}

type SelectRequest struct {
	Group  string `json:"group"`
	Member string `json:"member"`
}

type TestRequest struct {
	Group string `json:"group"`
}

type TestResponse struct {
	Delays map[string]int `json:"delays"` // 毫秒；-1 表示失败
}

type OverlayDoc struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type RepairStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type RepairResponse struct {
	Steps []RepairStep `json:"steps"`
}

// SubscriptionConfig 是 config.yaml 中的订阅来源（url 未脱敏，仅经本机 socket 提供给本人）。
type SubscriptionConfig struct {
	URL       string `json:"url"`
	File      string `json:"file,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// TUNApps 是按应用分流的设置（Android 包名，写在 config.yaml 的 tun 节）。
// IncludeApps 非空时只有这些应用经 TUN；否则 ExcludeApps 中的应用不经 TUN。
type TUNApps struct {
	IncludeApps []string `json:"include_apps"`
	ExcludeApps []string `json:"exclude_apps"`
}

// SetSubscriptionRequest 设置订阅地址：先拉取并解析，通过后才写入 config.yaml（同时清空 file）。
type SetSubscriptionRequest struct {
	URL       string `json:"url"`
	UserAgent string `json:"user_agent,omitempty"` // 为空表示不改
}

type SubscriptionStatus struct {
	Source    string    `json:"source"` // url（已脱敏）/ 文件路径
	Origin    string    `json:"origin"` // remote / cache / file
	FetchedAt time.Time `json:"fetched_at"`
	FetchErr  string    `json:"fetch_error,omitempty"`
	Nodes     int       `json:"nodes"`
	Groups    int       `json:"groups"`
	Rules     int       `json:"rules"`
	RuleSets  int       `json:"rule_sets"`
}

type SysProxyStatus struct {
	Supported bool `json:"supported"`
	Active    bool `json:"active"` // 已接管（存在快照）
	// Overridden 为 true 表示已写入各网络服务，但实际生效的代理不是 Velum（主网络服务被其他 VPN / 代理工具接管）。
	Overridden       bool   `json:"overridden,omitempty"`
	Effective        string `json:"effective,omitempty"` // 实际生效的 HTTP 代理
	PrimaryInterface string `json:"primary_interface,omitempty"`
}

// HelperStatus 是 velum-helper（TUN 所需的 root 组件）的可用性。
type HelperStatus struct {
	Socket    string `json:"socket"`
	Reachable bool   `json:"reachable"`          // 可连接且协议版本匹配
	Version   string `json:"version"`            // velumd 需要的协议版本
	Outdated  bool   `json:"outdated,omitempty"` // 在运行但版本不匹配，需要更新
	Error     string `json:"error,omitempty"`
}

type TUNStatus struct {
	Name      string   `json:"name,omitempty"` // utun 名称（引擎运行时）
	Address   string   `json:"address"`
	Address6  string   `json:"address6,omitempty"`
	SystemDNS []string `json:"system_dns,omitempty"`
	Routes    []string `json:"routes"`
	Exclude   []string `json:"exclude,omitempty"`
	// Android：只经 / 不经 TUN 的应用（包名）
	IncludeApps []string `json:"include_apps,omitempty"`
	ExcludeApps []string `json:"exclude_apps,omitempty"`
}

type NetworkStatus struct {
	PrimaryInterface string         `json:"primary_interface"`
	VPNs             []netsense.VPN `json:"vpns,omitempty"`
	SplitDNSDomains  []string       `json:"split_dns_domains,omitempty"`
	Captive          string         `json:"captive,omitempty"` // online / captive / offline
	CaptiveDetail    string         `json:"captive_detail,omitempty"`
	// Paused 非空表示接管被暂停的原因（Captive Portal / 让路给全局型 VPN / TUN 不可用）。
	Paused string `json:"paused,omitempty"`
}

// SwitchRequest 切换开关：on / off / config（回到 config.yaml 的设置）；留空表示不改。保存在 state.json，重启后保持。
type SwitchRequest struct {
	SystemProxy string `json:"system_proxy,omitempty"`
	TUN         string `json:"tun,omitempty"`
}

type Status struct {
	PID           int    `json:"pid"`
	Version       string `json:"version"`
	Running       bool   `json:"running"`
	Mode          string `json:"mode"`            // 便于展示：proxy / sysproxy / tun / tun+sysproxy（实际生效）
	SystemProxyOn bool   `json:"system_proxy_on"` // 实际生效
	TUNOn         bool   `json:"tun_on"`          // 实际生效
	// WantSystemProxy / WantTUN 是用户要求的状态（配置文件或运行时切换）；可能因 NetworkSense 暂停而与实际不同。
	WantSystemProxy bool           `json:"want_system_proxy"`
	WantTUN         bool           `json:"want_tun"`
	Overridden      bool           `json:"overridden,omitempty"` // 有运行时切换，未写入 config.yaml
	SysProxy        SysProxyStatus `json:"sys_proxy"`
	TUN             *TUNStatus     `json:"tun,omitempty"`
	Network         *NetworkStatus `json:"network,omitempty"`
	Listen          string         `json:"listen"`
	Home            string         `json:"home"`
	StartedAt       time.Time      `json:"started_at"`
	// Starting 为 true 时首次加载配置、启动引擎尚未完成（此时其余字段可能为空）。
	Starting     bool               `json:"starting,omitempty"`
	LoadedAt     time.Time          `json:"loaded_at"`
	Subscription SubscriptionStatus `json:"subscription"`
	Warnings     int                `json:"warnings"`
	LastError    string             `json:"last_error,omitempty"`
	Groups       []GroupStatus      `json:"groups"`
}

type WhyRequest struct {
	Target   string   `json:"target"`
	Resolved []string `json:"resolved,omitempty"`
	// NoResolve 为 true 时不向引擎查询解析结果（默认会查询，使 GeoIP 等 IP 规则可判定）。
	NoResolve bool   `json:"no_resolve,omitempty"`
	Process   string `json:"process,omitempty"`
	Network   string `json:"network,omitempty"`
}

type WhyResponse struct {
	Report explain.Report `json:"report"`
	// Chain 是决策出站按策略组当前选择展开后的链路（来自运行中的引擎）。
	Chain []string `json:"chain,omitempty"`
	// Resolved / ResolvedBy：推演使用的解析结果及来源（user = --resolve；engine = 引擎 DNS 规则）。
	Resolved   []string `json:"resolved,omitempty"`
	ResolvedBy string   `json:"resolved_by,omitempty"`
	ResolveErr string   `json:"resolve_error,omitempty"`
	// RuleSetErrors 是无法加载用于推演的规则集（tag → 原因）。
	RuleSetErrors map[string]string `json:"rule_set_errors,omitempty"`
}

type ConnectionsResponse struct {
	Connections []engine.Connection `json:"connections"`
}

type WarningsResponse struct {
	Warnings []ir.Warning `json:"warnings"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
