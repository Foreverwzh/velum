// 最小 App：Documents/run.json 存在时自动跑一轮：开隧道 → 稳定 → 下载压测 → 关隧道 → 把日志复制到 Documents。
// run.json 示例：{"label":"tuned","gogc":50,"limit":24,"settle":30,"parallel":20,"bytes":1000000,"big":30000000}
import NetworkExtension
import UIKit

let group = "group.dev.velum.spike.ios"
let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]

final class Runner {
    var say: (String) -> Void = { _ in }
    func run() async {
        do {
            let runURL = docs.appendingPathComponent("run.json")
            guard let d = try? Data(contentsOf: runURL), let p = try JSONSerialization.jsonObject(with: d) as? [String: Any] else {
                say("没有 run.json：等待 Mac 推送配置")
                return
            }
            let label = p["label"] as? String ?? "run"
            guard let dir = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else { throw E("取不到 App Group") }
            let cfg = dir.appendingPathComponent("cfg.json")
            try? FileManager.default.removeItem(at: cfg)
            try FileManager.default.copyItem(at: docs.appendingPathComponent("cfg.json"), to: cfg)

            let all = try await NETunnelProviderManager.loadAllFromPreferences()
            let m = all.first ?? NETunnelProviderManager()
            let proto = NETunnelProviderProtocol()
            proto.providerBundleIdentifier = "dev.velum.spike.ios.tunnel"
            proto.serverAddress = "Velum 内存测试"
            m.protocolConfiguration = proto
            m.localizedDescription = "Velum 内存测试"
            m.isEnabled = true
            say("保存 VPN 配置（首次要在手机上点允许）")
            try await m.saveToPreferences()
            try await m.loadFromPreferences()

            say("\(label)：启动隧道")
            let session = m.connection
            try session.startVPNTunnel(options: ["gogc": (p["gogc"] as? Int ?? 0) as NSNumber, "limit": (p["limit"] as? Int ?? 0) as NSNumber])
            for _ in 0..<60 {
                if session.status == .connected { break }
                try await Task.sleep(nanoseconds: 500_000_000)
            }
            guard session.status == .connected else { throw E("隧道没有连上，状态 \(session.status.rawValue)") }

            let settle = p["settle"] as? Int ?? 30
            say("\(label)：已连接，稳定 \(settle) 秒")
            try await Task.sleep(nanoseconds: UInt64(settle) * 1_000_000_000)

            var report = "稳定后状态 \(session.status.rawValue)\n"
            let n = p["parallel"] as? Int ?? 20
            let bytes = p["bytes"] as? Int ?? 1_000_000
            say("\(label)：\(n) 并发下载")
            report += await download(n: n, bytes: bytes, timeout: 90) + "，之后隧道状态 \(session.status.rawValue)\n"
            if let big = p["big"] as? Int, big > 0, session.status == .connected {
                say("\(label)：单连接大文件")
                report += await download(n: 1, bytes: big, timeout: 120) + "，之后隧道状态 \(session.status.rawValue)\n"
            }
            if let url = p["fastURL"] as? String, session.status == .connected {
                let fn = p["fastN"] as? Int ?? 4
                let fb = p["fastBytes"] as? Int ?? 100_000_000
                say("\(label)：高速下载 \(fn) 并发")
                report += await fast(url: url, n: fn, bytes: fb, timeout: 60) + "，之后隧道状态 \(session.status.rawValue)\n"
            }
            if session.status == .connected {
                report += "隧道内出口：" + (await trace()) + "\n"
            }
            let alive = session.status == .connected
            report += alive ? "隧道全程存活\n" : "⚠ 隧道中途断开（可能被系统杀掉）\n"
            session.stopVPNTunnel()
            try await Task.sleep(nanoseconds: 2_000_000_000)

            let out = docs.appendingPathComponent("result-\(label).log")
            let log = (try? String(contentsOf: dir.appendingPathComponent("mem.log"), encoding: .utf8)) ?? "（没有日志）"
            try (report + log).write(to: out, atomically: true, encoding: .utf8)
            try? FileManager.default.removeItem(at: runURL)
            say("\(label)：完成\n\(report)")
            try "done".write(to: docs.appendingPathComponent("done-\(label)"), atomically: true, encoding: .utf8)
        } catch {
            say("失败：\(error)")
            try? "\(error)".write(to: docs.appendingPathComponent("error.txt"), atomically: true, encoding: .utf8)
        }
    }

    func trace() async -> String {
        guard let u = URL(string: "https://www.cloudflare.com/cdn-cgi/trace"),
              let (d, _) = try? await URLSession(configuration: .ephemeral).data(from: u),
              let s = String(data: d, encoding: .utf8) else { return "失败" }
        return s.split(separator: "\n").filter { $0.hasPrefix("loc=") }.joined()
    }

    // 边收边丢，App 自身不占内存；超时后按已收到的字节算速度
    func fast(url: String, n: Int, bytes: Int, timeout: Double) async -> String {
        let cfg = URLSessionConfiguration.ephemeral
        let s = URLSession(configuration: cfg)
        let start = Date()
        let errs = ErrBox()
        let total = await withTaskGroup(of: Int.self) { g -> Int in
            for _ in 0..<n {
                g.addTask {
                    guard let u = URL(string: url) else { return 0 }
                    var r = URLRequest(url: u)
                    r.setValue("bytes=0-\(bytes - 1)", forHTTPHeaderField: "Range")
                    r.cachePolicy = .reloadIgnoringLocalCacheData
                    let stream: URLSession.AsyncBytes
                    do {
                        let (st, resp) = try await s.bytes(for: r)
                        stream = st
                        if let h = resp as? HTTPURLResponse, h.statusCode >= 300 { errs.add("HTTP \(h.statusCode)"); return 0 }
                    } catch { errs.add("\(error)"); return 0 }
                    var got = 0, buf = 0
                    do {
                        for try await _ in stream {
                            buf += 1
                            if buf == 65536 {
                                got += buf; buf = 0
                                if Date().timeIntervalSince(start) > timeout { break }
                            }
                        }
                    } catch { errs.add("\(error)") }
                    return got + buf
                }
            }
            var c = 0
            for await v in g { c += v }
            return c
        }
        let t = Date().timeIntervalSince(start)
        return String(format: "高速 %d 并发：共 %.0fMB，用时 %.0fs，%.1fMB/s", n, Double(total) / 1048576, t, Double(total) / 1048576 / t) + errs.text
    }

    func download(n: Int, bytes: Int, timeout: Double) async -> String {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForResource = timeout
        cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
        let s = URLSession(configuration: cfg)
        let start = Date()
        let ok = await withTaskGroup(of: Bool.self) { g -> Int in
            for _ in 0..<n {
                g.addTask {
                    guard let u = URL(string: "https://speed.cloudflare.com/__down?bytes=\(bytes)"),
                          let (d, r) = try? await s.data(from: u) else { return false }
                    return (r as? HTTPURLResponse)?.statusCode == 200 && d.count == bytes
                }
            }
            var c = 0
            for await v in g where v { c += 1 }
            return c
        }
        return String(format: "下载 %d×%d：成功 %d，用时 %.0fs", n, bytes, ok, Date().timeIntervalSince(start))
    }
}

final class ErrBox: @unchecked Sendable {
    private let l = NSLock(); private var v: String?
    func add(_ s: String) { l.lock(); if v == nil { v = String(s.prefix(160)) }; l.unlock() }
    var text: String { v.map { "；错误 " + $0 } ?? "" }
}

struct E: Error, CustomStringConvertible { let description: String; init(_ s: String) { description = s } }

final class VC: UIViewController {
    let label = UITextView()
    let runner = Runner()
    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .systemBackground
        label.frame = view.bounds.insetBy(dx: 16, dy: 80)
        label.font = .systemFont(ofSize: 16)
        label.isEditable = false
        label.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        view.addSubview(label)
        UIApplication.shared.isIdleTimerDisabled = true
        runner.say = { [weak self] s in DispatchQueue.main.async { self?.label.text = s } }
        Task { await runner.run() }
    }
}

// iOS 27 要求使用 Scene 生命周期，否则启动时被系统终止
final class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?
    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        guard let ws = scene as? UIWindowScene else { return }
        let w = UIWindow(windowScene: ws)
        w.rootViewController = VC()
        w.makeKeyAndVisible()
        window = w
    }
}

final class AppDelegate: UIResponder, UIApplicationDelegate {
    func application(_ application: UIApplication, configurationForConnecting session: UISceneSession, options: UIScene.ConnectionOptions) -> UISceneConfiguration {
        let c = UISceneConfiguration(name: "Default", sessionRole: session.role)
        c.delegateClass = SceneDelegate.self
        return c
    }
}

UIApplicationMain(CommandLine.argc, CommandLine.unsafeArgv, nil, NSStringFromClass(AppDelegate.self))
