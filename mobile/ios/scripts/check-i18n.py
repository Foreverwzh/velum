#!/usr/bin/env python3
# 检查 iOS 的文字都有英文。先构建（SWIFT_EMIT_LOC_STRINGS），再比较编译器提取的键和 en.lproj。
import json, re, subprocess, sys
han = re.compile(r"[一-鿿]")
keys = set()
for f in subprocess.run(["find", "build/dd", "-name", "*.stringsdata"], capture_output=True, text=True).stdout.split("\n"):
    if not f or "DemoCore" in f or "UITests" in f:
        continue
    try:
        data = json.load(open(f))
    except ValueError:
        continue
    for items in data.get("tables", {}).values():
        keys |= {i["key"] for i in items if han.search(i["key"])}
en = dict(re.findall(r'^"((?:[^"\\]|\\.)*)" = "((?:[^"\\]|\\.)*)";$', open("App/en.lproj/Localizable.strings").read(), re.M))
en = {k.replace('\\"', '"'): v for k, v in en.items()}
bad = [f"缺少英文：{k}" for k in sorted(keys - set(en))]
bad += [f"英文对照里有已经不用的条目：{k}" for k in sorted(set(en) - keys)]
spec = re.compile(r"%(?:lld|@)")
bad += [f"占位符不一致：{k} → {v}" for k, v in en.items() if spec.findall(k) != spec.findall(v)]
bad += [f"英文里还有中文：{v}" for v in en.values() if han.search(v)]
if not keys:
    bad.append("没有找到提取的键：先构建")
print("\n".join(bad) or f"i18n：{len(keys)} 条，全部有英文")
sys.exit(1 if bad else 0)
