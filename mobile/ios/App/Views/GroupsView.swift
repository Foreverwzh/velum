import SwiftUI

/// 全部策略组：每行显示组名、当前线路和延迟。
struct GroupsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List(model.groups) { g in
            NavigationLink(value: Route.group(g.tag)) {
                VStack(alignment: .leading, spacing: 3) {
                    HStack {
                        Text(g.tag)
                        if !g.isSelectable { Text("自动").font(.caption2).foregroundStyle(.secondary) }
                        Spacer()
                        LatencyLabel(ms: model.delay(of: g.tag))
                    }
                    RouteChain(steps: ChainStep.from(Array(model.chain(from: g.tag).dropFirst())), compact: true)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .navigationTitle("策略组")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { TestButton(group: "") }
    }
}

struct TestButton: ToolbarContent {
    var group: String
    @Environment(AppModel.self) private var model

    var body: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            if model.testing {
                ProgressView()
            } else {
                Button("测速", systemImage: "gauge.with.needle") { Task { await model.test(group: group) } }
                    .labelStyle(.titleOnly)
            }
        }
    }
}

/// 一个策略组的成员：可搜索，测速后可按延迟排序。
struct MembersView: View {
    var tag: String
    @Environment(AppModel.self) private var model
    @State private var query = ""
    @State private var byDelay = false
    @State private var picked = 0

    var body: some View {
        let g = model.group(tag)
        List {
            if g?.isSelectable == false {
                Section {
                    Text("这个组按测速结果自动选择，不能手动切换。").font(.footnote).foregroundStyle(.secondary)
                }
            }
            if let p = g?.policy { policySection(p) }
            ForEach(members(g), id: \.self) { m in
                row(m, group: g)
            }
        }
        .overlay {
            if !query.isEmpty && members(g).isEmpty { ContentUnavailableView.search(text: query) }
        }
        .navigationTitle(tag)
        .navigationBarTitleDisplayMode(.inline)
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .automatic), prompt: "筛选节点")
        .toolbar {
            TestButton(group: tag)
            ToolbarItem(placement: .topBarTrailing) {
                Button { withAnimation { byDelay.toggle() } } label: {
                    Image(systemName: byDelay ? "arrow.up.arrow.down.circle.fill" : "arrow.up.arrow.down.circle")
                }
                .accessibilityLabel(byDelay ? "恢复订阅顺序" : "按延迟排序")
            }
        }
        .sensoryFeedback(.selection, trigger: picked)
    }

    /// 这个组会随网络自动切换：说清楚规则，手动选择只保持到下一次换网。
    private func policySection(_ p: NetPolicy) -> some View {
        Section {
            ForEach((p.ssid ?? [:]).sorted { $0.key < $1.key }, id: \.key) { ssid, member in
                LabeledContent {
                    Text(memberName(member))
                } label: {
                    Label(ssid, systemImage: "wifi")
                }
            }
            if let m = p.default {
                LabeledContent {
                    Text(memberName(m))
                } label: {
                    // 没写蜂窝时，蜂窝也走这一条
                    Label(p.cellular == nil ? String(localized: "其他网络") : String(localized: "其他 Wi-Fi"), systemImage: "wifi")
                }
            }
            if let m = p.cellular {
                LabeledContent {
                    Text(memberName(m))
                } label: {
                    Label("蜂窝网络", systemImage: "antenna.radiowaves.left.and.right")
                }
            }
        } header: {
            Text("按网络自动切换")
        } footer: {
            Text("手动选择保持到下一次换网。")
        }
    }

    private func memberName(_ m: String) -> String { m == "DIRECT" ? String(localized: "直连") : m }

    private func members(_ g: GroupStatus?) -> [String] {
        var list = g?.members ?? []
        if !query.isEmpty { list = list.filter { $0.localizedCaseInsensitiveContains(query) } }
        if byDelay {
            // 没有结果的排在有结果的之后，超时排最后。
            func key(_ m: String) -> Int {
                guard let d = model.delay(of: m) else { return 1_000_000 }
                return d < 0 ? 2_000_000 : d
            }
            list.sort { key($0) < key($1) }
        }
        return list
    }

    private func row(_ m: String, group g: GroupStatus?) -> some View {
        Button {
            guard g?.isSelectable == true, m != g?.selected else { return }
            picked += 1
            Task { await model.select(group: tag, member: m) }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "checkmark")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.tint)
                    .opacity(m == g?.selected ? 1 : 0)
                VStack(alignment: .leading, spacing: 2) {
                    Text(m == "DIRECT" ? String(localized: "直连") : m).foregroundStyle(.primary).lineLimit(2)
                    if model.group(m) != nil {
                        Text(model.chain(from: m).dropFirst().joined(separator: " → ")).font(.footnote).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                LatencyLabel(ms: m == "DIRECT" ? nil : model.delay(of: m))
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(m == g?.selected ? .isSelected : [])
    }
}
