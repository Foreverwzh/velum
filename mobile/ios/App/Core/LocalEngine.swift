// App 内的 velum-engine 本地引擎（没开隧道时测速、解析、切换节点、经代理拉取）：Go 核心经 TunnelHost 的
// LocalStart / LocalStop / LocalCall 调这里。配置在 home/local（tunnelproto.LocalDir），先编译再以本地模式启动。
import Foundation
import VelumEngine

final class LocalEngine: @unchecked Sendable {
    private let lock = NSLock()
    private var engine: OpaquePointer?

    private static var paths: VelumFiles.Paths? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: TunnelController.group)
            .map { VelumFiles.Paths(dir: $0.appendingPathComponent("velum/local")) }
    }

    /// （重新）启动：编译配置，以本地模式启动引擎。
    func start() throws {
        lock.lock()
        defer { lock.unlock() }
        try startLocked()
    }

    private func startLocked(retried: Bool = false) throws {
        guard let p = Self.paths else { throw CoreError.message(String(localized: "取不到 App Group")) }
        stopLocked()
        guard try VelumFiles.ensureCompiled(p) else { throw CoreError.message(String(localized: "配置不是 velum-engine 的")) }
        let config = try Data(contentsOf: p.ir)
        var msg = [CChar](repeating: 0, count: 512)
        let h = config.withUnsafeBytes { c in
            velum_engine_start_local(c.bindMemory(to: UInt8.self).baseAddress, c.count, p.router.path, p.rules.path, &msg, msg.count)
        }
        guard let h else {
            let m = String(cString: msg)
            // 引擎升级后路由文件格式不兼容：重新编译一次再试
            if VelumFiles.isStaleRouter(m), !retried {
                VelumFiles.invalidate(p)
                return try startLocked(retried: true)
            }
            throw CoreError.message(m)
        }
        if let m = try? Data(contentsOf: p.ruleMeta) {
            _ = m.withUnsafeBytes { velum_engine_set_meta(h, $0.bindMemory(to: UInt8.self).baseAddress, $0.count) }
        }
        engine = h
    }

    func stop() {
        lock.lock()
        defer { lock.unlock() }
        stopLocked()
    }

    private func stopLocked() {
        if let e = engine {
            velum_engine_stop(e)
            engine = nil
        }
    }

    /// 一条 tunnelproto 消息。测速最长约 20 秒：调用发生在 Go 的线程上，可以阻塞。
    func call(_ req: String) throws -> String {
        lock.lock()
        guard let e = engine else {
            lock.unlock()
            throw CoreError.message(String(localized: "内核未运行"))
        }
        lock.unlock()
        let d = Data(req.utf8)
        let p = d.withUnsafeBytes { velum_engine_call(e, $0.bindMemory(to: UInt8.self).baseAddress, $0.count) }
        guard let p else { throw CoreError.message(String(localized: "内核未运行")) }
        defer { velum_string_free(p) }
        return String(cString: p)
    }
}
