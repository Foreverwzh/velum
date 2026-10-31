#!/bin/sh
# 构建并以 Developer ID 签名系统扩展，产物放到 desktop/src-tauri/extension/，由 tauri.ne.conf.json 打进 Velum.app：
#   dev.velum.desktop.tunnel.systemextension   签好名的扩展（含其描述文件）
#   embedded.provisionprofile                   Velum.app 的描述文件（扩展安装、packet tunnel、App Group 是受限能力）
#   Velum.entitlements                          Velum.app 的 entitlements，取自导出的占位宿主，与描述文件一致
#   bin/                                        预先签名的 velumd / velum-helper / vlu（不带 entitlements）
# Tauri 把同一份 entitlements 签进所有 externalBin，带受限 entitlements 的 sidecar 一启动就被系统杀掉（只有主程序认 App 的描述文件），
# 所以签名构建不用 externalBin，改由这里签好 sidecar、经 bundle.macOS.files 放进 Contents/MacOS（Tauri 不重签这些文件）。
# 需要 APPLE_SIGNING_IDENTITY（Developer ID Application 证书）。
# 签名：本机用 Xcode 登录的账号自动管理描述文件；CI 设置 VELUM_PROFILE_HOST / VELUM_PROFILE_TUNNEL（描述文件路径）时改为手动签名。
# CFBundleVersion 每次构建递增（系统按它决定是否替换已安装的扩展）：默认取当前时间戳，可用 VELUM_BUILD 指定。
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
out="$root/desktop/src-tauri/extension"
version="${VELUM_VERSION:-0.0.0}"
build="${VELUM_BUILD:-$(date +%s)}"
team=H3H5ULV8KR
: "${APPLE_SIGNING_IDENTITY:?需要 APPLE_SIGNING_IDENTITY（Developer ID Application）}"
cd "$here"

# Go 核心 → Mobilebind.xcframework（与 sidecars.sh 相同的构建标签与版本号）
tags="$(sed -n 's/^TAGS ?= //p' "$root/core/Makefile")"
ldflags="-s -w"
[ -n "${VELUM_VERSION:-}" ] && ldflags="$ldflags -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
(cd "$root/core" && PATH="$(go env GOPATH)/bin:$PATH" gomobile bind -target macos -tags "$tags" -trimpath -ldflags "$ldflags" \
  -o "$here/build/Mobilebind.xcframework" ./mobilebind)

xcodegen generate -q
rm -rf build/Velum.xcarchive build/export
sign=""
if [ -n "${VELUM_PROFILE_HOST:-}" ]; then
  sign="CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY=Developer\ ID\ Application"
fi
eval xcodebuild -project VelumExtension.xcodeproj -scheme VelumHost -configuration Release -derivedDataPath build/dd \
  -archivePath build/Velum.xcarchive -allowProvisioningUpdates \
  VELUM_VERSION="$version" CURRENT_PROJECT_VERSION="$build" $sign archive >build/archive.log 2>&1 \
  || { grep -E "error:" build/archive.log >&2; exit 1; }

# 导出：Developer ID 签名。CI 由 Tauri 公证整个 Velum.app；本机没有公证用的 API 密钥，改为经 Xcode 账号把占位宿主（含扩展）提交公证：
# 系统安装扩展时按代码哈希在线查询公证结果（未公证会被 sysextd 拒绝，-67050），扩展的哈希公证过即可，外层 App 不必公证。
if [ -n "${VELUM_PROFILE_HOST:-}" ]; then
  host_uuid=$(security cms -D -i "$VELUM_PROFILE_HOST" | plutil -extract UUID raw -)
  tunnel_uuid=$(security cms -D -i "$VELUM_PROFILE_TUNNEL" | plutil -extract UUID raw -)
  mkdir -p "$HOME/Library/MobileDevice/Provisioning Profiles"
  cp "$VELUM_PROFILE_HOST" "$HOME/Library/MobileDevice/Provisioning Profiles/$host_uuid.provisionprofile"
  cp "$VELUM_PROFILE_TUNNEL" "$HOME/Library/MobileDevice/Provisioning Profiles/$tunnel_uuid.provisionprofile"
  profiles="<key>provisioningProfiles</key><dict><key>dev.velum.desktop</key><string>$host_uuid</string><key>dev.velum.desktop.tunnel</key><string>$tunnel_uuid</string></dict>"
  style=manual
else
  profiles="<key>destination</key><string>upload</string>"
  style=automatic
fi
cat >build/export.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>method</key><string>developer-id</string>
<key>teamID</key><string>$team</string>
<key>signingStyle</key><string>$style</string>
$profiles
</dict></plist>
EOF
xcodebuild -exportArchive -archivePath build/Velum.xcarchive -exportOptionsPlist build/export.plist -exportPath build/export \
  -allowProvisioningUpdates >build/export.log 2>&1 || { grep -E "error:" build/export.log >&2; exit 1; }
if [ -z "${VELUM_PROFILE_HOST:-}" ]; then
  t0=$(date +%s)
  until xcodebuild -exportNotarizedApp -archivePath build/Velum.xcarchive -exportPath build/export >build/notarize.log 2>&1; do
    [ $(( $(date +%s) - t0 )) -gt 1800 ] && { tail -5 build/notarize.log >&2; exit 1; }
    sleep 15
  done
  echo "extension: 公证完成，用时 $(( $(date +%s) - t0 ))s"
fi

app="build/export/Velum.app"
ext="$app/Contents/Library/SystemExtensions/dev.velum.desktop.tunnel.systemextension"
rm -rf "$out" && mkdir -p "$out"
ditto "$ext" "$out/dev.velum.desktop.tunnel.systemextension"
cp "$app/Contents/embedded.provisionprofile" "$out/embedded.provisionprofile"
codesign -d --entitlements - --xml "$app" 2>/dev/null | plutil -convert xml1 -o "$out/Velum.entitlements" -

# sidecar：与 Tauri 打包时相同的构建（velum-helper 只接受同一团队签名的 velumd），以 hardened runtime 签名、不带 entitlements
triple="$(rustc -vV | sed -n 's/^host: //p')"
VELUM_TEAM_ID=$team sh "$root/desktop/scripts/sidecars.sh" >/dev/null
mkdir -p "$out/bin"
for b in velumd velum-helper vlu; do
  cp "$root/desktop/src-tauri/binaries/$b-$triple" "$out/bin/$b"
  codesign --force --options runtime --timestamp -s "$APPLE_SIGNING_IDENTITY" "$out/bin/$b"
done
echo "extension: $version ($build) → $out"
