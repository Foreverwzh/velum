# M0 Spike：动态实录（运行中的 sing-box → 每条连接的实际路由结论）

> 对应 ARCHITECTURE §4.4（Explain 动态实录），§7 M0 第 4 项。
> 结论：**可行 ✅**。用 sing-box 公开的 `Router.AppendTracker` 就能在进程内拿到全部所需信息，不需要开 clash_api。

## 1. 做法

| 能力 | 实现 |
| :--- | :--- |
| 实际命中规则、实际出站 | `Router.AppendTracker(ConnectionTracker)`：每条连接路由完成后回调 `RoutedConnection(ctx, conn, metadata, matchedRule, matchOutbound)` |
| 规则 → Velum 来源 | `Router.Rules()` 中按指针比较得到序号，再映射到 CompiledPlan 的 Provenance |
| 出站链 | 对 `adapter.OutboundGroup` 递归调用 `Now()`：`Proxy → Auto → stash` |
| 进程 / 域名 / 嗅探协议 / 解析结果 | `metadata.ProcessInfo` / `Domain` / `Protocol` / `DestinationAddresses` |
| 字节数、首包耗时、时长 | tracker 返回包装后的 conn：`Read` 计上行，`Write` 计下行，首次 `Write` 即首包时间 |
| 错误原因 | `box.Options.PlatformLogWriter` 接收日志，按连接 ID（`log.IDFromContext`）关联到记录 |

```bash
go build -o /tmp/velum-live-bin .   # GOPROXY=https://goproxy.cn,direct
./test.sh                           # 只开 127.0.0.1:17891 mixed 入站，上游经 Stash 7890
```

## 2. 结果（macOS 27，sing-box v1.14.3，2026-10-16）

| 场景 | 实录 |
| :--- | :--- |
| IP 直连 / 域名直连 | `rule[3] ip_cidr=…` ← `overlay: dev_bypass…`；`rule[4] domain_suffix=baidu.com` ← 订阅原文；首包 24～41ms |
| 策略组嵌套 | `chain = Proxy → Auto → stash`，首包约 120ms |
| 解析后走 final | `resolved=["198.18.0.198"]`（Stash fake-ip），`rule[-1] final` |
| SOCKS5 只发 IP（`198.18.16.10`） | TLS 嗅探还原出 `www.baidu.com`，正确命中域名规则 |
| `block` 出站 | 完整记录，`errors=[… outbound/block …]` |
| 拨号超时（绑定 en0） | `dur=2001ms`，`errors=[… dial … i/o timeout]` |
| `reject` 动作 | **不经过 tracker**；只能从 debug 日志拿到 `router: match[1] … => reject` |

## 3. 发现（需回写架构）

| # | 发现 | 处理 |
| :--- | :--- | :--- |
| F1 | `reject` / `hijack-dns` 等动作在调用 tracker 之前就返回了，tracker 看不到 | Velum 渲染 REJECT 时改用拦截出站，让 tracker 能完整记录。sing-box 的 `block` 出站已标记弃用，生产版应在自建的 outbound registry 里注册一个 Velum 自有的拦截出站。日志里的 `match[N] … => reject` 作为兜底 |
| F2 | 平台日志带 ANSI 颜色码，而且颜色码包住了连接 ID | 先剥离再解析。最好在 Velum 的 log writer 中直接关闭颜色 |
| F3 | 拨号失败时 sing-box **先关闭连接，后输出错误日志** | 记录需要延迟（约 100ms）异步落盘，才能关联到错误；不能在 `Close()` 中同步等待，否则会阻塞打日志的协程 |
| F4 | 跑在另一个 TUN 后面（如 Stash）时，上游 TCP 握手由对方 TUN 在本地完成，拨号失败会表现为「连上了但没有响应」 | Explain 要识别「下行 0 字节 + 无首包 + 持续到超时」的模式并给出提示 |
| F5 | ~~mixed 入站下的进程查询同样受 macOS 27 的 `pcblist_n` 限制~~（已撤回：限制来自测试环境，见 m0-utun F3） | 用户态 velumd 可直接查询，M1 已实测 |
| F6 | 每条连接都要在 `Rules()` 里线性查找序号 | 规则数通常只有几十条（大规模规则都在 rule_set 内），开销可忽略；生产版可预建指针 → 序号的映射表 |

## 4. 未覆盖

- UDP / QUIC 的字节统计（spike 中 UDP 只记录路由结论）。
- 规则集内部「具体是哪一条」命中：sing-box 只返回 `rule_set=xxx`。如需细化，要由 Velum 静态推演补充（§4.4 静态推演）。
- 高并发下 tracker 的性能开销没有压测。
