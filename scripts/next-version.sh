#!/bin/sh
# 打印下一个发布版本号：年.月日.当天序号（北京时间），如 2026.1115.1；当天再发一次就是 2026.1115.2。
# 版本号要能按语义化版本解析（Tauri 的自动更新、Cargo、npm），数字段不能有前导 0：1～9 月写成 2026.105.1（1 月 5 日），
# 按数值比较大小顺序不变。带 - 后缀（如 2026.1115.1-beta）的标签发布为预发布版本，只推给「测试版」通道。
#   sh scripts/next-version.sh        → 2026.1115.1
#   git tag "v$(sh scripts/next-version.sh)" && git push origin --tags
set -eu
cd "$(dirname "$0")/.."
git fetch -q --tags origin 2>/dev/null || true
day="$(TZ=Asia/Shanghai date +%Y).$(TZ=Asia/Shanghai date +%m%d | sed 's/^0//')"
n=$(git tag -l "v$day.*" | sed -E "s/^v$day\.([0-9]+).*/\1/" | sort -n | tail -1)
echo "$day.$(( ${n:-0} + 1 ))"
