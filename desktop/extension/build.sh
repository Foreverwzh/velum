#!/bin/sh
# 构建并以 Developer ID 签名系统扩展，产物放到 desktop/src-tauri/extension/，由 tauri.ne.conf.json 打进 Velum.app：
#   dev.velum.desktop.tunnel.systemextension   签好名的扩展（含其描述文件）
#   embedded.provisionprofile                   Velum.app 的描述文件（扩展安装、packet tunnel、App Group 是受限能力）
#   Velum.entitlements                          Velum.app 的 entitlements，与描述文件一致
#   bin/                                        预先签名的 velumd / velum-helper / vlu（不带 entitlements）
# Tauri 把同一份 entitlements 签进所有 externalBin，带受限 entitlements 的 sidecar 一启动就被系统杀掉（只有主程序认 App 的描述文件），
# 所以签名构建不用 externalBin，改由这里签好 sidecar、经 bundle.macOS.files 放进 Contents/MacOS（Tauri 不重签这些文件）。
#
# 两种签名方式（都需要 APPLE_SIGNING_IDENTITY，即 Developer ID Application 证书）：
#   本机：Xcode 登录的账号自动管理描述文件，导出占位宿主（Velum.app，内含扩展）并经 Xcode 账号公证。
#         sysextd 安装扩展时按代码哈希在线查询公证结果（未公证会被拒绝，-67050）；扩展公证过即可，外层 App 不必公证。
#   CI：  设置 VELUM_PROFILE_HOST / VELUM_PROFILE_TUNNEL（描述文件路径）。Xcode 只编译、不签名，这里用 codesign 签名：
#         Xcode 不允许手动签名使用 Xcode 托管的描述文件，也不会把 packet-tunnel-provider 换成 -systemextension（导出时才换）。
#         设置 APPLE_API_KEY_PATH / APPLE_API_KEY / APPLE_API_ISSUER 时用 notarytool 单独公证扩展；整个 Velum.app 由 Tauri 公证。
#   两种方式都给扩展装订票据：macOS 13 的 sysextd 只认扩展自身装订的票据，外层 App 的不算（macOS 27 会在线查询）。
# CFBundleVersion 每次构建递增（系统按它决定是否替换已安装的扩展）：默认取当前时间戳，可用 VELUM_BUILD 指定。
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
out="$root/desktop/src-tauri/extension"
version="${VELUM_VERSION:-0.0.0}"
build="${VELUM_BUILD:-$(date +%s)}"
team=H3H5ULV8KR
group=$team.dev.velum
: "${APPLE_SIGNING_IDENTITY:?需要 APPLE_SIGNING_IDENTITY（Developer ID Application）}"
cd "$here"

# 核心：VELUM_CORE=rust（默认）为 velum-engine 的 velum-macext 静态库（desktop/scripts/engine.sh：本机有源码就编译，
# 否则按 desktop/engine.lock 下载）；go 为 Go 的 Mobilebind.xcframework。与 sidecars.sh 的默认一致。
# build/core.xcconfig 给出头文件、链接参数与编译条件。
core="${VELUM_CORE:-rust}"
mkdir -p build
case "$core" in
  go)
    tags="$(sed -n 's/^TAGS ?= //p' "$root/core/Makefile")"
    ldflags="-s -w"
    [ -n "${VELUM_VERSION:-}" ] && ldflags="$ldflags -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
    (cd "$root/core" && PATH="$(go env GOPATH)/bin:$PATH" gomobile bind -target macos -tags "$tags" -trimpath -ldflags "$ldflags" \
      -o "$here/build/Mobilebind.xcframework" ./mobilebind)
    cat >build/core.xcconfig <<EOF
FRAMEWORK_SEARCH_PATHS = \$(PROJECT_DIR)/build/Mobilebind.xcframework/macos-arm64_x86_64
OTHER_LDFLAGS = -framework Mobilebind -lresolv
EOF
    ;;
  rust)
    sh "$root/desktop/scripts/engine.sh" macext "$here/build/macext"
    cat >build/core.xcconfig <<EOF
SWIFT_INCLUDE_PATHS = \$(PROJECT_DIR)/build/macext/include
LIBRARY_SEARCH_PATHS = \$(PROJECT_DIR)/build/macext/lib
OTHER_LDFLAGS = -lvelum_macext -lresolv -lc++ -framework Security -framework SystemConfiguration -framework CoreFoundation
SWIFT_ACTIVE_COMPILATION_CONDITIONS = \$(inherited) VELUM_RUST_CORE
EOF
    ;;
  *) echo "VELUM_CORE 只能是 go 或 rust" >&2; exit 1 ;;
esac
# 核心的英文表（Rust 核心随扩展打包、按语言加载）
sh "$root/core/scripts/core-i18n.sh" "$here/build/core-en.json" >/dev/null

xcodegen generate -q
rm -rf build/Velum.xcarchive build/export "$out"
mkdir -p "$out"
ext_name=dev.velum.desktop.tunnel.systemextension

if [ -n "${VELUM_PROFILE_HOST:-}" ]; then
  xcodebuild -project VelumExtension.xcodeproj -scheme VelumTunnel -configuration Release -derivedDataPath build/dd \
    VELUM_VERSION="$version" CURRENT_PROJECT_VERSION="$build" CODE_SIGNING_ALLOWED=NO \
    ARCHS="arm64 x86_64" ONLY_ACTIVE_ARCH=NO build >build/build.log 2>&1 \
    || { grep -E "error:" build/build.log >&2; exit 1; }
  ext="$out/$ext_name"
  ditto "build/dd/Build/Products/Release/$ext_name" "$ext"
  cp "$VELUM_PROFILE_TUNNEL" "$ext/Contents/embedded.provisionprofile"
  cat >build/Tunnel.entitlements <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>com.apple.application-identifier</key><string>$team.dev.velum.desktop.tunnel</string>
<key>com.apple.developer.team-identifier</key><string>$team</string>
<key>com.apple.developer.networking.networkextension</key><array><string>packet-tunnel-provider-systemextension</string></array>
<key>com.apple.security.app-sandbox</key><true/>
<key>com.apple.security.application-groups</key><array><string>$group</string></array>
<key>com.apple.security.network.client</key><true/>
<key>com.apple.security.network.server</key><true/>
</dict></plist>
EOF
  codesign --force --options runtime --timestamp --entitlements build/Tunnel.entitlements -s "$APPLE_SIGNING_IDENTITY" "$ext"
  # 扩展要单独公证并装订票据：Tauri 只装订外层 App，而 macOS 13 的 sysextd 只认扩展自身装订的票据（不在线查询，-67050）
  # 公证凭据：API 密钥（CI），或本机钥匙串里 notarytool store-credentials 存的配置（VELUM_NOTARY_PROFILE）
  if [ -n "${APPLE_API_KEY_PATH:-}" ]; then
    set -- --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY" --issuer "$APPLE_API_ISSUER"
  elif [ -n "${VELUM_NOTARY_PROFILE:-}" ]; then
    set -- --keychain-profile "$VELUM_NOTARY_PROFILE"
  else
    set --
  fi
  if [ $# -gt 0 ]; then
    ditto -c -k --keepParent "$ext" build/extension.zip
    xcrun notarytool submit build/extension.zip "$@" --wait --timeout 30m >build/notarize.log 2>&1 || { tail -5 build/notarize.log >&2; exit 1; }
    grep -q "status: Accepted" build/notarize.log || { tail -8 build/notarize.log >&2; exit 1; }
    xcrun stapler staple "$ext" >/dev/null
  fi
  cp "$VELUM_PROFILE_HOST" "$out/embedded.provisionprofile"
  cat >"$out/Velum.entitlements" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>com.apple.application-identifier</key><string>$team.dev.velum.desktop</string>
<key>com.apple.developer.team-identifier</key><string>$team</string>
<key>com.apple.developer.networking.networkextension</key><array><string>packet-tunnel-provider-systemextension</string></array>
<key>com.apple.developer.system-extension.install</key><true/>
<key>com.apple.security.personal-information.location</key><true/>
<key>com.apple.security.application-groups</key><array><string>$group</string></array>
</dict></plist>
EOF
else
  xcodebuild -project VelumExtension.xcodeproj -scheme VelumHost -configuration Release -derivedDataPath build/dd \
    -archivePath build/Velum.xcarchive -allowProvisioningUpdates VELUM_VERSION="$version" CURRENT_PROJECT_VERSION="$build" \
    archive >build/archive.log 2>&1 || { grep -E "error:" build/archive.log >&2; exit 1; }
  cat >build/export.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>method</key><string>developer-id</string>
<key>teamID</key><string>$team</string>
<key>signingStyle</key><string>automatic</string>
<key>destination</key><string>upload</string>
</dict></plist>
EOF
  xcodebuild -exportArchive -archivePath build/Velum.xcarchive -exportOptionsPlist build/export.plist -exportPath build/export \
    -allowProvisioningUpdates >build/export.log 2>&1 || { grep -E "error:" build/export.log >&2; exit 1; }
  t0=$(date +%s)
  until xcodebuild -exportNotarizedApp -archivePath build/Velum.xcarchive -exportPath build/export >build/notarize.log 2>&1; do
    [ $(( $(date +%s) - t0 )) -gt 1800 ] && { tail -5 build/notarize.log >&2; exit 1; }
    sleep 15
  done
  echo "extension: 公证完成，用时 $(( $(date +%s) - t0 ))s"
  app="build/export/Velum.app"
  ditto "$app/Contents/Library/SystemExtensions/$ext_name" "$out/$ext_name"
  xcrun stapler staple "$out/$ext_name" >/dev/null # 见上：macOS 13 只认扩展自身装订的票据
  cp "$app/Contents/embedded.provisionprofile" "$out/embedded.provisionprofile"
  codesign -d --entitlements - --xml "$app" 2>/dev/null | plutil -convert xml1 -o "$out/Velum.entitlements" -
fi

# sidecar：与 Tauri 打包时相同的构建（velum-helper 只接受同一团队签名的 velumd），以 hardened runtime 签名、不带 entitlements
# 目标架构与 tauri build --target 一致（TAURI_ENV_TARGET_TRIPLE，如 x86_64-apple-darwin），默认本机；扩展本身总是 arm64 + x86_64
triple="${TAURI_ENV_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
# sidecars.sh 按同一个 VELUM_CORE 取 velumd、vlu（Rust 或 Go），velum-helper 是 Go 的
TAURI_ENV_TARGET_TRIPLE=$triple VELUM_TEAM_ID=$team VELUM_CORE=$core sh "$root/desktop/scripts/sidecars.sh" >/dev/null
mkdir -p "$out/bin"
for b in velumd velum-helper vlu; do
  cp "$root/desktop/src-tauri/binaries/$b-$triple" "$out/bin/$b"
done
for b in velumd velum-helper vlu; do
  codesign --force --options runtime --timestamp -s "$APPLE_SIGNING_IDENTITY" "$out/bin/$b"
done
echo "extension: $version ($build) → $out"
