// 在系统扩展里运行完整的 Velum 核心（core/mobilebind，与 Android 相同的入口）。
// 核心经 Host.openTun 申请 TUN：这里设置隧道网络参数，再把系统建好的 utun 的 FD 交给核心。
// 验证过程与各条约束的来历（F1～F10）见 spikes/mn-netext/README.md。
import Foundation
import Mobilebind
import NetworkExtension
import SystemConfiguration
import os

let log = Logger(subsystem: "dev.velum.desktop.tunnel", category: "tunnel")
let appGroup = "H3H5ULV8KR.dev.velum"

class PacketTunnelProvider: NEPacketTunnelProvider {
    private var logTimer: DispatchSourceTimer?

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let started = Date()
        let home: URL
        do {
            home = try prepareHome(options ?? [:])
        } catch {
            log.error("准备目录失败：\(error, privacy: .public)")
            return completionHandler(error)
        }
        log.notice("启动核心 \(MobilebindVersion(), privacy: .public)，目录 \(home.path, privacy: .public)")
        forwardCoreLog(home)
        DispatchQueue.global().async {
            var err: NSError?
            MobilebindStart(home.path, Host(provider: self), &err)
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
        logTimer?.cancel()
        DispatchQueue.global().async {
            MobilebindStop()
            log.notice("核心已停止")
            completionHandler()
        }
    }

    // 扩展以 root 运行、在沙盒里，读不到用户目录（F4、F10）：App 把配置文件内容放在启动参数里传入，写到扩展自己的 App Group 容器
    private func prepareHome(_ options: [String: NSObject]) throws -> URL {
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else {
            throw err("取不到 App Group 容器")
        }
        let home = group.appendingPathComponent("velum")
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        for (name, value) in options {
            let url = home.appendingPathComponent(name)
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
        let logPath = MobilebindLogPath(home.path)
        var offset: UInt64 = 0
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

final class Host: NSObject, MobilebindHostProtocol {
    weak var provider: PacketTunnelProvider?
    init(provider: PacketTunnelProvider) { self.provider = provider }

    func openTun(_ req: String?, ret0_: UnsafeMutablePointer<Int32>?) throws {
        guard let provider, let data = req?.data(using: .utf8) else { throw err("没有 provider") }
        let r = try JSONDecoder().decode(TunRequest.self, from: data)
        log.notice("openTun \(req ?? "", privacy: .public)")
        let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
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
        let started = Date()
        let sem = DispatchSemaphore(value: 0)
        var setErr: Error?
        provider.setTunnelNetworkSettings(s) { setErr = $0; sem.signal() }
        sem.wait()
        if let setErr { throw setErr }
        guard let fd = provider.tunFD() else { throw err("找不到 utun FD") }
        let ms = Int(Date().timeIntervalSince(started) * 1000)
        log.notice("隧道参数已设置（\(ms)ms），utun FD \(fd)")
        ret0_?.pointee = dup(fd) // 所有权交给核心：给一个副本，核心关闭时不影响系统持有的 FD
    }

    func closeTun() {
        log.notice("closeTun")
    }

    func findOwner(_ proto: Int32, srcAddr: String?, srcPort: Int32, dstAddr: String?, dstPort: Int32, error: NSErrorPointer) -> String {
        ""
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
