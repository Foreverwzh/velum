import { useState } from "react";
import { api, type GroupStatus, type RepairStep, type Status } from "../api";
import { health } from "../health";
import { Badge, Button, Card, Delay, Dot, Toggle, cx, titlebarOverlay } from "../components/ui";
import { SubscriptionForm } from "../components/SubscriptionForm";
import { TunToggle } from "../components/TunToggle";
import type { Refresh } from "../App";

export default function Simple({ status, error, refresh, onPro }: { status: Status | null; error?: string; refresh: Refresh; onPro: () => void }) {
  const h = health(status, error);
  const ready = !!status && !status.starting;
  const [busy, setBusy] = useState<string>();
  const [msg, setMsg] = useState<string>();
  const [steps, setSteps] = useState<RepairStep[]>();

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setMsg(undefined);
    try {
      await fn();
      await refresh();
    } catch (e) {
      setMsg(String((e as Error).message ?? e));
    } finally {
      setBusy(undefined);
    }
  };

  const [failed, setFailed] = useState<Record<string, boolean>>({});
  const selectable = status?.groups?.filter((g) => g.type === "select") ?? [];

  return (
    <div className={cx("mx-auto flex h-full max-w-[520px] flex-col gap-3 overflow-auto p-5", titlebarOverlay && "pt-9")}>
      <header className="flex items-center justify-between">
        <div className="text-[15px] font-semibold tracking-tight">Velum</div>
        <Button variant="ghost" onClick={onPro}>专业模式 →</Button>
      </header>

      {status && !status.starting && !status.subscription?.source && (
        <Card title="欢迎使用 Velum">
          <p className="mb-3 text-neutral-500">填写订阅地址即可开始使用。订阅地址由提供订阅的人给你。</p>
          <SubscriptionForm onDone={refresh} autoFocus />
        </Card>
      )}

      <Card>
        <div className="flex items-start gap-4">
          <div className={cx("mt-1 flex h-12 w-12 shrink-0 items-center justify-center rounded-full",
            { green: "bg-emerald-50 dark:bg-emerald-950", amber: "bg-amber-50 dark:bg-amber-950", red: "bg-red-50 dark:bg-red-950", gray: "bg-neutral-100 dark:bg-neutral-800" }[h.tone])}>
            <Dot tone={h.tone} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[20px] font-semibold tracking-tight">{h.title}</div>
            <div className="selectable mt-0.5 text-neutral-500">{h.detail}</div>
          </div>
        </div>
        <div className="mt-3 divide-y divide-neutral-100 border-t border-neutral-100 dark:divide-neutral-800 dark:border-neutral-800">
          <Toggle label="系统代理" hint="浏览器等遵守系统代理的应用经 Velum" disabled={!ready || !!busy}
            on={!!status?.system_proxy_on} want={!!status?.want_system_proxy} paused={status?.network?.paused}
            onChange={(v) => run("sys", () => api.setSwitch("system_proxy", v ? "on" : "off"))} />
          <TunToggle status={status} disabled={!ready || !!busy} onDone={refresh} />
        </div>
        {status?.overridden && <div className="mt-1 text-[11px] text-neutral-400">开关由界面设置，覆盖配置文件；重启后保持</div>}
      </Card>

      {status?.network && (
        <Card title="网络环境">
          <dl className="grid grid-cols-[88px_1fr] gap-y-1.5">
            <dt className="text-neutral-500">主接口</dt>
            <dd>{status.network.primary_interface || "—"}</dd>
            <dt className="text-neutral-500">连通性</dt>
            <dd>{{ online: <Badge tone="green">正常</Badge>, captive: <Badge tone="amber">需要网页认证</Badge>, offline: <Badge tone="red">离线</Badge> }[status.network.captive ?? ""] ?? "检测中…"}</dd>
            {status.network.vpns?.map((v) => (
              <div key={v.iface} className="contents">
                <dt className="text-neutral-500">其他 VPN</dt>
                <dd>{v.full ? `其他代理 / VPN 正在接管全部流量（${v.iface}）` : `公司 / 内网 VPN（${v.iface}），其网段与域名会直连`}</dd>
              </div>
            ))}
            {!!status.network.split_dns_domains?.length && (
              <>
                <dt className="text-neutral-500">DNS 分流</dt>
                <dd className="selectable truncate" title={status.network.split_dns_domains.join("、")}>{status.network.split_dns_domains.join("、")}</dd>
              </>
            )}
          </dl>
        </Card>
      )}

      {selectable.length > 0 && (
        <Card
          title="分流"
          actions={
            <Button variant="ghost" disabled={!!busy} onClick={() => run("test", async () => {
              const r = await api.test("");
              setFailed(Object.fromEntries(Object.entries(r.delays).filter(([, v]) => v < 0).map(([k]) => [k, true])));
            })}>
              {busy === "test" ? "测速中…" : "测速"}
            </Button>
          }
        >
          <Groups groups={status!.groups!} selectable={selectable} failed={failed} disabled={!!busy}
            onSelect={(g, m) => run("select", () => api.select(g, m))} />
        </Card>
      )}

      <div className="flex gap-2">
        <Button variant="primary" disabled={!ready || !!busy} onClick={() => run("repair", async () => setSteps((await api.repair()).steps))}>
          {busy === "repair" ? "修复中…" : "一键修复"}
        </Button>
        <Button disabled={!ready || !!busy} onClick={() => run("update", api.update)}>{busy === "update" ? "更新中…" : "更新订阅"}</Button>
      </div>
      {msg && <div className="selectable rounded-md bg-red-50 px-3 py-2 text-red-700 dark:bg-red-950 dark:text-red-300">{msg}</div>}
      {steps && (
        <Card title="修复结果">
          <ul className="space-y-1">
            {steps.map((s) => (
              <li key={s.name} className="flex gap-2">
                <span className={s.ok ? "text-emerald-600" : "text-red-600"}>{s.ok ? "✓" : "✗"}</span>
                <span>{s.name}</span>
                {s.detail && <span className="selectable text-neutral-500">{s.detail}</span>}
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}

/** 每个可选策略组一行：当前选择 → 实际节点 + 延迟；点开在行下展开成员列表。 */
function Groups({ groups, selectable, failed, disabled, onSelect }: {
  groups: GroupStatus[]; selectable: GroupStatus[]; failed: Record<string, boolean>; disabled: boolean;
  onSelect: (group: string, member: string) => void;
}) {
  const [open, setOpen] = useState<string | undefined>(() => new URLSearchParams(location.search).get("open") ?? undefined);
  const byTag = new Map(groups.map((g) => [g.tag, g]));
  const leafOf = (tag: string) => {
    let cur = tag;
    for (let i = 0; i < 8; i++) {
      const g = byTag.get(cur);
      if (!g?.selected) break;
      cur = g.selected;
    }
    return cur;
  };
  const delayOf = (tag: string): number | undefined => {
    if (failed[tag]) return -1;
    for (const g of groups) if (g.delays?.[tag]) return g.delays[tag];
    return undefined;
  };

  return (
    <ul className="-my-1 divide-y divide-neutral-100 dark:divide-neutral-800">
      {selectable.map((g) => {
        const leaf = leafOf(g.tag);
        const expanded = open === g.tag;
        return (
          <li key={g.tag}>
            <button className="flex w-full items-center gap-3 py-2 text-left" onClick={() => setOpen(expanded ? undefined : g.tag)}>
              <span className="w-[120px] shrink-0 truncate font-medium" title={g.tag}>{g.tag}</span>
              <span className="min-w-0 flex-1 truncate text-right text-neutral-500" title={leaf}>{leaf}</span>
              <span className="w-[54px] shrink-0 text-right"><Delay ms={delayOf(leaf)} /></span>
              <span className={cx("w-3 shrink-0 text-neutral-400 transition", expanded && "rotate-90")}>›</span>
            </button>
            {expanded && (
              <ul className="mb-2 max-h-72 overflow-auto rounded-lg border border-neutral-200 bg-neutral-50 py-1 dark:border-neutral-700 dark:bg-neutral-800/60">
                {g.members.map((m) => {
                  const nested = byTag.has(m);
                  const mLeaf = nested ? leafOf(m) : m;
                  const current = m === g.selected;
                  return (
                    <li key={m}>
                      <button
                        disabled={disabled}
                        onClick={() => !current && onSelect(g.tag, m)}
                        className={cx("flex w-full items-center gap-2 px-3 py-1.5 text-left", current ? "bg-blue-50 dark:bg-blue-950/50" : "hover:bg-neutral-100 dark:hover:bg-neutral-700/60")}
                      >
                        <span className="w-4 shrink-0 text-blue-600">{current ? "✓" : ""}</span>
                        <span className="min-w-0 flex-1 truncate">
                          {m}
                          {nested && <span className="text-neutral-400"> → {mLeaf}</span>}
                        </span>
                        <span className="shrink-0"><Delay ms={delayOf(mLeaf)} /></span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </li>
        );
      })}
    </ul>
  );
}
