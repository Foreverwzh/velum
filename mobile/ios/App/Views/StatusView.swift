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
                // 需要处理的事排在线路之前，不被一长串策略组挤到下面
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
                if !model.groups.isEmpty { routesSection }
            }
            .navigationTitle("Velum")
            .refreshable { await model.refresh(); await model.refreshActivity() }
            .velumRoutes()
            .onAppear(perform: demoPush)
        }
    }

    /// 没有接管流量时，这些只是选择，不是正在走的线路。
    private var live: Bool {
        model.status?.tunOn == true && model.status?.network?.captive != "captive" && model.status?.network?.captive != "offline"
    }

    /// 线路：订阅里每个可以手动选的策略组一行，直接看到各自走哪个节点；第一行是主线路，显示完整的路由链。
    /// 点一行进入成员列表；长按直接切换。按测速自动选择的组收在最后一行。
    private var routesSection: some View {
        let selectable = model.groups.filter(\.isSelectable)
        let auto = model.groups.count - selectable.count
        let rows = selectable.isEmpty ? Array(model.groups.prefix(1)) : selectable
        return Section {
            ForEach(Array(rows.enumerated()), id: \.element.tag) { i, g in
                NavigationLink(value: Route.group(g.tag)) {
                    GroupRow(group: g, primary: i == 0, live: live)
                }
                .contextMenu { if g.isSelectable { QuickPick(group: g) } }
            }
            if auto > 0 {
                NavigationLink(value: Route.groups) {
                    LabeledContent("自动选择的组", value: "\(auto)")
                }
            }
        } header: {
            HStack {
                Text(live ? "当前线路" : "已选线路")
                Spacer()
                if model.testing {
                    ProgressView().controlSize(.small)
                } else {
                    Button("测速") { Task { await model.test(group: "") } }
                        .font(.footnote)
                        .textCase(nil)
                }
            }
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

/// 状态页的一个策略组：组名与延迟，下面是它实际走到的节点。主线路用完整的路由链，其余用一行紧凑文字。
struct GroupRow: View {
    var group: GroupStatus
    var primary: Bool
    var live: Bool
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: primary ? 6 : 3) {
            HStack(alignment: .firstTextBaseline) {
                Text(group.tag)
                    .font(primary ? .body.weight(.semibold) : .body)
                    .lineLimit(1)
                Spacer(minLength: 8)
                LatencyLabel(ms: model.delay(of: group.tag), muted: !live)
            }
            let rest = Array(model.chain(from: group.tag).dropFirst())
            if primary {
                RouteChain(steps: ChainStep.from(rest))
            } else if !rest.isEmpty {
                RouteChain(steps: ChainStep.from(rest), compact: true)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, primary ? 2 : 0)
    }
}

/// 长按策略组直接切换成员，不必进入成员列表。
struct QuickPick: View {
    var group: GroupStatus
    @Environment(AppModel.self) private var model

    var body: some View {
        Picker(group.tag, selection: Binding(
            get: { group.selected ?? "" },
            set: { m in Task { await model.select(group: group.tag, member: m) } }
        )) {
            ForEach(group.members, id: \.self) { m in
                Text(m == "DIRECT" ? String(localized: "直连") : m).tag(m)
            }
        }
    }
}
