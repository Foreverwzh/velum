import { useCallback, useEffect, useState } from "react";
import { Download, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, componentStatus, componentText, isWindows, native, onUpdateProgress, type CliStatus, type HelperStatus, type Profile, type Status, type UpdateProgress } from "../api";
import { useAction } from "../hooks";
import type { Refresh } from "../App";
import { Button, cx, IconButton, KV, Notice, PageHeader, Panel, Row, Section, Switch, Tag, TextInput } from "../components/ui";
import { UsageBar, usageOf } from "../components/Usage";
import { SubscriptionForm } from "../components/SubscriptionForm";
import { installHelper } from "../components/TunToggle";

export default function Settings({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader title="设置" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[720px] flex-col gap-7 px-6 py-5">
          <Subscription status={s} refresh={refresh} />
          <Components status={s} refresh={refresh} />
          <Section title="关于">
            <Release status={s} refresh={refresh} />
            <Panel className="mt-2 px-3.5 py-3">
              <KV rows={[
                ["核心", <span className="font-mono text-[12px]">velumd {s.version} · pid {s.pid}</span>],
                ["本地端口", <span className="selectable font-mono text-[12px]">{s.listen || "—"}</span>],
                ["配置目录", <span className="selectable font-mono text-[12px] break-all">{s.home}</span>],
                ["开关来源", s.overridden ? "界面设置（覆盖 config.yaml，重启后保持）" : "config.yaml"],
              ]} />
            </Panel>
          </Section>
        </div>
      </div>
    </div>
  );
}

function Subscription({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string>();
  const { busy, error, run } = useAction(refresh);
  const sub = s.subscription;
  const profiles = s.profiles ?? [];
  const fetched = sub.fetched_at && !sub.fetched_at.startsWith("0001") ? new Date(sub.fetched_at).toLocaleString([], { hour12: false }) : "—";
  return (
    <Section
      title="订阅"
      actions={profiles.length > 0 && !adding && (
        <>
          <Button size="sm" variant="ghost" icon={RefreshCw} loading={busy === "update"} disabled={!!busy} onClick={() => run("update", api.update)}>更新当前订阅</Button>
          <Button size="sm" variant="ghost" icon={Plus} disabled={!!busy} onClick={() => setAdding(true)}>添加订阅</Button>
        </>
      )}
    >
      {!profiles.length ? (
        <Panel className="px-3.5 py-3"><SubscriptionForm onDone={refresh} /></Panel>
      ) : (
        <Panel>
          {profiles.map((p) => editing === p.id ? (
            <ProfileEditor key={p.id} profile={p} onCancel={() => setEditing(undefined)}
              onSave={(edit) => run("edit", async () => { await api.editProfile(p.id, edit); setEditing(undefined); })} busy={busy === "edit"} />
          ) : (
            <ProfileRow key={p.id} profile={p} busy={busy}
              onUse={() => run("use:" + p.id, () => api.activateProfile(p.id))}
              onUpdate={() => run("update:" + p.id, () => (p.active ? api.update() : api.updateProfile(p.id)))}
              onEdit={() => setEditing(p.id)}
              onDelete={() => run("delete:" + p.id, () => api.deleteProfile(p.id))} />
          ))}
          {adding && <AddProfile busy={busy === "add"} onCancel={() => setAdding(false)}
            onAdd={(url, name, activate) => run("add", async () => { await api.addProfile(url, name, activate); setAdding(false); })} />}
        </Panel>
      )}
      {sub.source && (
        <p className="mt-2 px-1 text-[12px] text-fg-3">
          当前订阅：<span className="tnum">{sub.nodes} 个节点 · {sub.groups} 个策略组 · {sub.rules} 条规则 · {sub.rule_sets} 个规则集 · 上次获取 {fetched}</span>
          {sub.origin === "cache" && "（缓存）"}
        </p>
      )}
      {sub.fetch_error && <div className="mt-2"><Notice tone="warn">远程拉取失败，正在使用缓存：{sub.fetch_error}</Notice></div>}
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Section>
  );
}

function ProfileRow({ profile: p, busy, onUse, onUpdate, onEdit, onDelete }: {
  profile: Profile; busy?: string; onUse: () => void; onUpdate: () => void; onEdit: () => void; onDelete: () => void;
}) {
  const u = usageOf(p.info);
  return (
    <div className="flex items-center gap-4 border-b border-line px-3.5 py-2.5 last:border-b-0">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-medium">{p.name}</span>
          {p.active && <Tag tone="accent">使用中</Tag>}
          {p.info?.web_page && (
            <a href={p.info.web_page} target="_blank" rel="noreferrer" className="text-[11.5px] text-fg-3 hover:text-fg-2">用户中心</a>
          )}
        </div>
        {u ? (
          <>
            {u.pct !== undefined && <UsageBar pct={u.pct} tone={u.tone} className="mt-1.5 max-w-[360px]" />}
            <div className={cx("tnum mt-1 text-[12px]", u.tone === "err" ? "text-err" : u.tone === "warn" ? "text-warn" : "text-fg-2")}>{u.text}</div>
          </>
        ) : (
          <div className="mt-0.5 text-[12px] text-fg-3">{p.kind === "file" ? "本地文件" : "订阅未提供流量信息"}</div>
        )}
        <div className="selectable mt-0.5 truncate font-mono text-[11px] text-fg-3" title={p.source}>{p.source}</div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        {!p.active && <Button size="sm" loading={busy === "use:" + p.id} disabled={!!busy} onClick={onUse}>使用</Button>}
        {p.kind === "url" && <IconButton icon={RefreshCw} label="更新（同时刷新流量）" disabled={!!busy} className={cx(busy === "update:" + p.id && "animate-spin")} onClick={onUpdate} />}
        <IconButton icon={Pencil} label="编辑" disabled={!!busy} onClick={onEdit} />
        <IconButton icon={Trash2} label={p.active ? "正在使用的订阅不能删除" : "删除"} disabled={!!busy || p.active} onClick={onDelete} />
      </div>
    </div>
  );
}

function ProfileEditor({ profile: p, busy, onSave, onCancel }: {
  profile: Profile; busy: boolean; onSave: (edit: { name?: string; url?: string }) => void; onCancel: () => void;
}) {
  const [name, setName] = useState(p.name);
  const [url, setUrl] = useState("");
  return (
    <form className="flex flex-col gap-2 border-b border-line px-3.5 py-3 last:border-b-0"
      onSubmit={(e) => { e.preventDefault(); onSave({ name: name.trim() || undefined, url: url.trim() || undefined }); }}>
      <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="名称" autoFocus />
      {p.kind === "url" && <TextInput value={url} onChange={(e) => setUrl(e.target.value)} placeholder="新的订阅地址（不改则留空）" className="font-mono text-[12px]" />}
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={busy}>{busy && url.trim() ? "正在拉取并校验" : "保存"}</Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>取消</Button>
      </div>
    </form>
  );
}

function AddProfile({ busy, onAdd, onCancel }: { busy: boolean; onAdd: (url: string, name: string, activate: boolean) => void; onCancel: () => void }) {
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [activate, setActivate] = useState(false);
  return (
    <form className="flex flex-col gap-2 px-3.5 py-3" onSubmit={(e) => { e.preventDefault(); if (url.trim() && !busy) onAdd(url.trim(), name.trim(), activate); }}>
      <TextInput value={url} onChange={(e) => setUrl(e.target.value)} placeholder="订阅地址 https://…" autoFocus className="font-mono text-[12px]" />
      <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="名称（可选，默认取订阅提供的名称）" />
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={busy} disabled={!url.trim()} onClick={() => setActivate(false)}>{busy ? "正在拉取并校验" : "添加"}</Button>
        <Button type="submit" disabled={!url.trim() || busy} onClick={() => setActivate(true)}>添加并使用</Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>取消</Button>
      </div>
    </form>
  );
}

/** 版本与更新：velumd 启动后定时检查（每 6 小时），这里也可以立即检查；有更新清单时一键下载安装并重启。 */
function Release({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  const { busy, error, run } = useAction(refresh);
  const [progress, setProgress] = useState<UpdateProgress | null>(null);
  useEffect(() => {
    native.updateProgress().then(setProgress).catch(() => {});
    return onUpdateProgress(setProgress);
  }, []);
  const u = s.update;
  const channel = u?.channel ?? (/-/.test(s.version) ? "beta" : "stable");
  const installing = progress && progress.stage !== "error";
  const checked = u ? new Date(u.checked_at).toLocaleString([], { hour12: false }) : "";
  const pct = progress?.total ? Math.floor((progress.downloaded / progress.total) * 100) : undefined;
  const hint = installing ? (progress.stage === "install" ? "正在安装，完成后 Velum 会自动重启…" : `正在下载新版本 ${u?.latest ?? ""}${pct !== undefined ? ` · ${pct}%` : "…"}`)
    : !u ? "尚未检查新版本"
    : u.available ? <>新版本 <span className="font-mono">{u.latest}</span> 已发布{!u.manifest && (u.download ? "，需要手动下载安装" : "，当前平台没有安装包，请在发布页查看")}</>
    : u.error ? <span className="text-warn">检查失败：{u.error}</span>
    : channel === "stable" && /-/.test(s.version) ? `当前是测试版：正式版高于 ${s.version} 后会提示更新 · 检查于 ${checked}`
    : `已是最新版本 · 检查于 ${checked}`;
  const action = installing ? null
    : u?.available && u.manifest && native.available
      ? <Button variant="primary" icon={Download} disabled={!!busy} onClick={() => run("install", () => native.installUpdate(u.manifest!))}>立即更新</Button>
    : u?.available && u.download
      ? <Button variant="primary" icon={Download} onClick={() => run("open", () => native.openRelease(u.download!))}>下载</Button>
    : <Button icon={RefreshCw} loading={busy === "check"} disabled={!!busy} onClick={() => run("check", api.checkRelease)}>检查更新</Button>;
  return (
    <>
      <Panel>
        <Row label={<span className="flex items-center gap-2">Velum <span className="font-mono text-[12px] font-normal">{s.version}</span>{u?.available && <Tag tone="accent">有新版本</Tag>}</span>} hint={hint}>
          {u?.available && u.url && !installing && <Button variant="ghost" onClick={() => run("open", () => native.openRelease(u.url!))}>发布说明</Button>}
          {action}
        </Row>
        <Row label="更新通道" hint={channel === "beta" ? "测试版：同时接收预发布版本（rc），更早用上新功能，可能不够稳定" : "正式版：只接收正式版本"}>
          <div className="inline-flex rounded-md border border-line p-0.5">
            {([["stable", "正式版"], ["beta", "测试版"]] as const).map(([id, label]) => (
              <button key={id} disabled={!!busy || !!installing} onClick={() => id !== channel && run("channel", () => api.setChannel(id))}
                className={cx("inline-flex h-6 items-center rounded px-2.5 text-[12px] transition-colors", channel === id ? "bg-active font-medium text-fg" : "text-fg-2 hover:text-fg")}>
                {label}
              </button>
            ))}
          </div>
        </Row>
      </Panel>
      {(error || progress?.error) && <div className="mt-2"><Notice tone="err">{error ?? progress?.error}</Notice></div>}
    </>
  );
}

/** 系统组件：TUN 组件（macOS：velum-helper；Windows：Velum 服务）、命令行工具、开机启动。 */
function Components({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  const [helper, setHelper] = useState<HelperStatus>();
  const [auto, setAuto] = useState<boolean>();
  const [cli, setCli] = useState<CliStatus>();
  const load = useCallback(async () => {
    setHelper(await componentStatus().catch(() => undefined));
    if (native.available) {
      setAuto(await native.autostartGet().catch(() => undefined));
      setCli(await native.cliStatus().catch(() => undefined));
    }
  }, []);
  useEffect(() => void load(), [load]);
  const after = useCallback(async () => { await load(); await refresh(); }, [load, refresh]);
  const { busy, error, run } = useAction(after);

  const uninstall = async () => {
    if (s.want_tun) await api.setSwitch("tun", "off"); // 先关 TUN，避免卸载后断网
    await native.helperUninstall();
  };
  const state = !helper ? <Tag>检测中</Tag>
    : helper.reachable ? <Tag tone="accent">{isWindows ? "已安装" : `已安装 · 协议 ${helper.version}`}</Tag>
    : helper.outdated ? <Tag tone="warn">需要更新</Tag>
    : <Tag>未安装</Tag>;

  return (
    <Section title="系统组件">
      <Panel>
        <Row label={<span className="flex items-center gap-2">TUN 组件 {state}</span>} hint={componentText.brief}>
          {(helper?.reachable || helper?.outdated) && <Button variant="danger" disabled={!!busy} loading={busy === "uninstall"} onClick={() => run("uninstall", uninstall)}>卸载</Button>}
          <Button variant={helper?.outdated ? "primary" : "secondary"} loading={busy === "install"} disabled={!!busy} onClick={() => run("install", installHelper)}>
            {helper?.reachable ? "重新安装" : helper?.outdated ? "更新" : "安装"}
          </Button>
        </Row>
        {cli && (
          <Row
            label={<span className="flex items-center gap-2">命令行工具 vlu {cli.installed ? <Tag tone="accent">已安装</Tag> : <Tag>未安装</Tag>}</span>}
            hint={<>
              {isWindows ? "把 Velum 安装目录加入当前用户的 PATH，新打开的终端里可以直接使用 vlu" : <>在 <code className="font-mono">{cli.path}</code> 创建链接，终端里可以直接使用 vlu（需要管理员密码）</>}
              {cli.conflict && <span className="text-warn">；已存在其他 vlu（{cli.conflict}），安装会替换它</span>}
            </>}
          >
            <Button variant={cli.installed ? "danger" : "secondary"} loading={busy === "cli"} disabled={!!busy} onClick={() => run("cli", cli.installed ? native.cliUninstall : native.cliInstall)}>
              {cli.installed ? "移除" : "安装"}
            </Button>
          </Row>
        )}
        {native.available && auto !== undefined && (
          <Row label="登录时启动" hint="登录后在后台启动 Velum，只显示菜单栏图标">
            <Switch label="登录时启动" checked={auto} disabled={!!busy} onChange={(v) => run("auto", () => native.autostartSet(v))} />
          </Row>
        )}
      </Panel>
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Section>
  );
}
