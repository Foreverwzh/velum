// 真机：隧道用 velum-engine 内核跑一遍（tun.engine: velum）。只在设了 VELUM_DEVICE 时运行（xcodebuild test 用
// TEST_RUNNER_VELUM_DEVICE=1 传入）；还没有订阅时用 SUB_URL 添加。结束时切回 sing-box 并断开。
// 用系统浏览器打开几个网址（测试进程自己可能没有「无线数据」权限），再看活动页的连接列表（来自引擎的 App 消息）。
import XCTest

final class DeviceEngineTests: XCTestCase {
    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 20) -> Bool {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: timeout)
    }

    /// 用默认浏览器打开网址（经隧道），等页面加载。
    private func browse(_ url: String) {
        XCUIDevice.shared.system.open(URL(string: url)!)
        Thread.sleep(forTimeInterval: 6)
    }

    private func setEngine(_ app: XCUIApplication, velum: Bool) {
        // 活动页搜索时弹出的键盘会挡住标签栏：先收起
        if app.keyboards.count > 0 { app.typeText("\n") }
        for name in ["取消", "关闭", "Cancel", "Close"] where app.keyboards.count > 0 {
            let b = app.buttons[name].firstMatch
            if b.exists { b.tap() }
        }
        let tab = app.tabBars.buttons["设置"]
        for _ in 0..<3 where !tab.isSelected {
            tab.tap()
            Thread.sleep(forTimeInterval: 0.5)
        }
        let t = app.switches["使用 velum-engine 内核"].firstMatch
        // 页面可能停在上次滚动的位置：先回到顶部再往下找
        for _ in 0..<4 where !t.isHittable { app.swipeDown() }
        for _ in 0..<8 where !t.isHittable { app.swipeUp() }
        if !t.waitForExistence(timeout: 5) {
            print("DEVICE_SWITCHES \(app.switches.allElementsBoundByIndex.map(\.label)) TABS \(app.tabBars.buttons.allElementsBoundByIndex.map { "\($0.label):\($0.isSelected)" }) KB \(app.keyboards.count)")
        }
        XCTAssertTrue(t.exists, "设置里没有内核开关")
        if (t.value as? String == "1") != velum {
            t.switches.firstMatch.exists ? t.switches.firstMatch.tap() : t.tap()
        }
        let deadline = Date().addingTimeInterval(20)
        while Date() < deadline, (t.value as? String == "1") != velum { Thread.sleep(forTimeInterval: 0.5) }
        XCTAssertEqual(t.value as? String == "1", velum, "内核开关没有切换")
    }

    func testVelumEngineTunnel() throws {
        guard ProcessInfo.processInfo.environment["VELUM_DEVICE"] != nil else { throw XCTSkip("只在真机上运行（VELUM_DEVICE）") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
        app.launch()

        // 还没有订阅：首次引导
        let field = app.textFields.firstMatch
        if field.waitForExistence(timeout: 8) {
            guard let url = ProcessInfo.processInfo.environment["SUB_URL"] else { throw XCTSkip("还没有订阅，也没有 SUB_URL") }
            field.tap()
            field.typeText(url)
            app.buttons["添加订阅"].tap()
            XCTAssertTrue(see(app, "准备好了", timeout: 40))
            let later = app.buttons["稍后再连接"]
            if later.waitForExistence(timeout: 3) { later.tap() }
        }

        setEngine(app, velum: true)
        app.tabBars.buttons["状态"].tap()
        let toggle = app.switches["连接"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 20))
        if toggle.value as? String != "1" { toggle.switches.firstMatch.exists ? toggle.switches.firstMatch.tap() : toggle.tap() }
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["允许"]
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        XCTAssertTrue(see(app, "正在保护", timeout: 40), "隧道没有连上")
        Thread.sleep(forTimeInterval: 2)

        for u in ["http://captive.apple.com/hotspot-detect.html", "https://www.baidu.com/", "https://github.com/"] {
            browse(u)
        }
        app.activate()

        // 连接页：来自引擎的连接列表（App 消息 connections）
        app.tabBars.buttons["活动"].tap()
        Thread.sleep(forTimeInterval: 3)
        // 按域名搜索：列表滚出屏幕的行不在界面树里，直接找会漏
        var seen: [String] = []
        let search = app.searchFields.firstMatch
        for h in ["captive.apple.com", "baidu.com", "github.com"] {
            if search.waitForExistence(timeout: 5) {
                search.tap()
                if let v = search.value as? String, !v.isEmpty, search.buttons.firstMatch.exists { search.buttons.firstMatch.tap() }
                search.typeText(h)
            }
            if see(app, h, timeout: 8) { seen.append(h) }
        }
        if search.exists, search.buttons.firstMatch.exists { search.buttons.firstMatch.tap() }
        let cancel = app.buttons["取消"].firstMatch
        if cancel.exists { cancel.tap() }
        print("DEVICE_ENGINE_SEEN \(seen)")

        // 恢复：切回 sing-box 内核，再断开
        setEngine(app, velum: false)
        app.tabBars.buttons["状态"].tap()
        XCTAssertTrue(see(app, "正在保护", timeout: 40), "切回 sing-box 后隧道没有连上")
        let t2 = app.switches["连接"].firstMatch
        if t2.value as? String == "1" { t2.switches.firstMatch.exists ? t2.switches.firstMatch.tap() : t2.tap() }
        XCTAssertTrue(see(app, "未连接", timeout: 20))
        XCTAssertEqual(seen.count, 3, "有网址没出现在活动页：\(seen)")
    }
}
