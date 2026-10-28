# MN Spike：macOS 改用 NetworkExtension（系统扩展 + NEPacketTunnelProvider）

> 对应 ARCHITECTURE §3.2 / D4，路线图 MN。
> 阶段一（公司网络下）已完成；阶段二（与企业 NE VPN 共存、全局接管）待测。

## 1. 组成

| 组件 | 说明 |
| :--- | :--- |
| `App/` 宿主程序 | 命令行：`activate` / `deactivate` 安装卸载系统扩展；`start <run 目录>` / `stop` / `status` / `remove` 启停隧道 |
| `Tunnel/` 系统扩展 | `NEPacketTunnelProvider`，进程内运行完整核心（`core/mobilebind`，与 Android 同一入口）；`Host.openTun` 设置隧道参数后把 utun FD 交给核心 |
| `prepare.sh` | 从当前 Velum 目录生成 `run/`（入口 7999、TUN 172.19.8.1/30、只接管 `ROUTES`、不改系统 DNS / 代理），与正在运行的 Velum 隔离 |
| `build/ship.sh` | 归档 → Developer ID 导出并公证 → 安装到 /Applications → 激活（不提交，含本机路径） |

构建：`gomobile bind -target macos ... -o build/Mobilebind.xcframework ./mobilebind`（在 `core/` 下），再 `xcodegen generate` 与 `build/ship.sh`。

## 2. 结果（macOS 27.0 / arm64 / M5，2026-10-28）

| 验证项 | 结果 |
| :--- | :--- |
| MDM 托管设备能否安装 | ✅ 策略为 `UserOption`（用户批准即可），未被拦截 |
| 首次安装 | 两次批准：系统设置中批准系统扩展；首次保存配置时允许添加 VPN 配置 |
| 升级扩展 | ✅ `CFBundleVersion` 递增后直接替换，约 0.3s，**无需重新批准** |
| 签名 | 开发描述文件不含 `packet-tunnel-provider-systemextension`，**必须 Developer ID 签名 + 公证**（Xcode 导出时自动换成该值）；公证每次约 30s |
| 核心运行于扩展内 | ✅ 启动约 80ms，隧道约 0.4～1s 连上 |
| 流量经隧道进入引擎 | ✅ 测试地址经 `utunN` 进入 sing-box，按规则出站 |
| 内存（真实订阅：24 节点 / 38 规则 / 33 规则集） | phys_footprint 稳态约 75MB，峰值约 99MB；未遇到 iOS 式的 50MB 上限 |
| 与 Microsoft Defender 网络扩展（内容过滤）同时运行 | ✅ 无冲突 |
| 与 Stash（同为 packet tunnel 系统扩展、全局接管）同时运行 | ✅ 两个隧道同时 Connected；更具体的路由（我们的 `/32`）优先走我们的 utun，其余仍走 Stash |
| 另一隧道后启动 / 重启 | ✅ Stash 关闭再打开，我们的隧道保持连接、流量不变 |
| 全局接管（`safe-run.sh`，带看门狗） | ✅ 隧道成为主服务，物理网卡默认路由由系统转为作用域路由；系统 DNS 指向隧道内 DNS，`/etc/resolver` 等分流 DNS 保留；外网正常，常驻内存约 117MB |
| 正常停止后的清理 | ✅ 系统撤销隧道的路由与 DNS，恢复为停止前的状态，无残留 |
| 扩展崩溃（`kill -9`）后的清理 | ✅ 约 0.3s 内系统判定 `Plugin failed`、断开隧道，路由与 DNS 全部撤销，无残留；**不会自动重连**（需 On-Demand 规则或 App 侧重启）；经隧道的已有连接随之中断 |

## 3. 发现

- **F1 扩展内系统解析器不可用**：系统 DNS 指向另一隧道内的地址（Velum helper TUN 的 `172.19.0.2`）时，扩展进程的 `getaddrinfo` 直接失败（provider 自身的流量不进其他隧道）。核心里经 Go 默认解析器的请求（拉订阅、规则集）因此失败；引擎自身用 DHCP DNS，不受影响。迁移时核心在扩展内应统一用物理网卡的 DNS 解析。
- **F2 网卡名**：mobilebind 原先把 TUN 名写死为 `tun`（Android），macOS 上 sing-box 需要真实的 `utunN`（否则 `bind forwarder to interface: no such network interface`）。已在 `core/mobilebind/tunname_darwin.go` 经 `UTUN_OPT_IFNAME` 读取。
- **F3 让出 TUN**：核心的 `yield_to_full_vpn` 检测到全局 VPN 时会关闭 TUN；NE 方案下系统允许并存，这一策略需要重新考虑。
- **F4 打包细节**：系统扩展包名必须等于 bundle ID；Info.plist 需要 `CFBundlePackageType = SYSX`；扩展以 root 运行，App Group 容器在 `/private/var/root/Library/Group Containers/`，与用户侧不同，配置需经启动参数或 XPC 传入。
- **F6 全局接管必须用默认路由**：核心的全局接管是 `0/1 + 128/1`（不替换默认路由）。NE 下照搬时隧道不成为主服务，系统不会给物理网卡加作用域默认路由，引擎绑定物理网卡（`IP_BOUND_IF`）的出站一律 `network is unreachable`（即 M1.5 记录的问题）。改为 `NEIPv4Route.default()` 后由系统维护，问题消失。
- **F7 看门狗**：`safe-run.sh` 在验证隧道断开、探测连续失败、到达时限、收到信号或 `touch /tmp/velum-ne-restore` 时停隧道并重新打开 Velum TUN；第一次（F6 未修）约 30 秒内自动恢复。崩溃测试时只靠探测失败太慢（用户先手动打开了 Velum TUN），已加上「隧道断开立即还原」。
- **F5 网络快照**：`Host.network()` 读 `State:/Network/Global/IPv4`，有其他全局隧道时拿到的是对方的 utun，需改为读物理服务。

## 4. 待测

- [ ] 与企业 NE VPN（分流型）同时运行：内网网段走 VPN、其余走 Velum。
- [ ] 全局接管下换网、睡眠唤醒；长时间运行的内存。
