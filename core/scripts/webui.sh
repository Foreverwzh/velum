#!/bin/sh
# 构建桌面端界面并复制到 core/pkg/webui/dist，供 -tags webui 内嵌。
set -eu
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root/desktop"
[ -d node_modules ] || bun install --frozen-lockfile
bun run build
rm -rf "$root/core/pkg/webui/dist"
cp -R dist "$root/core/pkg/webui/dist"
