//! Velum 桌面壳：界面 + 托盘 + velumd 进程管理。业务逻辑全部在 velumd（Go 核心）中。

mod system;
mod tray;
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
#[tauri::command]
async fn helper_status() -> Result<Option<serde_json::Value>, String> {
    blocking(system::helper_status).await
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
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![velum_call, helper_install, helper_uninstall, helper_status, cli_status, cli_install, cli_uninstall, autostart_get, autostart_set])
        .setup(|app| {
            let handle = app.handle().clone();
            // 开机启动（--hidden）时只在托盘运行，不弹出窗口
            if !std::env::args().any(|a| a == "--hidden") {
                show_main(&handle);
            }

            // ---- 托盘（菜单见 tray.rs）----
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
                    if let Err(e) = velum::ensure_running() {
                        tray::refresh(&app, None, &e);
                    }
                    let opened = std::time::Instant::now();
                    let mut connected = false;
                    loop {
                        match velum::call("GET", "/v1/status", None) {
                            Ok(s) => {
                                connected = true;
                                tray::refresh(&app, Some(&s), &summary(&s));
                            }
                            // 刚打开时 velumd 还在启动，先不报未运行
                            Err(_) if !connected && opened.elapsed() < Duration::from_secs(20) => {}
                            Err(_) => tray::refresh(&app, None, "未运行"),
                        }
                        std::thread::sleep(Duration::from_secs(3));
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
