#!/bin/sh
# 准备 build/VelumEngine.xcframework（velum-engine，闭源，不在本仓库）与它的第三方声明 build/THIRD_PARTY_RUST.txt：
# - 本机有 velum-engine 源码（VELUM_ENGINE_DIR，默认与本仓库同级的 velum-engine）：从源码编译；
# - 否则（CI），或设了 VELUM_ENGINE_DOWNLOAD=1：按 engine.lock 从私有仓库的 GitHub Release 下载并校验 sha256，
#   需要 gh 与能读那个仓库的令牌（GH_TOKEN）。
set -eu
cd "$(dirname "$0")/.."
src="${VELUM_ENGINE_DIR:-$(cd ../../.. && pwd)/velum-engine}"
mkdir -p build
if [ -z "${VELUM_ENGINE_DOWNLOAD:-}" ] && [ -x "$src/scripts/build-xcframework.sh" ]; then
  "$src/scripts/build-xcframework.sh"
  rm -rf build/VelumEngine.xcframework
  cp -R "$src/ios/build/VelumEngine.xcframework" build/
  (cd "$src" && python3 scripts/notices.py "$OLDPWD/build/THIRD_PARTY_RUST.txt" >/dev/null)
  echo "velum-engine：从源码编译（$src）"
else
  lock() { sed -n "s/^$1=//p" engine.lock; }
  repo=$(lock repo) v=$(lock version) sum=$(lock sha256) nsum=$(lock notices_sha256)
  [ -n "$repo" ] && [ -n "$v" ] && [ -n "$sum" ] && [ -n "$nsum" ] || { echo "engine.lock 不完整" >&2; exit 1; }
  dl=build/engine-download
  rm -rf "$dl"
  mkdir -p "$dl"
  gh release download "ios-v$v" --repo "$repo" --dir "$dl" \
    --pattern "VelumEngine-$v.xcframework.zip" --pattern THIRD_PARTY_RUST.txt
  check() { [ "$(shasum -a 256 "$1" | cut -d' ' -f1)" = "$2" ] || { echo "$1 的 sha256 与 engine.lock 不符" >&2; exit 1; }; }
  check "$dl/VelumEngine-$v.xcframework.zip" "$sum"
  check "$dl/THIRD_PARTY_RUST.txt" "$nsum"
  rm -rf build/VelumEngine.xcframework
  ditto -x -k "$dl/VelumEngine-$v.xcframework.zip" build/
  cp "$dl/THIRD_PARTY_RUST.txt" build/THIRD_PARTY_RUST.txt
  rm -rf "$dl"
  echo "velum-engine：下载 ios-v${v}（已校验 sha256）"
fi
du -sh build/VelumEngine.xcframework
