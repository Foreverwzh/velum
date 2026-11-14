// 发布前校验 SHA256SUMS 的签名：私钥和程序内置的公钥不匹配时让发布失败，而不是发出一个无法升级的版本。
// 与 vlu upgrade 的校验一致（minisign 预哈希格式，受保护的注释里的文件名与版本号要对得上）。
// 公钥取桌面端 updater 的（desktop/src-tauri/tauri.conf.json），与 vlu 内置的是同一把。
//   node core/scripts/verify-release.mjs <SHA256SUMS> <SHA256SUMS.sig> <版本>
import { createHash, createPublicKey, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import { basename } from "node:path";

const [sumsPath, sigPath, version] = process.argv.slice(2);
if (!version) {
  console.error("用法：verify-release.mjs <SHA256SUMS> <SHA256SUMS.sig> <版本>");
  process.exit(2);
}
const b64 = (s) => Buffer.from(s.trim(), "base64");
const fail = (m) => {
  console.error("verify-release:", m);
  process.exit(1);
};

const conf = JSON.parse(readFileSync(new URL("../../desktop/src-tauri/tauri.conf.json", import.meta.url)));
const key = b64(b64(conf.plugins.updater.pubkey).toString().trim().split("\n")[1]);
if (key.length !== 42 || key.subarray(0, 2).toString() !== "Ed") fail("发布公钥无效");
// Ed25519 公钥的 SPKI 前缀 + 32 字节原始公钥
const pk = createPublicKey({
  key: Buffer.concat([Buffer.from("302a300506032b6570032100", "hex"), key.subarray(10)]),
  format: "der",
  type: "spki",
});

const lines = b64(readFileSync(sigPath, "utf8")).toString().replace(/\n+$/, "").split("\n");
if (lines.length !== 4) fail("签名格式错误：应为 4 行");
const body = b64(lines[1]);
if (body.length !== 74 || body.subarray(0, 2).toString() !== "ED") fail("签名格式错误：只接受预哈希（ED）签名");
if (!body.subarray(2, 10).equals(key.subarray(2, 10))) fail("签名不是由 Velum 的发布密钥产生的（私钥与内置公钥不匹配）");
const digest = createHash("blake2b512").update(readFileSync(sumsPath)).digest();
if (!verify(null, digest, pk, body.subarray(10))) fail("签名校验失败");
const comment = lines[2].replace(/^trusted comment: /, "");
if (comment === lines[2]) fail("签名格式错误：缺少受保护的注释");
if (!verify(null, Buffer.concat([body.subarray(10), Buffer.from(comment)]), pk, b64(lines[3]))) {
  fail("签名校验失败：受保护的注释被改动过");
}
const field = (k) => comment.split("\t").map((f) => f.split(/:(.*)/s)).find(([a]) => a === k)?.[1] ?? "";
if (field("file") !== basename(sumsPath)) fail(`签名针对的是 ${field("file")}，不是 ${basename(sumsPath)}`);
if (field("version") !== version) fail(`签名针对的版本是 ${field("version")}，不是 ${version}`);
console.log("签名校验通过：", sumsPath);
