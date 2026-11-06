# Velum iOS（SwiftUI）

设计、架构与进度见 [`docs/IOS-PLAN.md`](../../docs/IOS-PLAN.md)。隧道还没有在真机上验证。

```sh
sh scripts/core.sh       # Go 核心 → build/*.xcframework
xcodegen generate        # 生成 Velum.xcodeproj（不入库）
xcodebuild test -project Velum.xcodeproj -scheme Velum \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```
