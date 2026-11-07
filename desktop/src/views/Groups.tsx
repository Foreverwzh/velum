import { useMemo, useRef, useState, useEffect } from "react";
import { ArrowDownWideNarrow, ArrowLeft, Check, Gauge, Layers, ListOrdered } from "lucide-react";
import { api, type GroupStatus, type NetPolicy, type Status } from "../api";
import { delayOf, leafOf, pathOf, useAction } from "../hooks";
import type { Refresh } from "../App";
import { Button, Chain, Empty, IconButton, Latency, Notice, SearchInput, Tag, cx } from "../components/ui";
import { t } from "../i18n";

/** 策略组：左侧组列表（可搜索组名与节点名），右侧当前组的成员与延迟。 */
export default function Groups({ status, refresh, focus }: { status: Status; refresh: Refresh; focus?: string }) {
  const groups = status.groups ?? [];
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<string | undefined>(() => (focus && groups.some((g) => g.tag === focus) ? focus : groups[0]?.tag));
  const [byDelay, setByDelay] = useState(false);
  // 手机上一次只显示一层：组列表或组内成员
  const [open, setOpen] = useState(() => !!focus);
  const { busy, error, run } = useAction(refresh);
  const search = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (focus && groups.some((g) => g.tag === focus)) {
      setSel(focus);
      setOpen(true);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f") {
        e.preventDefault();
        search.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const keys = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (s: string) => keys.every((k) => s.toLowerCase().includes(k));
  const visible = useMemo(
    () => (keys.length ? groups.filter((g) => hit(g.tag) || g.members.some(hit)) : groups),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [groups, q],
  );
  const current = groups.find((g) => g.tag === sel) ?? visible[0];

  if (!groups.length) return <Empty icon={Layers} title={t("订阅中没有策略组")} hint={t("订阅只提供了节点时，所有流量按规则走默认出站")} />;

  return (
    <div className="flex min-h-0 flex-1">
      <div className={cx("flex w-[264px] shrink-0 flex-col border-r border-line phone:w-full phone:border-r-0", open && "phone:hidden")}>
        <div className="px-3 pt-1 pb-2">
          <SearchInput ref={search} value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("搜索策略组或节点")} shortcut={`${/Mac/.test(navigator.userAgent) ? "⌘" : "Ctrl+"}F`} />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-3">
          {!visible.length && <div className="px-3 py-6 text-center text-[12px] text-fg-3">{t("没有匹配「")}{q}{t("」的策略组或节点")}</div>}
          {visible.map((g) => {
            const on = current?.tag === g.tag;
            const leaf = leafOf(groups, g.tag);
            return (
              <button key={g.tag} onClick={() => { setSel(g.tag); setOpen(true); }}
                className={cx("flex w-full flex-col gap-0.5 rounded-md px-2.5 py-1.5 text-left transition-colors phone:py-2.5", on ? "bg-active" : "hover:bg-hover")}>
                <span className="flex w-full items-center gap-2">
                  <span className={cx("min-w-0 flex-1 truncate", on && "font-medium")}>{g.tag}</span>
                  {g.type !== "select" && <Tag>{t("自动")}</Tag>}
                </span>
                <span className="flex w-full items-center gap-2 text-[11.5px] text-fg-3">
                  <span className="min-w-0 flex-1 truncate">{leaf}</span>
                  <Latency ms={delayOf(groups, leaf)} />
                </span>
              </button>
            );
          })}
        </div>
      </div>

      {current && (
        <GroupDetail
          key={current.tag}
          group={current}
          className={open ? undefined : "phone:hidden"}
          onBack={() => setOpen(false)}
          hint={status.network?.hint}
          groups={groups}
          filter={keys.length && !hit(current.tag) ? hit : undefined}
          byDelay={byDelay}
          setByDelay={setByDelay}
          busy={busy}
          error={error}
          onTest={() => run("test", () => api.test(current.tag))}
          onSelect={(m) => run("select", () => api.select(current.tag, m))}
        />
      )}
    </div>
  );
}

function GroupDetail({ className, onBack, group: g, groups, hint, filter, byDelay, setByDelay, busy, error, onTest, onSelect }: {
  className?: string; onBack: () => void; group: GroupStatus; groups: GroupStatus[]; hint?: { kind: string; ssid?: string }; filter?: (s: string) => boolean; byDelay: boolean; setByDelay: (v: boolean) => void;
  busy?: string; error?: string; onTest: () => void; onSelect: (m: string) => void;
}) {
  const manual = g.type === "select";
  const groupTags = new Set(groups.map((x) => x.tag));
  const path = pathOf(groups, g.tag);
  let members = g.members.map((m) => {
    const leaf = groupTags.has(m) ? leafOf(groups, m) : m;
    return { m, leaf, nested: groupTags.has(m), delay: delayOf(groups, leaf) };
  });
  if (filter) members = members.filter((x) => filter(x.m) || filter(x.leaf));
  if (byDelay) {
    const key = (d?: number) => (d === undefined || d === 0 ? 1e9 : d < 0 ? 1e8 : d);
    members = [...members].sort((a, b) => key(a.delay) - key(b.delay));
  }
  const tested = g.members.filter((m) => delayOf(groups, groupTags.has(m) ? leafOf(groups, m) : m) !== undefined).length;

  return (
    <div className={cx("flex min-w-0 flex-1 flex-col", className)}>
      <header className="flex items-start justify-between gap-4 border-b border-line px-6 pt-1 pb-3 phone:gap-2 phone:px-2">
        <IconButton icon={ArrowLeft} label={t("返回")} onClick={onBack} className="hidden shrink-0 phone:inline-flex" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{g.tag}</h1>
            <Tag tone={manual ? "neutral" : "info"}>{manual ? t("手动选择") : t("自动选择")}</Tag>
          </div>
          <div className="mt-1.5 flex items-center gap-2 text-[12px] text-fg-3">
            <span className="shrink-0">{t("当前")}</span>
            <Chain size="sm" steps={path.slice(1).map((p, i, a) => ({ label: p, kind: p === "DIRECT" ? "direct" : i === a.length - 1 ? "node" : "group" }))} />
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <IconButton icon={byDelay ? ArrowDownWideNarrow : ListOrdered} label={byDelay ? t("按延迟排序（点击恢复订阅顺序）") : t("订阅顺序（点击按延迟排序）")}
            onClick={() => setByDelay(!byDelay)} className={byDelay ? "text-accent" : undefined} />
          <Button icon={Gauge} loading={busy === "test"} onClick={onTest}>{t("测速")}</Button>
        </div>
      </header>
      {error && <div className="px-6 pt-3 phone:px-4"><Notice tone="err">{error}</Notice></div>}
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2">
        {!manual && <div className="px-3 pb-2 text-[12px] text-fg-3">{t("自动选择延迟最低的成员，不能手动切换。")}</div>}
        {g.policy && <PolicyNote policy={g.policy} hint={hint} />}
        {!members.length && <div className="px-3 py-6 text-center text-[12px] text-fg-3">{t("这个策略组里没有匹配的节点")}</div>}
        {members.map(({ m, leaf, nested, delay }) => {
          const on = m === g.selected;
          return (
            <button key={m} disabled={!manual || !!busy} onClick={() => !on && onSelect(m)}
              className={cx(
                "group grid h-9 w-full grid-cols-[20px_1fr_auto] items-center gap-2 rounded-md px-3 text-left transition-colors phone:h-12",
                on ? "bg-accent-soft" : manual && "hover:bg-hover",
                !manual && "cursor-default",
              )}>
              {on ? <Check className="size-3.5 text-accent" strokeWidth={2.5} /> : <span className="size-1.5 justify-self-center rounded-full bg-line-strong opacity-0 transition-opacity group-hover:opacity-100" />}
              <span className="flex min-w-0 items-center gap-1.5">
                <span className={cx("truncate", on && "font-medium")}>{m}</span>
                {nested && <span className="truncate text-[12px] text-fg-3">→ {leaf}</span>}
              </span>
              <Latency ms={delay} />
            </button>
          );
        })}
      </div>
      <footer className="border-t border-line px-6 py-2 phone:px-4 text-[11.5px] text-fg-3 tnum">
        {g.members.length}{" "}{t("个成员 ·")}{" "}{tested}{" "}{t("个有测速结果")}{filter && t(" · 已按搜索过滤")}
      </footer>
    </div>
  );
}

/** 这个组会随网络自动切换：说清楚规则；读不到 Wi-Fi 名称时明确提示，否则用户只会看到「没切换」。 */
function PolicyNote({ policy, hint }: { policy: NetPolicy; hint?: { kind: string; ssid?: string } }) {
  const name = (m: string) => (m === "DIRECT" ? t("直连") : m);
  const rows: [string, string][] = Object.entries(policy.ssid ?? {}).map(([ssid, m]) => [ssid, name(m)]);
  if (policy.cellular) rows.push([t("蜂窝网络"), name(policy.cellular)]);
  if (policy.default) rows.push([t("其他网络"), name(policy.default)]);
  return (
    <div className="mx-3 mb-3 rounded-lg border border-line bg-panel px-3 py-2.5 text-[12px]">
      <div className="mb-1.5 font-medium">{t("按网络自动切换")}</div>
      <div className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 text-fg-2">
        {rows.map(([k, v]) => (
          <div key={k} className="contents"><span className="min-w-0 truncate">{k}</span><span className="text-right">{v}</span></div>
        ))}
      </div>
      <div className="mt-1.5 text-fg-3">{t("手动选择保持到下一次换网。")}</div>
      {hint?.kind === "wifi" && !hint.ssid && (
        <div className="mt-2"><Notice tone="warn">{t("读不到 Wi-Fi 名称，暂时不会自动切换。macOS：在系统设置的“隐私与安全性 → 定位服务”里允许 Velum。Windows：在“隐私和安全性 → 位置”里允许桌面应用访问位置。")}</Notice></div>
      )}
    </div>
  );
}
