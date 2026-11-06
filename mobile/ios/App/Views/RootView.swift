import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @State private var tab = Tab2.status
    @State private var demoApplied = false

    enum Tab2: Hashable { case status, activity, settings }

    var body: some View {
        @Bindable var model = model
        TabView(selection: $tab) {
            Tab("状态", systemImage: "shield.lefthalf.filled", value: Tab2.status) { StatusView() }
            Tab("活动", systemImage: "list.bullet.rectangle", value: Tab2.activity) { ActivityView() }
            Tab("设置", systemImage: "gearshape", value: Tab2.settings) { SettingsView() }
        }
        .tabViewBottomAccessory(isEnabled: model.hasSubscription) {
            // 随处可问：状态、活动、设置三个标签都能直接问一个节点或地址。
            Button { model.showSearch = true } label: {
                HStack(spacing: 8) {
                    Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                    Text("节点、或任意网址").foregroundStyle(.secondary)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 16)
                .frame(maxHeight: .infinity)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("搜索节点，或查询任意网址的线路")
        }
        .sheet(isPresented: $model.showSearch) { SearchView() }
        .fullScreenCover(isPresented: $model.onboarding) { OnboardingView() }
        .task(id: phase) {
            guard phase == .active else { return }
            (model.core as? RealCore)?.tunnel.reconcile()
            await model.poll()
        }
        .onAppear(perform: applyDemo)
    }

    /// 模拟器截图用：启动参数直接打开某一页。
    private func applyDemo() {
        #if DEBUG
        guard !demoApplied else { return }
        demoApplied = true
        let d = UserDefaults.standard
        switch d.string(forKey: "tab") {
        case "activity": tab = .activity
        case "settings": tab = .settings
        default: break
        }
        if d.string(forKey: "ask") != nil { model.showSearch = true }
        #endif
    }
}
