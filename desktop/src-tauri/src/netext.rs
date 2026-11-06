//! macOS 系统扩展（docs/MN-PLAN.md）：安装 / 替换 Velum.app 内的 packet tunnel 扩展，启停隧道（核心在扩展里运行）。
//! 扩展只在 Developer ID 签名、带描述文件的构建里存在（desktop/extension/build.sh + tauri.ne.conf.json）。

use crate::i18n::{t, tf};
use std::io::Read;
use std::path::Path;
use std::sync::{mpsc, Mutex};
use std::time::{Duration, Instant};

use dispatch2::DispatchQueue;
use objc2::rc::Retained;
use objc2::runtime::ProtocolObject;
use objc2::{define_class, msg_send, AllocAnyThread, DefinedClass};
use block2::RcBlock;
use objc2_foundation::{NSArray, NSData, NSDate, NSDictionary, NSError, NSObject, NSObjectProtocol, NSRunLoop, NSString};
use objc2_network_extension::{NETunnelProviderManager, NETunnelProviderProtocol, NEVPNStatus};
use objc2_system_extensions::{
    OSSystemExtensionManager, OSSystemExtensionProperties, OSSystemExtensionReplacementAction, OSSystemExtensionRequest,
    OSSystemExtensionRequestDelegate, OSSystemExtensionRequestResult,
};

pub const EXTENSION_ID: &str = "dev.velum.desktop.tunnel";

/// 激活请求的进展。NeedsApproval 之后请求仍在进行，等待用户在系统设置中批准。
#[derive(Debug)]
pub enum Event {
    Replacing { from: String, to: String },
    NeedsApproval,
    Activated,
    NeedsReboot,
    Failed(String),
}

impl std::fmt::Display for Event {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Event::Replacing { from, to } => write!(f, "{}", tf("替换已安装的扩展 {0} → {1}", &[from, to])),
            Event::NeedsApproval => write!(f, "{}", t("需要批准：系统设置 → 通用 → 登录项与扩展 → 网络扩展")),
            Event::Activated => write!(f, "{}", t("扩展已激活")),
            Event::NeedsReboot => write!(f, "{}", t("扩展将在重启后生效")),
            Event::Failed(e) => write!(f, "{}", tf("激活失败：{0}", &[e])),
        }
    }
}

impl Event {
    pub fn done(&self) -> bool {
        matches!(self, Event::Activated | Event::NeedsReboot | Event::Failed(_))
    }
}

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "VelumExtensionRequestDelegate"]
    #[ivars = Mutex<mpsc::Sender<Event>>]
    struct Delegate;

    unsafe impl NSObjectProtocol for Delegate {}

    unsafe impl OSSystemExtensionRequestDelegate for Delegate {
        // 已安装的扩展总是替换：系统按 CFBundleVersion 判断新旧，build.sh 每次构建递增
        #[unsafe(method(request:actionForReplacingExtension:withExtension:))]
        fn replacing(
            &self,
            _r: &OSSystemExtensionRequest,
            existing: &OSSystemExtensionProperties,
            ext: &OSSystemExtensionProperties,
        ) -> OSSystemExtensionReplacementAction {
            let v = |p: &OSSystemExtensionProperties| unsafe { format!("{}({})", p.bundleShortVersion(), p.bundleVersion()) };
            self.send(Event::Replacing { from: v(existing), to: v(ext) });
            OSSystemExtensionReplacementAction::Replace
        }

        #[unsafe(method(requestNeedsUserApproval:))]
        fn needs_approval(&self, _r: &OSSystemExtensionRequest) {
            self.send(Event::NeedsApproval);
        }

        #[unsafe(method(request:didFinishWithResult:))]
        fn finished(&self, _r: &OSSystemExtensionRequest, result: OSSystemExtensionRequestResult) {
            self.send(if result == OSSystemExtensionRequestResult::Completed { Event::Activated } else { Event::NeedsReboot });
        }

        #[unsafe(method(request:didFailWithError:))]
        fn failed(&self, _r: &OSSystemExtensionRequest, error: &NSError) {
            self.send(Event::Failed(format!("{}（{}）", error.localizedDescription(), error.code())));
        }
    }
);

impl Delegate {
    fn new(tx: mpsc::Sender<Event>) -> Retained<Self> {
        let this = Self::alloc().set_ivars(Mutex::new(tx));
        unsafe { msg_send![super(this), init] }
    }

    fn send(&self, e: Event) {
        if let Ok(tx) = self.ivars().lock() {
            let _ = tx.send(e);
        }
    }
}

/// 进行中的激活请求：持有 delegate（请求只弱引用它），从 events 读取进展直到 done。
pub struct Activation {
    _delegate: Retained<Delegate>,
    pub events: mpsc::Receiver<Event>,
}

/// 提交激活请求。扩展从主程序所在 App 的 Contents/Library/SystemExtensions 中查找；App 须位于 /Applications。
pub fn activate() -> Activation {
    let (tx, rx) = mpsc::channel();
    let delegate = Delegate::new(tx);
    let queue = DispatchQueue::new("dev.velum.desktop.netext", None);
    unsafe {
        let req = OSSystemExtensionRequest::activationRequestForExtension_queue(&NSString::from_str(EXTENSION_ID), &queue);
        req.setDelegate(Some(ProtocolObject::from_ref(&*delegate)));
        OSSystemExtensionManager::sharedManager().submitRequest(&req);
    }
    Activation { _delegate: delegate, events: rx }
}

/// `velum-desktop --activate-extension`：在终端里激活扩展并打印进展（MN.1 验收；界面引导见 MN.6）。
pub fn activate_cli() -> ! {
    let a = activate();
    for e in a.events.iter() {
        println!("{e}");
        if e.done() {
            std::process::exit(if matches!(e, Event::Failed(_)) { 1 } else { 0 });
        }
    }
    std::process::exit(1)
}

// ---- 隧道（MN.2）----

/// 等待 rx 的结果。NE 的回调在主队列上执行：在主线程（命令行模式）等待时要转动 run loop，否则回调永远不会到达。
fn wait<T>(rx: &mpsc::Receiver<T>, timeout: Duration) -> Option<T> {
    let deadline = Instant::now() + timeout;
    let on_main = objc2::MainThreadMarker::new().is_some();
    while Instant::now() < deadline {
        if on_main {
            NSRunLoop::currentRunLoop().runUntilDate(&NSDate::dateWithTimeIntervalSinceNow(0.05));
            if let Ok(v) = rx.try_recv() {
                return Some(v);
            }
        } else if let Ok(v) = rx.recv_timeout(Duration::from_millis(50)) {
            return Some(v);
        }
    }
    None
}

fn ns_err(e: *mut NSError) -> Option<String> {
    // SAFETY: NE 的回调传入的是有效的 NSError 或空指针
    unsafe { e.as_ref() }.map(|e| e.localizedDescription().to_string())
}

/// 读取 Velum 的 VPN 配置（providerBundleIdentifier 为扩展的那一份）；create 时没有就新建并保存（首次会请求「添加 VPN 配置」的批准）。
fn load_manager(create: bool) -> Result<Option<Retained<NETunnelProviderManager>>, String> {
    let (tx, rx) = mpsc::channel();
    let block = RcBlock::new(move |list: *mut NSArray<NETunnelProviderManager>, e: *mut NSError| {
        let found = unsafe { list.as_ref() }.and_then(|list| {
            list.iter().find(|m| {
                let id = unsafe { m.protocolConfiguration() }
                    .and_then(|p| p.downcast::<NETunnelProviderProtocol>().ok())
                    .and_then(|p| unsafe { p.providerBundleIdentifier() });
                id.is_some_and(|id| id.to_string() == EXTENSION_ID)
            })
        });
        let _ = tx.send(ns_err(e).map_or(Ok(found), Err));
    });
    unsafe { NETunnelProviderManager::loadAllFromPreferencesWithCompletionHandler(&block) };
    let found = wait(&rx, Duration::from_secs(10)).ok_or(t("读取 VPN 配置超时"))??;
    if found.is_some() || !create {
        return Ok(found);
    }
    let m = unsafe { NETunnelProviderManager::new() };
    unsafe {
        let p = NETunnelProviderProtocol::new();
        p.setProviderBundleIdentifier(Some(&NSString::from_str(EXTENSION_ID)));
        p.setServerAddress(Some(&NSString::from_str("Velum")));
        m.setProtocolConfiguration(Some(&p));
        m.setLocalizedDescription(Some(&NSString::from_str("Velum")));
        m.setEnabled(true);
    }
    let (tx, rx) = mpsc::channel();
    let block = RcBlock::new(move |e: *mut NSError| {
        let _ = tx.send(ns_err(e));
    });
    unsafe { m.saveToPreferencesWithCompletionHandler(Some(&block)) };
    // 首次保存会弹出「添加 VPN 配置」的系统对话框，等待用户操作
    if let Some(e) = wait(&rx, Duration::from_secs(600)).ok_or(t("保存 VPN 配置超时（未在 10 分钟内批准）"))? {
        return Err(tf("保存 VPN 配置：{0}", &[&e]));
    }
    // 保存后要重新读取，连接对象才可用
    let (tx, rx) = mpsc::channel();
    let block = RcBlock::new(move |e: *mut NSError| {
        let _ = tx.send(ns_err(e));
    });
    unsafe { m.loadFromPreferencesWithCompletionHandler(&block) };
    wait(&rx, Duration::from_secs(10)).ok_or(t("读取 VPN 配置超时"))?;
    Ok(Some(m))
}

fn status_name(s: NEVPNStatus) -> &'static str {
    match s {
        NEVPNStatus::Disconnected => t("已断开"),
        NEVPNStatus::Connecting => t("正在连接"),
        NEVPNStatus::Connected => t("已连接"),
        NEVPNStatus::Reasserting => t("正在重连"),
        NEVPNStatus::Disconnecting => t("正在断开"),
        _ => t("无效"),
    }
}

/// 等待连接状态变为 want，超时或中途断开时返回错误。
fn wait_status(m: &NETunnelProviderManager, want: NEVPNStatus, timeout: Duration) -> Result<(), String> {
    let conn = unsafe { m.connection() };
    let (never_tx, never) = mpsc::channel::<()>();
    let deadline = Instant::now() + timeout;
    let started = Instant::now();
    loop {
        let s = unsafe { conn.status() };
        if s == want {
            return Ok(());
        }
        if want == NEVPNStatus::Connected && s == NEVPNStatus::Disconnected && started.elapsed() > Duration::from_secs(1) {
            return Err(t("隧道启动失败（已断开），详见系统日志 subsystem dev.velum.desktop.tunnel").into());
        }
        if Instant::now() >= deadline {
            return Err(tf("等待隧道{0}超时（当前：{1}）", &[&status_name(want), &status_name(s)]));
        }
        wait(&never, Duration::from_millis(100));
        let _ = &never_tx;
    }
}

/// 随机令牌（32 字节，十六进制）。
fn token() -> Result<String, String> {
    let mut b = [0u8; 32];
    std::fs::File::open("/dev/urandom").and_then(|mut f| f.read_exact(&mut b)).map_err(|e| e.to_string())?;
    Ok(b.iter().map(|x| format!("{x:02x}")).collect())
}

/// 首次迁移交给扩展的文件（相对 Velum 目录）：配置、订阅列表、状态与订阅缓存。扩展只在自己的容器里没有时写入。
fn seed_files(home: &Path) -> Vec<(String, Vec<u8>)> {
    let mut out = Vec::new();
    for name in ["config.yaml", "overlay.yaml", "profiles.yaml", "state.json"] {
        if let Ok(b) = std::fs::read(home.join(name)) {
            out.push((name.to_string(), b));
        }
    }
    let mut stack = vec![home.join("cache")];
    while let Some(dir) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&dir) else { continue };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().is_none_or(|x| x != "db") {
                if let (Ok(rel), Ok(b)) = (p.strip_prefix(home), std::fs::read(&p)) {
                    out.push((rel.to_string_lossy().into_owned(), b));
                }
            }
        }
    }
    out
}

/// 启动隧道（核心随之在扩展里启动）：生成本地接口的端口与令牌，写入 Velum 目录的 extension.addr（0600）供界面与 vlu 使用，
/// 再把地址与首次迁移的文件放进启动参数。reset 时扩展先清空自己的数据目录（重新迁移）。
pub fn start_tunnel(reset: bool) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    let m = load_manager(true)?.ok_or(t("没有 VPN 配置"))?;
    let port = std::net::TcpListener::bind("127.0.0.1:0").and_then(|l| l.local_addr()).map_err(|e| e.to_string())?.port();
    let addr = format!("tcp://127.0.0.1:{port}?token={}", token()?);
    let home = crate::velum::home();
    std::fs::create_dir_all(&home).map_err(|e| e.to_string())?;
    let path = home.join("extension.addr");
    let _ = std::fs::remove_file(&path);
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)
        .and_then(|mut f| std::io::Write::write_all(&mut f, addr.as_bytes()))
        .map_err(|e| tf("写入 {0}：{1}", &[&path.display(), &e]))?;

    let mut keys: Vec<Retained<NSString>> = vec![NSString::from_str("socket")];
    let mut vals: Vec<Retained<NSObject>> = vec![Retained::into_super(NSString::from_str(&addr))];
    if reset {
        keys.push(NSString::from_str("reset"));
        vals.push(Retained::into_super(NSString::from_str("1")));
    }
    for (name, b) in seed_files(&home) {
        keys.push(NSString::from_str(&format!("files/{name}")));
        vals.push(Retained::into_super(NSData::with_bytes(&b)));
    }
    let key_refs: Vec<&NSString> = keys.iter().map(|k| &**k).collect();
    let opts = NSDictionary::from_retained_objects(&key_refs, &vals);
    unsafe { m.connection().startVPNTunnelWithOptions_andReturnError(Some(&opts)) }
        .map_err(|e| tf("启动隧道：{0}", &[&e.localizedDescription()]))?;
    wait_status(&m, NEVPNStatus::Connected, Duration::from_secs(30))
}

/// 退出 App：停止隧道，之后不再自动重连。
pub fn stop_for_quit() -> Result<(), String> {
    BUSY.store(true, std::sync::atomic::Ordering::SeqCst);
    stop_tunnel()
}

/// 停止隧道（核心随之停止）。
pub fn stop_tunnel() -> Result<(), String> {
    let Some(m) = load_manager(false)? else { return Ok(()) };
    unsafe { m.connection().stopVPNTunnel() };
    wait_status(&m, NEVPNStatus::Disconnected, Duration::from_secs(15))?;
    // 核心已随隧道停止：去掉地址文件，客户端不再尝试连接扩展
    let _ = std::fs::remove_file(crate::velum::home().join("extension.addr"));
    Ok(())
}

/// `velum-desktop --start-extension [--reset]` / `--stop-extension`：命令行启停隧道（MN.2 联调；界面接入见 MN.3 / MN.6）。
pub fn tunnel_cli(start: bool) -> ! {
    let r = if start { start_tunnel(std::env::args().any(|a| a == "--reset")) } else { stop_tunnel() };
    match r {
        Ok(()) => {
            println!("{}", if start { "隧道已连接" } else { "隧道已断开" });
            std::process::exit(0)
        }
        Err(e) => {
            println!("失败：{e}");
            std::process::exit(1)
        }
    }
}

// ---- 模式切换（实验开关，MN.7 前默认仍为 helper）----

/// 是否使用网络扩展：Velum 目录下有 extension.enabled 标记，且本 App 内置了扩展。
pub fn enabled() -> bool {
    bundled() && crate::velum::home().join("extension.enabled").exists()
}

/// 本 App 是否内置了扩展（只有 Developer ID 签名、带描述文件的构建才有）。
pub fn bundled() -> bool {
    std::env::current_exe()
        .ok()
        .and_then(|p| p.parent()?.parent().map(|c| c.join(format!("Library/SystemExtensions/{EXTENSION_ID}.systemextension"))))
        .is_some_and(|p| p.exists())
}

fn tunnel_status() -> Option<NEVPNStatus> {
    load_manager(false).ok().flatten().map(|m| unsafe { m.connection().status() })
}

fn tunnel_connected() -> bool {
    tunnel_status() == Some(NEVPNStatus::Connected)
}

/// NE 模式下的「确保核心在运行」：隧道已连接且本地接口可用时不动，否则启动隧道（Q1：隧道常驻，核心在扩展里）。
pub fn ensure_running() -> Result<(), String> {
    let _busy = Busy::hold();
    if tunnel_connected() {
        if crate::velum::extension_addr().is_some() && crate::velum::alive() {
            return Ok(());
        }
        // 隧道还在但核心不响应（或地址文件丢失）：重启隧道
        stop_tunnel()?;
    }
    start_tunnel(false)
}

/// 用户主动启停或切换模式期间为 true：自动重连（recover）不介入，避免把刚停掉的隧道又拉起来。
static BUSY: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

struct Busy;

impl Busy {
    fn hold() -> Self {
        BUSY.store(true, std::sync::atomic::Ordering::SeqCst);
        Busy
    }
}

impl Drop for Busy {
    fn drop(&mut self) {
        BUSY.store(false, std::sync::atomic::Ordering::SeqCst);
    }
}

/// 意外断开后的重连：系统不会自动重连隧道（扩展崩溃、被系统停止），由 App 重启。调用方在核心持续不响应时调用；
/// 只在使用网络扩展且没有进行中的启停时介入，返回是否做了处理。不以连接状态判断：刚读取的 VPN 配置，
/// 其连接状态由系统异步更新，可能还不是「已断开」（测试机上因此一再跳过，等到退避拉长才重连）。
pub fn recover() -> Result<bool, String> {
    if BUSY.load(std::sync::atomic::Ordering::SeqCst) || !enabled() {
        return Ok(false);
    }
    ensure_running().map(|()| true)
}

/// 切换到网络扩展：激活扩展（首次需要在系统设置中批准）→ 停止 velumd（它会还原 helper 的 TUN 与系统代理）→
/// 以用户目录的配置重新迁移并启动隧道。之后的设置保存在扩展的容器里。
pub fn enable() -> Result<(), String> {
    let _busy = Busy::hold();
    let a = activate();
    let deadline = Instant::now() + Duration::from_secs(600);
    loop {
        match a.events.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
            Ok(Event::Failed(e)) => return Err(tf("激活网络扩展失败：{0}", &[&e])),
            Ok(Event::NeedsReboot) => return Err(t("网络扩展将在重启后生效").into()),
            Ok(Event::Activated) => break,
            Ok(_) => {}
            Err(_) => return Err(t("激活网络扩展超时（未在 10 分钟内批准）").into()),
        }
    }
    let home = crate::velum::home();
    let _ = std::fs::remove_file(home.join("extension.addr"));
    if crate::velum::alive() {
        crate::velum::call("POST", "/v1/stop", None)?;
        let deadline = Instant::now() + Duration::from_secs(15);
        while crate::velum::alive() {
            if Instant::now() > deadline {
                return Err(t("velumd 未按时停止").into());
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }
    // 隧道起不来（拒绝添加 VPN 配置、扩展被禁用等）：velumd 已停，退回 velumd，不留下什么都没在运行的状态
    if let Err(e) = start_tunnel(true) {
        let _ = stop_tunnel();
        let back = crate::velum::ensure_running().err().map(|b| tf("；启动 velumd 也失败：{0}", &[&b])).unwrap_or_default();
        return Err(tf("网络扩展未能启动，已退回 TUN 组件（{0}）{1}", &[&e, &back]));
    }
    std::fs::write(home.join("extension.enabled"), b"").map_err(|e| e.to_string())
}

/// App 启动时使用网络扩展、但隧道起不来（扩展被用户在系统设置中移除或禁用、MDM 禁止、VPN 配置被删除且未获批准）：
/// 退回 velumd 并去掉标记，返回给用户看的说明；用户可在设置中重新开启。
pub fn fall_back(reason: &str) -> String {
    let msg = fall_back_inner(reason);
    if let Ok(mut n) = NOTICE.lock() {
        *n = Some(msg.clone());
    }
    msg
}

/// 退回 velumd 的说明，界面取走后显示一次（App 启动时可能还没有窗口）。
static NOTICE: Mutex<Option<String>> = Mutex::new(None);

pub fn take_notice() -> Option<String> {
    NOTICE.lock().ok()?.take()
}

pub fn has_notice() -> bool {
    NOTICE.lock().is_ok_and(|n| n.is_some())
}

fn fall_back_inner(reason: &str) -> String {
    let home = crate::velum::home();
    let _ = std::fs::remove_file(home.join("extension.enabled"));
    let _ = std::fs::remove_file(home.join("extension.addr"));
    wait_listen_port(&home);
    match crate::velum::ensure_running() {
        Ok(()) => tf("网络扩展未能启动，已退回 TUN 组件：{0}", &[&reason]),
        Err(e) => tf("网络扩展未能启动（{0}），启动 velumd 也失败：{1}", &[&reason, &e]),
    }
}

/// 切回 helper：停止隧道（系统撤销其路由与 DNS），去掉标记，启动 velumd。扩展容器里的设置不会带回用户目录。
pub fn disable() -> Result<(), String> {
    let _busy = Busy::hold();
    stop_tunnel()?;
    let home = crate::velum::home();
    let _ = std::fs::remove_file(home.join("extension.enabled"));
    wait_listen_port(&home);
    crate::velum::ensure_running()
}

/// 等入口端口可以绑定。扩展里的核心（root）停止后，其入口端口上的 TIME_WAIT 连接（核心经自身端口拉取规则集等）
/// 约 30 秒内仍归 root 所有；macOS 不允许普通用户绑定其他用户的套接字占着的端口（SO_REUSEADDR 也不行），
/// 此时启动的 velumd 会 address already in use，引擎起不来。
fn wait_listen_port(home: &Path) {
    let listen = std::fs::read_to_string(home.join("config.yaml"))
        .ok()
        .and_then(|c| c.lines().find_map(|l| l.strip_prefix("listen:").map(|v| v.trim().trim_matches('"').to_string())));
    let Some(addr) = listen else { return };
    let deadline = Instant::now() + Duration::from_secs(60);
    while std::net::TcpListener::bind(&addr).is_err() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(500));
    }
}
