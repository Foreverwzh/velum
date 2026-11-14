#!/bin/sh
# 生成 Rust 核心（velum-core、velumd、引擎）返回的文字的英文表（iOS App、桌面安装包共用）：
#   sh core/scripts/core-i18n.sh <输出的 JSON>
# = Go 版也在用的英文表（core/pkg/i18n/en.json）+ 只有 Rust 用到的条目（core/i18n/core-en.json）。不需要 Go。
# 有 velum-engine 源码时（VELUM_ENGINE_DIR）检查它的 t("…") / tf("…") 是否都有译文、占位符是否一致，不一致就失败。
set -eu
out="${1:?用法：$0 <输出的 JSON>}"
case "$out" in /* | [A-Za-z]:*) ;; *) out="$(pwd)/$out" ;; esac # Windows 的 D:/… 也是绝对路径
cd "$(dirname "$0")/.."
core=$(pwd)
src="${VELUM_ENGINE_DIR:-$(cd ../.. && pwd)/velum-engine}"
# PYTHONUTF8：Windows 上 Python 默认按 cp1252 读写文件
PYTHONUTF8=1 python3 - "$core" "$src" "$out" <<'PY'
import glob, json, os, re, sys
core, src, out = sys.argv[1:4]
cat = json.load(open(f"{core}/pkg/i18n/en.json"))
own = json.load(open(f"{core}/i18n/core-en.json"))
cat.update(own)
# 译文要保留原文的占位符（个数、顺序都一样：tf 按顺序替换）
verbs = lambda s: re.findall(r"%[sdq]", s)
bad = [k for k, v in own.items() if verbs(k) != verbs(v)]
if bad:
    print("这些译文的占位符与原文不一致：", *bad, sep="\n  ", file=sys.stderr)
    sys.exit(1)
os.makedirs(os.path.dirname(out), exist_ok=True)
json.dump(cat, open(out, "w"), ensure_ascii=False, sort_keys=True)
if os.path.isdir(f"{src}/crates"):
    pat = re.compile(r'\b(?:i18n::)?(?:tf?|gof)\(\s*"((?:[^"\\]|\\.)*)"')
    keys = set()
    for f in glob.glob(f"{src}/crates/*/src/**/*.rs", recursive=True):
        for m in pat.finditer(open(f, encoding="utf-8").read()):
            k = json.loads(f'"{m.group(1)}"', strict=False)
            if re.search(r"[一-鿿]", k):
                keys.add(k)
    missing = sorted(k for k in keys if k not in cat)
    if missing:
        print("这些文字还没有英文（加到 core/i18n/core-en.json）：", file=sys.stderr)
        for k in missing:
            print("  " + k, file=sys.stderr)
        sys.exit(1)
print(f"core-en.json：{len(cat)} 条")
PY
