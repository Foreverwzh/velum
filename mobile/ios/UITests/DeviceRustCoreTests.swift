// 真机：Rust 控制面（默认的核心）开关隧道。只在设了 VELUM_DEVICE 时运行（TEST_RUNNER_VELUM_DEVICE=1）。
// 订阅沿用 Go 版保存的（第一次启动时 velum-core 从 profiles.yaml / config.yaml 导入）；没有时用 SUB_URL 添加。
// 用系统浏览器打开网址产生经隧道的流量（测试进程自己可能没有「无线数据」权限），结束时断开。
import XCTest

final class DeviceRustCoreTests: XCTestCase {
    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 20) -> Bool {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: timeout)
    }

    /// 切换标签页：活动页搜索时弹出的键盘会挡住标签栏，先收起。
    private func openTab(_ app: XCUIApplication, _ name: String) {
        if app.keyboards.count > 0 { app.typeText("\n") }
        let tab = app.tabBars.buttons[name]
        for _ in 0..<3 where !tab.isSelected {
            tab.tap()
            Thread.sleep(forTimeInterval: 0.5)
        }
    }

    private func tap(_ s: XCUIElement) { s.switches.firstMatch.exists ? s.switches.firstMatch.tap() : s.tap() }

    func testRustCoreTunnel() throws {
        guard ProcessInfo.processInfo.environment["VELUM_DEVICE"] != nil else { throw XCTSkip("只在真机上运行（VELUM_DEVICE）") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
        app.launch()
        // 中途失败也要断开：重新打开 App 关掉开关
        addTeardownBlock {
            let a = XCUIApplication()
            a.launchArguments = ["-AppleLanguages", "(zh-Hans)"]
            a.activate()
            let t = a.switches["连接"].firstMatch
            if t.waitForExistence(timeout: 20), t.value as? String == "1" { self.tap(t) }
            _ = self.see(a, "未连接", timeout: 30)
        }

        let field = app.textFields.firstMatch
        if field.waitForExistence(timeout: 8) {
            print("DEVICE_RUST_ONBOARDING \(app.staticTexts.allElementsBoundByIndex.map(\.label))")
            guard let url = ProcessInfo.processInfo.environment["SUB_URL"] else { throw XCTSkip("还没有订阅，也没有 SUB_URL") }
            field.tap()
            field.typeText(url)
            app.buttons["添加订阅"].tap()
            XCTAssertTrue(see(app, "准备好了", timeout: 60))
            let later = app.buttons["稍后再连接"]
            if later.waitForExistence(timeout: 3) { later.tap() }
        }

        // 隧道没开时由 Rust 核心里的本地引擎提供策略组状态（开着时来自隧道里的引擎；文字是「当前线路」）
        XCTAssertTrue(see(app, "全部策略组", timeout: 60), "没有策略组状态")
        let toggle = app.switches["连接"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 20))
        if toggle.value as? String == "1" {
            tap(toggle)
            XCTAssertTrue(see(app, "未连接", timeout: 20), "断不开上一次留下的隧道")
        }
        tap(toggle)
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["允许"]
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        XCTAssertTrue(see(app, "正在保护", timeout: 60), "隧道没有连上")
        Thread.sleep(forTimeInterval: 2)

        // 浏览器经隧道访问：直连（captive.apple.com）与代理（github.com）。读不到 Safari 的界面（kAXErrorServerNotFound），
        // 是否走通由取回的隧道日志判断（velum.log 的 stats 行）
        for url in ["http://captive.apple.com/hotspot-detect.html", "https://github.com/", "https://www.baidu.com/"] {
            XCUIDevice.shared.system.open(URL(string: url)!)
            Thread.sleep(forTimeInterval: 8)
        }
        app.activate()

        // 隧道开着时，策略组状态来自隧道里的引擎
        let groupsShown = see(app, "当前线路", timeout: 20)
        if !groupsShown { print("DEVICE_RUST_APP \(app.staticTexts.allElementsBoundByIndex.map(\.label))") }
        XCTAssertTrue(groupsShown)
        XCTAssertTrue(see(app, "正在保护", timeout: 10))
        // 流量（隧道里引擎的计数，速率由核心算）
        XCTAssertTrue(see(app, "条连接", timeout: 10), "状态页没有流量")

        // 活动页：隧道里引擎的连接列表。按域名搜索：滚出屏幕的行不在界面树里
        openTab(app, "活动")
        Thread.sleep(forTimeInterval: 2)
        var seen: [String] = []
        let search = app.searchFields.firstMatch
        for h in ["captive.apple.com", "github.com", "baidu.com"] {
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
        print("DEVICE_RUST_SEEN \(seen)")
        XCTAssertEqual(seen.count, 3, "有网址没出现在活动页：\(seen)")

        // 一键修复（隧道开着）：重新拉取并加载、测速、经隧道实际访问一次
        openTab(app, "设置")
        let fix = app.buttons["一键修复"].firstMatch
        for _ in 0..<6 where !fix.isHittable { app.swipeUp() }
        XCTAssertTrue(fix.waitForExistence(timeout: 5), "设置里没有一键修复")
        fix.tap()
        let reached = see(app, "访问互联网", timeout: 120)
        print("DEVICE_RUST_REPAIR \(app.staticTexts.allElementsBoundByIndex.map(\.label).filter { $0.contains("毫秒") || $0.contains("节点") || $0.contains("重新") || $0.contains("访问") })")
        XCTAssertTrue(reached, "修复没有完成")
        XCTAssertTrue(see(app, "captive.apple.com，", timeout: 5), "修复时经隧道访问互联网失败")
        openTab(app, "状态")
        XCTAssertTrue(see(app, "正在保护", timeout: 60), "修复后隧道没有恢复")

        let t2 = app.switches["连接"].firstMatch
        if t2.value as? String == "1" { tap(t2) }
        XCTAssertTrue(see(app, "未连接", timeout: 30))
        XCTAssertTrue(see(app, "已选线路", timeout: 30), "断开后本地引擎没有恢复")
    }
}
