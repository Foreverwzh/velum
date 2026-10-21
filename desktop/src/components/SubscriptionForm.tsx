import { useState } from "react";
import { api, errText } from "../api";
import { Button, TextInput } from "./ui";

/** 填写 / 修改订阅地址：velumd 先拉取并校验，通过后才写入配置。 */
export function SubscriptionForm({ initial = "", onDone, onCancel, autoFocus }: {
  initial?: string; onDone: () => void | Promise<void>; onCancel?: () => void; autoFocus?: boolean;
}) {
  const [url, setUrl] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();
  const submit = async () => {
    setBusy(true);
    setErr(undefined);
    try {
      await api.setSubscription(url.trim());
      await onDone();
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="flex flex-col gap-2" onSubmit={(e) => { e.preventDefault(); if (url.trim() && !busy) void submit(); }}>
      <TextInput value={url} onChange={(e) => setUrl(e.target.value)} autoFocus={autoFocus} placeholder="https://…" invalid={!!err} className="font-mono text-[12px]" />
      {err && <div className="selectable text-[12px] text-err">{err}</div>}
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" loading={busy} disabled={!url.trim()}>{busy ? "正在拉取并校验" : "保存"}</Button>
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel} disabled={busy}>取消</Button>}
        <span className="text-[11.5px] text-fg-3">支持 Clash、Stash、mihomo 格式；首次拉取可能需要十几秒</span>
      </div>
    </form>
  );
}
