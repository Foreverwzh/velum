import { useState } from "react";
import { api, componentStatus, componentText, errText, native, type HelperStatus, type Status } from "../api";
import { Button, Toggle } from "./ui";

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

/** TUN 开关：开启前检查 TUN 组件（macOS：velum-helper；Windows：Velum 服务），未安装或需要更新时在开关下方说明并引导安装。 */
export function TunToggle({ status, disabled, onDone }: { status: Status | null; disabled?: boolean; onDone: () => unknown }) {
  const [prompt, setPrompt] = useState<HelperStatus>();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();

  const set = async (v: boolean) => {
    setErr(undefined);
    setPrompt(undefined);
    try {
      if (v) {
        const h = await componentStatus();
        if (!h.reachable) return setPrompt(h);
      }
      await api.setSwitch("tun", v ? "on" : "off");
    } catch (e) {
      setErr(errText(e));
    }
    await onDone();
  };
  const install = async () => {
    setBusy(true);
    setErr(undefined);
    try {
      await installHelper();
      await api.setSwitch("tun", "on");
      setPrompt(undefined);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
      await onDone();
    }
  };

  return (
    <div>
      <Toggle label="TUN（全局接管）" hint="所有应用的流量都经 Velum，可与系统代理同时开启" disabled={disabled || busy}
        on={!!status?.tun_on} want={!!status?.want_tun} paused={status?.network?.paused} onChange={set} />
      {prompt && (
        <div className="mb-2 rounded-lg bg-blue-50 p-3 text-[12px] dark:bg-blue-950/40">
          <div className="font-medium">{prompt.outdated ? "TUN 组件需要更新" : "开启 TUN 需要先安装一个系统组件"}</div>
          <div className="mt-1 text-neutral-600 dark:text-neutral-400">
            {componentText.detail}
          </div>
          <div className="mt-2 flex gap-2">
            <Button variant="primary" onClick={install} disabled={busy}>{busy ? "安装中…" : prompt.outdated ? "更新并开启" : "安装并开启"}</Button>
            <Button onClick={() => setPrompt(undefined)} disabled={busy}>取消</Button>
          </div>
        </div>
      )}
      {err && <div className="selectable pb-2 text-[12px] text-red-600">{err}</div>}
    </div>
  );
}
