#!/bin/sh
# 构建并安装到指定的 iPhone。用法：build.sh <设备 UDID>
# 需要：Xcode（已登录开发者账号）、xcodegen、与 core/go.mod 同版本的 gomobile / gobind。
set -eu
cd "$(dirname "$0")"
udid="$1"
tags="$(sed -n 's/^TAGS ?= //p' ../../core/Makefile)"
mkdir -p build
(cd ../../core && gomobile bind -target ios/arm64 -iosversion 16.0 -tags "$tags" -trimpath -ldflags "-s -w" -o ../spikes/ios-memory/build/Iosprobe.xcframework ./iosprobe)
xcodegen generate --quiet
xcodebuild -project VelumIOSSpike.xcodeproj -scheme VelumIOSSpike -configuration Release -destination "id=$udid" \
  -derivedDataPath build/dd -allowProvisioningUpdates build -quiet
xcrun devicectl device install app --device "$udid" build/dd/Build/Products/Release-iphoneos/VelumIOSSpike.app
