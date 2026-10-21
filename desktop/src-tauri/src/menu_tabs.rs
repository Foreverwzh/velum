//! macOS 菜单的左右对齐：标题中 '\t' 之后的部分右对齐（与 Stash 相同的排版）。
//!
//! Tauri 只能设置纯文本标题，NSMenu 也不会把 '\t' 排成右对齐。这里在菜单开始显示时
//! （NSMenuDidBeginTrackingNotification）把含 '\t' 的标题改为带右对齐制表位的富文本；
//! Windows 的菜单原生把 '\t' 之后的文字右对齐，无需处理。

use std::cell::RefCell;
use std::ptr::NonNull;

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2::AllocAnyThread;
use objc2_app_kit::{
    NSAttributedStringNSStringDrawing, NSFont, NSFontAttributeName, NSMenu,
    NSMenuDidBeginTrackingNotification, NSMenuDidEndTrackingNotification, NSMutableParagraphStyle,
    NSParagraphStyleAttributeName, NSTextAlignment, NSTextTab,
};
use objc2_foundation::{
    NSArray, NSAttributedString, NSAttributedStringKey, NSDictionary, NSNotification,
    NSNotificationCenter, NSString,
};
use tauri::AppHandle;

/// 左右两部分之间的最小间距（点）。
const GAP: f64 = 28.0;

thread_local! {
    /// 正在显示的菜单（只在主线程访问）。
    static OPEN: RefCell<Option<Retained<NSMenu>>> = const { RefCell::new(None) };
}

/// 注册菜单显示通知；在 setup 中调用一次。
pub fn install(app: &AppHandle) {
    let _ = app.run_on_main_thread(|| {
        let center = NSNotificationCenter::defaultCenter();
        let begin = RcBlock::new(|n: NonNull<NSNotification>| {
            if let Some(menu) = menu_of(unsafe { n.as_ref() }) {
                format(&menu);
                // 只记录最外层菜单（子菜单打开时也会发送此通知）
                OPEN.with(|o| {
                    o.borrow_mut().get_or_insert(menu);
                });
            }
        });
        let end = RcBlock::new(|n: NonNull<NSNotification>| {
            if let Some(menu) = menu_of(unsafe { n.as_ref() }) {
                OPEN.with(|o| {
                    let mut o = o.borrow_mut();
                    if o.as_deref().is_some_and(|m| std::ptr::eq(m, &*menu)) {
                        *o = None;
                    }
                });
            }
        });
        unsafe {
            // 观察者在进程生命周期内一直有效
            std::mem::forget(center.addObserverForName_object_queue_usingBlock(
                Some(NSMenuDidBeginTrackingNotification),
                None,
                None,
                &begin,
            ));
            std::mem::forget(center.addObserverForName_object_queue_usingBlock(
                Some(NSMenuDidEndTrackingNotification),
                None,
                None,
                &end,
            ));
        }
    });
}

/// 菜单显示期间标题被刷新（会清掉富文本）后重新排版。
pub fn reformat(app: &AppHandle) {
    let _ = app.run_on_main_thread(|| {
        OPEN.with(|o| {
            if let Some(menu) = o.borrow().as_ref() {
                format(menu);
            }
        })
    });
}

fn menu_of(n: &NSNotification) -> Option<Retained<NSMenu>> {
    n.object()?.downcast::<NSMenu>().ok()
}

/// 为一个菜单（及其子菜单）中含 '\t' 的标题设置右对齐制表位。
fn format(menu: &NSMenu) {
    let font = NSFont::menuFontOfSize(0.0);
    let plain = attrs(&font, None);
    let width = |s: &str| unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(s), &plain) }.size().width;

    let items = menu.itemArray();
    let mut rows = Vec::new();
    let (mut left, mut right) = (0.0f64, 0.0f64);
    for item in items.iter() {
        if let Some(sub) = item.submenu() {
            format(&sub);
        }
        let title = item.title().to_string();
        if let Some((l, r)) = title.split_once('\t') {
            left = left.max(width(l));
            right = right.max(width(r));
            rows.push((item, title));
        }
    }
    if rows.is_empty() {
        return;
    }
    let tab = unsafe {
        NSTextTab::initWithTextAlignment_location_options(
            NSTextTab::alloc(),
            NSTextAlignment::Right,
            left + GAP + right,
            &NSDictionary::new(),
        )
    };
    let style = NSMutableParagraphStyle::new();
    style.setTabStops(Some(&NSArray::from_retained_slice(&[tab])));
    let styled = attrs(&font, Some(&style));
    for (item, title) in rows {
        let s = unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(&title), &styled) };
        item.setAttributedTitle(Some(&s));
    }
}

fn attrs(
    font: &NSFont,
    style: Option<&NSMutableParagraphStyle>,
) -> Retained<NSDictionary<NSAttributedStringKey, AnyObject>> {
    let (font_key, style_key) = unsafe { (NSFontAttributeName, NSParagraphStyleAttributeName) };
    let font_obj: &AnyObject = font.as_ref();
    match style {
        Some(st) => NSDictionary::from_slices(&[font_key, style_key], &[font_obj, st.as_ref()]),
        None => NSDictionary::from_slices(&[font_key], &[font_obj]),
    }
}
