# M2 实施计划：桌面端（Tauri v2 + React）

> 对应 ARCHITECTURE §3 / §4.5 / §7 M2。核心能力（编译、引擎、Explain、系统代理、TUN、NetworkSense）已在 `core/` 完成，M2 的主要工作是**产品化外壳**。

## 原则

- **业务逻辑只在 Go 核心实现一次**：桌面端只负责界面、托盘与进程管理，所有能力都通过 velumd 的本地接口（HTTP over UDS / Named Pipe）获取。新增的界面需求先补到 velumd 的接口上（如切换策略组、读写 overlay）。
- **不内置策略**：界面上能设置的项目都落到用户可见的配置文件（config.yaml / overlay.yaml），界面不另存一份。
- 极简视图与专业视图共用同一个核心，只是视图不同。

## 分步

| 步骤 | 内容 | 依赖 |
| :--- | :--- | :--- |
| M2.1 macOS 桌面壳 | Tauri v2 工程（Rust 壳 + React/TS/Tailwind）；托盘菜单（状态灯、总开关、模式切换、打开主窗口）；随 App 携带 velumd / vlu / velum-helper，并管理 velumd 生命周期；**极简视图**（状态、开关、网络环境提示、一键修复）；**Pro 视图**（连接流、Explain 诊断台、策略组切换、overlay 编辑、告警面板）；velumd 补齐接口：切换策略组、读写 overlay、一键修复 | 本机即可（需安装 bun） |
| ~~M2.2 远端托管~~ | **已取消（2026-10-16）**：用户直接填写订阅地址即可，不需要加密托管、签名与镜像。保留的部分：首次启动引导（在界面中填写订阅地址）；订阅拉取失败用缓存、新配置无法启动时恢复旧配置（均已实现） | — |
| M2.3 打包与安装 | App 打包；SMAppService 注册 LaunchAgent（velumd）与 LaunchDaemon（velum-helper），以及 helper 的调用方代码签名校验；自动更新（Tauri updater + 签名校验）；Developer ID 签名与公证 | **需要付费 Apple 开发者账号**（与 D6 一同决定） |
| M2.4 Windows | velumd 作为 Windows Service（wintun + auto_route）；WinINet 系统代理与启动自愈；Named Pipe ACL；Windows 版 NetworkSense | **需要 Windows 测试环境** |

## 已确认（2026-10-16）

| 问题 | 结论 |
| :--- | :--- |
| UI 设计 | 先由我按「极简 / Pro」两套视图设计一版（Tailwind，偏系统原生风格），老板看过后再调 |
| Windows 测试环境 | 有（M2.4 可测） |
| 付费 Apple 开发者账号 | 暂不开通：M2.3 先做无签名的本地打包，正式签名与公证以后再补（与 D6 一起） |
| 前端工具链 | bun（经 mise 安装，项目级 `mise.toml`）；Node 由老板的 mise 全局管理 |

## M2.1 子步骤

1. velumd 接口补齐：切换策略组、测速、读写 overlay（校验后生效）、一键修复。
2. Tauri v2 工程：Rust 壳经 UDS 访问 velumd（含 SSE 事件转发），托盘菜单；随 App 携带并管理 velumd。
3. 极简视图 → Pro 视图（连接流、Explain、策略组、overlay、告警）。

## 进度

### M2.1-1 velumd 接口补齐 ✅

- `POST /v1/groups/select`：立即切换，并持久化到 `state.json`。渲染时写入 selector 的 `default`，引擎重启（网络变化 / reload / 订阅更新）后保持。
- `POST /v1/groups/test`：组内成员测速。urltest 组正在周期测速时，sing-box 会返回空结果，此时退回逐个成员测速。status 中附带各成员最近一次延迟。
- `GET/PUT /v1/overlay`：先校验再写入；重新加载失败时自动改回原内容。
- `POST /v1/repair`：清空 DNS 缓存 → 重新检测网络环境 → 强制更新订阅与规则集 → 校正系统代理 → 节点测速，逐步报告结果。
- `vlu select / test / repair`。

### M2.1-2 桌面工程 ✅（进行中：Rust 壳编译验证）

- `desktop/`：Vite + React 19 + TypeScript + Tailwind v4；`src/api.ts` 是唯一的接口层（Tauri 中走 Rust 命令；浏览器开发模式下由 Vite 代理到 UDS，便于预览与截图）。
- 极简视图：状态灯与一句话结论、三档模式、网络环境（普通用户能看懂的措辞）、当前节点与实际使用的节点、一键修复、更新订阅。
- Pro 视图：概览 / 连接（实时，可过滤，可查看详情）/ 诊断（四步推演 + 实际连接对照）/ 策略组（选择、测速）/ 本地覆盖（编辑、校验、应用）/ 告警。
- Rust 壳：UDS 上的 HTTP 客户端（支持 chunked）、SSE 事件转发、托盘（状态、打开、系统代理 / TUN 开关、一键修复、退出）、velumd 进程管理（按需启动，独立进程组）。关闭窗口只隐藏到托盘。

### M2.1-3 接管开关与生命周期 ✅

- **系统代理与 TUN 改为两个独立开关**（原先是 proxy / sysproxy / tun 三选一），可同时开启，与 Stash 一致：
  - 遵守系统代理的应用（浏览器等）直接交出域名，不经 fake-ip、无需逐包处理，更准也更省；
  - 其余应用由 TUN 兜底；TUN 让路或暂停时，浏览器仍可通过系统代理工作。
- 配置：`system_proxy: true/false`、`tun.enabled: true/false`；旧的 `mode:` 写法仍兼容。
- 界面 / vlu 切换的开关保存在 `state.json`（与节点选择一起），重启后保持，不改 config.yaml；`vlu sysproxy|tun config` 回到 config.yaml 的设置。
- NetworkSense：Captive Portal 时两个都暂停；全局型 VPN 只暂停 TUN。界面显示「已暂停」及原因。
- 接口：`POST /v1/switches {system_proxy?, tun?}`（取值 on / off / config）；status 增加 `system_proxy_on` / `tun_on`（实际生效）和 `want_system_proxy` / `want_tun`（用户要求）。
- **退出 App（Cmd+Q、Dock、托盘）即停止 velumd**，还原系统代理 / TUN；关闭窗口只隐藏到托盘。
- 修复：sing-box 在设置 PlatformLogWriter 时会强制启用缓存文件（默认为工作目录下的 `cache.db`），其中保存的 selector 选择会覆盖 Velum 的选择。现指定到 `$VELUM_HOME/cache/singbox.db` 并在每次启动前清空。
- `vlu down` 等待 velumd 真正退出后再返回。

### M2.1-4 首次启动引导 ✅

- 订阅为空时，极简视图显示「欢迎使用 Velum」和订阅地址输入框；专业视图的订阅卡片可以随时修改。
- `PUT /v1/subscription {url}`：先拉取（不回落缓存）并解析，必须至少有一个节点，通过后才写入 config.yaml；重新加载失败时改回原文件。
- 写入 config.yaml 时**只替换对应的那一行**（保留缩进与行尾注释），其余内容逐字节保留；字段或整节不存在时插入（`config.SetFields`）。
- `GET /v1/subscription`、`vlu sub [地址]`。
- `vlu test` 不带参数时测试全部节点（测速记录按节点全局共享）。

### M2.3a 无签名本地打包 ✅

付费 Apple 开发者账号到位之前的方案（之后再换成 SMAppService + Developer ID 签名与公证）：

- `bun tauri build` → `Velum.app`（ad-hoc 签名，`signingIdentity: "-"`）。velumd、velum-helper、vlu 作为 externalBin 内置在 `Contents/MacOS/`，由 `scripts/sidecars.sh` 构建 Go 核心后放到 `src-tauri/binaries/<名字>-<三元组>`。
- 数据目录 `~/Library/Application Support/Velum`；App 启动时按需拉起 velumd（独立进程组），退出 App 时停止。
- **TUN 组件**：第一次开启 TUN 时在开关下方说明用途，确认后通过 osascript 请求一次管理员权限：
  - 把 helper 复制到 `/Library/PrivilegedHelperTools/dev.velum.helper`（root 所有，用户无法篡改）；
  - 注册 LaunchDaemon `dev.velum.helper`（`--allow-uid <当前用户>`，开机启动、崩溃自动拉起，日志 `/Library/Logs/velum-helper.log`）；
  - 安装脚本作为 osascript 参数传入，不经临时文件。
  - `GET /v1/helper` 报告是否可用、协议版本是否匹配；App 升级后协议变化时提示「需要更新」。
  - 专业视图「系统组件」可重新安装 / 更新 / 卸载（卸载前先关闭 TUN）。
- **开机启动**：用户级 LaunchAgent `dev.velum.desktop`，以 `--hidden` 启动（只显示托盘）；窗口隐藏时点 Dock 图标重新打开。
- CI：`.github/workflows/desktop.yml`（类型检查、前端构建、构建 Go 核心、clippy、cargo test）。
- 暂未做：通用二进制（目前仅 arm64）、DMG、自动更新。

## M2.4 Windows

### 设计

| 方面 | 方案 |
| :--- | :--- |
| 进程模型 | `velumd` 以 Windows Service（LocalSystem）运行（D5）。用户可编辑的文件仍在安装用户的 `%APPDATA%\Velum`（安装时经 `--home` 写入服务参数，以用户身份读写），服务自己的数据在 `%ProgramData%\Velum`（见 M2.4-7）。开发时也可以像 macOS 一样以普通用户身份运行 `velumd` / `vlu up`，此时只有系统代理，没有 TUN |
| 本地接口 | Named Pipe，HTTP 协议不变：服务为 `\\.\pipe\velumd`，以用户身份运行时为 `\\.\pipe\velumd-<SID>`。ACL 只允许 SYSTEM、Administrators 和该用户；客户端校验 Pipe 所有者（见 M2.4-7） |
| TUN | sing-box 原生 wintun + `auto_route`：RoutePlan 的包含 / 排除网段渲染为 `route_address` / `route_exclude_address`。不使用 velum-helper（Windows 没有传 FD 的必要，服务本身即有权限） |
| 系统代理 | WinINet 设置属于用户会话（HKCU），SYSTEM 写入无效。**快照、写入、还原、自愈的逻辑仍在 Go 中**：velumd 以服务运行时，用活动控制台用户的令牌启动 `velumd sysproxy <操作>` 子进程执行 WinINet 读写（`InternetQueryOption` / `InternetSetOption` 按连接选项，并通知系统刷新）；以用户身份运行时直接在进程内调用。与架构文档最初「由 Tauri App 写入」的方案相比，CLI 也能用，且不依赖 App 在线 |
| 服务权限 | 安装时设置服务 DACL，允许安装用户启动 / 停止服务（App 启动时拉起、退出时停止，与 macOS 行为一致）；失败自动重启（SCM recovery） |
| NetworkSense | `GetIpForwardTable2` + `GetAdaptersAddresses`：隧道类网卡（PPP / 隧道 / 专有虚拟网卡）判定外部 VPN；物理网卡 DNS 用于防回环 |
| 桌面端 | Rust 经 Named Pipe 访问 velumd；首次开启 TUN 时经 UAC 安装服务；开机启动写 `HKCU\...\Run` |

### 分步

1. 跨平台编译与本地接口：`pkg/ipc`（UDS / Named Pipe + ACL）；helper 仅 macOS；CI 增加 Windows 任务。
2. TUN：渲染 wintun + `auto_route`；引擎在 Windows 上不走平台接口。
3. 系统代理：WinINet 实现 + 用户会话执行器，快照 / 还原 / 自愈与 macOS 共用流程。
4. Windows Service：`velumd service install|uninstall|run`、Pipe ACL、服务 DACL、失败自动重启。
5. NetworkSense：Windows 采集。
6. 桌面端：Named Pipe 客户端、服务安装（UAC）、开机启动、sidecar 与 CI。

> 开发机是 macOS：每一步以交叉编译 + 单元测试（含 CI 中的 windows-latest）验证，真机验收在 Windows 测试环境集中进行。

### M2.4-1 跨平台编译与本地接口 ✅

- `pkg/ipc`：macOS 为 Unix socket（0600），Windows 为 Named Pipe `\\.\pipe\velumd`（go-winio；受保护 DACL：SYSTEM、Administrators、`AllowSIDs` 或当前用户）。daemon 监听、rpc 客户端、存活检查都经它。
- `pkg/helper`：协议常量与类型与平台无关；Unix 连接与客户端只在 Unix 上编译，Windows 上为返回「不支持」的空实现。
- `velumd` 平台初始化按文件拆分（`platform_darwin.go` / `platform_windows.go`）；`vlu` 在 Windows 上以 `DETACHED_PROCESS` 启动 velumd。
- CI：core 在 macOS 与 Windows 上运行 lint 与测试；`.gitattributes` 统一 LF（否则 Windows 检出后 gofmt / golden 会因 CRLF 失败）。

### M2.4-2 TUN（wintun）✅（真机待验收）

- 渲染：`Platform == windows` 时 tun 入站为 `interface_name: Velum`、`auto_route: true`、`route_address` = RoutePlan 的路由（已扣除排除网段）。sing-tun 在 Windows 上自行设置 wintun 地址、接口 DNS（TUN 地址的下一个，即劫持 DNS 的地址）与路由；出站经 `auto_detect_interface` 绑定物理网卡，不会绕回 TUN。
- 引擎：`engine.TUNConfig.Opener` 为空时不使用平台接口，由内核自建 TUN；`TUNName()` 返回配置的网卡名（供 NetworkSense 识别自己的网卡）。daemon 只在 macOS 上使用 velum-helper。
- 未启用 `strict_route`：它会用 WFP 阻断其他网卡上的 DNS，与「分流型 VPN 共存」冲突。DNS 泄漏问题待真机验收时评估。

### M2.4-3 系统代理（WinINet）✅（真机待验收）

- `sysproxy.WinManager`：与 macOS 相同的快照 / 写入 / 还原 / 自愈流程（快照文件共用，增加 `windows` 字段），平台无关、在 macOS 上即可单元测试。接管时手动代理指向 Velum，并关闭 PAC 与自动检测（还原时写回）。
- 豁免转换（`WinBypass`）：域名与通配原样保留；IPv4 网段转为 WinINet 通配（`10.0.0.0/8` → `10.*`；不按整字节对齐的网段按下一字节展开，如 `172.16.0.0/12` → `172.16.*` … `172.31.*`；也接受 macOS 简写 `169.254/16`）；IPv6 网段 WinINet 不支持，忽略。
- 后端：`WinINetBackend` 在进程内调用 `InternetQueryOption` / `InternetSetOption`（按连接选项，之后通知 `PROXY_SETTINGS_CHANGED` / `SETTINGS_CHANGED` / `REFRESH`）；`UserSessionBackend` 供服务使用：以安装用户所在会话（优先控制台会话）的令牌启动 `velumd sysproxy read|write` 子进程。没有已登录用户时报错，不写快照。
- `vlu down` 在 velumd 不在时于本地执行自愈（Windows 同样支持）。

### M2.4-4 Windows Service ✅（真机待验收）

- `velumd service install --home <目录> --allow-sid <SID>`（管理员，App 经 UAC 调用）：先停止已有服务与以用户身份运行的 velumd，把自身复制到 `%ProgramFiles%\Velum\velumd.exe`（只有管理员可写，避免用户可写位置的程序被替换后以 SYSTEM 运行），注册**手动启动**的服务 `Velum`（LocalSystem），设置失败重启（1s / 5s / 30s），设置服务 DACL 允许安装用户启动 / 停止，然后启动。
- `velumd service uninstall | start | stop | status`：start / stop / status 只需安装用户权限；status 输出 JSON，`outdated` 表示已安装程序与 App 内置的 velumd 内容不同（App 升级后需要重新安装）。
- 服务运行：经本地接口停止（App 退出、`vlu down`）时正常退出，SCM 不会重启；崩溃或启动失败返回非 0 退出码，由 SCM 自动重启。
- 以普通用户运行 velumd 时设置 `TUNUnavailable`：开启 TUN 显示为「已暂停」并说明需要安装服务；`/v1/helper` 在 Windows 上报告服务是否可用。
- `vlu up` 在已安装服务时启动服务，否则以当前用户启动 velumd（只有系统代理）。

### M2.4-5 NetworkSense（Windows）✅（真机待验收）

- 采集（`netsense.WindowsSnapshot`）：`GetAdaptersAddresses`（网卡名用 FriendlyName，与 Go `net.Interface` 一致）+ `GetIpForwardTable2`；NRPT（本地与组策略的 `DnsPolicyConfig`）作为系统 DNS 分流；DHCP DNS 读注册表 `Tcpip\Parameters\Interfaces\{GUID}\DhcpNameServer`。组装逻辑（`BuildWindowsSnapshot`）与平台无关，在 macOS 上单元测试。
- 隧道判定：网卡类型为 PPP / 专有虚拟（wintun 等）/ 隧道封装，或描述含 TAP / WireGuard / VPN 等通用技术词（不列具体产品）。快照新增 `Tunnels`，名称前缀判定（macOS）照旧。
- 主接口：Windows 没有「主网络服务」，取最优默认路由所在网卡：0/1 + 128/1 比 0/0 更具体，优先；同类比较度量（路由 + 接口）。Velum 自己的 wintun 不参与。
- 分流型 VPN 的 DNS：网卡的连接特定 DNS 后缀作为该 VPN 的域名，但**不**交给系统解析器（wintun 的接口 DNS 指向 Velum，系统解析器可能绕回）。编译器在 Windows 上为每个分流型 VPN 生成经该 VPN 出站询问其 DNS 的服务器与规则。NRPT 域名仍交给系统解析器（有专用服务器，不会绕回）。
- Captive 探测绑定物理网卡：`IP_UNICAST_IF` / `IPV6_UNICAST_IF`。

### M2.4-6 桌面端 ✅（真机待验收）

- Rust 壳：本地接口在 Windows 上经 Named Pipe（以文件方式打开，忙时重试）；Velum 目录为 `%APPDATA%\Velum`。启动时已安装服务则 `velumd service start`，否则以独立进程（`DETACHED_PROCESS`）启动用户态 velumd。
- TUN 组件（Windows = Velum 服务）：安装 / 卸载经 `ShellExecuteEx("runas")` 以管理员身份运行 `velumd service install|uninstall`（UAC 确认一次）；失败原因由 velumd 写入 `logs\service.log` 后显示。卸载后以用户身份重新启动 velumd，App 继续可用（只有系统代理）。状态由 App 执行 `velumd service status` 并结合 `/v1/helper` 判断（含「需要更新」）。
- 开机启动：`HKCU\...\Run` 的 `Velum` 值（`--hidden`）。托盘在 Windows 上用彩色应用图标。
- 界面：TUN 组件的名称、说明、日志位置按平台显示，其余不变。
- 打包：`tauri.windows.conf.json`（NSIS，按用户安装；只携带 velumd / vlu）。`scripts/sidecars.sh` 按目标三元组交叉编译 Go 核心，不再依赖 make（Windows 开发机通常只有 Git Bash）。
- CI：desktop 在 macOS 与 Windows 上运行类型检查、前端构建、clippy、cargo test。

## M2.4 真机验收清单（Windows 测试环境）

1. CI 两个平台全绿（本机无法运行 Windows 测试与 Windows 资源编译）。
2. `bun tauri build` 产出安装包；未安装服务时：系统代理开 / 关、退出 App 后还原；`taskkill /f` 杀掉 velumd 后重启 App 能自愈。
3. 开启 TUN → UAC → 服务安装并启动；浏览器以外的程序（如 `curl` 不带代理）经 Velum；退出 App 后服务停止、wintun 消失、路由还原。
4. 服务模式下的系统代理（用户会话子进程）：开 / 关、豁免列表（含网段转换）在「Internet 选项」中可见。
5. NetworkSense：连接分流型 / 全局型 VPN 时识别正确（`vlu status`）；Captive Portal 让路。
6. App 升级后显示「需要更新」并能更新服务。

## M2.4-7 代码审查修复 ✅（真机待验收）

| 问题 | 修复 |
| :--- | :--- |
| 服务（SYSTEM）在用户可写目录中写文件，可被目录联接利用提权 | 服务的数据（日志、缓存、状态、快照）移到 `%ProgramData%\Velum`：启动与安装时检查该目录，任何一项是重定向点或所有者不是 SYSTEM / Administrators 就整个删除重建，并设置受保护的 DACL（SYSTEM / 管理员可写，安装用户只读）。用户目录中的 config.yaml、overlay.yaml、订阅文件、本地 provider 经 `pkg/userfs` **以用户身份**读写（`winuser.Impersonate` 在独占线程上模拟用户令牌），用户借联接或配置中的任意路径也只能访问自己本来就能访问的文件。内核读取的规则集文件只来自 Velum 的下载缓存（数据目录内）。 |
| 用户会话子进程未指定桌面，加载 wininet 失败 | `winuser.Run` 直接调用 `CreateProcessAsUser`，桌面为 `winsta0\default`，使用用户环境块；管道句柄创建期间持有 `ForkLock`，超时（15 秒）终止子进程 |
| 全局固定的 Pipe 名可被抢占冒充；多用户冲突 | 以用户身份运行的 velumd 使用 `\\.\pipe\velumd-<SID>`，服务使用 `\\.\pipe\velumd`；客户端（Go `ipc.Dial`、Rust `winpipe`）依次尝试，连接后检查 Pipe 对象的所有者必须是 SYSTEM / Administrators / 当前用户（普通用户无法创建所有者为前两者的对象）。服务端以 `FILE_CREATE` 创建首个实例，名字被占用时直接失败 |
| 非 0 退出码不会触发 SCM 重启 | 安装时设置 `SetRecoveryActionsOnNonCrashFailures(true)` |
| 每次查询状态都在持锁时启动子进程 | 实际生效代理在锁外读取，缓存 10 秒，修改系统代理后失效 |
| Rust 客户端在 Windows 上没有超时 | 请求限时 60 秒：看门狗线程超时后 `CancelIoEx` 取消挂起的读写 |
| `0.0.0.0/0` 豁免变成 `*`（等于关闭代理） | 忽略 /0 |
| 接管期间改监听地址后，快照中的地址过期，崩溃后自愈失效 | Apply 时同步更新快照中的 Velum 地址（macOS 与 Windows） |
| WinINet 代理地址解析过于简单 | `ParseWinServer`：支持 `[::1]:port`、`http://` 前缀、按协议分开的写法；写入时 IPv6 加方括号 |
| 其他平台开启 TUN 没有明确提示 | 非 macOS / Windows 上 TUN 标为不可用 |

> 安装失败原因写入 `%ProgramData%\Velum\logs\service.log`（App 按修改时间判断是否为本次失败）。从用户模式切换到服务模式时，`state.json`（策略组选择、开关）不会迁移，需要重新选择一次。

## 已知问题 / 待办（Windows）

- DNS 泄漏：未开启 `strict_route`（与分流型 VPN 共存冲突），Windows 的多宿主 DNS 可能同时经物理网卡查询。真机评估后决定是否按条件开启。
- 拨号 / VPN 连接（RAS）各自的代理设置未处理；ARM64 安装包未构建；代码签名（SmartScreen）待定。
