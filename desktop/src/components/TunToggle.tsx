import { useState } from "react";
import { ShieldCheck } from "lucide-react";
import { api, componentStatus, componentText, errText, native, type HelperStatus, type Status } from "../api";
import { Button, Row, Switch, Tag } from "./ui";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** 安装 / 更新 TUN 组件，并等待它就绪。 */
export async function installHelper() {
  await native.helperInstall();
  for (let i = 0; i < 25; i++) {
    if ((await componentStatus().catch(() => undefined))?.reachable) {
      // 让 velumd 用新组件重新加载：此前因组件缺失或版本不匹配而启动失败的 TUN 不会自己重试
      await api.reload().catch(() => undefined);
      return;
    }
    await sleep(300);
  }
  throw new Error(`TUN 组件已安装，但没有按时启动；可查看 ${componentText.log}`);
}

/** 系统代理与 TUN 两个开关（相互独立）。开启 TUN 前检查 TUN 组件，未安装或需要更新时在下方引导安装。 */
export function SwitchRows({ status, disabled, onDone }: { status: Status; disabled?: boolean; onDone: () => unknown }) {
  const [prompt, setPrompt] = useState<HelperStatus>();
  const [busy, setBusy] = useState<string>();
  const [err, setErr] = useState<string>();
  const paused = status.network?.paused;

  const act = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    setErr(undefined);
    try {
      await fn();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(undefined);
      await onDone();
    }
  };
  const setTun = (v: boolean) =>
    act("tun", async () => {
      setPrompt(undefined);
      if (v) {
        const h = await componentStatus();
        if (!h.reachable) return setPrompt(h);
      }
      await api.setSwitch("tun", v ? "on" : "off");
    });
  const install = () =>
    act("install", async () => {
      await installHelper();
      await api.setSwitch("tun", "on");
      setPrompt(undefined);
    });

  const pausedHint = (want: boolean, on: boolean) => (want && !on && paused ? <span className="text-warn">已暂停：{paused}</span> : undefined);
  return (
    <>
      <Row
        label={<span className="flex items-center gap-2">系统代理{status.want_system_proxy && !status.system_proxy_on && <Tag tone="warn">已暂停</Tag>}</span>}
        hint={pausedHint(status.want_system_proxy, status.system_proxy_on) ?? "浏览器等遵守系统代理设置的应用经过 Velum"}
      >
        <Switch label="系统代理" checked={status.want_system_proxy} paused={status.want_system_proxy && !status.system_proxy_on}
          disabled={disabled || !!busy} onChange={(v) => act("sys", () => api.setSwitch("system_proxy", v ? "on" : "off"))} />
      </Row>
      <Row
        label={<span className="flex items-center gap-2">TUN{status.want_tun && !status.tun_on && <Tag tone="warn">已暂停</Tag>}</span>}
        hint={pausedHint(status.want_tun, status.tun_on) ?? `接管所有应用的流量${status.tun?.system_dns?.length ? "，并接管系统 DNS" : ""}；可与系统代理同时开启`}
      >
        <Switch label="TUN" checked={status.want_tun} paused={status.want_tun && !status.tun_on} disabled={disabled || !!busy} onChange={setTun} />
      </Row>
      {prompt && (
        <div className="flex items-start gap-3 border-t border-line bg-info-soft px-3.5 py-3">
          <ShieldCheck className="mt-0.5 size-4 shrink-0 text-info" strokeWidth={1.8} />
          <div className="min-w-0 flex-1">
            <div className="font-medium">{prompt.outdated ? "TUN 组件需要更新" : "开启 TUN 需要先安装 TUN 组件"}</div>
            <div className="mt-0.5 text-[12px] text-fg-2">{componentText.detail}</div>
            <div className="mt-2.5 flex gap-2">
              <Button variant="primary" loading={busy === "install"} onClick={install}>{prompt.outdated ? "更新并开启" : "安装并开启"}</Button>
              <Button variant="ghost" disabled={!!busy} onClick={() => setPrompt(undefined)}>取消</Button>
            </div>
          </div>
        </div>
      )}
      {err && <div className="selectable border-t border-line px-3.5 py-2 text-[12px] text-err">{err}</div>}
    </>
  );
}
