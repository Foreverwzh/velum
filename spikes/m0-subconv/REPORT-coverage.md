# 订阅转换损耗报告：覆盖型合成样本（testdata/coverage）

- sing-box: v1.14.3，转换耗时 4ms
- **sing-box 整体校验（box.New）：✅ 通过**

## 1. 配置项转换结论

| 类别 | 支持 | 降级 | 不支持 |
| :--- | ---: | ---: | ---: |
| 节点 | 22 | 2 | 3 |
| 节点源 | 1 | 1 | 0 |
| 策略组 | 5 | 3 | 1 |
| 规则 | 21 | 1 | 3 |
| 规则集 | 2 | 1 | 0 |
| DNS | 4 | 1 | 1 |
| 全局 | 2 | 0 | 0 |

### 降级 / 不支持原因

| 类别 | 类型 | 结论 | 原因 | 次数 |
| :--- | :--- | :--- | :--- | ---: |
| DNS | fallback/fallback-filter | 不支持 | 无对应，需改写为 DNS 规则 | 1 |
| DNS | nameserver | 降级 | 多上游并发竞速无对应，仅使用首个 | 1 |
| 策略组 | fallback | 降级 | 降级为 urltest：按延迟而非顺序选择 | 1 |
| 策略组 | filter | 降级 | filter 正则含 Go RE2 不支持的语法，已忽略过滤 | 1 |
| 策略组 | load-balance | 降级 | 降级为 urltest：无负载均衡（strategy=round-robin） | 1 |
| 策略组 | relay | 不支持 | 非首跳成员是策略组或 endpoint，无法挂 detour | 1 |
| 节点 | hysteria | 降级 | protocol=faketcp（faketcp / wechat-video）无对应 | 1 |
| 节点 | snell v3 | 不支持 | sing-box 仅支持 snell v4 / v6 | 1 |
| 节点 | ss+restls | 不支持 | ss 插件 restls 无对应 | 1 |
| 节点 | ssr | 不支持 | sing-box 无对应协议或 spike 未实现 | 1 |
| 节点 | vmess | 降级 | 字段 smux 未映射 | 1 |
| 节点源 | override | 降级 | override 未实现 | 1 |
| 规则 | DOMAIN-WILDCARD | 降级 | 通配符转正则 | 1 |
| 规则 | IP-ASN | 不支持 | 子规则类型 IP-ASN 无对应 | 1 |
| 规则 | SCRIPT | 不支持 | 子规则 / 脚本规则无对应 | 1 |
| 规则 | SUB-RULE | 不支持 | 子规则 / 脚本规则无对应 | 1 |
| 规则集 | classical/yaml | 降级 | 含不支持的规则类型 map[IP-ASN:1 URL-REGEX:1 USER-AGENT:1] | 1 |

## 2. 规则集语义等价探测

对每条原始规则生成正例与相似反例，期望值由 Clash 语义参考实现给出，实际值由 sing-box 规则匹配器给出。

- 规则集 3 个（classical×1、domain×1、ipcidr×1），规则 23 条
- 探测 50 次，**语义不一致 0 次**
- 含进程规则、已拆分为独立 rule 的规则集：1 个
- 体积：source JSON 1.0KB，编译为 .srs 350B；加载为 sing-box 规则对象后堆内存增量约 67.5KB

| 规则类型 | 条数 | 降级 | 不支持 |
| :--- | ---: | ---: | ---: |
| DOMAIN | 2 | 0 | 0 |
| DOMAIN-KEYWORD | 1 | 0 | 0 |
| DOMAIN-SINGLE | 1 | 1 | 0 |
| DOMAIN-SUBONLY | 1 | 0 | 0 |
| DOMAIN-SUFFIX | 2 | 0 | 0 |
| DOMAIN-WILDCARD | 2 | 2 | 0 |
| DST-PORT | 1 | 0 | 0 |
| IP-ASN | 1 | 0 | 1 |
| IP-CIDR | 5 | 0 | 0 |
| IP-CIDR6 | 2 | 0 | 0 |
| NETWORK | 1 | 0 | 0 |
| PROCESS-NAME | 2 | 0 | 0 |
| URL-REGEX | 1 | 0 | 1 |
| USER-AGENT | 1 | 0 | 1 |

## 3. 内联规则（含 AND / OR / NOT）语义等价探测

- 内联规则 13 条（DOMAIN×1、DOMAIN-KEYWORD×1、DOMAIN-REGEX×1、DOMAIN-SUFFIX×1、DOMAIN-WILDCARD×1、DST-PORT×1、IP-CIDR×1、LOGICAL-AND×2、LOGICAL-NOT×1、LOGICAL-OR×1、PROCESS-NAME×1、SRC-IP-CIDR×1），探测 64 次，**语义不一致 0 次**
