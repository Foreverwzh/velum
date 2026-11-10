// App 与隧道扩展共用的文件（App Group 里的 velum 目录），与用哪个核心无关。
import Foundation

enum AppFiles {
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
    static func tunnelMemory() -> (current: Double, peak: Double, limit: Double)? {
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
    static func tunnelNetwork() -> (kind: String, ssid: String)? {
        guard let text = try? String(contentsOf: Self.home().appendingPathComponent("tunnel/network.txt"), encoding: .utf8) else { return nil }
        let lines = text.components(separatedBy: "\n")
        return (lines.first ?? "", lines.count > 1 ? lines[1] : "")
    }
}
