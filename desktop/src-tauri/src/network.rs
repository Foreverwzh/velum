//! 报告当前网络给核心，用于按网络切换策略组（订阅的 ssid-policy）。判断和切换都在核心里，这里只负责读系统。
//! 核心跑在服务或系统扩展里读不到 Wi-Fi 名称：macOS 要 App 自己申请定位权限。

use serde_json::Value;

use crate::velum;

/// 当前网络：kind 为 wifi / wired；连着 Wi-Fi 但读不到名称时 ssid 为空，核心不会切换。
#[derive(Debug, PartialEq)]
pub struct Network {
    pub kind: &'static str,
    pub ssid: String,
}

/// 用了 ssid-policy 且当前网络和核心记录的不同时报告。无状态：核心重启后记录清空，会自动重新报告。
pub fn sync(app: &tauri::AppHandle, status: &Value) {
    let used = status["groups"].as_array().is_some_and(|g| g.iter().any(|g| !g["policy"].is_null()));
    if !used {
        return;
    }
    prepare(app);
    let Some(n) = current() else { return };
    let hint = &status["network"]["hint"];
    if hint["kind"].as_str() == Some(n.kind) && hint["ssid"].as_str().unwrap_or("") == n.ssid {
        return;
    }
    let body = serde_json::json!({ "kind": n.kind, "ssid": n.ssid }).to_string();
    let _ = velum::call("PUT", "/v1/network-hint", Some(&body));
}

#[cfg(target_os = "macos")]
fn prepare(app: &tauri::AppHandle) {
    use std::sync::Once;
    static ASK: Once = Once::new();
    // 读 Wi-Fi 名称要定位权限。只在订阅确实用到时才申请，必须在主线程
    ASK.call_once(|| {
        let _ = app.run_on_main_thread(|| {
            use objc2_core_location::CLLocationManager;
            let m = unsafe { CLLocationManager::new() };
            unsafe { m.requestWhenInUseAuthorization() };
            std::mem::forget(m); // 管理器释放后系统弹窗会消失
        });
    });
}

#[cfg(target_os = "macos")]
fn current() -> Option<Network> {
    use objc2_core_wlan::CWWiFiClient;
    let iface = unsafe { CWWiFiClient::sharedWiFiClient().interface() };
    let Some(iface) = iface else { return Some(Network { kind: "wired", ssid: String::new() }) };
    // 有信道说明连着 Wi-Fi；名称要定位权限，没有权限时为空
    if unsafe { iface.wlanChannel() }.is_none() {
        return Some(Network { kind: "wired", ssid: String::new() });
    }
    Some(Network { kind: "wifi", ssid: unsafe { iface.ssid() }.map(|s| s.to_string()).unwrap_or_default() })
}

#[cfg(windows)]
fn prepare(_: &tauri::AppHandle) {}

#[cfg(windows)]
fn current() -> Option<Network> {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    let out = std::process::Command::new("netsh").args(["wlan", "show", "interfaces"]).creation_flags(CREATE_NO_WINDOW).output().ok()?;
    Some(parse_netsh(&String::from_utf8_lossy(&out.stdout)))
}

#[cfg(not(any(target_os = "macos", windows)))]
fn prepare(_: &tauri::AppHandle) {}

#[cfg(not(any(target_os = "macos", windows)))]
fn current() -> Option<Network> {
    None
}

/// 解析 netsh 的输出。SSID 这个字段名不随系统语言变化；要注意 BSSID 也包含它。
/// Windows 11 24H2 起读 Wi-Fi 信息也要定位权限，被拒绝时不能当成有线，否则在家也会走 default。
#[cfg_attr(not(windows), allow(dead_code))]
fn parse_netsh(output: &str) -> Network {
    for line in output.lines() {
        if let Some((k, v)) = line.split_once(':') {
            if k.trim() == "SSID" {
                return Network { kind: "wifi", ssid: v.trim().to_string() };
            }
        }
    }
    let low = output.to_lowercase();
    if low.contains("location") || output.contains("位置") {
        return Network { kind: "wifi", ssid: String::new() };
    }
    Network { kind: "wired", ssid: String::new() }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_netsh() {
        let wifi = "    Name : WLAN\n    State : connected\n    SSID                   : 我的家: 5G\n    BSSID                  : aa:bb:cc:dd:ee:ff\n";
        assert_eq!(parse_netsh(wifi), Network { kind: "wifi", ssid: "我的家: 5G".into() });
        let bssid_first = "    AP BSSID : aa:bb\n    SSID : home\n";
        assert_eq!(parse_netsh(bssid_first).ssid, "home");
        assert_eq!(parse_netsh("There is 0 interface on the system\n").kind, "wired");
        assert_eq!(parse_netsh("Network shell commands need location permission to access WLAN\n"), Network { kind: "wifi", ssid: String::new() });
    }
}
