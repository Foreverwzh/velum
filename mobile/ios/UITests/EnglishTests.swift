// 英文走查：系统语言是英文时，界面上不应该出现中文。演示数据（节点名等）来自订阅，不算。
import XCTest

final class EnglishTests: XCTestCase {
    /// 演示核心的虚构数据，真实使用时来自订阅或核心。
    private let demo = ["香港", "日本", "新加坡", "美国", "流媒体", "我的家", "家", "自动选择", "苹果服务", "兜底", "我的订阅", "备用", "演示数据",
                        "订阅服务返回", "另一个 VPN", "iOS 不能识别", "不支持 smux"]

    private func assertNoChinese(_ app: XCUIApplication, _ page: String, file: StaticString = #filePath, line: UInt = #line) {
        Thread.sleep(forTimeInterval: 0.6)
        var found: [String] = []
        for e in app.descendants(matching: .any).allElementsBoundByIndex.prefix(300) {
            guard e.exists else { continue }
            let label = e.label
            guard label.range(of: "\\p{Han}", options: .regularExpression) != nil else { continue }
            if demo.contains(where: { label.contains($0) }) || label == "节点" || label == "规则" { continue } // 演示核心的分类
            found.append(label)
        }
        XCTAssertEqual(Array(Set(found)).sorted(), [], "\(page) 里有中文", file: file, line: line)
        if let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] {
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("en-\(page).png"))
        }
    }

    private func launch(_ scenario: String, _ extra: [String] = []) -> XCUIApplication {
        continueAfterFailure = true
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-scenario", scenario] + extra
        app.launch()
        return app
    }

    func testStatusStates() {
        for s in ["protected", "off", "attention", "paused", "captive"] {
            let app = launch(s)
            XCTAssertTrue(app.staticTexts["Velum"].waitForExistence(timeout: 8))
            assertNoChinese(app, s)
            app.terminate()
        }
    }

    func testTabsAndPages() {
        for (name, extra) in [("activity", ["-tab", "activity"]), ("settings", ["-tab", "settings"]),
                              ("groups", ["-push", "groups"]), ("members", ["-push", "group:Proxy"]),
                              ("explain", ["-push", "explain:github.com"]), ("subs", ["-push", "subs"]),
                              ("rules", ["-push", "rules"]), ("warnings", ["-push", "warnings"]),
                              ("ask", ["-ask", "github.com"])] {
            let app = launch("attention", extra)
            Thread.sleep(forTimeInterval: 1.5)
            assertNoChinese(app, name)
            app.terminate()
        }
    }

    func testOnboarding() {
        let app = launch("firstRun")
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 8))
        assertNoChinese(app, "welcome")
        app.terminate()
    }
}
