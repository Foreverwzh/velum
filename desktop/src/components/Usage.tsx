import type { SubInfo } from "../api";
import { cx } from "./ui";

export function fmtBytes(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(n >= 100 ? 0 : n >= 10 ? 1 : 2)} ${units[i]}`;
}

/** 订阅的流量与到期：已用比例（没有额度时为 undefined）、一行文字、剩余天数。 */
export function usageOf(info?: SubInfo) {
  if (!info) return undefined;
  const used = info.upload + info.download;
  const pct = info.total > 0 ? Math.min(100, (used / info.total) * 100) : undefined;
  const expire = info.expire ? new Date(info.expire) : undefined;
  const days = expire ? Math.floor((expire.getTime() - Date.now()) / 86400000) : undefined;
  const parts: string[] = [];
  if (pct !== undefined) parts.push(`已用 ${fmtBytes(used)} / ${fmtBytes(info.total)} · 剩余 ${fmtBytes(Math.max(0, info.total - used))}`);
  if (expire && days !== undefined)
    parts.push(days < 0 ? `已于 ${expire.toLocaleDateString()} 到期` : `${expire.toLocaleDateString()} 到期（${days} 天后）`);
  if (!parts.length) return undefined;
  const tone = (pct ?? 0) >= 95 || (days !== undefined && days < 3) ? "err" : (pct ?? 0) >= 80 || (days !== undefined && days < 7) ? "warn" : "ok";
  return { pct, text: parts.join(" · "), tone } as const;
}

/** 流量额度进度条。 */
export function UsageBar({ pct, tone, className }: { pct: number; tone: "ok" | "warn" | "err"; className?: string }) {
  return (
    <div className={cx("h-1 overflow-hidden rounded-full bg-hover", className)} role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
      <div className={cx("h-full rounded-full", tone === "err" ? "bg-err" : tone === "warn" ? "bg-warn" : "bg-accent")} style={{ width: `${Math.max(pct, 1)}%` }} />
    </div>
  );
}
