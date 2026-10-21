// Package compiler 把订阅 IR 与 Overlay 合并编译为 CompiledPlan，
// 保证系统代理豁免 / DNS / 规则引擎（/ TUN 路由，M1.5）各层一致（ARCHITECTURE §4.2）。
//
// 原则：编译器只实现机制（Overlay 展开到各层、协议嗅探、按 Clash 语义插入 resolve），
// 不注入任何路由 / DNS / 豁免策略——策略一律来自订阅与 Overlay。
package compiler

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"go4.org/netipx"
)

// 流量接管方式：Mode 决定是否启用 TUN；系统代理由 Options.SystemProxy 独立控制，两者可同时开启。
const (
	ModeProxy    = "proxy"    // 不启用 TUN
	ModeSysProxy = "sysproxy" // 兼容写法：等同于 ModeProxy + SystemProxy
	ModeTUN      = "tun"      // 启用 TUN
)

// LocalDNS 是编译器注入的系统 DNS 服务器标签。
const LocalDNS = "velum-local"

// SystemHostsDNS 是读取系统 hosts 文件（/etc/hosts 等）的 DNS 服务器标签。
const SystemHostsDNS = "velum-hosts"

type Options struct {
	Mode        string
	SystemProxy bool           // 接管系统代理（可与 TUN 同时开启）
	Listen      netip.AddrPort // 本地 mixed 入站
	Platform    string         // darwin / windows / ios / android
	TUN         TUNOptions     // Mode == tun 时有效
	// Env 是 NetworkSense 感知到的环境（分流型 VPN 等）；为 nil 表示不考虑。
	Env *Environment
	// Selections 是用户为 select 组选择的成员（组 → 成员），引擎重启后保持。
	Selections map[string]string
}

// Environment 是编译器需要的环境输入（由 daemon 从 netsense 结果转换），保持编译器为纯函数。
type Environment struct {
	SplitVPNs []EnvVPN
	// SystemDNSDomains 是系统声明了专用解析器的域名（/etc/resolver、VPN 作用域 DNS 等）。
	// SystemResolvers 给出其中各域名的 DNS 服务器时直接询问它们；没有给出的域名交给系统解析器。
	SystemDNSDomains []string
	SystemResolvers  []EnvResolver
	// PhysicalDNS 是物理网络（DHCP）的 DNS；接管系统 DNS 后，原本用「系统 DNS」的地方改用它，避免绕回 Velum。
	PhysicalDNS []string
}

// SystemResolverDNS 是交给系统解析器（mDNSResponder）的 DNS 服务器标签，只用于系统 DNS 分流的域名。
const SystemResolverDNS = "velum-system"

// EnvResolver 是一个系统 DNS 分流域名及其服务器。
type EnvResolver struct {
	Domain      string
	Nameservers []string
}

type EnvVPN struct {
	Iface       string
	Routes      []netip.Prefix
	Domains     []string
	Nameservers []string
}

// VPNOutbound 返回绑定到某 VPN 接口的直连出站标签。
func VPNOutbound(iface string) string { return "VPN:" + iface }

// TUNOptions 来自用户配置（tun:）。
type TUNOptions struct {
	Address  netip.Prefix   // TUN 地址，如 172.19.0.1/30
	Address6 netip.Prefix   // TUN IPv6 地址（可选），如 fdfe:dcba:9876::1/126
	MTU      uint32         //
	Include  []netip.Prefix // 经 TUN 的网段；为空表示全局（0.0.0.0/1 + 128.0.0.0/1，有 IPv6 地址时另加 ::/1 + 8000::/1）
	Exclude  []netip.Prefix // 不经 TUN 的网段（另合并 overlay 的 dev_bypass.cidrs）
	// DNSHijack 为 true 时把系统 DNS 指向 TUN 内的 DNS 地址（由 helper 执行）。
	DNSHijack bool
	// IncludeApps / ExcludeApps：只经 / 不经 TUN 的应用（Android 包名）。
	IncludeApps, ExcludeApps []string
}

// RoutePlan 描述 TUN 的地址与经 TUN 的路由（由 helper 执行）。
type RoutePlan struct {
	Enabled  bool     `json:"enabled"`
	Address  string   `json:"address"`
	Address6 string   `json:"address6,omitempty"`
	MTU      uint32   `json:"mtu"`
	Routes   []string `json:"routes"`  // 经 TUN 的路由（已扣除排除网段，含 TUN 自身网段）
	Exclude  []string `json:"exclude"` // 排除网段（展示用）
	// SystemDNS 非空时，系统 DNS 被指向这些地址（TUN 内，由 DNS 劫持处理）。
	SystemDNS []string `json:"system_dns,omitempty"`
	// IncludeApps 非空时只有这些应用经 TUN；ExcludeApps 中的应用不经 TUN（Android，由 VpnService 执行）。
	IncludeApps []string `json:"include_apps,omitempty"`
	ExcludeApps []string `json:"exclude_apps,omitempty"`
}

type Exception struct {
	Pattern string    `json:"pattern"`
	Source  ir.Source `json:"source"`
}

// SysProxyPlan 描述系统代理应如何设置（由 sysproxy 包执行）。
type SysProxyPlan struct {
	Enabled    bool        `json:"enabled"`
	Host       string      `json:"host"`
	Port       uint16      `json:"port"`
	Exceptions []Exception `json:"exceptions"`
}

type Plan struct {
	Options  Options
	IR       *ir.Config // 已合并 Overlay 与内置规则，交给渲染器
	SysProxy SysProxyPlan
	Route    RoutePlan
	Warnings []ir.Warning
}

func velum(text string) ir.Source { return ir.Source{Kind: ir.SourceVelum, Text: text} }

func Compile(sub *ir.Config, ov *overlay.Overlay, opts Options) (*Plan, error) {
	if sub == nil {
		sub = &ir.Config{Final: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}}
	}
	if ov == nil {
		ov = &overlay.Overlay{}
	}
	if opts.Mode == "" {
		opts.Mode = ModeProxy
	}
	if opts.Mode == ModeSysProxy {
		opts.Mode, opts.SystemProxy = ModeProxy, true
	}
	cfg := *sub
	cfg.Rules = nil
	cfg.DNS.Servers = append([]ir.DNSServer(nil), sub.DNS.Servers...)
	cfg.DNS.Rules = nil
	cfg.Warnings = append([]ir.Warning(nil), sub.Warnings...)
	cfg.Groups = append([]ir.Group(nil), sub.Groups...)
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		if sel, ok := opts.Selections[g.Tag]; ok && g.Type == ir.GroupSelect {
			for _, m := range g.Members {
				if m == sel {
					g.Default = sel
				}
			}
		}
	}
	p := &Plan{Options: opts, IR: &cfg}

	warn := func(kind, level, reason, subject string, src ir.Source) {
		cfg.Warnings = append(cfg.Warnings, ir.Warning{Category: "overlay", Kind: kind, Level: level, Reason: reason, Subject: subject, Source: src})
	}
	ovSrc := func(line int, text string) ir.Source {
		return ir.Source{Kind: ir.SourceOverlay, File: ov.File, Line: line, Text: text}
	}

	// ---- DNS：注入系统 DNS，供本地开发域名与兜底使用 ----
	cfg.DNS.Servers = append(cfg.DNS.Servers, ir.DNSServer{Tag: LocalDNS, Type: "local", Source: velum("系统 DNS")})
	if cfg.DNS.Final == "" {
		cfg.DNS.Final = LocalDNS
	}
	if cfg.DNS.OutboundResolver == "" {
		cfg.DNS.OutboundResolver = firstRealDNS(&cfg.DNS)
	}
	if opts.Mode != ModeTUN && cfg.DNS.FakeIP != nil {
		// 系统代理模式下应用使用系统 DNS，fake-ip 无意义；且会让内核解析节点域名时拿到假地址。
		cfg.DNS.FakeIP = nil
	}

	// ---- 规则：内置 + Overlay 优先于订阅 ----
	var rules []ir.Rule
	rules = append(rules, ir.Rule{Action: ir.Action{Kind: ir.ActionSniff}, Source: velum("协议嗅探")})
	if opts.Mode == ModeTUN {
		rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{Protocol: []string{"dns"}}), Action: ir.Action{Kind: ir.ActionHijackDNS}, Source: velum("接管 DNS 查询")})
		if a := opts.TUN.Address; a.IsValid() {
			// 发往 TUN 内 DNS 地址的其他连接（如 Android「私人 DNS」对 DNS 服务器的 DoT 探测，853 端口）没有服务可接，
			// 立即拒绝，让系统马上回落到普通 DNS，而不是经出站等待超时
			rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{IPCIDR: []string{netip.PrefixFrom(a.Addr().Next(), 32).String()}, NoResolve: true}),
				Action: ir.Action{Kind: ir.ActionReject}, Source: velum("TUN 内 DNS 地址只接受 DNS 查询")})
		}
	}
	direct := ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect}

	hostDomains := map[string]bool{}
	for _, h := range ov.Hosts {
		hostDomains[h.Domain] = true
		src := ovSrc(h.Line, fmt.Sprintf("hosts: %s → %s", h.Domain, h.IP))
		rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{Domain: []string{h.Domain}}),
			Action: ir.Action{Kind: ir.ActionRoute, Outbound: ir.OutboundDirect, OverrideAddress: h.IP.String()}, Source: src})
		rr := "A"
		if h.IP.Is6() {
			rr = "AAAA"
		}
		cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{Match: ir.Leaf(ir.Cond{Domain: []string{h.Domain}}),
			Answer: []string{fmt.Sprintf("%s. IN %s %s", h.Domain, rr, h.IP)}, Source: src})
	}
	if !cfg.DNS.NoSystemHosts {
		// 系统 hosts 文件里有记录的域名按文件应答（Clash use-system-hosts，mihomo 默认开启），排在 Overlay hosts 之后、
		// 其他 DNS 规则之前：否则只写在 hosts 里的本地开发域名会被送到上游（如 VPN 作用域 DNS）而解析失败。文件修改后自动生效。
		src := velum("系统 hosts 文件")
		cfg.DNS.Servers = append(cfg.DNS.Servers, ir.DNSServer{Tag: SystemHostsDNS, Type: "hosts", Source: src})
		cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{PreferredBy: SystemHostsDNS, Server: SystemHostsDNS, Source: src})
	}
	for _, d := range ov.Domains {
		name, kind := overlay.ParseDomain(d.Value)
		if name == "" {
			continue
		}
		src := ovSrc(d.Line, "domains: "+d.Value)
		var c ir.Cond
		switch kind {
		case overlay.DomainExact:
			c.Domain = []string{name}
		case overlay.DomainSubOnly:
			c.DomainSuffix = []string{"." + name}
		default:
			c.DomainSuffix = []string{name}
		}
		rules = append(rules, ir.Rule{Match: ir.Leaf(c), Action: direct, Source: src})
		cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{Match: ir.Leaf(c), Server: LocalDNS, Source: src})
		for _, pat := range sysProxyPatterns(name, kind) {
			if h := coveredHost(pat, hostDomains); h != "" {
				// 豁免会让浏览器绕过 Velum 直连并使用系统 DNS，hosts 覆盖随之失效（ARCHITECTURE §4.2）。
				warn("domains", ir.Degraded, fmt.Sprintf("豁免写法 %s 会覆盖 hosts 主机 %s，已不写入系统代理豁免；这些域名改为经 Velum 代理端口直连", pat, h), d.Value, src)
				continue
			}
			p.SysProxy.Exceptions = append(p.SysProxy.Exceptions, Exception{pat, src})
		}
	}
	if len(ov.CIDRs) > 0 {
		for _, cidr := range ov.CIDRs {
			src := ovSrc(cidr.Line, "cidrs: "+cidr.Value)
			rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{IPCIDR: []string{cidr.Value}, NoResolve: true}), Action: direct, Source: src})
			p.SysProxy.Exceptions = append(p.SysProxy.Exceptions, Exception{cidr.Value, src})
		}
	}
	for _, pr := range ov.Processes {
		src := ovSrc(pr.Line, "processes: "+pr.Value)
		if opts.Platform == "ios" {
			warn("processes", ir.Unsupported, "iOS 不支持进程规则，已忽略", pr.Value, src)
			continue
		}
		rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{ProcessName: []string{pr.Value}}), Action: direct, Source: src})
	}

	// ---- NetworkSense：分流型 VPN 的网段与域名走该 VPN（优先于订阅）----
	var envExclude []netip.Prefix
	if opts.Env != nil {
		ifaceOutbounds := map[string]bool{} // 已创建的绑定网卡直连出站
		// 系统 DNS 分流的域名若由某个 VPN 网段内的服务器解析（如 VPN 客户端写入 /etc/resolver），这些域名也属于该 VPN：
		// 否则它们会按订阅规则（常见为 DIRECT）从物理网卡直连到只能经 VPN 访问的内网地址。
		vpnDomains := map[string][]string{}
		for _, r := range opts.Env.SystemResolvers {
			if v := vpnServing(opts.Env.SplitVPNs, r.Nameservers); v != "" {
				vpnDomains[v] = append(vpnDomains[v], r.Domain)
			}
		}
		for _, v := range opts.Env.SplitVPNs {
			for _, d := range vpnDomains[v.Iface] {
				if !slices.Contains(v.Domains, d) {
					v.Domains = append(v.Domains, d)
				}
			}
			src := velum(fmt.Sprintf("NetworkSense：检测到 VPN %s", v.Iface))
			ob := VPNOutbound(v.Iface)
			// 绑定到 VPN 接口的直连出站：TUN 模式下普通 DIRECT 会绑定物理网卡，导致内网流量走错接口
			cfg.Nodes = append(cfg.Nodes, ir.Node{Tag: ob, Protocol: "direct", Dial: ir.Dial{BindInterface: v.Iface}, Source: src})
			ifaceOutbounds[ob] = true
			route := ir.Action{Kind: ir.ActionRoute, Outbound: ob}
			if len(v.Routes) > 0 {
				var cidrs []string
				for _, pf := range v.Routes {
					cidrs = append(cidrs, pf.String())
					p.SysProxy.Exceptions = append(p.SysProxy.Exceptions, Exception{pf.String(), src})
				}
				rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{IPCIDR: cidrs, NoResolve: true}), Action: route, Source: src})
				envExclude = append(envExclude, v.Routes...)
			}
			if len(v.Domains) > 0 {
				c := ir.Cond{DomainSuffix: append([]string(nil), v.Domains...), NoResolve: true}
				rules = append(rules, ir.Rule{Match: ir.Leaf(c), Action: route, Source: src})
				for _, d := range v.Domains {
					p.SysProxy.Exceptions = append(p.SysProxy.Exceptions, Exception{d, src}, Exception{"*." + d, src})
				}
				// macOS：VPN 域名的解析由下面的系统 DNS 分流统一交给系统解析器（含该 VPN 的作用域 DNS 与回落）。
				// Windows：没有按域名分流的作用域 DNS（除 NRPT 外），系统解析器可能把查询发给 TUN 的 DNS 而绕回 Velum，
				// 因此直接经该 VPN 询问它的 DNS 服务器。
				if opts.Platform == "windows" && len(v.Nameservers) > 0 {
					tag := ob + "-dns"
					cfg.DNS.Servers = append(cfg.DNS.Servers, ir.DNSServer{Tag: tag, Type: "udp", Address: v.Nameservers[0], Detour: ob, Source: src})
					cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{Match: ir.Leaf(ir.Cond{DomainSuffix: append([]string(nil), v.Domains...), NoResolve: true}),
						Server: tag, Source: src})
				}
			}
		}
		systemDNSSplit(&cfg, opts, func(iface string) string {
			ob := VPNOutbound(iface)
			if !ifaceOutbounds[ob] {
				ifaceOutbounds[ob] = true
				cfg.Nodes = append(cfg.Nodes, ir.Node{Tag: ob, Protocol: "direct", Dial: ir.Dial{BindInterface: iface}, Source: velum("NetworkSense：经 " + iface + " 直连")})
			}
			return ob
		})
	}

	// ---- 订阅 ipv6: false（TUN）：拒绝连向 IPv6 地址的连接，应用随即回落到 IPv4 ----
	// 系统 DNS 被其他工具（如企业 VPN 客户端）抢先应答时，应用会拿到真实的 IPv6 地址；经不支持 IPv6 的节点转发只会卡住。
	// 放在 Overlay 与 VPN 规则之后：显式的直连 / VPN 网段不受影响。
	if opts.Mode == ModeTUN && sub.IPv6Disabled {
		rules = append(rules, ir.Rule{Match: ir.Leaf(ir.Cond{IPCIDR: []string{"::/0"}, NoResolve: true}), Action: ir.Action{Kind: ir.ActionReject},
			Source: ir.Source{Kind: ir.SourceSubscription, Text: "ipv6: false（拒绝 IPv6 连接，应用回落到 IPv4）"}})
	}

	// ---- 订阅规则，并在首条需要解析的规则前插入 resolve ----
	sets := map[string]ir.RuleSet{}
	for _, rs := range cfg.RuleSets {
		sets[rs.Tag] = rs
	}
	resolved := false
	for _, r := range sub.Rules {
		if !resolved && NeedsResolve(r.Match, sets) {
			rules = append(rules, ir.Rule{Action: ir.Action{Kind: ir.ActionResolve},
				Source: velum("解析域名（模拟 Clash 对未标 no-resolve 的 IP 规则按需解析）")})
			resolved = true
		}
		rules = append(rules, r)
	}
	cfg.Rules = rules
	cfg.DNS.Rules = append(cfg.DNS.Rules, sub.DNS.Rules...)
	if cfg.DNS.FakeIP == nil {
		cfg.DNS.Rules = dropServerRules(cfg.DNS.Rules, "fakeip")
	}

	// ---- 系统代理 ----
	p.SysProxy.Enabled = opts.SystemProxy
	p.SysProxy.Host = opts.Listen.Addr().String()
	p.SysProxy.Port = opts.Listen.Port()
	// 用户显式配置的豁免（system_proxy.bypass）排在前面；Velum 不内置任何豁免条目。
	var user []Exception
	for _, b := range ov.SysProxyBypass {
		src := ovSrc(b.Line, "system_proxy.bypass: "+b.Value)
		if h := coveredHost(b.Value, hostDomains); h != "" {
			warn("system_proxy.bypass", ir.Degraded, fmt.Sprintf("豁免写法 %s 会覆盖 hosts 主机 %s，已不写入系统代理豁免", b.Value, h), b.Value, src)
			continue
		}
		user = append(user, Exception{b.Value, src})
	}
	p.SysProxy.Exceptions = append(user, p.SysProxy.Exceptions...)
	var phys []string
	if opts.Env != nil {
		phys = opts.Env.PhysicalDNS
	}
	// usePhys 把经系统解析器（local）的解析改为直接询问物理网络的 DNS
	usePhys := func(why string) {
		for i := range cfg.DNS.Servers {
			if sv := &cfg.DNS.Servers[i]; sv.Type == "local" && sv.Tag != SystemResolverDNS {
				sv.Type, sv.Address = "udp", phys[0]
				sv.Source.Text += "（" + why + "，改用物理网络 DNS " + phys[0] + "）"
			}
		}
	}
	if opts.Platform == "android" {
		// Android 没有 /etc/resolv.conf，内核的系统解析器不可用：始终使用平台层报告的当前网络 DNS
		if len(phys) == 0 {
			warn("dns", ir.Degraded, "未获取到当前网络的 DNS，域名解析可能失败", "", velum("系统 DNS"))
		} else {
			usePhys("Android 上由系统报告当前网络的 DNS")
		}
	}
	if opts.Mode == ModeTUN {
		var extra []netip.Prefix
		for _, c := range ov.CIDRs {
			if pf, err := netip.ParsePrefix(c.Value); err == nil {
				extra = append(extra, pf)
			}
		}
		rp, err := BuildRoutePlan(opts.TUN, append(extra, envExclude...))
		if err != nil {
			return nil, err
		}
		if len(opts.TUN.IncludeApps)+len(opts.TUN.ExcludeApps) > 0 {
			if opts.Platform == "android" {
				rp.IncludeApps, rp.ExcludeApps = opts.TUN.IncludeApps, opts.TUN.ExcludeApps
				if len(rp.IncludeApps) > 0 && len(rp.ExcludeApps) > 0 {
					warn("tun.exclude_apps", ir.Degraded, "同时设置了 include_apps 与 exclude_apps：只有 include_apps 生效", "", velum("TUN"))
				}
			} else {
				warn("tun.exclude_apps", ir.Degraded, "按应用分流（include_apps / exclude_apps）只在 Android 上生效", "", velum("TUN"))
			}
		}
		if opts.TUN.DNSHijack {
			if len(phys) == 0 {
				warn("tun.dns_hijack", ir.Degraded, "未获取到物理网络的 DNS，暂不接管系统 DNS（接管后需要它来避免解析绕回 Velum）", "", velum("TUN"))
			} else {
				rp.SystemDNS = []string{opts.TUN.Address.Addr().Next().String()}
				// 系统默认 DNS 将指向 Velum：原本经系统解析器（local）的解析改为直接询问物理网络的 DNS，避免回环
				usePhys("系统 DNS 已由 Velum 接管")
			}
		}
		p.Route = rp
	}
	p.Warnings = cfg.Warnings
	return p, nil
}

// vpnServing 返回网段包含某个（非回环）服务器地址的分流 VPN 网卡名；没有则为空。
func vpnServing(vpns []EnvVPN, nameservers []string) string {
	for _, ns := range nameservers {
		a, err := netip.ParseAddr(ns)
		if err != nil || a.IsLoopback() {
			continue
		}
		for _, v := range vpns {
			for _, pf := range v.Routes {
				if pf.Contains(a) {
					return v.Iface
				}
			}
		}
	}
	return ""
}

// systemDNSSplit 处理系统 DNS 分流（/etc/resolver、VPN 下发的按域名 DNS、Windows NRPT）。
//
// 直接询问为这些域名配置的服务器，而不是交给 sing-box 的系统解析器：后者在 macOS 上只对 .local 使用 mDNSResponder，
// 其余域名按默认解析器查询；接管系统 DNS（TUN）后默认解析器就是 Velum 自己，会无限回环。
//   - 服务器在某个分流 VPN 的网段内：经该 VPN 网卡发出（Velum 的出站默认绑定物理网卡）；
//   - 回环地址（VPN 客户端的本地 DNS 代理，它可能把查询转发给系统默认 DNS 即 Velum）：还有其他服务器时跳过，
//     只有回环地址时经回环网卡发出（macOS）；
//   - 多个服务器并发竞速（与系统按序回落相比，任何一个可达即可应答）。
//
// 没有服务器信息的域名仍交给系统解析器。ifaceOutbound 返回绑定到某网卡的直连出站（按需创建）。
func systemDNSSplit(cfg *ir.Config, opts Options, ifaceOutbound func(iface string) string) {
	known := map[string]bool{}
	groups := map[string][]string{} // 服务器组（逗号连接）→ 域名
	var order []string
	for _, r := range opts.Env.SystemResolvers {
		var addrs, loop []netip.Addr
		for _, ns := range r.Nameservers {
			a, err := netip.ParseAddr(ns)
			if err != nil {
				continue
			}
			if a.IsLoopback() {
				loop = append(loop, a)
			} else {
				addrs = append(addrs, a)
			}
		}
		if len(addrs) == 0 {
			addrs = loop
		}
		if len(addrs) == 0 {
			continue
		}
		known[r.Domain] = true
		var ks []string
		for _, a := range addrs {
			ks = append(ks, a.String())
		}
		k := strings.Join(ks, ",")
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r.Domain)
	}
	src := velum("NetworkSense：系统 DNS 分流（/etc/resolver、VPN 作用域 DNS 等）")
	for gi, k := range order {
		var members []string
		for ni, ns := range strings.Split(k, ",") {
			a := netip.MustParseAddr(ns)
			sv := ir.DNSServer{Tag: fmt.Sprintf("%s-%d-%d", SystemResolverDNS, gi, ni), Type: "udp", Address: ns, Source: src}
			switch {
			case a.IsLoopback():
				if opts.Platform == "darwin" {
					sv.Detour = ifaceOutbound("lo0")
				}
			default:
				for _, v := range opts.Env.SplitVPNs {
					for _, pf := range v.Routes {
						if pf.Contains(a) {
							sv.Detour = VPNOutbound(v.Iface)
						}
					}
				}
			}
			cfg.DNS.Servers = append(cfg.DNS.Servers, sv)
			members = append(members, sv.Tag)
		}
		tag := members[0]
		if len(members) > 1 {
			tag = fmt.Sprintf("%s-%d", SystemResolverDNS, gi)
			cfg.DNS.Servers = append(cfg.DNS.Servers, ir.DNSServer{Tag: tag, Type: "race", Members: members, Source: src})
		}
		c := ir.Cond{DomainSuffix: groups[k], NoResolve: true}
		cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{Match: ir.Leaf(c), Server: tag, Source: src})
	}
	var rest []string
	for _, d := range opts.Env.SystemDNSDomains {
		if !known[d] {
			rest = append(rest, d)
		}
	}
	if len(rest) > 0 {
		cfg.DNS.Servers = append(cfg.DNS.Servers, ir.DNSServer{Tag: SystemResolverDNS, Type: "local", Source: velum("系统解析器")})
		cfg.DNS.Rules = append(cfg.DNS.Rules, ir.DNSRule{Match: ir.Leaf(ir.Cond{DomainSuffix: rest, NoResolve: true}), Server: SystemResolverDNS, Source: src})
	}
}

// BuildRoutePlan 计算经 TUN 的路由（IPv4，有 IPv6 地址时含 IPv6）：包含网段 − 排除网段，再加上 TUN 自身网段
// （外部 FD 模式下 sing-tun 不设路由，system 栈 NAT 回包依赖该路由，见 M0 F2）。
// 全局接管用 0.0.0.0/1 + 128.0.0.0/1，不替换默认路由。
func BuildRoutePlan(t TUNOptions, extraExclude []netip.Prefix) (RoutePlan, error) {
	if !t.Address.IsValid() || !t.Address.Addr().Is4() {
		return RoutePlan{}, fmt.Errorf("tun.address 无效：%v", t.Address)
	}
	if t.Address6.IsValid() && !t.Address6.Addr().Is6() {
		return RoutePlan{}, fmt.Errorf("tun.address6 无效：%v", t.Address6)
	}
	include := t.Include
	if len(include) == 0 {
		include = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
		if t.Address6.IsValid() {
			include = append(include, netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"))
		}
	}
	var b netipx.IPSetBuilder
	for _, p := range include {
		if p.Addr().Is4() || t.Address6.IsValid() { // 没有 IPv6 地址时无法承载 IPv6 路由
			b.AddPrefix(p.Masked())
		}
	}
	exclude := append(append([]netip.Prefix(nil), t.Exclude...), extraExclude...)
	for _, p := range exclude {
		b.RemovePrefix(p.Masked())
	}
	b.AddPrefix(t.Address.Masked())
	if t.Address6.IsValid() {
		b.AddPrefix(t.Address6.Masked())
	}
	set, err := b.IPSet()
	if err != nil {
		return RoutePlan{}, err
	}
	rp := RoutePlan{Enabled: true, Address: t.Address.String(), MTU: t.MTU}
	if t.Address6.IsValid() {
		rp.Address6 = t.Address6.String()
	}
	if rp.MTU == 0 {
		rp.MTU = 9000
	}
	for _, p := range set.Prefixes() {
		// 没有排除网段时集合会把两个 /1 合并成 /0：拆回去，始终不替换默认路由（helper 也拒绝 /0）
		if p.Bits() == 0 {
			if p.Addr().Is4() {
				rp.Routes = append(rp.Routes, "0.0.0.0/1", "128.0.0.0/1")
			} else {
				rp.Routes = append(rp.Routes, "::/1", "8000::/1")
			}
			continue
		}
		rp.Routes = append(rp.Routes, p.String())
	}
	for _, p := range exclude {
		rp.Exclude = append(rp.Exclude, p.Masked().String())
	}
	return rp, nil
}

// NeedsResolve 报告 Match 中是否有需要先解析域名才能匹配的 IP 条件。
func NeedsResolve(m ir.Match, sets map[string]ir.RuleSet) bool {
	if m.Cond != nil {
		c := m.Cond
		if c.NoResolve {
			return false
		}
		if c.HasIP() {
			return true
		}
		for _, t := range c.RuleSet {
			if sets[t].NeedsResolve {
				return true
			}
		}
		return false
	}
	for _, ch := range m.Children {
		if NeedsResolve(ch, sets) {
			return true
		}
	}
	return false
}

func firstRealDNS(d *ir.DNS) string {
	for _, s := range d.Servers {
		if s.Type != "fakeip" && !strings.HasPrefix(s.Tag, "bootstrap-") {
			return s.Tag
		}
	}
	return LocalDNS
}

func dropServerRules(rules []ir.DNSRule, server string) []ir.DNSRule {
	var out []ir.DNSRule
	for _, r := range rules {
		if r.Server != server {
			out = append(out, r)
		}
	}
	return out
}

// sysProxyPatterns 把 overlay 域名语义翻译为系统代理豁免写法（macOS / Windows 均支持 *.x）。
func sysProxyPatterns(name string, kind overlay.DomainKind) []string {
	switch kind {
	case overlay.DomainExact:
		return []string{name}
	case overlay.DomainSubOnly:
		return []string{"*." + name}
	}
	return []string{name, "*." + name}
}

// coveredHost 返回被该豁免写法覆盖到的某个 hosts 主机（没有则为空）。
func coveredHost(pattern string, hosts map[string]bool) string {
	for h := range hosts {
		if strings.HasPrefix(pattern, "*.") {
			if strings.HasSuffix(h, pattern[1:]) {
				return h
			}
		} else if h == pattern {
			return h
		}
	}
	return ""
}
