import SwiftUI

@main
struct VelumApp: App {
    @State private var model: AppModel = {
        #if DEBUG
        // 只有明确要求时才用演示数据（UI 测试、截图）
        if let name = UserDefaults.standard.string(forKey: "scenario") {
            return AppModel(core: DemoCore(DemoCore.Scenario(rawValue: name) ?? .protected))
        }
        #endif
        // 过渡期：启动参数 -VelumRustCore YES 改用 Rust 控制面（velum-core）
        if UserDefaults.standard.bool(forKey: "VelumRustCore") { return AppModel(core: RustCore()) }
        return AppModel(core: RealCore())
    }()

    var body: some Scene {
        WindowGroup {
            RootView().environment(model)
        }
    }
}
