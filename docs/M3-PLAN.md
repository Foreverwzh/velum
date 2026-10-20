# M3 实施计划：移动端（先做 Android）

> 对应 ARCHITECTURE §3.5 / §7 M3。iOS 部分仍卡在 D6（需付费 Apple 开发者账号），本计划先做 Android。

## 原则

- **业务逻辑只在 Go 核心实现一次**：与桌面端相同，Android 上运行的仍是完整的 `daemon.Daemon`（编译、引擎、Explain、策略组、订阅、一键修复），界面经同一套本地接口访问。
- **平台层只做 Go 做不到的事**：建立 VPN（`VpnService.Builder`）、前台通知、VPN 授权、连接归属查询等，由 Kotlin 实现，经 gomobile 接口回调。
- **不内置策略**：按应用排除等设置写入 config.yaml，与桌面端一致。

## 已确认（2026-10-19）

| 问题 | 结论 |
| :--- | :--- |
| 测试环境 | 只用模拟器（本机安装 Android 模拟器，arm64 系统镜像） |
| 最低版本 | Android 10（API 29）：可用 `getConnectionOwnerUid` 识别连接所属应用 |
| 包名 | `io.github.foreverwzh.velum` |

## 设计

| 关注点 | 方案 |
| :--- | :--- |
| 进程模型 | 单进程：Flutter 界面、VpnService、Go 核心都在 App 进程内。App 启动时启动 daemon；VpnService 作为前台服务保活 |
| Go ↔ Kotlin | `gomobile bind` 生成 AAR（`core/mobilebind`）。导出 `Start / Stop / TunRevoked` 等少量函数；Kotlin 实现 `Host` 接口供 Go 回调（建立 / 关闭 VPN、查连接归属） |
| 界面 ↔ 核心 | **与桌面端相同的 HTTP over UDS**（REST + SSE），socket 位于 App 私有目录。Dart 的 `HttpClient` 支持 Unix socket，界面不经 Kotlin 中转，接口与桌面端完全一致。（ARCHITECTURE 原写「Flutter 通过 FFI 直接调用」，此处修订：复用现有接口，无需另建一套 FFI 接口） |
| TUN | 复用 daemon 的 `TUNOpener` 抽象：macOS 由 helper 返回 utun FD，Android 由 VpnService 返回 `establish()` 的 FD。路由、地址、DNS、MTU 均来自编译器的 `RoutePlan` |
| 防回环 | VpnService 排除 Velum 自身（`addDisallowedApplication(自身包名)`），核心的出站连接自然走物理网络，不需要逐个 `protect()`。因此 TUN 使用 gVisor 栈：system 栈在本进程监听 TCP、回包要经内核路由回 TUN，而本进程不走 VPN，握手无法完成 |
| 系统代理 | Android 没有可写的全局系统代理；系统代理开关在 Android 上不可用。（可选：VPN 开启时用 `setHttpProxy` 向应用提示代理） |
| 网络与 DNS | Android 没有 `/etc/resolv.conf`，内核的「系统 DNS」不可用：App 跟踪物理网络（`NOT_VPN`），经 `Host.Network` 报告网卡与 DNS，复用 NetworkSense 的快照流程，编译器把系统 DNS 改为该网络的 DNS；网络切换时重新加载。同一时间只能有一个 VPN，不存在 VPN 共存 |
| 按应用排除 | config.yaml `tun.exclude_apps` / `tun.include_apps`（包名），由 VpnService 的 `addDisallowedApplication` / `addAllowedApplication` 实现 |
| 授权与撤销 | 开启 TUN 前由界面调用 `VpnService.prepare` 申请授权；系统断开（`onRevoke`，或被其他 VPN 取代）时通知 daemon 关闭 TUN 开关 |

## 分步

| 步骤 | 内容 |
| :--- | :--- |
| M3.A1 骨架 | `core/mobilebind` + gomobile AAR（含 sing-box）；`mobile/` Flutter 工程（Android）；App 内启动 daemon，界面经 UDS 读取 `/v1/status` |
| M3.A2 VPN | VpnService 作为 TUNOpener；前台通知；授权、撤销；模拟器中浏览器流量经 Velum 转发 |
| M3.A3 平台能力 | 连接归属（包名）用于进程规则与连接列表；按应用排除；网络切换（Wi-Fi ↔ 蜂窝）后引擎正常 |
| M3.A4 界面 | 极简视图（状态、总开关、当前节点、一键修复、更新订阅）、首次启动引导；Pro 视图（策略组、连接、诊断、告警） |
| M3.A5 打包与 CI | release APK（arm64 / x86_64）；CI 增加 Android 构建 |

## 进度

### M3.A1 骨架 ✅ / M3.A2 VPN ✅（2026-10-19，模拟器 Android 16 / API 36 验证）

- `core/mobilebind`：`Start / Stop / TunRevoked / Socket / LogPath / Version`，`Host` 接口（`OpenTun / CloseTun / Network`）。
  - 依赖 `golang.org/x/mobile` 固定在 `4776eadac327`（2026-08-21），与现有 x/sys 一致；gomobile / gobind 需安装同一版本。
  - `mobile/scripts/core.sh` 生成 `android/app/libs/velum.aar`（arm64 + x86_64，约 33MB）。
- 核心改动：
  - 编译器：Android 上系统 DNS 始终改用平台报告的网络 DNS（无论是否开启 TUN）；未获取到时告警。
  - daemon：关闭 NetworkSense 时，Android 仍跟踪网络 DNS（不做 Captive 等其他感知）。
  - 渲染：Android 不开 `auto_detect_interface`（Android 11+ 禁止应用使用 netlink 监听网卡），TUN 用 gVisor 栈。
- `mobile/`（Flutter，Android）：
  - Kotlin：`VelumApp` 启动核心（后台线程）；`Core` 实现 `Host`，用 `registerBestMatchingNetworkCallback(NOT_VPN)` 跟踪物理网络（默认网络回调会报告 Velum 自己的 VPN，导致核心误判为外部全局 VPN 而反复暂停 TUN）；`VelumVpnService` 前台服务（`systemExempted`）建立 VPN、`onRevoke` 通知核心；`MainActivity` 提供 VPN 授权与通知权限。
  - Dart：`lib/api.dart` 经 UDS 访问核心（与桌面端接口一致）；最小界面（状态、TUN 开关、订阅地址）。
- 模拟器验证：订阅保存（拉取校验）→ 开启 TUN → 系统授权框 → VPN 建立（tun0，IPv4 + IPv6，Velum 自身被排除）；经 TUN 的连接按规则分流（`tun-in → Proxy → DIRECT` 有下行；命中 REJECT 的域名被拒绝）；关闭 / 重新开启；App 重启后按保存的开关自动建立 VPN；在系统设置中「忘记 VPN」后核心关闭 TUN 并保存。
- 测试环境注意：模拟器的 DNS 经 Mac 解析，Mac 上开着 Stash 的 fake-ip 时会拿到 198.18.x 地址，外网测试结果不可信；用本机 HTTP 服务（10.0.2.2）验证链路。

### M3.A3 平台能力 ✅（2026-10-19，模拟器验证）

- **连接归属**：`engine.OwnerFinder`（TUNOpener 可选实现）→ sing-box 平台接口的 `FindConnectionOwner`；Android 由 Kotlin 调用 `getConnectionOwnerUid` + `getPackagesForUid`（需 `QUERY_ALL_PACKAGES`）。包名写入进程名，`PROCESS-NAME,<包名>` 与 overlay `processes` 直接生效；连接记录显示应用包名。共享 uid（如系统 uid 1000）以 uid 名称（`android.uid.system:1000`）显示。
- **按应用分流**：`tun.include_apps` / `tun.exclude_apps`（包名）→ `RoutePlan` → `TUNRequest` → `addAllowedApplication` / `addDisallowedApplication`（未安装的包跳过）；其他平台设置时告警。`TUNOpener.OpenTUN` 改为接收 `engine.TUNRequest` 结构体。
- **DoT 探测**：发往 TUN 内 DNS 地址的非 DNS 连接（Android「私人 DNS」的 853 端口）直接拒绝，系统立即回落到普通 DNS（各平台通用）。
- **始终开启的 VPN**：系统直接拉起 VPN 服务时启动核心并打开 TUN（`Mobilebind.tunRequested`）。
- **修复（各平台）**：重新加载时只比较内核配置，路由 / 系统 DNS / 应用列表变化（由 helper / VpnService 执行，不在内核配置里）不会重启引擎，修改不生效；现同时比较 `RoutePlan`。
- 模拟器验证：连接记录显示 `com.android.shell`、`com.google.android.gms` 等；排除 shell 后 VPN 的 uid 范围不含 2000，其流量不经 Velum；Wi-Fi 关闭 / 开启时核心识别物理网络切换（wlan0 ↔ eth0），经 VPN 的流量持续可用；应用未运行时打开「始终开启的 VPN」，系统拉起服务、核心启动并建立 VPN。

### M3.A4 界面 ✅（2026-10-19，模拟器验证）

- 首页（极简视图）：状态与一句话结论（与桌面端 health 口径一致，去掉系统代理分支）、「连接 / 断开」大按钮（开启前申请 VPN 授权与通知权限）、首次引导（填写订阅）、网络环境、分流组（底部面板选择成员、测速）、一键修复（显示每步结果）、更新订阅。
- 专业模式（设置中开启，保存在 Velum 目录的 `ui.json`，只影响界面）：底部导航增加「连接」「诊断」。
  - 连接：最近 300 条，按域名 / 应用 / 出站过滤，详情面板显示命中规则、规则来源、出站链、流量、首字节、错误。
  - 诊断：DNS → 规则 → 出站推演（Android 没有系统代理，省略该步），可展开全部规则及未命中原因，并与实际连接对照。
- 设置：订阅（节点 / 策略组 / 规则数与上次更新失败原因）、按应用分流、本地覆盖（overlay.yaml 编辑，先校验再应用）、告警（按类别）、日志（核心与界面同进程，直接读文件）、关于。
- 按应用分流页：两种模式（所选应用不经 Velum / 只有所选应用经 Velum），搜索、显示系统应用、应用图标按需加载；保存经新接口 `GET/PUT /v1/tun/apps` 写入 config.yaml 的 `tun.include_apps / exclude_apps`（`config.SetLists`：保留注释，原多行写法改为单行），核心重新加载并重建 VPN。
- 原生层：`apps`（已安装应用）、`appIcon`（PNG）方法通道。

### M3.A5 打包与 CI ✅（2026-10-19）

- Go 核心编译 32 / 64 位 ARM 与 x86_64 三个架构；`flutter build apk --release --split-per-abi` 每个架构一个 APK（约 24MB，原生库压缩存放）。
- 发布签名读取 `android/key.properties`（不入库），没有时用调试签名；生成密钥的方法见 `mobile/README.md`。
- R8：gomobile 的 AAR 自带保留规则（`go.**`、`io.github.foreverwzh.velum.core.**`），发布版中 Go → Kotlin 回调正常（模拟器验证：VPN、连接归属）。
- CI：`.github/workflows/mobile.yml`（Ubuntu）：安装与 go.mod 同版本的 gomobile → 编译核心 AAR → `flutter analyze / test` → 构建拆分 APK 并上传为产物。

## 待办

- 真机验收（目前只在模拟器 Android 16 上验证）：真实代理节点、Wi-Fi ↔ 蜂窝切换、息屏 / 省电模式下 VPN 保持、Android 10 / 11 的网络跟踪回退路径（`registerNetworkCallback`）。
- 正式发布签名（需要生成并保管密钥）。
- 可选：VPN 开启时用 `setHttpProxy` 向应用提示 Velum 的 HTTP 代理；快捷设置磁贴（Quick Settings Tile）开关。
