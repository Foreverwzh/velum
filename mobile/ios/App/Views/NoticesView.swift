// 第三方组件的版权声明：App 附带的 ThirdPartyNotices.txt（scripts/notices.sh 生成），按组件分段懒加载。
import SwiftUI

struct NoticesView: View {
    private static let url = Bundle.main.url(forResource: "ThirdPartyNotices", withExtension: "txt")
    static var available: Bool { url != nil }

    @State private var sections: [String] = []

    var body: some View {
        List(Array(sections.enumerated()), id: \.offset) { _, s in
            Text(s)
                .font(.caption.monospaced())
                .textSelection(.enabled)
        }
        .navigationTitle("第三方组件")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            guard sections.isEmpty, let u = Self.url, let t = try? String(contentsOf: u, encoding: .utf8) else { return }
            sections = t.components(separatedBy: String(repeating: "=", count: 72) + "\n")
                .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
                .filter { !$0.isEmpty }
        }
    }
}
