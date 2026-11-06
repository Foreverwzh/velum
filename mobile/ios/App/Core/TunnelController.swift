// 控制隧道扩展：Go 核心经 TunnelHost 回调这里。调用都发生在 Go 的线程上，可以阻塞，绝不会在主线程。
import Foundation
import Mobilebind
import Network
import NetworkExtension

final class TunnelController: NSObject, MobilebindTunnelHostProtocol, @unchecked Sendable {
    static let group = "group.io.github.foreverwzh.velum"
    static let tunnelBundleID = "io.github.foreverwzh.velum.tunnel"
    private static let startTimeout: TimeInterval = 30

    private let lock = NSLock()
    private var manager: NETunnelProviderManager?
    private var loaded = false
    /// 正在由核心主动启停：这期间的状态变化不是「被系统断开」。
    private var transitioning = false
    private var wasActive = false

    private let monitor = NWPathMonitor(prohibitedInterfaceTypes: [.other]) // 不看隧道自己
    private var path: Network.NWPath?

    override init() {
        super.init()
        monitor.pathUpdateHandler = { [weak self] p in
            guard let self else { return }
            lock.withLock { self.path = p }
        }
        monitor.start(queue: DispatchQueue(label: "velum.path"))
        // 通知在主线程发出，而 statusChanged 要等 NE 的回调（回调也在主线程）：必须转到后台且不等待。
        // 不能用 addObserver(queue:)：发送方会等观察者执行完，主线程照样被卡住。
        let queue = DispatchQueue(label: "velum.status")
        NotificationCenter.default.addObserver(forName: .NEVPNStatusDidChange, object: nil, queue: nil) { [weak self] _ in
            queue.async { self?.statusChanged() }
        }
    }

    /// App 回到前台时调用：在后台期间隧道可能已被用户或系统断开。
    func reconcile() {
        DispatchQueue.global().async { [self] in
            reload()
            statusChanged()
        }
    }

    private func statusChanged() {
        let active = tunnelActive()
        let notify = lock.withLock { () -> Bool in
            defer { wasActive = active }
            return wasActive && !active && !transitioning
        }
        if notify { MobilebindTunRevoked() }
    }

    // MARK: 配置

    @discardableResult private func reload() -> NETunnelProviderManager? {
        let sem = DispatchSemaphore(value: 0)
        var found: NETunnelProviderManager?
        NETunnelProviderManager.loadAllFromPreferences { list, _ in
            found = list?.first
            sem.signal()
        }
        sem.wait()
        lock.withLock { manager = found; loaded = true }
        return found
    }

    private func current() -> NETunnelProviderManager? {
        lock.withLock { loaded } ? lock.withLock { manager } : reload()
    }

    /// 第一次保存配置时系统会询问是否允许添加 VPN。
    private func prepared() throws -> NETunnelProviderManager {
        let m = current() ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = "Velum"
        m.protocolConfiguration = proto
        m.localizedDescription = "Velum"
        m.isEnabled = true
        let sem = DispatchSemaphore(value: 0)
        var saveErr: Error?
        m.saveToPreferences { saveErr = $0; sem.signal() }
        sem.wait()
        if let saveErr { throw Self.describe(saveErr) }
        // 保存后必须重新加载，否则启动时报配置无效
        guard let fresh = reload() else { throw CoreError.message(String(localized: "没有 VPN 配置：再次连接时，在系统弹窗中点“允许”")) }
        return fresh
    }

    private static func describe(_ e: Error) -> Error {
        let n = e as NSError
        if n.domain == NEVPNErrorDomain, n.code == NEVPNError.configurationReadWriteFailed.rawValue {
            return CoreError.message(String(localized: "没有 VPN 权限：再次连接时，在系统弹窗中点“允许”"))
        }
        return e
    }

    // MARK: MobilebindTunnelHost

    func startTunnel() throws {
        lock.withLock { transitioning = true }
        defer { lock.withLock { transitioning = false; wasActive = true } }
        let m = try prepared()
        do {
            try m.connection.startVPNTunnel()
        } catch {
            throw Self.describe(error)
        }
        let deadline = Date().addingTimeInterval(Self.startTimeout)
        var left = false
        while Date() < deadline {
            switch m.connection.status {
            case .connected: return
            case .connecting, .reasserting: left = true
            case .disconnected, .invalid:
                if left { throw lastError(m) ?? CoreError.message(String(localized: "隧道启动失败")) }
            default: break
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        m.connection.stopVPNTunnel()
        throw CoreError.message(String(localized: "等待隧道连接超时"))
    }

    private func lastError(_ m: NETunnelProviderManager) -> Error? {
        guard let session = m.connection as? NETunnelProviderSession else { return nil }
        let sem = DispatchSemaphore(value: 0)
        var out: Error?
        session.fetchLastDisconnectError { out = $0; sem.signal() }
        sem.wait()
        return out
    }

    func stopTunnel() {
        guard let m = current() else { return }
        lock.withLock { transitioning = true; wasActive = false }
        defer { lock.withLock { transitioning = false } }
        m.connection.stopVPNTunnel()
        let deadline = Date().addingTimeInterval(10)
        while Date() < deadline, m.connection.status != .disconnected, m.connection.status != .invalid {
            Thread.sleep(forTimeInterval: 0.05)
        }
    }

    func tunnelActive() -> Bool {
        switch current()?.connection.status {
        case .connected, .reasserting: true
        default: false
        }
    }

    func tunnelCall(_ req: String?, error: NSErrorPointer) -> String {
        guard let session = current()?.connection as? NETunnelProviderSession, let data = req?.data(using: .utf8) else {
            error?.pointee = CoreError.message(String(localized: "隧道未运行")) as NSError
            return ""
        }
        let sem = DispatchSemaphore(value: 0)
        var out: Data?
        do {
            try session.sendProviderMessage(data) { out = $0; sem.signal() }
        } catch let e {
            error?.pointee = e as NSError
            return ""
        }
        guard sem.wait(timeout: .now() + 25) == .success, let out, let s = String(data: out, encoding: .utf8) else {
            error?.pointee = CoreError.message(String(localized: "隧道没有响应")) as NSError
            return ""
        }
        return s
    }

    /// 当前物理网络。隧道开着时系统解析器返回的是隧道的 DNS，这时用切换前记下的值。
    func network() -> String {
        let p = lock.withLock { path }
        guard let p, p.status == .satisfied, let iface = p.availableInterfaces.first else { return "" }
        var dns = Self.resolvers().filter { !$0.hasPrefix("172.19.") && !$0.hasPrefix("fdfe:dcba:") }
        let key = "dns." + iface.name
        if dns.isEmpty { dns = UserDefaults.standard.stringArray(forKey: key) ?? [] }
        else { UserDefaults.standard.set(dns, forKey: key) }
        let n: [String: Any] = ["iface": iface.name, "dns": dns]
        return (try? JSONSerialization.data(withJSONObject: n)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }

    private static func resolvers() -> [String] {
        let size = MemoryLayout<__res_9_state>.size
        let raw = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: MemoryLayout<__res_9_state>.alignment)
        defer { raw.deallocate() }
        raw.initializeMemory(as: UInt8.self, repeating: 0, count: size) // res_ninit 要求结构体清零
        let st = raw.assumingMemoryBound(to: __res_9_state.self)
        guard res_9_ninit(st) == 0 else { return [] }
        defer { res_9_ndestroy(st) }
        var addrs = [res_9_sockaddr_union](repeating: res_9_sockaddr_union(), count: 8)
        let n = Int(res_9_getservers(st, &addrs, 8))
        var out: [String] = []
        for i in 0..<max(0, n) {
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            let rc = withUnsafePointer(to: &addrs[i]) { p in
                p.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                    getnameinfo(sa, socklen_t(sa.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST)
                }
            }
            if rc == 0 { out.append(String(cString: host)) }
        }
        return out
    }
}
