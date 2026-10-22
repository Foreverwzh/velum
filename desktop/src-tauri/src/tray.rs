//! 托盘菜单（参考 Stash 的菜单栏布局）：状态、策略组（切换节点 / 延迟测试）、活跃客户端、开关与常用操作。
//!
//! 菜单随 velumd 状态刷新：策略组结构（组、类型、成员）变化时整体重建，否则只更新有变化的勾选与文字，
//! 菜单打开时也不会闪动。

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{LazyLock, Mutex};

use serde_json::Value;
use tauri::{
    image::Image,
    menu::{CheckMenuItem, IconMenuItem, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu},
    AppHandle, Emitter, Manager, Wry,
};

use crate::{system, velum};

/// 菜单项 id 中分隔组名与成员名（名称里不会出现的控制字符）。
const SEP: char = '\u{1f}';

#[derive(Default)]
struct Items {
    built: bool,
    signature: String,
    status: Option<MenuItem<Wry>>,
    speed_title: Option<CheckMenuItem<Wry>>,
    sysproxy: Option<CheckMenuItem<Wry>>,
    tun: Option<CheckMenuItem<Wry>>,
    autostart: Option<CheckMenuItem<Wry>>,
    upgrade: Option<MenuItem<Wry>>,
    groups: HashMap<String, Submenu<Wry>>,
    /// 订阅切换（有多个订阅时显示）：子菜单与各订阅（id → 菜单项）
    profiles_menu: Option<Submenu<Wry>>,
    profiles: HashMap<String, CheckMenuItem<Wry>>,
    menu: Option<Menu<Wry>>,
    /// 活跃客户端分区：标题、各行、分隔线；没有客户端时整个分区不显示
    client_head: Option<MenuItem<Wry>>,
    client_rows: Vec<IconMenuItem<Wry>>,
    client_sep: Option<PredefinedMenuItem<Wry>>,
    members: HashMap<String, CheckMenuItem<Wry>>, // id = 组 SEP 成员
    /// 上次写入的文字与勾选，只在变化时更新
    last: HashMap<String, (String, bool)>,
}

static ITEMS: LazyLock<Mutex<Items>> = LazyLock::new(|| Mutex::new(Items::default()));

fn member_id(group: &str, member: &str) -> String {
    format!("sel{SEP}{group}{SEP}{member}")
}

fn group_title(g: &Value) -> String {
    let tag = g["tag"].as_str().unwrap_or("");
    match g["selected"].as_str().filter(|s| !s.is_empty()) {
        // '\t' 之后的部分右对齐（macOS 见 menu_tabs，Windows 菜单原生支持）
        Some(sel) => format!("{tag}\t{sel}"),
        None => tag.to_string(),
    }
}

fn member_label(g: &Value, m: &str) -> String {
    match g["delays"][m].as_i64() {
        Some(d) if d < 0 => format!("{m}\t超时"),
        Some(d) => format!("{m}\t{d} ms"),
        None => m.to_string(),
    }
}

fn is_auto(g: &Value) -> bool {
    g["type"].as_str() == Some("urltest")
}

fn members(g: &Value) -> Vec<&str> {
    g["members"]
        .as_array()
        .map(|a| a.iter().filter_map(|m| m.as_str()).collect())
        .unwrap_or_default()
}

fn profile_id(id: &str) -> String {
    format!("prof{SEP}{id}")
}

/// 订阅菜单项：名称，右侧为剩余流量（没有额度时为到期日期）。
fn profile_label(p: &Value) -> String {
    let name = p["name"].as_str().unwrap_or("");
    let info = &p["info"];
    let num = |k: &str| info[k].as_f64().unwrap_or(0.0);
    if num("total") > 0.0 {
        let left = (num("total") - num("upload") - num("download")).max(0.0);
        return format!("{name}\t剩余 {}", bytes(left));
    }
    match info["expire"].as_str().and_then(|e| e.get(..10)) {
        Some(day) => format!("{name}\t{day} 到期"),
        None => name.to_string(),
    }
}

fn profiles_title(profiles: &[Value]) -> String {
    match profiles.iter().find(|p| p["active"].as_bool() == Some(true)) {
        Some(p) => format!("订阅\t{}", p["name"].as_str().unwrap_or("")),
        None => "订阅".into(),
    }
}

fn bytes(n: f64) -> String {
    let units = ["B", "KB", "MB", "GB", "TB", "PB"];
    let (mut n, mut i) = (n, 0);
    while n >= 1024.0 && i < units.len() - 1 {
        n /= 1024.0;
        i += 1;
    }
    if i == 0 {
        format!("{n:.0} B")
    } else {
        format!("{n:.1} {}", units[i])
    }
}

/// 决定是否需要重建菜单：订阅列表、组、类型、成员（选择、延迟、流量变化只需就地更新）。
fn signature(groups: &[Value], profiles: &[Value]) -> String {
    let profs = profiles
        .iter()
        .map(|p| p["id"].as_str().unwrap_or(""))
        .collect::<Vec<_>>()
        .join(",");
    profs + "\n" + &groups
        .iter()
        .map(|g| {
            format!(
                "{}|{}|{}",
                g["tag"].as_str().unwrap_or(""),
                g["type"].as_str().unwrap_or(""),
                members(g).join("\u{1e}")
            )
        })
        .collect::<Vec<_>>()
        .join("\n")
}

fn build(app: &AppHandle, groups: &[Value], profiles: &[Value]) -> tauri::Result<(Menu<Wry>, Items)> {
    let mut items = Items::default();
    let menu = Menu::new(app)?;
    let sep = || PredefinedMenuItem::separator(app);

    let status = MenuItem::with_id(app, "status", "Velum：启动中…", false, None::<&str>)?;
    menu.append(&status)?;
    menu.append(&MenuItem::with_id(
        app,
        "open",
        "打开 Velum",
        true,
        Some("CmdOrCtrl+D"),
    )?)?;
    menu.append(&sep()?)?;

    if profiles.len() > 1 {
        let sub = Submenu::with_id(app, "profiles", profiles_title(profiles), true)?;
        for p in profiles {
            let id = p["id"].as_str().unwrap_or("");
            let item = CheckMenuItem::with_id(
                app,
                profile_id(id),
                profile_label(p),
                true,
                p["active"].as_bool() == Some(true),
                None::<&str>,
            )?;
            sub.append(&item)?;
            items.profiles.insert(id.to_string(), item);
        }
        menu.append(&sub)?;
        menu.append(&sep()?)?;
        items.profiles_menu = Some(sub);
    }

    if !groups.is_empty() {
        for g in groups {
            let tag = g["tag"].as_str().unwrap_or("");
            let sub = Submenu::with_id(app, format!("group{SEP}{tag}"), group_title(g), true)?;
            sub.append(&MenuItem::with_id(
                app,
                format!("test{SEP}{tag}"),
                "延迟测试",
                true,
                None::<&str>,
            )?)?;
            if is_auto(g) {
                sub.append(&MenuItem::with_id(
                    app,
                    format!("note{SEP}{tag}"),
                    "自动选择延迟最低的节点",
                    false,
                    None::<&str>,
                )?)?;
            }
            sub.append(&sep()?)?;
            let selected = g["selected"].as_str().unwrap_or("");
            for m in members(g) {
                let id = member_id(tag, m);
                let item = CheckMenuItem::with_id(
                    app,
                    &id,
                    member_label(g, m),
                    !is_auto(g),
                    m == selected,
                    None::<&str>,
                )?;
                sub.append(&item)?;
                items.members.insert(format!("{tag}{SEP}{m}"), item);
            }
            menu.append(&sub)?;
            items.groups.insert(tag.to_string(), sub);
        }
        menu.append(&sep()?)?;
    }

    let sysproxy = CheckMenuItem::with_id(
        app,
        "sysproxy",
        "设置为系统代理",
        true,
        false,
        Some("CmdOrCtrl+S"),
    )?;
    let tun = CheckMenuItem::with_id(
        app,
        "tun",
        "TUN（接管所有应用）",
        true,
        false,
        Some("CmdOrCtrl+E"),
    )?;
    let shell = MenuItem::with_id(
        app,
        "copyshell",
        "复制终端代理命令",
        true,
        Some("CmdOrCtrl+C"),
    )?;
    let autostart = CheckMenuItem::with_id(
        app,
        "autostart",
        "登录时启动",
        true,
        system::autostart_get(),
        None::<&str>,
    )?;
    let speed_title = CheckMenuItem::with_id(
        app,
        "speedtitle",
        "在菜单栏显示网速",
        cfg!(target_os = "macos"), // 只有 macOS 的菜单栏能在图标旁显示文字
        SPEED_TITLE.load(Ordering::Relaxed),
        None::<&str>,
    )?;
    let reload = MenuItem::with_id(app, "reload", "重新加载配置", true, Some("CmdOrCtrl+R"))?;
    let update = MenuItem::with_id(app, "update", "更新订阅", true, Some("CmdOrCtrl+U"))?;
    let repair = MenuItem::with_id(app, "repair", "一键修复", true, None::<&str>)?;
    let upgrade = MenuItem::with_id(app, "upgrade", upgrade_text(None), true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出 Velum", true, Some("CmdOrCtrl+Q"))?;
    let rest: [&dyn IsMenuItem<Wry>; 13] = [
        &sysproxy,
        &tun,
        &shell,
        &sep()?,
        &autostart,
        &speed_title,
        &sep()?,
        &reload,
        &update,
        &repair,
        &sep()?,
        &upgrade,
        &quit,
    ];
    for it in rest {
        menu.append(it)?;
    }

    items.menu = Some(menu.clone());
    items.status = Some(status);
    items.speed_title = Some(speed_title);
    items.sysproxy = Some(sysproxy);
    items.tun = Some(tun);
    items.autostart = Some(autostart);
    items.upgrade = Some(upgrade);
    items.built = true;
    Ok((menu, items))
}

/// 构建初始菜单（velumd 尚未连上时）。
pub fn initial(app: &AppHandle) -> tauri::Result<Menu<Wry>> {
    let (menu, items) = build(app, &[], &[])?;
    *ITEMS.lock().unwrap() = items;
    Ok(menu)
}

/// 按 velumd 状态刷新菜单；status 为 None 表示未连上，text 是状态行文字。
pub fn refresh(app: &AppHandle, status: Option<&Value>, text: &str) {
    let groups = status
        .and_then(|s| s["groups"].as_array())
        .cloned()
        .unwrap_or_default();
    let profiles = status
        .and_then(|s| s["profiles"].as_array())
        .cloned()
        .unwrap_or_default();
    let sig = signature(&groups, &profiles);
    let mut it = ITEMS.lock().unwrap();
    if !it.built || sig != it.signature {
        let Ok((menu, items)) = build(app, &groups, &profiles) else {
            return;
        };
        if let Some(t) = app.tray_by_id("main") {
            let _ = t.set_menu(Some(menu));
        }
        *it = items;
        it.signature = sig;
    }
    let Items {
        status: status_item,
        sysproxy,
        tun,
        upgrade,
        groups: subs,
        members: member_items,
        profiles_menu,
        profiles: profile_items,
        last,
        ..
    } = &mut *it;
    let mut put = |key: String, text: String, checked: bool, apply: &dyn Fn(&str, bool)| {
        if last.get(&key) != Some(&(text.clone(), checked)) {
            apply(&text, checked);
            last.insert(key, (text, checked));
        }
    };
    if let Some(item) = status_item {
        put(
            "status".into(),
            format!("Velum：{text}"),
            false,
            &|t, _| {
                let _ = item.set_text(t);
            },
        );
    }
    let on = |k: &str| status.and_then(|s| s[k].as_bool()).unwrap_or(false);
    for (key, item, want) in [
        ("sysproxy", sysproxy, on("want_system_proxy")),
        ("tun", tun, on("want_tun")),
    ] {
        if let Some(item) = item {
            put(key.into(), String::new(), want, &|_, c| {
                let _ = item.set_checked(c);
            });
        }
    }
    if let Some(item) = upgrade {
        put("upgrade".into(), upgrade_text(status), false, &|t, _| {
            let _ = item.set_text(t);
        });
    }
    if let Some(sub) = profiles_menu {
        put("profiles".into(), profiles_title(&profiles), false, &|t, _| {
            let _ = sub.set_text(t);
        });
    }
    for p in &profiles {
        let id = p["id"].as_str().unwrap_or("");
        if let Some(item) = profile_items.get(id) {
            put(
                profile_id(id),
                profile_label(p),
                p["active"].as_bool() == Some(true),
                &|t, c| {
                    let _ = item.set_text(t);
                    let _ = item.set_checked(c);
                },
            );
        }
    }
    for g in &groups {
        let tag = g["tag"].as_str().unwrap_or("");
        if let Some(sub) = subs.get(tag) {
            put(
                format!("group{SEP}{tag}"),
                group_title(g),
                false,
                &|t, _| {
                    let _ = sub.set_text(t);
                },
            );
        }
        let selected = g["selected"].as_str().unwrap_or("");
        for m in members(g) {
            if let Some(item) = member_items.get(&format!("{tag}{SEP}{m}")) {
                put(
                    member_id(tag, m),
                    member_label(g, m),
                    m == selected,
                    &|t, c| {
                        let _ = item.set_text(t);
                        let _ = item.set_checked(c);
                    },
                );
            }
        }
    }
    // 菜单打开时改写标题会清掉右对齐排版，重新应用
    #[cfg(target_os = "macos")]
    crate::menu_tabs::reformat(app);
}

/// 检查更新菜单项：有新版本时改为更新（能自动更新时）或下载。
fn upgrade_text(status: Option<&Value>) -> String {
    let u = status.map(|s| &s["update"]).filter(|u| u["available"].as_bool() == Some(true));
    match u.and_then(|u| Some((u["latest"].as_str()?, u["manifest"].is_string()))) {
        Some((v, true)) => format!("更新到 {v}…"),
        Some((v, false)) => format!("下载新版本 {v}…"),
        None => "检查更新…".into(),
    }
}

/// 有新版本时自动更新（没有更新清单时打开安装包下载）；否则立即检查。都在设置页显示结果与进度。
fn upgrade(app: &AppHandle) {
    let available = |u: Value| Some(u).filter(|u| u["available"].as_bool() == Some(true));
    let update = velum::call("GET", "/v1/status", None)
        .ok()
        .and_then(|s| available(s["update"].clone()))
        .or_else(|| velum::call("POST", "/v1/release/check", None).ok().and_then(available));
    crate::show_main(app);
    let _ = app.emit("velum://navigate", "settings");
    let Some(u) = update else { return };
    if let Some(m) = u["manifest"].as_str() {
        let _ = tauri::async_runtime::block_on(crate::update::install(app.clone(), m.to_string()));
    } else if let Some(url) = u["download"].as_str().or(u["url"].as_str()) {
        let _ = crate::open_release(url.to_string());
    }
}

// ---------------- 实时流量 ----------------

/// 是否在菜单栏图标旁显示网速（偏好保存在 App 配置目录的 tray.json）。
static SPEED_TITLE: AtomicBool = AtomicBool::new(false);

fn prefs_path(app: &AppHandle) -> Option<std::path::PathBuf> {
    app.path().app_config_dir().ok().map(|d| d.join("tray.json"))
}

/// 读取托盘偏好；在构建菜单之前调用。
pub fn load_prefs(app: &AppHandle) {
    let on = prefs_path(app)
        .and_then(|p| std::fs::read(p).ok())
        .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
        .and_then(|v| v["menubar_speed"].as_bool())
        .unwrap_or(false);
    SPEED_TITLE.store(on, Ordering::Relaxed);
}

fn save_prefs(app: &AppHandle) {
    if let Some(p) = prefs_path(app) {
        let _ = std::fs::create_dir_all(p.parent().unwrap());
        let v = serde_json::json!({ "menubar_speed": SPEED_TITLE.load(Ordering::Relaxed) });
        let _ = std::fs::write(p, v.to_string());
    }
}

/// 速率的可读形式，数字最多三位：512 B/s、30 KB/s、1.2 MB/s、120 MB/s。
fn rate(b: i64) -> String {
    let b = b.max(0) as f64;
    let (kb, mb) = (b / 1024.0, b / 1024.0 / 1024.0);
    if kb >= 999.5 {
        if mb < 9.95 {
            format!("{mb:.1} MB/s")
        } else {
            format!("{mb:.0} MB/s")
        }
    } else if b >= 999.5 {
        format!("{kb:.0} KB/s")
    } else {
        format!("{b:.0} B/s")
    }
}

/// 菜单中最多列出的活跃客户端数。
const MAX_CLIENTS: usize = 6;

/// 进程路径 → (显示名, 图标所取的文件)。
static LABELS: LazyLock<Mutex<HashMap<String, (String, String)>>> = LazyLock::new(Default::default);

/// 客户端的显示名与图标：macOS 取所在 .app 的本地化名称（最内层，如 Chrome 的各个 Helper），
/// 图标取最外层 .app；其他情况取可执行文件名。
fn label(path: &str) -> (String, String) {
    LABELS
        .lock()
        .unwrap()
        .entry(path.to_string())
        .or_insert_with(|| {
            let bundles: Vec<&str> = path.match_indices(".app/").map(|(i, _)| &path[..i + 4]).collect();
            let file = path.rsplit(['/', '\\']).next().unwrap_or(path);
            // 以版本号命名的可执行文件（如 ~/.local/share/claude/versions/2.1.306）取上层的程序目录名
            let file = if file.starts_with(|c: char| c.is_ascii_digit()) {
                path.rsplit(['/', '\\'])
                    .skip(1)
                    .find(|d| !d.is_empty() && !d.starts_with(|c: char| c.is_ascii_digit()) && !["versions", "bin"].contains(d))
                    .unwrap_or(file)
            } else {
                file
            };
            let name = match bundles.last() {
                #[cfg(target_os = "macos")]
                Some(app) => {
                    let n = crate::menu_tabs::display_name(app);
                    n.strip_suffix(".app").map(str::to_string).unwrap_or(n)
                }
                _ => file.strip_suffix(".exe").unwrap_or(file).to_string(),
            };
            (name, bundles.first().copied().unwrap_or(path).to_string())
        })
        .clone()
}

/// 是否为 Velum 自身的进程（velumd 与 App 在同一目录；订阅更新、延迟测试等由它发起）。
fn own(path: &str) -> bool {
    static DIR: LazyLock<Option<std::path::PathBuf>> =
        LazyLock::new(|| std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.to_path_buf())));
    DIR.as_deref().is_some_and(|d| std::path::Path::new(path).starts_with(d))
}

/// 未识别出进程的流量（root 进程、局域网设备等）显示的名称与图标（通用的可执行文件图标）。
const OTHER: (&str, &str) = ("其他", "/usr/bin/true");

/// 各客户端（进程）在上一秒的速率：velumd 与总速率同一时刻按进程统计（含期间已结束的连接），各项之和等于总速率。
/// 同名的进程合并，按速率、连接数排序；不含 Velum 自身。
fn clients(t: Option<&Value>) -> Vec<(String, String, i64)> {
    let mut by: HashMap<String, (String, i64, i64)> = HashMap::new();
    for c in t.and_then(|t| t["clients"].as_array()).into_iter().flatten() {
        let path = c["process"].as_str().unwrap_or("");
        if own(path) {
            continue;
        }
        let (name, icon) = if path.is_empty() { (OTHER.0.to_string(), OTHER.1.to_string()) } else { label(path) };
        let e = by.entry(name).or_insert((icon, 0, 0));
        e.1 += c["up_rate"].as_i64().unwrap_or(0) + c["down_rate"].as_i64().unwrap_or(0);
        e.2 += c["active"].as_i64().unwrap_or(0);
    }
    let mut out: Vec<_> = by.into_iter().collect();
    out.sort_by(|a, b| (b.1 .1, b.1 .2).cmp(&(a.1 .1, a.1 .2)).then_with(|| a.0.cmp(&b.0)));
    out.into_iter()
        .take(MAX_CLIENTS)
        .map(|(name, (icon, rate, _))| (name, icon, rate))
        .collect()
}

/// 客户端名称过长（如带哈希的可执行文件名）时截去中间，避免把菜单撑宽：菜单打开期间变宽后不会再缩回。
fn short(name: &str) -> String {
    const HEAD: usize = 16;
    const TAIL: usize = 8;
    let chars: Vec<char> = name.chars().collect();
    if chars.len() <= HEAD + TAIL + 1 {
        return name.to_string();
    }
    format!("{}…{}", chars[..HEAD].iter().collect::<String>(), chars[chars.len() - TAIL..].iter().collect::<String>())
}

/// 客户端的应用图标（macOS）；其他平台不显示图标。
fn app_icon(path: &str) -> Option<Image<'static>> {
    #[cfg(target_os = "macos")]
    return crate::menu_tabs::icon_png(path).and_then(|b| Image::from_bytes(&b).ok());
    #[cfg(not(target_os = "macos"))]
    {
        let _ = path;
        None
    }
}

/// 更新活跃客户端分区（位于策略组与开关之间）；行数变化时重建分区，否则只改文字。
fn set_clients(app: &AppHandle, rows: &[(String, String, i64)]) -> bool {
    let mut it = ITEMS.lock().unwrap();
    let Some(menu) = it.menu.clone() else {
        return false;
    };
    let mut changed = false;
    if rows.len() != it.client_rows.len() {
        if let Some(h) = it.client_head.take() {
            let _ = menu.remove(&h);
        }
        for r in std::mem::take(&mut it.client_rows) {
            let _ = menu.remove(&r);
        }
        if let Some(sep) = it.client_sep.take() {
            let _ = menu.remove(&sep);
        }
        it.last.retain(|k, _| !k.starts_with("client"));
        let pos = menu
            .items()
            .ok()
            .and_then(|v| v.iter().position(|i| i.id() == "sysproxy"));
        if let (false, Some(pos)) = (rows.is_empty(), pos) {
            let (Ok(head), Ok(sep)) = (
                MenuItem::with_id(app, "clients", "活跃客户端", false, None::<&str>),
                PredefinedMenuItem::separator(app),
            ) else {
                return false;
            };
            let _ = menu.insert(&head, pos);
            for i in 0..rows.len() {
                if let Ok(row) = IconMenuItem::with_id(app, format!("client{SEP}{i}"), "", true, None, None::<&str>) {
                    let _ = menu.insert(&row, pos + 1 + it.client_rows.len());
                    it.client_rows.push(row);
                }
            }
            let _ = menu.insert(&sep, pos + 1 + it.client_rows.len());
            it.client_head = Some(head);
            it.client_sep = Some(sep);
        }
        changed = true;
    }
    let Items { client_rows, last, .. } = &mut *it;
    for (i, ((name, icon, rate_), row)) in rows.iter().zip(client_rows.iter()).enumerate() {
        let key = format!("clienticon{i}");
        if last.get(&key) != Some(&(icon.clone(), false)) {
            let _ = row.set_icon(app_icon(icon));
            last.insert(key, (icon.clone(), false));
        }
        let text = format!("{}\t{}", short(name), rate(*rate_));
        let key = format!("client{i}");
        if last.get(&key) != Some(&(text.clone(), false)) {
            let _ = row.set_text(&text);
            last.insert(key, (text, false));
            changed = true;
        }
    }
    changed
}

/// 按 /v1/traffic 与进行中的连接更新活跃客户端与菜单栏网速；traffic 为 None 表示未连上。
pub fn traffic(app: &AppHandle, t: Option<&Value>) {
    if set_clients(app, &clients(t)) {
        #[cfg(target_os = "macos")]
        crate::menu_tabs::reformat(app);
    }
    #[cfg(target_os = "macos")]
    if SPEED_TITLE.load(Ordering::Relaxed) {
        let lines = t.map(|t| {
            let r = |k: &str| rate(t[k].as_i64().unwrap_or(0));
            (r("up_rate"), r("down_rate"))
        });
        crate::menu_tabs::speed_title(app, lines);
    }
    #[cfg(not(target_os = "macos"))]
    let _ = t; // 只有 macOS 的菜单栏能显示网速
}

/// 取当前状态并刷新菜单（菜单操作之后立即反映结果，不等下一次轮询）。
pub fn refresh_now(app: &AppHandle) {
    match velum::call("GET", "/v1/status", None) {
        Ok(s) => refresh(app, Some(&s), &crate::summary(&s)),
        Err(_) => refresh(app, None, "未运行"),
    }
}

/// 处理菜单点击；在后台线程中调用。
pub fn handle(app: &AppHandle, id: &str) {
    let mut parts = id.split(SEP);
    match (parts.next(), parts.next(), parts.next()) {
        (Some("sel"), Some(group), Some(member)) => {
            // 点击勾选项时系统会先自行切换勾选：清掉记录，刷新时把整组写回实际状态
            ITEMS
                .lock()
                .unwrap()
                .last
                .retain(|k, _| !k.starts_with(&format!("sel{SEP}{group}{SEP}")));
            let body = serde_json::json!({ "group": group, "member": member }).to_string();
            let _ = velum::call("POST", "/v1/groups/select", Some(&body));
        }
        (Some("prof"), Some(id), None) => {
            // 同上：勾选由系统先行切换，刷新时写回实际使用的订阅
            ITEMS
                .lock()
                .unwrap()
                .last
                .retain(|k, _| !k.starts_with(&format!("prof{SEP}")));
            let _ = velum::call("POST", &format!("/v1/profiles/{id}/activate"), None);
        }
        (Some("test"), Some(group), None) => {
            let body = serde_json::json!({ "group": group }).to_string();
            let _ = velum::call("POST", "/v1/groups/test", Some(&body));
        }
        (Some("open" | "client"), ..) => crate::show_main(app),
        (Some("sysproxy"), ..) => toggle("system_proxy"),
        (Some("tun"), ..) => toggle("tun"),
        (Some("autostart"), ..) => {
            let _ = system::autostart_set(!system::autostart_get());
            if let Some(item) = &ITEMS.lock().unwrap().autostart {
                let _ = item.set_checked(system::autostart_get());
            }
        }
        (Some("copyshell"), ..) => copy_shell_command(),
        (Some("speedtitle"), ..) => {
            let on = !SPEED_TITLE.load(Ordering::Relaxed);
            SPEED_TITLE.store(on, Ordering::Relaxed);
            save_prefs(app);
            if let Some(item) = &ITEMS.lock().unwrap().speed_title {
                let _ = item.set_checked(on);
            }
            #[cfg(target_os = "macos")]
            if !on {
                crate::menu_tabs::speed_title(app, None);
            }
        }
        (Some("reload"), ..) => {
            let _ = velum::call("POST", "/v1/reload", None);
        }
        (Some("update"), ..) => {
            let _ = velum::call("POST", "/v1/update", None);
        }
        (Some("upgrade"), ..) => upgrade(app),
        (Some("repair"), ..) => {
            let _ = velum::call("POST", "/v1/repair", None);
        }
        (Some("quit"), ..) => {
            crate::stop_velum(); // 退出时停止 velumd（其会还原系统代理 / TUN）
            app.exit(0);
            return;
        }
        _ => return,
    }
    refresh_now(app);
}

/// 切换开关：按用户当前意图（want_*）取反；两个开关相互独立。
fn toggle(which: &str) {
    let want_key = if which == "system_proxy" {
        "want_system_proxy"
    } else {
        "want_tun"
    };
    let want = velum::call("GET", "/v1/status", None)
        .ok()
        .and_then(|s| s[want_key].as_bool())
        .unwrap_or(false);
    let body = serde_json::json!({ which: if want { "off" } else { "on" } }).to_string();
    let _ = velum::call("POST", "/v1/switches", Some(&body));
}

/// 复制让终端程序走 Velum 的命令（本地端口同时支持 HTTP 与 SOCKS5）。
fn copy_shell_command() {
    let listen = velum::call("GET", "/v1/status", None)
        .ok()
        .and_then(|s| s["listen"].as_str().map(str::to_string))
        .filter(|l| !l.is_empty())
        .unwrap_or_else(|| "127.0.0.1:7899".into());
    #[cfg(windows)]
    let cmd = format!("$env:HTTP_PROXY=\"http://{listen}\"; $env:HTTPS_PROXY=\"http://{listen}\"; $env:ALL_PROXY=\"socks5://{listen}\"");
    #[cfg(not(windows))]
    let cmd = format!(
        "export https_proxy=http://{listen} http_proxy=http://{listen} all_proxy=socks5://{listen}"
    );
    let _ = set_clipboard(&cmd);
}

fn set_clipboard(text: &str) -> std::io::Result<()> {
    use std::io::Write;
    use std::process::{Command, Stdio};
    #[cfg(target_os = "macos")]
    let mut cmd = Command::new("/usr/bin/pbcopy");
    #[cfg(windows)]
    let mut cmd = {
        use std::os::windows::process::CommandExt;
        let mut c = Command::new("clip");
        c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
        c
    };
    #[cfg(not(any(target_os = "macos", windows)))]
    let mut cmd = Command::new("xclip");
    let mut child = cmd.stdin(Stdio::piped()).spawn()?;
    child
        .stdin
        .take()
        .expect("stdin")
        .write_all(text.as_bytes())?;
    child.wait()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn short_name() {
        assert_eq!(short("豆包输入法"), "豆包输入法");
        assert_eq!(
            short("smeltd-0477e9dce437e6a5560e4a88a05eaa895204af2edc43069252e9ff141d3025df"),
            "smeltd-0477e9dce…1d3025df"
        );
    }

    #[test]
    fn rate_text() {
        let got: Vec<_> = [512, 1000, 30 * 1024, 999 * 1024, 1000 * 1024, 12 * 1024 * 1024].map(rate).into();
        assert_eq!(got, ["512 B/s", "1 KB/s", "30 KB/s", "999 KB/s", "1.0 MB/s", "12 MB/s"]);
    }

    #[test]
    fn client_label() {
        assert_eq!(label("/usr/bin/curl"), ("curl".into(), "/usr/bin/curl".into()));
        let helper = "/Applications/Velum Test.app/Contents/Frameworks/Helpers/Velum Test Helper.app/Contents/MacOS/Velum Test Helper";
        let (name, icon) = label(helper);
        assert_eq!(icon, "/Applications/Velum Test.app");
        assert_eq!(name, "Velum Test Helper");
        assert_eq!(label(r"C:\Program Files\App\app.exe").0, "app");
        assert_eq!(label("/Users/u/.local/share/claude/versions/2.1.306").0, "claude");
        assert_eq!(label("/Users/u/.local/share/claude/versions/2.1.306").0, "claude");
    }

    #[test]
    fn profile_labels() {
        let p = json!({"name": "A", "info": {"upload": 1073741824u64, "download": 1073741824u64, "total": 10737418240u64}});
        assert_eq!(profile_label(&p), "A\t剩余 8.0 GB");
        let p = json!({"name": "B", "info": {"upload": 0, "download": 0, "total": 0, "expire": "2026-12-31T00:00:00+08:00"}});
        assert_eq!(profile_label(&p), "B\t2026-12-31 到期");
        assert_eq!(profile_label(&json!({"name": "C"})), "C");
        let ps = [json!({"name": "A"}), json!({"name": "B", "active": true})];
        assert_eq!(profiles_title(&ps), "订阅\tB");
    }

    #[test]
    fn upgrade_label() {
        assert_eq!(upgrade_text(None), "检查更新…");
        let s = json!({ "update": { "available": false, "latest": "0.1.0-rc.6" } });
        assert_eq!(upgrade_text(Some(&s)), "检查更新…");
        let s = json!({ "update": { "available": true, "latest": "0.1.0-rc.7" } });
        assert_eq!(upgrade_text(Some(&s)), "下载新版本 0.1.0-rc.7…");
        let s = json!({ "update": { "available": true, "latest": "0.1.0-rc.7", "manifest": "https://x/latest.json" } });
        assert_eq!(upgrade_text(Some(&s)), "更新到 0.1.0-rc.7…");
    }

    #[test]
    fn client_rates() {
        let c = |p: &str, up: i64, down: i64, active: i64| json!({"process": p, "up_rate": up, "down_rate": down, "active": active});
        // 同名进程合并；未识别出进程的流量显示为「其他」；按速率、连接数排序
        let t = json!({"clients": [
            c("/usr/bin/a", 10, 300, 1),
            c("/opt/x/a", 0, 100, 2),
            c("/usr/bin/b", 0, 1000, 1),
            c("", 0, 50, 3),
            c("/usr/bin/idle", 0, 0, 4),
        ]});
        let got: Vec<_> = clients(Some(&t)).iter().map(|r| (r.0.clone(), r.2)).collect();
        assert_eq!(got, [("b".to_string(), 1000), ("a".into(), 410), ("其他".into(), 50), ("idle".into(), 0)]);
    }
}
