// Go 核心（对照用，启动参数 -VelumGoCore YES）：daemon 在 App 进程内运行（core/mobilebind），内核在隧道扩展里。
// 界面经本地接口访问，接口与桌面端相同。上架构建（VELUM_ONLY）不含 Go：只有 Rust 控制面（RustCore）。
#if !VELUM_ONLY
import Foundation
import Mobilebind
import VelumEngine

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
        let home = AppFiles.home()
        let host = tunnel
        tunnel.onRevoked = { MobilebindTunRevoked() }
        started = Task.detached(priority: .userInitiated) {
            try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
            // 核心返回的错误、告警跟随系统语言（iOS 切换语言时 App 会重启）
            MobilebindSetLocale(Locale.preferredLanguages.first ?? "")
            var err: NSError?
            MobilebindStartSplit(home.path, host, &err)
            if let err { throw err }
        }
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

/// Go 核心回调 TunnelController（MobilebindTunnelHost）：调用在 Go 的线程上，可以阻塞。
extension TunnelController: MobilebindTunnelHostProtocol {
    func tunnelCall(_ req: String?, error: NSErrorPointer) -> String {
        do {
            return String(decoding: try call(Data((req ?? "").utf8)), as: UTF8.self)
        } catch let e {
            error?.pointee = e as NSError
            return ""
        }
    }

    // App 内的 velum-engine 本地引擎（Go 核心没开隧道时用）
    func localStart() throws { try goLocalEngine.start() }

    func localStop() { goLocalEngine.stop() }

    func localCall(_ req: String?, error: NSErrorPointer) -> String {
        do {
            return try goLocalEngine.call(req ?? "")
        } catch let e {
            error?.pointee = e as NSError
            return ""
        }
    }

    /// 当前物理网络（Go 核心的格式）。
    func network() -> String {
        guard let iface = physicalInterface() else { return "" }
        let n: [String: Any] = ["iface": iface.name, "dns": physicalDNS(iface)]
        return (try? JSONSerialization.data(withJSONObject: n)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }
}

private let goLocalEngine = LocalEngine()
#endif
