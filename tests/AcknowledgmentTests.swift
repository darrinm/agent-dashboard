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

func checkDateParsing() {
    let formatter=ISO8601DateFormatter();formatter.formatOptions=[.withInternetDateTime,.withFractionalSeconds]
    for text in ["2026-09-10T22:38:39.123456789Z","2026-09-10T22:38:39Z","2024-02-29T23:59:59.5Z","1970-01-01T00:00:00Z","2000-03-01T00:00:00.000001Z"] {
        let expected=formatter.date(from:text) ?? ISO8601DateFormatter().date(from:text)!
        guard let parsed=JSON.parseUTCDate(text) else {preconditionFailure("fast parser rejected \(text)")}
        precondition(abs(parsed.timeIntervalSince1970-expected.timeIntervalSince1970)<0.001,"fast parser mismatch for \(text)")
    }
    precondition(JSON.parseUTCDate("2026-09-10T22:38:39+02:00")==nil,"offsets must fall back to the formatter")
    precondition(JSON.parseUTCDate("0001-01-01T00:00:00Z")?.timeIntervalSince1970 == -62135596800,"Go's zero time must match Go's calendar")
}

@MainActor func checkPanelGrouping() {
    let now=Date()
    func session(_ id:String,daysOld:Double?,parent:String?=nil,working:Bool=false)->AgentSession {
        let attention=daysOld.map{Attention(id:"e-"+id,kind:"question",openedAt:now.addingTimeInterval(-$0*86400),seen:false,notified:false,snoozedUntil:.distantPast)}
        return AgentSession(id:id,nativeID:id,machineID:"m",machine:"Mac",provider:"codex",source:"codex",kind:parent==nil ? "cli":"subagent",title:id,project:"p",cwd:nil,branch:nil,parentID:parent,aliases:[],execution:working ? "working":"idle",outcome:"unknown",connectivity:"online",evidence:"inferred",attention:attention,lastActivity:now,stateSince:now,observedAt:now,summary:"",capabilities:[],transcript:nil,attachID:nil,buckets:[],hasActivity:false,openTools:0,remote:false)
    }
    // Six requests, all older than seven days: the three newest stay visible.
    let old=(0..<6).map{session("old\($0)",daysOld:13+Double($0))}
    let allOld=partitionRequests(old)
    precondition(allOld.shown.map(\.id)==["old0","old1","old2"],"newest old requests were folded")
    precondition(allOld.folded.map(\.id)==["old3","old4","old5"],"older requests were not folded")
    // Recent requests are never folded, and older ones fold behind them.
    let mixed=partitionRequests([session("recent0",daysOld:1),session("recent1",daysOld:2),session("recent2",daysOld:3),session("recent3",daysOld:4)]+old)
    precondition(mixed.shown.count==4 && mixed.folded.count==6,"recent requests must stay visible")
    // Idle subagents nest under a present parent; active or orphaned ones stay listed.
    let groups=nesting([session("parent",daysOld:nil),session("idleChild",daysOld:nil,parent:"parent"),session("workingChild",daysOld:nil,parent:"parent",working:true),session("orphan",daysOld:nil,parent:"missing")])
    precondition(groups.nested==["idleChild"],"nesting hid the wrong sessions: \(groups.nested)")
    precondition(groups.counts["parent"]==2,"parent subagent count wrong")
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
        checkDateParsing()
        checkPanelGrouping()
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
        print("Acknowledgment checks passed: stale episodes refresh without replay, passive failures stay silent, manual failures report, old snapshots cannot regress state, the newest requests stay visible, idle subagents nest under their parent, and collector dates parse without a formatter.")
    }
}
