# Velum iOS（SwiftUI）

设计、架构与进度见 [`docs/IOS-PLAN.md`](../../docs/IOS-PLAN.md)。目前只有界面和演示数据，还不能真正连接。

```sh
xcodegen generate        # 生成 Velum.xcodeproj（不入库）
xcodebuild test -project Velum.xcodeproj -scheme Velum \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```
