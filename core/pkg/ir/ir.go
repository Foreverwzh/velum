// Package ir 定义 VelumIR：与数据面内核无关的中间表示。
//
// 语义约定（渲染器必须保持）：
//   - 域名后缀 "example.com" 匹配根域及全部子域（按标签对齐）；".example.com" 只匹配子域。
//   - 同一个 Cond 内，地址类条件（Domain* / IPCIDR / IPPrivate）之间为「或」；
//     其余类别（端口、网络、进程、源地址、规则集）之间、以及与地址类之间为「与」。
//   - RuleSet 由多个 Cond 组成，彼此为「或」。
//   - 含 RuleSet 引用的 Cond 不得再含地址类条件（sing-box 中二者的组合语义取决于规则集内部结构）；
//     Cond.RuleSet 列出多个规则集时，任一命中即可。
package ir

import "time"

// Config 是一份完整的、已合并的路由配置。
type Config struct {
	Nodes    []Node
	Groups   []Group
	Rules    []Rule // 有序；不含 Final
	Final    Action
	RuleSets []RuleSet
	DNS      DNS
	IPv6     bool
	// IPv6Disabled 表示订阅显式写了 ipv6: false（mihomo 默认开启 IPv6）：不使用 IPv6，连向 IPv6 地址的连接直接拒绝。
	IPv6Disabled bool
	Warnings     []Warning
}

// Source 记录配置项的来源，供 Explain 追溯「这条是谁加的」。
type Source struct {
	Kind string `json:"kind"`           // subscription / overlay / velum
	File string `json:"file,omitempty"` // 来源文件（可能为空）
	Line int    `json:"line,omitempty"` // 1-based
	Text string `json:"text,omitempty"` // 原文（不含凭据）
}

const (
	SourceSubscription = "subscription"
	SourceOverlay      = "overlay"
	SourceVelum        = "velum"
)

// ---------- 节点 ----------

type Node struct {
	Tag       string
	Protocol  string // shadowsocks / vmess / vless / trojan / anytls / hysteria / hysteria2 / tuic / socks / http / snell / wireguard / shadowtls
	Server    string
	Port      int
	Ports     []string // 端口跳跃区间 "a:b"
	Detour    string
	TLS       *TLS
	Transport *Transport
	Dial      Dial
	// Options 为协议专有参数，键名统一使用 snake_case（例如 method / password / uuid / flow）。
	Options map[string]any
	Source  Source
}

type TLS struct {
	ServerName  string
	Insecure    bool
	ALPN        []string
	Fingerprint string // uTLS 指纹
	Reality     *Reality
}

type Reality struct{ PublicKey, ShortID string }

type Transport struct {
	Type            string // ws / grpc / http
	Path            string
	Host            []string
	Headers         map[string]string
	ServiceName     string
	Method          string
	MaxEarlyData    int
	EarlyDataHeader string
}

type Dial struct {
	TCPFastOpen    bool
	MPTCP          bool
	BindInterface  string
	DomainStrategy string // prefer_ipv4 / prefer_ipv6 / ipv4_only / ipv6_only
	ConnectTimeout time.Duration
}

// ---------- 策略组 ----------

const (
	GroupSelect  = "select"
	GroupURLTest = "urltest"
)

type Group struct {
	Tag          string
	Type         string // select / urltest
	Members      []string
	URL          string
	Interval     time.Duration
	Tolerance    int
	OriginalType string // 订阅中的原始类型（fallback / load-balance 等降级前的类型）
	Default      string // select 组的默认成员（用户上次的选择）
	Source       Source
}

// ---------- 规则 ----------

// 内置出站标签
const (
	OutboundDirect = "DIRECT"
	OutboundReject = "REJECT"
)

const (
	ActionRoute     = "route"      // 交给 Outbound
	ActionReject    = "reject"     // 拦截（渲染为拦截出站，保证可观测）
	ActionDrop      = "drop"       // 静默丢弃
	ActionSniff     = "sniff"      // 协议嗅探
	ActionResolve   = "resolve"    // 解析域名，供后续 IP 规则匹配
	ActionHijackDNS = "hijack-dns" // 接管 DNS 查询
)

type Action struct {
	Kind            string
	Outbound        string // Kind == route 时有效
	OverrideAddress string // hosts 覆盖：路由时改写目标地址
	// Immediate 用于 Kind == reject：渲染为内核的拒绝动作而不是拦截出站。排在嗅探之前时，TUN 在 TCP 握手前即回绝，
	// 应用得到「连接被拒绝」并换下一个地址，而不是握手成功后被重置。
	Immediate bool
}

type Rule struct {
	Match  Match
	Action Action
	Source Source
}

// Match 要么是叶子（Cond 非空），要么是逻辑组合。空 Match 表示无条件（用于 sniff / resolve 等动作）。
type Match struct {
	Logical  string // "" / and / or
	Invert   bool
	Children []Match
	Cond     *Cond
}

type Cond struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	DomainRegex   []string `json:"domain_regex,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	IPPrivate     bool     `json:"ip_private,omitempty"`
	SourceIPCIDR  []string `json:"source_ip_cidr,omitempty"`
	Port          []int    `json:"port,omitempty"`
	Network       []string `json:"network,omitempty"`
	ProcessName   []string `json:"process_name,omitempty"`
	ProcessPath   []string `json:"process_path,omitempty"`
	RuleSet       []string `json:"rule_set,omitempty"`
	Protocol      []string `json:"protocol,omitempty"` // 嗅探出的协议，如 dns / tls / http / quic
	// NoResolve 为 false 且含 IP 条件时，需要先解析域名再匹配。
	NoResolve bool `json:"no_resolve,omitempty"`
}

// HasIP 报告 Cond 是否含 IP 类条件。
func (c *Cond) HasIP() bool { return c != nil && (len(c.IPCIDR) > 0 || c.IPPrivate) }

// Leaf 构造只含一个 Cond 的 Match。
func Leaf(c Cond) Match { return Match{Cond: &c} }

// ---------- 规则集 ----------

const (
	RuleSetInline = "inline" // 规则内容已在 Rules 中
	RuleSetRemote = "remote" // 需下载（如 GeoIP / GeoSite 的 .srs）；运行前由 Velum 下载并转为 RuleSetFile
	RuleSetFile   = "file"   // 本地文件（Velum 下载缓存）
)

type RuleSet struct {
	Tag          string
	Kind         string
	Rules        []Cond // Kind == inline
	URL          string // Kind == remote：来源；Kind == file 时保留原地址用于展示
	Path         string // Kind == file
	Format       string // Kind == remote / file：binary / source
	NeedsResolve bool   // 含未标 no-resolve 的 IP 条件
	Source       Source
}

// ---------- DNS ----------

type DNS struct {
	Servers  []DNSServer
	Rules    []DNSRule
	Final    string
	Strategy string // "" / ipv4_only / ipv6_only / prefer_ipv4 / prefer_ipv6
	FakeIP   *FakeIP
	// OutboundResolver 用于解析节点服务器域名（对应 Clash proxy-server-nameserver）。
	OutboundResolver string
	// NoSystemHosts 表示订阅显式写了 use-system-hosts: false：不查系统 hosts 文件（mihomo 默认先查）。
	NoSystemHosts bool
}

type DNSServer struct {
	Tag     string
	Type    string // local / udp / tcp / tls / https / quic / h3 / dhcp / fakeip / race / hosts
	Address string
	// Members 是 race 类型的上游（其他 DNSServer 的 Tag）：并发查询，取最先成功的应答（mihomo 多个 nameserver 的语义）。
	Members  []string
	Port     int
	Path     string
	Resolver string // 当 Address 是域名时，用于解析它的 DNS 服务器
	Detour   string
	// RespectRules 为 true 时，连接该服务器按路由规则选择出站（Clash dns.respect-rules），由 daemon 推演后写入 Detour。
	RespectRules bool
	Source       Source
}

type DNSRule struct {
	Match     Match
	QueryType []string
	Server    string
	// Answer 非空时直接应答（hosts 覆盖），不再查询上游。
	Answer []string
	// PreferredBy 非空时只匹配该 DNS 服务器（hosts 类型）有记录的域名，与 Match 同时满足才命中。
	PreferredBy string
	Source      Source
}

type FakeIP struct{ Inet4Range, Inet6Range string }

// ---------- 告警 ----------

const (
	Degraded    = "degraded"
	Unsupported = "unsupported"
)

type Warning struct {
	Category string `json:"category"` // node / provider / group / rule / ruleset / dns / global / overlay
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"` // 节点名 / 组名 / 规则原文等（可能含名称，不含凭据）
	Source   Source `json:"source"`
}
