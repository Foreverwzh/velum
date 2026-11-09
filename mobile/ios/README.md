# Velum iOS（SwiftUI）

设计、架构与进度见 [`docs/IOS-PLAN.md`](../../docs/IOS-PLAN.md)。隧道还没有在真机上验证。

```sh
sh scripts/core.sh       # Go 核心 → build/*.xcframework
xcodegen generate        # 生成 Velum.xcodeproj（不入库）
xcodebuild test -project Velum.xcodeproj -scheme Velum \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

## 发 TestFlight

推送 `v*` 标签时，`release.yml` 调用 `ios.yml`，在 `macos-26` 上跑 `scripts/build-ios.sh --upload`：
版本号是标签去掉 `-rc.N`，构建号是 UTC 时间（`%y%j%H%M`）。传完在 App Store Connect 处理好后出现在 TestFlight，
只发内部测试组。本地出包：`sh scripts/build-ios.sh`（用 Xcode 里登录的账号签名，产物在 `build/ipa/`）。

一次性准备：

- App Store Connect 里建好 App，bundle id `io.github.foreverwzh.velum`。扩展的 App ID、App Group 和描述文件由
  `-allowProvisioningUpdates` 自动处理。
- 仓库 secrets（和 smelt 同一个团队，可以用同一套）：

| secret | 内容 |
| :--- | :--- |
| `ASC_API_KEY_P8` | App Store Connect API 密钥 `.p8` 的全文，角色要 Admin（要用云端托管的分发证书） |
| `ASC_API_KEY_ID` | 密钥 ID |
| `ASC_API_ISSUER_ID` | Issuer ID |
| `IOS_DEV_CERT_P12_BASE64` | Apple Development 证书连私钥导出的 `.p12`，`base64 -i x.p12` |
| `IOS_DEV_CERT_P12_PASSWORD` | `.p12` 的密码 |
