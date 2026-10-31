//! macOS 系统扩展（docs/MN-PLAN.md）：安装 / 替换 Velum.app 内的 packet tunnel 扩展。
//! 扩展只在 Developer ID 签名、带描述文件的构建里存在（desktop/extension/build.sh + tauri.ne.conf.json）。

use std::sync::{mpsc, Mutex};

use dispatch2::DispatchQueue;
use objc2::rc::Retained;
use objc2::runtime::ProtocolObject;
use objc2::{define_class, msg_send, AllocAnyThread, DefinedClass};
use objc2_foundation::{NSError, NSObject, NSObjectProtocol, NSString};
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
            Event::Replacing { from, to } => write!(f, "替换已安装的扩展 {from} → {to}"),
            Event::NeedsApproval => write!(f, "需要批准：系统设置 → 通用 → 登录项与扩展 → 网络扩展"),
            Event::Activated => write!(f, "扩展已激活"),
            Event::NeedsReboot => write!(f, "扩展将在重启后生效"),
            Event::Failed(e) => write!(f, "激活失败：{e}"),
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
