# M1.5 实施计划：macOS TUN + 特权 helper + NetworkSense

> 对应 ARCHITECTURE §3.2 / §4.3 / §7 M1.5。M0 原型见 `spikes/m0-utun`。

## 已对齐的约束（2026-10-16）

1. **签名**：免费开发者账号、无签名证书，暂不使用 SMAppService。开发阶段与 M0 一样，由用户执行 `sudo velum-helper` 手动启动；正式注册与调用方代码签名校验放到 M2（桌面 App 打包）。
2. **TUN 测试风险**：本机 Stash 开着增强模式（TUN 接管默认路由）。日常开发和自动化验证一律使用**有限网段**的 TUN（`tun.route_include` 只放测试 IP，与 M0 相同）；「全局 TUN」验收等用户临时关闭 Stash 增强模式时再做，或在其他机器上进行。
3. **TUN 下的进程归属**：在 helper 正常启动的环境中重新验证（M0 F3 已撤回）。

## 实现选择

- **helper 用 Go 实现**（原计划 Swift）：可以直接复用 M0 已验证的 utun / 路由 / FD 传递代码，核心只保留一种语言和工具链；launchd / SMAppService 对 helper 的实现语言没有要求。代码签名校验（Security 框架）在 M2 通过 cgo 补上，M1.5 只校验 uid。
- **TUN 路由不内置策略**：
  - `tun.route_include` 为空时表示全局接管（`0.0.0.0/1` + `128.0.0.0/1`，不替换默认路由）；
  - 排除网段来自 `tun.route_exclude`、overlay 的 `dev_bypass.cidrs`，以及 NetworkSense 的动态结果；
  - TUN 自身网段由 sing-tun 推导（M0 F2）。

## 分步

| 步骤 | 内容 | 验收 |
| :--- | :--- | :--- |
| 1. helper ✅ | `pkg/helper`（协议 / 服务端 / 客户端）+ `cmd/velum-helper`：创建 utun、配置地址与 MTU、路由（只管理自己添加的、拒绝 /0、不覆盖已有路由）、SCM_RIGHTS 传 FD、按 uid 鉴权、会话断开即清理 | 单元测试（假的系统调用层）；真机：有限网段 |
| 2. TUN 模式 ✅ | 配置 `tun:`；编译器生成 RoutePlan（包含 / 排除网段）、启用 hijack-dns 与 fake-ip；渲染 tun 入站；引擎通过 helper 打开 TUN（PlatformInterface）；daemon `mode: tun` / `vlu tun on|off` | 有限网段真机：流量经 TUN → 规则 → 出站；`kill -9 velumd` 后路由与 utun 被 helper 清理 |
| 3. 进程归属复核 ✅ | TUN 下 `process_name` 规则 / 动态实录中的进程 | 正常启动环境中能查到 curl |
| 4. NetworkSense v1 ✅ | Captive Portal 检测与让路、外部 VPN（分流 / 全局）感知与路由排除、睡眠唤醒重检 | 单元测试（录制的系统信号回放）+ 真机观察 |

## 进度

### 第 1～3 步 ✅（2026-10-16）

- `pkg/helper` + `cmd/velum-helper`（Go）：
  - 路由安全规则：拒绝 `/0`；拒绝落在回环网段内的路由；**不覆盖已存在的路由**（其他 TUN / VPN 占用时报错）；只删除本会话添加的路由；
  - 会话断开（含 `kill -9`）即清理。
  - 单元测试使用假系统层，传递真实 FD，不需要 root。测试抓到一个 bug：回环检查原先会把 `0.0.0.0/1` 误判为与回环重叠，导致全局 TUN 被拒绝。
- TUN 模式：配置段 `tun:`；RoutePlan 计算「包含 − 排除（含 overlay cidrs）+ TUN 自身网段」；tun 入站经平台接口取得 helper 创建的 utun；每次启动引擎建立一个 helper 会话，停止或重启时释放；`vlu tun on|off|config`。
- 真机验证（有限网段，Stash 增强模式保持开启）：
  - 3 个测试 IP 经 `utun5` 进入 Velum，默认路由仍为 Stash；
  - 直连规则生效；代理链路经国内入口节点正常；
  - **TUN 下用户态 velumd 能识别发起连接的进程（`/usr/bin/curl`）**：M0 F3 的撤回得到确认，不需要 helper 代查；
  - `vlu tun off` 后路由与 utun 清理干净；`kill -9 velumd` 后 helper 在 **36ms** 内删除路由并销毁 utun。
- 发现：
  - TUN 模式开启 `auto_detect_interface` 后，出站绑定物理网卡（en0）。**当其他全局型 VPN / TUN 占据主路由时，Velum 的出站会绕过它**。本机上某个节点只能经 Stash 套一层才能连通，直连时 TLS 握手失败。这正是 NetworkSense「全局型 VPN 让路」要处理的场景（§4.3.2）。
  - 本机 Stash 只占用一条 `default` 路由（没有拆成 0/1 + 128/1），因此 Velum 的全局 TUN 会抢走全部流量。全局 TUN 验收必须先关闭 Stash 增强模式。

### 待办（第 2 步遗留）

- **系统 DNS 指向 TUN**：helper 需要写入 SCDynamicStore 的 DNS 设置，fake-ip 和 DNS 劫持才能对全部应用生效。在此之前依赖嗅探还原域名。
- **IPv6**：helper 与 RoutePlan 目前只处理 IPv4；全局模式下 IPv6 流量不经过 TUN。

### 第 4 步 NetworkSense v1 ✅（2026-10-16）

- `pkg/netsense`：
  - 快照：路由表（路由 socket）、作用域 DNS（`scutil --dns`）、主接口；
  - `Analyze`（纯函数）：识别外部 VPN。只有声明了实际 IPv4 路由的隧道接口才算，系统自身的 utun0～3 不会被误判。占据主路由的为**全局型**，其余为**分流型**（附带网段、作用域 DNS 域与服务器）；
  - Captive 探测：绕过代理，绑定物理接口，**并使用该接口的作用域 DNS**；
  - `NetworkID`（主接口 + 默认网关 + 默认 DNS）用于判断「是否换了网络」。
- 编译器接收环境输入（保持纯函数）：
  - 分流型 VPN → 生成绑定其接口的直连出站 `VPN:<iface>`；其网段与域名路由到该出站，并优先于订阅；
  - 域名交给其 DNS 解析（resolve 前置机制推广到所有直连类出站）；
  - 网段从 TUN 排除，网段与域名写入系统代理豁免。
- daemon：
  - 启动时同步分析一次（不做探测），之后后台轮询；
  - 环境变化时重新编译（配置未变则不重启引擎）；
  - 全局型 VPN → TUN 让路；Captive → 暂停接管，认证后恢复；
  - 开关与探测地址见配置 `network_sense:`（均有默认值）。
- 测试：
  - 合成快照（分流 / 全局 / 0/1+128/1 / 系统 utun / 自身 utun / 反向解析域）；
  - `scutil --dns` 解析；Captive 各种情形（在线 / 204 / 重定向 / 正文被替换 / 离线）；
  - daemon 场景脚本：全局 VPN 让路 → 分流 VPN 共存（网段排除、绑定接口）→ Captive 暂停 → 认证后恢复。
- 本机实测：启动即识别 Stash 的 `utun4` 为全局接管；`vlu tun on` 立即让路并说明原因；Captive 探测正常。
- 发现并修复：
  - **同一网卡换网络时主接口名不变**，只看主接口会漏掉酒店 Wi-Fi 这类典型场景。改为按 NetworkID 判断；
  - **探测域名经系统 DNS 解析会拿到 fake-ip**（当前是 Stash 的；将来 Velum 自己的 fake-ip 也一样），绑定物理接口后无法连接，被误判为离线。改为使用物理接口的作用域 DNS；
  - 启动后首轮感知要等探测完成，期间 `vlu tun on` 不会让路。改为启动时先同步做分析。

### 系统 DNS 分流 ✅（2026-10-16）

- 起因：本机用 `/etc/resolver/corp.example`、`corp-net.example` 为公司域名指定 DNS。Velum 原本不遵守，会用订阅里的 DNS 去解析这些域名。
- NetworkSense 收集系统中所有声明了专用解析器的域名（`/etc/resolver`、VPN 下发的作用域 DNS，不限接口）；编译器把这些域名的 DNS 规则排在订阅之前，交给 `velum-local`。sing-box 的 local 在 macOS 上直接询问 mDNSResponder，天然遵守作用域 DNS 与多服务器回落。分流 VPN 的域名也改为同样处理（原先只取第一个服务器，无法回落）。开关：`network_sense.system_split_dns`。
- 本机验证：识别出 `corp.example`、`corp-net.example`；`vlu why` 的 DNS 步骤能追溯到系统 DNS 分流；经 Velum 访问 `app.corp.example` 正常，实录与推演一致。
- 更正：部分企业 VPN 在公司内网会自动断开，在内网测到的「不建 utun」不能作为它的类型结论，需在外网实测。
- 待处理（与系统 DNS 指向 TUN 一起做）：系统 DNS 指向 Velum 后，`local` 解析普通域名可能绕回 Velum 形成回环，需要改为使用物理接口的 DNS。

### 系统 DNS 接管 + IPv6（代码与单元测试 ✅，真机待验收）

- helper 协议 v2：
  - `open_tun` 支持 IPv6 地址；IPv6 路由沿用相同的安全规则（拒绝 `::/0`、回环、链路本地；不覆盖已有路由）；
  - `set_dns` 把已启用硬件服务的系统 DNS 设为 TUN 内地址（`172.19.0.2`），**会话断开时先还原 DNS、再删除路由**，与路由同等崩溃安全；不触碰 VPN 服务与 `/etc/resolver`。
- 编译器：
  - IPv6：配置 `tun.address6` 时，全局模式加入 `::/1` + `8000::/1`；没有 IPv6 地址时忽略 IPv6 路由。
  - DNS 接管：`tun.dns_hijack`，默认开启，在模板中可见。
  - **防回环**：系统默认 DNS 指向 Velum 后，原本经系统解析器的 DNS 服务器（overlay 开发域名、订阅的 `system` 策略、兜底）改为直接询问物理网络 DNS；系统 DNS 分流的域名仍交给系统解析器（`velum-system`），因为它们有专用服务器，不会绕回。
  - 取不到物理 DNS 时不接管系统 DNS，并给出告警。
- NetworkSense：物理 DNS **优先取 DHCP 下发值**（`ipconfig getpacket`），并剔除 Velum 自己的地址。原因是接管后，物理接口的作用域 DNS 也会变成 Velum 的地址，探测与防回环会因此失效。
- 测试：helper（IPv6 路由校验、`set_dns`、断开还原）、路由计划（IPv6、无 IPv6 地址时忽略）、DNS 接管改写、daemon 端到端组装。

### M1.5 剩余

- 全局 TUN 验收：需要临时关闭 Stash 增强模式。
- 真实 VPN 下的共存验证：在外网连企业 VPN 测试，同时复现企业 VPN 与 Stash 的启动顺序问题。

### 真机验收：全局 TUN（2026-10-19，本机，已退出 Stash）✅

用真实订阅（23 节点 / 33 规则集）在本机全局 TUN 下日常使用（包括与 Claude 的对话），网页速度正常。验收中发现并修复：

| 问题 | 原因 | 修复 |
| :--- | :--- | :--- |
| helper 拒绝打开 TUN（「拒绝 0.0.0.0/0」） | 没有排除网段时，路由集合把 `0/1 + 128/1` 合并成 `0/0` | 编译器把 `/0` 拆回两个 `/1` |
| 开 TUN 后全部断网，核心出站报 `network is unreachable` | 核心出站绑定物理网卡（IP_BOUND_IF），macOS 只查该网卡作用域内的路由；其他 VPN 退出后主网卡的默认路由是非作用域的 | helper 打开 TUN 时为物理网卡补作用域默认路由（IPv4 / IPv6），会话结束只删自己补的；协议升到 3 |
| 直连首包 400ms+、网页慢 | 订阅写了两个 nameserver（`tls://1.1.1.1` + `doh.pub`），Velum 只用第一个，国内直连经 1.1.1.1 DoT 解析 | 多上游竞速（自定义 DNS 传输 `velum-race`，与 mihomo 一致）；并实现 `respect-rules` 与 `地址#出站` |
| 退出 Velum 后系统代理指向已退出的 Stash（7890），断网 | 接管时快照记下了 Stash 的代理 | 还原时不再写回无人监听的本机代理 |
| Dock 退出 / AppleScript 退出后 velumd 仍在运行 | 只处理了 `ExitRequested`，系统发起的退出只有 `Exit` | 两个事件都停止核心 |
| 保存订阅报 `os error 35` | 校验时逐个拉取 32 个 provider，超过界面 60 秒上限 | provider 并发拉取；非 GET 请求超时 180 秒、超时提示可读 |
| 换到另一个 Wi-Fi 后全部断网，又是 `network is unreachable` | 网卡断开时系统删掉了它的全部路由，包括打开 TUN 时补的作用域默认路由；重连后默认路由又是非作用域的 | helper 监听路由套接字，网络变化后为打开着 TUN 的会话重新补作用域默认路由；协议升到 5 |
| 打开 App 先显示「Velum 未运行」，十几秒后才正常 | velumd 首次加载（拉取订阅、规则集，启动引擎）完成后才开始响应本地接口 | 本地接口先就绪，状态带 `starting`，界面与托盘显示「正在启动…」；App 打开后 20 秒内连不上也按启动中处理；启动期间拒绝写操作 |
| 启动比 Stash / Clash Verge 慢很多（缓存过期时 8～30 秒） | provider 缓存有效期 1 小时，过期后启动要先同步拉取 32 个 provider 再启动内核；冷启动时 Velum 自己的代理还没起来，境外地址只能等超时 | 启动时有缓存就用（即使已过期），内核启动后在后台更新，内容变化才重启引擎（与 Clash / Stash 一致）；重新加载串行化。实测过期缓存下启动 8.6s → 0.9s |
| 更新 Velum 后显示「配置有问题：helper 协议版本不匹配」，更新组件后仍不刷新，系统代理也不能用 | TUN 打不开导致整个引擎启动失败；更新组件后没有触发重新加载 | 组件缺失或版本不匹配时只暂停 TUN（给出更新提示），系统代理照常；安装 / 更新组件后让 velumd 重新加载 |

已验收：切换 Wi-Fi（2026-10-20，关闭再打开 Wi-Fi 后约 1 秒内自动恢复，期间少量连接报「网络不可达」）。

仍待验收（有机会再测）：
- 睡眠唤醒（原理与切换 Wi-Fi 相同，需实测）；
- 真实企业 VPN 共存：企业 VPN 连接时的内网访问与 Claude 对话（订阅 `ipv6: false` 时拒绝 IPv6 的修复待验证）；
- Captive Portal（需在要网页认证的 Wi-Fi 下测）；
- 开机启动。

待优化（低优先级，下次改 helper 时顺带）：helper 收到路由变化后固定等 1 秒再补作用域默认路由（`RouteSettle`），换网后这 1 秒内的出站连接会报「网络不可达」。可缩短到 0.2～0.3 秒：自己补路由产生的变化只会再触发一次空检查。需要更新 TUN 组件。
