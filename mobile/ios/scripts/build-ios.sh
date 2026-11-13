#!/usr/bin/env bash
# 出 iOS 发布包（App Store Connect / TestFlight 用）。
#
#   sh scripts/build-ios.sh            出 build/ipa/Velum.ipa，再用 Transporter 或 Xcode 上传
#   sh scripts/build-ios.sh --upload   出包后直接传到 App Store Connect（发版流水线用这个）
#
# App 用 Rust 控制面（VelumCore），隧道用 velum-engine：从本机的 velum-engine 编译或按 engine.lock 下载，附第三方声明。
#
# 版本号取 VELUM_VERSION，没设就取当前提交上的 v* 标签。Apple 只认纯数字的 x.y.z，所以去掉 -rc.N：
# 同一版本的 rc 和正式版共用一个版本号，在 TestFlight 里靠构建号区分。构建号默认取 UTC 时间
# （两位年、一年中的第几天、时、分），只增不减，又在 32 位整数以内；要指定就设 VELUM_IOS_BUILD_NUMBER。
#
# 签名是自动签名：本地用 Xcode 里登录的账号；没登录账号的机器（CI）设 ASC_KEY_PATH、ASC_KEY_ID、
# ASC_ISSUER_ID，用 App Store Connect API 密钥代替账号。
set -euo pipefail

cd "$(dirname "$0")/.."
die() { echo "✗ $*" >&2; exit 1; }

upload=false
for a in "$@"; do
  case "$a" in
    --upload) upload=true ;;
    *) die "不认识的参数：${a}（只有 --upload）" ;;
  esac
done

version="${VELUM_VERSION:-}"
if [ -z "$version" ]; then
  tag="$(git describe --tags --exact-match --match 'v*' 2>/dev/null)" \
    || die "没设 VELUM_VERSION，当前提交上也没有 v* 标签"
  version="${tag#v}"
fi
export VELUM_VERSION="$version"
marketing="${version%%-*}"
[[ "$marketing" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "版本号要是 x.y.z（可带 -rc.N）：$version"
build_number="${VELUM_IOS_BUILD_NUMBER:-$(date -u +%y%j%H%M)}"
[[ "$build_number" =~ ^[0-9]+$ ]] || die "构建号要是整数：$build_number"

signing=(-allowProvisioningUpdates)
if [ -n "${ASC_KEY_PATH:-}" ]; then
  [ -n "${ASC_KEY_ID:-}" ] && [ -n "${ASC_ISSUER_ID:-}" ] \
    || die "设了 ASC_KEY_PATH 就还要设 ASC_KEY_ID 和 ASC_ISSUER_ID"
  signing+=(
    -authenticationKeyPath "$ASC_KEY_PATH"
    -authenticationKeyID "$ASC_KEY_ID"
    -authenticationKeyIssuerID "$ASC_ISSUER_ID"
  )
fi

archive=build/archive/Velum.xcarchive
export_dir=build/ipa
export_options=ExportOptions.plist
if $upload; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  export_options="$tmp/ExportOptions.plist"
  cp ExportOptions.plist "$export_options"
  plutil -replace destination -string upload "$export_options"
fi

echo "▶ iOS $marketing ($build_number)，出自 $version"
# 发布包只要真机架构
sh scripts/engine.sh
sh scripts/notices.sh
xcodegen generate
rm -rf "$archive" "$export_dir"
xcodebuild archive -quiet \
  -project Velum.xcodeproj -scheme Velum -configuration Release \
  -destination generic/platform=iOS -archivePath "$archive" "${signing[@]}" \
  MARKETING_VERSION="$marketing" CURRENT_PROJECT_VERSION="$build_number"
xcodebuild -exportArchive -quiet \
  -archivePath "$archive" -exportPath "$export_dir" \
  -exportOptionsPlist "$export_options" "${signing[@]}"

if $upload; then
  echo "✓ 已传到 App Store Connect，处理完会出现在 TestFlight"
else
  echo "✓ 产物：$(ls "$export_dir"/*.ipa)"
fi
