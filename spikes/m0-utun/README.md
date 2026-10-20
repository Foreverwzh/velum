# M0 Spike：macOS 特权 helper 创建 utun + FD 传递

> 对应 ARCHITECTURE §3.2 / D4，§7 M0 第 3 项。
> 结论：**可行 ✅**，并发现 3 处需要回写架构的约束。

## 1. 验证内容

| 组件 | 权限 | 说明 |
| :--- | :--- | :--- |
| `cmd/velum-helper-spike` | root | 创建 utun、设 MTU/地址、增删路由、代查连接归属；UDS + peer uid 鉴权；`SCM_RIGHTS` 传 FD；断连兜底清理；20 分钟 TTL |
| `cmd/velumd-spike` | 用户 | 内嵌 sing-box v1.14.3；只实现 `adapter.PlatformInterface` 的必要方法，其余回落原生实现 |
| `cmd/pcbdiag` | 用户 | 诊断 `pcblist_n` 在当前 macOS 上的可见性（注意：需要脱离 agent 进程树运行，见 F3） |
| `cmd/pcbext` | 用户 | F3 复核：监听端口，查询外部 curl 连接的归属 |
| `test.sh` | 用户 | 5 组 16 项自动化验证 |

**安全设计**（在 Stash TUN 模式下验证，全程未打断现有网络）：
- 只新建 utun，不碰默认路由、DNS、系统代理；
- 只为 3 个测试 IP 加 `/32` 路由；
- helper 拒绝比 `/8` 更宽的前缀、拒绝回环地址、拒绝覆盖已存在的路由。

## 2. 结果（macOS 27.0.1 / arm64 / sing-box v1.14.3，2026-10-16）

| 验证项 | 结果 |
| :--- | :--- |
| 用户态进程拿到 helper 传来的 utun FD 并启动引擎 | ✅ FD 传递约 2ms，引擎启动约 5～7ms |
| 路由只影响测试 IP，默认路由（Stash `utun4`）不变 | ✅ |
| utun → sing-box → 物理网卡直连（`bind_interface en0`） | ✅ 约 50ms，与基线一致 |
| utun → sing-box → HTTP 上游（Stash 7890） | ✅ 约 260ms，与基线一致 |
| 连接归属查询（经 helper） | ✅ 拿到 `/usr/bin/curl`，单次 RPC 约 0.3～0.4ms（但「必须经 helper」的前提已撤回，见 F3） |
| 运行期增删路由，不重启引擎 | ✅ 单次约 0.2ms，引擎只启动 1 次 |
| `kill -9 velumd` 后兜底清理 | ✅ **约 30ms** 内路由还原、utun 销毁，原路径访问恢复 |

复现方法：

```bash
go build -o bin/ ./cmd/...                 # 需 GOPROXY=https://goproxy.cn,direct
sudo ./bin/velum-helper-spike              # 终端 A
./test.sh                                  # 终端 B
```

## 3. 发现（需回写架构）

### F1. 使用 sing-box 内部 `adapter.PlatformInterface`，而非 libbox 门面

- libbox 的 `PlatformInterface` 有约 30 个方法，并且默认假定运行在 NetworkExtension 中：它会强制接管网卡监听、网卡列表等能力。
- `adapter.PlatformInterface` 的每项能力都带 `UsePlatformXxx()` 开关。只打开 `UsePlatformInterface`（TUN）和 `UsePlatformConnectionOwnerFinder`（进程查询），其余全部回落 sing-box 的 darwin 原生实现，适配代码不到 100 行。
- 代价：这是 sing-box 的内部 API，升级时需要回归。它被限制在 `engine/singbox/bridge` 中，与 ARCHITECTURE §2.2 的隔离设计一致。
- **桌面端走 `adapter.PlatformInterface`；iOS / Android 仍评估 libbox**，因为它为 NE / VpnService 做的适配正是移动端需要的。

### F2. 外部 FD 模式下，路由完全由 helper 负责，包括 TUN 自身网段

- `sing-tun` 在 `FileDescriptor != 0` 时既不配置地址，也不设任何路由。
- darwin 上必须给 TUN 自身网段（例如 `172.19.0.0/30`）加一条走 utun 的路由，否则 system 栈 NAT 之后的回包（发往 `172.19.0.2`）会落到默认路由而丢失。
- 解法：velumd 用 `tun.Options.BuildAutoRouteRanges()` 推导出路由清单，与 Velum `RoutePlan` 合并后交给 helper 执行。

### F3. ~~macOS 27 上，非 root 进程只能看到自己的 socket~~（已撤回，2026-10-16 M1 复核）

- **原结论是测试环境造成的误判。** M1 第 2 步中，以普通用户运行的 velumd 正常查到了 curl。复核后发现：
  - 在 agent 的 bash 工具里前台启动的进程（含 `cmd &` 这类仍留在工具进程树内的进程）读 `pcblist_n` 会被过滤，只返回自身的 socket。M0 的 `pcbdiag` 和 TUN 测试中的 velumd-spike 都属于这种情况；
  - 同一个二进制用双重 fork 脱离进程树后，能拿到全量列表（约 113KB），正常查到其他进程（见 `cmd/pcbext`）。
- 结论：**正常启动的用户态进程可以查询连接归属，不需要 root**。mixed 入站（系统代理模式）已在 M1 中实测可用。
- TUN 模式下的进程归属需要在正常启动环境里重新验证（M1.5，需要 sudo 起 helper）。在此之前，「helper 代查」只作为备选方案保留，不作为必需设计。
- 原测试中 helper 代查本身是可行的（单次约 0.3ms）。如果 TUN 复核失败，仍可以使用该方案。

### F4. 杂项

- 只要传入平台接口，sing-box 默认就把日志写到 `io.Discard`，需要显式配置 `log.output`。生产版应改用 `PlatformLogWriter` 接入 Velum 日志。
- `proxy.golang.org` 在当前网络环境下不可达，构建需设置 `GOPROXY=https://goproxy.cn,direct`。

## 4. 未覆盖（留给 M1.5）

- 生产版 helper 的鉴权：用 Swift 重写，SMAppService 注册，并用 `SecCodeCopyGuestWithAttributes` 校验调用方代码签名（spike 只校验了 uid）。
- UDP / IPv6 / DNS 劫持；全局 TUN（默认路由切到自建 utun）与 Stash 等其他 TUN 共存的行为。
- 睡眠唤醒、网络切换后的路由重建；helper 自身崩溃后由 launchd 拉起的自愈流程。
