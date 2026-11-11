import { t } from "./i18n";
// velumd 本地接口的类型与客户端。数据类型由 velum-engine 从 velumd 的 wire.rs 生成（wire.ts）；
// 这里只把几个字符串字段收窄成前端用的取值，并补上只有 Go 核心才发送的字段（标了「Go 核心」，移除 Go 核心时删掉）。
// Tauri 中经 Rust 命令访问 Unix socket；浏览器开发模式下经 Vite 代理（/api）。
import type * as W from "./wire";

import type { Source, Warning, HelperStatus, RepairStep } from "./wire";

export type { Source, NetPolicy, GroupStatus, NetworkStatus, Warning, HelperStatus, RepairStep } from "./wire";
export type VPN = W.Vpn;
/** 订阅服务提供的流量额度与到期时间（subscription-userinfo 等响应头）。 */
export type SubInfo = W.Info;
export interface TUNStatus extends W.TunStatus {
  /** Go 核心 */
  exclude?: string[];
}
/** 订阅列表中的一项（profiles.yaml）；active 为正在使用的订阅。 */
export interface Profile extends Omit<W.ProfileStatus, "kind"> { kind: "url" | "file" }
export interface Status extends Omit<W.Status, "profiles" | "update" | "tun"> {
  profiles?: Profile[];
  tun?: TUNStatus;
  /** 最近一次检查新版本的结果；尚未检查过时为空。 */
  update?: UpdateStatus;
}
export interface UpdateStatus extends Omit<W.UpdateStatus, "channel"> { channel: UpdateChannel }
export type UpdateChannel = "stable" | "beta";
/** 自动更新进度（App 内）。 */
export interface UpdateProgress { stage: "download" | "install" | "error"; downloaded: number; total?: number; error?: string }
export interface Connection extends Omit<W.Connection, "connect_ms"> {
  /** 没有连上时 Go 核心不发送，Rust 核心为 -1 */
  connect_ms?: number;
  /** Go 核心 */
  process?: string; resolved?: string[]; sniffed?: string;
}
/** 逐条推演中的一条规则（Go 核心；Rust 核心由运行中的引擎直接给出结论，不逐条推演）。 */
export interface RuleTrace { index: number; action: string; matched: boolean; unknown?: boolean; reason: string; source: W.Source }
export interface WhyReport extends Omit<W.WhyReport, "rules" | "decision"> {
  rules: RuleTrace[];
  /** uncertain：Go 核心 */
  decision: W.WhyDecision & { uncertain?: boolean };
}
export interface WhyResponse extends Omit<W.WhyResponse, "report"> {
  report: WhyReport;
  /** Go 核心 */
  rule_set_errors?: Record<string, string>;
}
export interface Traffic extends W.Traffic {}
/** overlay 条目。domain 的 value 用 overlay 写法：x = 根域 + 子域，.x = 仅子域，=x = 仅此域名。 */
export type OverlayKind = "domain" | "cidr" | "host" | "process" | "bypass";
export interface OverlayEntry extends Omit<W.OverlayEntry, "kind"> { kind: OverlayKind }
export interface OverlayItems extends Omit<W.OverlayItems, "entries"> { entries: OverlayEntry[] }

const inTauri = typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  if (inTauri) {
    const { invoke } = await import("@tauri-apps/api/core");
    return invoke<T>("velum_call", { method, path, body: body === undefined ? null : JSON.stringify(body) });
  }
  const res = await fetch("/api" + path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error((data as { error?: string }).error ?? res.statusText);
  return data as T;
}

export const api = {
  status: () => call<Status>("GET", "/v1/status"),
  helper: () => call<HelperStatus>("GET", "/v1/helper"),
  reload: () => call<Status>("POST", "/v1/reload"),
  /** 核心返回的错误、告警跟随界面语言。 */
  setLanguage: (language: string) => call<Status>("PUT", "/v1/language", { language }),
  setDomainFirst: (enabled: boolean) => call<Status>("PUT", "/v1/rules/domain-first", { enabled }),
  update: () => call<Status>("POST", "/v1/update"),
  /** 运行时切换开关（不写入 config.yaml）：on / off / config（回到配置文件的设置）。两个开关相互独立。 */
  setSwitch: (which: "system_proxy" | "tun", value: "on" | "off" | "config") => call<Status>("POST", "/v1/switches", { [which]: value }),
  why: (target: string, opts: { resolved?: string[]; process?: string; network?: string; no_resolve?: boolean } = {}) =>
    call<WhyResponse>("POST", "/v1/why", { target, ...opts }),
  connections: (limit = 200, host = "", active = false) =>
    call<{ connections: Connection[] | null }>("GET", `/v1/connections?limit=${limit}${host ? `&host=${encodeURIComponent(host)}` : ""}${active ? "&active=1" : ""}`),
  /** 实时流量：总字节数与最近一秒的速率（字节 / 秒）。 */
  traffic: () => call<Traffic>("GET", "/v1/traffic"),
  warnings: () => call<{ warnings: Warning[] | null }>("GET", "/v1/warnings"),
  select: (group: string, member: string) => call<Status>("POST", "/v1/groups/select", { group, member }),
  test: (group: string) => call<{ delays: Record<string, number> }>("POST", "/v1/groups/test", { group }),
  overlay: () => call<{ path: string; content: string }>("GET", "/v1/overlay"),
  subscription: () => call<{ url: string; file?: string; user_agent?: string }>("GET", "/v1/subscription"),
  /** 先拉取并校验，通过后才写入 config.yaml；空字符串表示清除订阅。 */
  setSubscription: (url: string) => call<Status>("PUT", "/v1/subscription", { url }),
  /** 添加订阅：先拉取并校验；activate 为 true 时随即切换到它。 */
  addProfile: (url: string, name?: string, activate?: boolean) => call<Status>("POST", "/v1/profiles", { url, name, activate }),
  editProfile: (id: string, edit: { name?: string; url?: string }) => call<Status>("PUT", `/v1/profiles/${id}`, edit),
  deleteProfile: (id: string) => call<Status>("DELETE", `/v1/profiles/${id}`),
  /** 切换到该订阅（重新加载；失败时保持原订阅）。 */
  activateProfile: (id: string) => call<Status>("POST", `/v1/profiles/${id}/activate`),
  /** 重新拉取订阅，同时更新流量额度。 */
  updateProfile: (id: string) => call<Status>("POST", `/v1/profiles/${id}/update`),
  saveOverlay: (content: string) => call<Status>("PUT", "/v1/overlay", { content }),
  overlayItems: () => call<OverlayItems>("GET", "/v1/overlay/items"),
  /** 按条目增删 overlay（保留文件中的注释），随后重新加载；失败时自动改回。 */
  editOverlay: (op: "add" | "remove", entry: OverlayEntry) => call<Status>("POST", "/v1/overlay/items", { op, entry }),
  repair: () => call<{ steps: RepairStep[] }>("POST", "/v1/repair"),
  /** 立即检查新版本（GitHub Releases）。 */
  checkRelease: () => call<UpdateStatus>("POST", "/v1/release/check"),
  /** 切换更新通道并立即检查。 */
  setChannel: (channel: UpdateChannel) => call<UpdateStatus>("PUT", "/v1/release/channel", { channel }),
};

/** 订阅连接结束事件。Tauri 中由 Rust 转发 SSE；浏览器中直接用 EventSource。返回取消函数。 */
export function subscribeConnections(fn: (c: Connection) => void): () => void {
  if (inTauri) {
    let unlisten: (() => void) | undefined;
    let cancelled = false;
    import("@tauri-apps/api/event").then(({ listen }) =>
      listen<Connection>("velum://connection", (e) => fn(e.payload)).then((u) => (cancelled ? u() : (unlisten = u))),
    );
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }
  const es = new EventSource("/api/v1/connections/stream");
  es.onmessage = (e) => fn(JSON.parse(e.data));
  return () => es.close();
}

export function srcText(s?: Source): string {
  if (!s) return "";
  let loc = s.file ? s.file.split("/").pop()! : s.kind;
  if (s.line) loc += `:${s.line}`;
  return s.text ? `${loc}  ${s.text}` : loc;
}

/** 速率：1.2 MB/s、30 KB/s。 */
export function rate(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB/s";
  if (n >= 1 << 10) return Math.round(n / (1 << 10)) + " KB/s";
  return Math.round(n) + " B/s";
}

export function human(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + "M";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + "K";
  return n + "B";
}

async function nativeCall<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  if (!inTauri) throw new Error(t("浏览器预览中不可用，请在 Velum App 中操作（或在终端执行 sudo velum-helper）"));
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<T>(cmd, args);
}

export interface CliStatus { installed: boolean; path?: string; conflict?: string }
/** macOS 网络扩展（实验）：bundled 为 App 是否内置扩展（签名构建才有），enabled 为是否正在使用。 */
export interface NetextStatus { bundled: boolean; enabled: boolean }

/** 只在 App 中可用的系统操作（由 Rust 壳实现）。 */
export const native = {
  available: inTauri,
  /** 托盘和系统提示跟随界面语言。 */
  setLanguage: (language: string) => nativeCall<void>("set_language", { language }),
  /** 安装 / 更新 TUN 组件（会请求管理员权限）。 */
  helperInstall: () => nativeCall<void>("helper_install"),
  helperUninstall: () => nativeCall<void>("helper_uninstall"),
  /** 平台自行判断的 TUN 组件状态（Windows：Velum 服务）；null 表示直接查询 velumd（macOS）。 */
  helperStatus: () => nativeCall<HelperStatus | null>("helper_status"),
  /** 命令行工具 vlu：macOS 链接到 /usr/local/bin（需管理员权限），Windows 把安装目录加入用户 PATH。 */
  cliStatus: () => nativeCall<CliStatus>("cli_status"),
  cliInstall: () => nativeCall<void>("cli_install"),
  cliUninstall: () => nativeCall<void>("cli_uninstall"),
  /** 网络扩展（实验）：开启时激活扩展（首次需在系统设置中批准）、停止 velumd，核心改在扩展里运行；关闭时切回 velumd。 */
  netextStatus: () => nativeCall<NetextStatus>("netext_status"),
  netextSet: (on: boolean) => nativeCall<void>("netext_set", { on }),
  /** 一次性提示（如网络扩展起不来、已退回 TUN 组件的原因）；取走后清空。 */
  noticeTake: () => nativeCall<string | null>("notice_take"),
  autostartGet: () => nativeCall<boolean>("autostart_get"),
  autostartSet: (on: boolean) => nativeCall<void>("autostart_set", { on }),
  /** 用默认浏览器打开发布页或安装包。 */
  openRelease: (url: string) => nativeCall<void>("open_release", { url }),
  /** 下载、校验并安装更新，完成后 App 重启。进度见 onUpdateProgress。 */
  installUpdate: (manifest: string) => nativeCall<void>("install_update", { manifest }),
  updateProgress: () => nativeCall<UpdateProgress | null>("update_progress"),
};

/** 订阅自动更新进度；返回取消函数。 */
export function onUpdateProgress(fn: (p: UpdateProgress | null) => void): () => void {
  if (!inTauri) return () => {};
  let unlisten: (() => void) | undefined;
  let cancelled = false;
  import("@tauri-apps/api/event").then(({ listen }) =>
    listen<UpdateProgress | null>("velum://update", (e) => fn(e.payload)).then((u) => (cancelled ? u() : (unlisten = u))),
  );
  return () => {
    cancelled = true;
    unlisten?.();
  };
}

/** 在浏览器里打开（Linux 服务器的网页界面）：没有系统代理和需要安装的组件，升级用 vlu upgrade。 */
export const isWeb = !inTauri;

export const isWindows = typeof navigator !== "undefined" && /Windows/.test(navigator.userAgent);

/** TUN 组件状态：macOS 为 velum-helper（velumd 查询），Windows 为 Velum 服务（App 查询服务状态）。 */
export async function componentStatus(): Promise<HelperStatus> {
  if (native.available) {
    const s = await native.helperStatus().catch(() => null);
    if (s) return s;
  }
  return api.helper();
}

/** TUN 组件的说明文案（按平台）。 */
export const componentText = isWindows
  ? {
      name: t("Velum 服务"),
      brief: t("Velum 服务：以系统权限运行 Velum 核心，负责虚拟网卡与路由；只接受当前用户的请求"),
      detail: t("Velum 服务以系统权限运行 Velum 核心，负责创建虚拟网卡和设置路由，并且只接受你这个用户的请求。安装时 Windows 会请求一次管理员授权。"),
      log: "%ProgramData%\\Velum\\logs\\velumd.log",
    }
  : {
      name: "velum-helper",
      brief: t("velum-helper：以管理员身份运行，负责虚拟网卡、路由与 DNS；只接受当前用户的请求"),
      detail: t("velum-helper 以管理员身份在后台运行，只负责创建虚拟网卡、设置路由和 DNS，并且只接受你这个用户的请求。安装时 macOS 会要求输入一次管理员密码。"),
      log: "/Library/Logs/velum-helper.log",
    };

export const errText = (e: unknown) => {
  const msg = typeof e === "string" ? e : String((e as Error)?.message ?? e);
  // 旧版本的后台服务没有新功能的接口（如 Windows 服务没有随 App 更新）
  return msg === "HTTP 404" ? t("后台服务版本过旧，不支持这个功能：请先更新后台服务（概览页的提示，或设置 → 系统组件）") : msg;
};

/** App 自身的版本（Tauri 中可用）；用于发现仍在运行的旧版本后台服务。 */
export let appVersion = "";
if (inTauri) {
  import("@tauri-apps/api/app").then((m) => m.getVersion()).then((v) => (appVersion = v)).catch(() => undefined);
}

/** 后台服务（velumd）与 App 版本不同：Windows 服务需要单独更新；macOS 为更新前启动的 velumd 仍在运行。开发版本不比较。 */
export function staleDaemon(s: Status | null): boolean {
  return !!(s?.version && appVersion && s.version !== appVersion && !s.version.endsWith("-dev") && !appVersion.endsWith("-dev"));
}
