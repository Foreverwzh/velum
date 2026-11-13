// 界面状态：按需轮询核心，集中处理操作的进行中状态和错误。
import SwiftUI

@MainActor @Observable
final class AppModel {
    let core: VelumCore

    /// 真实的核心（不是演示数据）：可以读写 App Group 里的文件。
    var isRealCore: Bool {
        #if DEBUG
        return !(core is DemoCore)
        #else
        return true
        #endif
    }
    var status: Status?
    var traffic: Traffic?
    var connections: [Connection] = []
    var warnings: [Warning] = []
    var error: String?
    /// 上一次操作失败的原因，显示在状态区；下次操作时清除。
    var actionError: String?
    var toggling = false
    var testing = false
    /// 最新一次测速的结果（节点 → 毫秒，-1 为超时），与 status.groups 的 delays 合并显示。
    var delays: [String: Int] = [:]
    var showSearch = false
    var recentQueries: [String] = []
    /// 首次引导：没有订阅时打开；连接后或用户点「稍后」才关闭。
    var onboarding = false

    init(core: VelumCore) { self.core = core }

    var health: Health {
        let h = Health.of(status, error: error)
        // 隧道开着但当前节点不通：流量走不出去，不能显示「正在保护」。
        if h.tone == .green, let g = primaryGroup, delay(of: g.tag) == -1 {
            return Health(tone: .amber, title: String(localized: "当前节点超时"), detail: String(localized: "最近一次测速超时，可以换一个节点试试"), customSymbol: "exclamationmark.circle.fill")
        }
        return h
    }
    /// 所有策略组的当前选择；变化时，已经显示的推演结果要重算。
    var selectionKey: String { groups.map { $0.tag + "=" + ($0.selected ?? "") }.joined(separator: "|") }
    var groups: [GroupStatus] { status?.groups ?? [] }
    var hasSubscription: Bool { !(status?.subscription.source ?? "").isEmpty }
    /// 主线路：订阅中第一个可选策略组（Clash 订阅的惯例）。
    var primaryGroup: GroupStatus? { groups.first(where: \.isSelectable) ?? groups.first }

    func group(_ tag: String) -> GroupStatus? { groups.first { $0.tag == tag } }

    /// 沿当前选择走到最终节点：Proxy → 自动选择 → 香港 02。
    func chain(from tag: String) -> [String] {
        var out = [tag]
        var cur = tag
        for _ in 0..<8 {
            guard let sel = group(cur)?.selected, !sel.isEmpty else { break }
            out.append(sel)
            cur = sel
        }
        return out
    }

    func delay(of tag: String) -> Int? {
        let leaf = chain(from: tag).last ?? tag
        if leaf == "DIRECT" { return nil } // 直连不经过节点，这个数字没有意义
        if let d = delays[leaf] { return d }
        for g in groups { if let d = g.delays?[leaf] { return d } }
        return nil
    }

    func refresh() async {
        guard !toggling else { return } // 操作进行中不要用旧状态覆盖界面
        do {
            let first = status == nil
            status = try await core.status()
            error = nil
            if first && !hasSubscription { onboarding = true }
            if status?.tunOn == true { traffic = try? await core.traffic() } else { traffic = nil }
        } catch { self.error = error.localizedDescription }
    }

    func refreshActivity() async {
        if let c = try? await core.connections() {
            connections = c
            if isRealCore { AppFiles.saveConnections(c) }
        }
    }

    func refreshWarnings() async {
        if let w = try? await core.warnings() { warnings = w }
    }

    /// 只在 App 处于前台时轮询；隧道进程的内存有限，界面不看时不要求它出数据。
    func poll() async {
        await refresh()
        await refreshWarnings()
        await refreshActivity()
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(2))
            await refresh()
            await refreshActivity()
        }
    }

    func setTunEngine(_ velum: Bool) async {
        do { status = try await core.setTunEngine(velum ? "velum" : "") } catch { actionError = error.localizedDescription }
    }

    func setDomainFirst(_ on: Bool) async {
        do { status = try await core.setDomainFirst(on) } catch { actionError = error.localizedDescription }
    }

    func setTunnel(_ on: Bool) async {
        toggling = true
        actionError = nil
        defer { toggling = false }
        if on, var s = status { s.wantTun = true; status = s } // 立即显示「正在连接」
        do {
            status = try await core.setTunnel(on: on)
            if on { onboarding = false }
            toggling = false
        } catch {
            actionError = error.localizedDescription
            toggling = false // 先结束操作状态，否则下面的刷新会被跳过，界面停在「正在连接」
            await refresh()
        }
    }

    func select(group: String, member: String) async {
        actionError = nil
        // 乐观更新：切换是瞬时操作，失败时再改回。
        let old = status
        if let i = status?.groups?.firstIndex(where: { $0.tag == group }) { status?.groups?[i].selected = member }
        do { status = try await core.select(group: group, member: member) } catch {
            status = old
            actionError = error.localizedDescription
        }
    }

    func test(group: String = "") async {
        testing = true
        defer { testing = false }
        do {
            let r = try await core.test(group: group)
            delays.merge(r) { $1 }
        } catch { actionError = error.localizedDescription }
    }

    func remember(_ query: String) {
        let q = Fmt.cleanTarget(query)
        guard !q.isEmpty else { return }
        recentQueries.removeAll { $0 == q }
        recentQueries.insert(q, at: 0)
        if recentQueries.count > 6 { recentQueries.removeLast() }
    }

    /// 最近一分钟内失败的主机（去重）。更早的失败已经不用处理，留着只会一直误报。
    var failedHosts: [String] {
        var seen = Set<String>()
        return connections.filter { $0.failed && Date.now.timeIntervalSince($0.end ?? $0.start) < 60 }.map(\.host).filter { seen.insert($0).inserted }.prefix(3).map { $0 }
    }

    /// 需要留意的事项；为空时状态页不显示这一节。
    var attentions: [Attention] {
        guard let s = status else { return [] }
        var out: [Attention] = []
        if let e = s.lastError, !e.isEmpty {
            out.append(Attention(id: "cfg-sub", symbol: "arrow.clockwise.circle.fill", tone: .red, title: String(localized: "更换或更新订阅"), detail: String(localized: "问题来自订阅内容时"), target: .subscriptions))
            out.append(Attention(id: "cfg-rules", symbol: "text.badge.xmark", tone: .red, title: String(localized: "检查本地规则"), detail: String(localized: "问题来自本地规则时"), target: .rules))
        }
        if let e = s.subscription.fetchError, !e.isEmpty {
            out.append(Attention(id: "fetch", symbol: "arrow.clockwise.circle.fill", tone: .amber, title: String(localized: "订阅更新失败"), detail: e, target: .subscriptions))
        }
        if let g = primaryGroup, delay(of: g.tag) == -1, s.tunOn {
            out.append(Attention(id: "timeout", symbol: "arrow.triangle.2.circlepath", tone: .red, title: String(localized: "换一个节点"), detail: (chain(from: g.tag).last ?? "") + String(localized: " 超时"), target: .group(g.tag)))
        }
        if let i = s.subscription.info, i.total > 0, Double(i.upload + i.download) / Double(i.total) >= 0.9 {
            out.append(Attention(id: "quota", symbol: "gauge.with.dots.needle.100percent", tone: .amber, title: String(localized: "订阅流量快用完了"),
                                 detail: String(localized: "已用 \(Fmt.bytes(i.upload + i.download)) / \(Fmt.bytes(i.total))"), target: .subscriptions))
        }
        if s.warnings > 0 {
            out.append(Attention(id: "warn", symbol: "exclamationmark.triangle.fill", tone: .amber, title: String(localized: "\(s.warnings) 项订阅内容无法精确转换"), detail: String(localized: "查看被降级或跳过的内容"), target: .warnings))
        }
        if let f = failedHosts.first {
            out.append(Attention(id: "failed", symbol: "xmark.circle.fill", tone: .red, title: String(localized: "最近连接失败"), detail: f, target: .explain(f)))
        }
        // 影响当下使用的（红）排在前面
        return out.filter { $0.tone == .red } + out.filter { $0.tone != .red }
    }
}

struct Attention: Identifiable {
    enum Target: Hashable { case subscriptions, warnings, rules, group(String), explain(String) }
    var id: String
    var symbol: String
    var tone: Health.Tone
    var title: String
    var detail: String
    var target: Target
    var color: Color { Health(tone: tone, title: "", detail: "").color }
}

/// 导航目的地。状态页、搜索、活动共用同一套，保证解释页在任何入口都相同。
enum Route: Hashable {
    case groups
    case group(String)
    case explain(String)
    case connection(Connection)
    case subscriptions
    case rules
    case warnings
    case notices
}
