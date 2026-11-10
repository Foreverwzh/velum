#!/bin/sh
# 生成 build/core-en.json：Rust 核心（velum-core）返回的文字的英文表。
# = Go 版的英文表（core/pkg/i18n/en.go，经 core/internal/i18ndump 导出）+ 只有 Rust 核心用到的条目（i18n/core-en.json）。
# 有 velum-engine 源码时（VELUM_ENGINE_DIR）检查它的 t("…") / tf("…") 是否都有译文，缺的列出来并失败。
set -eu
cd "$(dirname "$0")/.."
ios=$(pwd)
mkdir -p build
(cd ../../core && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" go run ./internal/i18ndump) > build/go-en.json
src="${VELUM_ENGINE_DIR:-$(cd ../../.. && pwd)/velum-engine}"
python3 - "$ios" "$src" <<'PY'
import glob, json, os, re, sys
ios, src = sys.argv[1], sys.argv[2]
cat = json.load(open(f"{ios}/build/go-en.json"))
own = json.load(open(f"{ios}/i18n/core-en.json"))
cat.update(own)
json.dump(cat, open(f"{ios}/build/core-en.json", "w"), ensure_ascii=False, sort_keys=True)
os.remove(f"{ios}/build/go-en.json")
if os.path.isdir(f"{src}/crates"):
    pat = re.compile(r'\b(?:i18n::)?tf?\(\s*"((?:[^"\\]|\\.)*)"')
    keys = set()
    for f in glob.glob(f"{src}/crates/*/src/**/*.rs", recursive=True):
        for m in pat.finditer(open(f, encoding="utf-8").read()):
            k = json.loads(f'"{m.group(1)}"')
            if re.search(r"[一-鿿]", k):
                keys.add(k)
    missing = sorted(k for k in keys if k not in cat)
    if missing:
        print("这些文字还没有英文（加到 mobile/ios/i18n/core-en.json）：", file=sys.stderr)
        for k in missing:
            print("  " + k, file=sys.stderr)
        sys.exit(1)
print(f"core-en.json：{len(cat)} 条")
PY
