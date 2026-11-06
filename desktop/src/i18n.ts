// 界面语言。源文字就是中文，同时作为查找英文的键（与核心的 pkg/i18n 相同）；en.ts 的完整性由 check-i18n 保证。
// 语言在页面加载时确定，切换时重新加载页面：模块顶层的常量（导航、提示等）才能都换过来。
import { en } from "./en";

export type Language = "zh" | "en";
/** 用户的选择；auto 跟随系统。 */
export type LanguagePref = Language | "auto";

const KEY = "velum.language";

export function languagePref(): LanguagePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "zh" || v === "en") return v;
  } catch {
    /* 取不到存储时跟随系统 */
  }
  return "auto";
}

function systemLanguage(): Language {
  return typeof navigator !== "undefined" && /^zh/i.test(navigator.language) ? "zh" : "en";
}

export const language: Language = (() => {
  const p = languagePref();
  return p === "auto" ? systemLanguage() : p;
})();

/** 保存选择并重新加载页面。 */
export function setLanguagePref(p: LanguagePref) {
  try {
    if (p === "auto") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, p);
  } catch {
    /* 存不下就只在这次生效 */
  }
  location.reload();
}

/** 翻译 src；{0}、{1} 依次替换为 args。 */
export function t(src: string, ...args: unknown[]): string {
  const s = language === "en" ? ((en as Record<string, string>)[src] ?? src) : src;
  return args.length ? s.replace(/\{(\d+)\}/g, (_, i) => String(args[Number(i)])) : s;
}
