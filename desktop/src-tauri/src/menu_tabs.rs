//! macOS 菜单的左右对齐：标题中 '\t' 之后的部分右对齐（与 Stash 相同的排版）。
//!
//! Tauri 只能设置纯文本标题，NSMenu 也不会把 '\t' 排成右对齐。这里在菜单开始显示时
//! （NSMenuDidBeginTrackingNotification）把含 '\t' 的标题改为带右对齐制表位的富文本；
//! Windows 的菜单原生把 '\t' 之后的文字右对齐，无需处理。
//!
//! 同时提供 Tauri 做不到的其他 macOS 菜单栏能力：活跃客户端的应用图标与名称、菜单栏图标旁的两行网速。

use std::cell::RefCell;
use std::collections::HashMap;
use std::ptr::NonNull;
use std::sync::{LazyLock, Mutex};

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2::{AllocAnyThread, MainThreadMarker};
use objc2_app_kit::{
    NSAttributedStringNSStringDrawing, NSBaselineOffsetAttributeName, NSBitmapImageFileType,
    NSBitmapImageRep, NSColor, NSDeviceRGBColorSpace, NSGraphicsContext, NSFont, NSFontAttributeName, NSFontWeightMedium,
    NSForegroundColorAttributeName, NSMenu, NSMenuItem, NSMenuDidBeginTrackingNotification,
    NSMenuDidEndTrackingNotification, NSMutableParagraphStyle, NSParagraphStyleAttributeName,
    NSTextAlignment, NSTextTab, NSWorkspace,
};
use objc2_foundation::{
    NSArray, NSAttributedString, NSAttributedStringKey, NSDictionary, NSFileManager,
    NSNotification, NSNotificationCenter, NSNumber, NSPoint, NSRect, NSSize, NSString,
};
use tauri::AppHandle;

/// 左右两部分之间的最小间距（点）。
const GAP: f64 = 28.0;
/// 顶层菜单两侧的最小宽度所取的文字：活跃客户端的名称（截断后最多 25 个字符）与速率随时变化，
/// 按最宽的情形预留，菜单宽度与右侧一列的位置就不会随之抖动。
const MIN_LEFT: [&str; 2] = ["Google Chrome Helper (Ren", "smeltd-0477e9dce…1d3025df"];
const MIN_RIGHT: &str = "000 MB/s";
/// 菜单项图标与标题之间的间距（点）。
const IMAGE_GAP: f64 = 6.0;
/// 客户端行的图标宽度（点，与 muda 设置的图标大小一致）。
const CLIENT_ICON: f64 = 18.0;
/// 菜单栏两行网速的基线下移量（点）。
const BASELINE: f64 = -4.5;

thread_local! {
    /// 正在显示的菜单（只在主线程访问）。
    static OPEN: RefCell<Option<Retained<NSMenu>>> = const { RefCell::new(None) };
}

/// 应用图标（PNG），按文件路径缓存。
static ICONS: LazyLock<Mutex<HashMap<String, Option<Vec<u8>>>>> = LazyLock::new(Default::default);

/// 应用在 Finder 中的显示名（本地化，不带 .app），如「豆包输入法」。
pub fn display_name(path: &str) -> String {
    NSFileManager::defaultManager().displayNameAtPath(&NSString::from_str(path)).to_string()
}

/// 文件（.app 或可执行文件）在 Finder 中的图标，绘制为 32×32 的 PNG（菜单中显示为 16pt）。
///
/// macOS 27 起菜单项图标默认隐藏，显示时还需设置 preferredImageVisibility（见 show_image）。
pub fn icon_png(path: &str) -> Option<Vec<u8>> {
    ICONS
        .lock()
        .unwrap()
        .entry(path.to_string())
        .or_insert_with(|| render_icon(path, 32))
        .clone()
}

fn render_icon(path: &str, px: isize) -> Option<Vec<u8>> {
    let img = NSWorkspace::sharedWorkspace().iconForFile(&NSString::from_str(path));
    let rep = unsafe {
        NSBitmapImageRep::initWithBitmapDataPlanes_pixelsWide_pixelsHigh_bitsPerSample_samplesPerPixel_hasAlpha_isPlanar_colorSpaceName_bytesPerRow_bitsPerPixel(
            NSBitmapImageRep::alloc(),
            std::ptr::null_mut(),
            px,
            px,
            8,
            4,
            true,
            false,
            NSDeviceRGBColorSpace,
            0,
            0,
        )
    }?;
    let ctx = NSGraphicsContext::graphicsContextWithBitmapImageRep(&rep)?;
    NSGraphicsContext::saveGraphicsState_class();
    NSGraphicsContext::setCurrentContext(Some(&ctx));
    img.drawInRect(NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(px as f64, px as f64)));
    ctx.flushGraphics();
    NSGraphicsContext::restoreGraphicsState_class();
    let data = unsafe { rep.representationUsingType_properties(NSBitmapImageFileType::PNG, &NSDictionary::new()) }?;
    Some(data.to_vec())
}

/// 注册菜单显示通知；在 setup 中调用一次。
pub fn install(app: &AppHandle) {
    let _ = app.run_on_main_thread(|| {
        let center = NSNotificationCenter::defaultCenter();
        let begin = RcBlock::new(|n: NonNull<NSNotification>| {
            if let Some(menu) = menu_of(unsafe { n.as_ref() }) {
                format(&menu, true);
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
                format(menu, true);
            }
        })
    });
}

fn menu_of(n: &NSNotification) -> Option<Retained<NSMenu>> {
    n.object()?.downcast::<NSMenu>().ok()
}

/// 为一个菜单（及其子菜单）中含 '\t' 的标题设置右对齐制表位，并让带图标的菜单项显示图标；top 为顶层菜单。
fn format(menu: &NSMenu, top: bool) {
    let font = NSFont::menuFontOfSize(0.0);
    let plain = attrs(&font, None, None, None);
    let width = |s: &str| unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(s), &plain) }.size().width;

    let mut rows = Vec::new();
    let (mut left, mut right) = (0.0f64, 0.0f64);
    for item in menu.itemArray().iter() {
        if let Some(sub) = item.submenu() {
            format(&sub, false);
        }
        // 图标把标题往右推：制表位从标题起点算，带图标的行要相应前移
        let indent = match item.image() {
            Some(img) => {
                show_image(&item);
                img.size().width + IMAGE_GAP
            }
            None => 0.0,
        };
        let title = item.title().to_string();
        if let Some((l, r)) = title.split_once('\t') {
            left = left.max(indent + width(l));
            right = right.max(width(r));
            rows.push((item, title, indent));
        }
    }
    if rows.is_empty() {
        return;
    }
    if top {
        for l in MIN_LEFT {
            left = left.max(CLIENT_ICON + IMAGE_GAP + width(l));
        }
        right = right.max(width(MIN_RIGHT));
    }
    for (item, title, indent) in rows {
        let tab = unsafe {
            NSTextTab::initWithTextAlignment_location_options(
                NSTextTab::alloc(),
                NSTextAlignment::Right,
                left + GAP + right - indent,
                &NSDictionary::new(),
            )
        };
        let style = NSMutableParagraphStyle::new();
        style.setTabStops(Some(&NSArray::from_retained_slice(&[tab])));
        let s = unsafe { NSAttributedString::new_with_attributes(&NSString::from_str(&title), &attrs(&font, Some(&style), None, None)) };
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
                    NSAttributedString::new_with_attributes(&NSString::from_str("999 MB/s"), &attrs(&font, None, None, None))
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
                // 两行文字整体偏上，下移到与图标垂直居中
                let offset = NSNumber::new_f64(BASELINE);
                let s = unsafe {
                    NSAttributedString::new_with_attributes(&text, &attrs(&font, Some(&style), Some(&color), Some(&offset)))
                };
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

/// macOS 27 起 AppKit 默认隐藏菜单项图标，需将 preferredImageVisibility 设为 Visible（1）才会显示。
fn show_image(item: &NSMenuItem) {
    let sel = objc2::sel!(setPreferredImageVisibility:);
    if objc2_foundation::NSObjectProtocol::respondsToSelector(item, sel) {
        unsafe {
            let _: () = objc2::msg_send![item, setPreferredImageVisibility: 1isize];
        }
    }
}

fn attrs(
    font: &NSFont,
    style: Option<&NSMutableParagraphStyle>,
    color: Option<&NSColor>,
    baseline: Option<&NSNumber>,
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
    if let Some(b) = baseline {
        keys.push(unsafe { NSBaselineOffsetAttributeName });
        values.push(b.as_ref());
    }
    NSDictionary::from_slices(&keys, &values)
}
