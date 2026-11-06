#!/bin/sh
# 用 gomobile 编译 Go 核心：App 用 Mobilebind（完整 daemon），隧道扩展用 Iostunnel（只有内核）。
# 分开编译：扩展的内存上限约 50MB，不应该带上 daemon 的依赖。
# 需要：Xcode、与 core/go.mod 中 golang.org/x/mobile 同版本的 gomobile / gobind。
#   TARGETS  默认 ios/arm64,iossimulator/arm64
set -eu
cd "$(dirname "$0")/../../../core"
TAGS=$(sed -n 's/^TAGS ?= //p' Makefile)
out=../mobile/ios/build
mkdir -p "$out"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export PATH="$PATH:$(go env GOPATH)/bin"
LDFLAGS="-s -w"
[ -n "${VELUM_VERSION:-}" ] && LDFLAGS="$LDFLAGS -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
for pkg in mobilebind:Mobilebind iostunnel:Iostunnel; do
  gomobile bind -target "${TARGETS:-ios/arm64,iossimulator/arm64}" -iosversion 16.0 -tags "$TAGS" -trimpath -ldflags "$LDFLAGS" \
    -o "$out/${pkg#*:}.xcframework" "./${pkg%:*}"
done
du -sh "$out"/*.xcframework
