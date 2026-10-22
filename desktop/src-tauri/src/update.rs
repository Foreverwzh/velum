//! 自动更新：velumd 按更新通道查到新版本与它的清单（latest.json），这里经 Tauri updater 下载本平台的更新包、
//! 校验签名（公钥见 tauri.conf.json）后安装并重启。进度经 velum://update 事件发给界面。

use std::sync::Mutex;

use serde::Serialize;
use tauri::{AppHandle, Emitter};
use tauri_plugin_updater::UpdaterExt;

use crate::velum;

/// 只接受这个仓库发布中的清单。
const MANIFEST_PREFIX: &str = "https://github.com/Foreverwzh/velum/releases/download/";

#[derive(Clone, Serialize, Default)]
pub struct Progress {
    /// download / install / error
    pub stage: String,
    pub downloaded: u64,
    pub total: Option<u64>,
    pub error: Option<String>,
}

static STATE: Mutex<Option<Progress>> = Mutex::new(None);

fn set(app: &AppHandle, p: Option<Progress>) {
    *STATE.lock().unwrap() = p.clone();
    let _ = app.emit("velum://update", p);
}

/// 当前的更新进度（界面打开设置页时读取）；没有进行中的更新时为 None。
pub fn progress() -> Option<Progress> {
    STATE.lock().unwrap().clone()
}

/// 下载并安装 manifest 指向的版本，成功后重启 App（Windows 由安装程序完成后重新打开）。
/// 已有更新在进行时直接返回。
pub async fn install(app: AppHandle, manifest: String) -> Result<(), String> {
    {
        let mut st = STATE.lock().unwrap();
        if st.as_ref().is_some_and(|p| p.stage != "error") {
            return Ok(());
        }
        *st = Some(Progress { stage: "download".into(), ..Default::default() });
    }
    let res = run(&app, &manifest).await;
    if let Err(e) = &res {
        set(&app, Some(Progress { stage: "error".into(), error: Some(e.clone()), ..Default::default() }));
    }
    res
}

async fn run(app: &AppHandle, manifest: &str) -> Result<(), String> {
    // debug 构建允许任意地址，便于用本地服务测试自动更新
    if !cfg!(debug_assertions) && !manifest.starts_with(MANIFEST_PREFIX) {
        return Err(format!("不是 Velum 的更新清单：{manifest}"));
    }
    let url = manifest.parse().map_err(|e| format!("更新清单地址无效：{e}"))?;
    let mut builder = app
        .updater_builder()
        .endpoints(vec![url])
        .map_err(|e| e.to_string())?
        // Windows 上安装程序启动后 App 立即退出，不经过正常退出流程：先停止 velumd（还原系统代理），也让安装程序能覆盖它
        .on_before_exit(crate::stop_velum);
    // 经 Velum 自己的本地端口下载（按用户的规则走代理）；velumd 未运行时直连
    let status = tauri::async_runtime::spawn_blocking(|| velum::call("GET", "/v1/status", None)).await.ok().and_then(Result::ok);
    if let Some(listen) = status.filter(|s| s["running"].as_bool() == Some(true)).and_then(|s| s["listen"].as_str().map(str::to_string)) {
        if let Ok(proxy) = format!("http://{listen}").parse() {
            builder = builder.proxy(proxy);
        }
    }
    let update = builder
        .build()
        .map_err(|e| e.to_string())?
        .check()
        .await
        .map_err(|e| format!("获取更新失败：{e}"))?
        .ok_or("已是最新版本")?;
    let mut downloaded = 0u64;
    let bytes = update
        .download(
            |n, total| {
                downloaded += n as u64;
                set(app, Some(Progress { stage: "download".into(), downloaded, total, error: None }));
            },
            || {},
        )
        .await
        .map_err(|e| format!("下载更新失败：{e}"))?;
    set(app, Some(Progress { stage: "install".into(), downloaded, total: Some(downloaded), error: None }));
    // macOS 替换 App 后重启；Windows 在这里启动安装程序并退出
    update.install(bytes).map_err(|e| format!("安装更新失败：{e}"))?;
    crate::stop_velum();
    app.restart();
}
