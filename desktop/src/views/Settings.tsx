import { useCallback, useEffect, useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { api, componentStatus, componentText, isWindows, native, type CliStatus, type HelperStatus, type Status } from "../api";
import { useAction } from "../hooks";
import type { Refresh } from "../App";
import { Button, KV, Notice, PageHeader, Panel, Row, Section, Switch, Tag } from "../components/ui";
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
  const [edit, setEdit] = useState<string>();
  const { busy, error, run } = useAction(refresh);
  const sub = s.subscription;
  const fetched = sub.fetched_at && !sub.fetched_at.startsWith("0001") ? new Date(sub.fetched_at).toLocaleString([], { hour12: false }) : "—";
  return (
    <Section
      title="订阅"
      actions={edit === undefined && (
        <>
          <Button size="sm" variant="ghost" icon={RefreshCw} loading={busy === "update"} onClick={() => run("update", api.update)}>立即更新</Button>
          <Button size="sm" variant="ghost" onClick={() => run("load", async () => setEdit((await api.subscription()).url))}>修改地址</Button>
        </>
      )}
    >
      <Panel className="px-3.5 py-3">
        {edit !== undefined ? (
          <SubscriptionForm initial={edit} autoFocus onCancel={() => setEdit(undefined)} onDone={async () => { setEdit(undefined); await refresh(); }} />
        ) : (
          <KV rows={[
            ["地址", <span className="selectable font-mono text-[12px] break-all">{sub.source || "未配置"}</span>],
            ["上次获取", <span className="tnum">{fetched}{sub.origin === "cache" && <span className="ml-2 text-fg-3">（缓存）</span>}</span>],
            ["内容", <span className="tnum">{sub.nodes} 个节点 · {sub.groups} 个策略组 · {sub.rules} 条规则 · {sub.rule_sets} 个规则集</span>],
          ]} />
        )}
      </Panel>
      {sub.fetch_error && <div className="mt-2"><Notice tone="warn">远程拉取失败，正在使用缓存：{sub.fetch_error}</Notice></div>}
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
    </Section>
  );
}

/** 版本与新版本检查：velumd 启动后定时检查（每 6 小时），这里也可以立即检查。 */
function Release({ status: s, refresh }: { status: Status; refresh: Refresh }) {
  const { busy, error, run } = useAction(refresh);
  const u = s.update;
  const checked = u ? new Date(u.checked_at).toLocaleString([], { hour12: false }) : "";
  const hint = !u ? "尚未检查新版本"
    : u.available ? <>新版本 <span className="font-mono">{u.latest}</span> 已发布{!u.download && "，当前平台没有安装包，请在发布页查看"}</>
    : u.error ? <span className="text-warn">检查失败：{u.error}</span>
    : `已是最新版本 · 检查于 ${checked}`;
  return (
    <>
      <Panel>
        <Row label={<span className="flex items-center gap-2">Velum <span className="font-mono text-[12px] font-normal">{s.version}</span>{u?.available && <Tag tone="accent">有新版本</Tag>}</span>} hint={hint}>
          {u?.available && u.url && <Button variant="ghost" onClick={() => run("open", () => native.openRelease(u.url!))}>发布说明</Button>}
          {u?.available && u.download
            ? <Button variant="primary" icon={Download} onClick={() => run("open", () => native.openRelease(u.download!))}>下载</Button>
            : <Button icon={RefreshCw} loading={busy === "check"} disabled={!!busy} onClick={() => run("check", api.checkRelease)}>检查更新</Button>}
        </Row>
      </Panel>
      {error && <div className="mt-2"><Notice tone="err">{error}</Notice></div>}
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
