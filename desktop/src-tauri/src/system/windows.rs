//! Windows：TUN 组件即 Velum 服务（velumd 以 SYSTEM 运行，自建 wintun）。安装 / 卸载经 UAC 以管理员身份执行
//! `velumd service install|uninstall`；启动 / 停止与状态查询由安装用户直接执行（服务 DACL 已授权）。开机启动写 HKCU 的 Run 键。

use std::os::windows::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::velum;

const CREATE_NO_WINDOW: u32 = 0x0800_0000;
const RUN_KEY: &str = r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run";
const RUN_VALUE: &str = "Velum";

/// 运行命令（不弹出控制台窗口），返回 stdout；失败时返回 stderr。
pub fn run_hidden(bin: &Path, args: &[&str]) -> Result<String, String> {
    let out = Command::new(bin).args(args).creation_flags(CREATE_NO_WINDOW).output().map_err(|e| e.to_string())?;
    if out.status.success() {
        return Ok(String::from_utf8_lossy(&out.stdout).into_owned());
    }
    let err = String::from_utf8_lossy(&out.stderr);
    Err(if err.trim().is_empty() { format!("{} 失败：{}", bin.display(), out.status) } else { err.trim().to_string() })
}

/// `velumd service status`：{installed, running, outdated, binary, error}。
pub fn service_status() -> Result<serde_json::Value, String> {
    let bin = velum::velumd_path().ok_or("找不到内置的 velumd")?;
    let out = run_hidden(&bin, &["service", "status"])?;
    serde_json::from_str(&out).map_err(|e| e.to_string())
}

/// 按 Windows 命令行规则给参数加引号（反斜杠只在引号前需要转义）。
fn quote_arg(s: &str) -> String {
    if !s.is_empty() && !s.contains([' ', '\t', '"']) {
        return s.to_string();
    }
    let mut out = String::from("\"");
    let mut slashes = 0;
    for c in s.chars() {
        match c {
            '\\' => slashes += 1,
            '"' => {
                out.push_str(&"\\".repeat(slashes * 2 + 1));
                slashes = 0;
            }
            _ => {
                out.push_str(&"\\".repeat(slashes));
                slashes = 0;
            }
        }
        if c != '\\' {
            out.push(c);
        }
    }
    out.push_str(&"\\".repeat(slashes * 2));
    out.push('"');
    out
}

fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

/// Velum 服务的数据目录 %ProgramData%\Velum（只有 SYSTEM / 管理员可写，安装用户可读）。
pub fn service_data_dir() -> PathBuf {
    std::env::var("ProgramData").map(PathBuf::from).unwrap_or_else(|_| PathBuf::from(r"C:\ProgramData")).join("Velum")
}

/// 以管理员身份运行 velumd（系统会弹出 UAC 确认），等待结束。失败原因由 velumd 写入 %ProgramData%\Velum\logs\service.log。
fn run_elevated(args: &[&str]) -> Result<(), String> {
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{GetExitCodeProcess, WaitForSingleObject, INFINITE};
    use windows_sys::Win32::UI::Shell::{ShellExecuteExW, SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW};
    use windows_sys::Win32::UI::WindowsAndMessaging::SW_HIDE;

    let bin = velum::velumd_path().ok_or("找不到内置的 velumd")?;
    let params = args.iter().map(|a| quote_arg(a)).collect::<Vec<_>>().join(" ");
    let (verb, file, params) = (wide("runas"), wide(&bin.to_string_lossy()), wide(&params));
    let log = service_data_dir().join("logs").join("service.log");
    // 旧的失败记录由 velumd 在本次失败时覆盖；用户无权删除（目录只有管理员可写），按修改时间判断是否为本次
    let started = std::time::SystemTime::now();
    // SAFETY：结构体按文档初始化，字符串缓冲区在调用期间有效；进程句柄用完即关闭。
    unsafe {
        let mut info: SHELLEXECUTEINFOW = std::mem::zeroed();
        info.cbSize = std::mem::size_of::<SHELLEXECUTEINFOW>() as u32;
        info.fMask = SEE_MASK_NOCLOSEPROCESS;
        info.lpVerb = verb.as_ptr();
        info.lpFile = file.as_ptr();
        info.lpParameters = params.as_ptr();
        info.nShow = SW_HIDE;
        if ShellExecuteExW(&mut info) == 0 {
            let err = std::io::Error::last_os_error();
            return Err(if err.raw_os_error() == Some(1223) { "已取消".into() } else { format!("无法以管理员身份运行：{err}") });
        }
        WaitForSingleObject(info.hProcess, INFINITE);
        let mut code = 0u32;
        GetExitCodeProcess(info.hProcess, &mut code);
        CloseHandle(info.hProcess);
        if code != 0 {
            let fresh = std::fs::metadata(&log).and_then(|m| m.modified()).map(|t| t >= started).unwrap_or(false);
            let detail = if fresh { std::fs::read_to_string(&log).unwrap_or_default() } else { String::new() };
            return Err(if detail.trim().is_empty() { format!("操作失败（退出码 {code}）") } else { detail.trim().to_string() });
        }
    }
    Ok(())
}

/// 安装 / 更新 Velum 服务并启动；以用户身份运行的 velumd 会被停止，由服务接替。
pub fn helper_install() -> Result<(), String> {
    let sid = crate::winpipe::current_sid()?;
    let home = velum::home();
    run_elevated(&["service", "install", "--home", &home.to_string_lossy(), "--allow-sid", &sid])
}

/// 卸载 Velum 服务，然后以用户身份重新启动 velumd（只有系统代理），App 继续可用。
pub fn helper_uninstall() -> Result<(), String> {
    let home = velum::home();
    run_elevated(&["service", "uninstall", "--home", &home.to_string_lossy()])?;
    velum::ensure_running()
}

/// 供界面显示的 TUN 组件状态（与 velumd /v1/helper 同形）：服务已安装、正在为 App 服务且不是旧版本时可用。
pub fn helper_status() -> Option<serde_json::Value> {
    let st = service_status().ok()?;
    let h = velum::call("GET", "/v1/helper", None).unwrap_or_default();
    let outdated = st["installed"].as_bool() == Some(true) && st["outdated"].as_bool() == Some(true);
    let reachable = h["reachable"].as_bool() == Some(true) && !outdated;
    Some(serde_json::json!({
        "socket": "service",
        "version": h["version"].as_str().unwrap_or(""),
        "reachable": reachable,
        "outdated": outdated,
        "error": st["error"].as_str().or(h["error"].as_str()),
    }))
}

pub fn autostart_get() -> bool {
    Command::new("reg").args(["query", RUN_KEY, "/v", RUN_VALUE]).creation_flags(CREATE_NO_WINDOW).output().map(|o| o.status.success()).unwrap_or(false)
}

/// 开机启动：登录后在后台启动 Velum（只显示托盘图标）。
pub fn autostart_set(on: bool) -> Result<(), String> {
    if !on {
        if autostart_get() {
            run_hidden(Path::new("reg"), &["delete", RUN_KEY, "/v", RUN_VALUE, "/f"])?;
        }
        return Ok(());
    }
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let value = format!("{} --hidden", quote_arg(&exe.to_string_lossy()));
    run_hidden(Path::new("reg"), &["add", RUN_KEY, "/v", RUN_VALUE, "/t", "REG_SZ", "/d", &value, "/f"]).map(|_| ())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quoting() {
        assert_eq!(quote_arg("S-1-5-21-1"), "S-1-5-21-1");
        assert_eq!(quote_arg(r"C:\Users\a b\AppData\Roaming\Velum"), r#""C:\Users\a b\AppData\Roaming\Velum""#);
        assert_eq!(quote_arg(r"C:\a b\"), r#""C:\a b\\""#);
        assert_eq!(quote_arg(r#"a"b"#), r#""a\"b""#);
        assert_eq!(quote_arg(""), r#""""#);
    }
}
