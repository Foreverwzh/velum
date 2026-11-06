import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var repairing = false
    @State private var steps: [RepairStep]?

    var body: some View {
        NavigationStack {
            List {
                Section("订阅") {
                    if let s = model.status?.subscription, !s.source.isEmpty {
                        NavigationLink(value: Route.subscriptions) {
                            VStack(alignment: .leading, spacing: 6) {
                                Text(s.name ?? "订阅")
                                Text("\(s.nodes) 个节点 · \(s.groups) 个策略组 · \(s.rules) 条规则")
                                    .font(.footnote).foregroundStyle(.secondary)
                                if let i = s.info { UsageBar(info: i) }
                            }
                            .padding(.vertical, 2)
                        }
                    }
                }
                Section("规则") {
                    NavigationLink(value: Route.rules) { Label("本地规则", systemImage: "text.badge.plus") }
                    NavigationLink(value: Route.warnings) {
                        LabeledContent {
                            if (model.status?.warnings ?? 0) > 0 { Text("\(model.status!.warnings)") }
                        } label: {
                            Label("转换提醒", systemImage: "exclamationmark.triangle")
                        }
                    }
                }
                Section {
                    Button {
                        Task {
                            repairing = true
                            steps = try? await model.core.repair()
                            repairing = false
                            await model.refresh()
                        }
                    } label: {
                        HStack {
                            Label("一键修复", systemImage: "wrench.and.screwdriver")
                            Spacer()
                            if repairing { ProgressView() }
                        }
                    }
                    .disabled(repairing)
                    if let steps {
                        ForEach(steps) { st in
                            Label {
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(st.name).font(.subheadline)
                                    if let d = st.detail { Text(d).font(.footnote).foregroundStyle(.secondary) }
                                }
                            } icon: {
                                Image(systemName: !st.ok ? "xmark.circle.fill" : st.warn == true ? "exclamationmark.circle.fill" : "checkmark.circle.fill")
                                    .foregroundStyle(!st.ok ? Color.err : st.warn == true ? Color.warn : Color.teal500)
                            }
                        }
                    }
                } header: {
                    Text("排查")
                } footer: {
                    if steps == nil { Text("重新检测网络、拉取订阅、加载内核，然后测速。") }
                }
                Section("关于") {
                    LabeledContent("版本", value: model.status?.version ?? "—")
                    Link(destination: URL(string: "https://github.com/Foreverwzh/velum")!) {
                        LabeledContent("源代码", value: "GPL-3.0-or-later")
                    }
                    #if DEBUG
                    LabeledContent("数据", value: "演示数据，未接入核心")
                    #endif
                }
            }
            .navigationTitle("设置")
            .velumRoutes()
        }
    }
}

struct UsageBar: View {
    var info: SubInfo

    var body: some View {
        let used = info.upload + info.download
        let frac = info.total > 0 ? min(1, Double(used) / Double(info.total)) : 0
        VStack(alignment: .leading, spacing: 3) {
            ProgressView(value: frac).tint(frac >= 0.9 ? Color.warn : Color.accentColor)
            HStack {
                Text("\(Fmt.bytes(used)) / \(Fmt.bytes(info.total))").monospacedDigit()
                Spacer()
                if let e = info.expire { Text("\(e.formatted(.dateTime.year().month().day())) 到期") }
            }
            .font(.caption).foregroundStyle(.secondary)
        }
    }
}

struct SubscriptionsView: View {
    @Environment(AppModel.self) private var model
    @State private var showAdd = false
    @State private var busy: String?
    @State private var error: String?

    var body: some View {
        List {
            if let e = model.status?.subscription.fetchError, !e.isEmpty {
                Section { Label(e, systemImage: "exclamationmark.triangle.fill").labelStyle(TintedIcon(color: .warn)).font(.footnote) }
            }
            if let error {
                Section { Text(error).font(.footnote).foregroundStyle(Color.err) }
            }
            Section {
                ForEach(model.status?.profiles ?? []) { p in
                    Button {
                        guard !p.active else { return }
                        act(p.id) { try await model.core.activateProfile(id: p.id) }
                    } label: {
                        HStack(spacing: 10) {
                            Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(.tint).opacity(p.active ? 1 : 0)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(p.name).foregroundStyle(.primary)
                                if let f = p.fetchedAt { Text("更新于 " + Fmt.ago(f)).font(.footnote).foregroundStyle(.secondary) }
                                if let i = p.info { UsageBar(info: i) }
                            }
                            if busy == p.id { Spacer(); ProgressView() }
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .swipeActions(edge: .trailing) {
                        if !p.active {
                            Button("删除", role: .destructive) { act(p.id) { try await model.core.deleteProfile(id: p.id) } }
                        }
                        Button("更新") { act(p.id) { try await model.core.updateProfile(id: p.id) } }.tint(.accentColor)
                    }
                }
            } footer: {
                Text("点一下切换，左滑更新或删除。正在使用的订阅不能删除。")
            }
        }
        .navigationTitle("订阅")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) { Button("添加", systemImage: "plus") { showAdd = true } }
        }
        .sheet(isPresented: $showAdd) {
            NavigationStack {
                SubscriptionForm(cta: "添加") { showAdd = false }
                    .navigationTitle("添加订阅")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { showAdd = false } } }
            }
            .presentationDetents([.medium])
        }
    }

    private func act(_ id: String, _ f: @escaping () async throws -> Status) {
        Task {
            busy = id
            error = nil
            defer { busy = nil }
            do { model.status = try await f() } catch { self.error = error.localizedDescription }
        }
    }
}

/// 订阅地址表单：先拉取校验，通过才保存。
struct SubscriptionForm: View {
    var cta: String
    var intro: String?
    var onDone: () -> Void
    @Environment(AppModel.self) private var model
    @State private var url = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        Form {
            if let intro {
                Section {
                    Text(intro).font(.subheadline).foregroundStyle(.secondary)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20))
                }
            }
            Section {
                TextField("https://…", text: $url, axis: .vertical)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                PasteButton(payloadType: String.self) { v in
                    if let s = v.first { Task { @MainActor in url = s.trimmingCharacters(in: .whitespacesAndNewlines) } }
                }
                .labelStyle(.titleAndIcon)
            } header: {
                Text("订阅地址")
            } footer: {
                if let error { Text(error).foregroundStyle(Color.err) } else { Text("Velum 先拉取并校验，通过才保存。地址只留在这台设备上。") }
            }
            Section {
                Button {
                    Task {
                        busy = true
                        error = nil
                        defer { busy = false }
                        do { model.status = try await model.core.addProfile(url: url); onDone() }
                        catch { self.error = error.localizedDescription }
                    }
                } label: {
                    HStack(spacing: 8) {
                        if busy { ProgressView().tint(.white) }
                        Text(busy ? "正在校验…" : cta)
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
                .disabled(url.isEmpty || busy)
            }
        }
    }
}

struct LocalRulesView: View {
    @Environment(AppModel.self) private var model
    @State private var items: [OverlayEntry] = []

    var body: some View {
        List {
            ForEach(items) { e in
                VStack(alignment: .leading, spacing: 2) {
                    Text(display(e)).font(.subheadline.monospaced())
                    Text(e.kind == "host" ? "解析为 \(e.ip ?? "")" : "直连").font(.footnote).foregroundStyle(.secondary)
                }
                .swipeActions {
                    Button("删除", role: .destructive) {
                        Task { _ = try? await model.core.editOverlay(add: false, entry: e); await load() }
                    }
                }
            }
        }
        .overlay {
            if items.isEmpty {
                ContentUnavailableView("没有本地规则", systemImage: "text.badge.plus", description: Text("查询一个地址后，可以在结果页添加。"))
            }
        }
        .navigationTitle("本地规则")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func display(_ e: OverlayEntry) -> String {
        if e.value.hasPrefix("=") { return e.value.dropFirst() + "（仅此域名）" }
        if e.value.hasPrefix(".") { return e.value.dropFirst() + "（仅子域）" }
        return e.kind == "domain" ? e.value + "（含子域）" : e.value
    }

    private func load() async { items = (try? await model.core.overlayItems()) ?? [] }
}

struct WarningsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List {
            ForEach(Dictionary(grouping: model.warnings, by: \.category).sorted { $0.key < $1.key }, id: \.key) { cat, items in
                Section(cat) {
                    ForEach(items) { w in
                        VStack(alignment: .leading, spacing: 3) {
                            if let s = w.subject { Text(s).font(.subheadline.monospaced()).lineLimit(2) }
                            Text(w.reason).font(.subheadline)
                            HStack(spacing: 6) {
                                Text(w.level == "unsupported" ? "不生效" : "已降级").foregroundStyle(w.level == "unsupported" ? Color.err : Color.warn)
                                Text(w.source.location).foregroundStyle(.secondary)
                            }
                            .font(.caption)
                        }
                    }
                }
            }
        }
        .overlay {
            if model.warnings.isEmpty {
                ContentUnavailableView("全部精确转换", systemImage: "checkmark.circle", description: Text("订阅里的内容都按原意生效。"))
            }
        }
        .navigationTitle("转换提醒")
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.refreshWarnings() }
    }
}
