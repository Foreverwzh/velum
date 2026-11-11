#!/bin/sh
# 生成 build/core-en.json（Rust 核心返回的文字的英文表），见 core/scripts/core-i18n.sh。
set -eu
cd "$(dirname "$0")/.."
sh ../../core/scripts/core-i18n.sh "$(pwd)/build/core-en.json"
