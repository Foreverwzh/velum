// 真正点一遍关键流程（演示核心，不碰网络）。设置 SHOT_DIR 时把关键步骤的截图写到该目录。
import XCTest

final class FlowTests: XCTestCase {
    private func launch(_ scenario: String, slow: Bool = false) -> XCUIApplication {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-scenario", scenario] + (slow ? ["-slowConnect", "YES"] : [])
        app.launch()
        return app
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 8) {
        let e = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "没有看到：\(text)")
    }

    private func ask(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.buttons["搜索节点，或查询任意网址的线路"].tap()
        let f = app.searchFields.firstMatch
        XCTAssertTrue(f.waitForExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 0.6)
        f.tap()
        dismissKeyboardTip(app)
        f.typeText(text)
        return f
    }

    /// 全新模拟器第一次弹键盘时的滑行输入提示。
    private func dismissKeyboardTip(_ app: XCUIApplication) {
        let c = app.buttons["Continue"]
        if c.waitForExistence(timeout: 1) { c.tap() }
    }

    func testConnectAndDisconnect() {
        let app = launch("off", slow: true) // 连接窗口放长，才能确认中间状态
        see(app, "未连接")
        shot("flow-off")
        app.switches.firstMatch.tap()
        see(app, "正在连接")
        shot("flow-connecting")
        see(app, "正在保护")
        shot("flow-protected")
        app.switches.firstMatch.tap()
        see(app, "未连接")
    }

    func testVPNPermissionDenied() {
        let app = launch("denied")
        see(app, "未连接")
        app.switches.firstMatch.tap()
        see(app, "没有 VPN 权限")
        Thread.sleep(forTimeInterval: 0.3)
        shot("flow-denied")
        // 回到未连接，不停在「正在连接」；错误提示留着
        XCTAssertFalse(app.staticTexts["正在连接…"].exists)
        see(app, "未连接", timeout: 1)
    }

    func testSwitchNodeFromSearch() {
        let app = launch("protected")
        see(app, "香港 02")
        _ = ask(app, "日本")
        see(app, "日本 01")
        shot("flow-search-node")
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS '日本 01'")).firstMatch.tap()
        app.buttons["关闭"].firstMatch.tap()
        see(app, "日本 01") // 回到状态页，当前线路已变
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS '香港 02'")).firstMatch.exists)
        shot("flow-switched")
    }

    func testAddressAnswerAndLocalRule() {
        let app = launch("protected")
        let f = ask(app, "github.com")
        see(app, "DOMAIN-SUFFIX,github.com,Proxy") // 答案就在结果里
        f.typeText("\n")
        see(app, "经 🇭🇰 香港 02 转发")
        shot("flow-explain")
        app.buttons["添加本地规则…"].tap()
        see(app, "将写入本地规则")
        shot("flow-rule-sheet")
        app.buttons["添加"].tap()
        see(app, "本地规则已生效")
        see(app, "直连，不经过节点") // 重新推演，结论变了
        shot("flow-rule-done")
        // 撤销后结论变回去
        app.buttons["撤销"].tap()
        see(app, "经 🇭🇰 香港 02 转发")
        // 再加一次，回到搜索结果页，答案也要变
        app.buttons["添加本地规则…"].tap()
        see(app, "将写入本地规则")
        app.buttons["添加"].tap()
        see(app, "本地规则已生效")
        app.navigationBars.buttons.firstMatch.tap()
        see(app, "这个地址")
        see(app, "direct: github.com")
    }

    /// 配置出错时，已经开着的隧道仍然能关掉，并且有去处理的入口。
    func testConfigErrorCanStillDisconnect() {
        let app = launch("error")
        see(app, "配置有问题")
        see(app, "更换或更新订阅")
        XCTAssertTrue(app.switches.firstMatch.isEnabled)
        shot("flow-error")
        app.switches.firstMatch.tap()
        let off = app.switches.firstMatch
        expectation(for: NSPredicate(format: "value == '0'"), evaluatedWith: off)
        waitForExpectations(timeout: 5)
    }

    /// 没有接管流量时，不说「当前线路」；当前节点超时时不说「正在保护」。
    func testHonestWording() {
        let off = launch("off")
        see(off, "已选线路")
        off.terminate()
        let bad = launch("attention")
        see(bad, "当前节点超时")
        XCTAssertFalse(bad.staticTexts["正在保护"].exists)
        shot("flow-node-timeout")
    }

    func testOnboardingRejectsThenSucceeds() {
        let app = launch("firstRun")
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 8))
        shot("flow-welcome")
        field.tap()
        dismissKeyboardTip(app)
        field.typeText("https://example.com/bad")
        app.buttons["添加订阅"].tap()
        see(app, "订阅服务返回 403")
        shot("flow-welcome-error")
        field.tap()
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 30) + "https://example.com/sub")
        app.buttons["添加订阅"].tap()
        see(app, "准备好了")
        shot("flow-ready")
        app.buttons["连接"].tap()
        see(app, "正在保护")
    }
}
