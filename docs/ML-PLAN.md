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
| 服务管理 | `velumd service install [--add-user <用户>] \| uninstall [--purge] \| start \| stop \| status`，后三者是 `systemctl` 的薄封装；install 需要 root，创建用户、目录、复制程序到 `/usr/local/bin`、写 unit。已有同名用户但不是 Velum 创建的（主目录不是 `/var/lib/velum`）时拒绝安装，`--purge` 也不删除它。`StartLimitIntervalSec=0`：崩溃后一直重试。`vlu up` / `vlu down` 在服务已安装时走 systemd（`startService` 已有此扩展点） |
| 本地端口的安全 | **以服务运行时拒绝非回环的 `listen`**，并说明原因。对外提供代理用单独的入口 `listen_public`（地址、用户名、密码，映射 sing-box `users`，缺一项就拒绝）；`listen` 保持无认证的回环入口，核心自身经它拉取订阅的逻辑不用改。桌面平台的 `listen` 行为不变 |
| 无 TUN 时如何使用 | 新增 `vlu env`，输出 `export http_proxy=…`；文档给出 apt、Docker daemon、systemd 服务的代理设置方法。**不实现桌面代理后端**，服务器上 `system_proxy` 显示为不适用 |
| TUN | `render.go` 的 Linux 分支：sing-box 原生 tun + **`auto_route` + `auto_redirect`**（只用 `auto_route` 时，绑定通配地址的 UDP 服务回包会进隧道，见 `spikes/ml-linux/README.md` F1）。默认排除 RFC1918、`169.254.0.0/16`、`fc00::/7`、`fe80::/10`（否则云元数据和同 VPC 其他子网会进隧道，F7）。只在服务模式下提供：崩溃清理和回退都依赖 systemd |
| 崩溃清理 | `auto_redirect` 崩溃后留下 `inet sing-box` 表和 `ip rule`，容器出站不通，规则还会累积（F3）。`velumd service cleanup-net` 负责清理，由 `ExecStartPre` 和 `ExecStopPost` 调用。用 netlink 实现，不依赖 `nft` 命令；优先级 1 和 32768 上的规则要同时符合 sing-tun 的特征才删除，其他程序的规则不动。表名是 sing-box 写死的，**同机另跑 sing-box 的 `auto_redirect` 时会互相影响** |
| TUN 试运行 | 见下节 |
| DNS | 由路由层劫持 53 端口，不改系统文件，`tun.dns_hijack` 在 Linux 上不生效。内核直接问系统的上游 DNS（`/run/systemd/resolve/resolv.conf`，否则 `/etc/resolv.conf`），避免查询绕回自己。**上游 DNS 落在默认排除网段时单独加回隧道**，否则 systemd-resolved 的查询绕过劫持；这个地址上的其他连接走直连。限制：只有本机解析器（dnsmasq 等）时拿不到上游，会给出告警；云厂商内网域名可能解析不了（按设计推断，没有在云主机上测），见 L5 |
| 网络感知 | 新增 `netsense.LinuxSnapshot`：netlink 取网卡、路由，resolved 取按链路域，识别 WireGuard、Tailscale、OpenVPN。Captive 探测在服务模式下关闭。服务模式下默认不因其他全局 VPN 让出接管（`yield_to_full_vpn` 默认关闭，可手动开启） |
| 分发 | `Velum-<版本>-linux-{x64,arm64}.tar.gz`（静态链接的 `velumd`、`vlu`、`install.sh`）+ `SHA256SUMS`。`install.sh` 校验后调用 `velumd service install`，也能离线安装本地压缩包（部分服务器访问 GitHub 不稳定） |
| 更新 | `AssetSuffix` 增加 Linux 条目，`vlu status` 提示新版本。升级命令 `vlu upgrade` 校验并替换程序后重启服务；不做后台自动更新：服务器不应自行改变运行中的程序 |

### TUN 试运行：防止锁在外面

类似 `netplan try`：

1. `sudo vlu tun on --try [120s]`：先注册独立于 velumd 的回退定时器（`systemd-run --on-active=120s velumd service tun-revert`），然后开启 TUN。试运行状态只在 velumd 内存里，**不写入 `state.json` 和配置**，重启后自动回到端口模式。需要 root，因为要操作 systemd。
2. 用户**新开一个 SSH 连接**执行 `sudo vlu tun confirm`：先保存，再取消定时器。要求新开连接，是为了证明新的入站连接确实可用：velumd 比较父进程链上 sshd 的 PID（`SSH_CONNECTION` 经 sudo 通常被清除），同一连接确认会被拒绝，可用 `--force` 覆盖。
3. 超时未确认：定时器调用 `tun-revert`。先用 3 秒询问 velumd，试运行已结束就什么也不做；还在试运行或没有响应（可能已卡死）时，停止服务、清理网络、重新启动，并留下记录，`vlu status` 会说明原因。

规则：在 SSH 会话里直接用 `vlu tun on` 或 `vlu tun config` 时拒绝并提示，可用 `--force` 覆盖。手动把 `tun.enabled: true` 写进配置再 reload 不受这条保护。

### TUN 验证结果（L3，2026-11-05）

过程、数据和测试陷阱见 `spikes/ml-linux/README.md`。测试用上游 sing-box 1.14.3、手写配置和 direct 出站，还没有经过 Velum 的渲染和真实节点。

| # | 问题 | 结论 |
| :--- | :--- | :--- |
| S1 | 入站回包会不会进隧道 | TCP 不会。UDP：只用 `auto_route` 时通配地址的服务回包进隧道、源端口被改；加 `auto_redirect` 正常。公网客户端用 netns 模拟 |
| S2 | 专用用户 + capabilities 是否足够 | 足够，包括 nftables，不依赖 `nft` 命令 |
| S3 | `kill -9` 后残留 | `auto_redirect` 留下 nft 表和 `ip rule`，容器出站不通、规则累积，需要清理。本机出站在虚拟机和 netns 上结果不同，原因未查明 |
| S4 | DNS | systemd-resolved 下正常，直接问外部 DNS 也被劫持。纯 `/etc/resolv.conf` 在 L4 经 Velum 验证通过 |
| S5 | Docker | 容器出站进隧道，端口映射与网桥访问正常 |
| S6 | 防火墙与其他 VPN | **ufw 开启时接管会让容器出站不通，未解决，原因未查明**；本机与 SSH 不受影响。firewalld、WireGuard、Tailscale 没测（L5） |
| S7 | 云元数据与内网 | 需要默认排除 `169.254.0.0/16` 与 RFC1918 |

## 分步（阶段一）

| 步骤 | 内容 | 验收 |
| :--- | :--- | :--- |
| **L1 构建与发布** | `core.yml` 加 ubuntu（x64、arm64）；修复 Linux 上失败的测试；`release.yml` 产出压缩包与校验和；`AssetSuffix` 增加 Linux；`vlu env` | CI 全绿；Release 带 Linux 包；干净的 Ubuntu 24.04 上解压后能用订阅跑通本地端口 |
| **L2 系统服务** | `velumd service`、目录与 socket 权限、`vlu up/down` 接入 systemd、`install.sh`、`listen` 安全检查与 `listen_auth` | 所有目标系统上安装、升级、卸载干净；重启后自动运行；`kill -9` 后自动拉起；未加入 `velum` 组的用户访问被拒绝；无认证的 `0.0.0.0` 被拒绝 |
| **L3 TUN 验证（spike）** | 在虚拟机里回答 S1～S7，结果写入 `spikes/ml-linux/README.md` 和本文 | S1 有可行方案；未测的项目已列出，转入 L4、L5 |
| **L4 TUN 实现** | render 的 Linux 分支、`--try` / `confirm` / `tun-revert`、崩溃清理、DNS、默认排除网段；netns 集成测试进 CI | 虚拟机上：SSH 全程不断；回退定时器在超时、velumd 被 `SIGSTOP`、`kill -9` 三种情况下都恢复网络（**含容器出站**）；崩溃多次后 `ip rule` 不累积；经真实节点的流量与 `vlu why` 一致；通配地址的 UDP 服务源地址正确（netns） |
| **L5 共存与感知** | `LinuxSnapshot`；Docker、WireGuard、Tailscale、ufw 的规则与文档；按链路域把内网域名交给云厂商 DNS；IPv6 | ufw 开启时容器出站正常，或有明确记录的限制；WireGuard、Tailscale、firewalld 下内外网正常，或有明确记录的限制；云厂商内网域名可解析；IPv6 入站和出站正常，或默认关闭 |
| **L6 发布** | `vlu upgrade`、README 与部署文档（无 TUN、TUN、Docker 三种用法）、发布说明 | 按文档从零部署成功；从上一版本 `vlu upgrade` 成功，失败时保留旧版本 |

L2 完成后可以发布「端口模式」版本，不必等 TUN。

### 进度

- **L1 代码已完成，2026-11-05 CI 通过**：全量测试在 ubuntu x64、arm64 上首次运行即全部通过，没有需要修的 Linux 失败。两个架构的静态压缩包都跑通了 `core/scripts/smoke.sh`（启动、加载本地订阅、HTTP 与 SOCKS5 入口、`vlu env`、停止）。
- **L1 尚未验证**：`release.yml` 的 Linux 产物与 `SHA256SUMS` 要等下一次打标签；全新 Ubuntu 24.04 虚拟机上的 smoke 已通过；真实订阅（19 节点、规则集）经本地端口验证通过，出口符合规则（2026-11-05）。
- **L2 代码已完成，2026-11-05 在全新 Ubuntu 24.04 x86_64 虚拟机上验证**：`core/scripts/service-smoke.sh` 覆盖安装、目录与 socket 权限、capabilities、访问控制、对外监听、`kill -9` 拉起、`vlu up/down`、升级保留配置、卸载与 `--purge`、同名账号保护；另手动验证了 VM 重启后自动运行、压缩包安装与校验失败。CI 也运行这个脚本。
- **L3 已完成（2026-11-05）**：结论见上节；选定 `auto_route` + `auto_redirect` + 崩溃清理。
- **L4 已完成（2026-11-05，Ubuntu 24.04 x86_64 虚拟机）**：
  - 回退：超时、`SIGSTOP`（30 秒试运行，35 秒恢复）、`kill -9` 三种情况都恢复了网络和容器出站；`kill -9` 后 systemd 拉起时已回到端口模式，定时器到期不再重启。
  - 确认与重启：SSH 长连接从试运行到确认全程不断；同一连接确认被拒绝；确认后重启虚拟机 TUN 保持开启。
  - 崩溃：已确认状态下连续 `kill -9` 三次，`ip rule` 不累积，容器出站恢复；netns 回归（`core/scripts/netns-tun.sh`，CI 运行）确认清理不动其他程序的规则。
  - 真实节点（19 节点订阅）：本机和容器经 TUN 出口都是 HK，`vlu why` 命中同一条规则和策略组；云元数据和内网地址没有进隧道；路由器管理页可访问。
  - DNS：systemd-resolved（上游是路由器）和纯 `/etc/resolv.conf` 下都返回 fake-ip，直接问外部 DNS 也被劫持。
  - 验证发现并修复的问题：清理漏掉 32768 规则（netlink 解析时不设 `MarkSet`）；velumd 卡死时回退多等 30 秒；内网上游 DNS 绕过劫持。
- **L4 尚未验证**：真实公网客户端（入站只有 netns 模拟）；IPv6（默认配置会开启 IPv6 路由，虚拟机有公网 IPv6 地址，但没有测 IPv6 入站和出站）；Ubuntu 22.04、Debian 12；arm64 只有 CI 的 netns 回归。
- **L2 尚未验证**：Ubuntu 22.04、Debian 12 和 arm64 上的完整安装（arm64 只有 CI 覆盖）；`install.sh` 从 GitHub 下载的路径要等发布后才能测。

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

## 风险

| 风险 | 影响 | 应对 |
| :--- | :--- | :--- |
| TUN 导致 SSH 断开 | 用户被锁在服务器外 | 试运行与独立回退定时器；未确认不写配置，重启即恢复。S1 在 netns 模拟下通过，真实公网客户端要在 L6 前用云主机确认 |
| 崩溃后路由或防火墙规则残留 | 容器断网、规则累积 | `ExecStopPost` 与启动时清理；CI 回归 |
| 开放代理 | 服务器被他人滥用 | 服务模式拒绝无认证的对外监听 |
| 订阅地址泄露 | 节点信息被同机其他用户读取 | `/etc/velum` 权限 `0700`；接口只对 `velum` 组开放（组成员可经接口查看和修改配置，需在文档中说明） |
| 与 Docker、防火墙规则冲突 | 容器或对外服务不可用 | S5、S6；默认不改防火墙，或做成可关闭的选项 |
| 发行版差异 | 个别系统跑不起来 | 明确支持范围；静态链接；只依赖 systemd 与内核 tun |
