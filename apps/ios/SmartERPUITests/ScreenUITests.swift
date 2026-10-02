import XCTest

/// Screens after sign-in. Persona home and the CFO gate use GET /api/v1/me.
/// Document screens call the live API and stay red until those routes exist.
final class ScreenUITests: XCTestCase {
    private let apiBaseURL = URL(string: "http://127.0.0.1:8080")!

    func testPersonaHomeFollowsMe() async throws {
        let app = try await launchSignedIn()
        let home = app.staticTexts["persona-home"]
        XCTAssertTrue(home.waitForExistence(timeout: 5))
        XCTAssertEqual(home.label, "Sales Agent")
        XCTAssertTrue(app.buttons["My Day"].exists)
    }

    func testSalesAgentDoesNotSeeApproveOrBlacklist() async throws {
        let app = try await launchSignedIn()
        app.buttons["Vendor dashboard"].tap()
        XCTAssertFalse(app.buttons["Approve"].waitForExistence(timeout: 2))
        XCTAssertFalse(app.buttons["Blacklist"].exists)
    }

    func testVendorDashboardCallsLiveAPI() async throws {
        let app = try await launchSignedIn()
        app.buttons["Vendor dashboard"].tap()
        let sku = app.textFields["sku-filter"]
        XCTAssertTrue(sku.waitForExistence(timeout: 5))
        sku.tap()
        sku.typeText("RM-TEST")
        app.buttons["apply-sku-filter"].tap()
        try requireLoaded(app, status: "vendor-dashboard-status", error: "vendor-dashboard-error", label: "vendor dashboard")
        XCTAssertTrue(app.otherElements["invoice-counts"].exists || app.staticTexts["invoice-counts"].exists || app.otherElements["best-price"].exists)
        XCTAssertTrue(app.staticTexts["best-price"].exists || app.otherElements["best-price"].exists)
        XCTAssertTrue(app.staticTexts["blocked-group"].exists || app.otherElements["blocked-group"].exists)
    }

    func testSwipeOpensSheetWithoutApproving() async throws {
        let app = try await launchSignedIn()
        app.buttons["Vendor dashboard"].tap()
        let sku = app.textFields["sku-filter"]
        XCTAssertTrue(sku.waitForExistence(timeout: 5))
        sku.tap()
        sku.typeText("RM-TEST")
        app.buttons["apply-sku-filter"].tap()
        try requireLoaded(app, status: "vendor-dashboard-status", error: "vendor-dashboard-error", label: "vendor dashboard swipe")
        let row = app.otherElements["vendor-row"].exists ? app.otherElements["vendor-row"] : app.staticTexts["vendor-row"]
        XCTAssertTrue(row.waitForExistence(timeout: 5))
        row.swipeLeft()
        XCTAssertTrue(app.otherElements["approval-sheet"].waitForExistence(timeout: 3) || app.staticTexts["approval-sheet"].waitForExistence(timeout: 1) || app.staticTexts["Review"].waitForExistence(timeout: 1))
        XCTAssertFalse(app.staticTexts["Approved"].exists)
        XCTAssertFalse(app.buttons["Approve"].exists)
    }

    func testSalesOrderCallsLiveAPI() async throws {
        let app = try await launchSignedIn()
        app.buttons["Sales order"].tap()
        try fill(app.textFields["customer"], "00000000-0000-4000-8000-000000000002")
        try fill(app.textFields["order-sku"], "FG-1")
        try fill(app.textFields["quantity"], "1")
        try fill(app.textFields["unit-price"], "10.00")
        app.buttons["submit-order"].tap()
        try requireLoaded(app, status: "sales-order-number", error: "sales-order-error", label: "sales order")
    }

    func testCollectionReceiptCallsLiveAPI() async throws {
        let app = try await launchSignedIn()
        app.buttons["Collection receipt"].tap()
        try fill(app.textFields["amount"], "25.00")
        app.buttons["record-receipt"].tap()
        try requireLoaded(app, status: "collection-receipt-number", error: "collection-receipt-error", label: "collection receipt")
    }

    func testPurchaseListCallsLiveAPI() async throws {
        let app = try await launchSignedIn()
        app.buttons["Purchase list"].tap()
        try requireLoaded(app, status: "purchase-list-status", error: "purchase-list-error", label: "purchase list")
    }

    private func launchSignedIn() async throws -> XCUIApplication {
        var request = URLRequest(url: apiBaseURL.appending(path: "health"))
        request.timeoutInterval = 8
        let (body, response) = try await URLSession.shared.data(for: request)
        let http = try XCTUnwrap(response as? HTTPURLResponse)
        let text = String(data: body, encoding: .utf8) ?? ""
        XCTAssertEqual(http.statusCode, 200, text)
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", apiBaseURL.absoluteString]
        app.launch()
        let workEmail = app.buttons["use-work-email"]
        if workEmail.waitForExistence(timeout: 10) { workEmail.tap() }
        let login = app.textFields["login-name"]
        XCTAssertTrue(login.waitForExistence(timeout: 10))
        login.tap()
        login.typeText("admin@dev.localhost")
        let password = app.secureTextFields["password"]
        password.tap()
        password.typeText("Admin1234!")
        app.buttons["sign-in"].tap()
        let name = app.staticTexts["signed-in-name"]
        XCTAssertTrue(name.waitForExistence(timeout: 20))
        XCTAssertEqual(name.label, "Dev Admin")
        return app
    }

    private func fill(_ field: XCUIElement, _ value: String) throws {
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText(value)
    }

    private func requireLoaded(_ app: XCUIApplication, status: String, error: String, label: String) throws {
        let statusNode = app.staticTexts[status]
        let errorNode = app.staticTexts[error]
        let ready = statusNode.waitForExistence(timeout: 15) || errorNode.waitForExistence(timeout: 1)
        XCTAssertTrue(ready, "\(label) did not finish")
        if errorNode.exists {
            XCTFail("\(label): \(errorNode.label)")
        }
    }
}
