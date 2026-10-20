# 订阅转换损耗报告：本机 Stash 配置 / 原始订阅（2026-10-16，两者内容一致）

- sing-box: v1.14.3，转换耗时 25ms
- **sing-box 整体校验（box.New）：✅ 通过**

## 1. 配置项转换结论

| 类别 | 支持 | 降级 | 不支持 |
| :--- | ---: | ---: | ---: |
| 节点 | 21 | 0 | 0 |
| 节点源 | 0 | 0 | 0 |
| 策略组 | 18 | 1 | 0 |
| 规则 | 35 | 0 | 0 |
| 规则集 | 32 | 0 | 0 |
| DNS | 6 | 3 | 0 |
| 全局 | 2 | 0 | 0 |

### 降级 / 不支持原因

| 类别 | 类型 | 结论 | 原因 | 次数 |
| :--- | :--- | :--- | :--- | ---: |
| DNS | nameserver | 降级 | 多上游并发竞速无对应，仅使用首个 | 1 |
| DNS | respect-rules | 降级 | DNS 查询按路由规则走代理：sing-box 只能按服务器设置 detour | 1 |
| DNS | use-system-hosts | 降级 | 交由 Velum DNSPlan 统一处理 hosts（Overlay） | 1 |
| 策略组 | ssid-policy | 降级 | Stash 私有：按 Wi-Fi 切换，需由 NetworkSense 场景触发实现，sing-box 无对应 | 1 |

## 2. 规则集语义等价探测

对每条原始规则生成正例与相似反例，期望值由 Clash 语义参考实现给出，实际值由 sing-box 规则匹配器给出。

- 规则集 32 个（classical×28、domain×3、ipcidr×1），规则 14502 条
- 探测 43244 次，**语义不一致 0 次**
- 含进程规则、已拆分为独立 rule 的规则集：3 个
- 体积：source JSON 404.0KB，编译为 .srs 100.5KB；加载为 sing-box 规则对象后堆内存增量约 573.1KB

| 规则类型 | 条数 | 降级 | 不支持 |
| :--- | ---: | ---: | ---: |
| DOMAIN | 5136 | 0 | 0 |
| DOMAIN-KEYWORD | 67 | 0 | 0 |
| DOMAIN-SUFFIX | 5152 | 0 | 0 |
| DOMAIN-WILDCARD | 70 | 70 | 0 |
| IP-CIDR | 4007 | 0 | 0 |
| IP-CIDR6 | 12 | 0 | 0 |
| PROCESS-NAME | 58 | 0 | 0 |

## 3. 内联规则（含 AND / OR / NOT）语义等价探测

- 内联规则 0 条（），探测 0 次，**语义不一致 0 次**
