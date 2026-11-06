//! 托盘和系统提示的语言。源文字就是中文，同时作为查找英文的键（与核心、界面相同）。

use std::sync::atomic::{AtomicBool, Ordering};

static ENGLISH: AtomicBool = AtomicBool::new(false);

/// 启动时按系统语言设置；界面加载后按用户的选择再设置一次。
pub fn init() {
    set(&sys_locale::get_locale().unwrap_or_default());
}

/// 设置语言，返回是否变化。
pub fn set(lang: &str) -> bool {
    let en = !lang.to_lowercase().starts_with("zh");
    ENGLISH.swap(en, Ordering::Relaxed) != en
}

/// 翻译 src。
pub fn t(src: &'static str) -> &'static str {
    if ENGLISH.load(Ordering::Relaxed) {
        crate::i18n_en::lookup(src).unwrap_or(src)
    } else {
        src
    }
}

/// 翻译 src，并把 {0}、{1} 依次替换为 args。
pub fn tf(src: &'static str, args: &[&dyn std::fmt::Display]) -> String {
    let mut s = t(src).to_string();
    for (i, a) in args.iter().enumerate() {
        s = s.replace(&format!("{{{i}}}"), &a.to_string());
    }
    s
}
