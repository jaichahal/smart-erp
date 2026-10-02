import XCTest

/// Signs in on the simulator against the Docker API on this Mac (127.0.0.1).
/// Password matches the documented dev value in deploy/compose/.env.dev.example.
final class SignInUITests: XCTestCase {
    func testSignsInAndShowsDirectoryUserFromDockerAPI() async throws {
        let apiBaseURL = URL(string: "http://127.0.0.1:8080")!
        let healthURL = apiBaseURL.appending(path: "health")
        var request = URLRequest(url: healthURL)
        request.timeoutInterval = 8
        let (body, response) = try await URLSession.shared.data(for: request)
        let http = try XCTUnwrap(response as? HTTPURLResponse)
        let text = String(data: body, encoding: .utf8) ?? ""
        XCTAssertEqual(http.statusCode, 200, "baseline API is not reachable at \(healthURL.absoluteString): \(text)")
        XCTAssertTrue(text.contains("\"ready\""), text)

        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", apiBaseURL.absoluteString]
        app.launch()

        let login = app.buttons["use-work-email"]
        XCTAssertTrue(login.waitForExistence(timeout: 10), "phone onboarding was not shown")
        login.tap()
        let email = app.textFields["login-name"]
        XCTAssertTrue(email.waitForExistence(timeout: 10), "login field was not shown")
        email.tap()
        email.typeText("admin@dev.localhost")

        let password = app.secureTextFields["password"]
        XCTAssertTrue(password.waitForExistence(timeout: 5))
        password.tap()
        password.typeText("Admin1234!")

        app.buttons["sign-in"].tap()
        let name = app.staticTexts["signed-in-name"]
        XCTAssertTrue(name.waitForExistence(timeout: 20), "signed-in name did not appear")
        XCTAssertEqual(name.label, "Dev Admin")
    }
}
