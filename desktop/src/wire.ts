// 本地接口（/v1）的数据类型：由 velum-engine 的 `velum-tools wire-ts` 从 velumd 的 wire.rs 生成，不要手改。

/** 配置项的出处（ir.Source）。 */
export interface Source {
  kind: string;
  file?: string;
  line?: number;
  text?: string;
}

export interface NetPolicy {
  ssid?: Record<string, string>;
  cellular?: string;
  default?: string;
}

export interface GroupStatus {
  tag: string;
  type: string;
  selected: string;
  members: string[];
  delays?: Record<string, number>;
  policy?: NetPolicy;
}

export interface SelectRequest {
  group: string;
  member: string;
}

export interface TestRequest {
  group?: string;
}

export interface TestResponse {
  /** 毫秒；-1 表示失败 */
  delays: Record<string, number>;
}

export interface OverlayDoc {
  path: string;
  content: string;
}

export interface OverlayEntry {
  kind: string;
  value: string;
  ip?: string;
  line?: number;
}

export interface HostsEntry {
  name: string;
  addrs: string[];
}

export interface OverlayItems {
  path: string;
  entries: OverlayEntry[];
  system_hosts: HostsEntry[];
  system_hosts_total: number;
  system_hosts_off?: boolean;
}

export interface OverlayEdit {
  op: string;
  entry: OverlayEntry;
}

export interface RepairStep {
  name: string;
  ok: boolean;
  detail?: string;
  warn?: boolean;
}

export interface RepairResponse {
  steps: RepairStep[];
}

/** config.yaml 中的订阅来源（url 未脱敏，仅经本机接口提供给本人）。 */
export interface SubscriptionConfig {
  url: string;
  file?: string;
  user_agent?: string;
}

export interface TunApps {
  include_apps: string[];
  exclude_apps: string[];
}

export interface SetSubscriptionRequest {
  url: string;
  user_agent?: string;
}

/** 订阅服务提供的信息（source.Info）。 */
export interface Info {
  upload: number;
  download: number;
  total: number;
  expire?: string;
  title?: string;
  web_page?: string;
  updated_at: string;
}

export interface SubscriptionStatus {
  id?: string;
  name?: string;
  source: string;
  /** remote / cache / file */
  origin: string;
  fetched_at: string;
  fetch_error?: string;
  nodes: number;
  groups: number;
  rules: number;
  rule_sets: number;
  info?: Info;
}

export interface ProfileStatus {
  id: string;
  name: string;
  source: string;
  /** url / file */
  kind: string;
  active: boolean;
  fetched_at?: string;
  info?: Info;
}

export interface AddProfileRequest {
  name?: string;
  url: string;
  activate?: boolean;
}

export interface EditProfileRequest {
  name?: string;
  url?: string;
}

export interface SysProxyStatus {
  supported: boolean;
  active: boolean;
  overridden?: boolean;
  effective?: string;
  primary_interface?: string;
}

export interface HelperStatus {
  socket: string;
  reachable: boolean;
  version: string;
  outdated?: boolean;
  error?: string;
}

export interface SwitchRequest {
  system_proxy?: string;
  /** on / off / config，Linux 系统服务另有 try（试运行）/ confirm（确认试运行） */
  tun?: string;
  /** 试运行的回退时间（tun 为 try 时） */
  trial_deadline?: string;
  /** 发起请求的 SSH 连接（tun 为 try / confirm 时）：确认必须来自另一条连接，才能证明新的入站连接可用 */
  trial_session?: string;
  /** 允许在同一条连接上确认 */
  trial_force?: boolean;
}

/** 进行中的 TUN 试运行。 */
export interface TunTrial {
  deadline: string;
}

export interface NetworkHint {
  kind: string;
  ssid?: string;
}

export interface LanguageRequest {
  language: string;
}

export interface Traffic {
  up: number;
  down: number;
  up_rate: number;
  down_rate: number;
  active: number;
}

export interface DomainFirstRequest {
  enabled: boolean;
}

export interface NetworkStatus {
  primary_interface: string;
  vpns?: Vpn[];
  split_dns_domains?: string[];
  stale_dns?: string[];
  /** online / captive / offline（Captive Portal 探测的结果） */
  captive?: string;
  hint?: NetworkHint;
  captive_detail?: string;
  /** 接管被暂停的原因（Captive Portal、让路给全局型 VPN） */
  paused?: string;
}

export interface Status {
  domain_first?: boolean;
  tun_engine?: string;
  /** 网页界面实际监听的地址；没有启动时 web_error 是原因 */
  web_listen?: string;
  web_error?: string;
  pid: number;
  version: string;
  running: boolean;
  /** proxy / sysproxy / tun / tun+sysproxy（实际生效） */
  mode: string;
  system_proxy_on: boolean;
  tun_on: boolean;
  want_system_proxy: boolean;
  want_tun: boolean;
  /** TUN 正在试运行（到时没确认就回退）；tun_reverted 说明上一次试运行为何被回退 */
  tun_trial?: TunTrial;
  tun_reverted?: string;
  overridden?: boolean;
  sys_proxy: SysProxyStatus;
  /** TUN 正在运行时它的网卡、地址与路由 */
  tun?: TunStatus;
  network?: NetworkStatus;
  traffic?: Traffic;
  listen: string;
  home: string;
  started_at: string;
  starting?: boolean;
  loaded_at: string;
  subscription: SubscriptionStatus;
  profiles?: ProfileStatus[];
  warnings: number;
  last_error?: string;
  groups: GroupStatus[];
  /** 还没检查过新版本时为空 */
  update?: UpdateStatus;
}

/** 最近一次检查新版本的结果。 */
export interface UpdateStatus {
  current: string;
  /** stable / beta */
  channel: string;
  latest?: string;
  available: boolean;
  url?: string;
  download?: string;
  manifest?: string;
  checked_at: string;
  error?: string;
}

export interface ChannelRequest {
  channel?: string;
}

export interface WhyRequest {
  target: string;
}

export interface WhyTarget {
  raw: string;
  domain?: string;
  /** Go 的 netip.Addr 不会被 omitempty 省略：没有时也写空串 */
  ip: string;
  port: number;
}

export interface TunStatus {
  name?: string;
  address: string;
  address6?: string;
  routes: string[];
  /** 非空时系统 DNS 指向这些地址（由 TUN 接管） */
  system_dns?: string[];
}

export interface WhySysProxy {
  /** 正在接管系统代理 */
  enabled: boolean;
  /** 命中系统代理豁免：遵守系统代理的应用直接连接，不经过 Velum */
  bypassed: boolean;
  pattern?: string;
  source?: Source;
}

export interface WhyDns {
  applicable: boolean;
  /** 交给哪个服务器；hosts 指定时为 hosts */
  server?: string;
  /** hosts 指定的地址 */
  answer?: string[];
  /** 指定这些地址的条目 */
  source?: Source;
}

export interface WhyDecision {
  /** 配置中的规则下标；-1 表示 final */
  rule_index: number;
  action: string;
  outbound?: string;
  /** 目标被改写成的地址（hosts 覆盖） */
  override_address?: string;
  source: Source;
}

export interface WhyReport {
  target: WhyTarget;
  sys_proxy: WhySysProxy;
  dns: WhyDns;
  /** 逐条推演：由运行中的引擎回答后不再提供（结论与真实连接相同，见 velum-core 的 why） */
  rules: unknown[];
  decision: WhyDecision;
  notes?: string[];
}

export interface WhyResponse {
  report: WhyReport;
  chain?: string[];
  resolved?: string[];
  resolved_by?: string;
  resolve_error?: string;
}

/** 一条连接（engine.Connection）。 */
export interface Connection {
  id: number;
  start: string;
  end?: string;
  network: string;
  inbound: string;
  source: string;
  domain?: string;
  destination: string;
  rule_index: number;
  rule: string;
  provenance: Source;
  chain: string[];
  upload: number;
  download: number;
  connect_ms: number;
  first_byte_ms: number;
  duration_ms: number;
  errors?: string[];
}

export interface ConnectionsResponse {
  connections: Connection[];
}

/** 一条告警（ir.Warning）。 */
export interface Warning {
  category: string;
  kind: string;
  level: string;
  reason: string;
  subject?: string;
  source: Source;
}

export interface WarningsResponse {
  warnings: Warning[];
}

export interface ErrorResponse {
  error: string;
}

/** 检测到的、不属于 Velum 的隧道接口。 */
export interface Vpn {
  iface: string;
  /** 占据主路由（全局型） */
  full: boolean;
  routes?: string[];
  domains?: string[];
  nameservers?: string[];
}
