#!/bin/sh
# 准备 Android 的核心：velum-engine（闭源，不在本仓库）的 libvelum_android.so（App 进程里运行的 velumd，JNI 接口见
# velum-engine 的 crates/velum-android）与 NDK 的 libc++_shared.so 放到 android/app/src/main/jniLibs/<abi>/，
# 核心文字的英文表放到 android/app/src/main/assets/core-en.json。
# 来源：
# - 本机有 velum-engine 源码（VELUM_ENGINE_DIR，默认与本仓库同级的 velum-engine）：从源码编译，
#   需要 Rust（带 Android 目标）、Android SDK + NDK（ANDROID_HOME / ANDROID_NDK_HOME）；
# - 否则（CI），或设了 VELUM_ENGINE_DOWNLOAD=1：按 android/engine.lock 从私有仓库的 GitHub Release 下载并校验 sha256，
#   需要 gh 与能读那个仓库的令牌（GH_TOKEN）。
# 英文表由 Go 导出（core/scripts/core-i18n.sh）。
#   ABIS  从源码编译时的 ABI，默认 "arm64-v8a armeabi-v7a x86_64"（32 / 64 位 ARM 真机、x86_64 模拟器）
set -eu
cd "$(dirname "$0")/.."
src="${VELUM_ENGINE_DIR:-$(cd ../.. && pwd)/velum-engine}"
libs=android/app/src/main/jniLibs
rm -rf "$libs"
mkdir -p "$libs" android/app/src/main/assets
if [ -z "${VELUM_ENGINE_DOWNLOAD:-}" ] && [ -x "$src/scripts/build-android.sh" ]; then
  abis="${ABIS:-arm64-v8a armeabi-v7a x86_64}"
  ABIS="$abis" "$src/scripts/build-android.sh"
  for abi in $abis; do
    cp -R "$src/android/build/jniLibs/$abi" "$libs/"
  done
  echo "velum-engine：从源码编译（${src}）"
else
  lock() { sed -n "s/^$1=//p" android/engine.lock; }
  repo=$(lock repo) v=$(lock version) sum=$(lock sha256)
  [ -n "$repo" ] && [ -n "$v" ] && [ -n "$sum" ] || { echo "android/engine.lock 不完整" >&2; exit 1; }
  dl=build/engine-download
  rm -rf "$dl"
  mkdir -p "$dl"
  gh release download "android-v$v" --repo "$repo" --dir "$dl" --pattern "VelumAndroid-$v.zip"
  [ "$(shasum -a 256 "$dl/VelumAndroid-$v.zip" | cut -d' ' -f1)" = "$sum" ] ||
    { echo "VelumAndroid-$v.zip 的 sha256 与 engine.lock 不符" >&2; exit 1; }
  unzip -q "$dl/VelumAndroid-$v.zip" -d "$dl"
  cp -R "$dl"/jniLibs/* "$libs/"
  rm -rf "$dl"
  echo "velum-engine：下载 android-v${v}（已校验 sha256）"
fi
sh ../core/scripts/core-i18n.sh "$(pwd)/android/app/src/main/assets/core-en.json"
ls -l "$libs"/*/*.so android/app/src/main/assets/core-en.json
