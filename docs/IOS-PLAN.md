# iOS 实施计划

> 对应 ARCHITECTURE §3.4 / §7 M3。内存验证见 `spikes/ios-memory/README.md`。

## 已确认（2026-11-06）

| 问题 | 结论 |
| :--- | :--- |
| 技术 | SwiftUI 原生，工程在 `mobile/ios`（XcodeGen）。Android 继续用 Flutter，两端共用 Go 核心，不共用界面 |
| 第一屏 | 一问一答：状态、当前线路、需要留意，底部常驻「节点、或任意网址」 |
| 分发（D6） | 先只给自己装（开发者配置文件，约一年到期）。TestFlight 和上架以后再评估，GPLv3 与 App Store 条款的问题见 D8 |
| 最低系统 | iOS 26.1（`tabViewBottomAccessory`）。只做 iPhone，竖屏 |

## 界面与交互

三个标签：状态、活动、设置。标签之上常驻提问入口，点开是搜索面板，输入框在底部，单手可及。

| 页面 | 回答的问题 | 关键交互 |
| :--- | :--- | :--- |
| 状态 | 有没有在保护？走哪条线路？ | 开关就地反映「想要的状态」，操作中显示进度。「需要留意」只在有事时出现，影响当下使用的排在前面 |
| 问一问 | 这个节点在哪？这个地址怎么走？ | 输入地址，答案（路由链和规则）直接出现在结果里；输入节点名，就地切换，不关闭面板 |
| 解释 | 为什么这样走？ | 结论 → 证据（DNS、命中的规则和来源行、未命中的规则）→ 行动（本地规则）。加规则后重新推演，结论就地变化 |
| 活动 | 刚才发生了什么？ | 每条连接显示地址和路由链，可筛选失败。没有应用名：iOS 不提供连接归属 |
| 设置 | 订阅、规则、排查 | 订阅点一下切换，左滑更新或删除；一键修复就地显示每一步的结果 |
| 首次引导 | 怎么开始？ | 添加订阅（先校验再保存）→ 如实显示导入结果和无法转换的内容 → 说明 VPN 权限后连接 |

状态与桌面端同一口径：暂停是琥珀色并写明原因（开关也是琥珀色），只有失败才是红色；状态同时用图形和文字表达。

不做的事：

- **「让这个地址走某个策略组」**：overlay 只支持直连和固定 IP，界面不能承诺核心没有的能力。
- **系统代理、按应用分流、进程规则**：iOS 没有这些能力。订阅里的进程规则如实显示在「转换提醒」。
- **大电源按钮**：常用操作是确认状态和换节点，开关不需要占半个屏幕。

## 架构

iOS 与 Android 的根本区别：界面和隧道必须是两个进程，隧道进程的内存上限实测 50MB，只跑内核就有 25–30MB。

| 进程 | 内容 |
| :--- | :--- |
| App（只在前台时活着） | SwiftUI；完整 daemon：订阅拉取与转换、编译、Explain、overlay、告警 |
| 隧道扩展（后台常驻，≤ 50MB） | 只有内核：读 App Group 里编译好的配置 |

**切分点是 `engine.Engine` 接口**（`core/mobilebind/split.go`）：App 里的 daemon 不变，只把引擎换成转发实现，Explain 和规则编译全部复用。

| 情况 | 行为 |
| :--- | :--- |
| 未连接 | App 内运行本地内核，所以未连接时也能测速、解析、切换节点 |
| 连接 | 停本地内核，配置写到 App Group（`core/pkg/engine/tunnelproto`），启动隧道 |
| 连接中配置变化 | 扩展重新加载，隧道不重建 |
| 配置没变、隧道还在（重新打开 App） | 不打断现有连接 |
| App 退出 | 隧道继续运行 |
| 系统拉起隧道，App 才启动 | 不停隧道，把开关同步过去 |
| 扩展被单独拉起 | 配置和选择都已落盘（`selected.json`） |

- 选择、延迟、流量一次批量取回并缓存 1 秒：一次刷新要查几百次，不能每次都发消息给扩展。
- 扩展只保留 200 条连接记录，GOGC=50、GOMEMLIMIT=40MiB。
- 两个 framework 分开编译（`scripts/core.sh`），扩展不带 daemon 的依赖。
- 不开本地代理端口：iOS 上别的应用也能连本机回环，既没用处，也会被用来探测代理。
- 主线程不能等 NE 的回调（回调就在主线程）。接真实核心时踩过两次，见 `TunnelController` 的注释。

已知风险，要在真机上确认：

- **DNS**：iOS 没有公开接口读物理网络的 DNS。现在用系统解析器的结果（隧道开着时用切换前记下的值），换网后可能过期。
- **内存**：真机只测过纯内核，现在多了规则来源和连接记录，见 I3。
- **隧道被杀后恢复**：On-Demand 还没做，见 I4。

## 分步

| 步骤 | 内容 | 状态 |
| :--- | :--- | :--- |
| I1 界面 | 全部页面、状态、演示核心、7 条 UI 流程测试 | ✅ 模拟器（iOS 27.2） |
| I2 进程切分 | `core/mobilebind` 的 iOS 入口、转发引擎、App Group、隧道扩展 | 模拟器：真实核心通过；隧道待真机 |
| I3 真机内存 | 完整产品在 iPhone 上：长时间运行、大量短连接、QUIC、**WireGuard 高速**（模拟估算峰值约 55MB，可能超限） | 需要真机 |
| I4 网络变化 | Wi-Fi ↔ 蜂窝、Captive、其他 VPN 抢占、被系统结束后恢复（On-Demand） | 需要真机 |
| I5 系统入口 | 控制中心开关、锁屏小组件、快捷指令 | |

## 验证现状

模拟器上验证过：界面和交互（演示数据，7 条流程）；真实核心的订阅转换、Explain、切换节点、本地内核（2 条流程）；转发引擎和扩展入口的单元测试。**模拟器运行不了隧道，隧道本身还没有在真机上跑过。**

```sh
cd mobile/ios && sh scripts/core.sh && xcodegen generate
xcodebuild test -project Velum.xcodeproj -scheme Velum -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

真实核心的两条流程需要一个本机订阅：`TEST_RUNNER_SUB_URL=http://127.0.0.1:端口/sub.yaml`，没有时跳过。

查看某个状态：`xcrun simctl launch <模拟器> io.github.foreverwzh.velum -scenario paused`（protected、off、firstRun、paused、captive、error、attention、denied；可加 `-tab activity`、`-ask github.com`、`-push explain:github.com`）。
