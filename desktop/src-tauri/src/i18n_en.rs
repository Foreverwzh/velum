//! 中文源文字 → 英文。完整性由下面的测试保证。

pub fn lookup(src: &str) -> Option<&'static str> {
    Some(match src {
        "TUN（接管所有应用）" => "TUN (take over all apps)",
        "Velum 要卸载 TUN 组件（velum-helper）。" => "Velum wants to uninstall the TUN component (velum-helper).",
        "Velum 要安装 TUN 组件（velum-helper），用于创建虚拟网卡和设置路由。" => "Velum wants to install the TUN component (velum-helper) to create the virtual interface and set routes.",
        "Velum 要把命令行工具 vlu 安装到 /usr/local/bin。" => "Velum wants to install the command-line tool vlu to /usr/local/bin.",
        "Velum 要移除命令行工具 vlu。" => "Velum wants to remove the command-line tool vlu.",
        "Velum：启动中…" => "Velum: starting…",
        "extension.addr 无效：{0}" => "Invalid extension.addr: {0}",
        "velumd 未在 {0} 秒内响应（可能正在拉取订阅或规则集，稍后查看状态）" => "velumd did not respond within {0} seconds (it may be fetching the subscription or rule sets; check the status later)",
        "velumd 未按时停止" => "velumd did not stop in time",
        "velumd 未能在 10 秒内就绪，查看 logs/velumd.log" => "velumd was not ready within 10 seconds; see logs/velumd.log",
        "velumd 未运行" => "velumd is not running",
        "{0}\t{1} 到期" => "{0}\texpires {1}",
        "{0}\t剩余 {1}" => "{0}\t{1} left",
        "{0}\t超时" => "{0}\tTimeout",
        "{0} 失败：{1}" => "{0} failed: {1}",
        "一键修复" => "One-click repair",
        "下载新版本 {0}…" => "Download new version {0}…",
        "不是 Velum 的发布链接：{0}" => "Not a Velum release link: {0}",
        "仅本地端口" => "Local port only",
        "保存 VPN 配置超时（未在 10 分钟内批准）" => "Saving the VPN configuration timed out (not approved within 10 minutes)",
        "保存 VPN 配置：{0}" => "Save VPN configuration: {0}",
        "其他" => "Other",
        "写入 {0}：{1}" => "Write {0}: {1}",
        "写入用户 PATH 失败（{0}）" => "Could not write the user PATH ({0})",
        "只有 macOS 支持网络扩展" => "Only macOS supports the network extension",
        "启动 velumd 失败：{0}" => "Could not start velumd: {0}",
        "启动隧道：{0}" => "Start tunnel: {0}",
        "响应格式无效" => "Invalid response format",
        "在菜单栏显示网速" => "Show speed in the menu bar",
        "复制终端代理命令" => "Copy terminal proxy command",
        "已取消" => "Canceled",
        "已断开" => "disconnected",
        "已暂停：{0}" => "Paused: {0}",
        "已连接" => "connected",
        "延迟测试" => "Latency test",
        "所有连接都忙" => "All connections are busy",
        "打开 Velum" => "Open Velum",
        "打开用户环境变量失败（{0}）" => "Could not open the user environment variables ({0})",
        "扩展将在重启后生效" => "The extension takes effect after a restart",
        "扩展已激活" => "Extension activated",
        "找不到 velumd（可设置环境变量 VELUMD 指定路径）" => "velumd not found (set the VELUMD environment variable to its path)",
        "找不到内置的 velum-helper" => "Bundled velum-helper not found",
        "找不到内置的 velumd" => "Bundled velumd not found",
        "找不到内置的 vlu" => "Bundled vlu not found",
        "操作失败（退出码 {0}）" => "The operation failed (exit code {0})",
        "无效" => "invalid",
        "无法以管理员身份运行：{0}" => "Could not run as administrator: {0}",
        "无法确定当前用户：{0}" => "Could not determine the current user: {0}",
        "无法请求管理员权限：{0}" => "Could not request administrator rights: {0}",
        "无法连接 Velum 网络扩展：{0}" => "Could not connect to the Velum network extension: {0}",
        "无法连接 velumd：{0}" => "Could not connect to velumd: {0}",
        "更新到 {0}…" => "Update to {0}…",
        "更新订阅" => "Update subscription",
        "替换已安装的扩展 {0} → {1}" => "Replacing the installed extension {0} → {1}",
        "未运行" => "Not running",
        "检查更新…" => "Check for updates…",
        "正在保护（{0}）" => "Protected ({0})",
        "正在保护（{0}，部分已暂停）" => "Protected ({0}, partly paused)",
        "正在启动…" => "Starting…",
        "正在断开" => "disconnecting",
        "正在连接" => "connecting",
        "正在重连" => "reconnecting",
        "没有 VPN 配置" => "No VPN configuration",
        "活跃客户端" => "Active clients",
        "激活失败：{0}" => "Activation failed: {0}",
        "激活网络扩展失败：{0}" => "Could not activate the network extension: {0}",
        "激活网络扩展超时（未在 10 分钟内批准）" => "Activating the network extension timed out (not approved within 10 minutes)",
        "登录时启动" => "Start at login",
        "等待隧道{0}超时（当前：{1}）" => "Timed out waiting for the tunnel: {0} (current: {1})",
        "系统代理" => "System proxy",
        "缺少应用图标" => "The app icon is missing",
        "网络扩展将在重启后生效" => "The network extension takes effect after a restart",
        "网络扩展未能启动（{0}），启动 velumd 也失败：{1}" => "The network extension did not start ({0}), and starting velumd also failed: {1}",
        "网络扩展未能启动，已退回 TUN 组件（{0}）{1}" => "The network extension did not start, so Velum fell back to the TUN component ({0}){1}",
        "网络扩展未能启动，已退回 TUN 组件：{0}" => "The network extension did not start, so Velum fell back to the TUN component: {0}",
        "网络扩展重连失败：{0}" => "Reconnecting the network extension failed: {0}",
        "自动选择延迟最低的节点" => "Auto-select the lowest-latency node",
        "订阅" => "Subscription",
        "订阅\t{0}" => "Subscription\t{0}",
        "设置为系统代理" => "Set as system proxy",
        "读取 VPN 配置超时" => "Reading the VPN configuration timed out",
        "读取用户 PATH 失败（{0}）" => "Could not read the user PATH ({0})",
        "退出 Velum" => "Quit Velum",
        "配置有问题：{0}" => "Configuration problem: {0}",
        "重新加载配置" => "Reload configuration",
        "隧道启动失败（已断开），详见系统日志 subsystem dev.velum.desktop.tunnel" => "The tunnel failed to start (disconnected); see the system log, subsystem dev.velum.desktop.tunnel",
        "需要批准：系统设置 → 通用 → 登录项与扩展 → 网络扩展" => "Approval needed: System Settings → General → Login Items & Extensions → Network Extensions",
        "；启动 velumd 也失败：{0}" => "; starting velumd also failed: {0}",
        _ => return None,
    })
}

#[cfg(test)]
mod tests {
    use super::lookup;

    /// 源码里所有 t("…") / tf("…") 的键，测试模块之外。
    fn keys() -> Vec<String> {
        let files = [
            include_str!("tray.rs"), include_str!("netext.rs"), include_str!("velum.rs"), include_str!("lib.rs"),
            include_str!("system/macos.rs"), include_str!("system/windows.rs"),
        ];
        let mut out = vec![];
        for f in files {
            let code = f.split("#[cfg(test)]").next().unwrap();
            for pat in ["t(\"", "tf(\""] {
                for part in code.split(pat).skip(1) {
                    let mut key = String::new();
                    let mut chars = part.chars();
                    while let Some(c) = chars.next() {
                        match c {
                            '"' => break,
                            '\\' => match chars.next() {
                                Some('t') => key.push('\t'),
                                Some('n') => key.push('\n'),
                                Some(o) => key.push(o),
                                None => {}
                            },
                            c => key.push(c),
                        }
                    }
                    out.push(key);
                }
            }
        }
        out
    }

    #[test]
    fn every_string_is_translated() {
        let han = |s: &str| s.chars().any(|c| ('\u{4e00}'..='\u{9fff}').contains(&c));
        for k in keys().iter().filter(|k| han(k)) {
            let en = lookup(k).unwrap_or_else(|| panic!("缺少英文：{k}"));
            assert!(!han(en), "英文里还有中文：{en}");
            for i in 0..4 {
                let p = format!("{{{i}}}");
                assert_eq!(k.contains(&p), en.contains(&p), "占位符不一致：{k} → {en}");
            }
        }
    }
}
