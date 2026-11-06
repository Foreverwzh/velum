// 把核心状态归纳为一句结论（与 desktop/src/health.ts、mobile/lib/health.dart 口径一致，去掉系统代理和 helper 分支）。
import SwiftUI

struct Health: Equatable {
    enum Tone { case green, amber, red, gray }
    var tone: Tone
    var title: String
    var detail: String
    var customSymbol: String?

    var color: Color {
        switch tone {
        case .green: .teal500
        case .amber: .warn
        case .red: .err
        case .gray: .secondary
        }
    }

    /// 状态同时用图形表达，不只靠颜色。
    var symbol: String {
        if let customSymbol { return customSymbol }
        return switch tone {
        case .green: "checkmark.shield.fill"
        case .amber: "pause.circle.fill"
        case .red: "exclamationmark.triangle.fill"
        case .gray: "shield.slash"
        }
    }

    /// iOS 的网卡名：en0 是 Wi-Fi，pdp_ip 是蜂窝。
    static func networkName(_ iface: String?) -> String {
        guard let iface, !iface.isEmpty else { return "" }
        if iface.hasPrefix("en") { return "Wi-Fi" }
        if iface.hasPrefix("pdp_ip") { return String(localized: "蜂窝网络") }
        return iface
    }

    static func of(_ s: Status?, error: String?) -> Health {
        guard let s else {
            if let error { return Health(tone: .red, title: String(localized: "Velum 未运行"), detail: error) }
            return Health(tone: .gray, title: String(localized: "正在启动…"), detail: String(localized: "正在加载订阅与规则"))
        }
        if s.starting == true { return Health(tone: .gray, title: String(localized: "正在启动…"), detail: String(localized: "正在加载订阅与规则")) }
        if let e = s.lastError, !e.isEmpty { return Health(tone: .red, title: String(localized: "配置有问题"), detail: e) }
        if s.subscription.source.isEmpty { return Health(tone: .gray, title: String(localized: "还没有订阅"), detail: String(localized: "添加订阅后即可开始使用")) }
        if s.wantTun && !s.tunOn, s.network?.paused == nil { return Health(tone: .gray, title: String(localized: "正在连接…"), detail: String(localized: "正在建立 VPN")) }
        if let p = s.network?.paused, !p.isEmpty { return Health(tone: .amber, title: String(localized: "已暂停接管"), detail: p) }
        if s.network?.captive == "captive" { return Health(tone: .amber, title: String(localized: "需要网页认证"), detail: String(localized: "这个网络要先在浏览器中完成登录")) }
        if s.network?.captive == "offline" { return Health(tone: .amber, title: String(localized: "网络不通"), detail: String(localized: "当前网络无法访问互联网")) }
        if s.tunOn {
            let n = Self.networkName(s.network?.primaryInterface)
            return Health(tone: .green, title: String(localized: "正在保护"), detail: (n.isEmpty ? "" : n + " · ") + String(localized: "流量经 Velum 分流"))
        }
        return Health(tone: .gray, title: String(localized: "未连接"), detail: String(localized: "连接后，所有应用的流量经 Velum 分流"))
    }
}

extension Color {
    static let teal500 = Color("Teal")
    static let warn = Color("Warn")
    static let err = Color("Err")
    static let info = Color("Info")
}
