//! 托盘菜单（参考 Stash 的菜单栏布局）：状态、策略组（切换节点 / 延迟测试）、开关与常用操作。
//!
//! 菜单随 velumd 状态刷新：策略组结构（组、类型、成员）变化时整体重建，否则只更新有变化的勾选与文字，
//! 菜单打开时也不会闪动。

use std::collections::HashMap;
use std::sync::{LazyLock, Mutex};

use serde_json::Value;
use tauri::{
    menu::{CheckMenuItem, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu},
    AppHandle, Wry,
};

use crate::{system, velum};

/// 菜单项 id 中分隔组名与成员名（名称里不会出现的控制字符）。
const SEP: char = '\u{1f}';

#[derive(Default)]
struct Items {
    built: bool,
    signature: String,
    status: Option<MenuItem<Wry>>,
    sysproxy: Option<CheckMenuItem<Wry>>,
    tun: Option<CheckMenuItem<Wry>>,
    autostart: Option<CheckMenuItem<Wry>>,
    groups: HashMap<String, Submenu<Wry>>,
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
        Some(sel) => format!("{tag}：{sel}"),
        None => tag.to_string(),
    }
}

fn member_label(g: &Value, m: &str) -> String {
    match g["delays"][m].as_i64() {
        Some(d) if d < 0 => format!("{m}  ·  超时"),
        Some(d) => format!("{m}  ·  {d} ms"),
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

/// 决定是否需要重建菜单：组、类型、成员（选择与延迟变化只需就地更新）。
fn signature(groups: &[Value]) -> String {
    groups
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

fn build(app: &AppHandle, groups: &[Value]) -> tauri::Result<(Menu<Wry>, Items)> {
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
    let reload = MenuItem::with_id(app, "reload", "重新加载配置", true, Some("CmdOrCtrl+R"))?;
    let update = MenuItem::with_id(app, "update", "更新订阅", true, Some("CmdOrCtrl+U"))?;
    let repair = MenuItem::with_id(app, "repair", "一键修复", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出 Velum", true, Some("CmdOrCtrl+Q"))?;
    let rest: [&dyn IsMenuItem<Wry>; 11] = [
        &sysproxy,
        &tun,
        &shell,
        &sep()?,
        &autostart,
        &sep()?,
        &reload,
        &update,
        &repair,
        &sep()?,
        &quit,
    ];
    for it in rest {
        menu.append(it)?;
    }

    items.status = Some(status);
    items.sysproxy = Some(sysproxy);
    items.tun = Some(tun);
    items.autostart = Some(autostart);
    items.built = true;
    Ok((menu, items))
}

/// 构建初始菜单（velumd 尚未连上时）。
pub fn initial(app: &AppHandle) -> tauri::Result<Menu<Wry>> {
    let (menu, items) = build(app, &[])?;
    *ITEMS.lock().unwrap() = items;
    Ok(menu)
}

/// 按 velumd 状态刷新菜单；status 为 None 表示未连上，text 是状态行文字。
pub fn refresh(app: &AppHandle, status: Option<&Value>, text: &str) {
    let groups = status
        .and_then(|s| s["groups"].as_array())
        .cloned()
        .unwrap_or_default();
    let sig = signature(&groups);
    let mut it = ITEMS.lock().unwrap();
    if !it.built || sig != it.signature {
        let Ok((menu, items)) = build(app, &groups) else {
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
        groups: subs,
        members: member_items,
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
        (Some("test"), Some(group), None) => {
            let body = serde_json::json!({ "group": group }).to_string();
            let _ = velum::call("POST", "/v1/groups/test", Some(&body));
        }
        (Some("open"), ..) => crate::show_main(app),
        (Some("sysproxy"), ..) => toggle("system_proxy"),
        (Some("tun"), ..) => toggle("tun"),
        (Some("autostart"), ..) => {
            let _ = system::autostart_set(!system::autostart_get());
            if let Some(item) = &ITEMS.lock().unwrap().autostart {
                let _ = item.set_checked(system::autostart_get());
            }
        }
        (Some("copyshell"), ..) => copy_shell_command(),
        (Some("reload"), ..) => {
            let _ = velum::call("POST", "/v1/reload", None);
        }
        (Some("update"), ..) => {
            let _ = velum::call("POST", "/v1/update", None);
        }
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
