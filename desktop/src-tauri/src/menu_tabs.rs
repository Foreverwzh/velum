//! macOS 菜单的左右对齐：标题中 '\t' 之后的部分右对齐（与 Stash 相同的排版）。
//!
//! Tauri 只能设置纯文本标题，NSMenu 也不会把 '\t' 排成右对齐。这里在菜单开始显示时
//! （NSMenuDidBeginTrackingNotification）把含 '\t' 的标题改为带右对齐制表位的富文本；
//! Windows 的菜单原生把 '\t' 之后的文字右对齐，无需处理。
//!
//! 同时负责 Tauri 做不到的其他 macOS 菜单栏排版：活跃客户端的应用图标、菜单栏图标旁的两行网速。

use std::cell::RefCell;
use std::collections::HashMap;
use std::ptr::NonNull;
use std::sync::{LazyLock, Mutex};

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2::{AllocAnyThread, MainThreadMarker};
use objc2_app_kit::{
    NSAttributedStringNSStringDrawing, NSColor, NSFont, NSFontAttributeName, NSFontWeightMedium,
    NSForegroundColorAttributeName, NSImage, NSMenu, NSMenuDidBeginTrackingNotification,
    NSMenuDidEndTrackingNotification, NSMutableParagraphStyle, NSParagraphStyleAttributeName,
    NSTextAlignment, NSTextTab, NSWorkspace,
};
use objc2_foundation::{
    NSArray, NSAttributedString, NSAttributedStringKey, NSDictionary, NSFileManager,
    NSNotification, NSNotificationCenter, NSSize, NSString,
};
use tauri::AppHandle;

/// 左右两部分之间的最小间距（点）。
const GAP: f64 = 28.0;

thread_local! {
    /// 正在显示的菜单（只在主线程访问）。
    static OPEN: RefCell<Option<Retained<NSMenu>>> = const { RefCell::new(None) };
    /// 已加载的应用图标，按文件路径缓存（只在主线程访问）。
    static IMAGES: RefCell<HashMap<String, Retained<NSImage>>> = RefCell::new(HashMap::new());
}

/// 活跃客户端行的名称 → 图标所取的文件（.app 或可执行文件）。
static ICONS: LazyLock<Mutex<HashMap<String, String>>> = LazyLock::new(Default::default);

/// 登记菜单项（按 '\t' 之前的名称）要显示的应用图标；下次排版时生效。
pub fn set_icon(name: &str, path: &str) {
    ICONS.lock().unwrap().insert(name.to_string(), path.to_string());
}

/// 应用在 Finder 中的显示名（本地化，不带 .app），如「豆包输入法」。
pub fn display_name(path: &str) -> String {
    NSFileManager::defaultManager().displayNameAtPath(&NSString::from_str(path)).to_string()
}

fn icon(path: &str) -> Retained<NSImage> {
    IMAGES.with(|m| {
        m.borrow_mut()
            .entry(path.to_string())
            .or_insert_with(|| {
                let img = NSWorkspace::sharedWorkspace().iconForFile(&NSString::from_str(path));
                img.setSize(NSSize::new(16.0, 16.0));
                img
            })
            .clone()
    })
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
    let plain = attrs(&font, None, None);
    let width = |s: &str| unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(s), &plain) }.size().width;

    let icons = ICONS.lock().unwrap().clone();
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
            if item.submenu().is_none() {
                if let Some(path) = icons.get(l) {
                    item.setImage(Some(&icon(path)));
                }
            }
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
    let styled = attrs(&font, Some(&style), None);
    for (item, title) in rows {
        let s = unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(&title), &styled) };
        item.setAttributedTitle(Some(&s));
    }
}

/// 菜单栏图标旁的两行网速（上行在上、下行在下，右对齐）；None 时清除。
pub fn speed_title(app: &AppHandle, lines: Option<(String, String)>) {
    let Some(tray) = app.tray_by_id("main") else {
        return;
    };
    let _ = tray.with_inner_tray_icon(move |t| {
        let (Some(item), Some(mtm)) = (t.ns_status_item(), MainThreadMarker::new()) else {
            return;
        };
        let Some(button) = item.button(mtm) else {
            return;
        };
        match lines {
            Some((up, down)) => {
                let font = unsafe { NSFont::monospacedDigitSystemFontOfSize_weight(9.0, NSFontWeightMedium) };
                // 宽度按最宽的取值固定，数字变化时图标不会左右跳动
                let widest = unsafe {
                    NSAttributedString::new_with_attributes(&NSString::from_str("000.0 MB/s"), &attrs(&font, None, None))
                }
                .size()
                .width
                .ceil();
                let tab = unsafe {
                    NSTextTab::initWithTextAlignment_location_options(
                        NSTextTab::alloc(),
                        NSTextAlignment::Right,
                        widest,
                        &NSDictionary::new(),
                    )
                };
                let style = NSMutableParagraphStyle::new();
                style.setTabStops(Some(&NSArray::from_retained_slice(&[tab])));
                style.setMinimumLineHeight(10.0);
                style.setMaximumLineHeight(10.0);
                let color = NSColor::labelColor();
                let text = NSString::from_str(&format!("\t{up}\n\t{down}"));
                let s = unsafe { NSAttributedString::new_with_attributes(&text, &attrs(&font, Some(&style), Some(&color))) };
                button.setAttributedTitle(&s);
            }
            None => button.setTitle(&NSString::new()),
        }
        // tray-icon 在按钮上覆盖了一层接收点击的视图，按钮尺寸变化后跟着调整
        for v in button.subviews().iter() {
            v.setFrame(button.bounds());
        }
    });
}

fn attrs(
    font: &NSFont,
    style: Option<&NSMutableParagraphStyle>,
    color: Option<&NSColor>,
) -> Retained<NSDictionary<NSAttributedStringKey, AnyObject>> {
    let mut keys: Vec<&NSAttributedStringKey> = vec![unsafe { NSFontAttributeName }];
    let mut values: Vec<&AnyObject> = vec![font.as_ref()];
    if let Some(st) = style {
        keys.push(unsafe { NSParagraphStyleAttributeName });
        values.push(st.as_ref());
    }
    if let Some(c) = color {
        keys.push(unsafe { NSForegroundColorAttributeName });
        values.push(c.as_ref());
    }
    NSDictionary::from_slices(&keys, &values)
}
