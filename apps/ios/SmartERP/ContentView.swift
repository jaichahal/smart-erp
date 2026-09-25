import SwiftUI

/// Shows baseline API readiness and a sign-in form against that same API.
struct ContentView: View {
    @State private var statusLine = "API status unavailable"
    @State private var loginName = ""
    @State private var password = ""
    @State private var errorLine = ""
    @State private var account: Account?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text("Smart ERP")
                    .font(.largeTitle)
                    .accessibilityIdentifier("app-title")
                Text(statusLine)
                    .accessibilityIdentifier("api-status")
                if let account {
                    Text(account.name)
                        .accessibilityIdentifier("signed-in-name")
                    PhoneHome(account: account)
                } else {
                    TextField("Login name", text: $loginName)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("login-name")
                        .frame(minHeight: 44)
                    SecureField("Password", text: $password)
                        .accessibilityIdentifier("password")
                        .frame(minHeight: 44)
                    Button("Sign in") {
                        Task {
                            errorLine = ""
                            do {
                                let opened = try await DeviceSession.open(loginName: loginName, password: password)
                                account = opened
                            } catch {
                                account = nil
                                errorLine = String(describing: error)
                            }
                        }
                    }
                    .frame(minWidth: 44, minHeight: 44)
                    .accessibilityIdentifier("sign-in")
                    if !errorLine.isEmpty {
                        Text(errorLine)
                            .accessibilityIdentifier("sign-in-error")
                    }
                }
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
        }
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
