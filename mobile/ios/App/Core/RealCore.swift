// 真实核心：daemon 在 App 进程内运行（core/mobilebind），内核在隧道扩展里。界面经本地接口访问，接口与桌面端相同。
import Foundation
import Mobilebind

final class RealCore: VelumCore, @unchecked Sendable {
    let tunnel = TunnelController()
    private let started: Task<Void, Error>

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            guard let date = parseDate(s) else {
                throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: String(localized: "时间格式无效：\(s)")))
            }
            return date
        }
        return d
    }()

    init() {
        let home = Self.home()
        let host = tunnel
        started = Task.detached(priority: .userInitiated) {
            try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
            // 核心返回的错误、告警跟随系统语言（iOS 切换语言时 App 会重启）
            MobilebindSetLocale(Locale.preferredLanguages.first ?? "")
            var err: NSError?
            MobilebindStartSplit(home.path, host, &err)
            if let err { throw err }
        }
    }

    /// 数据放在 App Group，扩展才读得到。模拟器未签名时取不到 App Group：改用 App 自己的目录（模拟器也运行不了隧道）。
    static func home() -> URL {
        let fm = FileManager.default
        let base = fm.containerURL(forSecurityApplicationGroupIdentifier: TunnelController.group)
            ?? fm.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("velum")
    }

    /// 把隧道的 MetricKit 报告复制到 App 自己的 Documents：调试时能从设备上取出来（App Group 取不到）。
    static func syncTunnelMetrics() {
        let fm = FileManager.default
        let dest = fm.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent("metrickit", isDirectory: true)
        try? fm.createDirectory(at: dest, withIntermediateDirectories: true)
        let src = Self.home().appendingPathComponent("tunnel/metrickit", isDirectory: true)
        let files = (try? fm.contentsOfDirectory(at: src, includingPropertiesForKeys: nil)) ?? []
        for f in files {
            let to = dest.appendingPathComponent(f.lastPathComponent)
            if !fm.fileExists(atPath: to.path) { try? fm.copyItem(at: f, to: to) }
        }
        // 隧道已清理的旧报告这里也不留
        let names = Set(files.map(\.lastPathComponent))
        for f in (try? fm.contentsOfDirectory(at: dest, includingPropertiesForKeys: nil)) ?? [] where !names.contains(f.lastPathComponent) {
            try? fm.removeItem(at: f)
        }
    }

    /// 把日志复制到 App 自己的 Documents/logs：App Group 里的文件 devicectl 取不到，Documents 可以
    /// （xcrun devicectl device copy from --domain-type appDataContainer --domain-identifier io.github.foreverwzh.velum --source Documents/logs）。
    /// 包括隧道扩展的内核日志（含轮转出的 .1）、App 内 daemon 的日志，以及隧道配置里的 dns 部分
    /// （查 DNS 规则用；不导出整份配置，里面有节点密码）。源文件不存在时删掉旧副本，免得拿到过期日志。
    static func syncLogs() {
        let fm = FileManager.default
        let dest = fm.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent("logs", isDirectory: true)
        try? fm.createDirectory(at: dest, withIntermediateDirectories: true)
        let home = Self.home()
        let tunnelLog = home.appendingPathComponent("tunnel/tunnel.log")
        let sources: [(URL, String)] = [
            (tunnelLog, "tunnel.log"),
            (URL(fileURLWithPath: tunnelLog.path + ".1"), "tunnel.log.1"),
            (URL(fileURLWithPath: MobilebindLogPath(home.path)), "app.log"),
            (home.appendingPathComponent("tunnel/velum.log"), "velum.log"),
        ]
        for (src, name) in sources {
            let to = dest.appendingPathComponent(name)
            try? fm.removeItem(at: to)
            if fm.fileExists(atPath: src.path) { try? fm.copyItem(at: src, to: to) }
        }
        let dnsOut = dest.appendingPathComponent("dns.json")
        try? fm.removeItem(at: dnsOut)
        if let data = try? Data(contentsOf: home.appendingPathComponent("tunnel/config.json")),
           let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let dns = root["dns"],
           let out = try? JSONSerialization.data(withJSONObject: dns, options: [.prettyPrinted, .sortedKeys]) {
            try? out.write(to: dnsOut)
        }
    }

    /// 隧道扩展的内存读数（扩展最多隔一分钟写一行，休眠时不写）：当前、峰值、系统上限，单位 MB。
    func tunnelMemory() -> (current: Double, peak: Double, limit: Double)? {
        guard let line = try? String(contentsOf: Self.home().appendingPathComponent("tunnel/memory.txt"), encoding: .utf8) else { return nil }
        var v: [String: Double] = [:]
        for part in line.split(whereSeparator: \.isWhitespace) {
            let kv = part.split(separator: "=")
            if kv.count == 2, let d = Double(kv[1]) { v[String(kv[0])] = d }
        }
        guard let c = v["footprint"], let p = v["peak"], let l = v["limit"] else { return nil }
        return (c, p, l)
    }

    /// 隧道扩展看到的网络：类型（wifi / cellular / 空）和 Wi-Fi 名称。
    func tunnelNetwork() -> (kind: String, ssid: String)? {
        guard let text = try? String(contentsOf: Self.home().appendingPathComponent("tunnel/network.txt"), encoding: .utf8) else { return nil }
        let lines = text.components(separatedBy: "\n")
        return (lines.first ?? "", lines.count > 1 ? lines[1] : "")
    }

    private func raw(_ method: String, _ path: String, _ body: [String: Any]?) async throws -> Data {
        try await started.value
        let data = try body.map { try JSONSerialization.data(withJSONObject: $0) }
        // Call 会阻塞到请求结束（切换开关可能等系统授权框）：不占用协作线程池
        return try await withCheckedThrowingContinuation { c in
            DispatchQueue.global(qos: .userInitiated).async {
                var err: NSError?
                let out = MobilebindCall(method, path, data, &err)
                if let err { c.resume(throwing: CoreError.message(err.localizedDescription)) }
                else { c.resume(returning: out ?? Data()) }
            }
        }
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        try Self.decoder.decode(T.self, from: await raw("GET", path, nil))
    }

    private func send<T: Decodable>(_ method: String, _ path: String, _ body: [String: Any]? = nil) async throws -> T {
        try Self.decoder.decode(T.self, from: await raw(method, path, body))
    }

    private struct ConnectionList: Decodable { var connections: [Connection]? }
    private struct WarningList: Decodable { var warnings: [Warning]? }
    private struct Delays: Decodable { var delays: [String: Int]? }
    private struct OverlayItems: Decodable { var entries: [OverlayEntry]? }
    private struct Steps: Decodable { var steps: [RepairStep]? }

    func status() async throws -> Status { try await get("/v1/status") }
    func setDomainFirst(_ on: Bool) async throws -> Status { try await send("PUT", "/v1/rules/domain-first", ["enabled": on]) }
    func setTunEngine(_ engine: String) async throws -> Status { try await send("PUT", "/v1/tun/engine", ["engine": engine]) }
    func traffic() async throws -> Traffic { try await get("/v1/traffic") }
    func connections() async throws -> [Connection] { (try await get("/v1/connections?limit=200") as ConnectionList).connections ?? [] }
    func warnings() async throws -> [Warning] { (try await get("/v1/warnings") as WarningList).warnings ?? [] }
    func setTunnel(on: Bool) async throws -> Status { try await send("POST", "/v1/switches", ["tun": on ? "on" : "off"]) }
    func select(group: String, member: String) async throws -> Status { try await send("POST", "/v1/groups/select", ["group": group, "member": member]) }
    func test(group: String) async throws -> [String: Int] { (try await send("POST", "/v1/groups/test", ["group": group]) as Delays).delays ?? [:] }
    func why(target: String) async throws -> WhyResponse { try await send("POST", "/v1/why", ["target": target]) }
    func addProfile(url: String) async throws -> Status { try await send("POST", "/v1/profiles", ["url": url, "activate": true]) }
    func activateProfile(id: String) async throws -> Status { try await send("POST", "/v1/profiles/\(id)/activate") }
    func updateProfile(id: String) async throws -> Status { try await send("POST", "/v1/profiles/\(id)/update") }
    func deleteProfile(id: String) async throws -> Status { try await send("DELETE", "/v1/profiles/\(id)") }
    func overlayItems() async throws -> [OverlayEntry] { (try await get("/v1/overlay/items") as OverlayItems).entries ?? [] }

    func editOverlay(add: Bool, entry: OverlayEntry) async throws -> Status {
        var e: [String: Any] = ["kind": entry.kind, "value": entry.value]
        if let ip = entry.ip { e["ip"] = ip }
        return try await send("POST", "/v1/overlay/items", ["op": add ? "add" : "remove", "entry": e])
    }

    func repair() async throws -> [RepairStep] { (try await send("POST", "/v1/repair") as Steps).steps ?? [] }
}

/// Go 的 RFC3339 时间可能带 9 位小数，ISO8601DateFormatter 不接受，去掉小数后再解析。
func parseDate(_ s: String) -> Date? {
    let trimmed = s.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)
    return ISO8601DateFormatter().date(from: trimmed)
}
