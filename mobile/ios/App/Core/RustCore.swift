// Rust 控制面（velum-engine 的 velum-core，经 VelumCore.xcframework）：取代 App 进程里的 Go daemon。
// 订阅拉取、编译、生成隧道文件都在 Rust 里完成；这里只把 velum-core 的类型换成界面现在用的模型。
// 过渡期用启动参数 -VelumRustCore YES 选用。规则解释由运行中的引擎回答（与真实连接同一段决策），不再逐条推演。
import Foundation
import Network
import NetworkExtension

final class RustCore: VelumCore, @unchecked Sendable {
    let tunnel = TunnelController()
    private let started: Task<CoreClient, Error>

    init() {
        let home = RealCore.home()
        let host = tunnel
        started = Task.detached(priority: .userInitiated) {
            try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
            return try CoreClient(home: home.path, host: host)
        }
        // 系统断开了隧道（用户在系统设置里断开、被别的 VPN 取代）：告诉核心，开关跟着关掉
        tunnel.onRevoked = { [started] in
            Task { try? await started.value.tunnelStopped() }
        }
        tunnel.onNetworkChanged = { [started] in
            Task.detached { try? await started.value.networkChanged() }
        }
    }

    /// App 回到前台：订阅到了更新时间就重新拉取（App 挂起期间后台检查不运行）。
    func foreground() {
        Task.detached { [started] in try? await started.value.refreshIfDue() }
    }

    /// 核心的调用会阻塞（拉取订阅、等隧道连上）：放到后台线程，不占用协作线程池。
    private func run<T: Sendable>(_ f: @escaping @Sendable (CoreClient) throws -> T) async throws -> T {
        let c = try await started.value
        return try await withCheckedThrowingContinuation { k in
            DispatchQueue.global(qos: .userInitiated).async {
                k.resume(with: Result { try f(c) })
            }
        }
    }


    func status() async throws -> Status { Self.map(try await run { $0.status() }) }
    func warnings() async throws -> [Warning] { try await run { $0.warnings() }.map(Self.map) }
    func setTunnel(on: Bool) async throws -> Status { Self.map(try await run { try $0.setTunnel(on) }) }
    func setDomainFirst(_ on: Bool) async throws -> Status { Self.map(try await run { try $0.setDomainFirst(on) }) }
    func select(group: String, member: String) async throws -> Status { Self.map(try await run { try $0.select(group: group, member: member) }) }
    func test(group: String) async throws -> [String: Int] { try await run { try $0.test(group: group) }.mapValues(Int.init) }
    func addProfile(url: String) async throws -> Status { Self.map(try await run { try $0.addProfile(url: url) }) }
    func activateProfile(id: String) async throws -> Status { Self.map(try await run { try $0.activateProfile(id: id) }) }
    func updateProfile(id: String) async throws -> Status { Self.map(try await run { try $0.updateProfile(id: id) }) }
    func deleteProfile(id: String) async throws -> Status { Self.map(try await run { try $0.deleteProfile(id: id) }) }

    // 隧道里只有 velum-engine，没有可切换的内核
    func setTunEngine(_ engine: String) async throws -> Status { try await status() }

    func traffic() async throws -> Traffic {
        let t = try await run { $0.traffic() }
        return Traffic(upRate: Int64(t.upRate), downRate: Int64(t.downRate), active: Int(t.active))
    }

    func connections() async throws -> [Connection] { try await run { try $0.connections(limit: 200) }.map(Self.map) }
    func why(target: String) async throws -> WhyResponse { Self.map(try await run { try $0.why(target) }) }

    func overlayItems() async throws -> [OverlayEntry] {
        try await run { try $0.overlayItems() }.map { OverlayEntry(kind: $0.kind.rawValue, value: $0.value, ip: $0.ip) }
    }

    func editOverlay(add: Bool, entry: OverlayEntry) async throws -> Status {
        guard let kind = VelumOverlayKind(rawValue: entry.kind) else { throw CoreError.message(String(localized: "未知类型 \(entry.kind)")) }
        let e = VelumOverlayEntry(kind: kind, value: entry.value, ip: entry.ip, line: nil)
        return Self.map(try await run { try $0.editOverlay(add: add, entry: e) })
    }

    func repair() async throws -> [RepairStep] {
        try await run { $0.repair() }.map { RepairStep(name: $0.name, ok: $0.ok, detail: $0.detail, warn: nil) }
    }

    // MARK: 类型转换（界面换成 Velum* 类型后删除）

    private static func date(_ secs: Int64?) -> Date? { secs.map { Date(timeIntervalSince1970: TimeInterval($0)) } }

    private static func map(_ i: VelumSubInfo?) -> SubInfo? {
        i.map { SubInfo(upload: Int64($0.upload), download: Int64($0.download), total: Int64($0.total), expire: date($0.expire)) }
    }

    private static func map(_ s: VelumStatus) -> Status {
        let sub = s.subscription.map {
            SubscriptionStatus(name: $0.name, source: $0.source, fetchedAt: date($0.fetchedAt), fetchError: $0.fetchError,
                               nodes: Int($0.nodes), groups: Int($0.groups), rules: Int($0.rules), ruleSets: Int($0.ruleSets),
                               info: map($0.info))
        } ?? SubscriptionStatus(name: nil, source: "", fetchedAt: nil, fetchError: nil, nodes: 0, groups: 0, rules: 0, ruleSets: 0, info: nil)
        let groups = s.groups.map { g in
            GroupStatus(tag: g.tag, type: g.kind == .select ? "select" : "urltest", selected: g.selected, members: g.members,
                        delays: g.delays.mapValues(Int.init),
                        policy: g.policy.map { NetPolicy(ssid: $0.ssid, cellular: $0.cellular, default: $0.default) })
        }
        let profiles = s.profiles.map { p in
            Profile(id: p.id, name: p.name, source: p.source, kind: p.kind.rawValue, active: p.active,
                    fetchedAt: date(p.fetchedAt), info: map(p.info))
        }
        return Status(version: s.version, running: true, tunOn: s.tunnel.on, wantTun: s.tunnel.wanted, starting: s.tunnel.busy,
                      domainFirst: s.domainFirst, tunEngine: "velum", warnings: Int(s.warnings), lastError: s.lastError,
                      groups: groups, subscription: sub, profiles: profiles, network: nil)
    }

    private static func map(_ s: VelumSourceRef) -> Source {
        Source(kind: s.kind, file: s.file, line: s.line.map(Int.init), text: s.text)
    }

    private static func ms(_ v: Int64) -> Date { Date(timeIntervalSince1970: TimeInterval(v) / 1000) }

    private static func map(_ c: VelumConnection) -> Connection {
        Connection(id: Int(c.id), start: ms(c.start), end: c.end.map(ms), network: c.network, domain: c.domain,
                   destination: c.destination, ruleIndex: ruleNumber(c.rule, c.source), rule: c.source.text ?? "",
                   provenance: map(c.source), chain: c.chain, upload: Int64(c.upload), download: Int64(c.download),
                   firstByteMs: c.firstByteMs.map(Int.init) ?? -1, durationMs: Int(c.durationMs),
                   errors: c.error.map { [$0] }, canceled: nil, connectMs: c.connectMs.map(Int.init) ?? -1)
    }

    /// 给用户看的规则序号：在它的出处（订阅、本地规则）里是第几条；没有命中规则时为 -1。
    private static func ruleNumber(_ rule: UInt32?, _ source: VelumSourceRef) -> Int {
        guard let rule else { return -1 }
        return source.ordinal.map(Int.init) ?? Int(rule) + 1
    }

    private static func map(_ e: VelumExplain) -> WhyResponse {
        let isIP = IPv4Address(e.host) != nil || IPv6Address(e.host) != nil
        let target = WhyReport.Target(raw: e.target, domain: isIP ? nil : e.host, ip: isIP ? e.host : nil, port: Int(e.port))
        let dns = WhyReport.DNS(applicable: e.dnsServer != nil, server: e.dnsServer, answer: nil, source: nil)
        let decision = WhyReport.Decision(ruleIndex: ruleNumber(e.rule, e.source), action: e.action.rawValue,
                                          outbound: e.chain.first, source: map(e.source), uncertain: nil)
        let notes = e.fromTunnel ? nil : [String(localized: "还没有连接：这是连接后会怎么走（与隧道里的规则相同）")]
        let report = WhyReport(target: target, dns: dns, rules: nil, decision: decision, notes: notes)
        return WhyResponse(report: report, chain: e.chain, resolved: e.resolved.isEmpty ? nil : e.resolved,
                           resolvedBy: e.resolved.isEmpty ? nil : "engine", resolveError: e.resolveError)
    }

    private static func map(_ w: VelumWarning) -> Warning {
        Warning(category: w.category, kind: "", level: w.level.rawValue, reason: w.message, subject: w.subject.isEmpty ? nil : w.subject,
                source: map(w.source))
    }
}

// MARK: 宿主（velum-core 回调）

extension TunnelController: VelumHost {
    func tunnelCall(_ request: Data) throws -> Data { try call(request) }

    /// 当前物理网络：类型、Wi-Fi 名称（有 VPN 配置的 App 才取得到）、DNS。
    func currentNetwork() -> VelumNetworkInfo {
        guard let iface = physicalInterface() else { return VelumNetworkInfo(kind: .none, ssid: "", iface: "", dns: []) }
        let kind: VelumNetworkKind = switch iface.type {
        case .wifi: .wifi
        case .cellular: .cellular
        case .wiredEthernet: .wired
        default: .none
        }
        var ssid = ""
        if kind == .wifi {
            let sem = DispatchSemaphore(value: 0)
            let box = SSIDBox()
            NEHotspotNetwork.fetchCurrent { n in
                box.value = n?.ssid ?? ""
                sem.signal()
            }
            if sem.wait(timeout: .now() + 1) == .success { ssid = box.value }
        }
        return VelumNetworkInfo(kind: kind, ssid: ssid, iface: iface.name, dns: physicalDNS(iface))
    }
}

private final class SSIDBox: @unchecked Sendable {
    var value = ""
}
