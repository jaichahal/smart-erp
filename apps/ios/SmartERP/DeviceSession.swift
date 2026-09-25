import CryptoKit
import Foundation

struct Account {
    let name: String
    let roles: [String]
    let personas: [String]
    let accessToken: String
    let baseURL: String
    let key: P256.Signing.PrivateKey
    let jwk: [String: String]
}

enum DeviceSession {
    static func signIn(loginName: String, password: String) async throws -> String {
        try await open(loginName: loginName, password: password).name
    }

    static func open(loginName: String, password: String) async throws -> Account {
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
        return Account(
            name: name,
            roles: strings(me, "roles"),
            personas: strings(me, "personas"),
            accessToken: access,
            baseURL: base,
            key: key,
            jwk: jwk
        )
    }

    static func authorized(_ account: Account, method: String, path: String, body: [String: Any]? = nil) async throws -> Any {
        let url = account.baseURL + path
        let proof = try proof(key: account.key, jwk: account.jwk, method: method, url: url, access: account.accessToken)
        return try await envelope(url, method: method, body: body, access: account.accessToken, dpop: proof)
    }

    private static func strings(_ data: [String: Any], _ key: String) -> [String] {
        data[key] as? [String] ?? []
    }

    private static func proof(key: P256.Signing.PrivateKey, jwk: [String: String], method: String, url: String, access: String?) throws -> String {
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

    private static func envelope(_ url: String, method: String, body: [String: Any]?, access: String?, dpop: String?) async throws -> Any {
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
        guard let status = http?.statusCode, (200..<300).contains(status), let payload = object?["data"] else {
            let text = String(data: data, encoding: .utf8) ?? ""
            throw SessionError.message("HTTP \(http?.statusCode ?? 0) \(text)")
        }
        return payload
    }

    private static func call(_ url: String, method: String, body: [String: Any]?, access: String?, dpop: String?) async throws -> [String: Any] {
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
