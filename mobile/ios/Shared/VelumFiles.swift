// velum-engine 的文件，App 与隧道扩展共用。约定见 core/pkg/engine/tunnelproto：
// App（Go）写 meta.json（engine 为 velum 时）与 velum-ir.json / velum-rule-sets.json / velum-meta.json；
// 这里把它们编译成路由文件（App 进程里编译，扩展只加载；扩展发现产物缺失或过期时自己编译兜底）。
import Foundation
import VelumEngine

enum VelumFiles {
    static let engineName = "velum"

    struct Meta: Decodable {
        var engine: String?
        var hash: String
        var routes: [String]?
    }

    struct Paths {
        let dir: URL
        var meta: URL { dir.appendingPathComponent("meta.json") }
        var ir: URL { dir.appendingPathComponent("velum-ir.json") }
        var ruleSets: URL { dir.appendingPathComponent("velum-rule-sets.json") }
        var ruleMeta: URL { dir.appendingPathComponent("velum-meta.json") }
        var router: URL { dir.appendingPathComponent("velum-router.vrrt") }
        var rules: URL { dir.appendingPathComponent("velum-rules.vrdb") }
        /// 编译产物对应的 meta 摘要：与 meta.json 的 hash 不同就要重新编译。
        var stamp: URL { dir.appendingPathComponent("velum-compiled.txt") }
        var fakeIPCache: URL { dir.appendingPathComponent("velum-fakeip.bin") }
    }

    static func meta(_ p: Paths) -> Meta? {
        guard let d = try? Data(contentsOf: p.meta) else { return nil }
        return try? JSONDecoder().decode(Meta.self, from: d)
    }

    /// 配置要求用 velum-engine。
    static func selected(_ p: Paths) -> Bool { meta(p)?.engine == engineName }

    /// 让下一次 ensureCompiled 重新编译（引擎升级后路由文件格式不兼容时）。
    static func invalidate(_ p: Paths) {
        try? FileManager.default.removeItem(at: p.stamp)
    }

    /// 引擎报路由文件无效（格式版本不符等）。
    static func isStaleRouter(_ message: String) -> Bool { message.contains("路由文件无效") }

    /// 内核是 velum-engine 且编译产物缺失或过期时编译。返回是否用 velum-engine。
    @discardableResult
    static func ensureCompiled(_ p: Paths) throws -> Bool {
        guard let m = meta(p), m.engine == engineName else { return false }
        let fm = FileManager.default
        if fm.fileExists(atPath: p.router.path), fm.fileExists(atPath: p.rules.path),
           (try? String(contentsOf: p.stamp, encoding: .utf8)) == m.hash {
            return true
        }
        let ir = try Data(contentsOf: p.ir)
        let sets = (try? Data(contentsOf: p.ruleSets)) ?? Data()
        var msg = [CChar](repeating: 0, count: 512)
        let ok = ir.withUnsafeBytes { i in
            sets.withUnsafeBytes { s in
                velum_compile(i.bindMemory(to: UInt8.self).baseAddress, i.count,
                              s.bindMemory(to: UInt8.self).baseAddress, s.count,
                              p.router.path, p.rules.path, &msg, msg.count)
            }
        }
        guard ok else {
            throw NSError(domain: "io.github.foreverwzh.velum", code: 2,
                          userInfo: [NSLocalizedDescriptionKey: String(localized: "编译路由失败：\(String(cString: msg))")])
        }
        try m.hash.write(to: p.stamp, atomically: true, encoding: .utf8)
        return true
    }
}
