# MN 实施计划：macOS TUN 迁移到 NetworkExtension

> 对应 ARCHITECTURE §3.2 / D4 与路线图 MN。验证过程与数据见 `spikes/mn-netext/README.md`（F1～F9）。
> 本计划只涉及 macOS；Windows、Android 不变，iOS（M3）将复用这里的扩展实现。

## 为什么现在迁移

D4 选择「helper + utun」的三个理由，验证后都不再成立：

| D4 的理由 | 验证结论（2026-10-28） |
| :--- | :--- |
| 与企业 NE VPN 共存 | ✅ 与另一全局 packet tunnel（Stash）、分流型企业 VPN 同时运行均正常；更具体的路由优先，后启动的隧道不会挤掉先启动的 |
| 无内存限制 | ✅ 真实订阅下稳态约 75～117MB，未遇到 iOS 式上限 |
| 免扩展审批 | 需要两次批准（系统扩展、VPN 配置）；MDM 托管设备策略为「用户批准」，未被拦截；升级替换无需重新批准 |

迁移的收益正是 helper 方案反复出问题的地方：

- **崩溃与退出后自动还原**：扩展被 `kill -9` 后约 0.3s，系统撤销隧道的路由与 DNS，无残留（「残留 DNS / 代理」一类问题从机制上消失）。
- **路由由系统维护**：隧道成为主服务，物理网卡的默认路由自动转为作用域路由；换网、睡眠唤醒、其他 VPN 退出后不再需要 helper 补路由（M1.5 实测问题）。
- **DNS 不被企业 VPN 架空**：实测企业 VPN 会把「连接时的系统 DNS」钉到物理网卡，helper 方案下 Velum 的 TUN 内 DNS 因此不可达（ARCHITECTURE §4.3.2 第 5 条）。NE 隧道的解析器绑定隧道接口、按作用域发出查询，不受影响（F9）。
- **不再需要 root helper**：攻击面更小，安装时不再需要「登录项」中的后台项。

代价：构建链更复杂（Xcode 构建扩展、Developer ID 描述文件、每次改动都要公证）；首次使用多两次系统批准；核心进程模型要改。

## 原则

- **核心不变**：扩展进程内运行完整的 `daemon.Daemon`，入口为 `core/mobilebind`（与 Android 相同）；编译器、引擎、Explain、策略组、一键修复都不分叉。
- **界面与 CLI 接口不变**：仍是 HTTP over UDS（REST + SSE），只是 socket 换了位置。
- **平台层只做 Go 做不到的事**：隧道网络参数、系统代理、按需连接、扩展安装由 Swift 实现。
- **保留 helper 作为回退**：装不上系统扩展（MDM 禁止、用户拒绝批准）时仍可用，过渡期两条路径并存。

## 设计

| 关注点 | 方案 |
| :--- | :--- |
| 进程模型 | Velum.app（Tauri，界面、托盘、安装扩展）+ 系统扩展（`NEPacketTunnelProvider`，运行核心）。velumd LaunchAgent 不再运行核心（见待决定 Q1） |
| 核心入口 | `mobilebind.Start(home, host)`；Swift 实现 `Host`：`OpenTun` 设置隧道网络参数后把 utun FD 交给核心（扫描 `UTUN_OPT_IFNAME`，交出 `dup` 的副本）；网卡名按 FD 读取真实的 `utunN`（F2，已在 `tunname_darwin.go` 实现） |
| 路由 | 核心的路由覆盖超过一半 IPv4 即视为全局接管，只声明默认路由（F6）；**不使用 `excludedRoutes`**：排除网段会变成经物理网卡的同前缀路由，挤掉其他 VPN 的同名路由（F8）。排除改由引擎规则直连；其他 VPN 的网段本就更具体，系统自会送进对方 |
| DNS | `NEDNSSettings`（隧道内 DNS 地址，`matchDomains = [""]`），由系统注册、绑定隧道接口；隧道停止时系统撤销。overlay 的内网域名在检测到「不声明域名的 VPN DNS」时交给对方（ARCHITECTURE §4.3.2 第 4 条，已实现） |
| 系统代理 | 隧道网络参数里的 `NEProxySettings`（HTTP / HTTPS 代理、豁免列表），隧道停止时系统撤销，取代 `networksetup` 写入与快照还原（需验证，见 MN.5） |
| IPv6 | 订阅支持 IPv6 时声明 `NEIPv6Settings` 与默认路由；关闭时不声明，并沿用握手前拒绝 IPv6 的规则（已实现） |
| NetworkSense | 扩展内读取 `SCDynamicStore`：物理服务的接口、网关、DNS（不取 `State:/Network/Global/IPv4`，有其他全局隧道时那是对方的 utun，F5）；识别其他 VPN 的网段与解析器（含不声明域名的本机 DNS 代理），供编译器使用 |
| 核心自身的解析 | 扩展进程里系统解析器可能不可用（F1）：拉订阅、规则集等 Go 侧请求改用物理网络 DNS，不经 `getaddrinfo` |
| 出站绑定 | 引擎照常绑定物理网卡（`auto_detect_interface`）；隧道为主服务时系统维护作用域默认路由，绑定的出站可达 |
| 数据目录 | 扩展以 root 运行，App Group 容器在 `/private/var/root/Library/Group Containers/`（F4）。配置与状态由 App 经 IPC 写入，首次启动从 `~/Library/Application Support/Velum` 迁移 |
| 崩溃与重连 | 系统不会自动重连（实测）：App 监听 `NEVPNStatusDidChange`，非用户操作的断开按退避重启隧道；可选 On-Demand 规则 |
| 安装与升级 | App 首次开启 TUN 时提交 `OSSystemExtensionRequest`，界面引导到「登录项与扩展 → 网络扩展」；`CFBundleVersion` 每次发布递增，升级时替换（无需重新批准） |
| 签名与发布 | App 与扩展各需带 Network Extension（`packet-tunnel-provider-systemextension`）与 System Extension 能力的 Developer ID 描述文件；扩展包名必须等于 bundle ID、`CFBundlePackageType = SYSX`（F4）；整个 App 公证 |

## 分步

| 步骤 | 内容 | 验收 |
| :--- | :--- | :--- |
| MN.1 构建骨架 | `desktop/extension/`：Xcode 工程（xcodegen）构建系统扩展，链接 `gomobile bind -target macos` 产出的核心；Tauri 打包后嵌入 `Contents/Library/SystemExtensions` 并重签；CI 获取描述文件、签名、公证整个 App | CI 产出的 App 在干净机器上能激活扩展 |
| MN.2 核心进扩展 + IPC | 扩展内启动 daemon；确定 socket 位置（Q2）；界面与 `vlu` 连上扩展内的核心；数据目录迁移 | 界面全部功能可用；`vlu status` 正常 |
| MN.3 TUN | `OpenTun` 的路由、DNS、IPv6、MTU；全局接管与分流接管；开关与状态（`NEVPNStatus`） | 与 helper 方案的 TUN 行为一致；回归 M1.5 的验收项 |
| MN.4 NetworkSense | 扩展内的网络快照与其他 VPN 识别（F5）；Go 侧解析改用物理 DNS（F1） | 连企业 VPN 时内网域名可解析、内网与外网均正常；换网后自动重载 |
| MN.5 系统代理 | `NEProxySettings` 与仅系统代理模式（Q1）；豁免列表来自 overlay | 开关系统代理不再写 `networksetup`；退出 / 崩溃后代理自动撤销 |
| MN.6 生命周期 | 安装与批准引导、扩展替换、意外断开后重连、睡眠唤醒、卸载（停用扩展、删除 VPN 配置） | 首次安装到可用 ≤ 3 步；`kill -9` 扩展后数秒内自动恢复 |
| MN.7 回退与上线 | 激活失败（MDM 禁止、拒绝批准）时回退 helper 并提示；先作为设置里的实验开关发布，稳定后改为默认 | 两条路径都通过回归；默认切换后保留 helper 至少一个版本 |

## 待决定

| 编号 | 问题 | 建议 |
| :--- | :--- | :--- |
| Q1 | 只开系统代理（不开 TUN）时核心在哪里运行。扩展进程随隧道启停，隧道不连接时核心不运行 | **已定**：隧道始终连接，只开系统代理时不声明路由、只下发 `NEProxySettings`，核心仍在扩展里，进程模型只有一种。参照：Stash 的系统扩展同时提供隧道与系统代理（调用 `setProxySettings:`），仅系统代理模式下其 VPN 同样处于连接状态（2026-10-28 实测）。注意命令行工具（Claude Code、git 等）不读系统代理，仅代理模式对它们无效，这一模式是次要场景 |
| Q2 | 界面 / `vlu`（用户）与扩展（root、沙盒）之间的 IPC | 先试用户侧 App Group 容器里的 UDS（扩展按用户名定位容器）；不可行时改用回环 TCP + 令牌；XPC（`NEMachServiceName`）作为最后选择（需要把 HTTP 接口桥接到 XPC） |
| Q3 | helper 回退保留多久 | 至少保留到 NE 成为默认后的下一个版本；MDM 禁止系统扩展的机器长期需要它 |
| Q4 | 本地开发效率：开发描述文件不含 systemextension 权限，每次改动都要 Developer ID 签名并公证（约 30s） | 接受；核心逻辑在 `vlu` / 单元测试里调试，只有平台层改动才走扩展 |

## 风险

- **企业 VPN 的私有行为**：实测的企业 VPN 会钉住系统 DNS、注册匹配所有域名的本机 DNS 代理、在网络变化时重新应用配置。NE 下已验证可共存，但不同版本或不同产品可能有差异，需要 NetworkSense 持续适配。
- **批准流程的流失**：两次系统批准对非技术用户是门槛，引导文案与截图要做好；拒绝批准时自动回退 helper。
- **公证依赖网络**：CI 与本地签名都依赖 Apple 的公证和时间戳服务，网络受限时需要重试（今天本地遇到过时间戳服务不可达）。
- **两条 TUN 路径并存**：过渡期 helper 与 NE 都要维护和回归，尽量缩短。
