// velum-engine 内核（tun.engine: velum）：配置由 App 编译成路由文件，这里 mmap 加载；App 消息按 tunnelproto 的 JSON
// 交给引擎处理（reload 在这里重启引擎）。与 sing-box 内核二选一，见 PacketTunnelProvider。
import Darwin
import Foundation
import NetworkExtension
import VelumEngine

final class VelumTunnel {
    /// 引擎的 fake-ip 段：TUN 地址是 .1，DNS 是 .2（与 IR 的 dns.fake_ip.inet4_range 一致）。
    private static let tunAddr4 = "198.18.0.1/32"
    private static let dns4 = "198.18.0.2"
    /// IPv6 照常进隧道（订阅关了 IPv6 时由规则在握手前拒绝 TCP，应用随即改用 IPv4），地址同 Velum 默认。
    private static let tunAddr6 = "fdfe:dcba:9876::1/126"

    private weak var provider: PacketTunnelProvider?
    private let paths: VelumFiles.Paths
    private let home: URL
    private let lock = NSLock()
    private var engine: OpaquePointer?
    private var watchdog: DispatchSourceTimer?
    /// 最近一次的网络（重新加载后要再报给新引擎）。
    private var net = ("", "", "")

    init(provider: PacketTunnelProvider, home: URL) {
        self.provider = provider
        self.home = home
        paths = VelumFiles.Paths(dir: home.appendingPathComponent("tunnel"))
    }

    /// 设置隧道参数并启动引擎。
    func start() throws {
        guard let provider else { throw tunnelError("没有 provider") }
        let routes = VelumFiles.meta(paths)?.routes ?? ["0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"]
        let req = TunRequest(mtu: 1500, inet4: [Self.tunAddr4], inet6: [Self.tunAddr6], routes: routes, dns: [Self.dns4])
        let sem = DispatchSemaphore(value: 0)
        var setErr: Error?
        provider.setTunnelNetworkSettings(tunnelSettings(req)) { setErr = $0; sem.signal() }
        sem.wait()
        if let setErr { throw setErr }
        try lock.withLock { try startLocked() }
        startWatchdog()
    }

    private func startLocked() throws {
        // App 平时已经编译好；App 升级后还没打开过等情况下这里兜底编译
        guard try VelumFiles.ensureCompiled(paths) else { throw tunnelError(String(localized: "配置不是 velum-engine 的")) }
        guard let provider, let fd = provider.tunFD() else { throw tunnelError(String(localized: "找不到 utun")) }
        let config = try Data(contentsOf: paths.ir)
        var msg = [CChar](repeating: 0, count: 512)
        let owned = dup(fd) // 引擎会关闭它拿到的 fd，系统持有的那个不受影响
        let h = config.withUnsafeBytes { c in
            velum_engine_start_compiled(c.bindMemory(to: UInt8.self).baseAddress, c.count, paths.router.path, paths.rules.path,
                                        paths.fakeIPCache.path, owned, &msg, msg.count)
        }
        guard let h else {
            close(owned)
            throw tunnelError(String(cString: msg))
        }
        engine = h
        if let m = try? Data(contentsOf: paths.ruleMeta) {
            _ = m.withUnsafeBytes { velum_engine_set_meta(h, $0.bindMemory(to: UInt8.self).baseAddress, $0.count) }
        }
        // 用户的选择：配置渲染之后的切换不在配置里（tunnelproto.SaveSelected）
        if let d = try? Data(contentsOf: paths.dir.appendingPathComponent("selected.json")),
           let sel = try? JSONSerialization.jsonObject(with: d) as? [String: String] {
            for (g, m) in sel {
                _ = callLocked(["op": "select", "group": g, "member": m])
            }
        }
        // 之后按当前网络执行 ssid-policy（同 sing-box 内核：手动选择在下一次换网前不会被改回）
        if !net.0.isEmpty || !net.2.isEmpty {
            velum_engine_set_network(h, net.0, net.1, net.2)
        }
        log.notice("velum-engine \(String(cString: velum_engine_version()), privacy: .public) 已启动")
    }

    private func stopLocked() {
        if let e = engine {
            velum_engine_stop(e)
            engine = nil
        }
    }

    func stop() {
        watchdog?.cancel()
        watchdog = nil
        lock.withLock { stopLocked() }
    }

    /// 网络变了：ssid-policy 与系统 DNS（按物理网卡查询）。
    func setNetwork(kind: String, ssid: String, ifname: String) {
        lock.withLock {
            net = (kind, ssid, ifname)
            if let e = engine { velum_engine_set_network(e, kind, ssid, ifname) }
        }
    }

    /// App 消息。reload：重新加载配置（重启引擎，连接会断开）；其余交给引擎。
    func handle(_ data: Data) -> Data {
        let op = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["op"] as? String
        return lock.withLock {
            if op == "reload" {
                stopLocked()
                do {
                    try startLocked()
                    return Data("{}".utf8)
                } catch {
                    return Self.errorJSON(error.localizedDescription)
                }
            }
            guard let e = engine else { return Self.errorJSON(String(localized: "内核未运行")) }
            return Self.call(e, data)
        }
    }

    private func callLocked(_ req: [String: String]) -> Data? {
        guard let e = engine, let d = try? JSONSerialization.data(withJSONObject: req) else { return nil }
        return Self.call(e, d)
    }

    private static func call(_ e: OpaquePointer, _ req: Data) -> Data {
        let p = req.withUnsafeBytes { velum_engine_call(e, $0.bindMemory(to: UInt8.self).baseAddress, $0.count) }
        guard let p else { return errorJSON("引擎没有回复") }
        defer { velum_string_free(p) }
        return Data(String(cString: p).utf8)
    }

    private static func errorJSON(_ s: String) -> Data {
        (try? JSONSerialization.data(withJSONObject: ["error": s])) ?? Data()
    }

    /// 协议栈意外退出时 VPN 仍显示已连接、却不再转发，整机断网：发现了就主动断开。
    private func startWatchdog() {
        let t = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        t.schedule(deadline: .now() + 5, repeating: .seconds(5), leeway: .seconds(2))
        t.setEventHandler { [weak self] in
            guard let self else { return }
            var s = VelumStats()
            let exited = lock.withLock { () -> Bool in
                guard let e = self.engine else { return false }
                return velum_engine_stats(e, &s) && s.stack_exited != 0
            }
            if exited {
                log.error("velum-engine 协议栈意外退出（errno \(s.last_errno, privacy: .public)），断开隧道")
                watchdog?.cancel()
                provider?.cancelTunnelWithError(tunnelError(String(localized: "内核协议栈意外退出")))
            }
        }
        t.resume()
        watchdog = t
    }
}
