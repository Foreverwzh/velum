// Package config 管理 velumd 的配置与目录（$VELUM_HOME，默认 macOS ~/Library/Application Support/Velum、Windows %APPDATA%\Velum）。
//
// 原则：默认值在首次运行时写入用户可见、可编辑的 config.yaml，代码中不隐藏策略。
package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/subscription"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
	"gopkg.in/yaml.v3"
)

type Subscription struct {
	URL            string        `yaml:"url"`
	File           string        `yaml:"file"`
	UserAgent      string        `yaml:"user_agent"`
	UpdateInterval time.Duration `yaml:"update_interval"`
	// ProviderDirs 是 rule/proxy-provider 相对路径的基准目录（导入本地 Clash / Stash 配置时使用）。
	ProviderDirs []string `yaml:"provider_dirs"`
	// PreferLocalProviders 为 true 时，provider 的本地文件存在就直接使用，不再按 url 拉取。
	PreferLocalProviders bool   `yaml:"prefer_local_providers"`
	GeoRuleSetURL        string `yaml:"geo_ruleset_url"`
	// RuleSetUpdateInterval 是 GEOIP / GEOSITE 等远程规则集的更新间隔。
	RuleSetUpdateInterval time.Duration `yaml:"ruleset_update_interval"`
}

type TUN struct {
	Enabled      bool     `yaml:"enabled"`
	Address      string   `yaml:"address"`
	Address6     string   `yaml:"address6"`
	DNSHijack    bool     `yaml:"dns_hijack"`
	MTU          uint32   `yaml:"mtu"`
	RouteInclude []string `yaml:"route_include"`
	RouteExclude []string `yaml:"route_exclude"`
	HelperSocket string   `yaml:"helper_socket"`
	IncludeApps  []string `yaml:"include_apps"`
	ExcludeApps  []string `yaml:"exclude_apps"`
}

type NetworkSense struct {
	Enabled        bool          `yaml:"enabled"`
	Interval       time.Duration `yaml:"interval"`
	VPNCoexist     bool          `yaml:"vpn_coexist"`
	SystemSplitDNS bool          `yaml:"system_split_dns"`
	YieldToFullVPN bool          `yaml:"yield_to_full_vpn"`
	CaptivePortal  bool          `yaml:"captive_portal"`
	CaptiveProbes  []string      `yaml:"captive_probes"`
}

type Config struct {
	Listen       string       `yaml:"listen"`
	SystemProxy  bool         `yaml:"system_proxy"`
	NetworkSense NetworkSense `yaml:"network_sense"`
	TUN          TUN          `yaml:"tun"`
	Mode         string       `yaml:"mode"`
	LogLevel     string       `yaml:"log_level"`
	Subscription Subscription `yaml:"subscription"`
	Overlay      string       `yaml:"overlay"`
}

// Template 是首次运行写入的配置（带注释）。修改默认值请改这里，保证用户能看到。
var Template = `# Velum 配置。首次运行时生成，可随时编辑；修改后执行 vlu reload 生效。

# 本地代理入口（HTTP + SOCKS5 mixed）
listen: 127.0.0.1:7899

# 系统代理：遵守系统代理设置的应用（浏览器等）经 Velum；退出时还原
system_proxy: false

# TUN：经虚拟网卡接管所有应用的流量（macOS 需要 velum-helper，Windows 需要以服务运行 velumd，Android 为系统 VPN）；可与系统代理同时开启
tun:
  enabled: false
  # TUN 地址：仅本机使用的私有网段，需与现有网络不冲突
  address: 172.19.0.1/30
  # TUN IPv6 地址（私有 ULA）；留空则不经 TUN 处理 IPv6
  address6: fdfe:dcba:9876::1/126
  mtu: 9000
  # 把系统 DNS 指向 TUN（由 helper 设置、退出时还原），使所有应用的 DNS 都经 Velum 分流
  dns_hijack: true
  # 经 TUN 的目标网段；为空表示全局接管（0.0.0.0/1 + 128.0.0.0/1，不替换默认路由）
  route_include: []
  # 不经 TUN 的网段（另会合并 overlay 的 dev_bypass.cidrs）
  route_exclude: []
  helper_socket: /var/run/velum-helper.sock
  # Android：只经 TUN 的应用（包名）；为空表示所有应用
  include_apps: []
  # Android：不经 TUN 的应用（包名），如银行类应用；设置了 include_apps 时忽略
  exclude_apps: []

# 内核日志级别：trace / debug / info / warn / error
# info 会逐条记录每个连接（量大）；连接明细可随时用 vlu conns / vlu why --live 查看
log_level: warn

subscription:
  # 订阅地址（Clash / Stash / mihomo YAML），与 file 二选一
  url: ""
  # 或本地订阅文件
  file: ""
  # 拉取订阅时使用的 User-Agent（部分订阅服务按 UA 决定输出格式）
  user_agent: clash.meta
  update_interval: 12h
  # rule-provider / proxy-provider 相对路径的基准目录（导入本地 Clash / Stash 配置时填写）
  provider_dirs: []
  # provider 的本地文件存在时直接使用，不按 url 拉取
  prefer_local_providers: false
  # GEOIP / GEOSITE 规则集下载地址模板；{kind} = geoip / geosite，{code} = 代码（如 cn）
  geo_ruleset_url: ` + subscription.DefaultGeoRuleSetURL + `
  # 远程规则集（GEOIP / GEOSITE）由 Velum 下载并缓存（遵循 HTTPS_PROXY 环境变量），更新间隔：
  ruleset_update_interval: 24h

# 本地覆盖层（开发直连、hosts 覆盖、系统代理豁免），相对路径基于 Velum 目录
overlay: overlay.yaml

# 网络环境感知
network_sense:
  enabled: true
  interval: 5s
  # 检测到分流型 VPN（只接管部分网段）时：其网段与域名经该 VPN 直连、域名交给其 DNS，并从 TUN 与系统代理中排除
  vpn_coexist: true
  # 遵守系统 DNS 分流：/etc/resolver、VPN 下发的作用域 DNS 等声明的域名，交给系统解析器解析（与系统行为一致）
  system_split_dns: true
  # 检测到其他工具全局接管（占据主路由）时，暂停 Velum 的 TUN
  yield_to_full_vpn: true
  # 连上需要网页认证的网络时暂停接管，认证完成后自动恢复
  captive_portal: true
  # 探测地址："URL|期望正文"；不写 | 表示期望 HTTP 204
  captive_probes:
    - "http://captive.apple.com/hotspot-detect.html|Success"
    - "http://connectivitycheck.gstatic.com/generate_204"
`

// OverlayTemplate 是首次运行写入的 overlay.yaml（只有注释，不含任何条目）。
var OverlayTemplate = `# Velum 本地覆盖层：优先于订阅生效，并同步到系统代理豁免 / DNS / 规则各层。
# 修改后执行 vlu reload 生效。
#
# dev_bypass:
#   domains:             # "x" 根域 + 子域；".x" 仅子域；"=x" 精确
#     - corp.example.com
#   cidrs:
#     - 10.0.0.0/8
#   hosts:               # 域名覆盖（不修改 /etc/hosts）
#     staging.corp.example.com: 10.1.2.3
#   processes:           # 进程名
#     - SomeVPNClient
#
# system_proxy:
#   bypass:              # 系统代理豁免，原样写入系统设置
#     - localhost
#     - 127.0.0.1
`

// Home 返回 Velum 目录：$VELUM_HOME 优先。
func Home() (string, error) {
	if h := os.Getenv("VELUM_HOME"); h != "" {
		return filepath.Abs(h)
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "Velum"), nil
}

type Paths struct {
	// Home 是用户的 Velum 目录：config.yaml、overlay.yaml 等用户可编辑的文件（经 userfs 以用户身份读写）。
	Home, Config, Overlay string
	// Profiles 是订阅列表（profiles.yaml）；当前使用的订阅写在 config.yaml 的 subscription 中。
	Profiles string
	// Socket 是本地接口地址；ServiceSocket（Windows）是 Velum 服务的 Pipe，客户端在 Socket 不存在时回落到它。
	Socket, ServiceSocket string
	// Data 是 Velum 自己维护的数据目录：日志、缓存、状态、快照。默认与 Home 相同；Windows 服务（SYSTEM）
	// 使用 %ProgramData%\Velum（只有 SYSTEM / 管理员可写），不在用户可写的目录中以 SYSTEM 写文件。
	Data, Log, Cache string
	SysProxySnapshot string // 接管系统代理前的快照（存在即表示处于接管状态）
	State            string // 运行状态（如策略组选择），由 Velum 维护
}

func PathsFor(home string) Paths {
	p := Paths{
		Home:     home,
		Config:   filepath.Join(home, "config.yaml"),
		Overlay:  filepath.Join(home, "overlay.yaml"),
		Profiles: filepath.Join(home, "profiles.yaml"),
	}
	p.Socket, p.ServiceSocket = socketPaths(home)
	return p.WithData(home)
}

// WithData 把 Velum 维护的数据放到 dir。
func (p Paths) WithData(dir string) Paths {
	p.Data = dir
	p.Log = filepath.Join(dir, "logs", "velumd.log")
	p.Cache = filepath.Join(dir, "cache")
	p.SysProxySnapshot = filepath.Join(dir, "sysproxy.snapshot.json")
	p.State = filepath.Join(dir, "state.json")
	return p
}

// ClientAddrs 是客户端依次尝试的本地接口地址。
func (p Paths) ClientAddrs() []string {
	if p.ServiceSocket != "" && p.ServiceSocket != p.Socket {
		return []string{p.Socket, p.ServiceSocket}
	}
	return []string{p.Socket}
}

// ServicePipe 是 Windows 上 Velum 服务（全机一个）的 Named Pipe；以用户身份运行的 velumd 使用 UserPipe。
const ServicePipe = `\\.\pipe\velumd`

// UserPipe 是以用户 sid 身份运行的 velumd 的 Named Pipe：按用户区分，同一台机器上的多个用户互不冲突。
func UserPipe(sid string) string { return ServicePipe + "-" + sid }

// Ensure 创建目录，并在缺失时写入带注释的模板。
func Ensure(p Paths) error {
	if err := userfs.MkdirAll(p.Home, 0o700); err != nil {
		return err
	}
	for _, d := range []string{p.Data, filepath.Dir(p.Log), p.Cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	for path, content := range map[string]string{p.Config: Template, p.Overlay: OverlayTemplate} {
		if _, err := userfs.Stat(path); os.IsNotExist(err) {
			if err := userfs.WriteFile(path, []byte(content), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func Load(p Paths) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal([]byte(Template), &c); err != nil { // 模板即默认值
		return nil, err
	}
	data, err := userfs.ReadFile(p.Config)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Config, err)
	}
	if _, err := netip.ParseAddrPort(c.Listen); err != nil {
		return nil, fmt.Errorf("listen 无效：%q", c.Listen)
	}
	switch c.Mode { // 旧写法 mode: 仍然兼容
	case "", "proxy", "sysproxy", "tun":
	default:
		return nil, fmt.Errorf("mode 无效：%q（已改为 system_proxy / tun.enabled 两个开关）", c.Mode)
	}
	if c.Overlay != "" && !filepath.IsAbs(c.Overlay) {
		c.Overlay = filepath.Join(p.Home, c.Overlay)
	}
	if c.Subscription.File != "" && !filepath.IsAbs(c.Subscription.File) {
		c.Subscription.File = filepath.Join(p.Home, c.Subscription.File)
	}
	return &c, nil
}

func (c *Config) ListenAddr() netip.AddrPort { return netip.MustParseAddrPort(c.Listen) }

// TUNOptions 解析 tun: 配置段。
func (c *Config) TUNOptions() (addr, addr6 netip.Prefix, mtu uint32, include, exclude []netip.Prefix, err error) {
	if addr, err = netip.ParsePrefix(c.TUN.Address); err != nil {
		return addr, addr6, 0, nil, nil, fmt.Errorf("tun.address 无效：%q", c.TUN.Address)
	}
	if c.TUN.Address6 != "" {
		if addr6, err = netip.ParsePrefix(c.TUN.Address6); err != nil || !addr6.Addr().Is6() {
			return addr, addr6, 0, nil, nil, fmt.Errorf("tun.address6 无效：%q", c.TUN.Address6)
		}
	}
	parse := func(name string, ss []string) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, s := range ss {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				a, err2 := netip.ParseAddr(s)
				if err2 != nil {
					return nil, fmt.Errorf("%s 中的网段无效：%q", name, s)
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			out = append(out, p)
		}
		return out, nil
	}
	if include, err = parse("tun.route_include", c.TUN.RouteInclude); err != nil {
		return
	}
	exclude, err = parse("tun.route_exclude", c.TUN.RouteExclude)
	return addr, addr6, c.TUN.MTU, include, exclude, err
}

// WantSystemProxy / WantTUN 是配置文件中的开关（兼容旧的 mode: 写法）。
func (c *Config) WantSystemProxy() bool { return c.SystemProxy || c.Mode == "sysproxy" }
func (c *Config) WantTUN() bool         { return c.TUN.Enabled || c.Mode == "tun" }
