import SwiftUI

@main
struct VelumApp: App {
    @State private var model: AppModel = {
        #if DEBUG
        let name = UserDefaults.standard.string(forKey: "scenario") ?? "protected"
        return AppModel(core: DemoCore(DemoCore.Scenario(rawValue: name) ?? .protected))
        #else
        #error("真实核心尚未接入：演示数据不能进入 Release 构建")
        #endif
    }()

    var body: some Scene {
        WindowGroup {
            RootView().environment(model)
        }
    }
}
