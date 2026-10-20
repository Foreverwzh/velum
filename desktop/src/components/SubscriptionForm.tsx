import { useState } from "react";
import { api } from "../api";
import { Button, cx } from "./ui";

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
      setErr(String((e as Error).message ?? e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="flex flex-col gap-2" onSubmit={(e) => { e.preventDefault(); if (url.trim() && !busy) void submit(); }}>
      <input
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        autoFocus={autoFocus}
        spellCheck={false}
        placeholder="https://…"
        className={cx(
          "selectable w-full rounded-lg border bg-white px-3 py-2 font-mono text-[12px] outline-none focus:ring-2 focus:ring-blue-500/30 dark:bg-neutral-900",
          err ? "border-red-400" : "border-neutral-300 dark:border-neutral-700",
        )}
      />
      {err && <div className="selectable text-[12px] text-red-600">{err}</div>}
      <div className="flex items-center gap-2">
        <Button type="submit" variant="primary" disabled={!url.trim() || busy}>{busy ? "正在拉取并校验…" : "保存"}</Button>
        {onCancel && <Button type="button" onClick={onCancel} disabled={busy}>取消</Button>}
        <span className="text-[11px] text-neutral-400">支持 Clash / Stash / mihomo 格式</span>
      </div>
    </form>
  );
}
