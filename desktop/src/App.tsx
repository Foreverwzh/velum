import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeftRight, CircleGauge, FileCode2, Search, Settings as SettingsIcon, TriangleAlert, Waypoints, ScanSearch, type LucideIcon } from "lucide-react";
import { api, type Status } from "./api";
import { health } from "./health";
import { Kbd, StatusMark, cx, modKey, titlebarOverlay } from "./components/ui";
import { CommandPalette } from "./components/CommandPalette";
import Overview from "./views/Overview";
import Groups from "./views/Groups";
import Connections from "./views/Connections";
import Diagnose from "./views/Diagnose";
import Overlay from "./views/Overlay";
import Warnings from "./views/Warnings";
import Settings from "./views/Settings";

export type Refresh = () => Promise<void>;
export type Page = "overview" | "groups" | "connections" | "diagnose" | "overlay" | "warnings" | "settings";
/** 页面跳转：arg 是页面参数（策略组名、诊断目标、连接过滤词）。 */
export type Go = (page: Page, arg?: string) => void;

const nav: { id: Page; label: string; icon: LucideIcon }[] = [
  { id: "overview", label: "概览", icon: CircleGauge },
  { id: "groups", label: "策略组", icon: Waypoints },
  { id: "connections", label: "连接", icon: ArrowLeftRight },
  { id: "diagnose", label: "诊断", icon: ScanSearch },
  { id: "overlay", label: "本地覆盖", icon: FileCode2 },
  { id: "warnings", label: "告警", icon: TriangleAlert },
  { id: "settings", label: "设置", icon: SettingsIcon },
];

// App 打开后这段时间内连不上 velumd 视为仍在启动（App 正在拉起它），不显示错误
const BOOT_GRACE_MS = 20_000;
const openedAt = Date.now();

export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState<string>();
  const params = new URLSearchParams(location.search);
  // ?page=groups&arg=Proxy 便于开发预览
  const [page, setPage] = useState<Page>(() => (params.get("page") as Page) ?? "overview");
  const [arg, setArg] = useState<string | undefined>(() => params.get("arg") ?? undefined);
  const [palette, setPalette] = useState(() => params.has("palette"));

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

  const go: Go = useCallback((p, a) => {
    setPage(p);
    setArg(a);
  }, []);

  // 托盘「检查更新」：打开设置页显示结果
  useEffect(() => {
    if (!("__TAURI_INTERNALS__" in window)) return;
    let unlisten: (() => void) | undefined;
    let cancelled = false;
    import("@tauri-apps/api/event").then(({ listen }) =>
      listen<Page>("velum://navigate", (e) => go(e.payload)).then((u) => (cancelled ? u() : (unlisten = u))),
    );
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [go]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((v) => !v);
      } else if (mod && /^[1-7]$/.test(e.key)) {
        e.preventDefault();
        go(nav[Number(e.key) - 1].id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [go]);

  const h = health(status, error);
  const ready = !!status && !status.starting;

  return (
    <div className="flex h-full">
      {titlebarOverlay && <div data-tauri-drag-region className="fixed inset-x-0 top-0 z-40 h-7" />}
      <aside className={cx("flex w-[208px] shrink-0 flex-col border-r border-line bg-sidebar px-2.5 pb-3", titlebarOverlay ? "pt-10" : "pt-3")}>
        <div className="mb-3 flex items-center gap-2.5 px-2">
          <StatusMark tone={h.tone} />
          <div className="min-w-0">
            <div className="text-[13.5px] font-semibold tracking-[-0.01em]">Velum</div>
            <div className="truncate text-[11.5px] text-fg-3">{h.title}</div>
          </div>
        </div>
        <button
          onClick={() => setPalette(true)}
          className="mb-3 flex h-7 items-center gap-2 rounded-md border border-line bg-panel px-2 text-[12px] text-fg-3 shadow-raise transition-colors hover:border-line-strong hover:text-fg-2"
        >
          <Search className="size-3.5" strokeWidth={2} />
          <span className="flex-1 text-left">切换节点、执行操作</span>
          <Kbd>{modKey}K</Kbd>
        </button>
        <nav className="flex flex-col gap-px">
          {nav.map((n, i) => {
            const on = page === n.id;
            return (
              <button
                key={n.id}
                onClick={() => go(n.id)}
                title={`${n.label}（${modKey}${i + 1}）`}
                className={cx(
                  "flex h-7 items-center gap-2.5 rounded-md px-2 text-left text-[12.5px] transition-colors",
                  on ? "bg-active font-medium text-fg" : "text-fg-2 hover:bg-hover hover:text-fg",
                )}
              >
                <n.icon className={cx("size-[15px]", on ? "text-fg" : "text-fg-3")} strokeWidth={1.8} />
                <span className="flex-1">{n.label}</span>
                {n.id === "warnings" && !!status?.warnings && <span className="font-mono text-[11px] text-fg-3 tnum">{status.warnings}</span>}
                {n.id === "settings" && status?.update?.available && <span title="有新版本" className="size-1.5 rounded-full bg-accent" />}
              </button>
            );
          })}
        </nav>
        <div className="mt-auto px-2 font-mono text-[10.5px] leading-4 text-fg-3">
          {status?.listen && <div className="selectable tnum">{status.listen}</div>}
          {status?.version && <div>velumd {status.version}</div>}
        </div>
      </aside>

      <main className={cx("flex min-w-0 flex-1 flex-col", titlebarOverlay ? "pt-7" : "pt-3")}>
        {!ready ? (
          <Boot title={h.title} detail={h.detail} tone={h.tone} />
        ) : (
          {
            overview: <Overview status={status} refresh={refresh} go={go} />,
            groups: <Groups status={status} refresh={refresh} focus={arg} />,
            connections: <Connections go={go} refresh={refresh} initialQuery={arg} />,
            diagnose: <Diagnose key={arg} refresh={refresh} initialTarget={arg} />,
            overlay: <Overlay status={status} refresh={refresh} />,
            warnings: <Warnings />,
            settings: <Settings status={status} refresh={refresh} />,
          }[page]
        )}
      </main>

      {palette && <CommandPalette status={status} refresh={refresh} go={go} onClose={() => setPalette(false)} />}
    </div>
  );
}

/** 启动中 / 未运行：内容区只给一句结论，不显示空白页面。 */
function Boot({ title, detail, tone }: { title: string; detail: string; tone: "green" | "amber" | "red" | "gray" }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-8 text-center">
      <StatusMark tone={tone} size={12} />
      <div className="text-[15px] font-semibold">{title}</div>
      <div className="selectable max-w-[420px] text-[12.5px] text-fg-3">{detail}</div>
    </div>
  );
}
