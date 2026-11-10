// 控制隧道扩展：核心（Go 经 TunnelHost，Rust 经 VelumHost）回调这里。调用都发生在核心的后台线程上，可以阻塞，绝不会在主线程。
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
    /// 隧道被系统断开时通知核心；没有设置时通知 Go 核心。
    private var revoked: (() -> Void)?
    var onRevoked: (() -> Void)? {
        get { lock.withLock { revoked } }
        set { lock.withLock { revoked = newValue } }
    }

    private let monitor = NWPathMonitor()
    private var path: Network.NWPath?
    /// 第一次网络状态到达：启动时核心立刻会查询，不等就会被当成没有网络。
    private let firstPath = DispatchSemaphore(value: 0)
    private var gotPath = false

    override init() {
        super.init()
        monitor.pathUpdateHandler = { [weak self] p in
            guard let self else { return }
            let first = lock.withLock { () -> Bool in
                self.path = p
                defer { self.gotPath = true }
                return !self.gotPath
            }
            if first { firstPath.signal() }
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
            // 旧版本建立的隧道还在运行、配置里没有按需连接：补上，不用重新连接
            if let m = reload(), m.connection.status == .connected, !m.isOnDemandEnabled {
                let rule = NEOnDemandRuleConnect()
                rule.interfaceTypeMatch = .any
                m.onDemandRules = [rule]
                m.isOnDemandEnabled = true
                _ = save(m)
            }
            statusChanged()
        }
    }

    private func statusChanged() {
        let active = tunnelActive()
        // 隧道断了但按需连接还开着：是被系统结束，系统马上会拉起来，不算用户断开。
        // 配置必须仍是启用的：别的 VPN 被启用时系统会把本配置的 isEnabled 置为 false，
        // 按需连接标志虽然还在，却不会再生效，这时要当成被撤销，否则界面一直显示「已连接」。
        if !active, lock.withLock({ wasActive }), let m = reload(), m.isEnabled, m.isOnDemandEnabled { return }
        let notify = lock.withLock { () -> Bool in
            defer { wasActive = active }
            return wasActive && !active && !transitioning
        }
        guard notify else { return }
        if let f = onRevoked { f() } else { MobilebindTunRevoked() }
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
        // 按需连接：隧道被系统结束（如内存超限）后自动恢复。用户在系统设置里断开时，系统会自己关掉它
        let rule = NEOnDemandRuleConnect()
        rule.interfaceTypeMatch = .any
        m.onDemandRules = [rule]
        m.isOnDemandEnabled = true
        if let saveErr = save(m) { throw Self.describe(saveErr) }
        // 保存后必须重新加载，否则启动时报配置无效
        guard let fresh = reload() else { throw CoreError.message(String(localized: "没有 VPN 配置：再次连接时，在系统弹窗中点“允许”")) }
        return fresh
    }

    private func save(_ m: NETunnelProviderManager) -> Error? {
        let sem = DispatchSemaphore(value: 0)
        var out: Error?
        m.saveToPreferences { out = $0; sem.signal() }
        sem.wait()
        return out
    }

    private static func describe(_ e: Error) -> Error {
        let n = e as NSError
        if n.domain == NEVPNErrorDomain, n.code == NEVPNError.configurationReadWriteFailed.rawValue {
            return CoreError.message(String(localized: "没有 VPN 权限：再次连接时，在系统弹窗中点“允许”"))
        }
        return e
    }

    // MARK: MobilebindTunnelHost

    /// 扩展读取的共享目录（与 RealCore.home() 下的 tunnel 目录相同）。
    private static var velumPaths: VelumFiles.Paths? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group)
            .map { VelumFiles.Paths(dir: $0.appendingPathComponent("velum/tunnel")) }
    }

    /// 用 velum-engine 时，在 App 进程里把配置编译成路由文件（扩展只加载）。编译错误在这里报给用户。
    private func compileVelum() throws {
        guard let p = Self.velumPaths else { return }
        try VelumFiles.ensureCompiled(p)
    }

    func startTunnel() throws {
        lock.withLock { transitioning = true }
        defer { lock.withLock { transitioning = false; wasActive = true } }
        try compileVelum()
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
        // 先关掉按需连接，否则系统会立刻把隧道拉起来
        if m.isOnDemandEnabled {
            m.isOnDemandEnabled = false
            _ = save(m)
        }
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
        do {
            return String(decoding: try call(Data((req ?? "").utf8)), as: UTF8.self)
        } catch let e {
            error?.pointee = e as NSError
            return ""
        }
    }

    /// 发消息给隧道扩展（JSON 进、JSON 出）。
    func call(_ data: Data) throws -> Data {
        // 隧道没在运行时 sendProviderMessage 的回调可能永远不来，每次都要等满 25 秒；
        // 而核心在持读锁时会连续查询几百次（每个策略组、每个成员），切换开关就被一直卡住。
        guard tunnelActive(), let session = current()?.connection as? NETunnelProviderSession else {
            throw CoreError.message(String(localized: "隧道未运行"))
        }
        // 重新加载前先编译（velum-engine）：扩展收到 reload 时直接加载新的路由文件
        if let r = try? JSONSerialization.jsonObject(with: data) as? [String: Any], r["op"] as? String == "reload" {
            try compileVelum()
        }
        let sem = DispatchSemaphore(value: 0)
        var out: Data?
        try session.sendProviderMessage(data) { out = $0; sem.signal() }
        guard sem.wait(timeout: .now() + 25) == .success, let out else {
            throw CoreError.message(String(localized: "隧道没有响应"))
        }
        return out
    }

    // MARK: 本地引擎（velum-engine，没开隧道时）

    private let local = LocalEngine()

    func localStart() throws { try local.start() }

    func localStop() { local.stop() }

    func localCall(_ req: String?, error: NSErrorPointer) -> String {
        do {
            return try local.call(req ?? "")
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

    /// 当前的物理网络接口；没有网络时为 nil。
    func physicalInterface() -> NWInterface? {
        if !lock.withLock({ gotPath }), firstPath.wait(timeout: .now() + 1) == .success { firstPath.signal() }
        let p = lock.withLock { path }
        // 隧道开着时列表里也有隧道接口（类型 other），要跳过它才是物理网络
        guard let p, p.status == .satisfied else { return nil }
        return p.availableInterfaces.first(where: { $0.type != .other })
    }

    /// 物理网络的 DNS 服务器。隧道开着时系统解析器返回的是隧道的 DNS，这时用切换前记下的值。
    func physicalDNS(_ iface: NWInterface) -> [String] {
        var dns = Self.resolvers().filter { !$0.hasPrefix("172.19.") && !$0.hasPrefix("fdfe:dcba:") }
        let key = "dns." + iface.name
        if dns.isEmpty { dns = UserDefaults.standard.stringArray(forKey: key) ?? [] }
        else { UserDefaults.standard.set(dns, forKey: key) }
        return dns
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
