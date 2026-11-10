// 真机：Rust 控制面（-VelumRustCore YES）开关隧道。只在设了 VELUM_DEVICE 时运行（TEST_RUNNER_VELUM_DEVICE=1）。
// 订阅沿用 Go 版保存的（第一次启动时 velum-core 从 profiles.yaml / config.yaml 导入）；没有时用 SUB_URL 添加。
// 用系统浏览器打开网址产生经隧道的流量（测试进程自己可能没有「无线数据」权限），结束时断开。
import XCTest

final class DeviceRustCoreTests: XCTestCase {
    private func see(_ app: XCUIApplication, _ text: String, timeout: TimeInterval = 20) -> Bool {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: timeout)
    }

    private func tap(_ s: XCUIElement) { s.switches.firstMatch.exists ? s.switches.firstMatch.tap() : s.tap() }

    func testRustCoreTunnel() throws {
        guard ProcessInfo.processInfo.environment["VELUM_DEVICE"] != nil else { throw XCTSkip("只在真机上运行（VELUM_DEVICE）") }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-VelumRustCore", "YES"]
        app.launch()
        // 中途失败也要断开：重新打开 App 关掉开关（-VelumRustCore 的核心会停掉隧道）
        addTeardownBlock {
            let a = XCUIApplication()
            a.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-VelumRustCore", "YES"]
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

        let t2 = app.switches["连接"].firstMatch
        if t2.value as? String == "1" { tap(t2) }
        XCTAssertTrue(see(app, "未连接", timeout: 30))
        XCTAssertTrue(see(app, "已选线路", timeout: 30), "断开后本地引擎没有恢复")
    }
}
