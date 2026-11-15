# core

核心本身（velumd、vlu、velum-helper、引擎）在独立的 velum-engine 仓库（Rust），各端按 `engine.lock` 取预编译产物。
这里只剩各端共用的东西：

- `i18n/core-en.json`：核心返回的文字的英文表（中文原文 → 英文）；`scripts/core-i18n.sh` 生成安装包里的 core-en.json，
  有 velum-engine 源码时检查它用到的文字是否都有译文。
- `scripts/linux-package.sh`、`install.sh`、`smoke.sh`、`service-smoke.sh`、`netns-tun.sh`：Linux 命令行包的打包、安装与 CI 冒烟测试。
- `scripts/verify-release.mjs`：发布前校验 SHA256SUMS 的签名（release.yml）。

Go 核心（含 sing-box）已于 2026-11 删除，历史见 git log。
