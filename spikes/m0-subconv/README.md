# M0 Spike：订阅转换损耗（Clash/Stash YAML → sing-box）

> 对应 ARCHITECTURE §4.1，§7 M0 第 1 项。
> 结论：**可行 ✅**
> - 真实样本：节点、规则、规则集全部可转换，规则语义 0 差异，降级只出现在 DNS 和 Stash 私有字段。
> - 覆盖型合成样本：机场订阅常见的高级语法大部分可转换或可接受地降级，不支持项都有明确告警。
>
> 真实样本只有 1 份：本机 Stash 配置，与原始订阅逐字节一致。原始订阅来自自建服务，不含 vmess / trojan / 逻辑规则等语法，所以另外用 `testdata/coverage/` 合成样本补齐覆盖面。

## 1. 做了什么

`m0-subconv` 是一个独立命令行工具：

1. **转换**：Clash/Stash YAML → sing-box 配置（outbounds / endpoints / route / dns），rule-providers → sing-box 规则集源文件。每个配置项都记录「支持 / 降级 / 不支持」及原因。
2. **整体校验**：用 sing-box 自身的 `box.New` 加载生成的配置。
3. **规则语义等价探测**：
   - 用 Go 写了一份 **Clash 语义参考匹配器**；
   - 为每条原始规则生成正例和「相似但不应命中」的反例。例如 `DOMAIN-SUFFIX,x` 生成 `x`、`a.b.x` 和 `zzx` 三个探测；CIDR 生成首地址、末地址和越界地址；
   - 期望值由参考实现给出，实际值由 sing-box 的 `rule.NewHeadlessRule().Match()` 给出，两者逐一比对。
4. **探测框架自检**：用 `-mutate` 故意引入已知的语义错误，确认框架能发现。

```bash
go build -tags with_wireguard,with_utls,with_quic,with_gvisor -o /tmp/subconv .   # GOPROXY=https://goproxy.cn,direct
/tmp/subconv -label "xxx" -public REPORT.md       # 默认读取本机 Stash 配置
/tmp/subconv -config testdata/coverage/config.yaml -base testdata/coverage -out /tmp/subconv-cov -public REPORT-coverage.md
# 私有明细（含名称）：/tmp/velum-subconv/report-private.md；生成的配置也在该目录，均不提交
```

## 2. 结果

### 2.1 真实样本（详见 [REPORT.md](REPORT.md)）

| 维度 | 结果 |
| :--- | :--- |
| 节点 21 个（anytls / ss / vless+reality / wireguard） | 全部支持 |
| 策略组 19 个（select / url-test） | 18 个支持，1 个降级（Stash 私有的 `ssid-policy`） |
| 规则 34 条 + 规则集 32 个（14502 条规则） | 全部支持 |
| 规则语义探测 43244 次 | **0 不一致** |
| 探测框架自检 | 「进程规则不拆分」→ 903 次不一致；「后缀规则只匹配子域」→ 4969 次不一致，两类错误都能被发现 |
| sing-box 整体校验 | 通过 |
| 体积 / 内存 | source JSON 404KB → .srs 101KB；加载后堆内存增量约 0.4MB（不含 GeoIP） |

### 2.2 覆盖型合成样本（详见 [REPORT-coverage.md](REPORT-coverage.md)）

样本见 `testdata/coverage/`：服务器均为 `example.com`，凭据均为假数据，可直接提交。

| 维度 | 结果 |
| :--- | :--- |
| 节点 27 个：vmess（ws / grpc / h2）、trojan、vless（reality / ws）、hysteria / hysteria2（端口跳跃、obfs）、tuic、ss（2022 / obfs / shadow-tls / restls）、ssr、snell v3 / v4、socks5、http、wireguard、smux | 22 支持；2 降级（hysteria `faketcp`、`smux`）；3 不支持（ssr、snell v3、restls） |
| proxy-providers 2 个（`filter` / `exclude-filter` / `override`） | 编译期展开为普通节点；`override` 未实现 |
| 策略组 9 个：`use` / `include-all*` / `filter` / `exclude-type` / fallback / load-balance / relay | 动态成员在编译期展开；fallback 和 load-balance 降级为 urltest；含前瞻断言的 `filter` 降级（Go RE2 不支持）；第二跳是策略组的 relay 不支持 |
| 规则 25 条，含 AND / OR / NOT 嵌套、GEOSITE、DOMAIN-REGEX、SRC-IP-CIDR | 21 支持；IP-ASN、SCRIPT、SUB-RULE 不支持 |
| 规则集 3 个（yaml domain / classical 混合类型 / text ipcidr） | classical 中的 IP-ASN、USER-AGENT、URL-REGEX 不支持 |
| DNS：`geosite:` / `rule-set:` 策略、DoT / DoQ、fallback-filter | fallback / fallback-filter 不支持，其余支持 |
| 语义探测：规则集 50 次 + 内联规则 64 次 | **0 不一致**；自检「去掉 NOT 取反」→ 4 次不一致，能被发现 |
| sing-box 整体校验 | 通过 |

调试中由探测和抽查发现、已修复的问题：
- hysteria2 端口跳跃里的单个端口必须写成 `40000:40000`（转换 bug，`box.New` 报错发现）；
- `exclude-type` 要用 mihomo 适配器类型名（`Shadowsocks`、`Vmess`…）而不是 YAML type（转换 bug，抽查生成配置发现）；
- 探测元数据默认端口 443 与参考实现不一致（框架 bug，造成 2 次假报）。

## 3. 发现（需回写架构）

| # | 发现 | 处理 |
| :--- | :--- | :--- |
| F1 | **sing-box 同一条规则内，域名 / IP 条件与 `process_name` 等其他条件是「与」关系**。classical 规则集里混合的 `PROCESS-NAME` 不拆开，域名规则就会失效（自检中 740 条后缀规则失效） | 转换器按「地址类 / 进程 / 端口 / 源 IP」拆成多条 headless rule，rule 之间为「或」 |
| F2 | 需要以下构建标签：`with_wireguard`（WireGuard）、`with_utls`（reality / 指纹）、`with_quic`（hysteria2 / tuic） | 写入构建配置；对 iOS 二进制体积和内存的影响在 M0 iOS 项中实测 |
| F3 | `DOMAIN-WILDCARD`（Surge / Stash 私有）只能转为 `domain_regex` | 语义一致，但正则匹配远慢于后缀树；规则多时考虑展开为 suffix |
| F4 | Clash text 域名格式：`+.x`（根域 + 子域）→ `domain_suffix: x`；`.x`（仅子域）→ `domain_suffix: .x`；`*.x`（单级子域）→ regex | 已用探测验证 |
| F5 | IP 规则没写 `no-resolve` 时，Clash 会按需解析域名 | 在首条需要解析的规则前插入 `{"action":"resolve"}`。**路由级行为未探测**，留到 M1 做端到端验证 |
| F6 | DNS 有三处降级：①多个 nameserver 并发竞速 → 只取首个；②`respect-rules`（DNS 查询按路由规则走代理）无对应；③`use-system-hosts` | ①可接受；②③由 Velum DNSPlan 实现（与 Overlay hosts 统一） |
| F7 | `ssid-policy`（按 Wi-Fi 切换策略）是 Stash 私有字段 | 由 NetworkSense 场景触发器在运行时调用 selector API 实现 |
| F8 | Stash 的 rule-provider 相对路径以 `Core/resources/` 为基准 | 导入 Stash 配置时需要注意 |
| F9 | `proxy-providers` 和策略组动态成员（`use` / `include-all` / `filter`）在 sing-box 中没有运行期对应 | **由 Velum 在编译期展开**，订阅或 provider 更新时重新编译。mihomo 的 `filter` 使用 regexp2，Go RE2 不支持前瞻断言，需要把常见写法 `^(?!.*X).*$` 改写为 exclude |
| F10 | `relay` 改写为 detour 链：后续各跳克隆节点并挂 detour。第一跳可以是策略组，后续各跳必须是普通节点 | 不满足条件时告警 |
| F11 | ss 的 shadow-tls 插件改为 `shadowtls` 出站加 detour；snell 只支持 v4 / v6（Clash 常见的 v1～v3 不支持）；ssr、restls、hysteria `faketcp` 不支持 | 告警；不支持的节点从策略组中剔除 |
| F12 | 不支持的规则类型：IP-ASN（sing-box 没有 ASN 库）、USER-AGENT、URL-REGEX（需要 MITM）、SCRIPT、SUB-RULE；DNS fallback-filter 不支持 | 告警。fallback-filter 可以改写为基于 GeoIP 的 DNS 规则，作为后续优化 |

## 4. 未覆盖

- **真实数据有限**：高级语法只在合成样本上验证过。拿到真实机场订阅后，可以用 `-config` 直接复跑。
- 路由级端到端行为（`resolve` 插入、`final`、reject）未验证，需要启动引擎做实际连接测试。
- GeoIP / GeoSite 远程规则集的下载与内存占用。
