import { useCallback, useEffect, useState } from "react";
import { Download, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, componentStatus, componentText, isWindows, native, onUpdateProgress, type CliStatus, type HelperStatus, type NetextStatus, type Profile, type Status, type UpdateProgress } from "../api";
import { useAction } from "../hooks";
import type { Refresh } from "../App";
import { Button, cx, IconButton, KV, Notice, PageHeader, Panel, Row, Section, Switch, Tag, TextInput } from "../components/ui";
import { UsageBar, usageOf } from "../components/Usage";
import { SubscriptionForm } from "../components/SubscriptionForm";
import { installHelper } from "../components/TunToggle";
import { languagePref, setLanguagePref, t, type LanguagePref } from "../i18n";

export default function Settings({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader title={t("设置")} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[720px] flex-col gap-7 px-6 py-5">
          <Subscription status={s} refresh={refresh} />
          <Components status={s} refresh={refresh} />
          <RulesSection status={s} refresh={refresh} />
          <LanguageSection />
          <Section title={t("关于")}>
            <Release status={s} refresh={refresh} />
            <Panel className="mt-2 px-3.5 py-3">
              <KV rows={[
                [t("核心"), <span className="font-mono text-[12px]">velumd {s.version} · pid {s.pid}</span>],
                [t("本地端口"), <span className="selectable font-mono text-[12px]">{s.listen || "—"}</span>],
                [t("配置目录"), <span className="selectable font-mono text-[12px] break-all">{s.home}</span>],
                [t("开关来源"), s.overridden ? t("界面设置（覆盖 config.yaml，重启后保持）") : "config.yaml"],
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
      title={t("订阅")}
      actions={profiles.length > 0 && !adding && (
        <>
          <Button size="sm" variant="ghost" icon={RefreshCw} loading={busy === "update"} disabled={!!busy} onClick={() => run("update", api.update)}>{t("更新当前订阅")}</Button>
          <Button size="sm" variant="ghost" icon={Plus} disabled={!!busy} onClick={() => setAdding(true)}>{t("添加订阅")}</Button>
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
          {t("当前订阅：")}<span className="tnum">{sub.nodes}{" "}{t("个节点 ·")}{" "}{sub.groups}{" "}{t("个策略组 ·")}{" "}{sub.rules}{" "}{t("条规则 ·")}{" "}{sub.rule_sets}{" "}{t("个规则集 · 上次获取")}{" "}{fetched}</span>
          {sub.fetch_error && t("（缓存）")}
        </p>
      )}
      {sub.fetch_error && <div className="mt-2"><Notice tone="warn">{t("远程拉取失败，正在使用缓存：")}{sub.fetch_error}</Notice></div>}
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
          {p.active && <Tag tone="accent">{t("使用中")}</Tag>}
          {p.info?.web_page && (
            <a href={p.info.web_page} target="_blank" rel="noreferrer" className="text-[11.5px] text-fg-3 hover:text-fg-2">{t("用户中心")}</a>
          )}
        </div>
        {u ? (
          <>
            {u.pct !== undefined && <UsageBar pct={u.pct} tone={u.tone} className="mt-1.5 max-w-[360px]" />}
            <div className={cx("tnum mt-1 text-[12px]", u.tone === "err" ? "text-err" : u.tone === "warn" ? "text-warn" : "text-fg-2")}>{u.text}</div>
          </>
        ) : (
          <div className="mt-0.5 text-[12px] text-fg-3">{p.kind === "file" ? t("本地文件") : t("订阅未提供流量信息")}</div>
        )}
        <div className="selectable mt-0.5 truncate font-mono text-[11px] text-fg-3" title={p.source}>{p.source}</div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        {!p.active && <Button size="sm" loading={busy === "use:" + p.id} disabled={!!busy} onClick={onUse}>{t("使用")}</Button>}
        {p.kind === "url" && <IconButton icon={RefreshCw} label={t("更新（同时刷新流量）")} disabled={!!busy} className={cx(busy === "update:" + p.id && "animate-spin")} onClick={onUpdate} />}
        <IconButton icon={Pencil} label={t("编辑")} disabled={!!busy} onClick={onEdit} />
        <IconButton icon={Trash2} label={p.active ? t("正在使用的订阅不能删除") : t("删除")} disabled={!!busy || p.active} onClick={onDelete} />
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
      <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder={t("名称")} autoFocus />
      {p.kind === "url" && <TextInput value={url} onChange={(e) => setUrl(e.target.value)} placeholder={t("新的订阅地址（不改则留空）")} className="font-mono text-[12px]" />}
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={busy}>{busy && url.trim() ? t("正在拉取并校验") : t("保存")}</Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>{t("取消")}</Button>
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
      <TextInput value={url} onChange={(e) => setUrl(e.target.value)} placeholder={t("订阅地址 https://…")} autoFocus className="font-mono text-[12px]" />
      <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder={t("名称（可选，默认取订阅提供的名称）")} />
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={busy} disabled={!url.trim()} onClick={() => setActivate(false)}>{busy ? t("正在拉取并校验") : t("添加")}</Button>
        <Button type="submit" disabled={!url.trim() || busy} onClick={() => setActivate(true)}>{t("添加并使用")}</Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>{t("取消")}</Button>
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
  const hint = installing ? (progress.stage === "install" ? t("正在安装，完成后 Velum 会自动重启…") : t("正在下载新版本 {0}{1}", u?.latest ?? "", pct !== undefined ? ` · ${pct}%` : "…"))
    : !u ? t("尚未检查新版本")
    : u.available ? <>{t("新版本")}{" "}<span className="font-mono">{u.latest}</span>{" "}{t("已发布")}{!u.manifest && (u.download ? t("，需要手动下载安装") : t("，当前平台没有安装包，请在发布页查看"))}</>
    : u.error ? <span className="text-warn">{t("检查失败：")}{u.error}</span>
    : channel === "stable" && /-/.test(s.version) ? t("当前是测试版：正式版高于 {0} 后会提示更新 · 检查于 {1}", s.version, checked)
    : t("已是最新版本 · 检查于 {0}", checked);
  const action = installing ? null
    : u?.available && u.manifest && native.available
      ? <Button variant="primary" icon={Download} disabled={!!busy} onClick={() => run("install", () => native.installUpdate(u.manifest!))}>{t("立即更新")}</Button>
    : u?.available && u.download
      ? <Button variant="primary" icon={Download} onClick={() => run("open", () => native.openRelease(u.download!))}>{t("下载")}</Button>
    : <Button icon={RefreshCw} loading={busy === "check"} disabled={!!busy} onClick={() => run("check", api.checkRelease)}>{t("检查更新")}</Button>;
  return (
    <>
      <Panel>
        <Row label={<span className="flex items-center gap-2">Velum <span className="font-mono text-[12px] font-normal">{s.version}</span>{u?.available && <Tag tone="accent">{t("有新版本")}</Tag>}</span>} hint={hint}>
          {u?.available && u.url && !installing && <Button variant="ghost" onClick={() => run("open", () => native.openRelease(u.url!))}>{t("发布说明")}</Button>}
          {action}
        </Row>
        <Row label={t("更新通道")} hint={channel === "beta" ? t("测试版：同时接收预发布版本（rc），更早用上新功能，可能不够稳定") : t("正式版：只接收正式版本")}>
          <div className="inline-flex rounded-md border border-line p-0.5">
            {([["stable", t("正式版")], ["beta", t("测试版")]] as const).map(([id, label]) => (
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
  const [ne, setNe] = useState<NetextStatus>();
  const load = useCallback(async () => {
    setHelper(await componentStatus().catch(() => undefined));
    if (native.available) {
      setNe(await native.netextStatus().catch(() => undefined));
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
  const state = !helper ? <Tag>{t("检测中")}</Tag>
    : helper.reachable ? <Tag tone="accent">{isWindows ? t("已安装") : t("已安装 · 协议 {0}", helper.version)}</Tag>
    : helper.outdated ? <Tag tone="warn">{t("需要更新")}</Tag>
    : <Tag>{t("未安装")}</Tag>;

  return (
    <Section title={t("系统组件")}>
      <Panel>
        {ne?.bundled && (
          <Row
            label={<span className="flex items-center gap-2">{t("网络扩展")}{" "}<Tag tone="warn">{t("实验")}</Tag>{ne.enabled && <Tag tone="accent">{t("使用中")}</Tag>}</span>}
            hint={busy === "ne" && !ne.enabled
              ? t("如果系统提示，请在「系统设置 → 隐私与安全性」或「通用 → 登录项与扩展」中允许 Velum 的网络扩展，并允许添加 VPN 配置")
              : t("核心、TUN 与系统代理改由系统网络扩展运行，不需要 TUN 组件；Velum 退出或崩溃时系统自动撤销路由、DNS 与系统代理。开启时会以当前配置重新迁移，关闭后扩展里改过的设置不会带回")}
          >
            <Switch label={t("网络扩展")} checked={ne.enabled} disabled={!!busy && busy !== "ne"} pending={busy === "ne"} busy={!!busy}
              onChange={(v) => run("ne", () => native.netextSet(v))} />
          </Row>
        )}
        {!ne?.enabled && (
        <Row label={<span className="flex items-center gap-2">{t("TUN 组件")}{" "}{state}</span>} hint={componentText.brief}>
          {(helper?.reachable || helper?.outdated) && <Button variant="danger" disabled={!!busy} loading={busy === "uninstall"} onClick={() => run("uninstall", uninstall)}>{t("卸载")}</Button>}
          <Button variant={helper?.outdated ? "primary" : "secondary"} loading={busy === "install"} disabled={!!busy} onClick={() => run("install", installHelper)}>
            {helper?.reachable ? t("重新安装") : helper?.outdated ? t("更新") : t("安装")}
          </Button>
        </Row>
        )}
        {cli && (
          <Row
            label={<span className="flex items-center gap-2">{t("命令行工具 vlu")}{" "}{cli.installed ? <Tag tone="accent">{t("已安装")}</Tag> : <Tag>{t("未安装")}</Tag>}</span>}
            hint={<>
              {isWindows ? t("把 Velum 安装目录加入当前用户的 PATH，新打开的终端里可以直接使用 vlu") : <>{t("在")}{" "}<code className="font-mono">{cli.path}</code>{" "}{t("创建链接，终端里可以直接使用 vlu（需要管理员密码）")}</>}
              {cli.conflict && <span className="text-warn">{t("；已存在其他 vlu（")}{cli.conflict}{t("），安装会替换它")}</span>}
            </>}
          >
            <Button variant={cli.installed ? "danger" : "secondary"} loading={busy === "cli"} disabled={!!busy} onClick={() => run("cli", cli.installed ? native.cliUninstall : native.cliInstall)}>
              {cli.installed ? t("移除") : t("安装")}
            </Button>
          </Row>
        )}
        {native.available && auto !== undefined && (
          <Row label={t("登录时启动")} hint={t("登录后在后台启动 Velum，只显示菜单栏图标")}>
            <Switch label={t("登录时启动")} checked={auto} disabled={!!busy} onChange={(v) => run("auto", () => native.autostartSet(v))} />
          </Row>
        )}
      </Panel>
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Section>
  );
}

/** 规则匹配方式。 */
function RulesSection({ status, refresh }: { status: Status; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const change = async (v: boolean) => {
    setBusy(true);
    setError(undefined);
    try {
      await api.setDomainFirst(v);
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Section title={t("规则")}>
      <Panel>
        <Row label={t("域名规则优先")} hint={t("先匹配所有域名规则，都没命中才解析 DNS 并匹配 IP 规则，可以少查 DNS。排在前面的 IP 规则不再优先于后面的域名规则。")}>
          <Switch label={t("域名规则优先")} checked={!!status.domain_first} disabled={busy} pending={busy} onChange={change} />
        </Row>
      </Panel>
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Section>
  );
}

/** 语言：切换后重新加载页面。 */
function LanguageSection() {
  const options: { value: LanguagePref; label: string }[] = [
    { value: "auto", label: t("跟随系统") },
    { value: "zh", label: "中文" },
    { value: "en", label: "English" },
  ];
  const pref = languagePref();
  return (
    <Section title={t("语言")}>
      <Panel>
        <Row label={t("界面语言")} hint={t("当前显示中文")}>
          <div className="flex gap-1">
            {options.map((o) => (
              <Button key={o.value} size="sm" variant={pref === o.value ? "primary" : "secondary"} onClick={() => setLanguagePref(o.value)}>
                {o.label}
              </Button>
            ))}
          </div>
        </Row>
      </Panel>
    </Section>
  );
}
