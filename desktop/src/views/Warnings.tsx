import { useEffect, useState } from "react";
import { CircleCheck } from "lucide-react";
import { api, errText, srcText, type Warning } from "../api";
import { Empty, Notice, PageHeader, Panel, Section, Tag } from "../components/ui";

const names: Record<string, string> = { node: "节点", provider: "节点源", group: "策略组", rule: "规则", ruleset: "规则集", dns: "DNS", global: "全局", overlay: "本地覆盖", render: "渲染" };

/** 告警：订阅中无法完全等价转换的配置，按类别分组。 */
export default function Warnings() {
  const [ws, setWs] = useState<Warning[]>();
  const [err, setErr] = useState<string>();
  useEffect(() => {
    api.warnings().then((r) => setWs(r.warnings ?? [])).catch((e) => setErr(errText(e)));
  }, []);
  const groups = (ws ?? []).reduce<Record<string, Warning[]>>((m, w) => ((m[w.category] ??= []).push(w), m), {});
  const unsupported = (ws ?? []).filter((w) => w.level === "unsupported").length;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader title="告警" description={ws && ws.length ? `${ws.length} 项无法完全等价转换${unsupported ? `，其中 ${unsupported} 项不支持` : ""}；降级项通常不影响使用` : "订阅中无法完全等价转换的配置会列在这里"} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[780px] flex-col gap-6 px-6 py-5">
          {err && <Notice tone="err">{err}</Notice>}
          {ws && !ws.length && <Empty icon={CircleCheck} title="没有告警" hint="订阅中的配置都已完整转换" />}
          {Object.entries(groups).map(([cat, list]) => (
            <Section key={cat} title={<>{names[cat] ?? cat}<span className="ml-1.5 font-normal text-fg-3 tnum">{list.length}</span></>}>
              <Panel>
                {list.map((w, i) => (
                  <div key={i} className="grid grid-cols-[52px_1fr] items-start gap-3 border-b border-line px-3.5 py-2.5 last:border-0">
                    <Tag tone={w.level === "unsupported" ? "err" : "warn"} className="mt-px justify-self-start">{w.level === "unsupported" ? "不支持" : "降级"}</Tag>
                    <div className="min-w-0">
                      <div className="text-[12.5px]">{w.subject && <span className="font-medium">{w.subject}：</span>}{w.reason}</div>
                      <div className="selectable mt-0.5 truncate font-mono text-[11.5px] text-fg-3">{srcText(w.source)}</div>
                    </div>
                  </div>
                ))}
              </Panel>
            </Section>
          ))}
        </div>
      </div>
    </div>
  );
}
