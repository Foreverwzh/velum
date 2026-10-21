import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowRightLeft, Check, Cpu, MapPin, Route, X } from "lucide-react";
import { api, errText, type OverlayEntry, type WhyResponse } from "../api";
import type { Refresh } from "../App";
import { Button, Chain, IconButton, Notice, TextInput, chainSteps, cx } from "./ui";

export type Scope = "suffix" | "exact" | "sub";
export type RuleAction = "direct" | "host" | "bypass" | "process";

const scopeLabel: Record<Scope, string> = { suffix: "含子域", exact: "仅此域名", sub: "仅子域" };

/** 把 URL、host:port 等输入整理为主机名或网段。 */
export function cleanTarget(raw: string): string {
  let v = raw.trim();
  try {
    if (/^[a-z]+:\/\//i.test(v)) v = new URL(v).hostname;
  } catch { /* 原样使用 */ }
  v = v.replace(/^\[|\]$/g, "");
  const m = /^([^:/]+):\d+$/.exec(v); // host:port（IPv6 不含端口时有多个冒号，不会匹配）
  if (m) v = m[1];
  return v.replace(/\.$/, "").toLowerCase();
}

export function isNet(v: string): boolean {
  return /^[\d.]+(\/\d+)?$/.test(v) || (v.includes(":") && /^[\da-f:.]+(\/\d+)?$/i.test(v));
}

/** 域名的可选范围：自身及各级父域（至少两级）。 */
function parents(host: string): string[] {
  const parts = host.split(".");
  const out: string[] = [];
  for (let i = 0; i <= parts.length - 2; i++) out.push(parts.slice(i).join("."));
  return out;
}

/** 解析 overlay 域名写法。 */
export function parseDomain(v: string): { name: string; scope: Scope } {
  if (v.startsWith("=")) return { name: v.slice(1), scope: "exact" };
  if (v.startsWith("*.")) return { name: v.slice(2), scope: "sub" };
  if (v.startsWith(".")) return { name: v.slice(1), scope: "sub" };
  return { name: v, scope: "suffix" };
}

function domainValue(name: string, scope: Scope) {
  return { suffix: "", exact: "=", sub: "." }[scope] + name;
}

/** 由界面选择生成 overlay 条目。 */
function entriesFor(action: RuleAction, target: string, base: string, scope: Scope, ip: string, process?: string): OverlayEntry[] {
  const net = isNet(target);
  switch (action) {
    case "process":
      return [{ kind: "process", value: process!.split(/[\\/]/).pop()! }];
    case "host":
      return [{ kind: "host", value: target, ip: ip.trim() }];
    case "bypass":
      // 系统代理豁免按系统语义：根域 + 子域需要同时写 x 和 *.x
      if (net) return [{ kind: "bypass", value: target }];
      return scope === "exact" ? [{ kind: "bypass", value: base }]
        : scope === "sub" ? [{ kind: "bypass", value: "*." + base }]
        : [{ kind: "bypass", value: base }, { kind: "bypass", value: "*." + base }];
    default:
      return [net ? { kind: "cidr", value: target } : { kind: "domain", value: domainValue(base, scope) }];
  }
}

/** 加入本地规则：从连接、诊断或本地覆盖页打开，写入 overlay 后立即重新推演给出新的走向。 */
export function RuleDialog({ initial = "", process, refresh, onClose, onSaved }: {
  initial?: string; process?: string; refresh: Refresh; onClose: () => void; onSaved?: () => void;
}) {
  const [raw, setRaw] = useState(initial);
  const target = cleanTarget(raw);
  const net = isNet(target);
  const bases = useMemo(() => (net || !target.includes(".") ? [] : parents(target)), [target, net]);
  const [base, setBase] = useState("");
  const [scope, setScope] = useState<Scope>("suffix");
  const [action, setAction] = useState<RuleAction>("direct");
  const [ip, setIp] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();
  const [done, setDone] = useState<{ entries: OverlayEntry[]; why?: WhyResponse }>();
  // 默认只覆盖目标域名本身（含子域），需要更大范围时再选父域
  useEffect(() => setBase(bases[0] ?? ""), [bases]);
  useEffect(() => {
    if (net && action === "host") setAction("direct");
  }, [net, action]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const domainOK = net || bases.length > 0;
  const valid = action === "process" ? !!process : !!target && domainOK && (action !== "host" || /^[\da-f.:]+$/i.test(ip.trim()) && ip.trim() !== "");

  const save = async () => {
    if (!valid) return;
    setBusy(true);
    setErr(undefined);
    const entries = entriesFor(action, target, base || target, scope, ip, process);
    try {
      for (const e of entries) await api.editOverlay("add", e);
      await refresh();
      onSaved?.();
      // 豁免后请求不经 Velum，推演仍会标明豁免并给出经 Velum 时的走向
      const why = target ? await api.why(target).catch(() => undefined) : undefined;
      setDone({ entries, why });
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const actions: { id: RuleAction; icon: typeof Route; title: string; hint: string; show: boolean }[] = [
    { id: "direct", icon: Route, title: "直连", hint: "不经代理节点直接连接，并加入系统代理豁免；DNS 交给系统解析。适合公司内网、本地开发服务", show: true },
    { id: "host", icon: MapPin, title: "固定解析到", hint: "无论 DNS 返回什么，都连到指定 IP 并直连（不修改 /etc/hosts）", show: !net },
    { id: "bypass", icon: ArrowRightLeft, title: "不经系统代理", hint: "浏览器等遵守系统代理的应用直接连接，请求不进入 Velum（规则与 hosts 覆盖对它们不再生效）", show: true },
    { id: "process", icon: Cpu, title: "这个进程直连", hint: `${process?.split(/[\\/]/).pop()} 的所有连接都直连`, show: !!process },
  ];

  return (
    <div className="fixed inset-0 z-50 flex justify-center bg-black/25 pt-[10vh] backdrop-blur-[2px] animate-[fade_120ms_ease-out]" onMouseDown={onClose}>
      <div onMouseDown={(e) => e.stopPropagation()} role="dialog" aria-label="加入本地规则"
        className="flex h-fit max-h-[80vh] w-[min(520px,calc(100vw-48px))] flex-col overflow-hidden rounded-xl border border-line-strong bg-raised shadow-pop">
        <div className="flex items-center gap-2 border-b border-line px-4 py-3">
          <div className="flex-1">
            <div className="text-[14px] font-semibold">加入本地规则</div>
            <div className="text-[11.5px] text-fg-3">写入 overlay.yaml，优先于订阅，更新订阅不会覆盖</div>
          </div>
          <IconButton icon={X} label="关闭" onClick={onClose} />
        </div>

        {done ? (
          <div className="flex flex-col gap-4 overflow-y-auto px-4 py-4">
            <Notice tone="accent" icon={Check}>已加入并生效</Notice>
            <Field label="写入的条目">
              <div className="flex flex-col gap-1">
                {done.entries.map((e) => <code key={e.kind + e.value} className="selectable font-mono text-[12px] text-fg-2">{entryText(e)}</code>)}
              </div>
            </Field>
            {done.why && (
              <Field label={`现在 ${done.why.report.target.domain ?? done.why.report.target.ip} 会这样走`}>
                {done.why.report.sys_proxy.bypassed && <div className="mb-1.5 text-[12px] text-fg-2">遵守系统代理的应用：直接连接，不经 Velum</div>}
                <Chain className="flex-wrap" steps={done.why.report.decision.action === "reject" ? [{ label: "拦截", kind: "reject" }]
                  : chainSteps(done.why.chain?.length ? done.why.chain : [done.why.report.decision.outbound ?? "?"],
                    { label: done.why.report.decision.rule_index >= 0 ? `#${done.why.report.decision.rule_index}` : "final" })} />
                {done.why.report.decision.override_address && <div className="mt-1.5 text-[12px] text-fg-3">目标改写为 <code className="font-mono">{done.why.report.decision.override_address}</code></div>}
              </Field>
            )}
            <div className="flex justify-end"><Button variant="primary" onClick={onClose} autoFocus>完成</Button></div>
          </div>
        ) : (
          <form className="flex flex-col gap-4 overflow-y-auto px-4 py-4" onSubmit={(e) => { e.preventDefault(); void save(); }}>
            <Field label="目标">
              <TextInput value={raw} onChange={(e) => setRaw(e.target.value)} autoFocus={!initial}
                placeholder="域名、网址、IP 或网段，如 corp.example.com、10.0.0.0/8" invalid={!!target && !domainOK} />
              {!!target && !domainOK && <div className="mt-1 text-[11.5px] text-err">请输入完整域名（如 example.com）、IP 或网段</div>}
            </Field>

            {bases.length > 0 && action !== "host" && action !== "process" && (
              <Field label="范围">
                <div className="flex flex-wrap gap-1.5">
                  {bases.map((b) => (
                    <Chip key={b} on={base === b} onClick={() => setBase(b)}><span className="font-mono">{b}</span></Chip>
                  ))}
                </div>
                <div className="mt-2 inline-flex rounded-md border border-line p-0.5">
                  {(["suffix", "exact", "sub"] as Scope[]).map((s) => (
                    <button key={s} type="button" onClick={() => setScope(s)}
                      className={cx("h-6 rounded px-2.5 text-[12px] transition-colors", scope === s ? "bg-active font-medium text-fg" : "text-fg-2 hover:text-fg")}>
                      {scopeLabel[s]}
                    </button>
                  ))}
                </div>
                <div className="mt-1.5 text-[11.5px] text-fg-3">{scopeHint(base, scope)}</div>
              </Field>
            )}

            <Field label="动作">
              <div className="flex flex-col gap-1.5">
                {actions.filter((a) => a.show).map((a) => (
                  <label key={a.id}
                    className={cx("flex cursor-default items-start gap-2.5 rounded-md border px-3 py-2 transition-colors",
                      action === a.id ? "border-accent bg-accent-soft" : "border-line hover:bg-hover")}>
                    <input type="radio" name="action" checked={action === a.id} onChange={() => setAction(a.id)} className="sr-only" />
                    <a.icon className={cx("mt-0.5 size-4 shrink-0", action === a.id ? "text-accent" : "text-fg-3")} strokeWidth={1.8} />
                    <span className="min-w-0 flex-1">
                      <span className="block text-[12.5px] font-medium">{a.title}</span>
                      <span className="block text-[11.5px] text-fg-3">{a.hint}</span>
                      {a.id === "host" && action === "host" && (
                        <TextInput className="mt-2" value={ip} onChange={(e) => setIp(e.target.value)} placeholder="IP 地址，如 127.0.0.1" autoFocus />
                      )}
                    </span>
                  </label>
                ))}
              </div>
            </Field>

            {err && <Notice tone="err">{err}</Notice>}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>取消</Button>
              <Button type="submit" variant="primary" loading={busy} disabled={!valid}>加入并生效</Button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}

function scopeHint(base: string, scope: Scope) {
  if (!base) return "";
  return scope === "suffix" ? `${base} 以及 a.${base}、b.a.${base} 等所有子域`
    : scope === "exact" ? `只有 ${base}，不含子域` : `a.${base} 等子域，不含 ${base} 本身`;
}

/** 条目的可读形式（用于结果展示）。 */
export function entryText(e: OverlayEntry): string {
  switch (e.kind) {
    case "domain": { const d = parseDomain(e.value); return `直连  ${d.name}（${scopeLabel[d.scope]}）`; }
    case "cidr": return `直连  ${e.value}`;
    case "host": return `${e.value} → ${e.ip}`;
    case "process": return `直连  进程 ${e.value}`;
    default: return `不经系统代理  ${e.value}`;
  }
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 text-[11.5px] font-medium text-fg-3">{label}</div>
      {children}
    </div>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick}
      className={cx("h-6 rounded-md border px-2 text-[12px] transition-colors", on ? "border-accent bg-accent-soft text-fg" : "border-line text-fg-2 hover:bg-hover")}>
      {children}
    </button>
  );
}
