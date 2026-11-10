#!/bin/sh
# 生成 App 附带的第三方版权声明 build/ThirdPartyNotices.txt。上架构建不含 Go，链接的第三方代码都在 velum-engine 的
# Rust crate 里（App 的 VelumCore 与隧道扩展的 VelumEngine）：scripts/engine.sh 准备的 build/THIRD_PARTY_RUST.txt。
set -eu
cd "$(dirname "$0")/.."
[ -f build/THIRD_PARTY_RUST.txt ] || { echo "缺少 build/THIRD_PARTY_RUST.txt：先运行 scripts/engine.sh" >&2; exit 1; }
{ printf 'Velum 使用的第三方组件\n\n'; cat build/THIRD_PARTY_RUST.txt; } > build/ThirdPartyNotices.txt
echo "build/ThirdPartyNotices.txt：$(du -h build/ThirdPartyNotices.txt | cut -f1)"
