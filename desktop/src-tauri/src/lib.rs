//! Velum 桌面壳：界面 + 托盘 + velumd 进程管理。业务逻辑全部在 velumd（Go 核心）中。

#[cfg(target_os = "macos")]
mod menu_tabs;
#[cfg(target_os = "macos")]
mod netext;
mod system;
mod tray;
mod update;
mod velum;
#[cfg(windows)]
mod winpipe;

use std::time::Duration;
#[cfg(target_os = "macos")]
use tauri::image::Image;
use tauri::{tray::TrayIconBuilder, AppHandle, Emitter, Manager, WindowEvent};

/// 前端统一入口：转发到 velumd 本地接口。
#[tauri::command]
async fn velum_call(method: String, path: String, body: Option<String>) -> Result<serde_json::Value, String> {
    tauri::async_runtime::spawn_blocking(move || velum::call(&method, &path, body.as_deref()))
        .await
        .map_err(|e| e.to_string())?
}

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(f).await.map_err(|e| e.to_string())
}

#[tauri::command]
async fn helper_install() -> Result<(), String> {
    blocking(system::helper_install).await?
}

#[tauri::command]
async fn helper_uninstall() -> Result<(), String> {
    blocking(system::helper_uninstall).await?
}

/// 平台自行判断的 TUN 组件状态（Windows：Velum 服务）；None 表示由界面直接查询 velumd 的 /v1/helper（macOS）。
/// macOS 使用网络扩展时 TUN 由扩展提供，不需要 velum-helper，视为就绪。
#[tauri::command]
async fn helper_status() -> Result<Option<serde_json::Value>, String> {
    #[cfg(target_os = "macos")]
    if blocking(netext::enabled).await? {
        return Ok(Some(serde_json::json!({ "socket": "", "reachable": true, "version": "NE" })));
    }
    blocking(system::helper_status).await
}

/// 网络扩展（实验）：bundled 为本 App 是否内置扩展，enabled 为是否正在使用。
#[tauri::command]
async fn netext_status() -> serde_json::Value {
    #[cfg(target_os = "macos")]
    return serde_json::json!({ "bundled": netext::bundled(), "enabled": netext::enabled() });
    #[cfg(not(target_os = "macos"))]
    serde_json::json!({ "bundled": false, "enabled": false })
}

#[tauri::command]
async fn netext_set(on: bool) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    return blocking(move || if on { netext::enable() } else { netext::disable() }).await?;
    #[cfg(not(target_os = "macos"))]
    {
        let _ = on;
        Err("只有 macOS 支持网络扩展".into())
    }
}

/// 确保核心在运行：使用网络扩展时启动隧道（核心在扩展里），否则启动 velumd。
fn ensure_core() -> Result<(), String> {
    #[cfg(target_os = "macos")]
    if netext::enabled() {
        return netext::ensure_running();
    }
    velum::ensure_running()
}

#[tauri::command]
fn cli_status() -> serde_json::Value {
    system::cli_status()
}

#[tauri::command]
async fn cli_install() -> Result<(), String> {
    blocking(system::cli_install).await?
}

#[tauri::command]
async fn cli_uninstall() -> Result<(), String> {
    blocking(system::cli_uninstall).await?
}

#[tauri::command]
fn autostart_get() -> bool {
    system::autostart_get()
}

#[tauri::command]
fn autostart_set(on: bool) -> Result<(), String> {
    system::autostart_set(on)
}

/// 用默认浏览器打开 Velum 的发布页或安装包（链接来自 velumd 查询的 GitHub Releases，只允许这个仓库）。
#[tauri::command]
fn open_release(url: String) -> Result<(), String> {
    if !url.starts_with("https://github.com/Foreverwzh/velum/") {
        return Err(format!("不是 Velum 的发布链接：{url}"));
    }
    #[cfg(target_os = "macos")]
    let cmd = std::process::Command::new("/usr/bin/open").arg(&url).status();
    // explorer 打开链接后退出码不为 0，不检查
    #[cfg(windows)]
    let cmd = std::process::Command::new("explorer").arg(&url).status();
    #[cfg(not(any(target_os = "macos", windows)))]
    let cmd = std::process::Command::new("xdg-open").arg(&url).status();
    cmd.map(|_| ()).map_err(|e| e.to_string())
}

/// 自动更新到 manifest（velumd 查到的发布清单）指向的版本；成功时 App 会重启，不会返回。
#[tauri::command]
async fn install_update(app: AppHandle, manifest: String) -> Result<(), String> {
    update::install(app, manifest).await
}

#[tauri::command]
fn update_progress() -> Option<update::Progress> {
    update::progress()
}

/// 托盘上的一句话状态（与界面 health 口径一致的简化版）。
pub(crate) fn summary(s: &serde_json::Value) -> String {
    let on = |k: &str| s[k].as_bool().unwrap_or(false);
    if on("starting") {
        return "正在启动…".into();
    }
    if let Some(e) = s["last_error"].as_str() {
        return format!("配置有问题：{e}");
    }
    let mut parts = vec![];
    if on("system_proxy_on") {
        parts.push("系统代理");
    }
    if on("tun_on") {
        parts.push("TUN");
    }
    let paused = s["network"]["paused"].as_str().filter(|_| (on("want_system_proxy") && !on("system_proxy_on")) || (on("want_tun") && !on("tun_on")));
    match (parts.is_empty(), paused) {
        (_, Some(p)) if parts.is_empty() => format!("已暂停：{p}"),
        (false, Some(_)) => format!("正在保护（{}，部分已暂停）", parts.join(" + ")),
        (false, None) => format!("正在保护（{}）", parts.join(" + ")),
        (true, None) => "仅本地端口".into(),
        _ => unreachable!(),
    }
}

/// 停止 velumd（还原系统代理 / TUN）。退出 App 时调用；只执行一次。
pub(crate) fn stop_velum() {
    static STOPPED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);
    if !STOPPED.swap(true, std::sync::atomic::Ordering::SeqCst) {
        // 使用网络扩展时停止隧道：核心随之停止，系统撤销隧道的路由与 DNS（只停核心会留下没有核心的隧道）
        #[cfg(target_os = "macos")]
        if netext::enabled() {
            let _ = netext::stop_for_quit();
            return;
        }
        let _ = velum::call("POST", "/v1/stop", None);
    }
}

pub(crate) fn show_main(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

pub fn run() {
    #[cfg(target_os = "macos")]
    if std::env::args().any(|a| a == "--activate-extension") {
        netext::activate_cli();
    }
    #[cfg(target_os = "macos")]
    for (flag, start) in [("--start-extension", true), ("--stop-extension", false)] {
        if std::env::args().any(|a| a == flag) {
            netext::tunnel_cli(start);
        }
    }
    tauri::Builder::default()
        .plugin(tauri_plugin_updater::Builder::new().build())
        .invoke_handler(tauri::generate_handler![velum_call, helper_install, helper_uninstall, helper_status, cli_status, cli_install, cli_uninstall, netext_status, netext_set, autostart_get, autostart_set, open_release, install_update, update_progress])
        .setup(|app| {
            let handle = app.handle().clone();
            // 开机启动（--hidden）时只在托盘运行，不弹出窗口
            if !std::env::args().any(|a| a == "--hidden") {
                show_main(&handle);
            }

            // ---- 托盘（菜单见 tray.rs）----
            #[cfg(target_os = "macos")]
            menu_tabs::install(&handle);
            tray::load_prefs(&handle);
            let menu = tray::initial(&handle)?;
            // macOS 菜单栏用单色模板图标（随深浅色自动反色）；Windows 任务栏用彩色应用图标
            #[cfg(target_os = "macos")]
            let icon = Image::from_bytes(include_bytes!("../tray-template.png"))?;
            #[cfg(not(target_os = "macos"))]
            let icon = app.default_window_icon().cloned().ok_or("缺少应用图标")?;
            TrayIconBuilder::with_id("main")
                .icon(icon)
                .icon_as_template(true)
                .tooltip("Velum")
                .menu(&menu)
                .show_menu_on_left_click(true)
                .on_menu_event(|app, event| {
                    let app = app.clone();
                    let id = event.id().as_ref().to_string();
                    std::thread::spawn(move || tray::handle(&app, &id));
                })
                .build(app)?;

            // ---- velumd 生命周期与状态轮询（更新托盘）----
            {
                let app = handle.clone();
                std::thread::spawn(move || {
                    if let Err(e) = ensure_core() {
                        tray::refresh(&app, None, &e);
                    }
                    let opened = std::time::Instant::now();
                    let mut connected = false;
                    let mut ever_connected = false;
                    // 网络扩展意外断开后的重连：核心连续 3 秒不响应后尝试，失败按 5s → 60s 退避
                    #[cfg(target_os = "macos")]
                    let (mut down_since, mut next_try, mut backoff) = (None::<std::time::Instant>, std::time::Instant::now(), Duration::from_secs(5));
                    // 每秒更新实时流量，每 3 秒更新完整状态
                    let mut tick = 0u64;
                    loop {
                        tick += 1;
                        // 连不上时每秒重试，尽快发现恢复（网络扩展意外断开后的重连也依赖它）
                        if tick % 3 == 1 || !connected {
                            match velum::call("GET", "/v1/status", None) {
                                Ok(s) => {
                                    connected = true;
                                    ever_connected = true;
                                    #[cfg(target_os = "macos")]
                                    {
                                        down_since = None;
                                        backoff = Duration::from_secs(5);
                                        next_try = std::time::Instant::now();
                                    }
                                    tray::refresh(&app, Some(&s), &summary(&s));
                                }
                                // 刚打开时 velumd 还在启动，先不报未运行（只在还没连上过时：连上后再断开要立即处理，网络扩展的重连依赖它）
                                Err(_) if !ever_connected && opened.elapsed() < Duration::from_secs(20) => {}
                                Err(_) => {
                                    connected = false;
                                    tray::refresh(&app, None, "未运行");
                                    #[cfg(target_os = "macos")]
                                    {
                                        let now = std::time::Instant::now();
                                        let since = *down_since.get_or_insert(now);
                                        if now.duration_since(since) >= Duration::from_secs(3) && now >= next_try {
                                            next_try = now + backoff;
                                            backoff = (backoff * 2).min(Duration::from_secs(60));
                                            match netext::recover() {
                                                Ok(true) => eprintln!("[netext] 已重连隧道"),
                                                Ok(false) => {}
                                                Err(e) => {
                                                    eprintln!("[netext] 重连失败（{}s 后重试）：{e}", backoff.as_secs() / 2);
                                                    tray::refresh(&app, None, &format!("网络扩展重连失败：{e}"));
                                                }
                                            }
                                        }
                                    }
                                }
                            }
                        }
                        if connected {
                            let t = velum::call("GET", "/v1/traffic", None).ok();
                            tray::traffic(&app, t.as_ref());
                        }
                        std::thread::sleep(Duration::from_secs(1));
                    }
                });
            }

            // ---- 连接事件流：转发给前端，断开后重连 ----
            std::thread::spawn(move || loop {
                let h = handle.clone();
                let _ = velum::stream(|v| {
                    let _ = h.emit("velum://connection", v);
                });
                std::thread::sleep(Duration::from_secs(2));
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            // 关闭窗口只是隐藏到托盘，Velum 继续运行
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building Velum")
        .run(|_app, event| match event {
            // 退出 App 即停止 Velum，与 Stash 一致；关闭窗口只隐藏到托盘，不会走到这里。
            // 托盘退出等经 app.exit 的退出触发 ExitRequested；Dock 退出、AppleScript quit、注销 / 关机由系统发起，只触发 Exit
            tauri::RunEvent::ExitRequested { .. } | tauri::RunEvent::Exit => stop_velum(),
            // 窗口隐藏时点 Dock 图标：重新打开窗口
            #[cfg(target_os = "macos")]
            tauri::RunEvent::Reopen { .. } => show_main(_app),
            _ => {}
        });
}
