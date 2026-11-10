#!/bin/sh
# 生成 App 附带的第三方版权声明 build/ThirdPartyNotices.txt：上架构建（velumonly）链接的 Go 模块、gomobile 运行时、
# Go 标准库，以及 velum-engine 的 Rust crate（由 velum-engine 的 scripts/notices.py 生成）。
set -eu
cd "$(dirname "$0")/.."
ios=$(pwd)
src="${VELUM_ENGINE_DIR:-$(cd ../../.. && pwd)/velum-engine}"
mkdir -p build
(cd "$src" && python3 scripts/notices.py "$ios/build/THIRD_PARTY_RUST.txt" >/dev/null)
cd ../../core
TAGS="$(sed -n 's/^TAGS ?= //p' Makefile),velumonly"
{
  go list -tags "$TAGS" -deps -f '{{if .Module}}{{if not .Standard}}{{.Module.Path}} {{.Module.Version}} {{.Module.Dir}}{{end}}{{end}}' ./mobilebind
  go list -m -f '{{.Path}} {{.Version}} {{.Dir}}' golang.org/x/mobile
} | sort -u | grep -v '^github.com/Foreverwzh/velum/' | python3 -c '
import glob, os, sys
# Homebrew 的 Go 把 LICENSE 放在 GOROOT 的上一级
lic = next(p for p in (os.path.join(sys.argv[1], "LICENSE"), os.path.join(sys.argv[1], "..", "LICENSE")) if os.path.exists(p))
out = ["Velum 使用的第三方组件", "", "=" * 72, "Go 标准库", "", open(lic).read().strip(), ""]
for line in sys.stdin:
    path, ver, d = line.split()
    files = sorted(f for p in ("LICENSE*", "LICENCE*", "COPYING*", "NOTICE*") for f in glob.glob(os.path.join(d, p)) if os.path.isfile(f))
    texts = [open(f, errors="replace").read().strip() for f in files]
    if any("GNU GENERAL PUBLIC LICENSE" in t.upper() or "GNU LESSER GENERAL" in t.upper() for t in texts):
        sys.exit(f"Go 模块 {path} 是 copyleft 许可")
    out += ["=" * 72, f"{path} {ver}", ""] + texts + [""]
print("\n".join(out))
' "$(go env GOROOT)" > "$ios/build/THIRD_PARTY_GO.txt"
cd "$ios"
cat build/THIRD_PARTY_GO.txt build/THIRD_PARTY_RUST.txt > build/ThirdPartyNotices.txt
rm build/THIRD_PARTY_GO.txt build/THIRD_PARTY_RUST.txt
echo "build/ThirdPartyNotices.txt：$(du -h build/ThirdPartyNotices.txt | cut -f1)"
