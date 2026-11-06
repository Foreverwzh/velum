// 用真实的 Go 核心（订阅转换、编译、Explain、本地内核）走一遍。需要一个本机订阅，经 SUB_URL 传入；没有时跳过。
// 模拟器不能运行隧道扩展，所以「连接」应当如实报错并回到未连接。隧道本身只能在真机验证。
import XCTest

final class RealCoreTests: XCTestCase {
    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 15) {
        let e = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "没有看到：\(text)")
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    func test1Onboarding() throws {
        guard let url = ProcessInfo.processInfo.environment["SUB_URL"] else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
        app.launch()

        // 全新安装：首次引导，真实拉取并转换订阅
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 30), "没有出现首次引导")
        field.tap()
        let c = app.buttons["Continue"]
        if c.waitForExistence(timeout: 1) { c.tap() }
        field.typeText(url)
        app.buttons["添加订阅"].tap()
        see(app, "准备好了", timeout: 30)
        shot("real-ready")

        // 导入结果是真实转换出来的：进程规则在 iOS 上如实提醒
        see(app, "项无法精确转换")
        shot("real-ready")

        // 模拟器没有隧道：如实失败，不停在「正在连接」
        app.buttons["连接"].tap()
        see(app, "VPN", timeout: 35) // 引导页留在原地并说明原因
        Thread.sleep(forTimeInterval: 0.5)
        shot("real-connect")
        XCTAssertFalse(app.staticTexts["正在连接…"].exists)
        app.buttons["稍后再连接"].tap()
        see(app, "未连接")
        shot("real-after-later")
    }

    /// 接着上一条（订阅已保存）：线路、解释、切换节点都走真实核心。
    func test2ExplainAndSelect() throws {
        guard ProcessInfo.processInfo.environment["SUB_URL"] != nil else { throw XCTSkip("没有 SUB_URL") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
        app.launch()
        see(app, "未连接", timeout: 30)
        see(app, "已选线路")
        shot("real-status")

        app.buttons["搜索节点，或查询任意网址的线路"].tap()
        let f = app.searchFields.firstMatch
        XCTAssertTrue(f.waitForExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 0.6)
        f.tap()
        f.typeText("www.example.org")
        see(app, "DOMAIN-SUFFIX,example.org,PROXY") // 真实 Explain 的答案，来自订阅原文
        shot("real-answer")
        f.typeText("\n")
        see(app, "规则 #1")
        see(app, "来自 ")
        shot("real-explain")
        app.navigationBars.buttons.firstMatch.tap()

        // 切换节点到直连，同一个地址的答案跟着变
        f.buttons.firstMatch.tap() // 清空
        f.tap()
        f.typeText("直连")
        let row = app.staticTexts["直连"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), "搜索“直连”应当能找到 DIRECT")
        row.tap()
        Thread.sleep(forTimeInterval: 0.5)
        f.buttons.firstMatch.tap()
        f.tap()
        f.typeText("www.example.org")
        let chain = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS 'PROXY，然后 直连'")).firstMatch
        XCTAssertTrue(chain.waitForExistence(timeout: 8), "切换后答案应当走直连")
        shot("real-switched")
    }
}
