# ML spike：sing-box TUN 在 Linux 服务器上的行为

回答 `docs/ML-PLAN.md` 的 S1～S7。测试用上游 sing-box 1.14.3 的可执行文件和手写配置（出站只有 direct），与 Velum 的 go.mod 版本一致；**这里还没有经过 Velum 的渲染，也没有经过真实节点**。

| 脚本 | 环境 | 测什么 |
| :--- | :--- | :--- |
| `netns-s1.sh` | netns（不影响运行机器的网络） | 公网客户端入站（客户端经路由器、不在同一网段）、权限、崩溃残留 |
| `vm-takeover.sh` | 虚拟机全局接管，配独立回退定时器，先试跑过回退 | SSH、端口映射、Docker、DNS、云元数据、ufw |

环境：Ubuntu 24.04.5 x86_64，内核 6.8，Docker 29.1.3（2026-11-05）。

## 结论：用 `auto_route` + `auto_redirect`，并负责崩溃清理

| | `auto_route` | `auto_route` + `auto_redirect` |
| :--- | :--- | :--- |
| 公网客户端 TCP：已建立的连接、新连接 | ✅ | ✅ |
| 公网客户端 UDP（服务绑定通配地址） | ❌ 回包进隧道，源端口被改 | ✅ |
| 专用用户 + `CAP_NET_ADMIN/RAW/BIND_SERVICE` | ✅ | ✅ |
| 云元数据 `169.254.169.254` | 不进隧道 | ❌ 进隧道，需要排除 |
| `kill -9` 后本机出站 | ✅ | ⚠️ 虚拟机上正常，netns 里不通 |
| `kill -9` 后容器出站 | ✅ | ❌ 不通，直到清理 |
| 崩溃后直接重启 | ✅ 规则条数不变 | ⚠️ 能启动，但 `ip rule` 每次多一条 |

服务器上绑定通配地址的 UDP 服务很常见（WireGuard、DNS、QUIC），错误是静默的，而且接入真实规则后回包可能经代理节点发出，所以选 `auto_redirect`。它的缺点都是崩溃残留，可以由 systemd 的 `ExecStopPost` 和启动时清理解决。

## F1：入站回包不进隧道，靠的是源地址（S1）

`auto_route` 的策略路由只让「源地址未绑定」的本机流量进隧道：

```
9001: from all lookup 2022 suppress_prefixlength 0
9002: not from all dport 53 lookup main suppress_prefixlength 0
9003: from 0.0.0.0 iif lo lookup 2022
```

TCP 回包带已绑定的源地址，跳过这些规则走主路由表，所以 SSH 不断。通配地址的 UDP 回包没有绑定源地址，就进了隧道：sing-box 把它当作新的出站连接，客户端收到的源端口变了（5353 → 56658）。

**测试陷阱**：第一版客户端用 `recv` 不看来源，报告「通」。真实的 WireGuard、DNS 客户端会丢弃这种包，必须校验源地址和端口。

**同网段测不出来**：虚拟机上从同网段的 Mac 测 UDP 是通的，因为回包命中主表的直连路由（规则 9002）。公网场景只能看 netns 拓扑。

## F2：权限够用（S2）

`nobody` + 上述三个 capabilities（`setpriv --ambient-caps`）在 netns 里两种模式都能启动并接管，包括 `auto_redirect` 需要的 nftables。不需要 root，也不依赖 `nft` 命令。

## F3：崩溃残留（S3）

- `auto_route`：`ip rule` 留下 8 条，但路由表 2022 随 TUN 消失而清空，网络正常；重启后不累积。
- `auto_redirect`：留下 `inet sing-box` 表和 `ip rule`。表里的转发用 `queue flags bypass`，无人监听时放行；本机出站在虚拟机上正常（含未缓存的 DNS），netns 里不通，**差异原因未查明**。容器出站不通。直接重启能恢复，但 `ip rule` 每次多一条。
- 清理方式：删除 `inet sing-box` 表，按优先级删除残留的 `ip rule`（`vm-takeover.sh revert`）。清理后本机出站、DNS 正常；**清理后容器能否恢复还没有直接测**。

## F4：DNS（S4）

systemd-resolved 存根下，解析经 sing-box；直接问 `8.8.8.8` 也被劫持。两种模式相同。纯 `/etc/resolv.conf` 的系统没测。

## F5：Docker（S5）

两种模式下：容器出站通且进隧道；本机和局域网访问端口映射正常；容器访问网桥网关正常。

## F6：ufw 与接管冲突（S6，未解决）

ufw 开启（默认 `deny (routed)`）时：

| | 不接管 | 接管（两种模式） |
| :--- | :--- | :--- |
| 容器出站 | ✅ | ❌ |
| 本机出站、新 SSH | ✅ | ✅ |

原因未查明。WireGuard、Tailscale、firewalld 也还没测。

## F7：哪些目标进隧道（S7）

| 目标 | `auto_route` | `auto_redirect` |
| :--- | :--- | :--- |
| 同网段（`192.168.3.21`） | 不进 | 不进 |
| 不在同网段的私有地址（`10.0.0.2`、`172.31.255.1`） | **进** | **进** |
| `169.254.169.254` | 不进 | **进** |

云主机的同 VPC 其他子网属于第二行，所以要默认排除链路本地和 RFC1918。

## 没测到的

- 真实公网客户端（公网入站只有 netns 模拟）。
- 真实节点、Velum 渲染出的配置。
- Debian、Ubuntu 22.04、arm64。
- IPv6（测试配置只有 IPv4）。
