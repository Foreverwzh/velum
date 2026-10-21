import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowRightLeft, Cpu, FileCode2, Globe2, List, MapPin, Network, Plus, Trash2, TriangleAlert } from "lucide-react";
import { api, errText, type Connection, type OverlayEntry, type OverlayItems, type Status, type Warning } from "../api";
import type { Refresh } from "../App";
import { Button, Empty, IconButton, Notice, PageHeader, Panel, Section, Tag, cx, type Tone } from "../components/ui";
import { RuleDialog, parseDomain } from "../components/RuleDialog";

type Mode = "list" | "text";

/** 本地覆盖：默认以规则列表编辑（按条目增删，保留文件注释），也可直接编辑文本。 */
export default function Overlay({ status, refresh }: { status: Status; refresh: Refresh }) {
  const [mode, setMode] = useState<Mode>("list");
  const [items, setItems] = useState<OverlayItems>();
  const [err, setErr] = useState<string>();
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => api.overlayItems().then(setItems).catch((e) => setErr(errText(e))), []);
  useEffect(() => {
    if (mode === "list") void load();
  }, [mode, load]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="本地覆盖"
        description={<>优先于订阅，更新订阅不会覆盖。<span className="selectable ml-1 font-mono">{items?.path}</span></>}
        actions={
          <>
            <div className="inline-flex rounded-md border border-line p-0.5">
              {([["list", List, "列表"], ["text", FileCode2, "文本"]] as const).map(([id, Icon, label]) => (
                <button key={id} onClick={() => setMode(id)}
                  className={cx("inline-flex h-6 items-center gap-1.5 rounded px-2 text-[12px] transition-colors", mode === id ? "bg-active font-medium text-fg" : "text-fg-2 hover:text-fg")}>
                  <Icon className="size-3.5" strokeWidth={1.8} />{label}
                </button>
              ))}
            </div>
            {mode === "list" && <Button variant="primary" icon={Plus} onClick={() => setAdding(true)}>添加规则</Button>}
          </>
        }
      />
      {mode === "text" ? (
        <TextEditor refresh={refresh} />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex max-w-[820px] flex-col gap-6 px-6 py-5">
            {err && <Notice tone="err">{err}</Notice>}
            {items && <RuleList items={items} status={status} refresh={refresh} reload={load} onAdd={() => setAdding(true)} />}
          </div>
        </div>
      )}
      {adding && <RuleDialog refresh={refresh} onSaved={() => void load()} onClose={() => setAdding(false)} />}
    </div>
  );
}

function RuleList({ items, status, refresh, reload, onAdd }: { items: OverlayItems; status: Status; refresh: Refresh; reload: () => Promise<void>; onAdd: () => void }) {
  const [hits, setHits] = useState<Map<string, number>>(new Map());
  const [warns, setWarns] = useState<Warning[]>([]);
  const [busy, setBusy] = useState<string>();
  const [err, setErr] = useState<string>();

  useEffect(() => {
    // 命中次数来自最近的连接记录：overlay 条目按行号，自动识别的 VPN 按来源文字
    api.connections(500).then((r) => setHits(countHits(r.connections ?? []))).catch(() => undefined);
    api.warnings().then((r) => setWarns((r.warnings ?? []).filter((w) => w.category === "overlay"))).catch(() => undefined);
  }, [items]);

  const remove = async (e: OverlayEntry) => {
    const key = e.kind + e.value;
    setBusy(key);
    setErr(undefined);
    try {
      await api.editOverlay("remove", e);
      await Promise.all([reload(), refresh()]);
    } catch (x) {
      setErr(errText(x));
    } finally {
      setBusy(undefined);
    }
  };

  const vpns = (status.network?.vpns ?? []).filter((v) => !v.full);
  const split = status.network?.split_dns_domains ?? [];

  return (
    <>
      {err && <Notice tone="err">{err}</Notice>}
      <Section title={<>我的规则 <span className="font-normal text-fg-3 tnum">{items.entries.length}</span></>}>
        {!items.entries.length ? (
          <Panel>
            <Empty icon={List} title="还没有本地规则" hint="在「连接」或「诊断」里遇到走错的地址时，点「加入本地规则」即可；也可以在这里直接添加公司域名、内网网段"
              action={<Button icon={Plus} onClick={onAdd}>添加规则</Button>} />
          </Panel>
        ) : (
          <Panel>
            {items.entries.map((e) => (
              <EntryRow key={e.kind + e.value} entry={e} hits={e.kind === "bypass" ? undefined : hits.get(`overlay:${e.line}`) ?? 0}
                warnings={warns.filter((w) => w.source.line === e.line)} busy={busy === e.kind + e.value} onRemove={() => void remove(e)} />
            ))}
          </Panel>
        )}
      </Section>

      <Section title="自动识别" actions={<span className="text-[11.5px] text-fg-3">无需配置，随网络变化自动更新</span>}>
        <Panel>
          {vpns.map((v) => (
            <AutoRow key={v.iface} icon={Network} title={<>企业 VPN <span className="font-mono">{v.iface}</span></>}
              detail={[...(v.routes ?? []), ...(v.domains ?? [])].join("、") || "—"}
              action={<Tag tone="accent">经 {v.iface} 直连</Tag>} hits={hits.get(`vpn:${v.iface}`) ?? 0} />
          ))}
          {!!split.length && (
            <AutoRow icon={Globe2} title="系统 DNS 分流" detail={split.join("、")} action={<Tag>DNS 交给系统</Tag>} />
          )}
          {items.system_hosts_off ? (
            <AutoRow icon={MapPin} title="系统 hosts 文件" detail="订阅设置了 use-system-hosts: false，不读取" action={<Tag>未启用</Tag>} />
          ) : items.system_hosts.map((h) => (
            <AutoRow key={h.name} icon={MapPin} title={<span className="font-mono">{h.name}</span>} detail="系统 hosts 文件"
              action={<Tag>→ {h.addrs.join(", ")}</Tag>} />
          ))}
          {items.system_hosts_total > items.system_hosts.length && (
            <div className="px-3.5 py-2 text-[11.5px] text-fg-3">另有 {items.system_hosts_total - items.system_hosts.length} 条 hosts 条目未列出</div>
          )}
          {!vpns.length && !split.length && !items.system_hosts.length && !items.system_hosts_off && (
            <div className="px-3.5 py-5 text-center text-[12.5px] text-fg-3">当前没有识别到企业 VPN、DNS 分流或 hosts 条目</div>
          )}
        </Panel>
      </Section>
    </>
  );
}

function countHits(cs: Connection[]) {
  const m = new Map<string, number>();
  const add = (k: string) => m.set(k, (m.get(k) ?? 0) + 1);
  for (const c of cs) {
    if (c.provenance.kind === "overlay" && c.provenance.line) add(`overlay:${c.provenance.line}`);
    const vpn = /^NetworkSense：检测到 VPN (\S+)/.exec(c.provenance.text ?? "");
    if (vpn) add(`vpn:${vpn[1]}`);
  }
  return m;
}

const kindMeta: Record<OverlayEntry["kind"], { icon: typeof Globe2; action: string; tone: Tone }> = {
  domain: { icon: Globe2, action: "直连", tone: "accent" },
  cidr: { icon: Network, action: "直连", tone: "accent" },
  host: { icon: MapPin, action: "", tone: "info" },
  process: { icon: Cpu, action: "直连", tone: "accent" },
  bypass: { icon: ArrowRightLeft, action: "不经系统代理", tone: "neutral" },
};
const scopeText = { suffix: "含子域", exact: "仅此域名", sub: "仅子域" };

function EntryRow({ entry: e, hits, warnings, busy, onRemove }: { entry: OverlayEntry; hits?: number; warnings: Warning[]; busy: boolean; onRemove: () => void }) {
  const [confirm, setConfirm] = useState(false);
  useEffect(() => {
    if (!confirm) return;
    const t = setTimeout(() => setConfirm(false), 3000);
    return () => clearTimeout(t);
  }, [confirm]);
  const meta = kindMeta[e.kind];
  const d = e.kind === "domain" ? parseDomain(e.value) : undefined;
  return (
    <div className="group border-b border-line px-3.5 py-2.5 last:border-0">
      <div className="flex items-center gap-3">
        <meta.icon className="size-4 shrink-0 text-fg-3" strokeWidth={1.8} />
        <div className="min-w-0 flex-1">
          <span className="selectable font-mono text-[12.5px]">{d ? d.name : e.value}</span>
          {d && <span className="ml-2 text-[11.5px] text-fg-3">{scopeText[d.scope]}</span>}
          {e.kind === "process" && <span className="ml-2 text-[11.5px] text-fg-3">进程</span>}
        </div>
        <Tag tone={meta.tone}>{e.kind === "host" ? `→ ${e.ip}` : meta.action}</Tag>
        <span className="w-[72px] text-right font-mono text-[11.5px] text-fg-3 tnum" title={hits === undefined ? "豁免的请求不经过 Velum，无法统计" : "最近 500 条连接中命中的次数"}>
          {hits === undefined ? "—" : hits ? `${hits} 次` : "未命中"}
        </span>
        <span className="w-[52px] text-right font-mono text-[11px] text-fg-3 tnum">{e.line ? `L${e.line}` : ""}</span>
        {confirm ? (
          <Button size="sm" variant="danger" loading={busy} onClick={onRemove}>确认删除</Button>
        ) : (
          <IconButton icon={Trash2} label="删除" onClick={() => setConfirm(true)} className="opacity-60 group-hover:opacity-100" />
        )}
      </div>
      {warnings.map((w) => (
        <div key={w.reason} className="mt-1.5 flex items-start gap-1.5 pl-7 text-[11.5px] text-warn">
          <TriangleAlert className="mt-0.5 size-3 shrink-0" strokeWidth={2} />{w.reason}
        </div>
      ))}
    </div>
  );
}

function AutoRow({ icon: Icon, title, detail, action, hits }: { icon: typeof Globe2; title: ReactNode; detail: string; action: ReactNode; hits?: number }) {
  return (
    <div className="flex items-center gap-3 border-b border-line px-3.5 py-2.5 last:border-0">
      <Icon className="size-4 shrink-0 text-fg-3" strokeWidth={1.8} />
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px]">{title}</div>
        <div className="selectable truncate font-mono text-[11.5px] text-fg-3" title={detail}>{detail}</div>
      </div>
      {action}
      <span className="w-[72px] text-right font-mono text-[11.5px] text-fg-3 tnum">{hits === undefined ? "" : hits ? `${hits} 次` : "未命中"}</span>
      <span className="w-[52px]" />
      <span className="w-7" />
    </div>
  );
}

/** 文本模式：直接编辑 overlay.yaml；保存时先校验，生效失败自动改回。 */
function TextEditor({ refresh }: { refresh: Refresh }) {
  const [doc, setDoc] = useState<{ path: string; content: string }>();
  const [text, setText] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.overlay().then((d) => {
      setDoc(d);
      setText(d.content);
    }).catch((e) => setMsg({ ok: false, text: errText(e) }));
  }, []);
  const dirty = !!doc && text !== doc.content;
  const save = async () => {
    setBusy(true);
    try {
      await api.saveOverlay(text);
      setMsg({ ok: true, text: "已保存并生效" });
      setDoc((d) => d && { ...d, content: text });
      await refresh();
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    } finally {
      setBusy(false);
    }
  };
  const lines = useMemo(() => text.split("\n").length, [text]);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b border-line px-6 py-2">
        <span className={cx("flex-1 text-[12px]", msg ? (msg.ok ? "text-accent" : "text-err") : "text-fg-3")}>
          {msg?.text ?? `${lines} 行 · 保存前会先校验，生效失败自动改回`}
        </span>
        {dirty && <Tag tone="warn">未保存</Tag>}
        <Button size="sm" variant="ghost" disabled={!dirty || busy} onClick={() => doc && setText(doc.content)}>撤销修改</Button>
        <Button size="sm" variant="primary" loading={busy} disabled={!dirty} onClick={save}>保存并应用</Button>
      </div>
      <textarea
        value={text}
        onChange={(e) => { setText(e.target.value); setMsg(undefined); }}
        onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); if (dirty) void save(); } }}
        spellCheck={false}
        className="min-h-0 flex-1 resize-none bg-panel px-6 py-4 font-mono text-[12.5px] leading-[1.7] outline-none"
      />
    </div>
  );
}
