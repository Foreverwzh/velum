import { isWindows, staleDaemon, type Status } from "./api";

export interface Health {
  tone: "green" | "amber" | "red" | "gray";
  title: string;
  detail: string;
}

/** 把 velumd 状态归纳为一句给人看的结论（极简视图与托盘共用的口径）。 */
const STARTING: Health = { tone: "gray", title: "正在启动…", detail: "正在加载订阅与规则，通常需要几秒到十几秒" };

export function health(s: Status | null, err?: string): Health {
  // 没有错误也没有状态：App 刚打开、velumd 还在启动（App 在启动宽限期内不传错误）
  if (!s && !err) return STARTING;
  if (!s) return { tone: "red", title: "Velum 未运行", detail: err ?? "无法连接 velumd" };
  if (s.starting) return STARTING;
  if (s.last_error?.includes("协议版本不匹配"))
    return { tone: "amber", title: "TUN 组件需要更新", detail: "Velum 已更新，TUN 组件还是旧版本：在 TUN 开关或「系统组件」中更新即可" };
  if (staleDaemon(s))
    return isWindows
      ? { tone: "amber", title: "后台服务需要更新", detail: `Velum 服务还是 ${s.version}，部分功能不可用：点击「更新服务」（需要管理员授权）` }
      : { tone: "amber", title: "后台服务需要重启", detail: `仍在运行更新前的 velumd ${s.version}：退出 Velum 后重新打开` };
  if (s.last_error) return { tone: "red", title: "配置有问题", detail: s.last_error };
  if (!s.running) return { tone: "red", title: "引擎未运行", detail: "查看日志或尝试一键修复" };
  if (!s.subscription?.source) return { tone: "gray", title: "还没有订阅", detail: "填写订阅地址后即可开始使用" };
  if (s.network?.paused) return { tone: "amber", title: "已暂停接管", detail: s.network.paused };
  if (s.network?.stale_dns?.length)
    return { tone: "amber", title: "系统 DNS 有残留", detail: `系统 DNS 仍指向 ${s.network.stale_dns.join("、")}（代理工具退出时没有还原），域名解析会失败或很慢：在网络设置中把 DNS 改回自动获取` };
  if (s.network?.captive === "captive") return { tone: "amber", title: "需要网页认证", detail: "当前网络需要先在浏览器中完成登录" };
  if (s.network?.captive === "offline") return { tone: "amber", title: "网络不通", detail: "物理网络无法访问互联网" };
  if (s.sys_proxy.active && s.sys_proxy.overridden)
    return { tone: "amber", title: "系统代理未生效", detail: `实际生效的是 ${s.sys_proxy.effective ?? "其他设置"}（${s.sys_proxy.primary_interface} 上的其他 VPN / 代理工具优先）` };
  const parts = [s.system_proxy_on && "系统代理", s.tun_on && `TUN${s.tun?.system_dns?.length ? "（含系统 DNS）" : ""}`].filter(Boolean);
  if (parts.length) return { tone: "green", title: "正在保护", detail: `已接管：${parts.join(" + ")}` };
  return { tone: "gray", title: "仅本地端口", detail: `未接管系统流量；应用可手动使用代理 ${s.listen}` };
}
