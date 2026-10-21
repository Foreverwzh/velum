import { useEffect, useMemo, useState } from "react";
import { ArrowLeftRight, Pause, Play, ScanSearch, TriangleAlert, X } from "lucide-react";
import { api, human, srcText, subscribeConnections, type Connection } from "../api";
import type { Go } from "../App";
import { Button, Chain, Empty, IconButton, KV, SearchInput, Tag, chainSteps, cx } from "../components/ui";

/** 按规则拦截（REJECT 出站或 reject 动作），其报错是预期行为。 */
export function isRejected(c: Connection) {
  return c.chain.length === 1 && (c.chain[0] === "REJECT" || c.chain[0].startsWith("reject"));
}

const ruleLabel = (c: Connection) => (c.rule_index >= 0 ? `#${c.rule_index}` : "final");

export default function Connections({ go, initialQuery }: { go: Go; initialQuery?: string }) {
  const [list, setList] = useState<Connection[]>([]);
  const [q, setQ] = useState(initialQuery ?? "");
  const [sel, setSel] = useState<Connection>();
  const [paused, setPaused] = useState(false);
  const [onlyErr, setOnlyErr] = useState(false);

  useEffect(() => {
    api.connections(300).then((r) => setList(r.connections ?? [])).catch(() => undefined);
  }, []);
  useEffect(() => {
    if (paused) return;
    return subscribeConnections((c) => setList((xs) => [c, ...xs.filter((x) => x.id !== c.id)].slice(0, 500)));
  }, [paused]);

  const shown = useMemo(() => {
    const keys = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return list.filter((c) => {
      if (onlyErr && !(c.errors?.length && !isRejected(c))) return false;
      if (!keys.length) return true;
      const hay = [c.domain, c.destination, c.process, c.chain.join(" "), c.provenance.text, ruleLabel(c)].join(" ").toLowerCase();
      return keys.every((k) => hay.includes(k));
    });
  }, [list, q, onlyErr]);
  const errCount = useMemo(() => list.filter((c) => c.errors?.length && !isRejected(c)).length, [list]);
  // 拿不到进程信息时（如部分 TUN 连接）不占一列
  const hasProc = useMemo(() => shown.some((c) => c.process), [shown]);

  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-2 border-b border-line px-4 pt-1 pb-2.5">
          <SearchInput value={q} onChange={(e) => setQ(e.target.value)} placeholder="按域名、进程、节点或规则过滤" className="w-[320px]" />
          <button onClick={() => setOnlyErr(!onlyErr)}
            className={cx("inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 text-[12px] transition-colors",
              onlyErr ? "border-err/40 bg-err-soft text-err" : "border-line text-fg-2 hover:bg-hover")}>
            <TriangleAlert className="size-3.5" strokeWidth={1.8} />出错 <span className="font-mono tnum">{errCount}</span>
          </button>
          <span className="ml-auto text-[12px] text-fg-3 tnum">{shown.length} 条</span>
          <IconButton icon={paused ? Play : Pause} label={paused ? "继续实时更新" : "暂停实时更新"} onClick={() => setPaused(!paused)} className={paused ? "text-warn" : undefined} />
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {!shown.length ? (
            <Empty icon={ArrowLeftRight} title={list.length ? "没有匹配的连接" : "还没有连接"} hint={list.length ? "换个关键词，或关闭「出错」筛选" : "经 Velum 访问任意网站后，连接会实时出现在这里"} />
          ) : (
            <table className="w-full table-fixed text-[12.5px]">
              <thead className="sticky top-0 z-10 bg-bg text-left text-[11.5px] text-fg-3">
                <tr className="[&>th]:h-8 [&>th]:border-b [&>th]:border-line [&>th]:px-3 [&>th]:font-medium">
                  <th className="w-[76px] !pl-4">时间</th>
                  <th>目标</th>
                  {hasProc && <th className="w-[110px]">进程</th>}
                  <th className="w-[36%]">规则与出站</th>
                  <th className="w-[68px] text-right">下行</th>
                  <th className="w-[72px] !pr-4 text-right">首包</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((c) => {
                  const bad = !!c.errors?.length && !isRejected(c);
                  return (
                    <tr key={c.id} onClick={() => setSel(c)}
                      className={cx("h-9 border-b border-line transition-colors [&>td]:px-3", sel?.id === c.id ? "bg-active" : "hover:bg-hover")}>
                      <td className="!pl-4 font-mono text-[11.5px] text-fg-3 tnum">{new Date(c.start).toLocaleTimeString([], { hour12: false })}</td>
                      <td className="truncate">
                        <span className={cx("font-medium", bad && "text-err")}>{c.domain || c.destination}</span>
                      </td>
                      {hasProc && <td className="truncate text-fg-3">{c.process?.split(/[\\/]/).pop() ?? "—"}</td>}
                      <td><Chain size="sm" steps={chainSteps(c.chain, { label: ruleLabel(c), title: srcText(c.provenance) })} /></td>
                      <td className="text-right font-mono text-[11.5px] text-fg-2 tnum">{human(c.download)}</td>
                      <td className="!pr-4 text-right font-mono text-[11.5px] text-fg-3 tnum">{c.first_byte_ms >= 0 ? `${c.first_byte_ms} ms` : "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {sel && (
        <aside className="flex w-[340px] shrink-0 flex-col border-l border-line bg-panel">
          <div className="flex items-start gap-2 border-b border-line px-4 pt-1 pb-3">
            <div className="min-w-0 flex-1">
              <div className="selectable truncate text-[14px] font-semibold" title={sel.domain || sel.destination}>{sel.domain || sel.destination}</div>
              <div className="mt-0.5 font-mono text-[11.5px] text-fg-3 tnum">{new Date(sel.start).toLocaleString([], { hour12: false })} · {sel.network.toUpperCase()}</div>
            </div>
            <IconButton icon={X} label="关闭" onClick={() => setSel(undefined)} />
          </div>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
            <div>
              <div className="mb-1.5 text-[11.5px] text-fg-3">路由</div>
              <Chain className="flex-wrap" steps={chainSteps(sel.chain, { label: ruleLabel(sel) })} />
              <div className="selectable mt-2 rounded-md bg-hover px-2.5 py-2 font-mono text-[11.5px] leading-5 break-all text-fg-2">{srcText(sel.provenance) || "未命中任何规则，走最终出站"}</div>
            </div>
            <KV rows={[
              ["目标", <span className="selectable font-mono text-[12px] break-all">{sel.destination}</span>],
              ...(sel.resolved?.length ? [["解析", <span className="selectable font-mono text-[12px] break-all">{sel.resolved.join(", ")}</span>] as [string, React.ReactNode]] : []),
              ["进程", <span className="selectable break-all">{sel.process ?? "—"}</span>],
              ["流量", <span className="font-mono text-[12px] tnum">↑ {human(sel.upload)}　↓ {human(sel.download)}</span>],
              ["首包", <span className="font-mono text-[12px] tnum">{sel.first_byte_ms >= 0 ? `${sel.first_byte_ms} ms` : "无"}</span>],
              ["时长", <span className="font-mono text-[12px] tnum">{sel.duration_ms} ms</span>],
              ...(sel.sniffed ? [["嗅探", sel.sniffed] as [string, React.ReactNode]] : []),
            ]} />
            {isRejected(sel) ? (
              <Tag>按规则拦截，属于预期行为</Tag>
            ) : (
              !!sel.errors?.length && (
                <div className="space-y-1.5">
                  {sel.errors.map((e) => <div key={e} className="selectable rounded-md bg-err-soft px-2.5 py-2 font-mono text-[11.5px] break-all text-err">{e}</div>)}
                </div>
              )
            )}
          </div>
          <div className="border-t border-line p-3">
            <Button className="w-full" icon={ScanSearch} onClick={() => go("diagnose", sel.domain || sel.destination)}>诊断这个目标</Button>
          </div>
        </aside>
      )}
    </div>
  );
}
