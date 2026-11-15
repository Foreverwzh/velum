#!/bin/sh
# 桌面端与 Linux 用的 Rust 核心（velum-engine，闭源，不在本仓库）：
#   sh desktop/scripts/engine.sh core <目标三元组> <输出目录>   velumd、vlu，macOS 另有 velum-helper（如 x86_64-apple-darwin），
#                                                             Windows 是 velumd.exe、vlu.exe 与 wintun.dll、wintun-LICENSE.txt
#   sh desktop/scripts/engine.sh macext <输出目录>             macOS 系统扩展的核心：lib/libvelum_macext.a、include/（arm64 + x86_64）
#   sh desktop/scripts/engine.sh notices <文件>                velum-engine 的第三方声明
#   sh desktop/scripts/engine.sh wire [--check]                 重新生成 desktop/src/wire.ts（本地接口的数据类型，只能从源码）
# 来源：
# - 本机有 velum-engine 源码（VELUM_ENGINE_DIR，默认与本仓库同级的 velum-engine）：从源码编译；
# - 否则（CI），或设了 VELUM_ENGINE_DOWNLOAD=1：按 desktop/engine.lock 从私有仓库的 GitHub Release（desktop-v<版本>）
#   下载并校验 sha256，需要 gh 与能读那个仓库的令牌（GH_TOKEN）。
set -eu
here="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$here/.." && pwd)"
src="${VELUM_ENGINE_DIR:-$(cd "$root/.." && pwd)/velum-engine}"
what="${1:?用法：$0 core <三元组> <目录> | macext <目录> | notices <文件> | wire [--check]}"

from_source() { [ -z "${VELUM_ENGINE_DOWNLOAD:-}" ] && [ -f "$src/crates/velum-macext/Cargo.toml" ]; }

# sha256（Windows 的 Git Bash 没有 shasum，有 sha256sum；macOS 反过来）
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

# 下载并校验一个 Release 附件，打印本地路径
fetch() { # 附件名 engine.lock 里的键
  lock() { sed -n "s/^$1=//p" "$here/engine.lock"; }
  repo=$(lock repo) v=$(lock version) sum=$(lock "$2")
  [ -n "$repo" ] && [ -n "$v" ] && [ -n "$sum" ] || { echo "engine.lock 缺少 repo / version / $2" >&2; exit 1; }
  dl="$here/build/engine-download/$v"
  mkdir -p "$dl"
  name=$(echo "$1" | sed "s/{v}/$v/")
  if [ ! -f "$dl/$name" ]; then
    gh release download "desktop-v$v" --repo "$repo" --dir "$dl" --pattern "$name" >&2
  fi
  [ "$(sha256 "$dl/$name")" = "$sum" ] ||
    { echo "$name 的 sha256 与 engine.lock 不符" >&2; rm -f "$dl/$name"; exit 1; }
  echo "$dl/$name"
}

case "$what" in
  core)
    triple="${2:?缺少目标三元组}" out="${3:?缺少输出目录}"
    mkdir -p "$out"
    if from_source; then
      case "$triple" in
        *-linux-*) (cd "$src" && cargo zigbuild -q --release --target "$triple.2.31" -p velum-daemon --bins 2>/dev/null) ;;
        *-apple-darwin) (cd "$src" && MACOSX_DEPLOYMENT_TARGET=12.0 cargo build -q --release --target "$triple" -p velum-daemon --bins &&
          MACOSX_DEPLOYMENT_TARGET=12.0 cargo build -q --release --target "$triple" -p velum-helper) ;;
        # 在 macOS 上交叉编译（cargo-xwin，工具见 velum-engine 的 scripts/release-desktop.sh）；在 Windows 上直接编译
        *-windows-msvc) if [ "$(uname -s)" = Darwin ]; then
            (cd "$src" && PATH="${VELUM_XWIN_PATH:-$HOME/.local/share/xwin-shims}:$PATH" cargo xwin build -q --release --target "$triple" -p velum-daemon --bins)
          else
            (cd "$src" && cargo build -q --release --target "$triple" -p velum-daemon --bins)
          fi ;;
        *) echo "engine: 还不支持 $triple" >&2; exit 1 ;;
      esac
      case "$triple" in
        *-windows-*)
          cp "$src/target/$triple/release/velumd.exe" "$src/target/$triple/release/vlu.exe" "$src/third_party/wintun/wintun.dll" "$out/"
          cp "$src/third_party/wintun/LICENSE.txt" "$out/wintun-LICENSE.txt"
          ;;
        *) cp "$src/target/$triple/release/velumd" "$src/target/$triple/release/vlu" "$out/" ;;
      esac
      case "$triple" in *-apple-darwin) cp "$src/target/$triple/release/velum-helper" "$out/" ;; esac
      echo "engine: velumd、vlu（${triple}，从源码编译）" >&2
    else
      key=$(echo "velum-core-$triple" | sed -E 's/[^A-Za-z0-9]+/_/g')
      f=$(fetch "velum-core-{v}-$triple.tar.gz" "$key")
      tmp="$here/build/engine-download/x"
      rm -rf "$tmp" && mkdir -p "$tmp"
      tar -xzf "$f" -C "$tmp"
      case "$triple" in
        *-windows-*) cp "$tmp"/*/velumd.exe "$tmp"/*/vlu.exe "$tmp"/*/wintun.dll "$tmp"/*/wintun-LICENSE.txt "$out/" ;;
        *) cp "$tmp"/*/velumd "$tmp"/*/vlu "$out/" ;;
      esac
      # macOS 的发布包里有 velum-helper（desktop-v0.2.0 起）
      if [ -f "$tmp"/*/velum-helper ]; then cp "$tmp"/*/velum-helper "$out/"; fi
      rm -rf "$tmp"
      echo "engine: velumd、vlu（${triple}，下载，已校验 sha256）" >&2
    fi
    ;;
  macext)
    out="${2:?缺少输出目录}"
    rm -rf "$out"
    if from_source; then
      sh "$src/scripts/build-macext.sh" "$out" >/dev/null
      echo "engine: velum-macext（从源码编译）" >&2
    else
      f=$(fetch "VelumMacExt-{v}.tar.gz" VelumMacExt)
      mkdir -p "$out"
      tar -xzf "$f" -C "$out" --strip-components 1
      echo "engine: velum-macext（下载，已校验 sha256）" >&2
    fi
    ;;
  notices)
    out="${2:?缺少文件}"
    if from_source; then
      (cd "$src" && python3 scripts/notices.py "$out" >/dev/null)
    else
      cp "$(fetch THIRD_PARTY_RUST.txt THIRD_PARTY_RUST_txt)" "$out"
    fi
    ;;
  wire)
    [ -f "$src/crates/velum-tools/Cargo.toml" ] || { echo "engine: wire 需要 velum-engine 源码（VELUM_ENGINE_DIR）" >&2; exit 1; }
    (cd "$src" && cargo run -q -p velum-tools -- wire-ts "$here/src/wire.ts" ${2:-})
    ;;
  *) echo "用法：$0 core <三元组> <目录> | macext <目录> | notices <文件> | wire [--check]" >&2; exit 1 ;;
esac
