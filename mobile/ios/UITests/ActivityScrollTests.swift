// 活动页一边刷新、一边滚动时不能抖动，滚回顶部后搜索框要在。
import XCTest

final class ActivityScrollTests: XCTestCase {
    func testScrollToTopIsStableWhileConnectionsArrive() {
        continueAfterFailure = true
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-scenario", "attention", "-tab", "activity", "-liveConns", "YES"]
        app.launch()
        let list = app.collectionViews.firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 8))
        Thread.sleep(forTimeInterval: 25) // 攒够一屏多的连接（每 2 秒一条）

        for round in 0..<3 {
            list.swipeUp(velocity: .fast)
            Thread.sleep(forTimeInterval: 1.5)
            list.swipeDown(velocity: .fast)
            list.swipeDown(velocity: .fast)
            Thread.sleep(forTimeInterval: 1.5)

            let search = app.searchFields.firstMatch
            XCTAssertTrue(search.waitForExistence(timeout: 2) && search.isHittable, "第 \(round) 轮：回到顶部后没有搜索框")

            // 静止时位置不能跳：系统的滚动状态在来回切换就会表现为抖动
            var ys: [CGFloat] = []
            for _ in 0..<12 {
                ys.append(search.exists ? search.frame.minY : -1)
                Thread.sleep(forTimeInterval: 0.25)
            }
            XCTAssertEqual(Set(ys).count, 1, "第 \(round) 轮：搜索框位置在跳 \(ys)")
        }
    }
}
