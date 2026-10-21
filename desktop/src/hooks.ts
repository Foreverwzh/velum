import { useCallback, useState } from "react";
import { errText } from "./api";

/** 执行一个操作：记录进行中的操作名与错误，完成后调用 after（通常是刷新状态）。 */
export function useAction(after?: () => unknown) {
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const run = useCallback(
    async (label: string, fn: () => Promise<unknown>) => {
      setBusy(label);
      setError(undefined);
      try {
        await fn();
        return true;
      } catch (e) {
        setError(errText(e));
        return false;
      } finally {
        setBusy(undefined);
        await after?.();
      }
    },
    [after],
  );
  return { busy, error, setError, run };
}

/** 一个策略组的成员最终落到哪个节点（成员也可能是策略组）。 */
export function leafOf(groups: { tag: string; selected: string }[], tag: string): string {
  const byTag = new Map(groups.map((g) => [g.tag, g]));
  let cur = tag;
  for (let i = 0; i < 8; i++) {
    const g = byTag.get(cur);
    if (!g?.selected) break;
    cur = g.selected;
  }
  return cur;
}

/** 选择链：组 → 当前选择 → … → 节点。 */
export function pathOf(groups: { tag: string; selected: string }[], tag: string): string[] {
  const byTag = new Map(groups.map((g) => [g.tag, g]));
  const out = [tag];
  let cur = tag;
  for (let i = 0; i < 8; i++) {
    const g = byTag.get(cur);
    if (!g?.selected) break;
    cur = g.selected;
    out.push(cur);
  }
  return out;
}

/** 某个节点或组最近一次测得的延迟（任一组里有记录即可）。 */
export function delayOf(groups: { delays?: Record<string, number> }[], tag: string): number | undefined {
  for (const g of groups) if (g.delays?.[tag]) return g.delays[tag];
  return undefined;
}
