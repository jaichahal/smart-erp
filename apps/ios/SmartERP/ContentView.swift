import SwiftUI

/// Shows the baseline API readiness surface (`GET /health`).
struct ContentView: View {
    @State private var statusLine = "API status unavailable"

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Smart ERP")
                .font(.largeTitle)
                .accessibilityIdentifier("app-title")
            Text(statusLine)
                .accessibilityIdentifier("api-status")
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .task {
            await loadHealth()
        }
    }

    private func loadHealth() async {
        do {
            let health = try await HealthClient.health()
            statusLine = "\(health.data.status) \(health.data.version)"
        } catch {
            statusLine = "API status unavailable"
        }
    }
}
