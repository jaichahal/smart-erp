import XCTest

final class EngagementUITests: XCTestCase {
    func testPhoneNumberIsTheFirstQuestion() {
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080"]
        app.launch()
        XCTAssertTrue(app.staticTexts["1 of 3"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.textFields["phone-number"].exists)
        XCTAssertFalse(app.textFields["login-name"].exists)
    }

    func testCodeStepUsesTheSessionAPI() {
        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", "http://127.0.0.1:8080"]
        app.launch()
        let phone = app.textFields["phone-number"]
        XCTAssertTrue(phone.waitForExistence(timeout: 10))
        phone.tap()
        phone.typeText("501234567")
        app.buttons["phone-continue"].tap()
        let code = app.textFields["otp-code"]
        XCTAssertTrue(code.waitForExistence(timeout: 5))
        code.tap()
        code.typeText("000000")
        app.buttons["otp-verify"].tap()
        let error = app.staticTexts["otp-error"]
        XCTAssertTrue(error.waitForExistence(timeout: 20))
        XCTAssertTrue(error.label.contains("HTTP"), error.label)
        XCTAssertFalse(app.staticTexts["signed-in-name"].exists)
    }
}
