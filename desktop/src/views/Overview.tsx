import { useEffect, useState } from "react";
import { ArrowRight, Building2, Gauge, Globe, Network, Split, TriangleAlert, Wifi, Wrench } from "lucide-react";
import { isRejected } from "./Connections";
import { api, isWindows, staleDaemon, subscribeConnections, type Connection, type RepairStep, type Status } from "../api";
import { health } from "../health";
import { delayOf, leafOf, pathOf, useAction } from "../hooks";
import type { Go, Refresh } from "../App";
import { Button, Chain, Latency, Notice, Panel, Section, StatusMark, Tag, chainSteps, cx } from "../components/ui";
import { installHelper, SwitchRows } from "../components/TunToggle";
import { SubscriptionForm } from "../components/SubscriptionForm";
import { UsageBar, usageOf } from "../components/Usage";

export default function Overview({ status: s, refresh, go }: { status: Status; refresh: Refresh; go: Go }) {
  const h = health(s);
  const { busy, error, run } = useAction(refresh);
  const [steps, setSteps] = useState<RepairStep[]>();
  const hasSub = !!s.subscription?.source;

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-[760px] flex-col gap-7 px-6 pt-5 pb-10">
        <header className="flex items-start gap-4">
          <div className="mt-[7px]"><StatusMark tone={h.tone} size={12} /></div>
          <div className="min-w-0 flex-1">
            <h1 className="text-[22px] font-semibold tracking-[-0.02em]">{h.title}</h1>
            <p className="selectable mt-0.5 text-[13px] text-fg-2">{h.detail}</p>
          </div>
          {staleDaemon(s) && isWindows && (
            <Button variant="primary" loading={busy === "service"} disabled={!!busy} onClick={() => run("service", installHelper)}>更新服务</Button>
          )}
          <Button icon={Wrench} loading={busy === "repair"} title={REPAIR_HINT} onClick={() => run("repair", async () => setSteps((await api.repair()).steps))}>一键修复</Button>
        </header>

        {steps && (
          <Panel>
            <div className="border-b border-line px-3.5 py-2 text-[12px] text-fg-3">{REPAIR_HINT}</div>
            {steps.map((st) => (
              <div key={st.name} className="flex items-center gap-3 border-b border-line px-3.5 py-2 text-[12.5px] last:border-0">
                <Tag tone={st.ok ? "accent" : st.warn ? "warn" : "err"}>{st.ok ? "完成" : st.warn ? "需处理" : "失败"}</Tag>
                <span className="shrink-0">{st.name}</span>
                {st.detail && <span className={cx("selectable text-fg-3", !st.warn && "truncate")}>{st.detail}</span>}
              </div>
            ))}
          </Panel>
        )}
        {error && <Notice tone="err">{error}</Notice>}

        <Panel>
          <SwitchRows status={s} onDone={refresh} />
        </Panel>

        <NetworkLine status={s} />

        {!hasSub ? (
          <Section title="添加订阅">
            <Panel className="p-4">
              <p className="mb-3 text-[12.5px] text-fg-2">填写订阅地址即可开始使用。订阅地址由提供订阅的服务给你。</p>
              <SubscriptionForm onDone={refresh} autoFocus />
            </Panel>
          </Section>
        ) : (
          <>
            <SubscriptionLine status={s} refresh={refresh} go={go} />
            <GroupSummary status={s} go={go} busy={busy} onTest={() => run("test", () => api.test(""))} />
            <RecentConnections go={go} />
          </>
        )}
      </div>
    </div>
  );
}

const REPAIR_HINT = "依次清空系统 DNS 缓存、重新检测网络环境、还原残留的系统 DNS、检查其他代理工具的残留、重新拉取订阅并加载（规则集在后台更新）、校正系统代理、节点测速";

function NetworkLine({ status: s }: { status: Status }) {
  const n = s.network;
  if (!n) return null;
  const captive = { online: { t: "网络正常", tone: "text-fg-2" }, captive: { t: "需要网页认证", tone: "text-warn" }, offline: { t: "网络不通", tone: "text-err" } }[n.captive ?? ""] ?? { t: "检测中", tone: "text-fg-3" };
  const items: { icon: typeof Wifi; text: string; tone?: string; title?: string }[] = [
    { icon: Wifi, text: n.primary_interface || "无主接口" },
    { icon: Globe, text: captive.t, tone: captive.tone, title: n.captive_detail },
    ...(n.vpns ?? []).map((v) => ({
      icon: v.full ? Network : Building2,
      text: v.full ? `其他 VPN / 代理工具全局接管（${v.iface}）` : `企业 VPN 分流中（${v.iface}）`,
      tone: v.full ? "text-warn" : undefined,
      title: v.full ? "如果不是正在使用的 VPN，多为其他代理工具的 TUN 没有退出干净：在该工具中关闭 TUN 并退出，或重启电脑" : `${v.routes?.length ?? 0} 个网段、${v.domains?.length ?? 0} 个域名直连`,
    })),
    ...(n.stale_dns?.length
      ? [{ icon: TriangleAlert, text: `系统 DNS 残留（${n.stale_dns.join("、")}）`, tone: "text-warn", title: "代理工具退出时没有还原系统 DNS，解析会失败或很慢：在网络设置中把 DNS 改回自动获取" }]
      : []),
    ...(n.split_dns_domains?.length ? [{ icon: Split, text: `DNS 分流 ${n.split_dns_domains.length} 个域名`, title: n.split_dns_domains.join("、") }] : []),
  ];
  return (
    <div className="-mt-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 px-1 text-[12px] text-fg-3">
      {items.map((it, i) => (
        <span key={i} title={it.title} className={cx("inline-flex items-center gap-1.5", it.tone)}>
          <it.icon className="size-3.5" strokeWidth={1.8} />
          {it.text}
        </span>
      ))}
    </div>
  );
}

/** 当前订阅：流量额度与到期；有多个订阅时可直接切换。 */
function SubscriptionLine({ status: s, refresh, go }: { status: Status; refresh: Refresh; go: Go }) {
  const { busy, error, run } = useAction(refresh);
  const profiles = s.profiles ?? [];
  const u = usageOf(s.subscription.info);
  if (!u && profiles.length < 2) return null;
  return (
    <Panel className="px-3.5 py-2.5">
      <div className="flex items-center gap-3">
        <span className="shrink-0 text-[12px] text-fg-3">订阅</span>
        {profiles.length > 1 ? (
          <select value={s.subscription.id} disabled={!!busy} onChange={(e) => run("use", () => api.activateProfile(e.target.value))}
            className="h-7 min-w-0 rounded-md border border-line-strong bg-raised px-2 text-[12.5px] font-medium">
            {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        ) : (
          <span className="truncate font-medium">{s.subscription.name}</span>
        )}
        {busy && <span className="text-[12px] text-fg-3">正在切换…</span>}
        <div className="min-w-0 flex-1">
          {u?.pct !== undefined && <UsageBar pct={u.pct} tone={u.tone} />}
          {u && <div className={cx("tnum mt-1 truncate text-[12px]", u.tone === "err" ? "text-err" : u.tone === "warn" ? "text-warn" : "text-fg-2")}>{u.text}</div>}
        </div>
        <Button size="sm" variant="ghost" onClick={() => go("settings")}>管理<ArrowRight className="size-3" /></Button>
      </div>
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Panel>
  );
}

function GroupSummary({ status: s, go, busy, onTest }: { status: Status; go: Go; busy?: string; onTest: () => void }) {
  const groups = s.groups ?? [];
  const selectable = groups.filter((g) => g.type === "select");
  if (!selectable.length) return null;
  const shown = selectable.slice(0, 8);
  return (
    <Section
      title="策略组"
      actions={
        <>
          <Button size="sm" variant="ghost" icon={Gauge} loading={busy === "test"} onClick={onTest}>测速</Button>
          <Button size="sm" variant="ghost" onClick={() => go("groups")}>全部 {selectable.length} 个<ArrowRight className="size-3" /></Button>
        </>
      }
    >
      <Panel>
        {shown.map((g) => {
          const path = pathOf(groups, g.tag).slice(1);
          return (
            <button key={g.tag} onClick={() => go("groups", g.tag)}
              className="grid h-10 w-full grid-cols-[140px_1fr_76px] items-center gap-3 border-b border-line px-3.5 text-left transition-colors last:border-0 hover:bg-hover">
              <span className="truncate font-medium" title={g.tag}>{g.tag}</span>
              <Chain size="sm" steps={path.map((p, i) => ({ label: p, kind: p === "DIRECT" ? "direct" : i === path.length - 1 ? "node" : "group" }))} />
              <Latency ms={delayOf(groups, leafOf(groups, g.tag))} className="justify-self-end" />
            </button>
          );
        })}
      </Panel>
    </Section>
  );
}

function RecentConnections({ go }: { go: Go }) {
  const [list, setList] = useState<Connection[]>([]);
  useEffect(() => {
    // 按规则拦截的连接（如 ipv6: false 拒绝的 IPv6 重试）是预期行为，概览里不占位置，在「连接」页查看
    api.connections(60).then((r) => setList((r.connections ?? []).filter((c) => !isRejected(c)).slice(0, 6))).catch(() => undefined);
    return subscribeConnections((c) => !isRejected(c) && setList((xs) => [c, ...xs.filter((x) => x.id !== c.id)].slice(0, 6)));
  }, []);
  return (
    <Section title="最近连接" actions={<Button size="sm" variant="ghost" onClick={() => go("connections")}>全部连接<ArrowRight className="size-3" /></Button>}>
      <Panel>
        {!list.length && <div className="px-3.5 py-6 text-center text-[12.5px] text-fg-3">经 Velum 访问任意网站后，这里会实时显示它走了哪条规则、哪个节点</div>}
        {list.map((c) => (
          <button key={c.id} onClick={() => go("diagnose", c.domain || c.destination)} title="诊断这个目标"
            className="grid h-10 w-full grid-cols-[minmax(120px,200px)_1fr_64px] items-center gap-3 border-b border-line px-3.5 text-left transition-colors last:border-0 hover:bg-hover">
            <span className="truncate font-medium">{c.domain || c.destination}</span>
            <Chain size="sm" steps={chainSteps(c.chain, { label: c.rule_index >= 0 ? `#${c.rule_index}` : "final", title: c.provenance.text })} />
            <span className="justify-self-end font-mono text-[11.5px] text-fg-3 tnum">{c.first_byte_ms >= 0 ? `${c.first_byte_ms} ms` : "—"}</span>
          </button>
        ))}
      </Panel>
    </Section>
  );
}
