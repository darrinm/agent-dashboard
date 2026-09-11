import Foundation

final class AcknowledgmentProtocol: URLProtocol {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))!
    static var requests: [URLRequest] = []
    static func configure(_ response: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.lock(); defer { lock.unlock() }
        handler = response; requests = []
    }
    static func recordedRequests() -> [URLRequest] {
        lock.lock(); defer { lock.unlock() }; return requests
    }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.lock(); Self.requests.append(request); let respond = Self.handler!; Self.lock.unlock()
        do {
            let (status, data) = try respond(request)
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

@main struct AcknowledgmentTests {
    @MainActor static func settled(_ model: AgentModel) async throws {
        for _ in 0..<200 {
            if model.seenPending.isEmpty { return }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        preconditionFailure("Acknowledgment did not finish")
    }
    @MainActor static func main() async throws {
        UserDefaults.standard.setVolatileDomain(["notificationsEnabled": false], forName: UserDefaults.argumentDomain)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [AcknowledgmentProtocol.self]
        let network = URLSession(configuration: config)
        defer { network.invalidateAndCancel() }
        let now = Date()
        let old = AgentSession(id: "session", nativeID: "native", machineID: "machine", machine: "Test Mac", provider: "claude", source: "test", kind: "interactive", title: "Test", project: "Test", aliases: [], execution: "idle", outcome: "unknown", connectivity: "online", evidence: "direct", attention: Attention(id: "old", kind: "question", openedAt: now, seen: false, notified: false, snoozedUntil: .distantPast), lastActivity: now, stateSince: now, observedAt: now, summary: "Question", capabilities: [], buckets: [], hasActivity: false, openTools: 0, remote: false)
        var replacement = old
        replacement.attention?.id = "new"
        let current = Snapshot(version: "test", sequence: 2, sessions: [replacement], sources: [], machineID: "machine", machine: "Test Mac")
        let currentData = try JSON.encoder().encode(current)
        func model() -> AgentModel {
            let m = AgentModel(dataDir: dir, network: network)
            m.endpoint = CollectorEndpoint(url: "http://collector.test", token: "test-token", pid: 0)
            m.accept(Snapshot(version: "test", sequence: 1, sessions: [old], sources: [], machineID: "machine", machine: "Test Mac"))
            return m
        }
        for action in ["seen", "notified", "snooze"] {
            AcknowledgmentProtocol.configure { request in
                precondition(request.value(forHTTPHeaderField: "Authorization") == "Bearer test-token")
                return request.httpMethod == "POST" ? (409, Data()) : (200, currentData)
            }
            let m = model()
            m.acknowledge(old, action)
            try await settled(m)
            precondition((m.actionError != nil) == (action == "snooze"), "Passive stale acknowledgments must not alert")
            precondition(m.sessions.first?.attention?.id == "new")
            precondition(m.sessions.first?.attention?.seen == false)
            precondition(m.sessions.first?.attention?.notified == false)
            let requests = AcknowledgmentProtocol.recordedRequests()
            precondition(requests.map { $0.httpMethod! } == ["POST", "GET"], "Never replay the action against a replacement episode")
            m.accept(Snapshot(version: "test", sequence: 1, sessions: [old], sources: [], machineID: "machine", machine: "Test Mac"))
            precondition(m.sessions.first?.attention?.id == "new", "Late snapshots must not restore the old request")
        }
        for action in ["seen", "notified", "snooze"] {
            for offline in [false, true] {
                AcknowledgmentProtocol.configure { _ in
                    if offline { throw URLError(.notConnectedToInternet) }
                    return (500, Data())
                }
                let m = model()
                m.acknowledge(old, action)
                try await settled(m)
                precondition((m.actionError != nil) == (action == "snooze"), "Only explicit action failures should alert")
                if action == "notified" {
                    precondition(m.deliveredNotifications.contains("old"), "Failed bookkeeping must not redeliver notifications")
                }
            }
        }
        AcknowledgmentProtocol.configure { _ in (200, Data()) }
        let m = model()
        m.acknowledge(old, "seen")
        try await settled(m)
        precondition(m.actionError == nil && AcknowledgmentProtocol.recordedRequests().count == 1)
        print("Acknowledgment checks passed: stale episodes refresh without replay, passive failures stay silent, manual failures report, and old snapshots cannot regress state.")
    }
}
