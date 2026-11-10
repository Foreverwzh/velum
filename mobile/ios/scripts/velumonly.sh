#!/bin/sh
# 上架构建：不含 sing-box。Go 核心用 velumonly 标签（App 内本地内核也是 velum-engine），不编译 Iostunnel；
# 由 project.yml 生成 VelumStore.xcodeproj：去掉 Iostunnel，Swift 加编译条件 VELUM_ONLY。
#   sh scripts/velumonly.sh     先运行 scripts/engine.sh
set -eu
cd "$(dirname "$0")/.."
ios=$(pwd)
out=build/velumonly
mkdir -p "$out"
[ -d build/VelumEngine.xcframework ] && [ -d build/VelumCore.xcframework ] || { echo "缺少 build/VelumEngine.xcframework：先运行 scripts/engine.sh" >&2; exit 1; }
cd ../../core
TAGS="$(sed -n 's/^TAGS ?= //p' Makefile),velumonly"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export PATH="$PATH:$(go env GOPATH)/bin"
LDFLAGS="-s -w"
[ -n "${VELUM_VERSION:-}" ] && LDFLAGS="$LDFLAGS -X github.com/Foreverwzh/velum/core/pkg/daemon.Version=$VELUM_VERSION"
n=$(go list -tags "$TAGS" -deps ./mobilebind | grep -c sagernet || true)
[ "$n" = 0 ] || { echo "velumonly 的 Mobilebind 仍依赖 $n 个 sagernet 包" >&2; exit 1; }
gomobile bind -target "${TARGETS:-ios/arm64,iossimulator/arm64}" -iosversion 16.0 -tags "$TAGS" -trimpath -ldflags "$LDFLAGS" \
  -o "$ios/$out/Mobilebind.xcframework" ./mobilebind
cd "$ios"
python3 - "$out" <<'PY'
import sys
out = sys.argv[1]
s = open("project.yml").read()
def rep(old, new):
    global s
    assert s.count(old) == 1, f"project.yml 里找不到唯一的：{old!r}"
    s = s.replace(old, new)
rep("      - framework: build/Iostunnel.xcframework\n        embed: false\n", "")
rep("      - framework: build/Mobilebind.xcframework\n", f"      - framework: {out}/Mobilebind.xcframework\n")
cond = "      SWIFT_ACTIVE_COMPILATION_CONDITIONS: $(inherited) VELUM_ONLY\n"
rep("      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum\n", "      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum\n" + cond)
rep("      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum.tunnel\n", "      PRODUCT_BUNDLE_IDENTIFIER: io.github.foreverwzh.velum.tunnel\n" + cond)
rep("name: Velum\n", "name: VelumStore\n")
assert "Iostunnel" not in s
open("project-velumonly.yml", "w").write(s)
PY
# 工程放在本目录（路径都相对这里），名字与默认工程区分
xcodegen --spec project-velumonly.yml >/dev/null
rm project-velumonly.yml
echo "已生成 VelumStore.xcodeproj（不含 sing-box；scheme 仍是 Velum）"
