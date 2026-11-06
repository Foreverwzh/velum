// 问一个问题：输入节点名就地切换，输入地址就地得到答案。答案就在结果里，不用再点一层。
import SwiftUI

struct SearchView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var answer: WhyResponse?
    @State private var answerError: String?
    @State private var answerFor = ""
    @State private var picked = 0
    @State private var path = NavigationPath()
    @FocusState private var focused: Bool

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if query.trimmingCharacters(in: .whitespaces).isEmpty { startContent } else { resultContent }
            }
            .navigationTitle("问一问")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, prompt: "节点、或任意网址")
            .searchFocused($focused)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .keyboardType(.default)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("关闭", systemImage: "xmark") { dismiss() }
                }
            }
            .velumRoutes()
            .onSubmit(of: .search) {
                if Fmt.looksLikeAddress(query) { model.remember(query); path.append(Route.explain(Fmt.cleanTarget(query))) }
            }
        }
        .presentationDragIndicator(.visible)
        .onAppear(perform: start)
        // 从解释页回来（可能加了规则）或切换节点后，结果页的答案要重算。
        .task(id: "\(query)|\(path.count)|\(model.selectionKey)") { await ask() }
        .sensoryFeedback(.selection, trigger: picked)
    }

    private func start() {
        focused = true
        #if DEBUG
        if let q = UserDefaults.standard.string(forKey: "ask"), query.isEmpty { query = q; focused = false }
        #endif
    }

    // MARK: 空状态

    @ViewBuilder private var startContent: some View {
        if !model.failedHosts.isEmpty {
            Section("最近失败") {
                ForEach(model.failedHosts, id: \.self) { h in
                    NavigationLink(value: Route.explain(h)) {
                        Label(h, systemImage: "xmark.circle.fill").labelStyle(TintedIcon(color: .err))
                    }
                }
            }
        }
        if !model.recentQueries.isEmpty {
            Section("最近查过") {
                ForEach(model.recentQueries, id: \.self) { q in
                    NavigationLink(value: Route.explain(q)) { Label(q, systemImage: "clock") }
                }
            }
        }
        Section {
            hint("network", "输入网址", "看它命中哪条规则、走哪条线路")
            hint("point.3.connected.trianglepath.dotted", "输入节点名", "在任一策略组里直接切换")
        }
    }

    private func hint(_ symbol: String, _ title: String, _ detail: String) -> some View {
        Label {
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.subheadline)
                Text(detail).font(.footnote).foregroundStyle(.secondary)
            }
        } icon: {
            Image(systemName: symbol).foregroundStyle(.secondary)
        }
    }

    // MARK: 结果

    @ViewBuilder private var resultContent: some View {
        let isAddress = Fmt.looksLikeAddress(query)
        let target = Fmt.cleanTarget(query)
        if isAddress {
            Section("这个地址") {
                NavigationLink(value: Route.explain(target)) { answerRow(target) }
                    .simultaneousGesture(TapGesture().onEnded { model.remember(target) })
            }
        }
        let hits = nodeHits
        ForEach(hits, id: \.group.tag) { hit in
            Section(hit.group.tag) {
                ForEach(hit.members, id: \.self) { m in nodeRow(m, in: hit.group) }
            }
        }
        if !isAddress && !target.isEmpty {
            Section {
                NavigationLink(value: Route.explain(target)) {
                    Label("查询 “\(target)” 走哪条线路", systemImage: "questionmark.circle")
                }
            } footer: {
                if hits.isEmpty { Text("没有名字包含 “\(query)” 的节点或策略组。") }
            }
        }
    }

    @ViewBuilder private func answerRow(_ target: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(target).font(.body.monospaced()).lineLimit(1).truncationMode(.middle)
            if let a = answer, answerFor == target {
                RouteChain(steps: ChainStep.from(a.chain ?? [], ruleIndex: a.report.decision.ruleIndex))
                if let t = a.report.decision.source.text {
                    Text(t).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1)
                }
            } else if let e = answerError {
                Text(e).font(.footnote).foregroundStyle(Color.err)
            } else {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    Text("正在推演…").font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.vertical, 2)
    }

    private func nodeRow(_ m: String, in g: GroupStatus) -> some View {
        Button {
            guard g.selected != m else { return }
            picked += 1
            Task { await model.select(group: g.tag, member: m) }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(.tint).opacity(g.selected == m ? 1 : 0)
                Text(m == "DIRECT" ? "直连" : m).foregroundStyle(.primary).lineLimit(2)
                Spacer()
                LatencyLabel(ms: m == "DIRECT" ? nil : model.delay(of: m))
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(g.selected == m ? .isSelected : [])
    }

    /// 只显示可手动切换的组；组名匹配时列出全部成员，否则只列出名字匹配的成员。
    private var nodeHits: [(group: GroupStatus, members: [String])] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return [] }
        return model.groups.filter(\.isSelectable).compactMap { g in
            if g.tag.localizedCaseInsensitiveContains(q) { return (g, g.members) }
            let m = g.members.filter { $0.localizedCaseInsensitiveContains(q) }
            return m.isEmpty ? nil : (g, m)
        }
    }

    private func ask() async {
        answerError = nil
        guard Fmt.looksLikeAddress(query) else { answer = nil; return }
        let target = Fmt.cleanTarget(query)
        if answerFor != target { answer = nil }
        try? await Task.sleep(for: .milliseconds(250)) // 输入停下来再问
        guard !Task.isCancelled else { return }
        do {
            let r = try await model.core.why(target: target)
            guard !Task.isCancelled else { return }
            answer = r
            answerFor = target
        } catch { answerError = error.localizedDescription }
    }
}

struct TintedIcon: LabelStyle {
    var color: Color
    func makeBody(configuration: Configuration) -> some View {
        Label { configuration.title } icon: { configuration.icon.foregroundStyle(color) }
    }
}
