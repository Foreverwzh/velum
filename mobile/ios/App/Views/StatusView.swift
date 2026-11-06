// 第一屏：只回答「有没有在保护」和「流量走哪条线路」，有需要处理的事才多出一节。
import SwiftUI

struct StatusView: View {
    @Environment(AppModel.self) private var model
    @State private var path = NavigationPath()

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    StatusHeader()
                    if let g = model.primaryGroup {
                        NavigationLink(value: Route.group(g.tag)) {
                            VStack(alignment: .leading, spacing: 6) {
                                HStack {
                                    // 没有接管流量时，这只是选择，不是正在走的线路。
                                    let live = model.status?.tunOn == true && model.status?.network?.captive != "captive" && model.status?.network?.captive != "offline"
                                    Text(live ? "当前线路" : "已选线路").font(.subheadline).foregroundStyle(.secondary)
                                    Spacer()
                                    LatencyLabel(ms: model.delay(of: g.tag), muted: !live)
                                }
                                RouteChain(steps: ChainStep.from(model.chain(from: g.tag)))
                            }
                            .padding(.vertical, 2)
                        }
                        NavigationLink(value: Route.groups) {
                            LabeledContent("全部策略组", value: "\(model.groups.count)")
                        }
                    }
                } footer: {
                    if let t = model.traffic {
                        // 大字号放不下一行时竖排，不把数字折断
                        ViewThatFits(in: .horizontal) {
                            HStack(spacing: 12) {
                                Label(Fmt.rate(t.downRate), systemImage: "arrow.down").fixedSize()
                                Label(Fmt.rate(t.upRate), systemImage: "arrow.up").fixedSize()
                                Spacer(minLength: 0)
                                Text("\(t.active) 条连接").fixedSize()
                            }
                            VStack(alignment: .leading, spacing: 4) {
                                Label(Fmt.rate(t.downRate), systemImage: "arrow.down")
                                Label(Fmt.rate(t.upRate), systemImage: "arrow.up")
                                Text("\(t.active) 条连接")
                            }
                        }
                        .font(.footnote.monospacedDigit())
                        .labelStyle(TightLabel())
                        .accessibilityElement(children: .combine)
                    }
                }
                if !model.attentions.isEmpty {
                    Section("需要留意") {
                        ForEach(model.attentions) { a in
                            NavigationLink(value: route(a.target)) {
                                Label {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(a.title)
                                        Text(a.detail).font(.footnote).foregroundStyle(.secondary).lineLimit(2)
                                    }
                                } icon: {
                                    Image(systemName: a.symbol).foregroundStyle(a.color)
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Velum")
            .refreshable { await model.refresh(); await model.refreshActivity() }
            .velumRoutes()
            .onAppear(perform: demoPush)
        }
    }

    /// 模拟器截图用：启动时直接进入某一页。
    private func demoPush() {
        #if DEBUG
        guard path.isEmpty, let p = UserDefaults.standard.string(forKey: "push") else { return }
        let parts = p.split(separator: ":", maxSplits: 1).map(String.init)
        switch parts[0] {
        case "groups": path.append(Route.groups)
        case "group": path.append(Route.group(parts[1]))
        case "explain": path.append(Route.explain(parts[1]))
        case "subs": path.append(Route.subscriptions)
        case "rules": path.append(Route.rules)
        case "warnings": path.append(Route.warnings)
        default: break
        }
        #endif
    }

    private func route(_ t: Attention.Target) -> Route {
        switch t {
        case .subscriptions: .subscriptions
        case .warnings: .warnings
        case .rules: .rules
        case let .group(g): .group(g)
        case let .explain(h): .explain(h)
        }
    }
}

struct TightLabel: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 3) {
            configuration.icon.foregroundStyle(.secondary)
            configuration.title
        }
    }
}

/// 状态、一句话结论、总开关。开关反映「想要的状态」，正在连接时立即变化并显示进度。
struct StatusHeader: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let h = model.health
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: h.symbol)
                    .font(.title)
                    .foregroundStyle(h.color)
                    .symbolEffect(.breathe, options: .speed(0.4), isActive: h.tone == .green)
                    .contentTransition(.symbolEffect(.replace))
                    .frame(width: 38)
                VStack(alignment: .leading, spacing: 2) {
                    Text(h.title).font(.title3.weight(.semibold))
                    Text(h.detail).font(.subheadline).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .animation(.default, value: h)
                Spacer(minLength: 8)
                if model.toggling {
                    ProgressView()
                } else {
                    Toggle("连接", isOn: Binding(get: { model.status?.wantTun == true }, set: { on in Task { await model.setTunnel(on) } }))
                        .labelsHidden()
                        .tint(h.tone == .amber ? Color.warn : Color.accentColor)
                        .disabled(model.status == nil || !model.hasSubscription || (model.status?.lastError != nil && model.status?.wantTun != true))
                }
            }
            if let e = model.actionError {
                Text(e).font(.footnote).foregroundStyle(Color.err).fixedSize(horizontal: false, vertical: true)
            }
            if model.status?.network?.captive == "captive" {
                Link("打开认证页", destination: URL(string: "http://captive.apple.com")!)
                    .font(.subheadline)
                    .padding(.leading, 50)
            }
        }
        .padding(.vertical, 4)
        .sensoryFeedback(.success, trigger: model.status?.tunOn == true) { _, on in on }
    }
}
