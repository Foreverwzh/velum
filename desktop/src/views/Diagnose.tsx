import { useEffect, useState, type ReactNode } from "react";
import { ArrowRightLeft, Check, CircleHelp, Globe2, ListTree, RefreshCw, Route, ScanSearch, Server, X } from "lucide-react";
import { api, errText, human, srcText, type Connection, type Source, type WhyResponse } from "../api";
import { Button, Chain, Empty, Notice, Panel, Section, Tag, chainSteps, cx } from "../components/ui";

const examples = ["https://www.google.com/", "claude.ai", "github.com"];

/** 诊断：逐步推演一个地址会怎么走（系统代理 → DNS → 规则 → 出站），并对照实际连接。 */
export default function Diagnose({ initialTarget }: { initialTarget?: string }) {
  const [target, setTarget] = useState(initialTarget ?? "");
  const [res, setRes] = useState<WhyResponse>();
  const [live, setLive] = useState<Connection[]>([]);
  const [err, setErr] = useState<string>();
  const [busy, setBusy] = useState(false);

  const run = async (t = target) => {
    const v = t.trim();
    if (!v) return;
    setTarget(v);
    setBusy(true);
    setErr(undefined);
    try {
      const r = await api.why(v);
      setRes(r);
      const host = r.report.target.domain ?? r.report.target.ip ?? "";
      setLive((await api.connections(5, host)).connections ?? []);
    } catch (e) {
      setErr(errText(e));
      setRes(undefined);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (initialTarget) void run(initialTarget);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const r = res?.report;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <form className="flex items-center gap-2 border-b border-line px-6 pt-1 pb-3" onSubmit={(e) => { e.preventDefault(); void run(); }}>
        <label className="flex h-8 flex-1 items-center gap-2 rounded-md border border-line-strong bg-panel px-2.5 transition-colors focus-within:border-accent">
          <ScanSearch className="size-4 text-fg-3" strokeWidth={1.8} />
          <input value={target} onChange={(e) => setTarget(e.target.value)} autoFocus={!initialTarget} spellCheck={false}
            placeholder="输入网址、域名或 IP，看它会走哪条规则、哪个节点"
            className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-fg-3" />
        </label>
        <Button type="submit" variant="primary" loading={busy} disabled={!target.trim()}>推演</Button>
      </form>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[780px] px-6 py-5">
          {err && <Notice tone="err">{err}</Notice>}
          {!r && !err && (
            <Empty icon={Route} title="这个地址会怎么走？" hint="Velum 会逐步推演系统代理豁免、DNS、规则匹配，给出最终出站，并标明每一步来自订阅或本地覆盖的哪一行。"
              action={<div className="flex flex-wrap justify-center gap-2">{examples.map((x) => <Button key={x} size="sm" onClick={() => void run(x)}>{x}</Button>)}</div>} />
          )}
          {r && res && (
            <div className="flex flex-col gap-6">
              <header>
                <div className="flex items-center gap-2">
                  <h1 className="selectable truncate text-[18px] font-semibold tracking-[-0.015em]">{r.target.domain ?? r.target.ip}<span className="text-fg-3">:{r.target.port}</span></h1>
                  {r.decision.uncertain ? <Tag tone="warn">结论不确定</Tag> : <Tag tone="accent">结论确定</Tag>}
                </div>
                <div className="mt-2">
                  <Chain steps={r.decision.action === "reject" ? [{ label: ruleText(r.decision.rule_index), kind: "rule" }, { label: "拦截", kind: "reject" }]
                    : chainSteps(res.chain?.length ? res.chain : [r.decision.outbound ?? "?"], { label: ruleText(r.decision.rule_index) })} />
                </div>
              </header>

              <ol className="relative flex flex-col">
                <Step icon={ArrowRightLeft} title="系统代理">
                  {r.sys_proxy.bypassed ? <>命中豁免 <code className="font-mono">{r.sys_proxy.pattern}</code>，不经过 Velum <Src s={r.sys_proxy.source} /></>
                    : r.sys_proxy.enabled ? "未命中豁免，请求进入 Velum" : "未接管系统代理；经 Velum 端口或 TUN 访问时如下"}
                </Step>
                <Step icon={Globe2} title="DNS">
                  {!r.dns.applicable ? "目标是 IP，无需解析" : r.dns.answer?.length ? <>hosts 覆盖为 <code className="font-mono">{r.dns.answer.join(", ")}</code> <Src s={r.dns.source} /></> : <>由 <code className="font-mono">{r.dns.server}</code> 解析 <Src s={r.dns.source} /></>}
                  {!!res.resolved?.length && <div className="mt-1 text-fg-3">结果 <span className="selectable font-mono">{res.resolved.join(", ")}</span>（{res.resolved_by === "engine" ? "引擎按 DNS 规则解析" : "手动指定"}）</div>}
                  {res.resolve_error && <div className="mt-1 text-warn">解析失败：{res.resolve_error}</div>}
                </Step>
                <Step icon={ListTree} title="规则">
                  <RuleTrace res={res} />
                </Step>
                <Step icon={Server} title="出站" last>
                  <span className="font-medium">{res.chain?.length ? res.chain.join(" → ") : r.decision.action === "reject" ? "拦截" : r.decision.outbound}</span>
                  {r.decision.override_address && <span className="ml-2 text-fg-3">目标改写为 <code className="font-mono">{r.decision.override_address}</code>（hosts 覆盖）</span>}
                </Step>
              </ol>

              {r.notes?.map((n) => <Notice key={n} tone="warn">{n}</Notice>)}

              <Section title="实际连接（最近 5 条）">
                <Panel>
                  {!live.length && <div className="px-3.5 py-5 text-center text-[12.5px] text-fg-3">还没有访问过这个目标；访问一次后再推演，可以对照实际走向</div>}
                  {live.map((c) => (
                    <div key={c.id} className="flex items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-0">
                      <span className="w-[64px] shrink-0 font-mono text-[11.5px] text-fg-3 tnum">{new Date(c.start).toLocaleTimeString([], { hour12: false })}</span>
                      <Chain size="sm" className="flex-1" steps={chainSteps(c.chain, { label: ruleText(c.rule_index), title: srcText(c.provenance) })} />
                      {c.rule_index !== r.decision.rule_index && <Tag tone="warn">与推演不一致</Tag>}
                      <span className="shrink-0 font-mono text-[11.5px] text-fg-3 tnum">↓ {human(c.download)}</span>
                    </div>
                  ))}
                </Panel>
              </Section>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

const ruleText = (i: number) => (i >= 0 ? `#${i}` : "final");

function Step({ icon: Icon, title, children, last }: { icon: typeof Globe2; title: string; children: ReactNode; last?: boolean }) {
  return (
    <li className="relative grid grid-cols-[28px_1fr] gap-3 pb-5 last:pb-0">
      {!last && <span className="absolute top-7 bottom-0 left-[13.5px] w-px bg-line-strong" />}
      <span className="z-[1] flex size-7 items-center justify-center rounded-full border border-line-strong bg-panel text-fg-2">
        <Icon className="size-3.5" strokeWidth={1.8} />
      </span>
      <div className="min-w-0 pt-[5px] text-[12.5px]">
        <div className="mb-1 text-[12px] font-semibold text-fg-2">{title}</div>
        <div className="text-fg">{children}</div>
      </div>
    </li>
  );
}

function Src({ s }: { s?: Source }) {
  return s ? <span className="selectable ml-1 font-mono text-[11.5px] text-fg-3">{srcText(s)}</span> : null;
}

function RuleTrace({ res }: { res: WhyResponse }) {
  const [all, setAll] = useState(false);
  const r = res.report;
  const rows = r.rules.filter((t) => all || t.matched || t.unknown || t.action === "resolve");
  return (
    <div>
      <div className="overflow-hidden rounded-md border border-line">
        {rows.map((t) => {
          const Icon = t.action === "resolve" ? RefreshCw : t.matched ? Check : t.unknown ? CircleHelp : X;
          const tone = t.action === "resolve" ? "text-info" : t.matched ? "text-accent" : t.unknown ? "text-warn" : "text-fg-3";
          return (
            <div key={t.index} className={cx("grid grid-cols-[40px_16px_1fr] items-start gap-2 border-b border-line px-2.5 py-1.5 last:border-0", t.matched && t.action !== "resolve" && "bg-accent-soft")}>
              <span className="text-right font-mono text-[11.5px] text-fg-3 tnum">#{t.index}</span>
              <Icon className={cx("mt-[2px] size-3.5", tone)} strokeWidth={2.2} />
              <div className="min-w-0">
                <div className="selectable truncate font-mono text-[12px]" title={srcText(t.source)}>{t.source.text ?? srcText(t.source)}</div>
                <div className="text-[11.5px] text-fg-3">{t.reason}<span className="ml-2 font-mono">{srcText({ ...t.source, text: undefined })}</span></div>
              </div>
            </div>
          );
        })}
        {r.decision.rule_index < 0 && (
          <div className="grid grid-cols-[40px_16px_1fr] items-start gap-2 bg-accent-soft px-2.5 py-1.5">
            <span className="text-right font-mono text-[11.5px] text-fg-3">final</span>
            <Check className="mt-[2px] size-3.5 text-accent" strokeWidth={2.2} />
            <span className="selectable font-mono text-[12px]">{srcText(r.decision.source)}</span>
          </div>
        )}
      </div>
      <button className="mt-1.5 text-[12px] text-accent hover:underline" onClick={() => setAll(!all)}>
        {all ? "只看命中与未知" : `显示全部 ${r.rules.length} 条规则（含未命中原因）`}
      </button>
    </div>
  );
}
