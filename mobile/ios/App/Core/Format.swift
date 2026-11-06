import Foundation

enum Fmt {
    static func rate(_ n: Int64) -> String {
        if n >= 1 << 20 { return String(format: "%.1f MB/s", Double(n) / 1048576) }
        if n >= 1 << 10 { return "\(n >> 10) KB/s" }
        return "\(n) B/s"
    }

    static func bytes(_ n: Int64) -> String {
        if n >= 1 << 30 { return String(format: "%.1f GB", Double(n) / 1073741824) }
        if n >= 1 << 20 { return String(format: "%.1f MB", Double(n) / 1048576) }
        if n >= 1 << 10 { return "\(n >> 10) KB" }
        return "\(n) B"
    }

    static func ago(_ d: Date, now: Date = .now) -> String {
        if d.timeIntervalSince1970 < 0 { return String(localized: "未知") } // Go 的零值时间
        let s = Int(now.timeIntervalSince(d))
        if s < 5 { return String(localized: "刚刚") }
        if s < 60 { return String(localized: "\(s) 秒前") }
        if s < 3600 { return String(localized: "\(s / 60) 分钟前") }
        if s < 86400 { return String(localized: "\(s / 3600) 小时前") }
        return String(localized: "\(s / 86400) 天前")
    }

    /// 把 URL、host:port 等输入整理为主机名或 IP（与桌面端 cleanTarget 相同）。
    static func cleanTarget(_ raw: String) -> String {
        var v = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if v.range(of: #"^[a-zA-Z]+://"#, options: .regularExpression) != nil, let h = URL(string: v)?.host() { v = h }
        v = v.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        if let r = v.range(of: #"^[^:/]+:\d+$"#, options: .regularExpression), r == v.startIndex..<v.endIndex, let c = v.firstIndex(of: ":") {
            v = String(v[..<c])
        }
        if v.hasSuffix(".") { v.removeLast() }
        return v.lowercased()
    }

    /// 看起来是地址（域名、IP、URL），而不是节点名。
    static func looksLikeAddress(_ raw: String) -> Bool {
        let v = cleanTarget(raw)
        if isIP(v) { return true }
        return v.range(of: #"^([a-z0-9-]+\.)+[a-z]{2,}$"#, options: .regularExpression) != nil
    }

    static func isIP(_ v: String) -> Bool {
        v.range(of: #"^\d{1,3}(\.\d{1,3}){3}$"#, options: .regularExpression) != nil
            || (v.contains(":") && v.range(of: #"^[0-9a-f:.]+$"#, options: .regularExpression) != nil)
    }

    /// 域名自身及各级父域（至少两级）。
    static func parents(_ host: String) -> [String] {
        let parts = host.split(separator: ".").map(String.init)
        guard parts.count >= 2 else { return [host] }
        return (0...(parts.count - 2)).map { parts[$0...].joined(separator: ".") }
    }
}
