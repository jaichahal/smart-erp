import XCTest

/// Journeys the phone does not perform yet. Each assertion names the outcome
/// from docs/spec/08-acceptance-tests.md and stays red until that screen exists.
final class JourneyUITests: XCTestCase {
    func testSalesOrderThroughDelivery() { expect("Delivery note registered") }
    func testCollectionAndAging() { expect("Aging buckets") }
    func testBankMatch() { expect("Matched bank line") }
    func testPurchaseLpoThroughPayment() { expect("LPO payment released") }
    func testStockCount() { expect("Stock count posted") }
    func testApprovalInbox() { expect("Approval inbox") }
    func testNotifications() { expect("Notifications") }

    private func expect(_ heading: String) {
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080"]
        app.launch()
        XCTAssertTrue(app.staticTexts[heading].waitForExistence(timeout: 3), "missing \(heading)")
    }
}
