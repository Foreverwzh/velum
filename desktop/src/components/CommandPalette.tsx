import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowRight, Check, CornerDownLeft, Gauge, RefreshCw, ScanSearch, Shield, Waypoints, Wrench, Zap, type LucideIcon } from "lucide-react";
import { api, errText, type Status } from "../api";
import { delayOf, leafOf } from "../hooks";
import type { Go, Page, Refresh } from "../App";
import { Kbd, Latency, cx } from "./ui";

interface Item {
  id: string;
  section: string;
  icon: LucideIcon;
  title: ReactNode;
  /** 用于搜索匹配的文本 */
  text: string;
  hint?: ReactNode;
  current?: boolean;
  run: () => Promise<unknown> | void;
}

const pages: { page: Page; label: string }[] = [
  { page: "overview", label: "概览" },
  { page: "groups", label: "策略组" },
  { page: "connections", label: "连接" },
  { page: "diagnose", label: "诊断" },
  { page: "overlay", label: "本地覆盖" },
  { page: "warnings", label: "告警" },
  { page: "settings", label: "设置" },
];

/** Raycast 式命令面板：搜索并切换任意策略组的节点、开关代理、诊断一个地址、跳转页面。 */
export function CommandPalette({ status, refresh, go, onClose }: { status: Status | null; refresh: Refresh; go: Go; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [idx, setIdx] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();
  const list = useRef<HTMLDivElement>(null);

  const items = useMemo<Item[]>(() => {
    const out: Item[] = [];
    const groups = status?.groups ?? [];
    const query = q.trim();
    if (status) {
      const sw = (which: "system_proxy" | "tun", want: boolean, label: string) =>
        out.push({
          id: `sw-${which}`, section: "操作", icon: Shield, title: `${want ? "关闭" : "开启"}${label}`, text: `${label} 开关 ${which}`,
          run: () => api.setSwitch(which, want ? "off" : "on"),
        });
      sw("system_proxy", status.want_system_proxy, "系统代理");
      sw("tun", status.want_tun, "TUN");
      out.push({ id: "repair", section: "操作", icon: Wrench, title: "一键修复", text: "一键修复 repair", run: api.repair });
      out.push({ id: "update", section: "操作", icon: RefreshCw, title: "更新订阅", text: "更新订阅 update", run: api.update });
      out.push({ id: "test", section: "操作", icon: Gauge, title: "测速全部节点", text: "测速 延迟 test", run: () => api.test("") });
    }
    if (query && /[.:/]/.test(query)) {
      out.unshift({ id: "why", section: "诊断", icon: ScanSearch, title: <>诊断 <span className="font-mono">{query}</span></>, text: query, run: () => go("diagnose", query) });
    }
    for (const g of groups) {
      out.push({
        id: `g-${g.tag}`, section: "策略组", icon: Waypoints, title: g.tag, text: `${g.tag} ${g.selected}`,
        hint: <span className="truncate">{leafOf(groups, g.tag)}</span>, run: () => go("groups", g.tag),
      });
    }
    for (const g of groups.filter((g) => g.type === "select")) {
      for (const m of g.members) {
        const current = m === g.selected;
        out.push({
          id: `n-${g.tag}-${m}`, section: "切换节点", icon: current ? Check : Zap, current,
          title: <><span className="text-fg-3">{g.tag}</span><ArrowRight className="mx-1 inline size-3 text-fg-3" />{m}</>,
          text: `${g.tag} ${m}`, hint: <Latency ms={delayOf(groups, leafOf(groups, m))} />,
          run: () => api.select(g.tag, m),
        });
      }
    }
    for (const p of pages) out.push({ id: `p-${p.page}`, section: "页面", icon: ArrowRight, title: `前往${p.label}`, text: p.label, run: () => go(p.page) });

    if (!query) return out.filter((i) => i.section !== "切换节点" && i.section !== "策略组").concat(out.filter((i) => i.section === "策略组"));
    // 空格分隔的关键词都要命中（例如「ai 板橋」）
    const keys = query.toLowerCase().split(/\s+/);
    return out.filter((i) => i.section === "诊断" || keys.every((k) => i.text.toLowerCase().includes(k)));
  }, [q, status, go]);

  useEffect(() => setIdx(0), [q]);
  useEffect(() => {
    list.current?.querySelector(`[data-idx="${idx}"]`)?.scrollIntoView({ block: "nearest" });
  }, [idx]);

  const exec = async (it: Item | undefined) => {
    if (!it || busy) return;
    setBusy(true);
    setErr(undefined);
    try {
      await it.run();
      await refresh();
      onClose();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setIdx((i) => Math.min(i + 1, items.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setIdx((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      void exec(items[idx]);
    } else if (e.key === "Escape") {
      onClose();
    }
  };

  let lastSection = "";
  return (
    <div className="fixed inset-0 z-50 flex justify-center bg-black/25 pt-[12vh] backdrop-blur-[2px] animate-[fade_120ms_ease-out]" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-label="命令面板"
        onMouseDown={(e) => e.stopPropagation()}
        className="flex h-fit max-h-[min(520px,76vh)] w-[min(620px,calc(100vw-48px))] flex-col overflow-hidden rounded-xl border border-line-strong bg-raised shadow-pop"
      >
        <div className="flex h-12 items-center gap-3 border-b border-line px-4">
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={onKey}
            placeholder="搜索节点、策略组或操作；输入网址可直接诊断"
            spellCheck={false}
            className="min-w-0 flex-1 bg-transparent text-[14px] outline-none placeholder:text-fg-3"
          />
          <Kbd>esc</Kbd>
        </div>
        <div ref={list} className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {!items.length && <div className="px-3 py-8 text-center text-[12.5px] text-fg-3">没有匹配的节点或操作</div>}
          {items.map((it, i) => {
            const header = it.section !== lastSection ? it.section : undefined;
            lastSection = it.section;
            return (
              <div key={it.id}>
                {header && <div className="px-2.5 pt-2.5 pb-1 text-[11px] font-medium text-fg-3">{header}</div>}
                <button
                  data-idx={i}
                  onMouseMove={() => setIdx(i)}
                  onClick={() => void exec(it)}
                  className={cx("flex h-9 w-full items-center gap-3 rounded-md px-2.5 text-left text-[13px]", i === idx ? "bg-active" : "")}
                >
                  <it.icon className={cx("size-4 shrink-0", it.current ? "text-accent" : "text-fg-3")} strokeWidth={1.8} />
                  <span className="min-w-0 flex-1 truncate">{it.title}</span>
                  {it.hint && <span className="flex max-w-[40%] shrink-0 items-center text-[12px] text-fg-3">{it.hint}</span>}
                  {i === idx && <CornerDownLeft className="size-3.5 shrink-0 text-fg-3" strokeWidth={2} />}
                </button>
              </div>
            );
          })}
        </div>
        {(err || busy) && (
          <div className={cx("border-t border-line px-4 py-2 text-[12px]", err ? "selectable text-err" : "text-fg-3")}>{err ?? "正在执行…"}</div>
        )}
      </div>
    </div>
  );
}
