#!/bin/sh
# 用 gomobile 把 Go 核心（core/mobilebind）编译为 Android AAR：android/app/libs/velum.aar。
# 需要：Go、gomobile / gobind（版本与 core/go.mod 中的 golang.org/x/mobile 一致）、Android SDK + NDK、JDK。
#   ANDROID_HOME     Android SDK 目录
#   ANDROID_NDK_HOME NDK 目录（默认取 $ANDROID_HOME/ndk 下最新版本）
#   TARGETS          默认 android/arm,android/arm64,android/amd64（与 Flutter 打包的架构一致：32 / 64 位 ARM 真机、x86_64 模拟器）
set -eu
cd "$(dirname "$0")/../../core"
: "${ANDROID_HOME:?需要设置 ANDROID_HOME}"
if [ -z "${ANDROID_NDK_HOME:-}" ]; then
  ANDROID_NDK_HOME="$ANDROID_HOME/ndk/$(ls "$ANDROID_HOME/ndk" | sort -V | tail -1)"
  export ANDROID_NDK_HOME
fi
TAGS=$(sed -n 's/^TAGS ?= //p' Makefile)
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
out=../mobile/android/app/libs/velum.aar
mkdir -p "$(dirname "$out")"
# 发布构建由 CI 传入 VELUM_VERSION（如 0.1.0-rc.1），写入核心版本号
LDFLAGS="-s -w"
[ -n "${VELUM_VERSION:-}" ] && LDFLAGS="$LDFLAGS -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
gomobile bind -target "${TARGETS:-android/arm,android/arm64,android/amd64}" -androidapi 29 \
  -javapkg io.github.foreverwzh.velum.core -tags "$TAGS" -trimpath -ldflags "$LDFLAGS" \
  -o "$out" ./mobilebind
rm -f "${out%.aar}-sources.jar"
ls -l "$out"
