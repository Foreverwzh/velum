# ML 实施计划：Linux

> 对应 ARCHITECTURE §3.7 / D7 与路线图 ML。
> **阶段一只做服务器（无图形环境）**；桌面端的方案记录在阶段二，暂不排期。

## 范围

| | 阶段一：服务器 | 阶段二：桌面（不排期） |
| :--- | :--- | :--- |
| 用户 | 云主机、家用服务器、无图形环境的开发机 | Linux 工作站 |
| 界面 | `vlu` 命令行 | Tauri 窗口与托盘 |
| 接管方式 | 本地端口（默认）、TUN（需明确开启） | TUN、系统代理 |
| 安装 | 压缩包 + 安装脚本 | .deb，之后 AppImage |

阶段一的目标环境：Ubuntu 22.04 / 24.04、Debian 12，x86_64 与 arm64，systemd。其他 systemd 发行版「能跑但不保证」；不支持非 systemd 系统的服务安装（仍可前台运行）。

## 现状（2026-11-05 核对代码）

| 项目 | 现状 |
| :--- | :--- |
| 编译 | `velumd`、`vlu` 可交叉编译到 linux/amd64、linux/arm64（`CGO_ENABLED=0`），`go vet` 无报错。**测试从未在 Linux 上跑过，也没有实际运行过** |
| CI 与发布 | `core.yml` 只跑 macOS、Windows；`release.yml` 不产出 Linux 包 |
| TUN | `platform_other.go` 标为不可用；`render.go` 只有 Windows 分支开 `auto_route`，Linux 会落入「由平台创建 TUN」的分支 |
| 服务 | 没有；`vlu up` 在 Unix 上直接以子进程启动 `velumd` |
| 本地接口 | UDS，权限固定 `0600`；`Paths.ServiceSocket` 的回落机制已有，但只有 Windows 使用 |
| 目录 | 默认 `~/.config/Velum`（`os.UserConfigDir`），依赖 `$HOME`；`Paths.WithData` 已支持把数据与配置分开 |
| 网络感知 | 没有 Linux 实现（`NetSnapshot` 为 nil 时不启用，流程已支持） |
| 更新检查 | `update.AssetSuffix` 没有 Linux 条目 |
| 监听地址 | `listen` 接受任意地址，mixed 入站**没有认证**：改成 `0.0.0.0` 就是开放代理（服务器上必须处理，见下） |

## 原则

- **核心不分叉**：只增加平台层和渲染分支，订阅转换、规则、Explain、策略组不变。
- **默认不动网络**：装好后只有本地端口工作。TUN 必须明确开启，并经过试运行确认。
- **不能把人锁在服务器外面**：任何会改路由的操作都要有不依赖 velumd 的自动回退。
- **最小权限**：专用用户 + 所需 capabilities，不以 root 运行。
- **行为可解释**：TUN 不可用、被回退、配置被拒绝时，`vlu status` 要说明原因。

## 阶段一设计

| 关注点 | 方案 |
| :--- | :--- |
| 进程与权限（D7） | systemd 系统服务，专用系统用户 `velum`，`AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE`。不用 `User=安装用户`：服务器上服务不应绑定到某个登录用户 |
| 服务加固 | `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`、`ReadWritePaths=/etc/velum /var/lib/velum`、`DeviceAllow=/dev/net/tun`、`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK`；`Restart=on-failure` |
| 目录 | `/etc/velum`：`config.yaml`、`overlay.yaml`、`profiles.yaml`（含订阅地址，属主 `velum`，`0700`）；`/var/lib/velum`：日志、缓存、状态（`Paths.WithData`）；`/run/velum/velumd.sock`：本地接口（`RuntimeDirectory=velum`） |
| 本地接口权限 | socket 改为 `0660`、组 `velum`（给 `ipc.ListenOptions` 增加权限项，其他场景仍是 `0600`）。Linux 的 `socketPaths` 返回 `ServiceSocket=/run/velum/velumd.sock`，普通用户的 `vlu` 先试自己目录的 socket，再回落到服务；加入 `velum` 组才能操作 |
| 服务管理 | `velumd service install [--add-user <用户>] \| uninstall [--purge] \| start \| stop \| status`，后三者是 `systemctl` 的薄封装；install 需要 root，创建用户、目录、复制程序到 `/usr/local/bin`、写 unit。`vlu up` / `vlu down` 在服务已安装时走 systemd（`startService` 已有此扩展点） |
| 本地端口的安全 | **以服务运行时拒绝无认证的非回环 `listen`**，启动报错并说明。给 mixed 入站增加可选的 `listen_auth`（用户名、密码，映射 sing-box `users`），配置后才允许对外监听。桌面平台的行为不变 |
| 无 TUN 时如何使用 | 新增 `vlu env`，输出 `export http_proxy=…`；文档给出 apt、Docker daemon、systemd 服务的代理设置方法。**不实现桌面代理后端**，服务器上 `system_proxy` 显示为不适用 |
| TUN | `render.go` 增加 Linux 分支：sing-box 原生 tun + `auto_route`，`route_address` 取 `RoutePlan`（扣除排除网段后的结果），与 Windows 思路一致；`auto_redirect`、`strict_route`、入站回包处理由 S1～S6 决定。`daemon.go` 的 opener 只在 darwin 使用 helper，Linux 不需要改 |
| TUN 试运行 | 见下节 |
| DNS | 同时支持 systemd-resolved 和直接写 `/etc/resolv.conf` 的系统。优先由路由层劫持 53 端口，不改系统文件（S4 验证）；必须改时要能在崩溃后还原 |
| 网络感知 | 新增 `netsense.LinuxSnapshot`：netlink 取网卡、路由，resolved 取按链路域，识别 WireGuard、Tailscale、OpenVPN。Captive 探测在服务模式下关闭。服务模式下默认不因其他全局 VPN 让出接管（`yield_to_full_vpn` 默认关闭，可手动开启） |
| 分发 | `Velum-<版本>-linux-{x64,arm64}.tar.gz`（静态链接的 `velumd`、`vlu`、`install.sh`）+ `SHA256SUMS`。`install.sh` 校验后调用 `velumd service install`，也能离线安装本地压缩包（部分服务器访问 GitHub 不稳定） |
| 更新 | `AssetSuffix` 增加 Linux 条目，`vlu status` 提示新版本。升级命令 `vlu upgrade` 校验并替换程序后重启服务；不做后台自动更新：服务器不应自行改变运行中的程序 |

### TUN 试运行：防止锁在外面

类似 `netplan try`：

1. `vlu tun on --try [120s]`：先注册独立于 velumd 的回退定时器（`systemd-run --on-active=120s velumd service tun-revert`），然后开启 TUN。**此时 `tun.enabled` 不写入配置**，重启后自动回到端口模式。
2. 用户**另开一个 SSH 会话**执行 `vlu tun confirm`：取消定时器并写入配置。要求新开会话，是为了证明新的入站连接确实可用。
3. 超时未确认：定时器调用 `tun-revert`，关闭 TUN；velumd 无响应时直接重启服务（配置里仍是关闭状态）。

规则：直接用 `vlu tun on` 时，如果检测到当前是 SSH 会话（`SSH_CONNECTION` 或父进程链）且没有 `--try`，就拒绝并提示，可用 `--force` 覆盖。

### 开工前要验证的问题

以下是对 sing-box 在服务器上行为的待验证问题，**尚未实测，结论写回本节**。

| # | 问题 | 为什么重要 | 候选做法 |
| :--- | :--- | :--- | :--- |
| S1 | 全局 `auto_route` 下，入站连接（SSH、对外服务）的回包会不会进入 TUN | **决定 TUN 能否用于有公网入站的服务器**，桌面平台没有这个问题 | 按连接标记回包（入站打 connmark，`ip rule` 走主路由表）；或限定 `route_address` 只接管部分网段；或对源端口放行 |
| S2 | 专用用户 + 上述 capabilities 是否足够，尤其是 `auto_redirect` 需要的 nftables | 决定是否必须以 root 运行 | 不够时先记录缺什么，再决定是增加权限还是放弃 `auto_redirect` |
| S3 | `kill -9` 后路由规则、nft 表是否残留，重启后能否清理 | 崩溃后服务器不能断网 | `ExecStopPost` 清理；启动时先清理残留 |
| S4 | resolved、纯 `resolv.conf` 两种环境下 DNS 是否都经过 Velum 分流 | 否则规则按域名分流会失效 | 路由层劫持 53；resolved 环境用 `resolvectl` 设置链路 DNS |
| S5 | Docker 容器的转发流量是否被接管，开启 TUN 后容器之间、端口映射是否正常 | 服务器上 Docker 很常见 | `include_interface` / 排除 `docker0` 等网桥；是否接管容器做成选项 |
| S6 | 与 ufw、firewalld、Tailscale、WireGuard 的规则是否冲突 | 共存是 Velum 的定位 | 按结果确定 ip rule 优先级，文档列出已知限制 |
| S7 | 云元数据地址（`169.254.169.254`）、VPC 内网是否默认直连 | 接管后可能导致云主机取不到凭据或内网不通 | 默认排除链路本地与 RFC1918 网段 |

## 分步（阶段一）

| 步骤 | 内容 | 验收 |
| :--- | :--- | :--- |
| **L1 构建与发布** | `core.yml` 加 ubuntu（x64、arm64）；修复 Linux 上失败的测试；`release.yml` 产出压缩包与校验和；`AssetSuffix` 增加 Linux；`vlu env` | CI 全绿；Release 带 Linux 包；干净的 Ubuntu 24.04 上解压后能用订阅跑通本地端口 |
| **L2 系统服务** | `velumd service`、目录与 socket 权限、`vlu up/down` 接入 systemd、`install.sh`、`listen` 安全检查与 `listen_auth` | 所有目标系统上安装、升级、卸载干净；重启后自动运行；`kill -9` 后自动拉起；未加入 `velum` 组的用户访问被拒绝；无认证的 `0.0.0.0` 被拒绝 |
| **L3 TUN 验证（spike）** | 在虚拟机里回答 S1～S7，结果写入 `spikes/ml-linux/README.md` 和本文 | 每个问题都有实测结论；S1 有可行方案，否则暂停 L4 并重新评估 TUN 的范围 |
| **L4 TUN 实现** | render 的 Linux 分支、`--try` / `confirm` / `tun-revert`、崩溃清理、DNS、默认排除网段；netns 集成测试进 CI | 虚拟机上：SSH 全程不断；回退定时器在超时、velumd 被 `SIGSTOP`、`kill -9` 三种情况下都恢复网络；`vlu why` 与实际路由一致 |
| **L5 共存与感知** | `LinuxSnapshot`；Docker、WireGuard、Tailscale、ufw 的规则与文档 | S5、S6 的场景在 TUN 开启时内外网正常，或有明确记录的限制 |
| **L6 发布** | `vlu upgrade`、README 与部署文档（无 TUN、TUN、Docker 三种用法）、发布说明 | 按文档从零部署成功；从上一版本 `vlu upgrade` 成功，失败时保留旧版本 |

L2 完成后可以发布「端口模式」版本，不必等 TUN。

### 进度

- **L1 代码已完成，2026-11-05 CI 通过**：全量测试在 ubuntu x64、arm64 上首次运行即全部通过，没有需要修的 Linux 失败。两个架构的静态压缩包都跑通了 `core/scripts/smoke.sh`（启动、加载本地订阅、HTTP 与 SOCKS5 入口、`vlu env`、停止）。
- **L1 尚未验证**：`release.yml` 的 Linux 产物与 `SHA256SUMS` 要等下一次打标签；CI 环境不是全新系统；smoke 用的是本地订阅和直连规则，**没有测过真实订阅和真实节点**。

## 阶段二：Linux 桌面（不排期）

阶段一的 TUN、DNS、网络感知、CI 都能复用。需要新增：

- **进程模型**：工作站上服务以安装用户运行（`User=`、`AmbientCapabilities`），配置留在 `~/.config/Velum`；与服务器的专用用户模式由 `velumd service install` 的参数区分。
- **系统代理**：`pkg/sysproxy` 增加 GNOME（`gsettings`）、KDE（`kwriteconfig5/6`）后端，沿用快照、还原、自愈；服务开机时用户会话可能还不存在，要等 `/run/user/<uid>/bus` 出现后再应用。
- **桌面壳**：`system/linux.rs`（`pkexec` 安装服务、`~/.config/autostart`）；`sidecars.sh` 支持 Linux 目标；托盘依赖 libayatana-appindicator（GNOME 需要 AppIndicator 扩展），只依赖菜单，不依赖点击事件；复制命令增加 `wl-copy`；菜单栏网速等 macOS 专属功能不提供。
- **打包**：先出 .deb；AppImage 要先把 `velumd` 复制到固定路径（服务不能指向临时挂载点）；确认 Tauri updater 对 .deb 的支持程度，不支持就提示下载。
- **验收**：Ubuntu 24.04 GNOME 上安装 .deb 后，系统代理与 TUN 均可开关；`kill -9 velumd` 后系统代理被还原、网络恢复；连接或断开 WireGuard、OpenVPN 时内网可用。

## 测试环境

- **任何会改路由的测试都不在日常使用的 Mac 上跑**，只在 Linux 虚拟机里跑，有问题就回滚快照。
- arm64 虚拟机放在这台 Mac 上（UTM 或 Lima）；x86_64 虚拟机放在 Intel 测试 Mac 上，通过 SSH 操作。入站场景（S1）从虚拟机外部 SSH 进去测试。
- 共存场景需要 Docker、Tailscale、WireGuard，都在虚拟机内安装。
- CI 用 netns 搭出「客户端 — 服务器」的最小网络，自动回归路由、回退和清理。云主机只在 L6 前做一次端到端确认。

## 待决定

| # | 问题 | 倾向 |
| :--- | :--- | :--- |
| Q1 | 是否提供 `.deb` 给服务器用户 | 暂不提供；压缩包加脚本已够用，留到阶段二一起做 |
| Q2 | `vlu upgrade` 的校验是否只靠 `SHA256SUMS` | 校验和与压缩包都来自 GitHub，防不住发布被篡改；L6 前决定是否复用现有 updater 签名密钥签名 |
| Q3 | 是否提供 Docker 镜像 | 暂不提供；TUN 在容器里需要额外权限，等有需求再做 |
| Q4 | S1 没有可靠方案时怎么办 | TUN 限定为「只接管指定网段」或「仅无公网入站的机器」，并在文档中说明 |

## 风险

| 风险 | 影响 | 应对 |
| :--- | :--- | :--- |
| TUN 导致 SSH 断开 | 用户被锁在服务器外 | 试运行与独立回退定时器；未确认不写配置，重启即恢复；S1 不通过不进入 L4 |
| 崩溃后路由或防火墙规则残留 | 服务器断网 | S3；`ExecStopPost` 与启动时清理；CI 回归 |
| 开放代理 | 服务器被他人滥用 | 服务模式拒绝无认证的对外监听 |
| 订阅地址泄露 | 节点信息被同机其他用户读取 | `/etc/velum` 权限 `0700`；接口只对 `velum` 组开放（组成员可经接口查看和修改配置，需在文档中说明） |
| 与 Docker、防火墙规则冲突 | 容器或对外服务不可用 | S5、S6；默认不改防火墙，或做成可关闭的选项 |
| 发行版差异 | 个别系统跑不起来 | 明确支持范围；静态链接；只依赖 systemd 与内核 tun |
