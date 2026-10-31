// 验证用的宿主程序：命令行调用，不带界面。
//   VelumNESpike activate | deactivate      安装 / 卸载系统扩展
//   VelumNESpike start <run 目录> | stop | status | remove   启停隧道（run 目录由 prepare.sh 生成）
import Foundation
import NetworkExtension
import SystemExtensions

let extID = "dev.velum.spike.ne.tunnel"

func out(_ s: String) {
    let f = DateFormatter()
    f.dateFormat = "HH:mm:ss.SSS"
    print("[\(f.string(from: Date()))] \(s)")
    fflush(stdout)
}

func fail(_ s: String) -> Never {
    out("失败：\(s)")
    exit(1)
}

final class ExtDelegate: NSObject, OSSystemExtensionRequestDelegate {
    let started = Date()
    func request(_ r: OSSystemExtensionRequest, actionForReplacingExtension existing: OSSystemExtensionProperties,
                 withExtension ext: OSSystemExtensionProperties) -> OSSystemExtensionRequest.ReplacementAction {
        out("替换已安装的扩展 \(existing.bundleShortVersion)(\(existing.bundleVersion)) → \(ext.bundleShortVersion)(\(ext.bundleVersion))")
        return .replace
    }
    func requestNeedsUserApproval(_ r: OSSystemExtensionRequest) {
        out("需要批准：系统设置 → 通用 → 登录项与扩展 → 网络扩展")
    }
    func request(_ r: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        out("完成（\(result == .completed ? "已生效" : "需重启生效")），用时 \(String(format: "%.1f", Date().timeIntervalSince(started)))s")
        exit(0)
    }
    func request(_ r: OSSystemExtensionRequest, didFailWithError error: Error) {
        fail("\(error)")
    }
}

func loadManager(create: Bool, _ done: @escaping (NETunnelProviderManager?) -> Void) {
    NETunnelProviderManager.loadAllFromPreferences { list, err in
        if let err { fail("读取 VPN 配置：\(err)") }
        if let m = list?.first(where: { ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == extID }) {
            return done(m)
        }
        guard create else { return done(nil) }
        let m = NETunnelProviderManager()
        let p = NETunnelProviderProtocol()
        p.providerBundleIdentifier = extID
        p.serverAddress = "Velum NE Spike"
        m.protocolConfiguration = p
        m.localizedDescription = "Velum NE Spike"
        m.isEnabled = true
        m.saveToPreferences { err in
            if let err { fail("保存 VPN 配置：\(err)") }
            out("已添加 VPN 配置")
            m.loadFromPreferences { _ in done(m) }
        }
    }
}

func statusName(_ s: NEVPNStatus) -> String {
    switch s {
    case .invalid: return "invalid"
    case .disconnected: return "disconnected"
    case .connecting: return "connecting"
    case .connected: return "connected"
    case .reasserting: return "reasserting"
    case .disconnecting: return "disconnecting"
    @unknown default: return "unknown(\(s.rawValue))"
    }
}

// 等待连接状态稳定（connected / disconnected），打印每次变化
func watch(_ m: NETunnelProviderManager, until want: NEVPNStatus) {
    let started = Date()
    var last = m.connection.status
    out("状态 \(statusName(last))")
    let t = DispatchSource.makeTimerSource(queue: .main)
    t.schedule(deadline: .now(), repeating: 0.05)
    t.setEventHandler {
        let s = m.connection.status
        if s != last {
            last = s
            out("状态 \(statusName(s))（\(String(format: "%.2f", Date().timeIntervalSince(started)))s）")
        }
        if s == want { exit(0) }
        if s == .disconnected && want == .connected && Date().timeIntervalSince(started) > 1 {
            m.connection.fetchLastDisconnectError { err in fail("隧道断开：\(err.map { "\($0)" } ?? "无错误信息")") }
        }
        if Date().timeIntervalSince(started) > 60 { fail("等待超时") }
    }
    t.resume()
    timers.append(t)
}

var timers: [DispatchSourceTimer] = []

func readLine(_ fd: Int32) -> String {
    var buf = [UInt8](repeating: 0, count: 256)
    let n = read(fd, &buf, buf.count)
    close(fd)
    return n > 0 ? String(decoding: buf[0..<n], as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines) : "无应答"
}

func probeUnix(_ path: String) -> String {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    withUnsafeMutableBytes(of: &addr.sun_path) { buf in
        path.utf8CString.withUnsafeBytes { buf.copyMemory(from: UnsafeRawBufferPointer(rebasing: $0.prefix(buf.count - 1))) }
    }
    let rc = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) } }
    if rc != 0 { let e = errno; close(fd); return "连接失败 \(String(cString: strerror(e)))" }
    return readLine(fd)
}

func probeTCP(_ port: UInt16) -> String {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = port.bigEndian
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    let rc = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) } }
    if rc != 0 { let e = errno; close(fd); return "连接失败 \(String(cString: strerror(e)))" }
    return readLine(fd)
}

let args = CommandLine.arguments.dropFirst()
let delegate = ExtDelegate()

switch args.first {
case "activate", "deactivate":
    let r = args.first == "activate"
        ? OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: extID, queue: .main)
        : OSSystemExtensionRequest.deactivationRequest(forExtensionWithIdentifier: extID, queue: .main)
    r.delegate = delegate
    OSSystemExtensionManager.shared.submitRequest(r)
case "start":
    guard let dir = args.dropFirst().first else { fail("用法：start <run 目录>") }
    var opts: [String: NSObject] = [:]
    for name in ["config.yaml", "overlay.yaml", "profiles.yaml", "state.json"] {
        if let s = try? String(contentsOfFile: "\(dir)/\(name)", encoding: .utf8) { opts[name] = s as NSString }
    }
    // 订阅缓存逐个文件传入（扩展里的解析器可能不可用，先用缓存启动）
    if let e = FileManager.default.enumerator(atPath: "\(dir)/cache") {
        for case let rel as String in e {
            if let d = FileManager.default.contents(atPath: "\(dir)/cache/\(rel)"), !rel.hasSuffix(".db") {
                opts["cache/\(rel)"] = d as NSData
            }
        }
    }
    guard opts["config.yaml"] != nil else { fail("\(dir)/config.yaml 不存在，先运行 prepare.sh") }
    loadManager(create: true) { m in
        guard let m else { return }
        do { try m.connection.startVPNTunnel(options: opts) } catch { fail("启动隧道：\(error)") }
        watch(m, until: .connected)
    }
case "probe":
    // Q2：扩展在几个位置监听，这里以当前用户身份逐个连接
    guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "H3H5ULV8KR.dev.velum.spike") else { fail("取不到 App Group 容器") }
    try? FileManager.default.createDirectory(at: group, withIntermediateDirectories: true)
    try? "written by app".write(to: group.appendingPathComponent("probe-app.txt"), atomically: true, encoding: .utf8)
    out("用户容器 \(group.path)")
    let opts: [String: NSObject] = ["probe": group.path as NSString, "probeUID": NSNumber(value: getuid())]
    loadManager(create: true) { m in
        guard let m else { return }
        do { try m.connection.startVPNTunnel(options: opts) } catch { fail("启动隧道：\(error)") }
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) {
            for (name, path) in [("root-group", "/private/var/root/Library/Group Containers/H3H5ULV8KR.dev.velum.spike/probe.sock"),
                                 ("user-group", group.appendingPathComponent("probe.sock").path),
                                 ("var-run", "/private/var/run/velum-probe.sock"),
                                 ("tmp", "/private/tmp/velum-probe.sock")] {
                out("\(name)：\(probeUnix(path))")
            }
            out("tcp：\(probeTCP(7998))")
            let f = group.appendingPathComponent("probe-ext.txt").path
            out("扩展写入的文件：\((try? String(contentsOfFile: f, encoding: .utf8)) ?? "不存在")")
            m.connection.stopVPNTunnel()
            exit(0)
        }
    }
case "stop":
    loadManager(create: false) { m in
        guard let m else { fail("没有 VPN 配置") }
        m.connection.stopVPNTunnel()
        watch(m, until: .disconnected)
    }
case "status":
    loadManager(create: false) { m in
        out(m.map { "状态 \(statusName($0.connection.status))" } ?? "没有 VPN 配置")
        exit(0)
    }
case "remove":
    loadManager(create: false) { m in
        guard let m else { out("没有 VPN 配置"); exit(0) }
        m.removeFromPreferences { err in
            if let err { fail("\(err)") }
            out("已删除 VPN 配置")
            exit(0)
        }
    }
default:
    fail("用法：activate | deactivate | start <run 目录> | probe | stop | status | remove")
}
dispatchMain()
