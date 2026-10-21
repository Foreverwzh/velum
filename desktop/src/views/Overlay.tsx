import { useEffect, useState } from "react";
import { api, errText } from "../api";
import type { Refresh } from "../App";
import { Button, PageHeader, Tag } from "../components/ui";

/** 本地覆盖：开发直连、hosts 覆盖、系统代理豁免；保存时先校验，生效失败自动改回。 */
export default function Overlay({ refresh }: { refresh: Refresh }) {
  const [doc, setDoc] = useState<{ path: string; content: string }>();
  const [text, setText] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api.overlay().then((d) => {
      setDoc(d);
      setText(d.content);
    }).catch((e) => setMsg({ ok: false, text: errText(e) }));
  }, []);
  const dirty = !!doc && text !== doc.content;
  const save = async () => {
    setBusy(true);
    try {
      await api.saveOverlay(text);
      setMsg({ ok: true, text: "已保存并生效" });
      setDoc((d) => d && { ...d, content: text });
      await refresh();
    } catch (e) {
      setMsg({ ok: false, text: errText(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="本地覆盖"
        description={<>优先于订阅，更新订阅不会覆盖。<span className="selectable ml-1 font-mono">{doc?.path}</span></>}
        actions={
          <>
            {dirty && <Tag tone="warn">未保存</Tag>}
            <Button variant="ghost" disabled={!dirty || busy} onClick={() => doc && setText(doc.content)}>撤销修改</Button>
            <Button variant="primary" loading={busy} disabled={!dirty} onClick={save}>保存并应用</Button>
          </>
        }
      />
      {msg && <div className={`selectable border-b border-line px-6 py-2 text-[12px] ${msg.ok ? "text-accent" : "text-err"}`}>{msg.text}</div>}
      <textarea
        value={text}
        onChange={(e) => { setText(e.target.value); setMsg(undefined); }}
        onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); if (dirty) void save(); } }}
        spellCheck={false}
        className="min-h-0 flex-1 resize-none bg-panel px-6 py-4 font-mono text-[12.5px] leading-[1.7] outline-none"
      />
    </div>
  );
}
