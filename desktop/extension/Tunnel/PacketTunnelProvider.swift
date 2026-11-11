// 在系统扩展里运行完整的 Velum 核心：Go 版（core/mobilebind，与 Android 相同的入口），
// 或 Rust 版（velum-engine 的 velum-macext，编译条件 VELUM_RUST_CORE，见 build.sh 的 VELUM_CORE=rust）。
// 两者的宿主能力相同：核心经 Host.openTun 申请 TUN（这里设置隧道网络参数，再把系统建好的 utun 的 FD 交给核心），
// 经 Host.setProxy 设置系统代理，经 Host.network 取物理网络。
// 验证过程与各条约束的来历（F1～F10）见 spikes/mn-netext/README.md。
import Foundation
#if VELUM_RUST_CORE
import VelumMacExt
#else
import Mobilebind
#endif
import NetworkExtension
import SystemConfiguration
import os

let log = Logger(subsystem: "dev.velum.desktop.tunnel", category: "tunnel")
let appGroup = "H3H5ULV8KR.dev.velum"

class PacketTunnelProvider: NEPacketTunnelProvider {
    private var logTimer: DispatchSourceTimer?
    /// 隧道正在停止：系统会撤销全部隧道参数，核心关闭 TUN 时不必（也无法）再清空
    var stopping = false

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let started = Date()
        let home: URL
        do {
            home = try prepareHome(options ?? [:])
        } catch {
            log.error("准备目录失败：\(error, privacy: .public)")
            return completionHandler(error)
        }
        // 本地接口：App 指定的回环 TCP + 令牌（界面与 vlu 以用户身份连接，Q2）
        let socket = options?["socket"] as? String ?? ""
        log.notice("启动核心 \(Core.version(), privacy: .public)，目录 \(home.path, privacy: .public)")
        forwardCoreLog(home)
        DispatchQueue.global().async {
            let err = Core.start(home: home.path, socket: socket, host: Host(provider: self))
            let ms = Int(Date().timeIntervalSince(started) * 1000)
            if let err {
                log.error("核心启动失败（\(ms)ms）：\(err, privacy: .public)")
            } else {
                log.notice("核心已就绪（\(ms)ms）")
            }
            completionHandler(err)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log.notice("停止隧道，原因 \(reason.rawValue)")
        stopping = true
        logTimer?.cancel()
        DispatchQueue.global().async {
            Core.stop()
            log.notice("核心已停止")
            completionHandler()
        }
    }

    // 扩展以 root 运行、在沙盒里，读不到用户目录（F4、F10）：App 把用户目录里的配置文件内容放在启动参数（files/<相对路径>）里传入，
    // 只在扩展自己的 App Group 容器里还没有时写入（首次迁移）；之后以容器为准，界面经本地接口修改的设置不会被旧文件覆盖。
    private func prepareHome(_ options: [String: NSObject]) throws -> URL {
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else {
            throw err("取不到 App Group 容器")
        }
        let home = group.appendingPathComponent("velum")
        // reset：清空后重新迁移（用户无权访问 root 的容器，只能由扩展自己清理）
        if options["reset"] as? String == "1" {
            try? FileManager.default.removeItem(at: home)
            log.notice("已清空数据目录，重新迁移")
        }
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        for (key, value) in options where key.hasPrefix("files/") {
            let name = String(key.dropFirst("files/".count))
            guard !name.isEmpty, !name.split(separator: "/").contains("..") else { continue }
            let url = home.appendingPathComponent(name)
            if FileManager.default.fileExists(atPath: url.path) { continue }
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            if let s = value as? String {
                try s.write(to: url, atomically: true, encoding: .utf8)
            } else if let d = value as? Data {
                try d.write(to: url)
            }
        }
        return home
    }

    // 核心日志写在 root 的容器里，用户看不到：转到系统日志（subsystem dev.velum.desktop.tunnel）
    private func forwardCoreLog(_ home: URL) {
        let t = DispatchSource.makeTimerSource(queue: .global())
        t.schedule(deadline: .now() + 1, repeating: 2)
        let logPath = Core.logPath(home.path)
        // 从文件末尾开始：日志文件跨次保留，从头读会把历史日志重新转发一遍
        var offset: UInt64 = (try? FileManager.default.attributesOfItem(atPath: logPath)[.size] as? UInt64) ?? 0
        t.setEventHandler {
            guard let f = FileHandle(forReadingAtPath: logPath) else { return }
            defer { try? f.close() }
            let end = (try? f.seekToEnd()) ?? 0
            if end < offset { offset = 0 }
            try? f.seek(toOffset: offset)
            if let d = try? f.readToEnd(), let s = String(data: d, encoding: .utf8) {
                for line in s.split(separator: "\n") { log.notice("core: \(line, privacy: .public)") }
            }
            offset = end
        }
        t.resume()
        logTimer = t
    }

    // 找到隧道的 utun FD：扩展进程里只有这一个 utun 控制套接字
    func tunFD() -> Int32? {
        var buf = [CChar](repeating: 0, count: Int(IFNAMSIZ))
        for fd: Int32 in 0...1024 {
            var len = socklen_t(buf.count)
            if getsockopt(fd, 2 /* SYSPROTO_CONTROL */, 2 /* UTUN_OPT_IFNAME */, &buf, &len) == 0,
               String(cString: buf).hasPrefix("utun") {
                return fd
            }
        }
        return nil
    }
}

struct TunRequest: Decodable {
    var mtu: Int
    var inet4: [String]?
    var inet6: [String]?
    var routes: [String]?
    var dns: [String]?
}

struct ProxyRequest: Decodable {
    var host: String
    var port: Int
    var exceptions: [String]?
}

// 隧道参数由两部分组成：TUN（地址、路由、DNS，核心开启 TUN 时设置）与系统代理（NEProxySettings，核心接管系统代理时设置）。
// setTunnelNetworkSettings 整体替换，任一部分变化都按当前两部分重新下发；两部分都没有时清空（隧道保持连接，Q1）。
// 系统代理放在隧道参数里：隧道停止或扩展崩溃时系统一并撤销，不会残留指向 Velum 的系统代理（MN.5）。
final class Host: NSObject {
    weak var provider: PacketTunnelProvider?
    init(provider: PacketTunnelProvider) { self.provider = provider }

    private let lock = NSLock()
    private var tun: TunRequest?
    private var proxy: ProxyRequest?

    /// 按核心的请求设置隧道参数，返回 utun FD 的副本（所有权交给核心）。
    func openTun(_ req: String?) throws -> Int32 {
        guard let provider, let data = req?.data(using: .utf8) else { throw err("没有 provider") }
        let r = try JSONDecoder().decode(TunRequest.self, from: data)
        log.notice("openTun \(req ?? "", privacy: .public)")
        let started = Date()
        try update { $0.tun = r }
        guard let fd = provider.tunFD() else { throw err("找不到 utun FD") }
        let ms = Int(Date().timeIntervalSince(started) * 1000)
        log.notice("隧道参数已设置（\(ms)ms），utun FD \(fd)")
        return dup(fd) // 所有权交给核心：给一个副本，核心关闭时不影响系统持有的 FD
    }

    // 核心关闭 TUN（FD 已由核心关闭）：去掉 TUN 部分，系统随即撤销路由与 DNS；隧道正在停止时系统会撤销全部参数
    func closeTun() {
        guard let provider, !provider.stopping else { return }
        do {
            try update { $0.tun = nil }
            log.notice("closeTun：已撤销 TUN 参数")
        } catch {
            log.error("撤销 TUN 参数失败：\(error, privacy: .public)")
        }
    }

    // 核心接管 / 还原系统代理：req 为空表示清除
    func setProxy(_ req: String?) throws {
        guard let provider, !provider.stopping else { return }
        let p = try req.flatMap { $0.isEmpty ? nil : $0.data(using: .utf8) }.map { try JSONDecoder().decode(ProxyRequest.self, from: $0) }
        try update { $0.proxy = p }
        log.notice("系统代理：\(p.map { "\($0.host):\($0.port)，\($0.exceptions?.count ?? 0) 条豁免" } ?? "已清除", privacy: .public)")
    }

    private func update(_ change: (Host) -> Void) throws {
        guard let provider else { throw err("没有 provider") }
        lock.lock()
        defer { lock.unlock() }
        change(self)
        let s = settings()
        let sem = DispatchSemaphore(value: 0)
        var setErr: Error?
        provider.setTunnelNetworkSettings(s) { setErr = $0; sem.signal() }
        sem.wait()
        if let setErr { throw setErr }
    }

    private func settings() -> NEPacketTunnelNetworkSettings? {
        if tun == nil && proxy == nil { return nil }
        let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        if let r = tun {
            s.mtu = NSNumber(value: r.mtu)
            let v4 = (r.inet4 ?? []).compactMap(cidr)
            let routes4 = (r.routes ?? []).compactMap(cidr).filter { !$0.0.contains(":") }
            if let a = v4.first {
                let ip = NEIPv4Settings(addresses: [a.0], subnetMasks: [mask4(a.1)])
                // 核心的全局接管是 0/1 + 128/1 扣除排除网段；NE 下改为默认路由，隧道才成为主服务，
                // 系统随之把物理网卡的默认路由转为作用域路由，引擎绑定物理网卡的出站才可达（F6）。
                // 排除网段不写成 excludedRoutes：它们会变成经物理网卡的同前缀路由，把其他 VPN 的同名路由挤成作用域路由（F8）。
                if covered4(uncovered4(routes4)) <= UInt64(1) << 31 {
                    ip.includedRoutes = [NEIPv4Route.default()]
                } else {
                    ip.includedRoutes = routes4.map { NEIPv4Route(destinationAddress: $0.0, subnetMask: mask4($0.1)) }
                }
                s.ipv4Settings = ip
            }
            let v6 = (r.inet6 ?? []).compactMap(cidr)
            let routes6 = (r.routes ?? []).compactMap(cidr).filter { $0.0.contains(":") }
            if let a = v6.first {
                let ip = NEIPv6Settings(addresses: [a.0], networkPrefixLengths: [NSNumber(value: a.1)])
                let full6 = Set(routes6.map { "\($0.0)/\($0.1)" }).isSuperset(of: ["::/1", "8000::/1"])
                ip.includedRoutes = full6 ? [NEIPv6Route.default()]
                    : routes6.map { NEIPv6Route(destinationAddress: $0.0, networkPrefixLength: NSNumber(value: $0.1)) }
                s.ipv6Settings = ip
            }
            if let dns = r.dns, !dns.isEmpty {
                let d = NEDNSSettings(servers: dns)
                d.matchDomains = [""] // 接管所有域名
                s.dnsSettings = d
            }
        }
        if let p = proxy {
            let ps = NEProxySettings()
            ps.httpEnabled = true
            ps.httpServer = NEProxyServer(address: p.host, port: p.port)
            ps.httpsEnabled = true
            ps.httpsServer = NEProxyServer(address: p.host, port: p.port)
            ps.exceptionList = p.exceptions
            ps.excludeSimpleHostnames = true
            // 匹配所有域名的补充代理：不要求隧道是主服务。否则只有 TUN 开着（隧道为主服务）时系统才采用这里的代理，
            // 只开系统代理时不生效（测试机 macOS 13 实测；与 DNS 的 matchDomains 同理）
            ps.matchDomains = [""]
            s.proxySettings = ps
        }
        return s
    }

    // 当前物理网络：接口、网关与其 DNS。不读 State:/Network/Global/IPv4：有其他全局隧道（或我们自己全局接管）时那是隧道的 utun（F5），
    // 改为在各网络服务中找带网关的非隧道接口，主服务优先。核心在沙盒里取不到 DHCP DNS，用这里的 DNS 补上。
    func network() -> String {
        guard let store = SCDynamicStoreCreate(nil, "velum" as CFString, nil, nil) else { return "" }
        let primary = (SCDynamicStoreCopyValue(store, "State:/Network/Global/IPv4" as CFString) as? [String: Any])?["PrimaryService"] as? String
        let keys = (SCDynamicStoreCopyKeyList(store, "State:/Network/Service/[^/]+/IPv4" as CFString) as? [String]) ?? []
        let tunnel = ["utun", "ppp", "ipsec", "tun", "tap", "gif", "stf", "lo"]
        var found: (svc: String, iface: String, gw: String?)?
        for k in keys {
            guard let v = SCDynamicStoreCopyValue(store, k as CFString) as? [String: Any],
                  let iface = v["InterfaceName"] as? String, v["Router"] != nil,
                  !tunnel.contains(where: { iface.hasPrefix($0) }) else { continue }
            let svc = String(k.split(separator: "/")[3])
            if found == nil || svc == primary { found = (svc, iface, v["Router"] as? String) }
        }
        guard let found else { return "" }
        var n: [String: Any] = ["iface": found.iface]
        if let gw = found.gw { n["gateway"] = gw }
        if let d = SCDynamicStoreCopyValue(store, "State:/Network/Service/\(found.svc)/DNS" as CFString) as? [String: Any],
           let servers = d["ServerAddresses"] as? [String] {
            n["dns"] = servers
        }
        return (try? JSONSerialization.data(withJSONObject: n)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }
}

// 两种核心的入口。
enum Core {
    #if VELUM_RUST_CORE
    static func version() -> String { take(velum_core_version()) }
    static func logPath(_ home: String) -> String { take(velum_core_log_path(home)) }
    static func stop() { velum_core_stop() }

    /// 启动核心（本地接口就绪后返回）；失败时返回原因。Host 由核心一直持有（扩展进程只启动一次核心）。
    static func start(home: String, socket: String, host: Host) -> Error? {
        var h = VelumHost()
        h.ctx = Unmanaged.passRetained(host).toOpaque()
        h.open_tun = { ctx, req, errOut in
            let host = Unmanaged<Host>.fromOpaque(ctx!).takeUnretainedValue()
            do {
                return try host.openTun(req.map { String(cString: $0) })
            } catch {
                errOut?.pointee = strdup(error.localizedDescription)
                return -1
            }
        }
        h.close_tun = { ctx in
            Unmanaged<Host>.fromOpaque(ctx!).takeUnretainedValue().closeTun()
        }
        h.set_proxy = { ctx, req, errOut in
            let host = Unmanaged<Host>.fromOpaque(ctx!).takeUnretainedValue()
            do {
                try host.setProxy(req.map { String(cString: $0) })
                return 0
            } catch {
                errOut?.pointee = strdup(error.localizedDescription)
                return -1
            }
        }
        h.network = { ctx in
            let n = Unmanaged<Host>.fromOpaque(ctx!).takeUnretainedValue().network()
            return n.isEmpty ? nil : strdup(n)
        }
        // 语言：扩展以 root 运行，取扩展自己的首选语言；英文表随扩展打包
        let lang = Locale.preferredLanguages.first ?? "en"
        let tr = Bundle.main.path(forResource: "core-en", ofType: "json")
        guard let e = velum_core_start(home, socket, lang, tr, h) else { return nil }
        return err(take(e))
    }

    private static func take(_ p: UnsafeMutablePointer<CChar>?) -> String {
        guard let p else { return "" }
        defer { velum_free(p) }
        return String(cString: p)
    }
    #else
    static func version() -> String { MobilebindVersion() }
    static func logPath(_ home: String) -> String { MobilebindLogPath(home) }
    static func stop() { MobilebindStop() }

    static func start(home: String, socket: String, host: Host) -> Error? {
        var e: NSError?
        MobilebindSetSocket(socket)
        let gh = GoHost(host)
        MobilebindSetProxyHost(gh)
        MobilebindStart(home, gh, &e)
        return e
    }
    #endif
}

#if !VELUM_RUST_CORE
/// Go 核心的回调接口（gomobile 生成的协议）转给 Host。
final class GoHost: NSObject, MobilebindHostProtocol, MobilebindProxyHostProtocol {
    let host: Host
    init(_ host: Host) { self.host = host }
    func openTun(_ req: String?, ret0_: UnsafeMutablePointer<Int32>?) throws {
        ret0_?.pointee = try host.openTun(req)
    }
    func closeTun() { host.closeTun() }
    func setProxy(_ req: String?) throws { try host.setProxy(req) }
    func network() -> String { host.network() }
    func findOwner(_ proto: Int32, srcAddr: String?, srcPort: Int32, dstAddr: String?, dstPort: Int32, error: NSErrorPointer) -> String {
        ""
    }
}
#endif

func err(_ s: String) -> NSError {
    NSError(domain: "dev.velum.desktop.tunnel", code: 1, userInfo: [NSLocalizedDescriptionKey: s])
}

func cidr(_ s: String) -> (String, Int)? {
    let p = s.split(separator: "/")
    guard p.count == 2, let n = Int(p[1]) else { return nil }
    return (String(p[0]), n)
}

func mask4(_ n: Int) -> String {
    let m = n == 0 ? UInt32(0) : ~UInt32(0) << (32 - n)
    return [24, 16, 8, 0].map { String((m >> UInt32($0)) & 0xff) }.joined(separator: ".")
}

// IPv4 网段的补集（相对 0.0.0.0/0）：没有被任何路由覆盖的网段
func uncovered4(_ routes: [(String, Int)]) -> [(UInt32, Int)] {
    let rs: [(UInt32, Int)] = routes.compactMap { r in
        var a = in_addr()
        guard inet_pton(AF_INET, r.0, &a) == 1 else { return nil }
        return (UInt32(bigEndian: a.s_addr), r.1)
    }
    func mask(_ n: Int) -> UInt32 { n == 0 ? 0 : ~UInt32(0) << UInt32(32 - n) }
    func walk(_ base: UInt32, _ len: Int) -> [(UInt32, Int)] {
        if rs.contains(where: { $0.1 <= len && (base & mask($0.1)) == ($0.0 & mask($0.1)) }) { return [] } // 被覆盖
        if !rs.contains(where: { $0.1 > len && ($0.0 & mask(len)) == base }) { return [(base, len)] } // 完全未覆盖
        let half = UInt32(1) << UInt32(31 - len)
        return walk(base, len + 1) + walk(base | half, len + 1)
    }
    return walk(0, 0)
}

func covered4(_ ps: [(UInt32, Int)]) -> UInt64 { ps.reduce(0) { $0 + (UInt64(1) << UInt64(32 - $1.1)) } }
