//! macOS：安装 / 卸载 TUN 组件（velum-helper，LaunchDaemon）、开机启动（LaunchAgent）。
//!
//! 没有付费开发者账号，无法使用 SMAppService，因此通过 osascript 请求一次管理员权限完成安装。
//! 安装脚本作为参数直接交给 osascript，不经过临时文件（避免被替换后以 root 执行）。

use std::os::unix::fs::MetadataExt;
use std::path::PathBuf;
use std::process::Command;

use crate::velum;

const HELPER_LABEL: &str = "dev.velum.helper";
const HELPER_BIN: &str = "/Library/PrivilegedHelperTools/dev.velum.helper";
const HELPER_PLIST: &str = "/Library/LaunchDaemons/dev.velum.helper.plist";
const HELPER_SOCKET: &str = "/var/run/velum-helper.sock";
const AGENT_LABEL: &str = "dev.velum.desktop";

/// 单引号转义，用于拼接 shell 脚本。
fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

fn current_uid() -> Result<u32, String> {
    let home = std::env::var("HOME").map_err(|e| e.to_string())?;
    std::fs::metadata(&home)
        .map(|m| m.uid())
        .map_err(|e| format!("无法确定当前用户：{e}"))
}

/// 以管理员身份执行脚本（系统会弹出密码框）。
fn run_admin(script: &str, prompt: &str) -> Result<(), String> {
    let out = Command::new("/usr/bin/osascript")
        .args([
            "-e", "on run argv",
            "-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
            "-e", "end run",
            script, prompt,
        ])
        .output()
        .map_err(|e| format!("无法请求管理员权限：{e}"))?;
    if out.status.success() {
        return Ok(());
    }
    let err = String::from_utf8_lossy(&out.stderr);
    if err.contains("-128") {
        return Err("已取消".into());
    }
    Err(err.trim().to_string())
}

pub fn helper_install() -> Result<(), String> {
    let src = velum::bin_path("velum-helper").ok_or("找不到内置的 velum-helper")?;
    let uid = current_uid()?;
    let plist = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{HELPER_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{HELPER_BIN}</string>
    <string>--allow-uid</string><string>{uid}</string>
    <string>--socket</string><string>{HELPER_SOCKET}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Library/Logs/velum-helper.log</string>
  <key>StandardErrorPath</key><string>/Library/Logs/velum-helper.log</string>
</dict>
</plist>
"#
    );
    let script = format!(
        r#"set -e
launchctl bootout system/{HELPER_LABEL} 2>/dev/null || true
pkill -x velum-helper 2>/dev/null || true
install -d -m 755 -o root -g wheel /Library/PrivilegedHelperTools
install -m 755 -o root -g wheel {src} {HELPER_BIN}
xattr -c {HELPER_BIN} 2>/dev/null || true
cat > {HELPER_PLIST} <<'VELUM_PLIST'
{plist}VELUM_PLIST
chown root:wheel {HELPER_PLIST}
chmod 644 {HELPER_PLIST}
launchctl enable system/{HELPER_LABEL} 2>/dev/null || true
ok=0
for i in 1 2 3 4 5; do
  if launchctl bootstrap system {HELPER_PLIST} 2>/dev/null; then ok=1; break; fi
  sleep 1
done
[ "$ok" = 1 ] || launchctl kickstart -k system/{HELPER_LABEL}
"#,
        src = sh_quote(&src.to_string_lossy()),
        plist = plist, // 内容全为常量与数字 uid，无外部输入
    );
    run_admin(
        &script,
        "Velum 要安装 TUN 组件（velum-helper），用于创建虚拟网卡和设置路由。",
    )
}

pub fn helper_uninstall() -> Result<(), String> {
    let script = format!(
        "launchctl bootout system/{HELPER_LABEL} 2>/dev/null || true\nrm -f {HELPER_PLIST} {HELPER_BIN} {HELPER_SOCKET}\n"
    );
    run_admin(&script, "Velum 要卸载 TUN 组件（velum-helper）。")
}

/// TUN 组件状态由 velumd 的 /v1/helper 报告（界面直接查询），这里不需要额外信息。
pub fn helper_status() -> Option<serde_json::Value> {
    None
}

const CLI_LINK: &str = "/usr/local/bin/vlu";

/// 命令行工具 vlu 的安装状态：/usr/local/bin/vlu 是否指向本 App 内置的 vlu（链接到 App 包内，更新 App 后无需重装）。
pub fn cli_status() -> serde_json::Value {
    let ours = velum::bin_path("vlu");
    let target = std::fs::read_link(CLI_LINK).ok();
    let exists = std::fs::symlink_metadata(CLI_LINK).is_ok();
    let installed = matches!((&target, &ours), (Some(t), Some(o)) if t == o);
    serde_json::json!({
        "installed": installed,
        "path": CLI_LINK,
        // 已存在但不是我们的链接（如其他位置安装的 vlu）：安装会替换它
        "conflict": (exists && !installed).then(|| target.map(|t| t.display().to_string()).unwrap_or_else(|| CLI_LINK.to_string())),
    })
}

/// 在 /usr/local/bin（macOS 默认 PATH 中）创建 vlu 的符号链接；该目录归 root 所有，需要管理员权限。
pub fn cli_install() -> Result<(), String> {
    let src = velum::bin_path("vlu").ok_or("找不到内置的 vlu")?;
    let script = format!(
        "mkdir -p /usr/local/bin && ln -sfn {} {CLI_LINK}",
        sh_quote(&src.to_string_lossy())
    );
    run_admin(&script, "Velum 要把命令行工具 vlu 安装到 /usr/local/bin。")
}

/// 只删除指向本 App 的链接，不动用户自己放置的 vlu。
pub fn cli_uninstall() -> Result<(), String> {
    if !cli_status()["installed"].as_bool().unwrap_or(false) {
        return Ok(());
    }
    run_admin(&format!("rm -f {CLI_LINK}"), "Velum 要移除命令行工具 vlu。")
}

fn agent_plist() -> Result<PathBuf, String> {
    let home = std::env::var("HOME").map_err(|e| e.to_string())?;
    Ok(PathBuf::from(home)
        .join("Library/LaunchAgents")
        .join(format!("{AGENT_LABEL}.plist")))
}

pub fn autostart_get() -> bool {
    agent_plist().map(|p| p.exists()).unwrap_or(false)
}

/// 开机启动：登录后在后台启动 Velum（不弹出窗口）。写入用户级 LaunchAgent，登录时由 launchd 加载。
pub fn autostart_set(on: bool) -> Result<(), String> {
    let path = agent_plist()?;
    if !on {
        return match std::fs::remove_file(&path) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => Err(e.to_string()),
            _ => Ok(()),
        };
    }
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let plist = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{AGENT_LABEL}</string>
  <key>ProgramArguments</key>
  <array><string>{}</string><string>--hidden</string></array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
"#,
        xml_escape(&exe.to_string_lossy())
    );
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    }
    std::fs::write(&path, plist).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quote() {
        assert_eq!(
            sh_quote("/Applications/Velum.app/x"),
            "'/Applications/Velum.app/x'"
        );
        assert_eq!(sh_quote("a'b"), "'a'\\''b'");
        assert_eq!(xml_escape("a&<b>"), "a&amp;&lt;b&gt;");
    }
}
