# Velum iOS（SwiftUI）

设计、架构与进度见 [`docs/IOS-PLAN.md`](../../docs/IOS-PLAN.md)。不含 Go：App 用 Rust 控制面（velum-core），隧道扩展用 velum-engine。

```sh
sh scripts/engine.sh     # velum-engine（Rust）→ build/VelumCore.xcframework、build/VelumEngine.xcframework
xcodegen generate        # 生成 Velum.xcodeproj（不入库）
xcodebuild test -project Velum.xcodeproj -scheme Velum \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

## 发 TestFlight

推送 `v*` 标签时，`release.yml` 调用 `ios.yml`，在 `macos-26` 上跑 `scripts/build-ios.sh --upload`
（预编译的 velum-engine 按 `engine.lock` 从私有仓库的 Release 下载并校验 sha256）：
版本号是标签去掉 `-rc.N`，构建号是 UTC 时间（`%y%j%H%M`）。传完在 App Store Connect 处理好后出现在 TestFlight，
只发内部测试组。本地出包：`sh scripts/build-ios.sh`（用 Xcode 里登录的账号签名，产物在 `build/ipa/`）。

一次性准备：

- App Store Connect 里建好 App，bundle id `io.github.foreverwzh.velum`。扩展的 App ID、App Group 和描述文件由
  `-allowProvisioningUpdates` 自动处理。
- 仓库 secrets：

| secret | 内容 |
| :--- | :--- |
| `APPLE_API_PRIVATE_KEY` | App Store Connect API 密钥 `.p8` 的全文，和桌面公证共用；角色要 Admin（要用云端托管的分发证书） |
| `APPLE_API_KEY` | 密钥 ID |
| `APPLE_API_ISSUER` | Issuer ID |
| `IOS_DEV_CERT_P12_BASE64` | Apple Development 证书连私钥导出的 `.p12`，`base64 -i x.p12` |
| `IOS_DEV_CERT_P12_PASSWORD` | `.p12` 的密码 |
| `VELUM_ENGINE_TOKEN` | 能读 `Foreverwzh/velum-engine` 的 fine-grained 令牌（Repository access 只选这个仓库，权限 Contents: Read-only），下载预编译引擎 |

更新引擎：在 velum-engine 里运行 `scripts/release-ios.sh <版本>`，把它最后打印的几行写进 `engine.lock`。
本机有 velum-engine 源码时 `scripts/engine.sh` 直接从源码编译；`VELUM_ENGINE_DOWNLOAD=1` 强制按 `engine.lock` 下载。
