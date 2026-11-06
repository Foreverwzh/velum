// 活动：经过 Velum 的连接。iOS 不能识别连接来自哪个应用，所以每行只有地址和线路。
import SwiftUI

struct ActivityView: View {
    @Environment(AppModel.self) private var model
    @State private var filter = "all"
    @State private var query = ""
    /// 正在显示的连接。滚离顶部或手指在滚动时不更新：新连接插到顶部会让内容跳动。
    @State private var shown: [Connection] = []
    @State private var atTop = true
    @State private var scrolling = false

    var body: some View {
        NavigationStack {
            List {
                ForEach(items) { c in
                    NavigationLink(value: Route.connection(c)) { ConnectionRow(c: c) }
                        .contextMenu {
                            Button("复制地址", systemImage: "doc.on.doc") { UIPasteboard.general.string = c.host }
                            NavigationLink(value: Route.explain(c.host)) { Label("推演线路", systemImage: "questionmark.circle") }
                        }
                }
            }
            .contentMargins(.top, 8, for: .scrollContent) // 搜索框下面不留大片空白
            .overlay { empty }
            .navigationTitle("活动")
            // 标题和搜索框都不随滚动折叠：折叠会改变内容偏移，和不断到来的新连接叠加就会来回跳
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "筛选地址或节点")
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Picker("筛选", selection: $filter) {
                        Text("全部").tag("all")
                        Text("失败").tag("failed")
                    }
                    .pickerStyle(.segmented)
                    .frame(width: 260) // 导航栏默认只给内容所需的宽度，两段会挤成一小块
                }
                .sharedBackgroundVisibility(.hidden) // 分段控件自带背景，不再套一层玻璃胶囊
            }
            .onScrollGeometryChange(for: Bool.self) { g in
                g.contentOffset.y <= -g.contentInsets.top + 8
            } action: { _, top in
                atTop = top
                syncIfIdle()
            }
            .onScrollPhaseChange { _, phase in
                scrolling = phase != .idle
                syncIfIdle()
            }
            .onChange(of: model.connections) { syncIfIdle() }
            .onAppear { shown = model.connections }
            .refreshable {
                await model.refreshActivity()
                shown = model.connections
            }
            .velumRoutes()
        }
    }

    private func syncIfIdle() {
        if atTop && !scrolling { shown = model.connections }
    }

    private var items: [Connection] {
        shown.filter { c in
            if filter == "failed" && !c.failed { return false }
            if query.isEmpty { return true }
            return c.host.localizedCaseInsensitiveContains(query) || c.chain.contains { $0.localizedCaseInsensitiveContains(query) }
        }
    }

    @ViewBuilder private var empty: some View {
        if items.isEmpty {
            if !query.isEmpty {
                ContentUnavailableView.search(text: query)
            } else if filter == "failed" {
                ContentUnavailableView("没有失败的连接", systemImage: "checkmark.circle", description: Text("最近的连接都正常。"))
            } else if model.status?.tunOn != true {
                ContentUnavailableView("还没有连接", systemImage: "list.bullet.rectangle", description: Text("连接 Velum 后，这里显示经过它的每条连接，以及命中的规则和线路。"))
            } else {
                ContentUnavailableView("等待连接", systemImage: "list.bullet.rectangle", description: Text("打开任意 App 后，这里会出现记录。"))
            }
        }
    }
}

struct ConnectionRow: View {
    var c: Connection

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(c.host).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 8)
                if c.failed {
                    Label("失败", systemImage: "xmark.circle.fill").labelStyle(TightErr()).font(.footnote)
                } else {
                    Text(Fmt.ago(c.end ?? c.start)).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
                }
            }
            RouteChain(steps: ChainStep.from(c.chain, ruleIndex: c.ruleIndex), compact: true)
        }
        .padding(.vertical, 1)
    }
}

struct TightErr: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 3) { configuration.icon; configuration.title }.foregroundStyle(Color.err)
    }
}

/// 一条真实连接发生了什么；「重新推演」回答现在的规则会怎么走。
struct ConnectionView: View {
    var connection: Connection

    var body: some View {
        let c = connection
        List {
            Section {
                VStack(alignment: .leading, spacing: 8) {
                    Text(c.host).font(.headline.monospaced()).textSelection(.enabled)
                    RouteChain(steps: ChainStep.from(c.chain, ruleIndex: c.ruleIndex))
                }
                .padding(.vertical, 2)
            }
            if let errs = c.errors, !errs.isEmpty {
                Section("错误") {
                    ForEach(errs, id: \.self) { Label($0, systemImage: "xmark.circle.fill").labelStyle(TintedIcon(color: .err)).font(.subheadline) }
                }
            }
            Section("命中的规则") {
                Text(c.provenance.text ?? c.rule).font(.subheadline.monospaced()).textSelection(.enabled)
                LabeledContent("来源", value: c.provenance.location)
            }
            Section("流量") {
                LabeledContent("下行", value: Fmt.bytes(c.download))
                LabeledContent("上行", value: Fmt.bytes(c.upload))
                if c.firstByteMs > 0 { LabeledContent("首字节", value: "\(c.firstByteMs) ms") }
                LabeledContent("目标", value: c.destination)
                LabeledContent("协议", value: c.network.uppercased())
            }
            .monospacedDigit()
            Section {
                NavigationLink(value: Route.explain(c.host)) { Text("按当前规则重新推演") }
            }
        }
        .navigationTitle("连接详情")
        .navigationBarTitleDisplayMode(.inline)
    }
}
