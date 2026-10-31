// Q2 验证：扩展（root、沙盒）能在哪里监听，让用户侧的界面与 vlu 连上。
// 不设置隧道参数（不影响路由与 DNS），在几个候选位置各开一个 UDS / 回环 TCP，应答一行后关闭；30 秒后以错误结束启动。
import Foundation
import os

enum Probe {
    static var fds: [Int32] = []

    static func run(userDir: String, uid: UInt32, _ done: @escaping (Error?) -> Void) {
        let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "H3H5ULV8KR.dev.velum.spike")?.path ?? ""
        log.notice("probe 开始：uid=\(getuid()) 用户目录 \(userDir, privacy: .public) root 容器 \(group, privacy: .public)")
        let candidates = [
            ("root-group", "\(group)/probe.sock"),
            ("user-group", "\(userDir)/probe.sock"),
            ("var-run", "/private/var/run/velum-probe.sock"),
            ("tmp", "/private/tmp/velum-probe.sock"),
        ]
        for (name, path) in candidates { unixListen(name, path, uid) }
        tcpListen("tcp", 7998)
        // 扩展能否读写用户容器里的文件（App 写配置、扩展读）
        let f = "\(userDir)/probe-ext.txt"
        do {
            try "written by extension".write(toFile: f, atomically: false, encoding: .utf8)
            log.notice("probe file-write ok \(f, privacy: .public)")
        } catch {
            log.error("probe file-write 失败 \(error, privacy: .public)")
        }
        let r = (try? String(contentsOfFile: "\(userDir)/probe-app.txt", encoding: .utf8)) ?? "读取失败 errno=\(errno)"
        log.notice("probe file-read \(r, privacy: .public)")
        DispatchQueue.global().asyncAfter(deadline: .now() + 30) {
            fds.forEach { close($0) }
            for (_, path) in candidates { unlink(path) }
            done(NSError(domain: "spike", code: 2, userInfo: [NSLocalizedDescriptionKey: "probe 结束"]))
        }
    }

    static func unixListen(_ name: String, _ path: String, _ uid: UInt32) {
        unlink(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { buf in
            path.utf8CString.withUnsafeBytes { buf.copyMemory(from: UnsafeRawBufferPointer(rebasing: $0.prefix(buf.count - 1))) }
        }
        let rc = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) } }
        guard rc == 0 else {
            log.error("probe \(name, privacy: .public) bind 失败 errno=\(errno) \(String(cString: strerror(errno)), privacy: .public)")
            close(fd)
            return
        }
        let chm = chmod(path, 0o666)
        let cho = chown(path, uid, 20)
        listen(fd, 8)
        log.notice("probe \(name, privacy: .public) 监听 \(path, privacy: .public) chmod=\(chm) chown=\(cho)")
        serve(name, fd)
    }

    static func tcpListen(_ name: String, _ port: UInt16) {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, socklen_t(MemoryLayout<Int32>.size))
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let rc = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) } }
        guard rc == 0 else {
            log.error("probe \(name, privacy: .public) bind 失败 errno=\(errno)")
            close(fd)
            return
        }
        listen(fd, 8)
        log.notice("probe \(name, privacy: .public) 监听 127.0.0.1:\(port)")
        serve(name, fd)
    }

    // 应答一行：位置名与对端 uid（UDS 用 getpeereid，供鉴权设计参考）
    static func serve(_ name: String, _ fd: Int32) {
        fds.append(fd)
        DispatchQueue.global().async {
            while true {
                let c = accept(fd, nil, nil)
                if c < 0 { return }
                var peer: uid_t = 0, gid: gid_t = 0
                let p = getpeereid(c, &peer, &gid) == 0 ? "\(peer)" : "?"
                let s = "\(name) ok peer-uid=\(p)\n"
                _ = s.withCString { write(c, $0, strlen($0)) }
                close(c)
            }
        }
    }
}
