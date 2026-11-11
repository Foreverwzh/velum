#!/bin/sh
# 打 Linux 命令行包（Rust 核心）：velumd、vlu，网页界面（web/），核心的英文表与版本号，许可证、安装脚本、第三方声明。
#   sh core/scripts/linux-package.sh <目标三元组> <包目录>
# 目标三元组如 x86_64-unknown-linux-gnu、aarch64-unknown-linux-gnu；VELUM_VERSION 为空时版本是 dev。
# velumd、vlu 来自 velum-engine（desktop/scripts/engine.sh：本机有源码就编译，否则按 desktop/engine.lock 下载）。
# 网页界面要先构建（desktop/dist，bun run build）。
set -eu
triple="${1:?用法：$0 <目标三元组> <包目录>}"
dir="${2:?用法：$0 <目标三元组> <包目录>}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
[ -f "$root/desktop/dist/index.html" ] || { echo "linux-package: 先构建网页界面（cd desktop && bun run build）" >&2; exit 1; }
mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"
sh "$root/desktop/scripts/engine.sh" core "$triple" "$dir"
sh "$root/desktop/scripts/engine.sh" notices "$dir/THIRD_PARTY_RUST.txt"
rm -rf "$dir/web"
cp -R "$root/desktop/dist" "$dir/web"
sh "$root/core/scripts/core-i18n.sh" "$dir/core-en.json" >/dev/null
printf '%s\n' "${VELUM_VERSION:-dev}" > "$dir/VERSION"
cp "$root/LICENSE" "$root/NOTICE.md" "$root/core/scripts/install.sh" "$dir/"
echo "linux-package: ${dir}（${triple}，版本 ${VELUM_VERSION:-dev}）"
