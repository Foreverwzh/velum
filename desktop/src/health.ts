import { isWindows, staleDaemon, type Status } from "./api";
import { t } from "./i18n";

export interface Health {
  tone: "green" | "amber" | "red" | "gray";
  title: string;
  detail: string;
}

/** 把 velumd 状态归纳为一句给人看的结论（极简视图与托盘共用的口径）。 */
const STARTING: Health = { tone: "gray", title: t("正在启动…"), detail: t("正在加载订阅与规则，通常需要几秒到十几秒") };

export function health(s: Status | null, err?: string): Health {
  // 没有错误也没有状态：App 刚打开、velumd 还在启动（App 在启动宽限期内不传错误）
  if (!s && !err) return STARTING;
  if (!s) return { tone: "red", title: t("Velum 未运行"), detail: err ?? t("无法连接 velumd") };
  if (s.starting) return STARTING;
  if (s.last_error?.includes(t("协议版本不匹配")))
    return { tone: "amber", title: t("TUN 组件需要更新"), detail: t("Velum 已更新，TUN 组件还是旧版本：在 TUN 开关或「系统组件」中更新即可") };
  if (staleDaemon(s))
    return isWindows
      ? { tone: "amber", title: t("后台服务需要更新"), detail: t("Velum 服务还是 {0}，部分功能不可用：点击「更新服务」（需要管理员授权）", s.version) }
      : { tone: "amber", title: t("后台服务需要重启"), detail: t("仍在运行更新前的 velumd {0}：退出 Velum 后重新打开", s.version) };
  if (s.last_error) return { tone: "red", title: t("配置有问题"), detail: s.last_error };
  if (!s.running) return { tone: "red", title: t("引擎未运行"), detail: t("查看日志或尝试一键修复") };
  if (!s.subscription?.source) return { tone: "gray", title: t("还没有订阅"), detail: t("填写订阅地址后即可开始使用") };
  if (s.network?.paused) return { tone: "amber", title: t("已暂停接管"), detail: s.network.paused };
  if (s.network?.stale_dns?.length)
    return { tone: "amber", title: t("系统 DNS 有残留"), detail: t("系统 DNS 仍指向 {0}（代理工具退出时没有还原），域名解析会失败或很慢：在网络设置中把 DNS 改回自动获取", s.network.stale_dns.join("、")) };
  if (s.network?.captive === "captive") return { tone: "amber", title: t("需要网页认证"), detail: t("当前网络需要先在浏览器中完成登录") };
  if (s.network?.captive === "offline") return { tone: "amber", title: t("网络不通"), detail: t("物理网络无法访问互联网") };
  if (s.sys_proxy.active && s.sys_proxy.overridden)
    return { tone: "amber", title: t("系统代理未生效"), detail: t("实际生效的是 {0}（{1} 上的其他 VPN / 代理工具优先）", s.sys_proxy.effective ?? t("其他设置"), s.sys_proxy.primary_interface) };
  const parts = [s.system_proxy_on && t("系统代理"), s.tun_on && `TUN${s.tun?.system_dns?.length ? t("（含系统 DNS）") : ""}`].filter(Boolean);
  if (parts.length) return { tone: "green", title: t("正在保护"), detail: t("已接管：{0}", parts.join(" + ")) };
  return { tone: "gray", title: t("仅本地端口"), detail: t("未接管系统流量；应用可手动使用代理 {0}", s.listen) };
}
