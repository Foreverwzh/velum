// 核心本地接口的数据类型，与 core/pkg/rpc 保持一致（JSON 字段为蛇形命名）。
import Foundation

struct Source: Codable, Hashable {
    var kind: String
    var file: String?
    var line: Int?
    var text: String?

    /// `sub.yaml:120`
    var location: String {
        var s = file.map { ($0 as NSString).lastPathComponent } ?? kind
        if let line { s += ":\(line)" }
        return s
    }
}

/// 按网络自动切换的策略（订阅的 ssid-policy）。
struct NetPolicy: Codable, Hashable {
    var ssid: [String: String]?
    var cellular: String?
    var `default`: String?
}

struct GroupStatus: Codable, Hashable, Identifiable {
    var tag: String
    var type: String
    var selected: String?
    var members: [String]
    var delays: [String: Int]?
    var policy: NetPolicy?
    var id: String { tag }
    var isSelectable: Bool { type == "select" }
}

struct NetworkStatus: Codable, Hashable {
    var primaryInterface: String?
    var captive: String?
    var paused: String?
}

struct SubInfo: Codable, Hashable {
    var upload: Int64
    var download: Int64
    var total: Int64
    var expire: Date?
}

struct Profile: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var source: String
    var kind: String
    var active: Bool
    var fetchedAt: Date?
    var info: SubInfo?
}

struct SubscriptionStatus: Codable, Hashable {
    var name: String?
    var source: String
    var fetchedAt: Date?
    var fetchError: String?
    var nodes: Int
    var groups: Int
    var rules: Int
    var ruleSets: Int
    var info: SubInfo?
}

struct Status: Codable, Hashable {
    var version: String
    var running: Bool
    var tunOn: Bool
    var wantTun: Bool
    var starting: Bool?
    var warnings: Int
    var lastError: String?
    var groups: [GroupStatus]?
    var subscription: SubscriptionStatus
    var profiles: [Profile]?
    var network: NetworkStatus?
}

struct Traffic: Codable, Hashable {
    var upRate: Int64
    var downRate: Int64
    var active: Int
}

struct Connection: Codable, Hashable, Identifiable {
    var id: Int
    var start: Date
    var end: Date?
    var network: String
    var domain: String?
    var destination: String
    var ruleIndex: Int
    var rule: String
    var provenance: Source
    var chain: [String]
    var upload: Int64
    var download: Int64
    var firstByteMs: Int
    var durationMs: Int
    var errors: [String]?

    var host: String { domain ?? destination }
    /// 按规则拦截是预期行为（如订阅拒绝 IPv6），即使带错误信息也不算失败。
    var blocked: Bool { chain.last.map { $0 == "REJECT" || $0.hasPrefix("reject") } ?? false }
    var failed: Bool { !(errors ?? []).isEmpty && !blocked }
}

extension Connection {
    // 内核可能给出 null 的出站链（连接还没选定出站）：解码为空数组，界面不用到处判断。
    init(from decoder: Decoder) throws {
        enum K: String, CodingKey {
            case id, start, end, network, domain, destination, ruleIndex, rule, provenance, chain, upload, download, firstByteMs, durationMs, errors
        }
        let c = try decoder.container(keyedBy: K.self)
        self.init(id: try c.decode(Int.self, forKey: .id), start: try c.decode(Date.self, forKey: .start),
                  end: try c.decodeIfPresent(Date.self, forKey: .end), network: try c.decode(String.self, forKey: .network),
                  domain: try c.decodeIfPresent(String.self, forKey: .domain), destination: try c.decode(String.self, forKey: .destination),
                  ruleIndex: try c.decode(Int.self, forKey: .ruleIndex), rule: try c.decode(String.self, forKey: .rule),
                  provenance: try c.decode(Source.self, forKey: .provenance), chain: try c.decodeIfPresent([String].self, forKey: .chain) ?? [],
                  upload: try c.decode(Int64.self, forKey: .upload), download: try c.decode(Int64.self, forKey: .download),
                  firstByteMs: try c.decode(Int.self, forKey: .firstByteMs), durationMs: try c.decode(Int.self, forKey: .durationMs),
                  errors: try c.decodeIfPresent([String].self, forKey: .errors))
    }
}

struct RuleTrace: Codable, Hashable, Identifiable {
    var index: Int
    var action: String
    var matched: Bool
    var unknown: Bool?
    var reason: String
    var source: Source
    var id: Int { index }
}

struct WhyReport: Codable, Hashable {
    struct Target: Codable, Hashable { var raw: String; var domain: String?; var ip: String?; var port: Int }
    struct DNS: Codable, Hashable { var applicable: Bool; var server: String?; var answer: [String]?; var source: Source? }
    struct Decision: Codable, Hashable {
        var ruleIndex: Int
        var action: String
        var outbound: String?
        var source: Source
        var uncertain: Bool?
    }
    var target: Target
    var dns: DNS
    var rules: [RuleTrace]?
    var decision: Decision
    var notes: [String]?
}

struct WhyResponse: Codable, Hashable {
    var report: WhyReport
    var chain: [String]?
    var resolved: [String]?
    var resolvedBy: String?
    var resolveError: String?
}

struct Warning: Codable, Hashable, Identifiable {
    var category: String
    var kind: String
    var level: String
    var reason: String
    var subject: String?
    var source: Source
    var id: String { category + kind + reason + (subject ?? "") + source.location }
}

/// value 用 overlay 写法：x = 根域 + 子域，.x = 仅子域，=x = 仅此域名。iOS 上只有 domain、cidr、host。
struct OverlayEntry: Codable, Hashable, Identifiable {
    var kind: String
    var value: String
    var ip: String?
    var id: String { kind + value + (ip ?? "") }
}

struct RepairStep: Codable, Hashable, Identifiable {
    var name: String
    var ok: Bool
    var detail: String?
    var warn: Bool?
    var id: String { name }
}

enum CoreError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case let .message(m) = self { m } else { nil } }
}

/// 界面访问核心的全部入口。真实实现经隧道扩展和 App 内的编译器提供；Debug 下可用演示实现。
protocol VelumCore: AnyObject, Sendable {
    func status() async throws -> Status
    func traffic() async throws -> Traffic
    func connections() async throws -> [Connection]
    func warnings() async throws -> [Warning]
    func setTunnel(on: Bool) async throws -> Status
    func select(group: String, member: String) async throws -> Status
    func test(group: String) async throws -> [String: Int]
    func why(target: String) async throws -> WhyResponse
    func addProfile(url: String) async throws -> Status
    func activateProfile(id: String) async throws -> Status
    func updateProfile(id: String) async throws -> Status
    func deleteProfile(id: String) async throws -> Status
    func overlayItems() async throws -> [OverlayEntry]
    func editOverlay(add: Bool, entry: OverlayEntry) async throws -> Status
    func repair() async throws -> [RepairStep]
}
