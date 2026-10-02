import SwiftUI

/// Shows baseline API readiness and a sign-in form against that same API.
struct ContentView: View {
    @State private var statusLine = "API status unavailable"
    @State private var loginName = ""
    @State private var password = ""
    @State private var showPassword = false
    @State private var session: APISession?
    @State private var errorLine = ""
    @State private var mode = "phone"
    @AppStorage("smarterp-theme") private var theme = "system"
    @AppStorage("smarterp-direction") private var direction = "ltr"

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Smart ERP")
                .font(.largeTitle)
                .accessibilityIdentifier("app-title")
            Text(theme == "dark" ? "dark" : theme == "light" ? "light" : "system")
                .accessibilityIdentifier("theme-applied")
            Text(statusLine)
                .accessibilityIdentifier("api-status")
            if let session {
                SignedInHome(session: session)
            } else if mode == "phone" {
                PhoneOnboarding { mode = "email" }
            } else {
                TextField("Login name", text: $loginName)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("login-name")
                    .frame(minHeight: 44)
                if showPassword {
                    TextField("Password", text: $password)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("password")
                        .frame(minHeight: 44)
                } else {
                    SecureField("Password", text: $password)
                        .accessibilityIdentifier("password")
                        .frame(minHeight: 44)
                }
                Button(showPassword ? "Hide password" : "Show password") {
                    showPassword.toggle()
                }
                .frame(minHeight: 44)
                Button("Sign in") {
                    Task {
                        errorLine = ""
                        do {
                            session = try await DeviceSession.open(loginName: loginName, password: password)
                        } catch {
                            session = nil
                            errorLine = String(describing: error)
                        }
                    }
                }
                .accessibilityIdentifier("sign-in")
                .frame(minHeight: 44)
                if !errorLine.isEmpty {
                    Text(errorLine)
                        .accessibilityIdentifier("sign-in-error")
                }
            }
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(theme == "dark" ? Color.black : Color.white)
        .preferredColorScheme(theme == "dark" ? .dark : theme == "light" ? .light : nil)
        .environment(\.layoutDirection, direction == "rtl" ? .rightToLeft : .leftToRight)
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
