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
        #if !VELUM_ONLY
        // 对照用：启动参数 -VelumGoCore YES 改用 Go 核心（上架构建不含 Go）
        if UserDefaults.standard.bool(forKey: "VelumGoCore") { return AppModel(core: RealCore()) }
        #endif
        return AppModel(core: RustCore())
    }()

    var body: some Scene {
        WindowGroup {
            RootView().environment(model)
        }
    }
}
