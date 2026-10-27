#!/bin/sh
# 构建 Go 核心并放到 Tauri 要求的位置：src-tauri/binaries/<名字>-<目标三元组>[.exe]
# 按目标三元组交叉编译（TAURI_ENV_TARGET_TRIPLE 由 tauri build --target 提供）；Windows 没有 velum-helper。
set -e
root="$(cd "$(dirname "$0")/../.." && pwd)"
triple="${TAURI_ENV_TARGET_TRIPLE:-$(rustc -vV | sed -n 's/^host: //p')}"
case "$triple" in
  *-apple-darwin) goos=darwin; ext=""; bins="velumd velum-helper vlu" ;;
  *-windows-*) goos=windows; ext=".exe"; bins="velumd vlu" ;;
  *) echo "sidecars: 不支持的目标 $triple" >&2; exit 1 ;;
esac
case "$triple" in
  x86_64-*) goarch=amd64 ;;
  aarch64-*) goarch=arm64 ;;
  *) echo "sidecars: 不支持的架构 $triple" >&2; exit 1 ;;
esac
out="$root/core/bin/$goos-$goarch"
# 与 core/Makefile 的 release 相同（不依赖 make：Windows 开发机通常只有 Git Bash）
tags="$(sed -n 's/^TAGS ?= //p' "$root/core/Makefile")"
# 发布构建由 CI 传入 VELUM_VERSION（如 0.1.0-rc.1），写入 velumd / vlu 的版本号
ldflags="-s -w"
[ -n "$VELUM_VERSION" ] && ldflags="$ldflags -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
# 以开发者证书签名的构建传入 VELUM_TEAM_ID：velum-helper 只接受该团队签名的 velumd 连接。
# 必须与签名身份同时提供，否则 helper 会拒绝未签名的 velumd，TUN 无法使用。
if [ -n "$VELUM_TEAM_ID" ]; then
  [ -n "$APPLE_SIGNING_IDENTITY" ] || { echo "sidecars: 设置了 VELUM_TEAM_ID，但没有 APPLE_SIGNING_IDENTITY（velumd 不会被签名）" >&2; exit 1; }
  ldflags="$ldflags -X github.com/Foreverwzh/velum/core/pkg/helper.TeamID=$VELUM_TEAM_ID"
fi
(cd "$root/core" && GOOS=$goos GOARCH=$goarch go build -tags "$tags" -trimpath -ldflags "$ldflags" -o "$out/" ./cmd/...)
mkdir -p "$root/desktop/src-tauri/binaries"
for b in $bins; do
  cp "$out/$b$ext" "$root/desktop/src-tauri/binaries/$b-$triple$ext"
done
echo "sidecars: $triple"
