// 隧道扩展：只运行内核（velum-engine，见 VelumTunnel），配置由 App 写到 App Group。内存上限约 50MB，见 spikes/ios-memory。
// 路由和 DNS 的写法来自 macOS 扩展（desktop/extension），各条约束的来历见 spikes/mn-netext/README.md。
import Darwin
import Foundation
import Network
import NetworkExtension
import os

let log = Logger(subsystem: "io.github.foreverwzh.velum.tunnel", category: "tunnel")
let appGroup = "group.io.github.foreverwzh.velum"

/// 系统判定内存超限用的指标。
/// 当前内存和进程启动以来的峰值。峰值由系统记录，采样之间的尖峰也算在内。
func footprintMiB() -> (current: Double, peak: Double) {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<integer_t>.size)
    let kr = withUnsafeMutablePointer(to: &info) {
        $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
            task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count)
        }
    }
    guard kr == KERN_SUCCESS else { return (-1, -1) }
    return (Double(info.phys_footprint) / 1048576, Double(info.ledger_phys_footprint_peak) / 1048576)
}

class PacketTunnelProvider: NEPacketTunnelProvider {
    var stopping = false
    private var velum: VelumTunnel?
    private var memTimer: DispatchSourceTimer?
    private var memPath: URL?
    private let memQueue = DispatchQueue(label: "velum.memory", qos: .utility)
    private var writtenPeak = 0.0
    private var writtenAt = Date.distantPast
    private let pathMonitor = NWPathMonitor()
    private let netQueue = DispatchQueue(label: "velum.net")
    /// 最近一次的网络（类型、Wi-Fi 名称、网卡）。引擎启动前就开始监听：启动时就交给引擎，
    /// 否则在第一次网络回调之前，引擎不知道物理网卡，按系统 DNS 的解析会失败（刚连上时的连接报错）。
    private let netLock = NSLock()
    private var lastNet: (String, String, String)?
    private let firstNet = DispatchSemaphore(value: 0)

    /// 按网络切换策略组（ssid-policy）、系统 DNS 按物理网卡查询。在扩展里做：换网时 App 通常不在前台。
    /// 在引擎启动之前开始，并等第一次结果（最多 1 秒）：这时隧道还没建立，报告的就是物理网络。
    private func watchNetwork() {
        pathMonitor.pathUpdateHandler = { [weak self] path in
            self?.reportNetwork(path, retries: 3)
        }
        pathMonitor.start(queue: netQueue)
        _ = firstNet.wait(timeout: .now() + 1)
    }

    private func reportNetwork(_ path: Network.NWPath, retries: Int) {
        guard path.status == .satisfied, let iface = path.availableInterfaces.first(where: { $0.type != .other }) else {
            report("", "", "")
            return
        }
        // 物理网卡名（en0、pdp_ip0）：velum-engine 按它查询系统 DNS
        let ifname = iface.name
        switch iface.type {
        case .cellular:
            report("cellular", "", ifname)
        case .wifi:
            // 连上 Wi-Fi 后名称可能要等一会儿才取得到；取不到时核心不会切换，避免把家里当成其他网络
            NEHotspotNetwork.fetchCurrent { [weak self] net in
                guard let self else { return }
                report("wifi", net?.ssid ?? "", ifname)
                if net?.ssid == nil, retries > 0 {
                    netQueue.asyncAfter(deadline: .now() + 2) { self.reportNetwork(self.pathMonitor.currentPath, retries: retries - 1) }
                }
            }
        default:
            report("", "", ifname)
        }
    }

    /// 交给核心，并记下来给设置页显示：切换不对时要能看出隧道认为自己在什么网络。
    private func report(_ kind: String, _ ssid: String, _ ifname: String) {
        let first = netLock.withLock { () -> Bool in
            defer { lastNet = (kind, ssid, ifname) }
            return lastNet == nil
        }
        velum?.setNetwork(kind: kind, ssid: ssid, ifname: ifname)
        if first { firstNet.signal() }
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else { return }
        try? "\(kind)\n\(ssid)\n".write(to: group.appendingPathComponent("velum/tunnel/network.txt"), atomically: true, encoding: .utf8)
    }

    override func wake() {
        log.notice("设备唤醒")
        velum?.setSleeping(false)
        memQueue.async { [weak self] in self?.startMemoryTimer() }
        netQueue.async { [weak self] in
            guard let self else { return }
            reportNetwork(pathMonitor.currentPath, retries: 3)
        }
    }

    override func sleep(completionHandler: @escaping () -> Void) {
        log.notice("设备休眠")
        memQueue.sync { stopMemoryTimer() }
        velum?.setSleeping(true)
        completionHandler()
    }

    /// 把内存读数写到共享目录（一行，覆盖写）：被系统杀掉后还能看到最后的峰值。
    /// 休眠时不采样；写文件比采样费电，只在峰值上涨或读数过旧时才写。
    private func startMemoryTimer() {
        guard memTimer == nil, memPath != nil else { return }
        let t = DispatchSource.makeTimerSource(queue: memQueue)
        t.schedule(deadline: .now() + 1, repeating: .seconds(10), leeway: .seconds(5))
        t.setEventHandler { [weak self] in self?.sampleMemory() }
        t.resume()
        memTimer = t
    }

    private func stopMemoryTimer() {
        memTimer?.cancel()
        memTimer = nil
    }

    private func sampleMemory() {
        guard let memPath else { return }
        let m = footprintMiB()
        guard m.peak >= writtenPeak + 1 || Date().timeIntervalSince(writtenAt) >= 60 else { return }
        let limit = m.current + Double(os_proc_available_memory()) / 1048576
        let line = String(format: "%.0f footprint=%.1f peak=%.1f limit=%.1f\n", Date().timeIntervalSince1970, m.current, m.peak, limit)
        try? line.write(to: memPath, atomically: true, encoding: .utf8)
        writtenPeak = m.peak
        writtenAt = Date()
    }

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else {
            return completionHandler(tunnelError(String(localized: "取不到 App Group")))
        }
        let home = group.appendingPathComponent("velum")
        DispatchQueue.global().async {
            self.watchNetwork()
            let err = self.startEngine(home)
            if let err { log.error("内核启动失败：\(err, privacy: .public)") }
            else {
                self.memQueue.sync {
                    self.memPath = home.appendingPathComponent("tunnel/memory.txt")
                    self.startMemoryTimer()
                }
                TunnelMetrics.shared.start(home: home)
            }
            completionHandler(err)
        }
    }

    /// 启动 velum-engine。App 写的配置不是它的（旧版本留下的 sing-box 配置）时报错：App 会重新生成。
    private func startEngine(_ home: URL) -> NSError? {
        guard VelumFiles.selected(VelumFiles.Paths(dir: home.appendingPathComponent("tunnel"))) else {
            return tunnelError(String(localized: "配置不是 velum-engine 的"))
        }
        let v = VelumTunnel(provider: self, home: home)
        // 已知的网络先交给它：引擎一启动就用（在恢复用户的选择之后执行 ssid-policy）
        if let n = netLock.withLock({ lastNet }) { v.setNetwork(kind: n.0, ssid: n.1, ifname: n.2) }
        do {
            try v.start()
            velum = v
            return nil
        } catch {
            return error as NSError
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log.notice("停止隧道，原因 \(reason.rawValue)")
        stopping = true
        memQueue.sync { stopMemoryTimer() }
        pathMonitor.cancel()
        DispatchQueue.global().async {
            self.velum?.stop()
            completionHandler()
        }
    }

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        DispatchQueue.global().async {
            if let v = self.velum {
                completionHandler?(v.handle(messageData))
            } else {
                completionHandler?(Data(#"{"error":"内核未运行"}"#.utf8))
            }
        }
    }

    /// 隧道的 utun FD：扩展进程里只有这一个 utun 控制套接字。
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

/// 隧道网络参数（velum-engine 经 VelumTunnel 请求）。
func tunnelSettings(_ r: TunRequest) -> NEPacketTunnelNetworkSettings {
    let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
    s.mtu = NSNumber(value: r.mtu)
    let routes4 = (r.routes ?? []).compactMap(cidr).filter { !$0.0.contains(":") }
    if let a = (r.inet4 ?? []).compactMap(cidr).first {
        let ip = NEIPv4Settings(addresses: [a.0], subnetMasks: [mask4(a.1)])
        // 全局接管是 0/1 + 128/1 扣除排除网段：改为默认路由，隧道才成为主服务（F6）。
        // 排除网段不写成 excludedRoutes（F8），由引擎规则直连。
        if covered4(uncovered4(routes4)) <= UInt64(1) << 31 {
            ip.includedRoutes = [NEIPv4Route.default()]
        } else {
            ip.includedRoutes = routes4.map { NEIPv4Route(destinationAddress: $0.0, subnetMask: mask4($0.1)) }
        }
        s.ipv4Settings = ip
    }
    let routes6 = (r.routes ?? []).compactMap(cidr).filter { $0.0.contains(":") }
    if let a = (r.inet6 ?? []).compactMap(cidr).first {
        let ip = NEIPv6Settings(addresses: [a.0], networkPrefixLengths: [NSNumber(value: a.1)])
        let full = Set(routes6.map { "\($0.0)/\($0.1)" }).isSuperset(of: ["::/1", "8000::/1"])
        ip.includedRoutes = full ? [NEIPv6Route.default()]
            : routes6.map { NEIPv6Route(destinationAddress: $0.0, networkPrefixLength: NSNumber(value: $0.1)) }
        s.ipv6Settings = ip
    }
    if let dns = r.dns, !dns.isEmpty {
        let d = NEDNSSettings(servers: dns)
        d.matchDomains = [""]
        s.dnsSettings = d
    }
    return s
}

func tunnelError(_ s: String) -> NSError {
    NSError(domain: "io.github.foreverwzh.velum.tunnel", code: 1, userInfo: [NSLocalizedDescriptionKey: s])
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

/// IPv4 网段的补集（相对 0.0.0.0/0）：没有被任何路由覆盖的网段。
func uncovered4(_ routes: [(String, Int)]) -> [(UInt32, Int)] {
    let rs: [(UInt32, Int)] = routes.compactMap { r in
        var a = in_addr()
        guard inet_pton(AF_INET, r.0, &a) == 1 else { return nil }
        return (UInt32(bigEndian: a.s_addr), r.1)
    }
    func mask(_ n: Int) -> UInt32 { n == 0 ? 0 : ~UInt32(0) << UInt32(32 - n) }
    func walk(_ base: UInt32, _ len: Int) -> [(UInt32, Int)] {
        if rs.contains(where: { $0.1 <= len && (base & mask($0.1)) == ($0.0 & mask($0.1)) }) { return [] }
        if !rs.contains(where: { $0.1 > len && ($0.0 & mask(len)) == base }) { return [(base, len)] }
        let half = UInt32(1) << UInt32(31 - len)
        return walk(base, len + 1) + walk(base | half, len + 1)
    }
    return walk(0, 0)
}

func covered4(_ ps: [(UInt32, Int)]) -> UInt64 { ps.reduce(0) { $0 + (UInt64(1) << UInt64(32 - $1.1)) } }
