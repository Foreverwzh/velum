// velumd 本地接口的类型与客户端。与 core/pkg/rpc 保持一致。
// Tauri 中经 Rust 命令访问 Unix socket；浏览器开发模式下经 Vite 代理（/api）。

export interface Source { kind: string; file?: string; line?: number; text?: string }
export interface GroupStatus { tag: string; type: string; selected: string; members: string[]; delays?: Record<string, number> }
export interface VPN { iface: string; full: boolean; routes?: string[]; domains?: string[]; nameservers?: string[] }
export interface NetworkStatus {
  primary_interface: string; vpns?: VPN[]; split_dns_domains?: string[];
  captive?: string; captive_detail?: string; paused?: string;
}
export interface TUNStatus { name?: string; address: string; address6?: string; routes: string[]; exclude?: string[]; system_dns?: string[] }
export interface Status {
  pid: number; version: string; running: boolean; mode: string; listen: string; home: string;
  system_proxy_on: boolean; tun_on: boolean; want_system_proxy: boolean; want_tun: boolean; overridden?: boolean;
  /** 首次加载配置、启动引擎尚未完成（其余字段可能为空）。 */
  starting?: boolean;
  started_at: string; loaded_at: string; warnings: number; last_error?: string; groups?: GroupStatus[];
  subscription: { source: string; origin: string; fetched_at: string; fetch_error?: string; nodes: number; groups: number; rules: number; rule_sets: number };
  sys_proxy: { supported: boolean; active: boolean; overridden?: boolean; effective?: string; primary_interface?: string };
  tun?: TUNStatus; network?: NetworkStatus;
}
export interface Connection {
  id: number; start: string; end?: string; network: string; inbound: string; source: string; process?: string;
  domain?: string; destination: string; resolved?: string[]; sniffed?: string; rule_index: number; rule: string;
  provenance: Source; chain: string[]; upload: number; download: number; first_byte_ms: number; duration_ms: number;
  errors?: string[]; untracked?: boolean;
}
export interface RuleTrace { index: number; action: string; matched: boolean; unknown?: boolean; reason: string; source: Source }
export interface WhyReport {
  target: { raw: string; domain?: string; ip?: string; port: number };
  sys_proxy: { enabled: boolean; bypassed: boolean; pattern?: string; source?: Source };
  dns: { applicable: boolean; server?: string; answer?: string[]; source?: Source };
  rules: RuleTrace[];
  decision: { rule_index: number; action: string; outbound?: string; override_address?: string; source: Source; uncertain?: boolean };
  notes?: string[];
}
export interface WhyResponse { report: WhyReport; chain?: string[]; resolved?: string[]; resolved_by?: string; resolve_error?: string; rule_set_errors?: Record<string, string> }
export interface Warning { category: string; kind: string; level: string; reason: string; subject?: string; source: Source }
export interface HelperStatus { socket: string; reachable: boolean; version: string; outdated?: boolean; error?: string }
export interface RepairStep { name: string; ok: boolean; detail?: string }

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
  update: () => call<Status>("POST", "/v1/update"),
  /** 运行时切换开关（不写入 config.yaml）：on / off / config（回到配置文件的设置）。两个开关相互独立。 */
  setSwitch: (which: "system_proxy" | "tun", value: "on" | "off" | "config") => call<Status>("POST", "/v1/switches", { [which]: value }),
  why: (target: string, opts: { resolved?: string[]; process?: string; network?: string; no_resolve?: boolean } = {}) =>
    call<WhyResponse>("POST", "/v1/why", { target, ...opts }),
  connections: (limit = 200, host = "") =>
    call<{ connections: Connection[] | null }>("GET", `/v1/connections?limit=${limit}${host ? `&host=${encodeURIComponent(host)}` : ""}`),
  warnings: () => call<{ warnings: Warning[] | null }>("GET", "/v1/warnings"),
  select: (group: string, member: string) => call<Status>("POST", "/v1/groups/select", { group, member }),
  test: (group: string) => call<{ delays: Record<string, number> }>("POST", "/v1/groups/test", { group }),
  overlay: () => call<{ path: string; content: string }>("GET", "/v1/overlay"),
  subscription: () => call<{ url: string; file?: string; user_agent?: string }>("GET", "/v1/subscription"),
  /** 先拉取并校验，通过后才写入 config.yaml；空字符串表示清除订阅。 */
  setSubscription: (url: string) => call<Status>("PUT", "/v1/subscription", { url }),
  saveOverlay: (content: string) => call<Status>("PUT", "/v1/overlay", { content }),
  repair: () => call<{ steps: RepairStep[] }>("POST", "/v1/repair"),
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

export function human(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + "M";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + "K";
  return n + "B";
}

async function nativeCall<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  if (!inTauri) throw new Error("浏览器预览中不可用，请在 Velum App 中操作（或在终端执行 sudo velum-helper）");
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<T>(cmd, args);
}

export interface CliStatus { installed: boolean; path?: string; conflict?: string }

/** 只在 App 中可用的系统操作（由 Rust 壳实现）。 */
export const native = {
  available: inTauri,
  /** 安装 / 更新 TUN 组件（会请求管理员权限）。 */
  helperInstall: () => nativeCall<void>("helper_install"),
  helperUninstall: () => nativeCall<void>("helper_uninstall"),
  /** 平台自行判断的 TUN 组件状态（Windows：Velum 服务）；null 表示直接查询 velumd（macOS）。 */
  helperStatus: () => nativeCall<HelperStatus | null>("helper_status"),
  /** 命令行工具 vlu：macOS 链接到 /usr/local/bin（需管理员权限），Windows 把安装目录加入用户 PATH。 */
  cliStatus: () => nativeCall<CliStatus>("cli_status"),
  cliInstall: () => nativeCall<void>("cli_install"),
  cliUninstall: () => nativeCall<void>("cli_uninstall"),
  autostartGet: () => nativeCall<boolean>("autostart_get"),
  autostartSet: (on: boolean) => nativeCall<void>("autostart_set", { on }),
};

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
      name: "Velum 服务",
      brief: "Velum 服务：以系统权限运行 Velum 核心，负责虚拟网卡与路由；只接受当前用户的请求",
      detail: "Velum 服务以系统权限运行 Velum 核心，负责创建虚拟网卡和设置路由，并且只接受你这个用户的请求。安装时 Windows 会请求一次管理员授权。",
      log: "%ProgramData%\\Velum\\logs\\velumd.log",
    }
  : {
      name: "velum-helper",
      brief: "velum-helper：以管理员身份运行，负责虚拟网卡、路由与 DNS；只接受当前用户的请求",
      detail: "velum-helper 以管理员身份在后台运行，只负责创建虚拟网卡、设置路由和 DNS，并且只接受你这个用户的请求。安装时 macOS 会要求输入一次管理员密码。",
      log: "/Library/Logs/velum-helper.log",
    };

export const errText = (e: unknown) => (typeof e === "string" ? e : String((e as Error)?.message ?? e));
