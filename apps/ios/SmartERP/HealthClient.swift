import Foundation

struct HealthSnapshot: Decodable {
    struct Payload: Decodable {
        let status: String
        let version: String
        let commit: String
    }

    let data: Payload
}

/// Talks to the compose-published API. Host port 8080 is the mapping in
/// `deploy/compose/docker-compose.yml`. The simulator reaches that port on
/// 127.0.0.1. A launch argument `-apiBaseURL <url>` overrides the default.
enum HealthClient {
    static func baseURL() -> URL {
        let args = CommandLine.arguments
        if let index = args.firstIndex(of: "-apiBaseURL"),
           index + 1 < args.count,
           let url = URL(string: args[index + 1]) {
            return url
        }
        let saved = UserDefaults.standard.string(forKey: "smarterp-server") ?? ""
        if !saved.isEmpty, let url = URL(string: saved) {
            return url
        }
        return URL(string: "http://127.0.0.1:8080")!
    }

    static func health() async throws -> HealthSnapshot {
        let url = baseURL().appending(path: "health")
        var request = URLRequest(url: url)
        request.timeoutInterval = 8
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        return try JSONDecoder().decode(HealthSnapshot.self, from: data)
    }
}
