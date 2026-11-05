# iOS spike：网络扩展的内存够不够用

回答一个问题：真实订阅在 iOS 隧道扩展里会不会超过系统的内存上限。扩展只运行内核（`core/iosprobe`），配置在 Mac 上预先生成。

## 结果（2026-11-05，iPhone 12 Pro，iOS 27.0.1）

配置：真实订阅，19 个节点、33 个规则集、1 个 WireGuard；gVisor 协议栈；隧道内出口确认是节点（`loc=HK`）。

| 场景 | Go 参数 | 系统上限 | 峰值 |
| :--- | :--- | :--- | :--- |
| 节点：20 并发下载，再加 30MB 大文件（约 270KB/s） | 默认 | 50.0MB | 30.3MB |
| 直连高速：4 并发，381MB，约 80MB/s | 默认 | 50.0MB | 29.4MB |
| 直连高速：同上 | `GOGC=50`、`GOMEMLIMIT=40MiB` | 50.0MB | 25.0MB |

全程隧道没有断开，也没有新的崩溃或 Jetsam 记录。上限来自 `os_proc_available_memory()` 加 `phys_footprint`，每轮 223 次以内的采样里都是 50.0MB。

结论：内存不阻塞 iOS，不需要先精简规则集。

## 这些数字不能说明的

- **只有内核**：没有 daemon、订阅转换、连接记录的本地接口。真实产品放进扩展的内容会更多。
- **采样间隔 1 秒**：高速下载只持续约 5 秒，短于 1 秒的尖峰可能漏掉。
- **运行时间短**：每轮只有几分钟，没有测长时间使用、日常浏览、大量短连接和 QUIC。
- **单一设备和系统**：其他 iOS 版本的上限可能不同，Apple 没有公开这个数字。
- **WireGuard 没有走流量**：它只是在配置里，初始化的缓冲算进去了，但没有实际转发。

## 踩过的坑

- iOS 27 要求 App 使用 Scene 生命周期，否则启动时被系统终止（`UIApplicationEvaluateRuntimeIssueForNoSceneLifecycleAdoption`）。
- 链接扩展需要 `UIKit`、`SystemConfiguration`、`Security`、`libresolv`。
- `owner_darwin.go` 用了 `libproc.h`，iOS SDK 没有，所以按 `darwin && !ios` 排除。

## 复现

`build.sh <UDID>` 构建并安装。配置和 `run.json` 用 `devicectl device copy to` 推送到 App 的 `Documents`（**配置含节点信息，不要打进安装包或提交**），启动 App 后自动跑一轮，结果写到 `Documents/result-<label>.log`。卸载 App 即可清除配置。

Mac 上的内存探针：`core/internal/memprobe`。
