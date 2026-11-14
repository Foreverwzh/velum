#!/bin/sh
# 准备核心并放到 Tauri 要求的位置：src-tauri/binaries/<名字>-<目标三元组>[.exe]
# macOS 用 Rust 的（velum-engine），Windows 用 Go 的。
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
mkdir -p "$root/desktop/src-tauri/binaries"
if [ "$goos" = darwin ]; then
  # Rust 的 velumd、vlu、velum-helper（velum-engine，见 engine.sh）。
  # 签名构建：helper 只接受该团队签名的 velumd（从源码编译时读 VELUM_TEAM_ID；下载的是发布时编进去的）
  if [ -n "$VELUM_TEAM_ID" ] && [ -z "$APPLE_SIGNING_IDENTITY" ]; then
    echo "sidecars: 设置了 VELUM_TEAM_ID，但没有 APPLE_SIGNING_IDENTITY（velumd 不会被签名）" >&2; exit 1
  fi
  rs="$root/desktop/src-tauri/binaries/rust-$triple"
  rm -rf "$rs"
  sh "$root/desktop/scripts/engine.sh" core "$triple" "$rs"
  for b in $bins; do
    cp "$rs/$b" "$root/desktop/src-tauri/binaries/$b-$triple$ext"
  done
else
  # Windows 还是 Go 的 velumd、vlu。与 core/Makefile 的 release 相同（不依赖 make：Windows 开发机通常只有 Git Bash）
  out="$root/core/bin/$goos-$goarch"
  tags="$(sed -n 's/^TAGS ?= //p' "$root/core/Makefile")"
  # 发布构建由 CI 传入 VELUM_VERSION（如 0.1.0-rc.1），写入 velumd / vlu 的版本号
  ldflags="-s -w"
  [ -n "$VELUM_VERSION" ] && ldflags="$ldflags -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
  (cd "$root/core" && GOOS=$goos GOARCH=$goarch go build -tags "$tags" -trimpath -ldflags "$ldflags" -o "$out/" ./cmd/...)
  for b in $bins; do
    cp "$out/$b$ext" "$root/desktop/src-tauri/binaries/$b-$triple$ext"
  done
fi
res="$root/desktop/src-tauri/core-res"
mkdir -p "$res"
# macOS App 包的 Resources：核心报告的版本（与 App 相同，App 据此判断后台核心是否要重启）与核心的英文表
version="${VELUM_VERSION:-$(sed -n 's/^  "version": "\(.*\)",$/\1/p' "$root/desktop/src-tauri/tauri.conf.json")}"
if [ "$goos" = darwin ]; then
  printf '%s\n' "$version" > "$res/VERSION"
  sh "$root/core/scripts/core-i18n.sh" "$res/core-en.json" >/dev/null
fi
echo "sidecars: ${triple}（版本 ${version}）"
