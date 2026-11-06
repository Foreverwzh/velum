// 共用的小部件。路由链是 Velum 的标志：规则 → 策略组 → 节点，在所有页面都是同一个形状。
import SwiftUI

struct ChainStep: Identifiable, Hashable {
    enum Kind { case rule, group, node, direct, reject }
    var label: String
    var kind: Kind
    var id: String { label + "\(kind)" }

    /// 与桌面端 chainSteps 相同：最后一段是节点，DIRECT 和 REJECT 单独表示。
    static func from(_ chain: [String], ruleIndex: Int? = nil) -> [ChainStep] {
        var out: [ChainStep] = []
        if let ruleIndex, ruleIndex >= 0 { out.append(ChainStep(label: "#\(ruleIndex)", kind: .rule)) }
        for (i, c) in chain.enumerated() {
            if c == "DIRECT" { out.append(ChainStep(label: String(localized: "直连"), kind: .direct)) }
            else if c == "REJECT" || c.hasPrefix("reject") { out.append(ChainStep(label: String(localized: "拦截"), kind: .reject)) }
            else { out.append(ChainStep(label: c, kind: i == chain.count - 1 ? .node : .group)) }
        }
        return out
    }
}

struct RouteChain: View {
    var steps: [ChainStep]
    var compact = false

    var body: some View {
        FlowLayout(spacing: compact ? 3 : 4, lineSpacing: 4) {
            ForEach(Array(steps.enumerated()), id: \.offset) { i, step in
                // 箭头跟在前一段后面：折行时落在行尾，不会出现在下一行开头。
                HStack(spacing: compact ? 3 : 4) {
                    label(step)
                    if i < steps.count - 1 {
                        Image(systemName: "chevron.compact.right")
                            .font(compact ? .caption2 : .footnote)
                            .foregroundStyle(.tertiary)
                    }
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(steps.map(\.spoken).joined(separator: String(localized: "，然后 ")))
    }

    private func label(_ step: ChainStep) -> some View {
        Text(step.label)
            .font(font(step))
            .foregroundStyle(step.foreground)
            .lineLimit(1)
            .padding(.horizontal, compact ? 0 : 6)
            .padding(.vertical, compact ? 0 : 2)
            .background {
                if !compact, let bg = step.background {
                    RoundedRectangle(cornerRadius: 6, style: .continuous).fill(bg)
                }
            }
    }

    private func font(_ step: ChainStep) -> Font {
        let base: Font = compact ? .footnote : .subheadline
        return step.kind == .rule ? base.monospaced() : step.kind == .node && !compact ? base.weight(.medium) : base
    }
}

extension ChainStep {
    var foreground: Color {
        switch kind {
        case .rule: .info
        case .group: .primary
        case .node: .teal500
        case .direct: .primary
        case .reject: .secondary
        }
    }

    var background: Color? {
        switch kind {
        case .rule: .info.opacity(0.12)
        case .group, .direct: Color(.tertiarySystemFill)
        case .node: .teal500.opacity(0.12)
        case .reject: nil
        }
    }

    var spoken: String {
        switch kind {
        case .rule: String(localized: "规则 \(String(label.dropFirst()))")
        default: label
        }
    }
}

/// 延迟：数字和颜色同时表达；超时用文字。
struct LatencyLabel: View {
    var ms: Int?
    /// 只是参考数值（没有接管流量时），不用健康色。
    var muted = false

    var body: some View {
        if let ms {
            HStack(spacing: 4) {
                if ms < 0 { Image(systemName: "exclamationmark.circle.fill").font(.caption) }
                Text(ms < 0 ? "超时" : "\(ms) ms")
            }
            .font(.footnote.monospacedDigit())
            .foregroundStyle(muted && ms >= 0 ? Color.secondary : color(ms))
            .accessibilityLabel(ms < 0 ? "超时" : "延迟 \(ms) 毫秒")
        }
    }

    private func color(_ ms: Int) -> Color {
        if ms < 0 { return .err }
        if ms < 300 { return .teal500 }
        if ms < 800 { return .warn }
        return .err
    }
}

/// 换行布局：节点名很长时路由链折到下一行，而不是截断。
struct FlowLayout: Layout {
    var spacing: CGFloat = 4
    var lineSpacing: CGFloat = 4

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let s = arrange(width: proposal.width ?? .infinity, subviews: subviews).size
        // 向上取整：放置时拿到的宽度可能比这里算出的小一点点，不能因此把最后一段挤到下一行
        return CGSize(width: s.width.rounded(.up), height: s.height.rounded(.up))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let r = arrange(width: bounds.width, subviews: subviews)
        for (i, p) in r.points.enumerated() {
            subviews[i].place(at: CGPoint(x: bounds.minX + p.x, y: bounds.minY + p.y), proposal: ProposedViewSize(r.sizes[i]))
        }
    }

    private func arrange(width: CGFloat, subviews: Subviews) -> (size: CGSize, points: [CGPoint], sizes: [CGSize]) {
        var x: CGFloat = 0, y: CGFloat = 0, line: CGFloat = 0, maxX: CGFloat = 0
        var points: [CGPoint] = [], sizes: [CGSize] = []
        for v in subviews {
            let s = v.sizeThatFits(ProposedViewSize(width: width, height: nil))
            if x > 0, x + s.width > width + 0.5 { x = 0; y += line + lineSpacing; line = 0 } // 容忍浮点误差
            points.append(CGPoint(x: x, y: y))
            sizes.append(s)
            x += s.width + spacing
            maxX = max(maxX, x - spacing)
            line = max(line, s.height)
        }
        return (CGSize(width: maxX, height: y + line), points, sizes)
    }
}

/// 共用的导航目的地。
extension View {
    func velumRoutes() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case .groups: GroupsView()
            case let .group(tag): MembersView(tag: tag)
            case let .explain(t): ExplainView(target: t)
            case let .connection(c): ConnectionView(connection: c)
            case .subscriptions: SubscriptionsView()
            case .rules: LocalRulesView()
            case .warnings: WarningsView()
            }
        }
    }
}
