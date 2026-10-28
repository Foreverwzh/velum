// 在系统扩展里运行完整的 Velum 核心（core/mobilebind，与 Android 相同的入口）。
// 核心经 Host.openTun 申请 TUN：这里设置隧道网络参数，再把系统建好的 utun 的 FD 交给核心。
import Foundation
import Mobilebind
import NetworkExtension
import SystemConfiguration
import os

let log = Logger(subsystem: "dev.velum.spike", category: "tunnel")

class PacketTunnelProvider: NEPacketTunnelProvider {
    private var timer: DispatchSourceTimer?

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
        startFootprintLog(home)
        diagnose()
        DispatchQueue.global().async {
            var err: NSError?
            MobilebindStart(home.path, Host(provider: self), &err)
            let ms = Int(Date().timeIntervalSince(started) * 1000)
            if let err {
                log.error("核心启动失败（\(ms)ms）：\(err, privacy: .public)")
            } else {
                log.notice("核心已就绪（\(ms)ms），\(footprint(), privacy: .public)")
            }
            completionHandler(err)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log.notice("停止隧道，原因 \(reason.rawValue)")
        timer?.cancel()
        DispatchQueue.global().async {
            MobilebindStop()
            log.notice("核心已停止")
            completionHandler()
        }
    }

    // 宿主程序把配置文件内容放在启动参数里传入，写到扩展自己的 App Group 容器（以 root 运行，与用户的容器不同）
    private func prepareHome(_ options: [String: NSObject]) throws -> URL {
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "H3H5ULV8KR.dev.velum.spike") else {
            throw NSError(domain: "spike", code: 1, userInfo: [NSLocalizedDescriptionKey: "取不到 App Group 容器"])
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

    private func startFootprintLog(_ home: URL) {
        let t = DispatchSource.makeTimerSource(queue: .global())
        t.schedule(deadline: .now() + 2, repeating: 5)
        let logPath = MobilebindLogPath(home.path)
        var offset: UInt64 = 0
        t.setEventHandler {
            log.notice("内存 \(footprint(), privacy: .public)")
            // 核心日志写在 root 的容器里，转到系统日志便于查看
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
        timer = t
    }

    // 诊断：沙盒里系统解析器、resolv.conf、当前网络是否可用
    private func diagnose() {
        var res: UnsafeMutablePointer<addrinfo>?
        let rc = getaddrinfo("www.apple.com", "443", nil, &res)
        if res != nil { freeaddrinfo(res) }
        let rcText = rc == 0 ? "ok" : String(cString: gai_strerror(rc))
        log.notice("诊断 getaddrinfo=\(rcText, privacy: .public)")
        let conf = (try? String(contentsOfFile: "/etc/resolv.conf", encoding: .utf8))?
            .split(separator: "\n").filter { $0.hasPrefix("nameserver") }.joined(separator: ",") ?? "读取失败"
        log.notice("诊断 resolv.conf=\(conf, privacy: .public)")
        log.notice("诊断 network=\(Host(provider: self).network(), privacy: .public)")
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

func footprint() -> String {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<integer_t>.size)
    let kr = withUnsafeMutablePointer(to: &info) {
        $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
    }
    guard kr == KERN_SUCCESS else { return "未知" }
    return String(format: "phys_footprint %.1fMB（峰值 %.1fMB）", Double(info.phys_footprint) / 1048576, Double(info.ledger_phys_footprint_peak) / 1048576)
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
            // 核心的全局接管是 0/1 + 128/1（不替换默认路由）；NE 下改为默认路由，隧道才成为主服务，
            // 系统随之把物理网卡的默认路由转为作用域路由，引擎绑定物理网卡的出站才可达
            let full4 = Set(routes4.map { "\($0.0)/\($0.1)" }).isSuperset(of: ["0.0.0.0/1", "128.0.0.0/1"])
            ip.includedRoutes = full4 ? [NEIPv4Route.default()]
                : routes4.map { NEIPv4Route(destinationAddress: $0.0, subnetMask: mask4($0.1)) }
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

    // 当前物理网络：主接口、网关与其 DHCP DNS
    func network() -> String {
        guard let store = SCDynamicStoreCreate(nil, "spike" as CFString, nil, nil),
              let g = SCDynamicStoreCopyValue(store, "State:/Network/Global/IPv4" as CFString) as? [String: Any],
              let iface = g["PrimaryInterface"] as? String else { return "" }
        var n: [String: Any] = ["iface": iface]
        if let gw = g["Router"] as? String { n["gateway"] = gw }
        if let svc = g["PrimaryService"] as? String,
           let d = SCDynamicStoreCopyValue(store, "State:/Network/Service/\(svc)/DNS" as CFString) as? [String: Any],
           let servers = d["ServerAddresses"] as? [String] {
            n["dns"] = servers
        }
        let js = (try? JSONSerialization.data(withJSONObject: n)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
        return js
    }

    private func err(_ s: String) -> NSError {
        NSError(domain: "spike", code: 1, userInfo: [NSLocalizedDescriptionKey: s])
    }
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
