#!/bin/sh
# 编译 velum-engine 的 xcframework（闭源，源码不在本仓库）并放到 build/VelumEngine.xcframework。
# 需要本机有 velum-engine 的源码：VELUM_ENGINE_DIR，默认与本仓库同级的 velum-engine 目录。
set -eu
cd "$(dirname "$0")/.."
src="${VELUM_ENGINE_DIR:-$(cd ../../.. && pwd)/velum-engine}"
[ -x "$src/scripts/build-xcframework.sh" ] || { echo "找不到 velum-engine：$src（设 VELUM_ENGINE_DIR）" >&2; exit 1; }
"$src/scripts/build-xcframework.sh"
mkdir -p build
rm -rf build/VelumEngine.xcframework
cp -R "$src/ios/build/VelumEngine.xcframework" build/
du -sh build/VelumEngine.xcframework
