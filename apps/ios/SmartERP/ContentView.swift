import SwiftUI

/// Shows baseline API readiness and a sign-in form against that same API.
struct ContentView: View {
    @State private var statusLine = "API status unavailable"
    @State private var loginName = ""
    @State private var password = ""
    @State private var signedInName = ""
    @State private var errorLine = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Smart ERP")
                .font(.largeTitle)
                .accessibilityIdentifier("app-title")
            Text(statusLine)
                .accessibilityIdentifier("api-status")
            TextField("Login name", text: $loginName)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("login-name")
            SecureField("Password", text: $password)
                .accessibilityIdentifier("password")
            Button("Sign in") {
                Task {
                    errorLine = ""
                    do {
                        signedInName = try await DeviceSession.signIn(loginName: loginName, password: password)
                    } catch {
                        signedInName = ""
                        errorLine = String(describing: error)
                    }
                }
            }
            .accessibilityIdentifier("sign-in")
            if !signedInName.isEmpty {
                Text(signedInName)
                    .accessibilityIdentifier("signed-in-name")
            }
            if !errorLine.isEmpty {
                Text(errorLine)
                    .accessibilityIdentifier("sign-in-error")
            }
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
