import { forwardRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";
import { ChevronRight, Loader2, Search, type LucideIcon } from "lucide-react";

export function cx(...xs: (string | false | null | undefined)[]) {
  return xs.filter(Boolean).join(" ");
}

/**
 * macOS 上窗口使用 Overlay 标题栏（交通灯叠在内容上），需要为其留出空间并提供拖动区域。
 * 浏览器预览可用 ?titlebar=overlay 模拟。
 */
export const titlebarOverlay =
  new URLSearchParams(location.search).get("titlebar") === "overlay" ||
  ("__TAURI_INTERNALS__" in window && /Mac/.test(navigator.userAgent));

export const isMac = typeof navigator !== "undefined" && /Mac/.test(navigator.userAgent);
export const modKey = isMac ? "⌘" : "Ctrl";

// ---------------- 按钮 ----------------

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md";
  icon?: LucideIcon;
  loading?: boolean;
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", icon: Icon, loading, className, children, disabled, ...rest },
  ref,
) {
  const styles = {
    primary: "bg-accent text-on-accent hover:bg-accent-strong shadow-raise",
    secondary: "border border-line-strong bg-raised text-fg hover:bg-hover shadow-raise",
    ghost: "text-fg-2 hover:bg-hover hover:text-fg",
    danger: "border border-line-strong bg-raised text-err hover:bg-err-soft",
  }[variant];
  return (
    <button
      ref={ref}
      {...rest}
      disabled={disabled || loading}
      className={cx(
        "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors duration-150",
        "disabled:pointer-events-none disabled:opacity-45",
        size === "sm" ? "h-6 px-2 text-[12px]" : "h-7 px-2.5 text-[12.5px]",
        styles,
        className,
      )}
    >
      {loading ? <Loader2 className="size-3.5 animate-spin" /> : Icon && <Icon className="size-3.5" strokeWidth={2} />}
      {children}
    </button>
  );
});

export function IconButton({ icon: Icon, label, className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: LucideIcon; label: string }) {
  return (
    <button
      {...rest}
      aria-label={label}
      title={label}
      className={cx("inline-flex size-7 items-center justify-center rounded-md text-fg-3 transition-colors hover:bg-hover hover:text-fg disabled:opacity-40", className)}
    >
      <Icon className="size-4" strokeWidth={1.8} />
    </button>
  );
}

// ---------------- 开关 ----------------

/** 开关：checked 是用户意图；paused 表示已开启但当前被暂停（琥珀色）。 */
export function Switch({ checked, paused, disabled, onChange, label }: {
  checked: boolean; paused?: boolean; disabled?: boolean; onChange: (v: boolean) => void; label: string;
}) {
  return (
    <button
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "relative h-[18px] w-[30px] shrink-0 rounded-full transition-colors duration-200 disabled:opacity-45",
        checked ? (paused ? "bg-warn" : "bg-accent") : "bg-line-strong",
      )}
    >
      <span
        className={cx(
          "absolute top-[2px] size-[14px] rounded-full bg-white shadow-[0_1px_2px_rgba(0,0,0,0.3)] transition-[left] duration-200 ease-out-expo",
          checked ? "left-[14px]" : "left-[2px]",
        )}
      />
    </button>
  );
}

// ---------------- 标签与状态 ----------------

export type Tone = "neutral" | "accent" | "warn" | "err" | "info";

export function Tag({ tone = "neutral", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  const t = {
    neutral: "bg-hover text-fg-2",
    accent: "bg-accent-soft text-accent",
    warn: "bg-warn-soft text-warn",
    err: "bg-err-soft text-err",
    info: "bg-info-soft text-info",
  }[tone];
  return <span className={cx("inline-flex h-[18px] shrink-0 items-center gap-1 rounded px-1.5 text-[11px] font-medium whitespace-nowrap", t, className)}>{children}</span>;
}

export function delayTone(ms?: number): Tone {
  if (ms === undefined || ms === 0) return "neutral";
  if (ms < 0) return "err";
  if (ms < 300) return "accent";
  if (ms < 800) return "warn";
  return "err";
}

/** 延迟：色点 + 数字（不只靠颜色）；未测速不显示，-1 为超时。 */
export function Latency({ ms, className }: { ms?: number; className?: string }) {
  if (ms === undefined || ms === 0) return <span className={cx("text-[11.5px] text-fg-3", className)}>—</span>;
  const tone = delayTone(ms);
  const dot = { accent: "bg-accent", warn: "bg-warn", err: "bg-err", neutral: "bg-fg-3", info: "bg-info" }[tone];
  const text = { accent: "text-fg-2", warn: "text-warn", err: "text-err", neutral: "text-fg-3", info: "text-info" }[tone];
  return (
    <span className={cx("inline-flex items-center gap-1.5 font-mono text-[11.5px] tnum whitespace-nowrap", text, className)}>
      <span className={cx("size-1.5 rounded-full", dot)} />
      {ms < 0 ? "超时" : `${ms} ms`}
    </span>
  );
}

/** 状态标识：Velum 的状态环，用于侧栏与总览。 */
export function StatusMark({ tone, size = 10 }: { tone: "green" | "amber" | "red" | "gray"; size?: number }) {
  const c = { green: "var(--accent)", amber: "var(--warn)", red: "var(--err)", gray: "var(--fg-3)" }[tone];
  return (
    <span className="relative inline-flex shrink-0" style={{ width: size, height: size }}>
      {tone === "green" && <span className="absolute inset-0 animate-[ping_2.4s_cubic-bezier(0,0,0.2,1)_infinite] rounded-full opacity-30" style={{ background: c }} />}
      <span className="relative rounded-full" style={{ width: size, height: size, background: c, boxShadow: `0 0 0 3px color-mix(in srgb, ${c} 18%, transparent)` }} />
    </span>
  );
}

// ---------------- 路由链（Velum 的签名样式）----------------

export interface ChainStep {
  label: ReactNode;
  kind?: "rule" | "group" | "node" | "direct" | "reject";
  title?: string;
}

/** 规则 → 策略组 → 节点：Velum 处处用同一种写法说明「这条流量为什么走这里」。 */
export function Chain({ steps, size = "md", className }: { steps: ChainStep[]; size?: "sm" | "md"; className?: string }) {
  return (
    <span className={cx("inline-flex min-w-0 items-center gap-0.5", className)}>
      {steps.map((s, i) => (
        <span key={i} className={cx("inline-flex min-w-0 items-center gap-0.5", i === steps.length - 1 ? "shrink" : "shrink-[3]")}>
          {i > 0 && <ChevronRight className="size-3 shrink-0 text-fg-3" strokeWidth={2} />}
          <span
            title={s.title ?? (typeof s.label === "string" ? s.label : undefined)}
            className={cx(
              "truncate rounded px-1.5",
              size === "sm" ? "h-[18px] text-[11.5px] leading-[18px]" : "h-5 text-[12px] leading-5",
              s.kind === "rule" && "bg-info-soft font-mono text-info",
              s.kind === "group" && "bg-hover text-fg-2",
              s.kind === "node" && "bg-accent-soft font-medium text-fg",
              s.kind === "direct" && "bg-hover font-medium text-fg",
              s.kind === "reject" && "bg-err-soft font-medium text-err",
              !s.kind && "text-fg-2",
            )}
          >
            {s.label}
          </span>
        </span>
      ))}
    </span>
  );
}

/** 出站链（如 ["Proxy","Auto","HK 01"]）转成路由链步骤。 */
export function chainSteps(chain: string[], rule?: { label: string; title?: string }): ChainStep[] {
  const steps: ChainStep[] = [];
  if (rule) steps.push({ label: rule.label, kind: "rule", title: rule.title });
  chain.forEach((c, i) => {
    const last = i === chain.length - 1;
    const kind: ChainStep["kind"] = c === "DIRECT" ? "direct" : c === "REJECT" || c.startsWith("reject") ? "reject" : last ? "node" : "group";
    steps.push({ label: c === "REJECT" || c.startsWith("reject") ? "拦截" : c, kind });
  });
  return steps;
}

// ---------------- 输入 ----------------

export const SearchInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { shortcut?: string }>(function SearchInput(
  { className, shortcut, ...rest },
  ref,
) {
  return (
    <label className={cx("flex h-7 items-center gap-2 rounded-md border border-line bg-panel px-2 transition-colors focus-within:border-accent", className)}>
      <Search className="size-3.5 shrink-0 text-fg-3" strokeWidth={2} />
      <input ref={ref} {...rest} spellCheck={false} className="min-w-0 flex-1 bg-transparent text-[12.5px] outline-none placeholder:text-fg-3" />
      {shortcut && <Kbd>{shortcut}</Kbd>}
    </label>
  );
});

export function TextInput({ className, invalid, ...rest }: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      {...rest}
      spellCheck={false}
      className={cx(
        "h-8 w-full rounded-md border bg-panel px-2.5 text-[12.5px] outline-none transition-colors placeholder:text-fg-3 focus:border-accent",
        invalid ? "border-err" : "border-line-strong",
        className,
      )}
    />
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="inline-flex h-[18px] min-w-[18px] items-center justify-center rounded border border-line bg-hover px-1 font-sans text-[10.5px] text-fg-3">{children}</kbd>;
}

// ---------------- 版式 ----------------

export function PageHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="flex min-h-[52px] items-center justify-between gap-4 border-b border-line px-6 pb-3">
      <div className="min-w-0">
        <h1 className="text-[15px] font-semibold tracking-[-0.01em]">{title}</h1>
        {description && <p className="mt-0.5 truncate text-[12px] text-fg-3">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </header>
  );
}

export function Section({ title, actions, children, className }: { title: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={className}>
      <div className="mb-2 flex h-7 items-center justify-between gap-3">
        <h2 className="text-[12px] font-semibold text-fg-2">{title}</h2>
        {actions && <div className="flex items-center gap-1.5">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

/** 细线框起来的一组行（不是卡片）。 */
export function Panel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("overflow-hidden rounded-lg border border-line bg-panel", className)}>{children}</div>;
}

export function Row({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cx("flex min-h-[44px] items-center justify-between gap-4 border-b border-line px-3.5 py-2 last:border-b-0", className)}>
      <div className="min-w-0">
        <div className="font-medium">{label}</div>
        {hint && <div className="mt-0.5 text-[12px] text-fg-3">{hint}</div>}
      </div>
      {children !== undefined && <div className="flex shrink-0 items-center gap-2">{children}</div>}
    </div>
  );
}

export function KV({ rows }: { rows: [ReactNode, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[88px_1fr] gap-x-4 gap-y-2 text-[12.5px]">
      {rows.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-fg-3">{k}</dt>
          <dd className="min-w-0 text-fg">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Empty({ icon: Icon, title, hint, action }: { icon?: LucideIcon; title: string; hint?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-14 text-center">
      {Icon && (
        <span className="mb-3 flex size-9 items-center justify-center rounded-lg border border-line bg-panel text-fg-3">
          <Icon className="size-4" strokeWidth={1.8} />
        </span>
      )}
      <div className="font-medium">{title}</div>
      {hint && <div className="mt-1 max-w-[360px] text-[12px] text-fg-3">{hint}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Notice({ tone = "info", icon: Icon, children, action }: { tone?: "info" | "warn" | "err" | "accent"; icon?: LucideIcon; children: ReactNode; action?: ReactNode }) {
  const t = { info: "bg-info-soft text-info", warn: "bg-warn-soft text-warn", err: "bg-err-soft text-err", accent: "bg-accent-soft text-accent" }[tone];
  return (
    <div className={cx("flex items-start gap-2.5 rounded-lg px-3 py-2.5 text-[12.5px]", t)}>
      {Icon && <Icon className="mt-[2px] size-3.5 shrink-0" strokeWidth={2} />}
      <div className="selectable min-w-0 flex-1 text-fg">{children}</div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}
