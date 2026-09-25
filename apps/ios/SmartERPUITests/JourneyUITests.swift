import XCTest

/// Journeys the phone does not perform yet. Each assertion names the outcome
/// from docs/spec/08-acceptance-tests.md and stays red until that screen exists.
final class JourneyUITests: XCTestCase {
    func testSalesOrderThroughDelivery() { expect("Delivery note registered") }
    func testCollectionAndAging() {
        let app = launchSignedIn()
        XCTAssertTrue(app.staticTexts["Aging buckets"].waitForExistence(timeout: 20))
        XCTAssertTrue(app.staticTexts["On-account remainder"].exists)
    }
    func testBankMatch() { expect("Matched bank line") }
    func testPurchaseLpoThroughPayment() { expect("LPO payment released") }
    func testStockCount() { expect("Stock count posted") }
    func testApprovalInbox() {
        let app = launchSignedIn()
        XCTAssertTrue(app.staticTexts["Approval inbox"].waitForExistence(timeout: 20))
    }
    func testNotifications() { expect("Notifications") }

    func testSwipeDoesNotChangeApprovalState() {
        let app = XCUIApplication()
        let doc = "UI-IOS-\(Int(Date().timeIntervalSince1970))"
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080", "-prepareApproval", "-approvalDoc", doc]
        app.launch()
        signIn(app)
        let card = app.staticTexts[doc]
        XCTAssertTrue(card.waitForExistence(timeout: 20), "approval card was not returned")
        var swipes = 0
        while !card.isHittable && swipes < 6 && !app.frame.intersects(card.frame) {
            app.swipeUp(velocity: .slow)
            swipes += 1
        }
        if app.keyboards.count > 0 {
            app.staticTexts["Approval inbox"].coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        }
        if card.isHittable {
            card.swipeRight()
        } else {
            let start = app.coordinate(withNormalizedOffset: CGVector(
                dx: card.frame.midX / max(app.frame.width, 1),
                dy: min(card.frame.midY / max(app.frame.height, 1), 0.7)))
            let end = start.withOffset(CGVector(dx: 140, dy: 0))
            start.press(forDuration: 0.25, thenDragTo: end)
        }
        XCTAssertTrue(app.staticTexts["Approve request"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Live state pending"].waitForExistence(timeout: 8))
    }

    private func launchSignedIn() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080"]
        app.launch()
        signIn(app)
        return app
    }

    private func signIn(_ app: XCUIApplication) {
        let login = app.textFields["login-name"]
        XCTAssertTrue(login.waitForExistence(timeout: 10))
        login.tap()
        login.typeText("admin@dev.localhost")
        let password = app.secureTextFields["password"]
        XCTAssertTrue(password.waitForExistence(timeout: 5))
        password.tap()
        password.typeText("Admin1234!")
        app.buttons["sign-in"].tap()
        XCTAssertTrue(app.staticTexts["signed-in-name"].waitForExistence(timeout: 20))
    }

    private func expect(_ heading: String) {
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080"]
        app.launch()
        XCTAssertTrue(app.staticTexts[heading].waitForExistence(timeout: 3), "missing \(heading)")
    }
}
