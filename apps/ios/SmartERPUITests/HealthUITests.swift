import XCTest

/// End-to-end UI check against the compose-published API.
/// `deploy/compose/docker-compose.yml` maps host port 8080 to the api container.
/// The iOS Simulator shares the Mac network stack, so 127.0.0.1 is the host.
final class HealthUITests: XCTestCase {
    private let apiBaseURL = URL(string: "http://127.0.0.1:8080")!

    func testHealthSurfaceShowsReadyFromBaselineAPI() async throws {
        let healthURL = apiBaseURL.appending(path: "health")
        let probe: (Data, HTTPURLResponse)
        do {
            probe = try await fetch(healthURL)
        } catch {
            XCTFail("baseline API not reachable at \(healthURL.absoluteString): \(error)")
            return
        }
        let (body, response) = probe
        guard response.statusCode == 200 else {
            let text = String(data: body, encoding: .utf8) ?? ""
            XCTFail("baseline API not reachable at \(healthURL.absoluteString): HTTP \(response.statusCode) \(text)")
            return
        }
        let health = try JSONDecoder().decode(HealthEnvelope.self, from: body)
        XCTAssertFalse(health.data.status.isEmpty, "baseline health status was empty")

        let app = XCUIApplication()
        app.launchArguments = ["-apiBaseURL", apiBaseURL.absoluteString]
        app.launch()

        let status = app.staticTexts["api-status"]
        XCTAssertTrue(status.waitForExistence(timeout: 15), "api-status did not appear")
        let visible = status.label
        XCTAssertTrue(
            visible.contains(health.data.status),
            "expected visible health status \(health.data.status) from \(healthURL.absoluteString), got \(visible)"
        )
        XCTAssertTrue(
            visible.contains(health.data.version),
            "expected visible health version \(health.data.version) from \(healthURL.absoluteString), got \(visible)"
        )
    }

    private func fetch(_ url: URL) async throws -> (Data, HTTPURLResponse) {
        var request = URLRequest(url: url)
        request.timeoutInterval = 8
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw URLError(.badServerResponse)
        }
        return (data, http)
    }
}

private struct HealthEnvelope: Decodable {
    struct Payload: Decodable {
        let status: String
        let version: String
        let commit: String
    }

    let data: Payload
}
