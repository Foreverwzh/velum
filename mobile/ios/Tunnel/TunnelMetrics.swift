import Foundation
import MetricKit

/// 把系统给隧道进程的 MetricKit 报告（CPU、内存、网络、崩溃诊断等，约每天一次）存到共享目录。
/// 报告不含访问过的地址。App 打开时把它们复制到自己的目录，便于导出分析。
final class TunnelMetrics: NSObject, MXMetricManagerSubscriber {
    static let shared = TunnelMetrics()
    private static let keep = 32
    private let queue = DispatchQueue(label: "velum.metrics", qos: .utility)
    private var dir: URL?

    func start(home: URL) {
        queue.async { [self] in
            guard dir == nil else { return }
            dir = home.appendingPathComponent("tunnel/metrickit", isDirectory: true)
            MXMetricManager.shared.add(self)
        }
    }

    func didReceive(_ payloads: [MXMetricPayload]) {
        queue.async { [self] in
            for p in payloads { save("metric", p.jsonRepresentation(), p.timeStampEnd) }
        }
    }

    func didReceive(_ payloads: [MXDiagnosticPayload]) {
        queue.async { [self] in
            for p in payloads { save("diagnostic", p.jsonRepresentation(), p.timeStampEnd) }
        }
    }

    private func save(_ kind: String, _ data: Data, _ end: Date) {
        guard let dir else { return }
        let fm = FileManager.default
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        // 文件名以结束时间开头：同一份报告不重复保存，也能按名字清理最旧的
        let name = "\(Int(end.timeIntervalSince1970))-\(kind).json"
        try? data.write(to: dir.appendingPathComponent(name), options: .atomic)
        let files = ((try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? [])
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
        for old in files.dropLast(Self.keep) { try? fm.removeItem(at: old) }
    }
}
