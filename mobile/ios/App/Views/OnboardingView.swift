// 首次启动：添加订阅 → 看到真实导入结果（包括无法转换的内容）→ 说明 VPN 权限后连接。
import SwiftUI

struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @State private var imported = false
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if imported { ready } else { add }
            }
            .interactiveDismissDisabled()
        }
    }

    private var add: some View {
        SubscriptionForm(cta: "添加订阅", intro: "填写订阅地址即可开始使用。订阅地址由提供订阅的服务给你。") { imported = true }
            .navigationTitle("欢迎使用 Velum")
    }

    private var ready: some View {
        let s = model.status?.subscription
        return List {
            Section("已导入") {
                LabeledContent("节点", value: "\(s?.nodes ?? 0)")
                LabeledContent("策略组", value: "\(s?.groups ?? 0)")
                LabeledContent("规则", value: "\(s?.rules ?? 0)")
                LabeledContent("规则集", value: "\(s?.ruleSets ?? 0)")
                if (model.status?.warnings ?? 0) > 0 {
                    NavigationLink {
                        WarningsView()
                    } label: {
                        Label("\(model.status!.warnings) 项无法精确转换", systemImage: "exclamationmark.triangle.fill")
                            .labelStyle(TintedIcon(color: .warn))
                    }
                }
            }
            Section {
                Button {
                    Task { await model.setTunnel(true) }
                } label: {
                    Text("连接").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
            } footer: {
                Text("iOS 接下来会询问是否允许添加 VPN 配置，点“允许”。Velum 没有自己的服务器，流量只按你的订阅转发。")
            }
        }
        .navigationTitle("准备好了")
    }
}
