// 用 Rust 控制面（velum-core，默认的核心）走一遍：拉取订阅、编译、本地引擎、切换节点。
// 需要一个本机订阅，经 SUB_URL 传入；没有时跳过。模拟器不能运行隧道扩展，「连接」应当如实报错并回到未连接。
import XCTest

final class RustCoreTests: XCTestCase {
    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 15) {
        let e = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "没有看到：\(text)")
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
        app.launch()
        return app
    }

    func test1Onboarding() throws {
        guard let url = ProcessInfo.processInfo.environment["SUB_URL"] else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = launch()

        // 全新安装：首次引导，Rust 拉取并编译订阅
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 30), "没有出现首次引导")
        field.tap()
        let c = app.buttons["Continue"]
        if c.waitForExistence(timeout: 1) { c.tap() }
        field.typeText(url)
        app.buttons["添加订阅"].tap()
        see(app, "准备好了", timeout: 30)
        shot("rust-ready")

        // 模拟器没有隧道：如实失败，不停在「正在连接」
        app.buttons["连接"].tap()
        see(app, "VPN", timeout: 35)
        Thread.sleep(forTimeInterval: 0.5)
        shot("rust-connect")
        XCTAssertFalse(app.staticTexts["正在连接…"].exists)
        app.buttons["稍后再连接"].tap()
        see(app, "未连接")
        shot("rust-after-later")
    }

    /// 接着上一条（订阅已保存在 core.json）：重新启动后读到订阅，切换节点由本地引擎生效并保存。
    func test2Select() throws {
        guard ProcessInfo.processInfo.environment["SUB_URL"] != nil else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = launch()
        see(app, "未连接", timeout: 30)
        see(app, "香港 01")
        shot("rust-status")

        app.buttons["搜索节点，或查询任意网址的线路"].tap()
        let f = app.searchFields.firstMatch
        XCTAssertTrue(f.waitForExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 0.6)
        f.tap()
        f.typeText("日本")
        let row = app.staticTexts["日本 01"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), "搜索“日本”应当能找到节点")
        row.tap()
        Thread.sleep(forTimeInterval: 1)
        shot("rust-selected")

        // 重新启动：选择已保存
        app.terminate()
        let again = launch()
        see(again, "日本 01", timeout: 30)
        shot("rust-relaunch")
    }

    /// 接着上面（订阅已保存）：查一个地址怎么走（引擎回答）、加本地规则后结论跟着变、撤销后变回去。
    func test3WhyAndLocalRule() throws {
        guard ProcessInfo.processInfo.environment["SUB_URL"] != nil else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = launch()
        see(app, "未连接", timeout: 30)
        app.buttons["搜索节点，或查询任意网址的线路"].tap()
        let f = app.searchFields.firstMatch
        XCTAssertTrue(f.waitForExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 0.6)
        f.tap()
        f.typeText("www.example.org")
        see(app, "DOMAIN-SUFFIX,example.org,PROXY") // 命中规则的原文，来自订阅
        f.typeText("\n")
        see(app, "经 日本 01 转发") // 上一条测试选了日本 01
        see(app, "规则 #1") // 订阅的第 1 条规则（Velum 自己生成的规则不计）
        see(app, "来自 sub.yaml")
        shot("rust-explain")
        app.buttons["添加本地规则…"].tap()
        see(app, "将写入本地规则")
        app.buttons["添加"].tap()
        see(app, "本地规则已生效")
        see(app, "直连，不经过节点")
        shot("rust-rule-done")
        app.buttons["撤销"].tap()
        see(app, "经 日本 01 转发")
        shot("rust-rule-undone")
    }

    /// 英文系统：核心返回的文字（修复步骤等）用 App 附带的英文表。
    func test4EnglishFromCore() throws {
        guard ProcessInfo.processInfo.environment["SUB_URL"] != nil else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons.element(boundBy: 2).waitForExistence(timeout: 30))
        app.tabBars.buttons.element(boundBy: 2).tap()
        let fix = app.buttons.containing(NSPredicate(format: "label CONTAINS[c] 'repair'")).firstMatch
        for _ in 0..<6 where !fix.isHittable { app.swipeUp() }
        XCTAssertTrue(fix.waitForExistence(timeout: 5), "\(app.buttons.allElementsBoundByIndex.map(\.label))")
        fix.tap()
        see(app, "Fetch the subscription and rule sets again", timeout: 90)
        see(app, "Reach the internet", timeout: 60)
        see(app, "captive.apple.com, ", timeout: 30) // 经本地引擎实际访问到了
        shot("rust-english-repair")
    }
}

