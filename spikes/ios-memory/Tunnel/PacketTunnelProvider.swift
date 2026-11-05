// 隧道扩展：只运行内核，每秒把内存读数追加到 App Group 的日志。
// footprint 是系统判定超限用的指标；footprint + available 就是系统此刻给这个进程的上限。
// 被系统杀掉时日志停在最后一秒，App 那边能看到。
import Darwin
import Foundation
import Iosprobe
import NetworkExtension
import os

let group = "group.dev.velum.spike.ios"
let log = Logger(subsystem: "dev.velum.spike.ios.tunnel", category: "tunnel")

func footprintMiB() -> Double {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<integer_t>.size)
    let kr = withUnsafeMutablePointer(to: &info) {
        $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
            task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count)
        }
    }
    return kr == KERN_SUCCESS ? Double(info.phys_footprint) / 1048576 : -1
}

class PacketTunnelProvider: NEPacketTunnelProvider {
    private var timer: DispatchSourceTimer?
    private var logFile: FileHandle?
    private var peak = 0.0

    private func write(_ s: String) {
        guard let d = (s + "\n").data(using: .utf8) else { return }
        try? logFile?.write(contentsOf: d)
    }

    private func sample(_ tag: String = "") {
        let f = footprintMiB()
        peak = max(peak, f)
        let avail = Double(os_proc_available_memory()) / 1048576
        write(String(format: "%.1f footprint=%.1f available=%.1f limit=%.1f peak=%.1f %@ %@",
                     Date().timeIntervalSince1970, f, avail, f + avail, peak, IosprobeStats(), tag))
    }

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        guard let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
            return completionHandler(NSError(domain: "spike", code: 1, userInfo: [NSLocalizedDescriptionKey: "取不到 App Group"]))
        }
        let path = dir.appendingPathComponent("mem.log")
        FileManager.default.createFile(atPath: path.path, contents: nil)
        logFile = try? FileHandle(forWritingTo: path)
        sample("扩展启动")

        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        let v4 = NEIPv4Settings(addresses: ["172.19.0.1"], subnetMasks: ["255.255.255.252"])
        v4.includedRoutes = [NEIPv4Route.default()]
        settings.ipv4Settings = v4
        let v6 = NEIPv6Settings(addresses: ["fdfe:dcba:9876::1"], networkPrefixLengths: [126])
        v6.includedRoutes = [NEIPv6Route.default()]
        settings.ipv6Settings = v6
        let dns = NEDNSSettings(servers: ["172.19.0.2"])
        dns.matchDomains = [""]
        settings.dnsSettings = dns
        settings.mtu = 9000

        setTunnelNetworkSettings(settings) { [self] error in
            if let error { write("设置隧道失败：\(error)"); return completionHandler(error) }
            guard let fd = tunFD() else {
                write("找不到 utun")
                return completionHandler(NSError(domain: "spike", code: 2, userInfo: [NSLocalizedDescriptionKey: "找不到 utun"]))
            }
            sample("隧道就绪")
            let gogc = (options?["gogc"] as? NSNumber)?.intValue ?? 0
            let limit = (options?["limit"] as? NSNumber)?.intValue ?? 0
            write("参数 gogc=\(gogc) limit=\(limit) fd=\(fd)")
            let t = DispatchSource.makeTimerSource(queue: .global())
            t.schedule(deadline: .now() + 1, repeating: 1)
            t.setEventHandler { [weak self] in self?.sample() }
            t.resume()
            timer = t
            DispatchQueue.global().async { [self] in
                var err: NSError?
                IosprobeStart(dir.appendingPathComponent("cfg.json").path, dir.appendingPathComponent("cache.db").path,
                              Int(fd), gogc, limit, &err)
                write("内核启动：\(err?.localizedDescription ?? "成功")")
                sample("内核就绪")
                completionHandler(err)
            }
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        write("停止，原因 \(reason.rawValue)")
        timer?.cancel()
        IosprobeStop()
        sample("已停止")
        completionHandler()
    }

    private func tunFD() -> Int32? {
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
