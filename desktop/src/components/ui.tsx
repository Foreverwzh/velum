import type { ReactNode, ButtonHTMLAttributes } from "react";

export function cx(...xs: (string | false | null | undefined)[]) {
  return xs.filter(Boolean).join(" ");
}

export function Card({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("rounded-xl border border-neutral-200 bg-white shadow-xs dark:border-neutral-800 dark:bg-neutral-850 dark:bg-neutral-800/60", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between border-b border-neutral-100 px-4 py-2.5 dark:border-neutral-800">
          <h3 className="text-[13px] font-semibold">{title}</h3>
          <div className="flex items-center gap-2">{actions}</div>
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function Button({ variant = "default", className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "default" | "primary" | "ghost" | "danger" }) {
  const styles = {
    default: "border border-neutral-200 bg-white hover:bg-neutral-50 dark:border-neutral-700 dark:bg-neutral-800 dark:hover:bg-neutral-700",
    primary: "bg-blue-600 text-white shadow-sm hover:bg-blue-700 active:bg-blue-800",
    ghost: "hover:bg-neutral-200/60 dark:hover:bg-neutral-700/60",
    danger: "border border-red-200 bg-white text-red-600 hover:bg-red-50 dark:border-red-900 dark:bg-transparent dark:text-red-400",
  }[variant];
  return (
    <button
      {...rest}
      className={cx("inline-flex h-7 items-center gap-1.5 rounded-md px-3 text-[12.5px] font-medium transition disabled:cursor-not-allowed disabled:opacity-50", styles, className)}
    />
  );
}

export function Segmented<T extends string>({ value, options, onChange, disabled }: { value: T; options: { value: T; label: string; hint?: string }[]; onChange: (v: T) => void; disabled?: boolean }) {
  return (
    <div className="inline-flex rounded-lg bg-neutral-200/70 p-0.5 dark:bg-neutral-800">
      {options.map((o) => (
        <button
          key={o.value}
          title={o.hint}
          disabled={disabled}
          onClick={() => o.value !== value && onChange(o.value)}
          className={cx(
            "h-7 rounded-md px-3 text-[12.5px] font-medium transition disabled:opacity-50",
            o.value === value ? "bg-white shadow-sm dark:bg-neutral-600" : "text-neutral-600 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-neutral-100",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Badge({ tone = "neutral", children }: { tone?: "neutral" | "green" | "amber" | "red" | "blue"; children: ReactNode }) {
  const t = {
    neutral: "bg-neutral-100 text-neutral-600 dark:bg-neutral-800 dark:text-neutral-300",
    green: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300",
    amber: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
    red: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-300",
    blue: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300",
  }[tone];
  return <span className={cx("inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium", t)}>{children}</span>;
}

export function Dot({ tone }: { tone: "green" | "amber" | "red" | "gray" }) {
  const c = { green: "bg-emerald-500", amber: "bg-amber-500", red: "bg-red-500", gray: "bg-neutral-400" }[tone];
  return (
    <span className="relative inline-flex h-2.5 w-2.5">
      {tone === "green" && <span className={cx("absolute inline-flex h-full w-full animate-ping rounded-full opacity-40", c)} />}
      <span className={cx("relative inline-flex h-2.5 w-2.5 rounded-full", c)} />
    </span>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="py-10 text-center text-neutral-400">{children}</div>;
}

export function delayTone(ms?: number): "green" | "amber" | "red" | "neutral" {
  if (ms === undefined || ms === 0) return "neutral";
  if (ms < 0) return "red";
  if (ms < 300) return "green";
  if (ms < 800) return "amber";
  return "red";
}

/** 延迟标签：未测速不显示；-1 为超时。 */
export function Delay({ ms }: { ms?: number }) {
  if (ms === undefined || ms === 0) return null;
  return <Badge tone={delayTone(ms)}>{ms < 0 ? "超时" : `${ms}ms`}</Badge>;
}

/**
 * macOS 上窗口使用 Overlay 标题栏（交通灯叠在内容上），需要为其留出空间并提供拖动区域。
 * 浏览器预览可用 ?titlebar=overlay 模拟。
 */
export const titlebarOverlay =
  new URLSearchParams(location.search).get("titlebar") === "overlay" ||
  ("__TAURI_INTERNALS__" in window && /Mac/.test(navigator.userAgent));

/** 开关行：标题 + 说明 + 开关；paused 表示用户已开启但当前被暂停。 */
export function Toggle({ label, hint, on, want, paused, disabled, onChange }: {
  label: string; hint: string; on: boolean; want: boolean; paused?: string; disabled?: boolean; onChange: (v: boolean) => void;
}) {
  const checked = want;
  return (
    <div className="flex items-center justify-between gap-4 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2 font-medium">
          {label}
          {want && !on && <Badge tone="amber">已暂停</Badge>}
        </div>
        <div className="text-[12px] text-neutral-500">{want && !on && paused ? paused : hint}</div>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx(
          "relative h-[22px] w-[38px] shrink-0 rounded-full transition disabled:opacity-50",
          checked ? (on ? "bg-emerald-500" : "bg-amber-400") : "bg-neutral-300 dark:bg-neutral-600",
        )}
      >
        <span className={cx("absolute top-[2px] h-[18px] w-[18px] rounded-full bg-white shadow transition-all", checked ? "left-[18px]" : "left-[2px]")} />
      </button>
    </div>
  );
}
