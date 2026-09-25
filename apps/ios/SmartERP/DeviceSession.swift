import CryptoKit
import Foundation

enum DeviceSession {
    static func signIn(loginName: String, password: String) async throws -> String {
        try await open(loginName: loginName, password: password).name
    }

    static func verifyPhoneCode(phone: String, code: String) async -> String {
        let base = HealthClient.baseURL().absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        do {
            let session = try await post("\(base)/api/v1/auth/session", ["login_name": phone])
            guard let id = session["session_id"] as? String, !id.isEmpty else {
                return "HTTP session id missing"
            }
            let checked = try await post("\(base)/api/v1/auth/session/\(id)/check", ["totp": code])
            if (checked["verified"] as? Bool) == true {
                return "verified"
            }
            return "HTTP session was not verified"
        } catch {
            let text = String(describing: error)
            return text.contains("HTTP") ? text : "HTTP \(text)"
        }
    }

    static func open(loginName: String, password: String) async throws -> APISession {
        let base = HealthClient.baseURL().absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        let key = P256.Signing.PrivateKey()
        let raw = key.publicKey.x963Representation
        guard raw.count == 65 else { throw SessionError.badKey }
        let jwk: [String: String] = [
            "kty": "EC",
            "crv": "P-256",
            "x": b64(Data(raw.dropFirst().prefix(32))),
            "y": b64(Data(raw.dropFirst(33))),
        ]
        let enrolled = try await post(
            "\(base)/api/v1/auth/device/enroll",
            [
                "public_key": jwk,
                "platform": "ios",
                "app_version": "0.1.0",
                "device_name": "simulator",
            ]
        )
        let deviceID = try string(enrolled, "device_id")
        let session = try await post("\(base)/api/v1/auth/session", ["login_name": loginName])
        let sessionID = try string(session, "session_id")
        let checked = try await post("\(base)/api/v1/auth/session/\(sessionID)/check", ["password": password])
        guard (checked["verified"] as? Bool) == true else { throw SessionError.message("Sign-in was not verified") }
        let tokenURL = "\(base)/api/v1/auth/token"
        let tokens = try await post(
            tokenURL,
            ["session_id": sessionID, "device_id": deviceID],
            dpop: try proof(key: key, jwk: jwk, method: "POST", url: tokenURL, access: nil)
        )
        let access = try string(tokens, "access_token")
        let meURL = "\(base)/api/v1/me"
        let me = try await get(meURL, access: access, dpop: try proof(key: key, jwk: jwk, method: "GET", url: meURL, access: access))
        let name = try string(me, "name")
        guard !name.isEmpty else { throw SessionError.message("Current user name was empty") }
        return APISession(
            name: name,
            userId: try string(me, "id"),
            roles: strings(me["roles"]),
            personas: strings(me["personas"]),
            base: base,
            access: access,
            key: key,
            jwk: jwk
        )
    }

    fileprivate static func proof(key: P256.Signing.PrivateKey, jwk: [String: String], method: String, url: String, access: String?) throws -> String {
        let header: [String: Any] = ["typ": "dpop+jwt", "alg": "ES256", "jwk": jwk]
        var payload: [String: Any] = [
            "htm": method,
            "htu": url,
            "iat": Int(Date().timeIntervalSince1970),
            "jti": UUID().uuidString,
        ]
        if let access {
            let digest = SHA256.hash(data: Data(access.utf8))
            payload["ath"] = b64(Data(digest))
        }
        let headerData = try JSONSerialization.data(withJSONObject: header, options: [.sortedKeys])
        let payloadData = try JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys])
        let signingInput = b64(headerData) + "." + b64(payloadData)
        let signature = try key.signature(for: Data(signingInput.utf8))
        return signingInput + "." + b64(signature.rawRepresentation)
    }

    private static func post(_ url: String, _ body: [String: Any], dpop: String? = nil) async throws -> [String: Any] {
        try await call(url, method: "POST", body: body, access: nil, dpop: dpop)
    }

    private static func get(_ url: String, access: String, dpop: String) async throws -> [String: Any] {
        try await call(url, method: "GET", body: nil, access: access, dpop: dpop)
    }

    fileprivate static func call(_ url: String, method: String, body: [String: Any]?, access: String?, dpop: String?) async throws -> [String: Any] {
        var request = URLRequest(url: URL(string: url)!)
        request.httpMethod = method
        request.timeoutInterval = 8
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.setValue(UUID().uuidString, forHTTPHeaderField: "Idempotency-Key")
        }
        if let access {
            request.setValue("Bearer \(access)", forHTTPHeaderField: "Authorization")
        }
        if let dpop {
            request.setValue(dpop, forHTTPHeaderField: "DPoP")
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        let http = response as? HTTPURLResponse
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        guard let status = http?.statusCode, (200..<300).contains(status), let dataObject = object?["data"] as? [String: Any] else {
            let text = String(data: data, encoding: .utf8) ?? ""
            throw SessionError.message("HTTP \(http?.statusCode ?? 0) \(text)")
        }
        return dataObject
    }

    fileprivate static func exchange(_ url: String, method: String, body: [String: Any]?, access: String, dpop: String) async throws -> (Int, [String: Any]) {
        var request = URLRequest(url: URL(string: url)!)
        request.httpMethod = method
        request.timeoutInterval = 8
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("Bearer \(access)", forHTTPHeaderField: "Authorization")
        request.setValue(dpop, forHTTPHeaderField: "DPoP")
        if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.setValue(UUID().uuidString, forHTTPHeaderField: "Idempotency-Key")
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        return (status, object)
    }

    private static func strings(_ value: Any?) -> [String] {
        value as? [String] ?? []
    }

    private static func string(_ data: [String: Any], _ key: String) throws -> String {
        guard let value = data[key] as? String else { throw SessionError.message("missing \(key)") }
        return value
    }

    private static func b64(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

enum SessionError: Error {
    case badKey
    case message(String)
}

final class APISession {
    let name: String
    let userId: String
    let roles: [String]
    let personas: [String]
    private let base: String
    private let access: String
    private let key: P256.Signing.PrivateKey
    private let jwk: [String: String]

    init(name: String, userId: String, roles: [String], personas: [String], base: String, access: String, key: P256.Signing.PrivateKey, jwk: [String: String]) {
        self.name = name
        self.userId = userId
        self.roles = roles
        self.personas = personas
        self.base = base
        self.access = access
        self.key = key
        self.jwk = jwk
    }

    func exchange(_ method: String, path: String, body: [String: Any]?) async throws -> (Int, [String: Any]) {
        let url = base + path
        let htu = url.split(separator: "?", maxSplits: 1).first.map(String.init) ?? url
        let proof = try DeviceSession.proof(key: key, jwk: jwk, method: method, url: htu, access: access)
        return try await DeviceSession.exchange(url, method: method, body: body, access: access, dpop: proof)
    }
}

func homeKind(roles: [String], personas: [String]) -> String {
    let labels = (roles + personas).map { $0.trimmingCharacters(in: .whitespaces).lowercased() }.filter { !$0.isEmpty }
    func has(_ needle: String) -> Bool { labels.contains { $0.contains(needle) } }
    if has("cfo") || has("partner") { return "cfo" }
    if has("finance manager") || has("finance_manager") { return "finance-manager" }
    if has("accountant") { return "accountant" }
    if has("sales") || has("collection") { return "sales" }
    return "default"
}

func sheetForDrag(dx: CGFloat, dy: CGFloat) -> String? {
    if dy > 72 && abs(dy) > abs(dx) { return "flag" }
    if dx > 72 { return "approve" }
    if dx < -72 { return "reject" }
    return nil
}
