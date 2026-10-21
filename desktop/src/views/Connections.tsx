import { useEffect, useMemo, useRef, useState } from "react";
import { Activity, ArrowDown, ArrowLeftRight, ArrowUp, Ban, ListPlus, Pause, Play, ScanSearch, TriangleAlert, X } from "lucide-react";
import { api, human, rate, srcText, subscribeConnections, type Connection, type Traffic } from "../api";
import type { Go, Refresh } from "../App";
import { RuleDialog } from "../components/RuleDialog";
import { Button, Chain, Empty, IconButton, KV, SearchInput, Tag, chainSteps, cx } from "../components/ui";

/** 按规则拦截（REJECT 出站或 reject 动作），其报错是预期行为。 */
export function isRejected(c: Connection) {
  return c.chain.length === 1 && (c.chain[0] === "REJECT" || c.chain[0].startsWith("reject"));
}

const ruleLabel = (c: Connection) => (c.rule_index >= 0 ? `#${c.rule_index}` : "final");

export default function Connections({ go, refresh, initialQuery }: { go: Go; refresh: Refresh; initialQuery?: string }) {
  const [list, setList] = useState<Connection[]>([]);
  const [q, setQ] = useState(initialQuery ?? "");
  const [sel, setSel] = useState<Connection>();
  const [paused, setPaused] = useState(false);
  const [onlyErr, setOnlyErr] = useState(false);
  const [hideRej, setHideRej] = useState(false);
  const [rule, setRule] = useState<Connection>();
  const [onlyActive, setOnlyActive] = useState(false);
  // 进行中的连接（每秒拉取）及由两次采样得出的速率
  const [active, setActive] = useState<Connection[]>([]);
  const [speeds, setSpeeds] = useState<Map<number, { up: number; down: number }>>(new Map());
  const [traffic, setTraffic] = useState<Traffic>();
  const prev = useRef<{ at: number; bytes: Map<number, { up: number; down: number }> }>(undefined);

  useEffect(() => {
    // 已结束的连接来自历史与结束事件；进行中的由下面的轮询维护
    api.connections(300).then((r) => setList((r.connections ?? []).filter((c) => c.end))).catch(() => undefined);
  }, []);
  useEffect(() => {
    if (paused) return;
    return subscribeConnections((c) => setList((xs) => [c, ...xs.filter((x) => x.id !== c.id)].slice(0, 500)));
  }, [paused]);
  useEffect(() => {
    if (paused) return;
    const poll = async () => {
      const [r, t] = await Promise.all([api.connections(500, "", true), api.traffic()]).catch(() => [undefined, undefined]);
      if (!r || !t) return;
      const now = performance.now();
      const cs = r.connections ?? [];
      const bytes = new Map(cs.map((c) => [c.id, { up: c.upload, down: c.download }]));
      const p = prev.current;
      const sp = new Map<number, { up: number; down: number }>();
      if (p) {
        const sec = (now - p.at) / 1000;
        for (const c of cs) {
          const b = p.bytes.get(c.id);
          if (b && sec > 0) sp.set(c.id, { up: Math.max(0, (c.upload - b.up) / sec), down: Math.max(0, (c.download - b.down) / sec) });
        }
      }
      prev.current = { at: now, bytes };
      setActive(cs);
      setSpeeds(sp);
      setTraffic(t);
    };
    void poll();
    const id = setInterval(poll, 1000);
    return () => clearInterval(id);
  }, [paused]);
  const all = useMemo(() => {
    const ids = new Set(active.map((c) => c.id));
    return [...active, ...(onlyActive ? [] : list.filter((c) => !ids.has(c.id)))];
  }, [active, list, onlyActive]);

  const shown = useMemo(() => {
    const keys = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return all.filter((c) => {
      if (onlyErr && !(c.errors?.length && !isRejected(c))) return false;
      if (hideRej && isRejected(c)) return false;
      if (!keys.length) return true;
      const hay = [c.domain, c.destination, c.process, c.chain.join(" "), c.provenance.text, ruleLabel(c)].join(" ").toLowerCase();
      return keys.every((k) => hay.includes(k));
    });
  }, [all, q, onlyErr, hideRej]);
  const errCount = useMemo(() => list.filter((c) => c.errors?.length && !isRejected(c)).length, [list]);
  const rejCount = useMemo(() => list.filter(isRejected).length, [list]);
  // 详情跟随实时数据（进行中的连接每秒更新）
  const cur = sel && (active.find((c) => c.id === sel.id) ?? list.find((c) => c.id === sel.id) ?? sel);
  // 拿不到进程信息时（如部分 TUN 连接）不占一列
  // 详情面板打开时，进程与首包只在面板中显示，给目标列留出宽度
  const hasProc = useMemo(() => !cur && shown.some((c) => c.process), [shown, cur]);

  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2 border-b border-line px-4 pt-1 pb-2.5">
          <SearchInput value={q} onChange={(e) => setQ(e.target.value)} placeholder="按域名、进程、节点或规则过滤" className="min-w-[140px] max-w-[320px] flex-1" />
          <button onClick={() => setOnlyErr(!onlyErr)}
            className={cx("inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-[12px] transition-colors",
              onlyErr ? "border-err/40 bg-err-soft text-err" : "border-line text-fg-2 hover:bg-hover")}>
            <TriangleAlert className="size-3.5" strokeWidth={1.8} />出错 <span className="font-mono tnum">{errCount}</span>
          </button>
          {!!rejCount && (
            <button onClick={() => setHideRej(!hideRej)} title="按规则拦截的连接是预期行为（如订阅 ipv6: false 拒绝 IPv6）"
              className={cx("inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-[12px] transition-colors",
                hideRej ? "border-line-strong bg-active text-fg" : "border-line text-fg-2 hover:bg-hover")}>
              <Ban className="size-3.5" strokeWidth={1.8} />{hideRej ? "已隐藏拦截" : "隐藏拦截"} <span className="font-mono tnum">{rejCount}</span>
            </button>
          )}
          <button onClick={() => setOnlyActive(!onlyActive)} title="只看正在传输的连接"
            className={cx("inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-[12px] transition-colors",
              onlyActive ? "border-accent/40 bg-accent-soft text-fg" : "border-line text-fg-2 hover:bg-hover")}>
            <Activity className="size-3.5" strokeWidth={1.8} />活动 <span className="font-mono tnum">{active.length}</span>
          </button>
          <div className="ml-auto flex shrink-0 items-center gap-3 whitespace-nowrap font-mono text-[12px] tnum" title="经过 Velum 的实时总速率">
            <span className="inline-flex items-center gap-1 text-fg"><ArrowDown className="size-3.5 text-accent" strokeWidth={2} />{traffic ? rate(traffic.down_rate) : "—"}</span>
            <span className="inline-flex items-center gap-1 text-fg-2"><ArrowUp className="size-3.5 text-fg-3" strokeWidth={2} />{traffic ? rate(traffic.up_rate) : "—"}</span>
          </div>
          <IconButton icon={paused ? Play : Pause} label={paused ? "继续实时更新" : "暂停实时更新"} onClick={() => setPaused(!paused)} className={paused ? "text-warn" : undefined} />
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {!shown.length ? (
            <Empty icon={ArrowLeftRight} title={all.length ? "没有匹配的连接" : onlyActive ? "当前没有活动连接" : "还没有连接"}
              hint={all.length ? "换个关键词，或关闭「出错」「隐藏拦截」筛选" : "经 Velum 访问任意网站后，连接会实时出现在这里"} />
          ) : (
            <table className="w-full table-fixed text-[12.5px]">
              <thead className="sticky top-0 z-10 bg-bg text-left text-[11.5px] text-fg-3">
                <tr className="[&>th]:h-8 [&>th]:border-b [&>th]:border-line [&>th]:px-3 [&>th]:font-medium">
                  <th className="w-[88px] !pl-4">时间</th>
                  <th>目标</th>
                  {hasProc && <th className="w-[110px]">进程</th>}
                  <th className="w-[32%]">规则与出站</th>
                  <th className="w-[84px] text-right">速率</th>
                  <th className={cx("w-[68px] text-right", cur && "!pr-4")}>下行</th>
                  {!cur && <th className="w-[72px] !pr-4 text-right">首包</th>}
                </tr>
              </thead>
              <tbody>
                {shown.map((c) => {
                  const bad = !!c.errors?.length && !isRejected(c);
                  const sp = speeds.get(c.id);
                  const live = !c.end;
                  return (
                    <tr key={c.id} onClick={() => setSel(c)}
                      className={cx("h-9 border-b border-line transition-colors [&>td]:px-3", sel?.id === c.id ? "bg-active" : "hover:bg-hover")}>
                      <td className="!pl-4 whitespace-nowrap font-mono text-[11.5px] text-fg-3 tnum">
                        <span className={cx("mr-1.5 inline-block size-1.5 rounded-full align-middle", live ? "bg-accent" : "bg-transparent")} title={live ? "进行中" : undefined} />
                        {new Date(c.start).toLocaleTimeString([], { hour12: false })}
                      </td>
                      <td className="truncate">
                        <span className={cx("font-medium", bad && "text-err")}>{c.domain || c.destination}</span>
                      </td>
                      {hasProc && <td className="truncate text-fg-3">{c.process?.split(/[\\/]/).pop() ?? "—"}</td>}
                      <td className="overflow-hidden"><Chain size="sm" className="overflow-hidden" steps={chainSteps(c.chain, { label: ruleLabel(c), title: srcText(c.provenance) })} /></td>
                      <td className="text-right font-mono text-[11.5px] tnum" title={sp ? `↓ ${rate(sp.down)}  ↑ ${rate(sp.up)}` : undefined}>
                        {live && sp && sp.down + sp.up >= 1 ? <span className={sp.down >= 1 << 20 ? "text-accent" : "text-fg"}>{rate(Math.max(sp.down, sp.up))}</span> : <span className="text-fg-3">—</span>}
                      </td>
                      <td className={cx("text-right font-mono text-[11.5px] text-fg-2 tnum", cur && "!pr-4")}>{human(c.download)}</td>
                      {!cur && <td className="!pr-4 whitespace-nowrap text-right font-mono text-[11.5px] text-fg-3 tnum">{c.first_byte_ms >= 0 ? `${c.first_byte_ms} ms` : "—"}</td>}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {cur && (
        <aside className="flex w-[340px] shrink-0 flex-col border-l border-line bg-panel">
          <div className="flex items-start gap-2 border-b border-line px-4 pt-1 pb-3">
            <div className="min-w-0 flex-1">
              <div className="selectable truncate text-[14px] font-semibold" title={cur.domain || cur.destination}>{cur.domain || cur.destination}</div>
              <div className="mt-0.5 font-mono text-[11.5px] text-fg-3 tnum">{new Date(cur.start).toLocaleString([], { hour12: false })} · {cur.network.toUpperCase()}</div>
            </div>
            <IconButton icon={X} label="关闭" onClick={() => setSel(undefined)} />
          </div>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
            <div>
              <div className="mb-1.5 text-[11.5px] text-fg-3">路由</div>
              <Chain className="flex-wrap" steps={chainSteps(cur.chain, { label: ruleLabel(sel) })} />
              <div className="selectable mt-2 rounded-md bg-hover px-2.5 py-2 font-mono text-[11.5px] leading-5 break-all text-fg-2">{srcText(cur.provenance) || "未命中任何规则，走最终出站"}</div>
            </div>
            <KV rows={[
              ["目标", <span className="selectable font-mono text-[12px] break-all">{cur.destination}</span>],
              ...(cur.resolved?.length ? [["解析", <span className="selectable font-mono text-[12px] break-all">{cur.resolved.join(", ")}</span>] as [string, React.ReactNode]] : []),
              ["进程", <span className="selectable break-all">{cur.process ?? "—"}</span>],
              ["流量", <span className="font-mono text-[12px] tnum">↑ {human(cur.upload)}　↓ {human(cur.download)}</span>],
              ...(!cur.end ? [["速率", <span className="font-mono text-[12px] tnum">↑ {rate(speeds.get(cur.id)?.up ?? 0)}　↓ {rate(speeds.get(cur.id)?.down ?? 0)}</span>] as [string, React.ReactNode]] : []),
              ["首包", <span className="font-mono text-[12px] tnum">{cur.first_byte_ms >= 0 ? `${cur.first_byte_ms} ms` : "无"}</span>],
              ["时长", <span className="font-mono text-[12px] tnum">{cur.end ? `${cur.duration_ms} ms` : "进行中"}</span>],
              ...(cur.sniffed ? [["嗅探", cur.sniffed] as [string, React.ReactNode]] : []),
            ]} />
            {isRejected(sel) ? (
              <Tag>按规则拦截，属于预期行为</Tag>
            ) : (
              !!cur.errors?.length && (
                <div className="space-y-1.5">
                  {cur.errors.map((e) => <div key={e} className="selectable rounded-md bg-err-soft px-2.5 py-2 font-mono text-[11.5px] break-all text-err">{e}</div>)}
                </div>
              )
            )}
          </div>
          <div className="flex gap-2 border-t border-line p-3">
            <Button className="flex-1" icon={ListPlus} onClick={() => setRule(sel)}>加入本地规则</Button>
            <Button className="flex-1" icon={ScanSearch} onClick={() => go("diagnose", cur.domain || cur.destination)}>诊断</Button>
          </div>
        </aside>
      )}
      {rule && <RuleDialog initial={rule.domain || rule.destination} process={rule.process} refresh={refresh} onClose={() => setRule(undefined)} />}
    </div>
  );
}
