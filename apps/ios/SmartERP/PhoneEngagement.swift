import SwiftUI
import UIKit

struct PhoneOnboarding: View {
    var onWorkEmail: () -> Void
    @State private var step = "phone"
    @State private var country = "+971"
    @State private var phone = ""
    @State private var code = ""
    @State private var errorLine = ""
    @State private var verified = false
    @State private var name = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if step == "phone" {
                Text("1 of 3").accessibilityIdentifier("onboarding-progress")
                Text("What is your mobile number?").font(.title2)
                HStack {
                    ForEach(["+971", "+91", "+44", "+1"], id: \.self) { item in
                        Button(item) { country = item }
                            .frame(minHeight: 44)
                            .accessibilityIdentifier(item == country ? "country-code" : "country-\(item)")
                    }
                }
                TextField("Phone number", text: $phone)
                    .keyboardType(.phonePad)
                    .textContentType(.telephoneNumber)
                    .accessibilityIdentifier("phone-number")
                    .frame(minHeight: 44)
                Button("Continue") { step = "code" }
                    .disabled(phone.filter(\.isNumber).count < 7)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("phone-continue")
                Button("Use work email", action: onWorkEmail)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("use-work-email")
            } else if step == "code" {
                Text("2 of 3").accessibilityIdentifier("onboarding-progress")
                Text("Enter the code sent to \(country) \(phone)").font(.title2)
                TextField("Code", text: $code)
                    .keyboardType(.numberPad)
                    .textContentType(.oneTimeCode)
                    .accessibilityIdentifier("otp-code")
                    .frame(minHeight: 44)
                Button("Verify code") {
                    Task {
                        let result = await DeviceSession.verifyPhoneCode(phone: "\(country)\(phone)", code: code)
                        if result == "verified" {
                            verified = true
                            step = "name"
                        } else {
                            errorLine = result
                        }
                    }
                }
                .disabled(code.count < 4)
                .frame(minHeight: 44)
                .accessibilityIdentifier("otp-verify")
                if !errorLine.isEmpty {
                    Text(errorLine).accessibilityIdentifier("otp-error")
                }
            } else if step == "name" && verified {
                Text("3 of 3").accessibilityIdentifier("onboarding-progress")
                Text("What should we call you?").font(.title2)
                TextField("Display name", text: $name)
                    .accessibilityIdentifier("profile-name")
                    .frame(minHeight: 44)
                Button("Skip") { step = "biometric" }
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("profile-skip")
            } else {
                Text("Confirm with biometrics").accessibilityIdentifier("biometric-prompt")
                Text("A later step-up still goes to the session API. This screen does not invent a token.")
            }
        }
    }
}

struct SettingsPanel: View {
    let session: APISession
    @AppStorage("smarterp-theme") private var theme = "system"
    @AppStorage("smarterp-biometric") private var biometricLock = false
    @AppStorage("smarterp-notify-approvals") private var notifyApprovals = true
    @AppStorage("smarterp-notify-payments") private var notifyPayments = true
    @AppStorage("smarterp-direction") private var direction = "ltr"
    @AppStorage("smarterp-server") private var server = ""
    @State private var draft = ""
    @State private var biometricDetail = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Settings").accessibilityIdentifier("settings-screen")
            Button("System") { theme = "system" }.frame(minHeight: 44).accessibilityIdentifier("theme-system")
            Button("Light") { theme = "light" }.frame(minHeight: 44).accessibilityIdentifier("theme-light")
            Button("Black") { theme = "dark" }.frame(minHeight: 44).accessibilityIdentifier("theme-dark")
            Text(theme).accessibilityIdentifier("theme-value")
            Toggle("Biometric lock", isOn: $biometricLock)
                .frame(minHeight: 44)
                .accessibilityIdentifier("biometric-lock")
                .onChange(of: biometricLock) { _, on in
                    guard on else { return }
                    Task {
                        let (status, body) = try await session.exchange(
                            "POST",
                            path: "/api/v1/auth/step-up",
                            body: ["method": "biometric", "code": ""]
                        )
                        let data = body["data"] as? [String: Any]
                        let token = data?["step_up_token"] as? String ?? ""
                        if (200..<300).contains(status), !token.isEmpty {
                            biometricDetail = "Step-up accepted"
                        } else {
                            biometricDetail = (body["error"] as? [String: Any])?["message"] as? String ?? "HTTP \(status)"
                        }
                    }
                }
            if biometricLock {
                Text("Confirm with biometrics").accessibilityIdentifier("biometric-prompt")
            }
            if !biometricDetail.isEmpty {
                Text(biometricDetail).accessibilityIdentifier("biometric-error")
            }
            Toggle("Approval notifications", isOn: $notifyApprovals)
                .frame(minHeight: 44)
                .accessibilityIdentifier("notify-approvals")
            Toggle("Payment notifications", isOn: $notifyPayments)
                .frame(minHeight: 44)
                .accessibilityIdentifier("notify-payments")
            Text(direction == "rtl" ? "الاتجاه من اليمين" : "Left to right").accessibilityIdentifier("direction-preview")
            Button("Right to left") { direction = "rtl" }.frame(minHeight: 44).accessibilityIdentifier("direction-rtl")
            Text(direction).accessibilityIdentifier("direction-value")
            TextField("Server URL", text: $draft)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("server-url")
                .frame(minHeight: 44)
            Button("Save server") { server = draft.trimmingCharacters(in: .whitespaces) }
                .frame(minHeight: 44)
                .accessibilityIdentifier("save-server")
            Text("This phone has no offline copy. Screens read the live API.")
                .accessibilityIdentifier("sync-status")
            Text(Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.1.0")
                .accessibilityIdentifier("app-version")
        }
        .accessibilityIdentifier("settings-panel")
        .onAppear { draft = server }
    }
}

func decisionHaptic() {
    UIImpactFeedbackGenerator(style: .medium).impactOccurred()
}
