import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Status } from "./api";
import { titlebarOverlay } from "./components/ui";
import Simple from "./views/Simple";
import Pro from "./views/Pro";

export type Refresh = () => Promise<void>;

// App 打开后这段时间内连不上 velumd 视为仍在启动（App 正在拉起它），不显示错误
const BOOT_GRACE_MS = 20_000;
const openedAt = Date.now();

export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState<string>();
  // ?view=pro 便于开发预览；否则记住用户上次的选择
  const [view, setView] = useState<"simple" | "pro">(
    () => (new URLSearchParams(location.search).get("view") as "simple" | "pro") ?? (localStorage.getItem("velum.view") as "simple" | "pro") ?? "simple",
  );

  const connected = useRef(false);
  const refresh = useCallback(async () => {
    try {
      setStatus(await api.status());
      setError(undefined);
      connected.current = true;
    } catch (e) {
      setStatus(null);
      setError(!connected.current && Date.now() - openedAt < BOOT_GRACE_MS ? undefined : String((e as Error).message ?? e));
    }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 2000);
    return () => clearInterval(t);
  }, [refresh]);

  const switchView = (v: "simple" | "pro") => {
    setView(v);
    localStorage.setItem("velum.view", v);
  };

  return (
    <>
      {/* 标题栏拖动区域（交通灯所在的一行） */}
      {titlebarOverlay && <div data-tauri-drag-region className="fixed inset-x-0 top-0 z-50 h-7" />}
      {view === "simple" ? (
        <Simple status={status} error={error} refresh={refresh} onPro={() => switchView("pro")} />
      ) : (
        <Pro status={status} error={error} refresh={refresh} onSimple={() => switchView("simple")} />
      )}
    </>
  );
}
