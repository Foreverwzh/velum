# Velum 对 wireguard-go 的改动

基于 `github.com/sagernet/wireguard-go v0.0.8-0.20260929150556-6731c7387c81`，只改了一个常量：`device/queueconstants_default.go`（macOS、Linux）和 `device/queueconstants_android.go` 里的 `MaxSegmentSize`，从 64KB 改为 `2048 - 32`。Windows 上游就是这个值，iOS 是 1700，都没有改。

## 为什么

每个包都占一块这么大的缓冲，而 WireGuard 的包实际约 1400 字节。流量大时队列里的包都要占缓冲，64KB 的缓冲会让内核涨到 200MB 以上，负载结束后也不会马上还给系统。

## 影响

MTU 超过 1984（`MaxContentSize`）时，wireguard-go 会自动调低。如果对端发来更大的包，会被截断而丢弃，所以 Velum 在配置 MTU 超限时给出告警。

## 升级 sing-box 时

`core/go.mod` 里的版本变了，`TestWireGuardPatch` 就会失败。用新版本重新复制到这里，重新应用上面的改动，并更新 `.velum-base`。
