#!/bin/sh
# 上架构建：不含 Go（没有 sing-box，也没有 Go 核心）。App 只用 Rust 控制面（VelumCore.xcframework 里的 velum-core），
# 隧道扩展只用 velum-engine。由 project.yml 生成 VelumStore.xcodeproj：去掉 Mobilebind、Iostunnel，Swift 加编译条件 VELUM_ONLY。
#   sh scripts/velumonly.sh     先运行 scripts/engine.sh
set -eu
cd "$(dirname "$0")/.."
for f in build/VelumEngine.xcframework build/VelumCore.xcframework build/VelumCoreSwift build/core-en.json; do
  [ -e "$f" ] || { echo "缺少 $f：先运行 scripts/engine.sh" >&2; exit 1; }
done
python3 - <<'PY'
s = open("project.yml").read()
def rep(old, new):
    global s
    assert s.count(old) == 1, f"project.yml 里找不到唯一的：{old!r}"
    s = s.replace(old, new)
rep("      - framework: build/Iostunnel.xcframework\n        embed: false\n", "")
rep("      - framework: build/Mobilebind.xcframework\n        embed: false\n", "")
cond = "      SWIFT_ACTIVE_COMPILATION_CONDITIONS: $(inherited) VELUM_ONLY\n"
rep("      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum\n", "      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum\n" + cond)
rep("      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum.tunnel\n", "      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum.tunnel\n" + cond)
rep("name: Velum\n", "name: VelumStore\n")
assert "Iostunnel" not in s and "Mobilebind" not in s
open("project-velumonly.yml", "w").write(s)
PY
# 工程放在本目录（路径都相对这里），名字与默认工程区分
xcodegen --spec project-velumonly.yml >/dev/null
rm project-velumonly.yml
echo "已生成 VelumStore.xcodeproj（不含 Go；scheme 仍是 Velum）"
