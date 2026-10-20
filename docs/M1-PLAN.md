# M1 实施计划：Go 核心 + `vlu` CLI（macOS，仅系统代理模式）

> 对应 ARCHITECTURE §7 M1。M0 结论见 `spikes/`。

## 开发期安全原则

本机日常使用 Stash（系统代理 `127.0.0.1:7890` + TUN）。开发和测试 Velum 时：

- Velum 默认只监听 `127.0.0.1:7899`，**不碰系统代理、不开 TUN**；
- 只有显式执行 `vlu up --system-proxy` 才接管系统代理，接管前先保存快照，退出或崩溃时还原；
- 自动化测试一律使用随机端口，不修改任何系统设置。

## 分步

| 步骤 | 内容 | 验收 |
| :--- | :--- | :--- |
| 1. 编译链路 ✅ | `core/` 工程骨架；`ir`（VelumIR）；`subscription`（Clash YAML → IR，带行号 Provenance 与告警）；`overlay`；`compiler`（IR + Overlay → CompiledPlan：EngineIR / SysProxyPlan / DNS）；`engine/singbox/render`；import 边界检查；CI | golden 测试；渲染产物通过 `box.New`；规则语义探测 0 差异；本机 Stash 配置可完整编译 |
| 2. 运行时 ✅ | `engine/singbox`（生命周期、tracker、日志关联）；`velumd`；RPC（HTTP over UDS）；`vlu up/down/status/why/why --live/reload` | 起 `velumd` 后，经 7899 访问能命中正确规则；`vlu why` 静态推演与 `--live` 实录一致 |
| 3. 系统代理 ✅ | `sysproxy`：快照、写入、还原、启动自愈 | `vlu up --system-proxy` 后 `kill -9 velumd`，重启时能自愈 |

## VelumIR 语义约定（Velum 自己定义，渲染器负责保持）

- **域名后缀**：`example.com` 匹配根域及全部子域（按标签对齐）；`.example.com` 只匹配子域。
- **Cond 组合**：同一个 `Cond` 内，地址类条件（domain / domain_suffix / domain_keyword / domain_regex / ip_cidr / ip_private）之间为「或」；其余类别（端口、网络、进程、源 IP、规则集）之间及与地址类之间为「与」。规则集由多个 `Cond` 组成，彼此为「或」。
- **未标 no-resolve 的 IP 条件**：编译器在首条此类规则前插入 `resolve` 动作。
- **REJECT**：渲染为拦截出站（而不是 `reject` 动作），保证动态实录可见（M0 F1）。
- **不内置策略**：编译器不注入任何路由 / DNS / 系统代理豁免条目，策略一律来自订阅与 Overlay（`TestNoBuiltinPolicy` 守护）。GeoIP / GeoSite 规则集地址可配置（`GeoRuleSetURL`）。

## 进度

### 第 1 步 ✅（2026-10-16）

- 修正（老板评审）：移除编译器中写死的「私有地址 + CGNAT 直连」规则和内置系统代理豁免清单，改为由 Overlay 的 `system_proxy.bypass` 配置。
- 包：`ir` / `subscription` / `overlay` / `compiler` / `explain`（IR 参考求值器 + 探测生成）/ `engine/singbox/render`；`internal/archtest`（内核 import 边界）；CI（`.github/workflows/core.yml`）。
- 测试：`make test`（需构建标签，见 `core/Makefile`）。
  - 渲染产物通过 sing-box `box.New`；golden 快照。
  - 语义等价：IR 求值器（即 `vlu why` 静态推演）对比 sing-box 匹配器：覆盖样本 179 次探测，0 差异；对比 Clash 语义参考实现：60 次探测，0 差异。
  - 本机真实配置端到端（`VELUM_TEST_STASH=1`，不进 CI）：1.45 万条规则，订阅侧 41837 次探测、渲染侧 51209 次探测，均为 0 差异。
  - 变异自检：渲染器后缀语义被改坏时报 10 次不一致；故意加入违规 import 时边界测试失败。
- 在实现中发现并修复的问题：
  - **hosts 覆盖被通配豁免吞掉**：`*.corp.example.com` 会覆盖 `staging.corp.example.com`。现在任何会覆盖 hosts 主机的豁免写法（来自 `dev_bypass` 或 `system_proxy.bypass`）都不写入，并给出告警。
  - **`rule_set` 与地址条件混放时语义不确定**：sing-box 中二者的组合取决于规则集是否可合并。IR 约定不混放，DNS 策略拆成多条规则，渲染器检查此约束。
  - **`process_name` 区分大小写**（与 sing-box 一致）。mihomo 是否区分大小写待核实。
  - **classical 规则集中的无逗号行导致 panic**：已修复，并加入回归测试。

### 第 2 步 ✅（2026-10-16）

- 包：`engine`（接口）/ `engine/singbox`（生命周期、tracker、日志关联、环形缓冲、订阅）/ `explain/static`（vlu why）/ `config`（首次运行写入带注释的 config.yaml、overlay.yaml）/ `source`（订阅与 provider 拉取 + 缓存，遵循 HTTP(S)_PROXY）/ `rpc`（类型 + 客户端）/ `daemon`；`cmd/velumd`、`cmd/vlu`。
- `vlu`：up / down / status / reload / why [--live --resolve --process --udp --all --json] / conns [-n -f] / warnings。
- 测试：
  - 真实 sing-box 引擎端到端：随机端口加本地 httptest，不出网；验证规则下标、来源、出站、REJECT 记录；
  - DIRECT 域名必须经 DNS 规则解析（变异检查有效）；
  - daemon 使用假引擎测试：本地接口、reload 失败回滚、拒绝第二个实例、空配置启动；
  - provider 回落顺序；静态推演的各类场景。
- 本机真实配置（隔离目录 `/tmp/velum-dev`，只开 7899，不碰系统代理）：`vlu why` 静态推演与 `--live` 实录一致。已验证 overlay 直连、hosts 覆盖、订阅规则集、REJECT、策略组链路展开。
- 发现并修复的问题：
  - **出站域名解析绕过 DNS 规则**（sing-box ≥1.12 语义）：overlay 内网域名会被拿去公网 DoH 解析。改为在直连规则前插入 `resolve`；节点改用 `domain_resolver`（详见 ARCHITECTURE §4.2）；
  - **provider 的 `path` 未作为回落**：拉取失败时直接报错。现在按 Clash 语义回落到 path 文件；
  - 渲染插入 resolve 后，内核规则下标与 IR 下标不一致：新增 `RuleIndex` 映射，保证静态推演和动态实录可以对照；
  - 引擎启动期间日志回调与锁死锁：生命周期锁与数据锁分离；
  - **撤回 M0 F3**：「非 root 查不到其他进程连接」是 agent 执行环境造成的误判，用户态 velumd 实测可以查到。

### 第 3 步 ✅（2026-10-16）

- `sysproxy`：基于 networksetup 实现快照、接管、还原、启动自愈。
  - 接管时 HTTP / HTTPS / SOCKS 全部指向 Velum；豁免列表完全来自计划；PAC 和自动发现在接管期间关闭。
  - 只操作已启用的硬件服务（Device 非空），企业 VPN、其他代理工具等 NE 类服务不动。
  - 自愈：系统代理仍指向 Velum 时按快照还原；用户已手动改过时只丢弃快照，不覆盖。
  - 读取实际生效的代理（`scutil --proxy`），发现被主网络服务覆盖时在 status 中提示。
- daemon：启动先自愈；引擎启动成功后才接管；reload 时同步；退出时先还原再停引擎。`vlu sysproxy on|off|config` 在运行时切换（不改写 config.yaml）。velumd 不在时，`vlu down` 在本地执行自愈。
- 测试：模拟 networksetup 的单元测试（往返还原、崩溃自愈、不覆盖用户改动、跳过禁用服务和 VPN 服务）；daemon 使用假的 SysProxy 测试，`make test` 不触碰真实系统。
- 真机验证（经老板确认）：
  - ① 闲置服务 `AX88179A 2`：写入 611ms、还原 661ms，还原后功能等价，无需管理员授权；
  - ② 4 个硬件服务真实接管：写入、reload 后保持、Stash 5 秒内未抢回；`kill -9` 后 `vlu down` 自愈还原为 Stash 设置（2.3s，之后已优化判定逻辑）；结束后豁免列表与测试前一致。保险措施：独立还原脚本、脱离对话的后台测试、90 秒定时强制还原。
- 发现：
  - **其他 VPN / 代理工具占据主网络服务时（如 Stash 增强模式），它们隧道下发的代理设置优先，写入各硬件服务的设置不生效**（`scutil --proxy` 仍是 7890）。Velum 不覆盖其他工具的设置，改为检测实际生效的代理并在 `vlu status` 中提示。
  - networksetup 无法清空代理地址字段：原本未设置的代理还原后为「关闭 + 残留地址」，功能等价。
- 限制（M1.5 解决）：没有 helper 时不存在实时看门狗；velumd 被 kill 后系统代理仍指向 Velum，直到下次 `vlu up` 或 `vlu down` 才自愈。

### M1 回顾与补缺（2026-10-16）

对照验收标准：编译链路、运行时、动态实录与静态推演一致性、系统代理均已完成。「日常用 `vlu up` 接管本机」在机制上成立，但在同时开着 Stash 增强模式的机器上，生效的系统代理会被其隧道覆盖（已能检测并提示）。

补齐的缺口：

| 级别 | 缺口 | 处理 |
| :--- | :--- | :--- |
| P0 | 远程规则集（GeoIP / GeoSite）由 sing-box 走 DIRECT 下载，**首次下载失败会导致整个引擎无法启动**，而且没有缓存 | 改由 Velum 下载：遵循 HTTPS_PROXY，带缓存，失败时回落缓存，以本地 `.srs` 交给内核；无缓存时该规则集降级为空集并告警，不阻断启动（sing-box 不接受空的内联集，用 `.invalid` 占位） |
| P1 | `update_interval` 未实现 | 后台定时 reload（拉取带 TTL）；**内核配置未变化时不重启引擎**；新增 `vlu update` 强制刷新 |
| P1 | 日志无限增长、噪音大 | 日志 10MB × 3 轮转；模板默认 `log_level: warn`；修复内核日志 output 为空时默认写 stderr、导致 `velumd.out` 被全量 debug 日志灌满的问题 |
| P2 ✅ | 静态推演无法理解远程规则集 | 已完成（见下） |

### P2：静态推演理解 GeoIP / GeoSite ✅（2026-10-16）

- `engine/singbox.LoadRuleSetFile`：用 sing-box 的 srs 解析器（recover 模式）把本地 `.srs` 或 source JSON 还原为 IR 条件。`explain` 包仍不依赖 sing-box（daemon 注入加载函数）。规则集按路径与修改时间缓存。
- **解析结果与内核一致**：`vlu why` 未带 `--resolve` 时，velumd 用运行中引擎的 DNS 路由器解析目标，与真实连接使用同一套 DNS 规则，不受系统 DNS / fake-ip 影响。`--no-resolve` 可关闭。
- **语义修正**：经过 resolve 动作但没有解析结果时，IP 条件（含规则集中的 IP 条目）判为「未知」，不再误判为「未命中」，并把结论标记为不确定。
- 本机验证：国内站点的推演使用了引擎解析结果，得到确定结论，且与 `--live` 实录命中同一条规则。
