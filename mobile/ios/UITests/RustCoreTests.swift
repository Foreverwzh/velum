// 用 Rust 控制面（velum-core，启动参数 -VelumRustCore YES）走一遍：拉取订阅、编译、本地引擎、切换节点。
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
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-VelumRustCore", "YES"]
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
}
