import { useCallback, useEffect, useMemo, useState } from "react";
import { SubscriptionForm } from "../components/SubscriptionForm";
import { TunToggle, installHelper } from "../components/TunToggle";
import { api, componentStatus, componentText, errText, human, isWindows, native, srcText, subscribeConnections, type CliStatus, type Connection, type HelperStatus, type Status, type Warning, type WhyResponse } from "../api";
import { health } from "../health";
import { Badge, Button, Card, Dot, Empty, Toggle, cx, delayTone, titlebarOverlay } from "../components/ui";
import type { Refresh } from "../App";

type Section = "overview" | "connections" | "why" | "groups" | "overlay" | "warnings";
const nav: { id: Section; label: string }[] = [
  { id: "overview", label: "概览" },
  { id: "connections", label: "连接" },
  { id: "why", label: "诊断" },
  { id: "groups", label: "策略组" },
  { id: "overlay", label: "本地覆盖" },
  { id: "warnings", label: "告警" },
];

export default function Pro({ status, error, refresh, onSimple }: { status: Status | null; error?: string; refresh: Refresh; onSimple: () => void }) {
  const [section, setSection] = useState<Section>(() => (new URLSearchParams(location.search).get("section") as Section) ?? "overview");
  const h = health(status, error);
  return (
    <div className="flex h-full">
      <aside className={cx("flex w-[188px] shrink-0 flex-col border-r border-neutral-200 bg-neutral-50/80 p-3 dark:border-neutral-800 dark:bg-neutral-900", titlebarOverlay && "pt-10")}>
        <div className="mb-4 flex items-center gap-2 px-2 pt-1">
          <Dot tone={h.tone} />
          <div className="min-w-0">
            <div className="text-[14px] font-semibold">Velum</div>
            <div className="truncate text-[11px] text-neutral-500">{h.title}</div>
          </div>
        </div>
        <nav className="flex flex-col gap-0.5">
          {nav.map((n) => (
            <button
              key={n.id}
              onClick={() => setSection(n.id)}
              className={cx("flex h-7 items-center justify-between rounded-md px-2 text-left", section === n.id ? "bg-neutral-200/80 font-medium dark:bg-neutral-800" : "hover:bg-neutral-200/50 dark:hover:bg-neutral-800/60")}
            >
              {n.label}
              {n.id === "warnings" && !!status?.warnings && <span className="text-[11px] text-neutral-500">{status.warnings}</span>}
            </button>
          ))}
        </nav>
        <div className="mt-auto">
          <Button variant="ghost" className="w-full justify-start" onClick={onSimple}>← 极简模式</Button>
        </div>
      </aside>
      <main className={cx("min-w-0 flex-1 overflow-auto p-5", titlebarOverlay && "pt-9")}>
        {!status || status.starting ? (
          <Empty>{h.title}{h.detail && `：${h.detail}`}</Empty>
        ) : (
          {
            overview: <Overview status={status} refresh={refresh} />,
            connections: <Connections />,
            why: <Why />,
            groups: <Groups status={status} refresh={refresh} />,
            overlay: <OverlayEditor refresh={refresh} />,
            warnings: <Warnings />,
          }[section]
        )}
      </main>
    </div>
  );
}

// ---------------- 概览 ----------------

function Overview({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  const h = health(s);
  const [msg, setMsg] = useState<string>();
  const [editSub, setEditSub] = useState<string>(); // undefined = 未在编辑
  const openSub = async () => {
    try {
      setEditSub((await api.subscription()).url);
    } catch (e) {
      setMsg(String((e as Error).message));
    }
  };
  const setSwitch = async (which: "system_proxy" | "tun", v: boolean) => {
    try {
      await api.setSwitch(which, v ? "on" : "off");
      setMsg(undefined);
    } catch (e) {
      setMsg(String((e as Error).message));
    }
    await refresh();
  };
  return (
    <div className="grid max-w-[880px] gap-4">
      <Card title="状态" actions={<Badge tone={h.tone === "gray" ? "neutral" : h.tone}>{h.title}</Badge>}>
        <div className="selectable text-neutral-500">{h.detail}</div>
        <div className="mt-2 grid max-w-[520px] divide-y divide-neutral-100 dark:divide-neutral-800">
          <Toggle label="系统代理" hint="浏览器等遵守系统代理的应用经 Velum" on={s.system_proxy_on} want={s.want_system_proxy}
            paused={s.network?.paused} onChange={(v) => setSwitch("system_proxy", v)} />
          <TunToggle status={s} onDone={refresh} />
        </div>
        {s.listen && <div className="mt-1 text-neutral-500">本地代理入口 <code className="selectable">{s.listen}</code>{s.overridden && "；开关由界面设置，覆盖配置文件；重启后保持"}</div>}
        {msg && <div className="selectable mt-2 text-red-600">{msg}</div>}
      </Card>
      <div className="grid grid-cols-2 gap-4">
        <Card title="订阅" actions={editSub === undefined && <Button variant="ghost" onClick={() => void openSub()}>修改</Button>}>
          {editSub !== undefined ? (
            <SubscriptionForm initial={editSub} autoFocus onCancel={() => setEditSub(undefined)}
              onDone={async () => { setEditSub(undefined); await refresh(); }} />
          ) : <KV rows={[
            ["来源", <span className="selectable break-all">{s.subscription.source || (s.loaded_at.startsWith("0001") ? "—" : "未配置")}</span>],
            ["获取", s.subscription.source ? `${s.subscription.origin} · ${new Date(s.subscription.fetched_at).toLocaleString()}` : "—"],
            ["内容", `${s.subscription.nodes} 节点 · ${s.subscription.groups} 策略组 · ${s.subscription.rules} 规则 · ${s.subscription.rule_sets} 规则集`],
            ...(s.subscription.fetch_error ? [["⚠", <span className="text-amber-600">远程失败，使用缓存：{s.subscription.fetch_error}</span>] as [string, React.ReactNode]] : []),
          ]} />}
        </Card>
        <Card title="网络环境">
          <KV rows={[
            ["主接口", s.network?.primary_interface || "—"],
            ["连通性", s.network?.captive ?? "—"],
            ...(s.network?.vpns ?? []).map((v) => ["外部 VPN", `${v.iface} · ${v.full ? "全局接管" : `分流 ${v.routes?.length ?? 0} 网段 / ${v.domains?.length ?? 0} 域名`}`] as [string, string]),
            ...(s.network?.split_dns_domains?.length ? [["DNS 分流", s.network.split_dns_domains.join("、")] as [string, string]] : []),
            ...(s.network?.paused ? [["已暂停", <span className="text-amber-600">{s.network.paused}</span>] as [string, React.ReactNode]] : []),
          ]} />
        </Card>
      </div>
      <SystemCard status={s} refresh={refresh} />
      {s.tun && (
        <Card title="TUN">
          <KV rows={[
            ["接口", `${s.tun.name || "（未打开）"} · ${s.tun.address} ${s.tun.address6 ?? ""}`],
            ["路由", <span className="selectable font-mono text-[11.5px]">{s.tun.routes.join("  ")}</span>],
            ...(s.tun.system_dns?.length ? [["系统 DNS", `已指向 ${s.tun.system_dns.join(", ")}`] as [string, string]] : []),
          ]} />
        </Card>
      )}
      <div className="text-[11px] text-neutral-400">velumd {s.version} · pid {s.pid} · {s.home}</div>
    </div>
  );
}

function KV({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[72px_1fr] gap-x-3 gap-y-1.5">
      {rows.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-neutral-500">{k}</dt>
          <dd className="min-w-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// ---------------- 连接 ----------------

function Connections() {
  const [list, setList] = useState<Connection[]>([]);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<Connection>();
  useEffect(() => {
    api.connections(300).then((r) => setList(r.connections ?? []));
    return subscribeConnections((c) => setList((xs) => [c, ...xs.filter((x) => x.id !== c.id)].slice(0, 500)));
  }, []);
  const shown = useMemo(() => {
    const k = q.trim().toLowerCase();
    if (!k) return list;
    return list.filter((c) => [c.domain, c.destination, c.process, c.chain.join(" "), c.provenance.text].some((x) => x?.toLowerCase().includes(k)));
  }, [list, q]);
  return (
    <div className="flex h-full flex-col gap-3">
      <div className="flex items-center gap-2">
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="按域名 / 进程 / 出站 / 规则过滤"
          className="h-8 w-[360px] rounded-md border border-neutral-200 bg-white px-2.5 outline-none focus:border-blue-400 dark:border-neutral-700 dark:bg-neutral-800" />
        <span className="text-neutral-500">{shown.length} 条 · 实时</span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto rounded-lg border border-neutral-200 bg-white dark:border-neutral-800 dark:bg-neutral-800/40">
        <table className="w-full table-fixed text-[12px]">
          <thead className="sticky top-0 bg-neutral-50 text-left text-neutral-500 dark:bg-neutral-800">
            <tr>
              <th className="w-[70px] px-3 py-1.5 font-medium">时间</th>
              <th className="px-3 py-1.5 font-medium">目标</th>
              <th className="w-[110px] px-3 py-1.5 font-medium">进程</th>
              <th className="w-[200px] px-3 py-1.5 font-medium">出站</th>
              <th className="w-[70px] px-3 py-1.5 text-right font-medium">下行</th>
              <th className="w-[70px] px-3 py-1.5 text-right font-medium">首包</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((c) => (
              <tr key={c.id} onClick={() => setSel(c)} className={cx("cursor-default border-t border-neutral-100 dark:border-neutral-800", sel?.id === c.id ? "bg-blue-50 dark:bg-blue-950/40" : "hover:bg-neutral-50 dark:hover:bg-neutral-800")}>
                <td className="px-3 py-1 text-neutral-500">{new Date(c.start).toLocaleTimeString()}</td>
                <td className="truncate px-3 py-1">{c.domain || c.destination}{c.errors?.length && !isRejected(c) ? <span className="ml-1 text-red-500" title={c.errors[0]}>⚠</span> : null}</td>
                <td className="truncate px-3 py-1 text-neutral-500">{c.process?.split("/").pop()}</td>
                <td className="truncate px-3 py-1">{isRejected(c) ? <Badge tone="neutral">拦截</Badge> : c.chain.join(" → ")}</td>
                <td className="px-3 py-1 text-right tabular-nums">{human(c.download)}</td>
                <td className="px-3 py-1 text-right tabular-nums text-neutral-500">{c.first_byte_ms >= 0 ? `${c.first_byte_ms}ms` : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!shown.length && <Empty>暂无连接。经 Velum 访问任意站点后会实时出现在这里。</Empty>}
      </div>
      {sel && (
        <Card title={sel.domain || sel.destination} actions={<Button variant="ghost" onClick={() => setSel(undefined)}>关闭</Button>}>
          <KV rows={[
            ["规则", <span className="selectable">{sel.rule_index >= 0 ? `#${sel.rule_index} ` : "final "}{srcText(sel.provenance)}</span>],
            ["出站", sel.chain.join(" → ")],
            ["目标", <span className="selectable">{sel.destination}{sel.resolved?.length ? `（解析 ${sel.resolved.join(", ")}）` : ""}</span>],
            ["进程", sel.process ?? "—"],
            ["流量", `↑${human(sel.upload)} ↓${human(sel.download)} · 首包 ${sel.first_byte_ms >= 0 ? sel.first_byte_ms + "ms" : "无"} · 时长 ${sel.duration_ms}ms${sel.sniffed ? ` · 嗅探 ${sel.sniffed}` : ""}`],
            ...(isRejected(sel) ? [] : (sel.errors ?? []).map((e) => ["错误", <span className="selectable text-red-600">{e}</span>] as [string, React.ReactNode])),
          ]} />
        </Card>
      )}
    </div>
  );
}

/** 按规则拦截（REJECT 出站或 reject 动作），其报错是预期行为。 */
function isRejected(c: Connection) {
  return c.chain.length === 1 && (c.chain[0] === "REJECT" || c.chain[0].startsWith("reject"));
}

// ---------------- 诊断 ----------------

function Why() {
  const [target, setTarget] = useState(() => new URLSearchParams(location.search).get("target") ?? "");
  const [res, setRes] = useState<WhyResponse>();
  const [live, setLive] = useState<Connection[]>([]);
  const [err, setErr] = useState<string>();
  const [all, setAll] = useState(false);
  const run = async () => {
    if (!target.trim()) return;
    try {
      const r = await api.why(target.trim());
      setRes(r);
      setErr(undefined);
      const host = r.report.target.domain ?? r.report.target.ip ?? "";
      setLive((await api.connections(5, host)).connections ?? []);
    } catch (e) {
      setErr(String((e as Error).message));
    }
  };
  useEffect(() => {
    if (target) run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const r = res?.report;
  return (
    <div className="grid max-w-[880px] gap-4">
      <div className="flex gap-2">
        <input value={target} onChange={(e) => setTarget(e.target.value)} onKeyDown={(e) => e.key === "Enter" && run()}
          placeholder="输入 URL、域名或 IP，例如 https://www.google.com/"
          className="h-8 flex-1 rounded-md border border-neutral-200 bg-white px-2.5 outline-none focus:border-blue-400 dark:border-neutral-700 dark:bg-neutral-800" />
        <Button variant="primary" onClick={run}>推演</Button>
      </div>
      {err && <div className="selectable text-red-600">{err}</div>}
      {r && res && (
        <>
          <Card title={<span className="selectable">{r.target.domain ?? r.target.ip}:{r.target.port}</span>}
            actions={r.decision.uncertain ? <Badge tone="amber">结论不确定</Badge> : <Badge tone="green">确定</Badge>}>
            <ol className="space-y-3">
              <Step n="①" title="系统代理">
                {r.sys_proxy.bypassed ? <>命中豁免 <code>{r.sys_proxy.pattern}</code> <Src s={r.sys_proxy.source} /></> : r.sys_proxy.enabled ? "未命中豁免，请求进入 Velum" : "未接管系统代理（经 Velum 端口访问时如下）"}
              </Step>
              <Step n="②" title="DNS">
                {!r.dns.applicable ? "目标是 IP，无需解析" : r.dns.answer?.length ? <>hosts 覆盖 {r.dns.answer.join(", ")} <Src s={r.dns.source} /></> : <>{r.dns.server} <Src s={r.dns.source} /></>}
                {!!res.resolved?.length && <div className="text-neutral-500">解析结果 {res.resolved.join(", ")}（{res.resolved_by === "engine" ? "引擎按 DNS 规则解析" : "手动指定"}）</div>}
                {res.resolve_error && <div className="text-amber-600">解析失败：{res.resolve_error}</div>}
              </Step>
              <Step n="③" title="规则">
                <ul className="space-y-1">
                  {r.rules.filter((t) => all || t.matched || t.unknown).map((t) => (
                    <li key={t.index} className="flex gap-2">
                      <span className="w-8 shrink-0 text-right text-neutral-400 tabular-nums">#{t.index}</span>
                      <span className={t.action === "resolve" ? "text-blue-600" : t.matched ? "text-emerald-600" : t.unknown ? "text-amber-600" : "text-neutral-400"}>
                        {t.action === "resolve" ? "↻" : t.matched ? "✓" : t.unknown ? "?" : "✗"}
                      </span>
                      <span className="min-w-0">
                        <span className="selectable">{srcText(t.source)}</span>
                        <span className="ml-2 text-neutral-500">{t.reason}</span>
                      </span>
                    </li>
                  ))}
                  {r.decision.rule_index < 0 && <li className="flex gap-2"><span className="w-8 text-right text-neutral-400">final</span><span className="text-emerald-600">✓</span>{srcText(r.decision.source)}</li>}
                </ul>
                <button className="mt-1 text-[12px] text-blue-600" onClick={() => setAll(!all)}>
                  {all ? "只看命中与未知" : `显示全部 ${r.rules.length} 条（含未命中原因）`}
                </button>
              </Step>
              <Step n="④" title="出站">
                <span className="font-medium">{res.chain?.length ? res.chain.join(" → ") : r.decision.action === "reject" ? "拦截" : r.decision.outbound}</span>
                {r.decision.override_address && <span className="ml-2 text-neutral-500">目标改写为 {r.decision.override_address}（hosts 覆盖）</span>}
              </Step>
            </ol>
            {r.notes?.map((n) => <div key={n} className="mt-3 rounded-md bg-amber-50 px-3 py-2 text-amber-800 dark:bg-amber-950 dark:text-amber-200">{n}</div>)}
          </Card>
          <Card title="实际连接（最近 5 条）">
            {!live.length ? <div className="text-neutral-500">暂无记录。经 Velum 访问一次该目标后再推演，可对照实际走向。</div> : (
              <ul className="space-y-2">
                {live.map((c) => (
                  <li key={c.id} className="border-b border-neutral-100 pb-2 last:border-0 dark:border-neutral-800">
                    <div className="flex gap-3"><span className="text-neutral-500">{new Date(c.start).toLocaleTimeString()}</span><span>{c.chain.join(" → ")}</span>
                      {c.rule_index !== r.decision.rule_index && <Badge tone="amber">与推演不一致</Badge>}</div>
                    <div className="selectable text-neutral-500">{c.rule_index >= 0 ? `#${c.rule_index} ` : "final "}{srcText(c.provenance)} · ↓{human(c.download)} · 首包 {c.first_byte_ms >= 0 ? c.first_byte_ms + "ms" : "无"}</div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </>
      )}
    </div>
  );
}

function Step({ n, title, children }: { n: string; title: string; children: React.ReactNode }) {
  return (
    <li className="grid grid-cols-[72px_1fr] gap-2">
      <div className="font-medium text-neutral-500">{n} {title}</div>
      <div className="min-w-0">{children}</div>
    </li>
  );
}

function Src({ s }: { s?: { kind: string; file?: string; line?: number; text?: string } }) {
  return s ? <span className="selectable ml-1 text-neutral-500">（{srcText(s)}）</span> : null;
}

// ---------------- 策略组 ----------------

function Groups({ status, refresh }: { status: Status; refresh: Refresh }) {
  const [testing, setTesting] = useState<string>();
  const [err, setErr] = useState<string>();
  const test = async (g: string) => {
    setTesting(g);
    try {
      await api.test(g);
      await refresh();
    } catch (e) {
      setErr(String((e as Error).message));
    } finally {
      setTesting(undefined);
    }
  };
  const select = async (g: string, m: string) => {
    try {
      await api.select(g, m);
      setErr(undefined);
      await refresh();
    } catch (e) {
      setErr(String((e as Error).message));
    }
  };
  if (!status.groups?.length) return <Empty>订阅中没有策略组</Empty>;
  return (
    <div className="grid max-w-[880px] gap-4">
      {err && <div className="selectable text-red-600">{err}</div>}
      {status.groups.map((g) => (
        <Card key={g.tag} title={<span>{g.tag} <span className="ml-1 font-normal text-neutral-400">{g.type === "select" ? "手动选择" : "自动测速"}</span></span>}
          actions={<Button variant="ghost" disabled={!!testing} onClick={() => test(g.tag)}>{testing === g.tag ? "测速中…" : "测速"}</Button>}>
          <div className="grid grid-cols-2 gap-1.5 lg:grid-cols-3">
            {g.members.map((m) => (
              <button key={m} disabled={g.type !== "select"} onClick={() => select(g.tag, m)}
                className={cx("flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 text-left",
                  g.selected === m ? "border-blue-500 bg-blue-50 dark:bg-blue-950/40" : "border-neutral-200 dark:border-neutral-700",
                  g.type === "select" ? "hover:border-blue-300" : "cursor-default")}>
                <span className="truncate">{m}</span>
                {g.delays?.[m] !== undefined && <Badge tone={delayTone(g.delays[m])}>{g.delays[m]}ms</Badge>}
              </button>
            ))}
          </div>
        </Card>
      ))}
    </div>
  );
}

// ---------------- 本地覆盖 ----------------

function OverlayEditor({ refresh }: { refresh: Refresh }) {
  const [doc, setDoc] = useState<{ path: string; content: string }>();
  const [text, setText] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  useEffect(() => {
    api.overlay().then((d) => {
      setDoc(d);
      setText(d.content);
    });
  }, []);
  const save = async () => {
    try {
      await api.saveOverlay(text);
      setMsg({ ok: true, text: "已保存并生效" });
      setDoc((d) => d && { ...d, content: text });
      await refresh();
    } catch (e) {
      setMsg({ ok: false, text: String((e as Error).message) });
    }
  };
  return (
    <div className="flex h-full max-w-[880px] flex-col gap-3">
      <div className="text-neutral-500">
        开发直连、hosts 覆盖、系统代理豁免。保存时先校验，生效失败会自动改回原内容。<span className="selectable ml-1 font-mono text-[11px]">{doc?.path}</span>
      </div>
      <textarea value={text} onChange={(e) => setText(e.target.value)} spellCheck={false}
        className="min-h-[360px] flex-1 rounded-lg border border-neutral-200 bg-white p-3 font-mono text-[12px] leading-5 outline-none focus:border-blue-400 dark:border-neutral-700 dark:bg-neutral-800" />
      <div className="flex items-center gap-3">
        <Button variant="primary" disabled={!doc || text === doc.content} onClick={save}>保存并应用</Button>
        <Button disabled={!doc || text === doc.content} onClick={() => doc && setText(doc.content)}>撤销修改</Button>
        {msg && <span className={cx("selectable", msg.ok ? "text-emerald-600" : "text-red-600")}>{msg.text}</span>}
      </div>
    </div>
  );
}

// ---------------- 告警 ----------------

function Warnings() {
  const [ws, setWs] = useState<Warning[]>();
  useEffect(() => {
    api.warnings().then((r) => setWs(r.warnings ?? []));
  }, []);
  if (!ws) return null;
  if (!ws.length) return <Empty>没有告警</Empty>;
  const groups = ws.reduce<Record<string, Warning[]>>((m, w) => ((m[w.category] ??= []).push(w), m), {});
  const names: Record<string, string> = { node: "节点", provider: "节点源", group: "策略组", rule: "规则", ruleset: "规则集", dns: "DNS", global: "全局", overlay: "本地覆盖", render: "渲染" };
  return (
    <div className="grid max-w-[880px] gap-4">
      {Object.entries(groups).map(([cat, list]) => (
        <Card key={cat} title={`${names[cat] ?? cat}（${list.length}）`}>
          <ul className="space-y-2">
            {list.map((w, i) => (
              <li key={i} className="grid grid-cols-[64px_1fr] items-start gap-2">
                <span className="justify-self-start"><Badge tone={w.level === "unsupported" ? "red" : "amber"}>{w.level === "unsupported" ? "不支持" : "降级"}</Badge></span>
                <div className="min-w-0">
                  <div>{w.subject && <span className="font-medium">{w.subject}：</span>}{w.reason}</div>
                  <div className="selectable text-[11.5px] text-neutral-500">{srcText(w.source)}</div>
                </div>
              </li>
            ))}
          </ul>
        </Card>
      ))}
    </div>
  );
}

/** 系统组件：TUN 组件（macOS：velum-helper；Windows：Velum 服务）的安装状态与开机启动。 */
function SystemCard({ status, refresh }: { status: Status; refresh: Refresh }) {
  const [helper, setHelper] = useState<HelperStatus>();
  const [auto, setAuto] = useState<boolean>();
  const [cli, setCli] = useState<CliStatus>();
  const [busy, setBusy] = useState<string>();
  const [msg, setMsg] = useState<string>();
  const load = useCallback(async () => {
    setHelper(await componentStatus().catch(() => undefined));
    if (native.available) {
      setAuto(await native.autostartGet().catch(() => undefined));
      setCli(await native.cliStatus().catch(() => undefined));
    }
  }, []);
  useEffect(() => void load(), [load]);

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setMsg(undefined);
    try {
      await fn();
    } catch (e) {
      setMsg(errText(e));
    } finally {
      setBusy(undefined);
      await load();
      await refresh();
    }
  };
  const uninstall = async () => {
    if (status.want_tun) await api.setSwitch("tun", "off"); // 先关 TUN，避免卸载后断网
    await native.helperUninstall();
  };

  const state = !helper ? <span className="text-neutral-400">检测中…</span>
    : helper.reachable ? <Badge tone="green">{isWindows ? `已安装 · ${helper.version}` : `已安装 · 协议 ${helper.version}`}</Badge>
    : helper.outdated ? <Badge tone="amber">需要更新</Badge>
    : <Badge tone="neutral">未安装</Badge>;
  return (
    <Card title="系统组件">
      <div className="flex items-center justify-between gap-4 py-1">
        <div className="min-w-0">
          <div className="flex items-center gap-2 font-medium">TUN 组件 {state}</div>
          <div className="text-[12px] text-neutral-500">{componentText.brief}</div>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button disabled={!!busy} onClick={() => run("install", installHelper)}>
            {busy === "install" ? "安装中…" : helper?.reachable ? "重新安装" : helper?.outdated ? "更新" : "安装"}
          </Button>
          {(helper?.reachable || helper?.outdated) && (
            <Button variant="danger" disabled={!!busy} onClick={() => run("uninstall", uninstall)}>{busy === "uninstall" ? "卸载中…" : "卸载"}</Button>
          )}
        </div>
      </div>
      {cli && (
        <div className="mt-1 flex items-center justify-between gap-4 border-t border-neutral-100 py-2 dark:border-neutral-800">
          <div className="min-w-0">
            <div className="flex items-center gap-2 font-medium">
              命令行工具 vlu {cli.installed ? <Badge tone="green">已安装</Badge> : <Badge tone="neutral">未安装</Badge>}
            </div>
            <div className="text-[12px] text-neutral-500">
              {isWindows ? "把 Velum 安装目录加入当前用户的 PATH，新打开的终端中可直接使用 vlu" : <>在 <code>{cli.path}</code> 创建链接，终端中可直接使用 vlu（需要管理员密码）</>}
              {cli.conflict && <span className="text-amber-600">；已存在其他 vlu（{cli.conflict}），安装会替换它</span>}
            </div>
          </div>
          <Button disabled={!!busy} variant={cli.installed ? "danger" : undefined} onClick={() => run("cli", cli.installed ? native.cliUninstall : native.cliInstall)}>
            {busy === "cli" ? "处理中…" : cli.installed ? "移除" : "安装"}
          </Button>
        </div>
      )}
      {native.available && auto !== undefined && (
        <div className="mt-1 border-t border-neutral-100 dark:border-neutral-800">
          <Toggle label="开机启动" hint="登录后在后台启动 Velum（只显示托盘图标）" on={auto} want={auto} disabled={!!busy}
            onChange={(v) => run("auto", () => native.autostartSet(v))} />
        </div>
      )}
      {msg && <div className="selectable mt-1 text-[12px] text-red-600">{msg}</div>}
    </Card>
  );
}
