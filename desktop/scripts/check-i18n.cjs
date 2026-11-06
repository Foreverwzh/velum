// 检查界面文字都有英文：源码里的中文都经 t()，t() 的键都在 en.ts，占位符一致。
const ts = require("typescript"), fs = require("fs"), path = require("path");
const root = path.join(__dirname, "..", "src"), han = /[一-鿿]/;
const en = new Function(fs.readFileSync(path.join(root, "en.ts"), "utf8").replace(/^\/\/.*$/m, "").replace("export const en =", "return") )();
const used = new Set(), bad = [];
function walk(d) { for (const f of fs.readdirSync(d)) { const p = path.join(d, f); if (fs.statSync(p).isDirectory()) walk(p); else if (/\.tsx?$/.test(f) && f !== "en.ts") one(p); } }
function one(p) {
  const sf = ts.createSourceFile(p, fs.readFileSync(p, "utf8"), ts.ScriptTarget.Latest, true, p.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const at = (n) => `${path.relative(root, p)}:${sf.getLineAndCharacterOfPosition(n.getStart()).line + 1}`;
  (function visit(n) {
    if (ts.isTypeNode(n)) return;
    if (ts.isJsxText(n) && han.test(n.getFullText())) bad.push(`${at(n)} 没有经 t()：${n.getFullText().trim()}`);
    if (ts.isTemplateExpression(n) && [n.head, ...n.templateSpans.map((s) => s.literal)].some((x) => han.test(x.text))) bad.push(`${at(n)} 模板字符串没有经 t()`);
    if (ts.isStringLiteralLike(n) && han.test(n.text)) {
      const c = n.parent;
      if (ts.isCallExpression(c) && c.expression.getText() === "t" && c.arguments[0] === n) used.add(n.text);
      else if (n.text !== "中文") bad.push(`${at(n)} 没有经 t()：${n.text}`); // 语言名称总是用它自己的文字
    }
    ts.forEachChild(n, visit);
  })(sf);
}
walk(root);
const vars = (s) => (s.match(/\{\d+\}/g) || []).sort().join();
for (const k of used) if (!(k in en)) bad.push(`缺少英文：${k}`);
for (const k of Object.keys(en)) {
  if (!used.has(k)) bad.push(`英文对照里有已经不用的条目：${k}`);
  if (vars(k) !== vars(en[k])) bad.push(`占位符不一致：${k} → ${en[k]}`);
  if (han.test(en[k])) bad.push(`英文里还有中文：${en[k]}`);
}
if (bad.length) { console.error(bad.join("\n")); process.exit(1); }
console.log(`i18n：${used.size} 条，全部有英文`);
