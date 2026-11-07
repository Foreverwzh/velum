import { useCallback, useEffect, useRef, useState } from "react";
import { language } from "./i18n";
import { ArrowLeftRight, CircleGauge, Ellipsis, FileCode2, Search, Settings as SettingsIcon, TriangleAlert, Waypoints, ScanSearch, X, type LucideIcon } from "lucide-react";
import { api, native, type Status } from "./api";
import { health } from "./health";
import { IconButton, Kbd, Notice, StatusMark, cx, isMac, modKey, titlebarOverlay } from "./components/ui";
import { CommandPalette } from "./components/CommandPalette";
import Overview from "./views/Overview";
import Groups from "./views/Groups";
import Connections from "./views/Connections";
import Diagnose from "./views/Diagnose";
import Overlay from "./views/Overlay";
import Warnings from "./views/Warnings";
import Settings from "./views/Settings";
import { t } from "./i18n";

export type Refresh = () => Promise<void>;
export type Page = "overview" | "groups" | "connections" | "diagnose" | "overlay" | "warnings" | "settings";
/** 页面跳转：arg 是页面参数（策略组名、诊断目标、连接过滤词）。 */
export type Go = (page: Page, arg?: string) => void;

const nav: { id: Page; label: string; icon: LucideIcon }[] = [
  { id: "overview", label: t("概览"), icon: CircleGauge },
  { id: "groups", label: t("策略组"), icon: Waypoints },
  { id: "connections", label: t("连接"), icon: ArrowLeftRight },
  { id: "diagnose", label: t("诊断"), icon: ScanSearch },
  { id: "overlay", label: t("本地覆盖"), icon: FileCode2 },
  { id: "warnings", label: t("告警"), icon: TriangleAlert },
  { id: "settings", label: t("设置"), icon: SettingsIcon },
];

// 手机底部的标签：常用的四页，其余收进「更多」
const phoneTabs: Page[] = ["overview", "groups", "connections", "diagnose"];

// App 打开后这段时间内连不上 velumd 视为仍在启动（App 正在拉起它），不显示错误
const BOOT_GRACE_MS = 20_000;
const openedAt = Date.now();

export default function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const languageSent = useRef(false);
  useEffect(() => {
    if (native.available) native.setLanguage(language).catch(() => undefined);
  }, []);
  const [error, setError] = useState<string>();
  const params = new URLSearchParams(location.search);
  // ?page=groups&arg=Proxy 便于开发预览
  const [page, setPage] = useState<Page>(() => (params.get("page") as Page) ?? "overview");
  const [arg, setArg] = useState<string | undefined>(() => params.get("arg") ?? undefined);
  const [palette, setPalette] = useState(() => params.has("palette"));
  // App 壳的一次性提示（如网络扩展起不来、已退回 TUN 组件）：启动时可能早于窗口出现，定期取一次
  const [notice, setNotice] = useState<string>();
  useEffect(() => {
    if (!native.available) return;
    const take = () => native.noticeTake().then((n) => n && setNotice(n)).catch(() => undefined);
    void take();
    const t = setInterval(take, 3000);
    return () => clearInterval(t);
  }, []);

  const connected = useRef(false);
  const refresh = useCallback(async () => {
    try {
      const st = await api.status();
      setStatus(st);
      // 核心连上后告诉它语言（旧版本的后台服务没有这个接口：忽略）
      if (!languageSent.current) {
        languageSent.current = true;
        api.setLanguage(language).catch(() => undefined);
      }
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
    <div className="flex h-full phone:flex-col">
      {titlebarOverlay && <div data-tauri-drag-region className="fixed inset-x-0 top-0 z-40 h-7" />}
      <header className="hidden h-12 shrink-0 items-center gap-2.5 border-b border-line bg-sidebar pr-1.5 pl-4 phone:flex">
        <StatusMark tone={h.tone} />
        {/* 概览页自己写着状态，这里不重复 */}
        <div className="min-w-0 flex-1 truncate text-[13.5px] font-semibold tracking-[-0.01em]">{page === "overview" ? "Velum" : h.title}</div>
        <IconButton icon={Search} label={t("切换节点、执行操作")} onClick={() => setPalette(true)} />
      </header>
      <aside className={cx("flex w-[208px] shrink-0 flex-col border-r border-line bg-sidebar px-2.5 pb-3 phone:hidden", titlebarOverlay ? "pt-10" : "pt-3")}>
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
          <Search className="size-3.5 shrink-0" strokeWidth={2} />
          <span className="min-w-0 flex-1 truncate whitespace-nowrap text-left">{t("切换节点、执行操作")}</span>
          <Kbd>{isMac ? "⌘K" : "Ctrl K"}</Kbd>
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
                {n.id === "settings" && status?.update?.available && <span title={t("有新版本")} className="size-1.5 rounded-full bg-accent" />}
              </button>
            );
          })}
        </nav>
        <div className="mt-auto px-2 font-mono text-[10.5px] leading-4 text-fg-3">
          {status?.listen && <div className="selectable tnum">{status.listen}</div>}
          {status?.version && <div>velumd {status.version}</div>}
        </div>
      </aside>

      <main className={cx("flex min-h-0 min-w-0 flex-1 flex-col phone:pt-2", titlebarOverlay ? "pt-7" : "pt-3")}>
        {notice && (
          <div className="px-6 pb-2">
            <Notice tone="warn" icon={TriangleAlert} action={<IconButton icon={X} label={t("关闭")} onClick={() => setNotice(undefined)} />}>{notice}</Notice>
          </div>
        )}
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

      <PhoneTabs page={page} status={status} go={go} />

      {palette && <CommandPalette status={status} refresh={refresh} go={go} onClose={() => setPalette(false)} />}
    </div>
  );
}

/** 手机底部导航。 */
function PhoneTabs({ page, status, go }: { page: Page; status: Status | null; go: Go }) {
  const [more, setMore] = useState(false);
  const rest = nav.filter((n) => !phoneTabs.includes(n.id));
  const alert = !!status?.warnings || !!status?.update?.available;
  const tab = (label: string, Icon: LucideIcon, on: boolean, onClick: () => void, dot = false) => (
    <button key={label} onClick={onClick} aria-current={on ? "page" : undefined}
      className={cx("relative flex h-14 flex-col items-center justify-center gap-0.5 text-[11px] transition-colors", on ? "font-medium text-fg" : "text-fg-3")}>
      <Icon className="size-5" strokeWidth={on ? 2 : 1.8} />
      {label}
      {dot && <span className="absolute top-2 left-1/2 ml-2 size-1.5 rounded-full bg-accent" />}
    </button>
  );
  return (
    <>
      <nav className="hidden shrink-0 grid-cols-5 border-t border-line bg-sidebar pb-[env(safe-area-inset-bottom)] phone:grid">
        {nav.filter((n) => phoneTabs.includes(n.id)).map((n) => tab(n.label, n.icon, page === n.id && !more, () => { setMore(false); go(n.id); }))}
        {tab(t("更多"), Ellipsis, more || rest.some((n) => n.id === page), () => setMore(!more), alert)}
      </nav>
      {more && (
        <div className="fixed inset-x-0 top-0 bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-40 hidden flex-col justify-end bg-black/25 phone:flex" onClick={() => setMore(false)}>
          <div className="border-t border-line bg-panel">
            {rest.map((n) => (
              <button key={n.id} onClick={() => { setMore(false); go(n.id); }}
                className={cx("flex h-12 w-full items-center gap-3 border-b border-line px-4 text-left text-[14px] last:border-0", page === n.id && "font-medium")}>
                <n.icon className="size-[18px] text-fg-3" strokeWidth={1.8} />
                <span className="flex-1">{n.label}</span>
                {n.id === "warnings" && !!status?.warnings && <span className="font-mono text-[12px] text-fg-3 tnum">{status.warnings}</span>}
              </button>
            ))}
          </div>
        </div>
      )}
    </>
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
