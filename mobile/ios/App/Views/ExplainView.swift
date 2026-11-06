// 解释页：一个地址为什么这样走。先给结论（路由链），再给证据（DNS、命中的规则和来源行），最后给行动（本地规则）。
import SwiftUI

struct ExplainView: View {
    var target: String
    @Environment(AppModel.self) private var model
    @State private var r: WhyResponse?
    @State private var error: String?
    @State private var showRule = false
    @ScaledMetric(relativeTo: .body) private var iconWidth = 24
    /// 刚添加的本地规则，可以就地撤销。
    @State private var added: OverlayEntry?

    var body: some View {
        List {
            if let r { content(r) }
            else if let error {
                ContentUnavailableView {
                    Label("无法推演", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(error)
                } actions: {
                    Button("重试") { Task { await load() } }
                }
            } else {
                HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
            }
        }
        .navigationTitle(target)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: model.selectionKey) { await load() } // 换了节点，结论要跟着变
        .sheet(isPresented: $showRule) {
            RuleSheet(target: target) { entry in
                added = entry
                Task { await load() }
            }
        }
    }

    @ViewBuilder private func content(_ r: WhyResponse) -> some View {
        let d = r.report.decision
        Section {
            VStack(alignment: .leading, spacing: 8) {
                Text(sentence(r)).font(.headline)
                RouteChain(steps: ChainStep.from(r.chain ?? [], ruleIndex: d.ruleIndex))
                if let entry = added {
                    HStack {
                        Label("本地规则已生效", systemImage: "checkmark.circle.fill")
                            .foregroundStyle(Color.teal500)
                        Spacer()
                        Button("撤销") {
                            Task {
                                _ = try? await model.core.editOverlay(add: false, entry: entry)
                                added = nil
                                await load()
                            }
                        }
                        .buttonStyle(.borderless)
                    }
                    .font(.footnote)
                }
                if d.uncertain == true {
                    Label("结果取决于运行时才知道的信息，实际可能不同", systemImage: "questionmark.circle")
                        .font(.footnote).foregroundStyle(Color.warn)
                }
            }
            .padding(.vertical, 2)
        }

        Section("为什么") {
            if r.report.dns.applicable, let a = r.resolved?.first {
                step("DNS", symbol: "globe") {
                    Text(a).font(.subheadline.monospaced())
                    if let by = r.resolvedBy { Text("由 \(by) 解析").font(.footnote).foregroundStyle(.secondary) }
                }
            }
            if let e = r.resolveError {
                step("DNS", symbol: "globe") { Text(e).font(.footnote).foregroundStyle(Color.warn) }
            }
            step("规则 #\(d.ruleIndex)", symbol: "checkmark.seal") {
                if let t = d.source.text { Text(t).font(.subheadline.monospaced()).textSelection(.enabled) }
                Text("来自 " + d.source.location).font(.footnote).foregroundStyle(.secondary)
            }
            let misses = r.report.rules.filter { !$0.matched }
            if !misses.isEmpty {
                NavigationLink {
                    RuleTraceList(rules: r.report.rules)
                } label: {
                    Text("前面 \(misses.count) 条规则未命中").font(.subheadline)
                }
            }
        }

        if let notes = r.report.notes, !notes.isEmpty {
            Section("说明") { ForEach(notes, id: \.self) { Text($0).font(.footnote) } }
        }

        let real = model.connections.filter { $0.host == target }.prefix(3)
        if !real.isEmpty {
            Section {
                ForEach(Array(real)) { c in
                    NavigationLink(value: Route.connection(c)) { ConnectionRow(c: c) }
                }
            } header: {
                Text("最近的真实连接")
            } footer: {
                if real.contains(where: { $0.chain != (r.chain ?? []) }) {
                    Text("和上面的推演不同，可能是规则或线路变化之前的连接。")
                }
            }
        }

        Section {
            Button("添加本地规则…", systemImage: "plus.circle") { showRule = true }
        } footer: {
            Text("本地规则优先于订阅，比如让这个地址直连。")
        }
    }

    private func sentence(_ r: WhyResponse) -> String {
        guard let last = r.chain?.last else { return "没有匹配的线路" }
        if last == "DIRECT" { return "直连，不经过节点" }
        if last == "REJECT" || last.hasPrefix("reject") { return "被规则拦截" }
        return "经 \(last) 转发"
    }

    private func step<C: View>(_ title: String, symbol: String, @ViewBuilder content: () -> C) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: symbol).foregroundStyle(.secondary).frame(width: iconWidth)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.footnote).foregroundStyle(.secondary)
                content()
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func load() async {
        do { r = try await model.core.why(target: target); error = nil } catch { self.error = error.localizedDescription }
    }
}

/// 规则逐条推演：未命中的原因，以及最后命中的那一条。
struct RuleTraceList: View {
    var rules: [RuleTrace]

    var body: some View {
        List(rules) { t in
            VStack(alignment: .leading, spacing: 3) {
                HStack {
                    Text("#\(t.index)").font(.footnote.monospaced()).foregroundStyle(Color.info)
                    Text(t.reason).font(.footnote).foregroundStyle(t.matched ? Color.teal500 : .secondary)
                    Spacer()
                    if t.matched { Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(Color.teal500) }
                }
                if let x = t.source.text { Text(x).font(.footnote.monospaced()).lineLimit(2) }
                Text(t.source.location).font(.caption2).foregroundStyle(.secondary)
            }
        }
        .navigationTitle("规则推演")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// 添加本地规则。overlay 只支持直连和固定 IP，没有「走某个策略组」，所以这里也不提供。
struct RuleSheet: View {
    var target: String
    var onDone: (OverlayEntry) -> Void
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var action = "direct"
    @State private var base = ""
    @State private var scope = "suffix"
    @State private var ip = ""
    @State private var saving = false
    @State private var error: String?

    private var isIP: Bool { Fmt.isIP(target) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("动作", selection: $action) {
                        Text("直连").tag("direct")
                        if !isIP { Text("指定 IP").tag("host") }
                    }
                    .pickerStyle(.segmented)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }
                if !isIP {
                    Section("范围") {
                        if action == "direct" {
                            Picker("域名", selection: $base) {
                                ForEach(Fmt.parents(target), id: \.self) { Text($0).tag($0) }
                            }
                            Picker("匹配", selection: $scope) {
                                Text("含子域").tag("suffix")
                                Text("仅此域名").tag("exact")
                                Text("仅子域").tag("sub")
                            }
                        } else {
                            LabeledContent("域名", value: target)
                            TextField("IP 地址", text: $ip)
                                .keyboardType(.numbersAndPunctuation)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                        }
                    }
                }
                Section {
                    Text(summary).font(.footnote)
                } header: {
                    Text("将写入本地规则")
                } footer: {
                    if let error { Text(error).foregroundStyle(Color.err) }
                }
            }
            .navigationTitle("本地规则")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if saving { ProgressView() } else {
                        Button("添加") { Task { await save() } }.disabled(action == "host" && ip.isEmpty)
                    }
                }
            }
            .onAppear { if base.isEmpty { base = Fmt.parents(target).last ?? target } }
        }
        .presentationDetents([.medium, .large])
    }

    private var entry: OverlayEntry {
        if isIP { return OverlayEntry(kind: "cidr", value: target) }
        if action == "host" { return OverlayEntry(kind: "host", value: target, ip: ip.trimmingCharacters(in: .whitespaces)) }
        return OverlayEntry(kind: "domain", value: ["suffix": "", "exact": "=", "sub": "."][scope]! + base)
    }

    private var summary: String {
        if isIP { return "\(target) 直连" }
        if action == "host" { return "\(target) 解析为 \(ip.isEmpty ? "…" : ip)" }
        let what = ["suffix": "\(base) 及其子域", "exact": "仅 \(base)", "sub": "\(base) 的子域"][scope]!
        return "\(what) 直连"
    }

    private func save() async {
        saving = true
        defer { saving = false }
        do {
            _ = try await model.core.editOverlay(add: true, entry: entry)
            onDone(entry)
            dismiss()
        } catch { self.error = error.localizedDescription }
    }
}
