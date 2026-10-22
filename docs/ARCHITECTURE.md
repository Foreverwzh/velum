# Velum 系统架构与工程设计方案

> **定位**：公开发布的跨平台代理工具（macOS / Windows / iOS / Android），同时服务「重度开发者」与「只想日常无感使用的普通用户」。
> **设计目标**：对开发者**可解释、内网零冲突**；对普通用户**填入订阅即可用、国内网络始终可用**；在各类用户环境（企业 VPN、受管设备、其他代理工具共存、Captive Portal）下行为可预期。
> **文档状态**：v2（评审修订版）。所有标注「待 M0 验证」的结论在技术验证完成前不作为承诺。

---

## 0. 决策记录（ADR 摘要）

| # | 决策 | 结论 | 状态 |
| :--- | :--- | :--- | :--- |
| D1 | 数据面内核 | **只用 sing-box**；桌面端直接实现内部 `adapter.PlatformInterface`，移动端评估 `experimental/libbox`（见 §2.2）；保留 `Engine` 接口，不实现 mihomo | ✅ 已定（M0 修订） |
| D2 | 桌面 UI | **Tauri v2 + React + TypeScript** | ✅ 已定 |
| D3 | 移动 UI | **Flutter** | ✅ 已定 |
| D4 | macOS TUN 方案 | **SMAppService 特权 helper 创建 utun + 配路由，`SCM_RIGHTS` 传 FD 给用户态 `velumd`**；不用 SystemExtension | ✅ 已定（理由见 §3.2） |
| D5 | Windows TUN 方案 | `velumd` 以 Windows Service 运行，sing-box 原生 wintun + auto_route | ✅ 已定 |
| D6 | 移动端分发（Ad Hoc / TestFlight / 其他） | 待定，M3 开始前决定。**前提：需要付费 Apple 开发者计划**（免费账号不能使用 Network Extension / App Groups，M0 的 iOS 两项也因此阻塞） | ⏳ 待定 |
| D7 | Linux 支持方案 | 低优先级，未排期。倾向：`velumd` 以 systemd 系统服务运行，`User=` 为安装用户，通过 `AmbientCapabilities` 获得网络权限（不以 root 运行）；见 §3.7 | ⏳ 待定 |
| D8 | 开源许可证 | **GPL-3.0-or-later**。velumd、Android 核心静态链接 sing-box（GPL-3.0-or-later），发布产物整体须以 GPLv3 提供源码；sing-box 许可证另有条款：衍生作品未经许可不得使用其名称或暗示关联（Velum 只做事实性说明）。**影响 D6**：GPLv3 与 App Store 条款存在冲突（附加使用限制），iOS 上架前需评估（可能只能走 TestFlight 以外的分发方式，或取得 sing-box 作者授权） | ✅ 已定（2026-10-20） |

---

## 1. 为什么做 Velum？（现有工具的核心痛点）

通过对现有商业客户端（Stash / Surge / Clash 系）的分析与日常踩坑，归纳出以下结构性问题：

1. **多层分流配置各自为政（本地调试的头号痛点）**：
   当开发者配置 `*.corp.example.com -> DIRECT` 时，**系统代理豁免列表**、**Fake-IP DNS 过滤**、**TUN 路由排除**与**规则引擎**彼此独立。漏配任一层，浏览器就会把内网请求发往代理端口，或拿到 `198.18.x.x` 假 IP，导致本地调试服务无法访问。
2. **黑盒转发，缺乏可观测性**：请求失败或变慢时，用户只能看到最终命中的规则名，无法得知：前置规则为何没命中？DNS 走了哪个上游？是否被系统代理豁免截走？还是节点握手失败？
3. **环境切换脆弱，对非技术用户不友好**：
   - 连上酒店/机场认证 Wi-Fi（Captive Portal）时，代理导致认证页打不开；
   - 开启企业 VPN（AnyConnect / EasyConnect 等）时，与代理争抢 DNS 与路由导致内网断联；
   - 进程异常退出后，系统代理设置残留指向已死端口，导致全机断网。
4. **进程臃肿、语言栈复杂**：例如 Stash 混用 Swift + Go (`c-archive`) + Rust + 双 JS 引擎，单架构二进制 55MB+。
   > Velum 自身也是多语言（Go / Rust / TS / Dart / Swift / Kotlin），但我们约束**业务逻辑只在 Go 核心中实现一次**，其余语言只做 UI 与极薄的平台胶水层（见 §3）。

---

## 2. 内核选型与隔离设计

### 2.1 sing-box vs mihomo

| 评估维度 | sing-box | mihomo (Clash.Meta) |
| :--- | :--- | :--- |
| iOS NetworkExtension 内存（上限约 50MB） | 官方 iOS 客户端（SFI）即基于它，`libbox` 提供 `SetMemoryLimit` | 规则/Geo 库较大时社区反馈易触发 jetsam（**未实测**） |
| 平台集成 | `libbox` 是官方为 SFI/SFA/SFM 维护的门面层：`PlatformInterface`（OpenTun / 网卡监听 / 连接归属查询）+ CommandServer（统计 / 连接 / 日志） | 无同等稳定的嵌入门面 |
| Clash YAML 订阅兼容 | **不支持**，需要自行转换（见 §4.1） | 原生支持 |
| 内部 Go API 稳定性 | 大版本间变化频繁（`option.*`、`adapter`、context 注册） | 全局状态较多，嵌入耦合重 |

**结论（D1）**：只用 sing-box，原因有三：

- 四端统一；
- iOS 有官方客户端背书；
- `libbox` 提供了平台集成门面。

代价是需要承担 **Clash YAML → Velum IR 的转换成本**，这是整个项目工作量最大的模块，在 §4.1 单独设计。

> 不做双引擎的原因：Clash → IR → Clash YAML 这条链路本身会丢失信息，「双引擎 100% 兼容机场订阅」在 IR 架构下并不成立；维护两个渲染器并保证语义一致的成本远高于收益；切换引擎必然断开全部连接，无法做到热切换。`Engine` 接口保留，以备将来需要时使用。

### 2.2 隔离设计：VelumIR + Renderer + Engine 适配层

sing-box 的变化分两类，要分别隔离：

| 变化来源 | 例子 | 隔离手段 |
| :--- | :--- | :--- |
| **JSON 配置格式** | legacy DNS rule 废弃、GeoIP/GeoSite 迁到 `rule_set`、字段改名 | `SingBoxRenderer`：VelumIR → 目标版本 JSON 文本，按版本维护 |
| **Go 平台集成 API** | TUN FD 注入、网卡监听、连接归属、统计/连接列表、日志 | 封装在 `engine/singbox/bridge`。**桌面端**直接实现 `adapter.PlatformInterface`：只打开 `UsePlatformInterface`（TUN），其余（含进程查询）回落 sing-box 原生实现。libbox 的接口约 30 个方法，且默认假定运行在 NE 中，桌面端不适用。**移动端**评估 libbox |

```text
[ 机场 Clash/Stash YAML 订阅 ] + [ rule-providers ] + [ Velum Overlay ] + [ NetworkSense 会话态 ]
                                         │
                                         ▼  subscription / overlay 解析
                     ┌──────────────────────────────────────────┐
                     │ VelumIR（自有结构体，内核无关）           │
                     │ Nodes / Groups / Rules / DNS / Bypass /  │
                     │ Hosts / RuleSets / Provenance(来源追溯)   │
                     └─────────────────────┬────────────────────┘
                                           │ compiler
                                           ▼
                     ┌──────────────────────────────────────────┐
                     │ CompiledPlan                              │
                     │ - EngineConfig   (经 Renderer → JSON)    │
                     │ - SysProxyPlan   (豁免列表 / 开关)        │
                     │ - RoutePlan      (TUN 包含 / 排除网段)    │
                     │ - DNSPlan        (Scoped DNS 转发)        │
                     │ - Warnings       (降级 / 不支持项)        │
                     └───────┬──────────────────┬───────────────┘
                             │                  │
                ┌────────────▼─────┐   ┌────────▼────────────────────┐
                │ Engine (sing-box)│   │ 平台执行器                    │
                │ libbox.Service   │   │ sysproxy / helper 路由 / ...  │
                └──────────────────┘   └─────────────────────────────┘
```

**约束**：

1. 整个仓库只有 `core/pkg/engine/singbox/` 允许 `import "github.com/sagernet/sing-box/..."`。这条约束靠 CI 中的 `depguard` 或 import lint 强制执行。
2. `go.mod` 锁定经过验证的 sing-box 发布 Tag。升级流程如下：
   1. 升级依赖；
   2. 跑 Renderer golden test 与 `sing-box check` 校验；
   3. 跑平台冒烟测试；
   4. 修改仅限于 `engine/singbox/` 目录内。
   > 不承诺「5 分钟升级」，承诺的是「升级影响面限定在一个目录」。
3. NetworkSense、Explain（静态部分）、SysProxy、Watchdog、RemoteSync 等核心资产全部在 Velum 自有代码中，**不 import sing-box**。

### 2.3 接口定义

```go
// 渲染与运行分离：Renderer 纯函数、可 golden test；Engine 管生命周期。
type Renderer interface {
    Render(ir *ir.Config, target Platform) (*RenderedConfig, []Warning, error)
}

type Engine interface {
    Name() string
    Capabilities() Capabilities // 支持的协议 / 规则 / 平台特性，供编译器裁剪与告警

    Start(ctx context.Context, cfg *RenderedConfig, hooks PlatformHooks) error
    // Restart 会断开所有现有连接（sing-box 不支持原地热重载）。
    // 调用方需做 diff：仅 SysProxyPlan / RoutePlan 变化时不应触发 Restart。
    Restart(ctx context.Context, cfg *RenderedConfig) error
    Stop(ctx context.Context) error

    Stats(ctx context.Context) (*TrafficStats, error)
    Connections(ctx context.Context) ([]Connection, error) // 含实际命中规则与出站链
    SubscribeLogs(ctx context.Context) (<-chan LogEntry, error)
    URLTest(ctx context.Context, group string) (map[string]time.Duration, error)
}

// 平台差异全部收敛到 hooks，Engine 接口本身不含平台细节。
type PlatformHooks interface {
    // macOS: 向 helper 请求 utun FD；iOS/Android: 系统 API；Windows: 返回 nil，由引擎自建 wintun
    OpenTun(opts TunOptions) (TunHandle, error)
    InterfaceMonitor() InterfaceMonitor
    FindConnectionOwner(conn ConnMeta) (*ProcessInfo, error) // iOS 不支持，返回 ErrUnsupported
}
```

---

## 3. 跨端总体架构

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│                                   UI 层                                       │
│  ┌──────────────────────────────────┐   ┌──────────────────────────────────┐  │
│  │ 桌面: Tauri v2 (macOS / Windows) │   │ 移动: Flutter (iOS / Android)     │  │
│  │ Rust 托盘 + React/TS/Tailwind    │   │ Dart UI                          │  │
│  └───────────────┬──────────────────┘   └───────────────┬──────────────────┘  │
└──────────────────┼──────────────────────────────────────┼─────────────────────┘
                   │ HTTP over UDS / Named Pipe            │ iOS: App↔NE IPC（见 §3.4）
                   │ (REST + SSE 事件流)                    │ Android: 进程内 FFI
┌──────────────────▼──────────────────────────────────────▼─────────────────────┐
│                         libvelum 共享核心（纯 Go）                              │
│  subscription · overlay · compiler · netsense · explain · remotesync · rpc     │
│                                   │                                            │
│                         Engine 接口 → engine/singbox（libbox）                  │
└─────────┬──────────────────────────┬─────────────────────────┬────────────────┘
          │                          │                         │
┌─────────▼───────────┐  ┌───────────▼──────────┐  ┌───────────▼─────────────────┐
│ macOS 平台层         │  │ Windows 平台层        │  │ iOS / Android 平台层        │
│ velum-helper (root) │  │ velumd = Win Service  │  │ iOS NEPacketTunnelProvider  │
│ - utun 创建 + 传 FD  │  │ - wintun + auto_route │  │ Android VpnService          │
│ - 路由表 / 系统代理  │  │ 用户态: WinINet 代理  │  │                             │
│ - 崩溃兜底还原       │  │ - 启动自愈            │  │                             │
└─────────────────────┘  └──────────────────────┘  └─────────────────────────────┘
```

### 3.1 进程模型

| 平台 | 进程 | 权限 | 职责 |
| :--- | :--- | :--- | :--- |
| macOS | Tauri App | 用户 | UI、托盘、注册 LaunchAgent / helper |
| macOS | `velumd` | 用户（LaunchAgent） | Go 核心 + sing-box 引擎 |
| macOS | `velum-helper` | root（SMAppService LaunchDaemon） | utun、路由表、`SCPreferences` 系统代理、崩溃兜底 |
| Windows | Tauri App | 用户 | UI、托盘、安装服务（UAC） |
| Windows | `velumd` | SYSTEM（Windows Service） | Go 核心 + sing-box，wintun + 路由 |
| Linux（规划，§3.7） | Tauri App | 用户 | UI、托盘、安装服务（pkexec） |
| Linux（规划，§3.7） | `velumd` | 安装用户 + `CAP_NET_ADMIN` 等（systemd 系统服务） | Go 核心 + sing-box，原生 tun + auto_route |
| iOS | Flutter App | 沙盒 | UI、配置编辑、订阅更新与转换（编译产物写入 App Group） |
| iOS | PacketTunnel 扩展 | NE 沙盒，≤50MB | **仅运行 engine + 最小 RPC**，加载预编译好的 JSON |
| Android | Flutter App + VpnService | 同进程 | 核心（完整 daemon）在 App 进程内运行；界面经 HTTP over UDS 访问，VpnService 经 gomobile 接口为核心建立 VPN（见 `docs/M3-PLAN.md`） |

### 3.2 macOS：为什么选择「特权 helper + utun 传 FD」（D4）

候选方案对比：

| 方案 | 与公司 VPN（NE 类）共存 | 内存限制 | 用户授权成本 | 迭代成本 |
| :--- | :--- | :--- | :--- | :--- |
| **SMAppService helper + utun 传 FD** | 好：自管 utun 与路由，不占 NE VPN 槽位 | 无 | 首次一次「登录项与扩展」授权 | 改 `velumd` / UI 不涉及 helper，无需重新授权 |
| SystemExtension + NEPacketTunnel | 与其他 NE VPN 存在互斥 / 抢占风险 | 有 NE 内存约束 | 系统设置中批准扩展，升级可能需重新批准 | 需额外 entitlement，调试成本高 |

**设计要点**：

1. **helper 只做特权操作、保持极薄**（Swift 实现，目标 < 1000 行，需要用到 Security / SystemConfiguration / PF_ROUTE 原生 API）：
   - 创建 utun，配置地址与 MTU。外部 FD 模式下 sing-tun 不会配置任何地址和路由；
   - 读写 `SCPreferences` 系统代理，**写入前保存快照**；
   - 增删路由：路由清单由 `velumd` 给出，内容是 `tun.Options.BuildAutoRouteRanges()`（**必须包含 TUN 自身网段**，否则 system 栈的回包会丢失）与 Velum `RoutePlan` 的合并。NetworkSense 动态排除网段时**直接改路由表，无需重启引擎**；
   - ~~代查连接归属~~：M0 曾判断非 root 进程查不到其他进程的连接，M1 复核后确认是测试环境造成的误判（见 `spikes/m0-utun/README.md` F3）。用户态 velumd 可以直接查询；TUN 模式下待 M1.5 复核，复核失败才启用 helper 代查（已验证可行）。
2. **调用方鉴权**：`velumd` 通过 UDS 连接 helper。helper 用 `LOCAL_PEERPID` 拿到对端 PID，再用 `SecCodeCopyGuestWithAttributes` 加 `SecRequirement` 校验对端签名（Team ID + Bundle ID）。校验通过后才响应，并用 `SCM_RIGHTS` 传回 utun FD。
3. **崩溃兜底**：helper 持有与 `velumd` 的长连接，连接断开时立即执行：
   1. 删除 Velum 添加的路由；
   2. 关闭 utun；
   3. 用快照还原系统代理。
   > helper 自身启动时，若发现系统代理指向 Velum 端口且 `velumd` 不在线，同样执行还原（覆盖整机重启、helper 被杀等场景）。
4. **`velumd` 无需 root**：sing-box 的 `auto_route` 在此模式下关闭，由 `PlatformHooks.OpenTun` 向 helper 申请。路由归 helper 管。

> ✅ M0 已验证（见 `spikes/m0-utun/README.md`）：外部 FD 接管流量、运行期增删路由不重启引擎、`kill -9` 后约 30ms 内完成兜底清理。
> 另外注意：传入平台接口后，sing-box 默认把日志写到 `io.Discard`，需要通过 `PlatformLogWriter` 接入 Velum 的日志。

> **M1.5 实测（路由维护成本）**：helper 自管 utun 与路由，物理网卡的路由也得自己维护。velumd 的出站连接绑定物理网卡（`IP_BOUND_IF`），依赖物理网卡上的作用域默认路由；其他 VPN 退出、换 Wi-Fi 后这条路由可能不存在，绑定的连接会一律「网络不可达」。现由 helper 在打开 TUN 时补上，并监听路由套接字在网络变化后重新补（协议 5）。NetworkExtension 方案由系统维护这些路由，是否迁移见路线图 MN。

> **M1 实测（系统代理）**：其他 VPN / 代理工具占据主网络服务时（例如 Stash 增强模式、企业 VPN 下发代理），它们隧道下发的代理设置优先，Velum 写入各硬件服务的设置不会生效。Velum 不覆盖其他工具的设置，而是读取实际生效的代理（`State:/Network/Global/Proxies`）并提示用户。这一信号也供 NetworkSense（§4.3.2）使用。

### 3.3 Windows

- `velumd` 作为 Windows Service（SYSTEM）运行，直接使用 sing-box 原生 wintun 和 `auto_route`。
- 系统代理位于 HKCU，属于用户会话，SYSTEM 写入无效。~~由 Tauri App（Rust）写入~~（M2.4 修订：业务逻辑只在 Go 实现一次）：
  - velumd 用活动控制台用户的令牌启动 `velumd sysproxy <操作>` 子进程，在用户会话中执行 WinINet 读写（按连接选项 `InternetSetOption`，并通知 `SETTINGS_CHANGED / REFRESH`）；
  - 快照、还原、启动自愈与 macOS 共用同一套流程；velumd 以普通用户身份运行（开发 / 仅代理）时直接在进程内调用；
  - 拨号 / VPN 连接（RAS）的独立代理设置暂不处理。
- **启动自愈**：Tauri App 或 Service 启动时，若发现系统代理指向 Velum 端口但核心不可用，就执行还原。Service 配置失败自动重启。
- RPC 使用 Named Pipe，ACL 只允许 SYSTEM、Administrators 与该用户；以用户身份运行时 Pipe 名带用户 SID。客户端校验 Pipe 所有者，防止同名 Pipe 冒充。
- 服务只在 `%ProgramData%\Velum`（SYSTEM / 管理员可写）中写自己的数据；用户目录中的配置以用户身份读写，避免 SYSTEM 在用户可控的位置操作文件。

### 3.4 iOS

- **内存预算**（待 M0 实测后填写真实数据）：扩展稳态目标 ≤ 35MB，通过 `libbox.SetMemoryLimit` 设置软上限。
- **扩展内只运行**：engine、最小状态 RPC、连接/统计查询。订阅解析、Overlay 编译、Explain 静态仿真、RemoteSync 全部放在 **App 进程**，编译产物写入 App Group 容器，扩展只加载 JSON。
- **App ↔ 扩展通信**：`NETunnelProviderSession.sendProviderMessage`，用于发送控制命令；配置与大块数据走 App Group 共享文件，扩展收到「配置已更新」通知后再读取。
- **Go 运行时体积**：以动态 framework 形式嵌入，App 与扩展共享同一份磁盘副本（待 M0 验证打包方式）。
- **平台限制**：
  - 不支持 `process_name` 规则，编译时剔除并告警；
  - 同一时间只能有一个 VPN，因此不存在 VPN 共存场景；
  - Captive Portal 由系统 CNA 处理，扩展只需配合 On-Demand 规则。

### 3.5 Android

- `VpnService` 与核心同进程。~~Flutter 通过 FFI 直接调用~~（M3 修订）：界面与桌面端一样经 HTTP over UDS 访问核心；Kotlin 只实现 Go 做不到的平台能力（建立 VPN、报告物理网络），经 gomobile 接口回调。
- VpnService 排除 Velum 自身，核心的出站连接直接走物理网络；TUN 使用 gVisor 栈。Android 没有 `/etc/resolv.conf`，系统 DNS 由 App 报告的物理网络 DNS 代替。
- 进程规则使用包名，连接归属通过 `ConnectivityManager.getConnectionOwnerUid`（Android 10+）查询。
- 同一时间只能有一个 VPN。与公司 VPN 共存的替代方案是**按应用排除**（`addDisallowedApplication`）。
- Captive Portal 检测使用系统的 `NET_CAPABILITY_CAPTIVE_PORTAL` 信号。

### 3.6 本地 RPC

- 协议：**HTTP/1.1 over UDS（macOS / Android）/ Named Pipe（Windows）**，REST 请求 + SSE 事件流（连接、日志、状态）。
- 鉴权：macOS socket 文件位于用户目录、权限 `0600`；Windows 使用 Pipe ACL，客户端校验 Pipe 所有者。
- sing-box 自带的 `clash_api` **默认关闭**。连接数据通过 `Router.AppendTracker` 在进程内获取（M0 已验证，见 §4.4），统计由 tracker 自行累计。

### 3.7 Linux（低优先级，规划中）

**现状**（M2.4 之后）：`velumd` / `vlu` 可以在 Linux 上编译运行，订阅、分流、策略组、`vlu why` 和本地混合端口都可用；但不接管系统代理，TUN 明确标为不可用，没有 NetworkSense，没有桌面端安装包，CI 也不跑 Linux。目前只能手动编译，作为本地代理使用。

**目标范围**：先支持 Ubuntu 22.04+ / Debian 12（x86_64、arm64），桌面环境以 GNOME 为主，KDE 次之；其他发行版和桌面环境「能跑但不保证」。

**方案要点**（D7 待定，以下为倾向）：

- **进程与权限**：`velumd` 作为 systemd **系统**服务运行，`User=` 设为安装用户，`AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE`。
  - 进程以用户身份运行，配置、数据、socket 都留在用户目录，不需要 Windows 那样的 ProgramData 分离和用户模拟；
  - 不需要 macOS 那样的 root helper：sing-box 原生 tun + `auto_route`（必要时 `auto_redirect`）在具备 `CAP_NET_ADMIN` 时即可工作；
  - systemd 用户服务（`systemctl --user`）拿不到 ambient capabilities，因此只适合仅代理模式，可作为不装服务时的退路（与 Windows 的「用户模式」对应）。
- **系统代理**：按桌面环境选择后端，放在 `pkg/sysproxy`，沿用同一套快照 / 还原 / 启动自愈流程。
  - GNOME 系：`gsettings` 的 `org.gnome.system.proxy`（mode、http / https / socks、ignore-hosts）；
  - KDE：`kwriteconfig5/6` 写 `kioslaverc`，并发 D-Bus 通知刷新；
  - 其他环境：报告「不支持自动设置」，界面给出手动设置与 `http_proxy` 环境变量的说明；
  - 服务进程需带上用户会话的 `DBUS_SESSION_BUS_ADDRESS`（`/run/user/<uid>/bus`）才能写 gsettings。
- **DNS**：多数发行版使用 systemd-resolved。TUN 模式下由 sing-box 劫持 DNS；分流型 VPN 的域名通过 resolved 的按链路域（per-link domains）识别，对应 Windows 的 NRPT。
- **NetworkSense**：用 netlink（rtnetlink）订阅路由与网卡变化；VPN 判定依据网卡类型（tun / wireguard / ppp）和 resolved 的链路域；Captive 探测用 `SO_BINDTODEVICE` 绑定物理网卡（需 `CAP_NET_RAW`）。
- **本地 RPC**：与 macOS 相同，HTTP over UDS，socket 位于用户目录，权限 `0600`。
- **桌面端**：Tauri 在 Linux 依赖 webkit2gtk-4.1，托盘依赖 libayatana-appindicator（GNOME 需要 AppIndicator 扩展）；打包为 .deb 和 AppImage；开机启动用 `~/.config/autostart/*.desktop`；安装服务通过 `pkexec velumd service install`。

---

## 4. 核心子系统设计

### 4.1 订阅转换：Clash YAML → VelumIR（最大工作量模块）

**原则**：可以降级，但**绝不静默丢弃**。每个降级或不支持的项都要产生 `Warning`，在 UI 与 `vlu status` 中可见。

| 类别 | Clash 原语 | VelumIR / sing-box 映射 | 状态 |
| :--- | :--- | :--- | :--- |
| 节点 | ss（含 2022 / obfs）/ vmess / vless(+reality) / trojan / anytls / hysteria / hysteria2 / tuic / socks5 / http / snell v4·v6 | 对应 outbound | 支持（传输层 ws/grpc/h2 等逐项映射）。需构建标签 `with_utls`（reality / 指纹）、`with_quic`（hysteria2 / tuic） |
| 节点 | ss + shadow-tls 插件 | `shadowtls` 出站，ss 以它为 detour | 支持 |
| 节点 | wireguard | sing-box **endpoint**（不是 outbound） | 支持，需构建标签 `with_wireguard` |
| 节点 | ssr / snell v1～v3 / ss restls 插件 / hysteria `faketcp` / `smux` | — | 不支持或降级，告警；不支持的节点从策略组中剔除 |
| 节点 | `dialer-proxy` | outbound `detour` | 支持 |
| 节点源 | `proxy-providers`（含 `filter` / `exclude-filter`） | **Velum 拉取后在编译期展开为普通节点** | 支持；`override` 待实现 |
| 策略组 | `select` | `selector` | 支持 |
| 策略组 | `url-test` | `urltest` | 支持 |
| 策略组 | `fallback` | `urltest` + 顺序优先（语义有差异） | 降级 + 告警 |
| 策略组 | `load-balance` | `urltest` | 降级 + 告警 |
| 策略组 | `relay` | 克隆后续各跳节点并挂 `detour` 链 | 支持；第一跳可以是策略组，后续各跳必须是普通节点，否则不支持 |
| 策略组 | 动态成员 `use` / `include-all*` / `filter` / `exclude-filter` / `exclude-type` | **编译期展开**为静态成员，订阅更新时重新编译 | 支持。`filter` 含前瞻断言（Go RE2 不支持）时降级；`exclude-type` 按 mihomo 适配器类型名（`Shadowsocks` / `Vmess`…）匹配 |
| 规则 | DOMAIN / SUFFIX / KEYWORD / REGEX、IP-CIDR(6)、SRC-IP、DST-PORT、PROCESS-NAME/PATH、MATCH | 对应 route rule / `final` | 支持（PROCESS 在 iOS 剔除） |
| 规则 | DOMAIN-WILDCARD（Surge / Stash 私有） | `domain_regex` | 支持，语义一致；正则性能低于后缀匹配 |
| 规则集 | classical 中混有 PROCESS-NAME / 端口 / 源 IP | **按类别拆成多条 headless rule** | ⚠️ sing-box 同一条规则内，域名 / IP 条件与进程等其他条件是「与」关系，不拆开会导致域名规则失效（M0 实测） |
| 规则 | IP 规则未标 `no-resolve` | 在首条需要解析的规则前插入 `{"action":"resolve"}` | 支持；路由级行为待 M1 端到端验证 |
| 规则 | AND / OR / NOT | `logical` rule | 支持 |
| 规则 | GEOIP / GEOSITE | `rule_set`（随应用内置 + 远端更新） | 支持 |
| 规则 | RULE-SET（rule-providers：classical / domain / ipcidr，yaml / text） | Velum 自行拉取并转换为 sing-box source 格式的本地 `rule_set` | 支持 |
| 规则 | IP-ASN / USER-AGENT / URL-REGEX / SCRIPT / SUB-RULE | — | 不支持，告警（sing-box 没有 ASN 库；UA / URL 匹配需要 MITM） |
| DNS | `fake-ip-filter`、`nameserver-policy`（含 `geosite:` / `rule-set:`） | sing-box DNS rules + `rule_set` | 支持 |
| DNS | `fallback` / `fallback-filter` | — | 不支持，告警（后续可改写为基于 GeoIP 的 DNS 规则） |

- **规则集分发**：GeoIP / GeoSite 规则集与 rule-providers 都由 Velum 自行下载（遵循 HTTPS_PROXY，带缓存，失败回落缓存；provider 的 `path` 作为最后回落），以本地文件交给内核。原因：sing-box 远程规则集首次下载失败会导致整个引擎无法启动（M1 实测）。无缓存且下载失败时，该规则集降级为空集并告警。下载地址可在配置中替换为镜像。
- **策略组私有字段**：Stash 的 `ssid-policy`（按 Wi-Fi 切换）由 NetworkSense 场景触发器在运行时调用 selector 实现。
- **DNS 降级项**：多个 nameserver 并发竞速 → 只取首个；`respect-rules` 由 Velum DNSPlan 实现。
- **系统 hosts**：与 mihomo 一致默认读取系统 hosts 文件（`use-system-hosts: false` 时关闭）。编译器注入 sing-box 的 `hosts` DNS 服务器和一条 `preferred_by` 规则，只应答文件里有记录的域名，排在 Overlay hosts 之后、其他 DNS 规则之前；文件修改后自动生效。`vlu why` 的 DNS 一步显示「系统 hosts 文件」。
- **验证方式**：用真实订阅（脱敏，不入库）和覆盖型合成样本（`spikes/m0-subconv/testdata/coverage`）做 golden snapshot 测试，并对产物执行 `box.New` 校验。另外用 Clash 语义参考匹配器生成正例 / 反例，与 sing-box 匹配器逐条比对，覆盖规则集和内联 AND / OR / NOT。方法见 `spikes/m0-subconv`。

### 4.2 Overlay 多层一致性编译器（终结开发域名断网）

配置分为两层：

- **底层**（只读，可随时刷新）：机场订阅。
- **表层**（用户私有）：`overlay.yaml`，或 UI 中的「本地开发直连」列表。

```yaml
dev_bypass:
  domains:            # "x" 根域 + 子域；".x" 仅子域；"=x" 精确
    - "corp.example.com"
    - "local.dev"
  cidrs:
    - "10.12.0.0/16"
  hosts:              # 域名覆盖（不修改 /etc/hosts）
    "staging.corp.example.com": "10.12.34.56"
  processes:          # iOS 不支持；Android 填包名
    - "SomeVPNClient"
system_proxy:
  bypass:             # 系统代理豁免，原样写入系统设置
    - "localhost"
    - "192.168.0.0/16"
```

**域名语义统一**：IR 中每条 `DomainMatcher` 都有明确的语义（根域+子域 / 仅子域 / 精确匹配），由各层渲染器分别翻译为本层的等价写法。例如在系统代理豁免中，「根域+子域」需要同时写 `corp.example.com` 和 `*.corp.example.com`。golden test 必须覆盖三类用例：根域、子域，以及**相似但不应命中的域名**（如 `xcorp.example.com`）。

**原则：策略来自配置，代码只实现机制。** 编译器不注入任何路由、DNS 或系统代理豁免策略（包括「私有地址直连」「localhost 豁免」这类看似通用的条目）。它只负责把订阅和 Overlay 展开到各层，并实现协议嗅探、按 Clash 语义插入 resolve 等机制。需要默认值时，做成用户可见、可编辑的配置。

**编译器一次产出 4 个层面，保证一致**：

| 层面 | 普通直连域名 / 网段 | 带 `hosts` 覆盖的域名 |
| :--- | :--- | :--- |
| ① 系统代理豁免（`SysProxyPlan`） | 写入豁免列表（另加用户在 `system_proxy.bypass` 中配置的条目；Velum 不内置任何条目） | **不写入豁免**。请求必须经过代理端口，覆盖才能生效（否则浏览器直连会使用系统 DNS，绕过 Velum） |
| ② DNS（`DNSPlan`） | 排除 Fake-IP，转发到系统 / Scoped DNS 获取真实 IP | 由 Velum 内存 hosts 直接应答 |
| ③ 规则引擎（`EngineConfig`） | 在所有订阅规则之前插入 `→ direct` | 插入 `→ direct`，目标地址改写为覆盖 IP |
| ④ TUN 路由（`RoutePlan`） | `cidrs` 与当前局域网段从 TUN 中排除 | 不排除（需要进入 TUN 才能被 DNS 劫持与改写） |

**出站域名解析（sing-box ≥1.12 语义，M1 实测）**：出站一旦设置了 `domain_resolver`（或 `route.default_domain_resolver`），就直接使用该服务器，**不经过 DNS 规则**；而「不设 resolver、改走 DNS 规则」的旧行为在 1.14 已移除。为了保持 Clash 语义，渲染器做了三件事：
- 节点出站的 `domain_resolver` 设为 `proxy-server-nameserver`；
- 规则**直接**指向 DIRECT 时，在其前插入条件相同的 `resolve` 动作（不指定 server，因此经过 DNS 规则：overlay 内网域名走系统 DNS，nameserver-policy 生效），直连出站随后直接使用解析出的地址；
- 经策略组选到 DIRECT 的连接走 `default_domain_resolver`（DNS final），不应用 DNS 分流规则，以免被代理的域名泄漏到直连 DNS，并给出告警。

> 每条生成项都带有 `Provenance`（来源文件与行号），供 Explain 追溯「这条豁免是谁加的」。

### 4.3 NetworkSense：环境自适应

不硬编码任何公司名称，完全基于操作系统的网络信号工作。**以下能力仅限桌面端**（移动端见 §3.4 / §3.5）。

```text
             [ 网卡 / 路由 / DNS / Wi-Fi 变化事件（去抖 1s）]
                                 │
      ┌──────────────────────────┼──────────────────────────┐
      ▼                          ▼                          ▼
 [Captive Portal]          [外部 VPN 感知]             [健康熔断]
```

#### 4.3.1 Captive Portal 自动让路

- 切换网络时（按 NetworkID：主接口 + 默认网关 + 默认 DNS 判断；同一网卡换 Wi-Fi 时主接口名不变），绑定物理网卡直连探测多个地址。**探测的域名解析也必须使用物理接口的作用域 DNS**，否则会被其他工具或自身的 fake-ip 误导（M1.5 实测）。探测地址（`captive.apple.com`、`connectivitycheck.platform.hicloud.com/generate_204` 等），任一结果为 302 或正文不符即判定为认证网络。
- 判定后暂停系统代理与 TUN，状态显示为 `Paused (Captive Portal)`；之后周期探测，认证通过后自动恢复。

#### 4.3.2 外部 VPN 共存

1. **感知**：
   - macOS：`SCDynamicStore`（Scoped DNS / Search Domains）与路由表；
   - Windows：`GetAdaptersAddresses` / `GetIpForwardTable2`；
   - 识别 `utun*` / `ppp*` / `ipsec*` / TAP / WireGuard 等非 Velum 网卡所声明的路由与 DNS 域。
2. **分流型 VPN**（只接管部分网段，例如 `10.0.0.0/8`）：
   - **路由层**：这些网段加入 `RoutePlan` 排除表。macOS 上由 helper 直接改路由，**不重启引擎**；Windows 上经引擎 Restart 生效，已做去抖。
   - **DNS 层**：对方声明的域后缀加入直连，解析请求转发到对方的 DNS 服务器。
   - **出站层**：这类流量使用 `direct-os` 出站，**不绑定物理网卡**，交给系统路由表选路，从而走对方的 utun。
     > 默认的 `direct` 出站在 `auto_detect_interface` 下会绑定 `en0`，会把内网流量从物理网卡送出去。`direct-os` 不会形成回环，前提是这些网段已从 Velum 的 TUN 中排除。
   - **系统代理层**：对应的域后缀与网段加入豁免列表。
3. **全局型 VPN**（对方接管默认路由）：进入「让路模式」，即暂停 TUN 并通过通知告知用户。系统代理不受影响：遵守系统代理的应用仍经 Velum，出站再由对方 VPN 承载。

#### 4.3.3 健康熔断

- 国内直连规则始终位于节点规则之前，不受节点状态影响。
- 当所有节点不可用时：先尝试刷新订阅，再全量测速；仍失败则提示用户（极简视图下提示「海外加速暂不可用，国内网络正常」）。

#### 4.3.4 场景触发器（Condition Profiles）

可按 Wi-Fi SSID、接口类型、网关网段、特定进程是否运行等条件，自动切换代理模式或挂载指定的 Overlay。

### 4.4 Explain：全链路诊断

诊断分为**两种模式**，并在报告中明确标注，避免用推演结果冒充事实：

| 模式 | 数据来源 | 适用场景 |
| :--- | :--- | :--- |
| **静态推演**（`vlu why <url>`） | 在 CompiledPlan 上仿真（Velum 自有代码，不依赖 sing-box） | 「这个域名会怎么走？」；配置修改前的预演 |
| **动态实录**（`vlu why --live`、连接列表点选） | `Router.AppendTracker`：实际命中规则（序号 → Provenance）、出站链（递归 `OutboundGroup.Now()`）、嗅探域名、解析结果、字节数、首包耗时；错误原因经 `PlatformLogWriter` 按连接 ID 关联 | 「刚才那个请求实际怎么走的？」 |

动态实录的实现约束（M0 实测）：
- `reject` 等动作在调用 tracker 之前就返回，tracker 看不到。所以 REJECT 要渲染为 **Velum 自有的拦截出站**（sing-box 的 `block` 出站已标记弃用），才能被完整记录；
- 拨号失败时 sing-box 先关闭连接、后输出错误日志，记录需要延迟异步落盘才能关联到错误；
- 跑在其他 TUN 后面时，拨号失败会表现为「下行 0 字节 + 无首包」，Explain 需要识别这种模式。

报告分为 5 步：

1. **System Proxy**：是否命中系统代理豁免列表，命中的是哪一条（附来源）。
   > 注意：无法判断某个 App 是否遵守系统代理，报告只给出豁免判定。
2. **NetworkSense**：是否命中外部 VPN 网段或 Scoped DNS、hosts 覆盖、让路状态。
3. **DNS**：走 Fake-IP 还是真实 IP、上游是谁、解析结果与耗时（动态模式下给出）。
4. **Rule**：静态模式列出命中规则，以及之前每条未命中规则的原因；动态模式给出实际命中的规则。若两者不一致，提示可能的原因：sniff、应用自带 DoH、直接用 IP 访问。
5. **Outbound**：出站链；节点可用性与延迟来自主动 URLTest。
   > 单连接的握手耗时不在 v1 范围内。

**一键修复**：报告末尾给出可执行动作，例如「该域名解析为 `10.x` 内网 IP 但走了代理，加入 `dev_bypass`？」

### 4.5 一核双面：极简视图与专业视图

#### 4.5.1 极简视图

- **界面**：只有状态灯、总开关、网络环境提示，不出现任何专业术语。
- **一键修复流水线**：
  1. 清空 DNS 缓存；
  2. 重新检测网络环境（VPN、DNS 分流）并探测 Captive Portal；
  3. 检查其他代理工具的影响：仍在全局接管的隧道（如用一串网段覆盖整个 IPv4 的代理工具 TUN）、
     工具退出时没有还原的系统 DNS（198.18.0.0/15 虚拟地址）。只提示，不替其他工具做决定；
  4. 强制刷新订阅并重新加载；
  5. 校正系统代理；
  6. 全量测速。
- **导出诊断（可选，需用户确认）**：一键导出脱敏的 Explain 报告与状态快照，用户可自行附在问题反馈中；不自动上传。

#### 4.5.2 远端托管同步（RemoteSync）

> **已取消（2026-10-16）**：用户在首次启动引导中直接填写订阅地址即可，不需要额外的托管与配对。以下设计保留作参考，暂不实现。

| 关注点 | 设计 |
| :--- | :--- |
| **机密性** | 托管包内含机场订阅 token，**必须加密**：使用 age（X25519），接收设备在首次配对时（扫码）获得解密私钥，私钥存入 Keychain / DPAPI / Android Keystore |
| **完整性** | ed25519 签名，公钥在配对时固化到设备上；验签失败直接拒绝 |
| **可达性** | 支持多个镜像地址（如 R2 + 国内对象存储 / WebDAV）；拉取顺序为先走代理、失败后直连、再换下一个镜像。`raw.githubusercontent.com` / Gist 在国内经常被污染，不作为唯一来源 |
| **安全回滚** | 应用新配置后做健康检查（国内探测 + 海外探测）。失败则自动回滚到上一份可用配置（Last-Known-Good），并上报 |
| **能力白名单** | 远端配置只允许修改节点、订阅、规则、Overlay；**禁止下发脚本或重写规则**，也禁止关闭本地安全选项 |
| **频率** | 每 6 小时检查一次，另外在启动、网络恢复时各检查一次；HTTP ETag 去重 |

#### 4.5.3 开发者专业模式

提供实时连接流（可按进程 / 域名 / 规则过滤）、Explain 诊断台、`dev_bypass` / hosts 编辑器、编译告警面板，以及完整的 `vlu` CLI。

---

## 5. 横切关注点

| 主题 | 要求 |
| :--- | :--- |
| IPv6 | Fake-IP 的 v6 段、TUN v6 路由、系统代理豁免的 v6 回环地址；默认策略需防止 IPv6 泄漏（无 v6 节点时阻断或回落 v4） |
| QUIC / HTTP3 | 节点不支持 UDP 时，提供可选的 UDP 443 拦截，迫使浏览器回落到 TCP |
| DNS 泄漏 | TUN 模式劫持 53 端口；Explain 中提示浏览器自带 DoH 的影响 |
| 睡眠 / 唤醒 | 唤醒后触发 NetworkSense 全量重检，并校验系统代理与路由 |
| 机密存储 | 订阅 token 存入系统钥匙串，不落盘明文 |
| 日志与隐私 | 默认不记录完整 URL，只记录域名级别；日志使用环形缓冲；诊断回传前做脱敏 |
| 应用更新 | 桌面：Tauri updater + 签名校验（默认开启）；移动端分发待定（D6） |
| 代码签名 | macOS：Developer ID + 公证，helper 与主程序同 Team ID；Windows：代码签名，避免 SmartScreen 拦截 |
| 许可证 | sing-box 为 GPLv3（README 另有关于名称使用的附加声明，**需核实**）；Velum 核心需与 GPLv3 兼容。公开分发或上架前必须完成合规评估 |

### 5.1 测试策略

- **编译器 / Renderer**：基于真实订阅 fixtures 做 golden snapshot 测试，并对产物执行 `sing-box check`；域名语义三类用例（见 §4.2）。
- **NetworkSense**：将系统信号抽象为接口，用录制的 `scutil --dns`、路由表快照回放测试。
- **Explain**：对同一批 URL，比对静态推演与动态实录结果，差异必须有解释。
- **平台冒烟测试**：每个里程碑定义验收用例，例如「企业 VPN 已连接时，其内网域名可访问且走 `direct-os`」「`kill -9 velumd` 后 1 秒内系统代理已还原」。
- **sing-box 升级回归**：以上全部测试，加上 iOS 扩展内存基线测试。

---

## 6. 仓库目录结构

```text
velum/
├── docs/
│   ├── ARCHITECTURE.md
│   └── adr/                        # 各项决策的详细记录
├── core/                           # Go 核心 (module: github.com/Foreverwzh/velum/core)
│   ├── cmd/
│   │   ├── velumd/                 # 桌面守护进程（macOS LaunchAgent / Windows Service）
│   │   └── vlu/                    # CLI：up / down / status / why / bypass / sync
│   ├── pkg/
│   │   ├── ir/                     # VelumIR 定义（内核无关）
│   │   ├── subscription/           # Clash YAML / rule-providers → IR，含告警
│   │   ├── overlay/                # overlay.yaml 模型与合并
│   │   ├── compiler/               # IR + 会话态 → CompiledPlan（含 Provenance）
│   │   ├── engine/                 # Engine / Renderer / PlatformHooks 接口
│   │   │   └── singbox/            # ★ 唯一允许 import sing-box 的目录
│   │   │       ├── render/         #   IR → 目标版本 JSON
│   │   │       └── bridge/         #   平台集成（桌面：adapter.PlatformInterface；移动：libbox）
│   │   ├── netsense/               # Captive Portal / VPN 感知 / 健康熔断 / 场景触发
│   │   ├── explain/                # 静态推演 + 动态实录
│   │   ├── sysproxy/               # SysProxyPlan → 平台执行（含快照与还原）
│   │   ├── remotesync/             # 加密、签名、镜像、LKG 回滚
│   │   └── rpc/                    # HTTP over UDS / Named Pipe，REST + SSE
│   └── mobilebind/                 # iOS / Android 导出层
├── platform/
│   └── macos-helper/               # velum-helper（Swift，root，极薄）
├── desktop/                        # Tauri v2
│   ├── src-tauri/                  # Rust：托盘、进程管理、SMAppService 注册、Windows 系统代理
│   └── src/                        # React + TypeScript + Tailwind（极简 / Pro 双视图）
└── mobile/                         # Flutter
    ├── lib/
    ├── ios/                        # App + PacketTunnel 扩展（Swift）
    └── android/                    # VpnService（Kotlin）
```

---

## 7. 分阶段路线图

按**风险优先**原则：先验证最可能推翻架构的假设，再铺功能。

### 🔬 M0：技术验证（1～2 周，产出验证报告 + ADR 更新）

- [x] **订阅转换损耗**：Clash → sing-box，统计支持 / 降级 / 不支持的比例。✅ 2026-10-16：真实样本（本机订阅）加覆盖型合成样本，规则语义 0 差异，报告见 `spikes/m0-subconv/`。拿到更多真实机场订阅后可直接复跑。
- [ ] **iOS 扩展内存**：最小 NEPacketTunnel + libbox + 真实规则集（含 GeoSite-CN），在真机上测稳态与峰值内存。⛔ 阻塞：需要付费开发者账号（与 D6 一起决定）
- [x] **macOS utun 传 FD**：最小 helper 创建 utun → `SCM_RIGHTS` → `velumd` 接管流量，验证路由与进程归属查询。✅ 2026-10-16，报告见 `spikes/m0-utun/README.md`
- [x] **动态实录**：拿到「实际命中规则 + 出站链」。✅ 2026-10-16，用 `Router.AppendTracker`，不依赖 libbox / clash_api，报告见 `spikes/m0-live/README.md`
- [ ] **Go 动态 framework**：验证 App 与扩展共享同一份打包产物。⛔ 阻塞：同上

> 任一项失败，回到本文档修订对应决策，再进入 M1。

### 🎯 M1：Go 核心 + `vlu` CLI（macOS，仅系统代理模式）

- [x] `core/` 工程骨架、import 约束 lint、CI。
- [x] 订阅转换（§4.1）+ 告警体系 + golden test。
- [x] Overlay 编译器（§4.2，系统代理 / DNS / 规则三层）。
- [x] `velumd` 生命周期与 Engine 集成（sing-box 系统代理模式）。
- [x] `vlu why` 静态推演 + `--live` 动态实录（静态推演支持 GeoIP / GeoSite 规则集）。
- [x] 系统代理快照与还原（M1 先用用户授权写入，M1.5 迁移到 helper）。
- ✅ 2026-10-16 完成，详见 `docs/M1-PLAN.md`。
- **验收**：日常用 `vlu up` 接管本机；`dev_bypass` 中的本地调试域名零断联；`vlu why` 输出与实际行为一致。

### 🎯 M1.5：macOS TUN + NetworkSense

- [x] `velum-helper`：utun、路由、系统 DNS、崩溃兜底（§3.2）。
- [x] 编译器补齐 TUN 路由层（`RoutePlan`，含 IPv6、系统 DNS 接管与防回环）。
- [x] NetworkSense：Captive Portal、外部 VPN 共存（分流型 / 全局型）、睡眠唤醒处理、系统 DNS 分流。
- ⏳ 全局 TUN + 系统 DNS 接管已在本机真机验收（2026-10-19，修复见 `docs/M15-PLAN.md`）；切换 Wi-Fi 已验收（2026-10-20）；待验收：真实企业 VPN 共存、Captive Portal、睡眠唤醒、开机启动。
- **验收**：企业 VPN 连接与断开过程中内网全程可用；`kill -9 velumd` 后 1 秒内网络恢复；在酒店 Wi-Fi 下能自动让路。

### 🎯 M2：桌面端 Tauri（macOS + Windows）

- [x] Tauri v2 工程、托盘菜单、`velumd` 进程管理。
- [x] 无签名本地打包（M2.3a）：内置 velumd / helper / vlu，helper 经 LaunchDaemon 安装，开机启动。
- [ ] SMAppService 注册（替换 M2.3a 的 LaunchDaemon 安装方式）。
- [x] 极简 / Pro 双视图；连接流、Explain 诊断台、Overlay 编辑器、告警面板；首次启动引导。
- [ ] Windows：Service、wintun、WinINet 系统代理与启动自愈、Named Pipe ACL。⏳ 代码与单元测试完成，真机待验收（见 `docs/M2-PLAN.md` M2.4）。
- [x] ~~RemoteSync（加密、签名、镜像、LKG 回滚）与配对流程~~：已取消，改为首次启动引导中填写订阅地址。
- [ ] 自动更新与代码签名 / 公证（需付费 Apple 开发者账号）。

### 🎯 M3：移动端 Flutter（Android + iOS）

通用：

- [x] `mobilebind`：gomobile 绑定 Go 核心（启停、TUN 回调、网络状态、连接归属）。
- [x] 初始化 `mobile/`（Flutter），实现极简 / 专业双视图与首次启动引导（填写订阅地址）。

Android（详见 `docs/M3-PLAN.md`）：

- [x] `VpnService`（gVisor 栈、按应用包含 / 排除、始终开启 VPN、物理网络跟踪）、连接归属、打包与 CI。模拟器（Android 16）验证通过。
- [ ] 真机验收：真实节点、Wi-Fi ↔ 蜂窝切换、息屏 / 省电模式下保持、Android 10 / 11 的网络跟踪回退路径。
- [ ] 正式发布签名（生成并保管密钥）。

iOS（⛔ 阻塞：需要付费 Apple 开发者账号）：

- [ ] 决定 D6（移动端分发方式）。
- [ ] iOS App ↔ 扩展 IPC、App Group 配置下发。
- [ ] `NEPacketTunnelProvider`（On-Demand、内存基线测试纳入 CI）。

### 🎯 M4：进阶特性（按需）

- [ ] 请求重写 / 脚本引擎（QuickJS / Wasm）。**仅限本地配置，禁止远端下发。**
- [ ] 单连接握手耗时诊断（需评估对 sing-box 的侵入程度）。
- [ ] 若出现明确需求，再评估实现第二个 Engine。

### 🧊 ML：Linux 桌面（低优先级，未排期）

排在 M3 之后，或有明确需求时再启动。设计见 §3.7，开工前先确定 D7。

- [ ] 决定 D7（Linux 进程与权限模型）。
- [ ] CI 加入 ubuntu（core 测试；desktop 编译打包）。
- [ ] 系统代理：GNOME（gsettings）与 KDE 后端，接入快照 / 还原 / 自愈；其他环境给出手动设置说明。
- [ ] `velumd service install|uninstall|start|stop|status`（systemd 系统服务，`User=` + ambient capabilities）。
- [ ] TUN：sing-box 原生 tun + `auto_route`，补 render 与编译器的 Linux 分支；验证与 systemd-resolved 的 DNS 配合。
- [ ] NetworkSense：netlink 路由 / 网卡、resolved 链路域、Captive 绑定物理网卡。
- [ ] 桌面端：托盘、pkexec 安装服务、autostart、.deb / AppImage 打包。
- **验收**：Ubuntu 24.04 GNOME 上安装 .deb 后，系统代理与 TUN 均可开关；`kill -9 velumd` 后系统代理被还原、网络恢复；连接 / 断开 WireGuard 或 OpenVPN 时内网可用。


### 🧊 MN：macOS TUN 评估改用 NetworkExtension（低优先级，未排期）

背景：D4 选择 helper + utun，换来与企业 NE VPN 共存、无内存限制、免扩展审批；代价是路由由 Velum 自己维护（作用域默认路由、换网与睡眠唤醒后的重建，见 §3.2「M1.5 实测」）。NEPacketTunnelProvider 下这些由系统负责：隧道成为主服务，物理网卡的默认路由自动转为作用域路由，隧道进程自身的连接不回流。

前提：付费 Apple 开发者账号（Network Extension entitlement、签名与公证），与 D6 一起决定。开工前需重新评估 D4 的理由：

- [ ] 实测与常用企业 VPN（NE 类）同时运行时是否互斥 / 抢占；
- [ ] 内存：macOS 上 NE 进程的内存约束与 sing-box + 规则集的稳态占用；
- [ ] 公开分发后的场景兼容：系统扩展的首次批准与升级后重新批准、MDM 管理的设备禁止安装扩展、已有其他 NE VPN 或代理工具的机器，各自如何安装、降级与提示（必要时保留 helper 方案作为回退）；
- [ ] 结论回写 D4；若迁移，复用 iOS `NEPacketTunnelProvider` 的实现（M3）。