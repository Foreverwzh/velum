// 演示核心：只在 Debug 下编译，用于在模拟器里检查界面和交互。所有数据都是虚构的，没有网络行为。
#if DEBUG
import Foundation

final class DemoCore: VelumCore, @unchecked Sendable {
    enum Scenario: String { case protected, off, firstRun, paused, captive, error, attention, denied }

    private let lock = NSLock()
    private var s: Status
    private var overlay: [OverlayEntry] = [OverlayEntry(kind: "domain", value: "internal.example.com")]
    private var conns: [Connection]
    private let scenario: Scenario
    private var nextID = 100

    init(_ scenario: Scenario) {
        self.scenario = scenario
        s = Self.status(scenario)
        conns = scenario == .firstRun || scenario == .off ? [] : Self.connections(failed: scenario == .attention)
    }

    private static let nodes = ["🇭🇰 香港 01", "🇭🇰 香港 02", "🇭🇰 香港 03 IEPL 专线", "🇯🇵 日本 01", "🇯🇵 日本 02", "🇸🇬 新加坡 01", "🇸🇬 新加坡 02", "🇺🇸 美国 01", "🇺🇸 美国 02 洛杉矶高速", "🇺🇸 美国 03"]
    private static let delays: [String: Int] = ["🇭🇰 香港 01": 58, "🇭🇰 香港 02": 72, "🇭🇰 香港 03 IEPL 专线": 41, "🇯🇵 日本 01": 96, "🇯🇵 日本 02": 340, "🇸🇬 新加坡 01": 140, "🇸🇬 新加坡 02": 165, "🇺🇸 美国 01": 212, "🇺🇸 美国 02 洛杉矶高速": 890, "🇺🇸 美国 03": -1]

    private static func status(_ sc: Scenario) -> Status {
        let info = SubInfo(upload: 3_200_000_000, download: sc == .attention ? 95_000_000_000 : 41_000_000_000, total: 107_374_182_400, expire: .now.addingTimeInterval(86400 * 42))
        let sub = SubscriptionStatus(name: "我的订阅", source: sc == .firstRun ? "" : "https://example.com/sub", fetchedAt: .now.addingTimeInterval(-3600 * 3),
                                     fetchError: sc == .attention ? "订阅服务返回 502：稍后重试，当前继续使用上次的内容" : nil, nodes: nodes.count, groups: 6, rules: 412, ruleSets: 33, info: info)
        let groups = sc == .firstRun ? [] : [
            GroupStatus(tag: "Proxy", type: "select", selected: sc == .attention ? "🇺🇸 美国 03" : "🇭🇰 香港 02", members: ["自动选择", "DIRECT"] + nodes, delays: delays),
            GroupStatus(tag: "自动选择", type: "urltest", selected: "🇭🇰 香港 03 IEPL 专线", members: nodes, delays: delays),
            GroupStatus(tag: "流媒体", type: "select", selected: "🇸🇬 新加坡 01", members: ["Proxy", "DIRECT"] + nodes, delays: delays),
            GroupStatus(tag: "家", type: "select", selected: "DIRECT", members: ["DIRECT", "🇭🇰 香港 01"], delays: delays,
                        policy: NetPolicy(ssid: ["我的家": "DIRECT"], cellular: "🇭🇰 香港 01", default: "🇭🇰 香港 01")),
            GroupStatus(tag: "AI", type: "select", selected: "🇺🇸 美国 01", members: ["Proxy"] + nodes, delays: delays),
            GroupStatus(tag: "苹果服务", type: "select", selected: "DIRECT", members: ["DIRECT", "Proxy"], delays: delays),
            GroupStatus(tag: "兜底", type: "select", selected: "Proxy", members: ["Proxy", "DIRECT"], delays: delays),
        ]
        let on = sc == .protected || sc == .captive || sc == .attention || sc == .paused || sc == .error
        return Status(version: "0.1.0-dev", running: sc != .error, tunOn: on && sc != .paused && sc != .error, wantTun: on, warnings: sc == .firstRun ? 0 : sc == .attention ? 2 : 0,
                      lastError: sc == .error ? "规则第 88 行引用了不存在的策略组 “流媒体2”：修改订阅或在本地规则中覆盖" : nil,
                      groups: groups, subscription: sub,
                      profiles: sc == .firstRun ? [] : [
                        Profile(id: "a", name: "我的订阅", source: sub.source, kind: "url", active: true, fetchedAt: sub.fetchedAt, info: info),
                        Profile(id: "b", name: "备用", source: "https://example.org/backup", kind: "url", active: false, fetchedAt: .now.addingTimeInterval(-86400 * 3)),
                      ],
                      network: NetworkStatus(primaryInterface: "Wi-Fi", captive: sc == .captive ? "captive" : "online", paused: sc == .paused ? "另一个 VPN 正在运行，Velum 已让出" : nil))
    }

    private static func connections(failed: Bool) -> [Connection] {
        func c(_ id: Int, _ host: String, _ ago: Double, _ idx: Int, _ rule: String, _ chain: [String], up: Int64, down: Int64, fb: Int, err: [String]? = nil) -> Connection {
            Connection(id: id, start: .now.addingTimeInterval(-ago - 2), end: .now.addingTimeInterval(-ago), network: "tcp", domain: host, destination: "\(host):443", ruleIndex: idx, rule: rule,
                       provenance: Source(kind: "subscription", file: "sub.yaml", line: 120 + idx, text: rule), chain: chain, upload: up, download: down, firstByteMs: fb, durationMs: 2000, errors: err)
        }
        var out = [
            c(1, "github.com", 1, 12, "DOMAIN-SUFFIX,github.com,Proxy", ["Proxy", "🇭🇰 香港 02"], up: 4200, down: 182_000, fb: 88),
            c(2, "api.apple.com", 3, 40, "DOMAIN-SUFFIX,apple.com,苹果服务", ["苹果服务", "DIRECT"], up: 900, down: 22_000, fb: 24),
            c(3, "ads.example.com", 5, 3, "RULE-SET,ads,REJECT", ["REJECT"], up: 0, down: 0, fb: 0),
            c(4, "www.youtube.com", 9, 21, "DOMAIN-SUFFIX,youtube.com,流媒体", ["流媒体", "🇸🇬 新加坡 01"], up: 88_000, down: 48_200_000, fb: 150),
            c(5, "chat.example.ai", 14, 30, "DOMAIN-SUFFIX,example.ai,AI", ["AI", "🇺🇸 美国 01"], up: 12_000, down: 340_000, fb: 230),
            c(6, "www.baidu.com", 21, 60, "GEOSITE,cn,DIRECT", ["DIRECT"], up: 1400, down: 91_000, fb: 18),
        ]
        if failed {
            out.insert(c(7, "cdn.foo.net", 2, 88, "MATCH,兜底", ["兜底", "Proxy", "🇺🇸 美国 03"], up: 300, down: 0, fb: 0, err: ["连接节点超时（5 秒）"]), at: 1)
        }
        return out
    }

    private func read<T>(_ f: () -> T) -> T { lock.lock(); defer { lock.unlock() }; return f() }

    func status() async throws -> Status { read { s } }
    func traffic() async throws -> Traffic { Traffic(upRate: Int64.random(in: 8_000...40_000), downRate: Int64.random(in: 300_000...1_900_000), active: Int.random(in: 11...16)) }
    func connections() async throws -> [Connection] {
        read {
            // 模拟持续有新连接（滚动稳定性的测试用）
            if UserDefaults.standard.bool(forKey: "liveConns"), let first = conns.first {
                var c = first
                nextID += 1
                c.id = nextID
                c.domain = "live\(nextID).example.com"
                c.start = .now
                c.end = .now
                conns.insert(c, at: 0)
            }
            return conns
        }
    }

    func warnings() async throws -> [Warning] {
        guard read({ s.warnings }) > 0 else { return [] }
        return [
            Warning(category: "规则", kind: "unsupported", level: "unsupported", reason: "iOS 不能识别连接来自哪个应用，这条规则不会生效", subject: "PROCESS-NAME,Telegram,Proxy", source: Source(kind: "subscription", file: "sub.yaml", line: 88)),
            Warning(category: "节点", kind: "degraded", level: "degraded", reason: "不支持 smux 的 brutal 选项，已按普通 smux 连接", subject: "🇺🇸 美国 02 洛杉矶高速", source: Source(kind: "subscription", file: "sub.yaml", line: 31)),
        ]
    }

    func setTunnel(on: Bool) async throws -> Status {
        let slow = UserDefaults.standard.bool(forKey: "slowConnect")
        try await Task.sleep(for: .milliseconds(on ? (slow ? 6000 : 1200) : 400))
        if on && scenario == .denied { read { s.wantTun = false }; throw CoreError.message("没有 VPN 权限：再次连接时，在系统弹窗中点“允许”") }
        return read {
            s.wantTun = on; s.tunOn = on
            if on && conns.isEmpty { conns = Self.connections(failed: false) }
            return s
        }
    }

    func select(group: String, member: String) async throws -> Status {
        try await Task.sleep(for: .milliseconds(120))
        return read {
            if let i = s.groups?.firstIndex(where: { $0.tag == group }) { s.groups?[i].selected = member }
            return s
        }
    }

    func test(group: String) async throws -> [String: Int] {
        try await Task.sleep(for: .milliseconds(1400))
        return Self.delays.mapValues { $0 < 0 ? $0 : max(10, $0 + Int.random(in: -12...12)) }
    }

    func why(target: String) async throws -> WhyResponse {
        try await Task.sleep(for: .milliseconds(180))
        let host = Fmt.cleanTarget(target)
        let isIP = Fmt.isIP(host)
        let table: [(String, Int, String, String, [String])] = [
            ("github.com", 12, "DOMAIN-SUFFIX,github.com,Proxy", "Proxy", read { Self.path(s, "Proxy") }),
            ("apple.com", 40, "DOMAIN-SUFFIX,apple.com,苹果服务", "苹果服务", read { Self.path(s, "苹果服务") }),
            ("youtube.com", 21, "DOMAIN-SUFFIX,youtube.com,流媒体", "流媒体", read { Self.path(s, "流媒体") }),
            ("example.com", 3, "RULE-SET,ads,REJECT", "REJECT", ["REJECT"]),
            ("baidu.com", 60, "GEOSITE,cn,DIRECT", "DIRECT", ["DIRECT"]),
        ]
        var decided = table.first { host == $0.0 || host.hasSuffix("." + $0.0) }
        var source = Source(kind: "subscription", file: "sub.yaml", line: nil, text: nil)
        let local = read { overlay.first { e in
            let v = e.value.trimmingCharacters(in: CharacterSet(charactersIn: "=."))
            return e.kind == "domain" && (host == v || host.hasSuffix("." + v))
        } }
        if let local {
            decided = (host, 0, "DOMAIN-SUFFIX,\(local.value),DIRECT", "DIRECT", ["DIRECT"])
            source = Source(kind: "overlay", file: "overlay.yaml", line: 4, text: "direct: " + local.value)
        }
        let d = decided ?? (host, 88, "MATCH,兜底", "兜底", read { Self.path(s, "兜底") })
        if local == nil { source.line = 120 + d.1; source.text = d.2 }
        let misses = stride(from: 0, to: d.1, by: max(1, d.1 / 8)).map { i in
            RuleTrace(index: i, action: "Proxy", matched: false, reason: "域名不匹配", source: Source(kind: "subscription", file: "sub.yaml", line: 120 + i, text: "DOMAIN-SUFFIX,site\(i).example,Proxy"))
        }
        let matched = RuleTrace(index: d.1, action: d.3, matched: true, reason: "命中", source: source)
        let report = WhyReport(target: .init(raw: target, domain: isIP ? nil : host, ip: isIP ? host : nil, port: 443),
                               dns: .init(applicable: !isIP, server: "223.5.5.5", answer: isIP ? nil : ["198.51.100.24"], source: Source(kind: "subscription", file: "sub.yaml", line: 9)),
                               rules: misses + [matched],
                               decision: .init(ruleIndex: d.1, action: d.3, outbound: d.4.last, source: source))
        return WhyResponse(report: report, chain: d.4, resolved: isIP ? nil : ["198.51.100.24"], resolvedBy: isIP ? nil : "223.5.5.5")
    }

    private static func path(_ s: Status, _ tag: String) -> [String] {
        var out = [tag]; var cur = tag
        for _ in 0..<8 {
            guard let sel = s.groups?.first(where: { $0.tag == cur })?.selected else { break }
            out.append(sel); cur = sel
        }
        return out
    }

    func addProfile(url: String) async throws -> Status {
        try await Task.sleep(for: .milliseconds(1500))
        guard url.hasPrefix("http") else { throw CoreError.message("这不是有效的订阅地址：应以 http:// 或 https:// 开头") }
        if url.contains("bad") { throw CoreError.message("订阅服务返回 403：确认地址没有过期，或联系提供订阅的人") }
        return read {
            let keep = s.subscription.source
            let p = Self.status(.protected)
            if keep.isEmpty { s = p; s.tunOn = false; s.wantTun = false; s.warnings = 2 }
            else { s.profiles?.append(Profile(id: UUID().uuidString, name: "新订阅", source: url, kind: "url", active: false, fetchedAt: .now)) }
            return s
        }
    }

    func activateProfile(id: String) async throws -> Status {
        try await Task.sleep(for: .milliseconds(500))
        return read {
            var list = s.profiles ?? []
            for i in list.indices { list[i].active = list[i].id == id }
            s.profiles = list
            return s
        }
    }

    func updateProfile(id: String) async throws -> Status {
        try await Task.sleep(for: .milliseconds(900))
        return read {
            if let i = s.profiles?.firstIndex(where: { $0.id == id }) { s.profiles?[i].fetchedAt = .now }
            if s.profiles?.first(where: { $0.id == id })?.active == true { s.subscription.fetchedAt = .now; s.subscription.fetchError = nil }
            return s
        }
    }

    func deleteProfile(id: String) async throws -> Status { read { s.profiles?.removeAll { $0.id == id }; return s } }
    func overlayItems() async throws -> [OverlayEntry] { read { overlay } }

    func editOverlay(add: Bool, entry: OverlayEntry) async throws -> Status {
        try await Task.sleep(for: .milliseconds(350))
        return read {
            if add { overlay.append(entry) } else { overlay.removeAll { $0 == entry } }
            return s
        }
    }

    func repair() async throws -> [RepairStep] {
        try await Task.sleep(for: .milliseconds(1600))
        return [RepairStep(name: "重新检测网络环境", ok: true, detail: "Wi-Fi，可访问互联网"),
                RepairStep(name: "重新拉取订阅", ok: true, detail: "10 个节点，412 条规则"),
                RepairStep(name: "重新加载内核", ok: true),
                RepairStep(name: "节点测速", ok: true, detail: "9 个可用，1 个超时", warn: true)]
    }
}
#endif
