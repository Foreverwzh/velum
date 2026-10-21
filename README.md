# Velum

**可解释、能与公司内网共存的跨平台代理客户端。** 直接使用 Clash / Stash / mihomo 订阅，内核基于 sing-box。

> An explainable cross-platform proxy client (macOS / Windows / Android) that uses Clash-style subscriptions, built on a sing-box core.

> ⚠️ 项目处于预发布阶段（`v0.1.0-rc`），安装包未签名，适合愿意反馈问题的用户试用。

## 特点

- **直接用现有订阅**：支持 Clash / Stash / mihomo YAML，包括 proxy-providers、rule-providers、GEOIP / GEOSITE、策略组、DNS（多上游竞速、`respect-rules`、fake-ip）。无法完全等价转换的配置项会给出告警，不会静默丢弃。
- **可解释**：`vlu why <域名或地址>` 逐条推演 DNS、系统代理豁免与规则匹配，告诉你某个连接为什么走了这个出站，以及命中的规则来自订阅还是本地配置的哪一行。连接列表中，每个连接也会显示实际命中的规则和出站链。
- **与公司内网共存**：自动识别分流型企业 VPN，其网段和域名直连、DNS 交给 VPN 的服务器；检测到其他工具全局接管时让路；连上需要网页认证的 Wi-Fi 时暂停接管，认证后自动恢复。遵守系统的 DNS 分流设置（`/etc/resolver`、VPN 下发的作用域 DNS）。
- **本地覆盖层**：开发域名直连、hosts 覆盖、系统代理豁免写在独立的 `overlay.yaml` 中，优先于订阅，更新订阅时不会被覆盖。在连接或诊断中遇到走错的地址，点「加入本地规则」即可，不用手写 YAML；系统 hosts 文件默认生效。
- **两种接管方式**：系统代理，以及 TUN（接管所有应用，可同时接管系统 DNS），两者可以同时开启。
- **一个窗口**：概览回答「是否在保护、流量走哪条线」；另有策略组、连接、诊断、本地覆盖、告警和设置。⌘K（Windows 为 Ctrl+K）可以切换任意节点、执行操作、诊断地址。

## 平台

| 平台 | 状态 | TUN 实现 |
| :--- | :--- | :--- |
| macOS（Apple 芯片） | 预发布，日常使用中 | 特权 helper 创建 utun，文件描述符交给用户态核心 |
| Windows（x64） | 预发布，待真机验收 | 核心以 Windows 服务运行，使用 wintun |
| Android 10+ | 预发布，模拟器验证通过 | VpnService，支持按应用分流 |
| iOS | 计划中 | — |
| Linux | 仅命令行（系统代理 / 本地端口），桌面端计划中 | — |

## 安装

从 [Releases](https://github.com/Foreverwzh/velum/releases) 下载对应平台的安装包，具体的安装步骤（包括未签名应用如何打开）见发布说明。

首次打开时填写订阅地址即可使用。在 macOS 上首次开启 TUN 时，会安装一个 TUN 组件，需要输入管理员密码。

## 命令行

桌面 App 内置命令行工具 `vlu`。在「设置 → 系统组件」中点「命令行工具 vlu → 安装」后，终端里即可直接使用（macOS 链接到 `/usr/local/bin/vlu`，Windows 把安装目录加入用户 PATH）：

```sh
vlu status                     # 当前状态
vlu why https://example.com    # 推演这个地址会怎么走
vlu why --live example.com     # 查看实际连接的命中规则与出站链
vlu conns                      # 最近的连接
vlu reload / vlu update        # 重新加载 / 强制更新订阅
```

配置文件位于 macOS 的 `~/Library/Application Support/Velum/`、Windows 的 `%AppData%\Velum\`：`config.yaml` 是主配置，`overlay.yaml` 是本地覆盖层，两者都可以直接编辑。Windows 服务模式的日志与缓存在 `%ProgramData%\Velum\`。

## 从源码构建

需要 Go（版本见 `core/go.mod`）、Rust、[mise](https://mise.jdx.dev)（用于安装固定版本的 bun）。Android 另外需要 Flutter、Android SDK / NDK、JDK 17+。

```sh
# 核心：velumd / vlu / velum-helper
cd core && make test && make build

# 桌面 App（会先构建核心）
cd desktop && mise install && bun install && bun tauri build

# Android
cd mobile && sh scripts/core.sh && flutter build apk --release --split-per-abi
```

推送 `v*` 标签时，CI 会构建 macOS、Windows、Android 的安装包并创建 Release（见 `.github/workflows/release.yml`）。

## 文档

- [架构设计](docs/ARCHITECTURE.md)：决策记录、进程模型、各平台 TUN 方案、订阅转换、路线图
- 各阶段实施记录：[M1](docs/M1-PLAN.md)（核心与 CLI）、[M1.5](docs/M15-PLAN.md)（macOS TUN 与网络感知）、[M2](docs/M2-PLAN.md)（桌面端与 Windows）、[M3](docs/M3-PLAN.md)（Android）

## 许可证

Velum 以 [GPL-3.0-or-later](LICENSE) 发布。

Velum 使用 [sing-box](https://github.com/SagerNet/sing-box)（GPL-3.0-or-later）作为数据面内核，但它是独立项目，与 sing-box 及其作者没有关联，也没有得到其认可。第三方组件及其许可证见 [NOTICE.md](NOTICE.md)。
