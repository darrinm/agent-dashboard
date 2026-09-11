import Foundation
import SwiftUI
import UserNotifications

struct Attention: Codable, Equatable {
    var id: String; var kind: String; var openedAt: Date; var seen: Bool; var notified: Bool; var snoozedUntil: Date
}
struct ActivityBucket: Codable, Equatable { var at: Date; var tokens: Int; var tool: Bool }
struct AgentSession: Codable, Identifiable, Equatable {
    var id: String; var nativeID: String; var machineID: String; var machine: String
    var provider: String; var source: String; var kind: String; var title: String; var project: String
    var cwd: String?; var branch: String?; var parentID: String?; var aliases: [String]
    var execution: String; var outcome: String; var connectivity: String; var evidence: String
    var attention: Attention?; var lastActivity: Date; var stateSince: Date; var observedAt: Date
    var summary: String; var capabilities: [String]; var transcript: String?; var attachID: String?
    var buckets: [ActivityBucket]; var hasActivity: Bool; var openTools: Int; var remote: Bool
    var needsYou: Bool { attention.map { $0.kind != "review" } ?? false }
    var older: Bool { needsYou && Date().timeIntervalSince(attention!.openedAt) > 7 * 86400 }
    var active: Bool { needsYou || execution == "working" }
    var isReview: Bool { attention?.kind == "review" && attention?.seen == false && Date().timeIntervalSince(attention!.openedAt)<86400 }
    var color: Color { attention?.kind == "failure" ? Palette.failure : needsYou ? Palette.amber : execution == "working" ? Palette.activity : .secondary }
    var symbol: String { attention?.kind == "failure" ? "exclamationmark.triangle.fill" : needsYou ? "questionmark.circle.fill" : execution == "working" ? "circle.dotted" : "checkmark.circle" }
    var status: String { if let a = attention { return a.kind == "permission" ? "Permission needed" : a.kind == "failure" ? "Needs intervention" : a.kind == "review" ? "Turn finished" : "Has a question" }; return execution == "working" ? "Working" : execution.capitalized }
}
struct SourceHealth: Codable, Identifiable, Equatable { var id: String; var name: String; var status: String; var detail: String; var updatedAt: Date }
struct Snapshot: Codable { var version: String; var sequence: Int64; var sessions: [AgentSession]; var sources: [SourceHealth]; var machineID: String; var machine: String }
struct CollectorEndpoint: Codable { var url: String; var token: String; var pid: Int }
enum JSON {
    static let fractional: ISO8601DateFormatter = { let f=ISO8601DateFormatter(); f.formatOptions=[.withInternetDateTime,.withFractionalSeconds];return f }()
    static let plain=ISO8601DateFormatter()
    static func decoder() -> JSONDecoder { let d=JSONDecoder(); d.dateDecodingStrategy = .custom { decoder in let c=try decoder.singleValueContainer();let s=try c.decode(String.self);if let date=fractional.date(from:s) ?? plain.date(from:s){return date};throw DecodingError.dataCorruptedError(in:c,debugDescription:"Invalid date") };return d }
    static func encoder() -> JSONEncoder { let e=JSONEncoder();e.dateEncodingStrategy = .iso8601;return e }
}
enum Palette {
    static let amber = Color(nsColor: NSColor(name:nil) { $0.bestMatch(from:[.aqua,.darkAqua]) == .darkAqua ? NSColor(srgbRed:1,green:0.71,blue:0.28,alpha:1) : NSColor(srgbRed:0.64,green:0.37,blue:0,alpha:1) })
    static let activity = Color(nsColor: NSColor(name:nil) { $0.bestMatch(from:[.aqua,.darkAqua]) == .darkAqua ? NSColor(srgbRed:0.38,green:0.89,blue:0.81,alpha:1) : NSColor(srgbRed:0.055,green:0.50,blue:0.44,alpha:1) })
    static let failure = Color(nsColor: NSColor.systemRed)
}
func age(_ date: Date) -> String { let seconds=max(0,Int(Date().timeIntervalSince(date)));if seconds>86400{return "\(seconds/86400)d"};if seconds>3600{return "\(seconds/3600)h"};if seconds>60{return "\(seconds/60)m"};return "\(seconds)s" }

@MainActor final class AgentModel: ObservableObject {
    @Published var sessions: [AgentSession]=[]
    @Published var sources: [SourceHealth]=[]
    @Published var connected=false
    @Published var message="Connecting to your sessions…"
    @Published var expandedIDs=Set<String>()
    @Published var revealID:String?
    @Published var search=""
    @Published var showOlder=false
    @Published var showIdle=false
    @Published var settings=false
    @Published var actionError: String?
    let dataDir: URL
    let network: URLSession
    var lastSequence:Int64 = -1
    var snapshotRevision=0
    var acceptedEndpointToken:String?
    var endpoint: CollectorEndpoint?
    var process: Process?
    var streamTask: Task<Void,Never>?
    var onUpdate: (() -> Void)?
    var seenPending=Set<String>()
    var notificationPending=Set<String>()
    var deliveredNotifications=Set<String>()
    @Published var panelVisible=false
    var machine="This Mac"
    init(dataDir: URL, network:URLSession = .shared) { self.dataDir=dataDir;self.network=network
        if let data=try? Data(contentsOf:dataDir.appendingPathComponent("ui-cache.json")),let s=try? JSON.decoder().decode(Snapshot.self,from:data){sessions=s.sessions;sources=s.sources;machine=s.machine}
    }
    var needs: [AgentSession] { sessions.filter(\.needsYou).sorted { ($0.attention?.openedAt ?? .distantPast) > ($1.attention?.openedAt ?? .distantPast) } }
    var hasMissingSource: Bool { !connected || sources.contains { $0.status == "unavailable" || $0.status == "error" || $0.status == "degraded" } || sessions.contains { $0.active && $0.connectivity != "online" } }
    func start() { streamTask?.cancel();streamTask=Task { while !Task.isCancelled { do {
        let data=try Data(contentsOf:dataDir.appendingPathComponent("collector.json"));let e=try JSON.decoder().decode(CollectorEndpoint.self,from:data);endpoint=e
        guard let url=URL(string:e.url+"/v1/events") else { throw URLError(.badURL) };var request=URLRequest(url:url);request.setValue("Bearer \(e.token)",forHTTPHeaderField:"Authorization");request.timeoutInterval=3600
        let (bytes,response)=try await network.bytes(for:request);guard (response as? HTTPURLResponse)?.statusCode==200 else {throw URLError(.userAuthenticationRequired)}
        for try await line in bytes.lines { if Task.isCancelled {return};if line.hasPrefix("data: "),let body=String(line.dropFirst(6)).data(using:.utf8) { let snapshot=try JSON.decoder().decode(Snapshot.self,from:body);accept(snapshot) } }
        throw URLError(.networkConnectionLost)
    } catch { if Task.isCancelled{return};connected=false;message="Collector unavailable. Showing last known sessions.";onUpdate?();launchCollectorIfNeeded();try? await Task.sleep(nanoseconds:2_000_000_000) } } } }
    func accept(_ snapshot: Snapshot) {
        if acceptedEndpointToken != endpoint?.token {acceptedEndpointToken=endpoint?.token;lastSequence = -1}
        guard snapshot.sequence>=lastSequence else{return}
        lastSequence=snapshot.sequence;snapshotRevision+=1
        sessions=snapshot.sessions;sources=snapshot.sources;connected=true;machine=snapshot.machine;message="Connected";onUpdate?()
        deliveredNotifications.formIntersection(Set(sessions.compactMap{$0.attention?.id}))
        if let data=try? JSON.encoder().encode(snapshot) {let file=dataDir.appendingPathComponent("ui-cache.json");try? data.write(to:file,options:.atomic);try? FileManager.default.setAttributes([.posixPermissions:0o600],ofItemAtPath:file.path)}
        checkNotifications()
    }
    func launchCollectorIfNeeded() {
        if process?.isRunning == true{return}
        guard let binary=Bundle.main.resourceURL?.appendingPathComponent("agents-collector"),FileManager.default.isExecutableFile(atPath:binary.path) else {message="Bundled collector is missing";return}
        let p=Process();p.executableURL=binary;p.arguments=["serve","--data-dir",dataDir.path,"--claude",findCLI("claude"),"--codex",findCLI("codex")]
        p.standardOutput=FileHandle.nullDevice;p.standardError=FileHandle.nullDevice
        do {try FileManager.default.createDirectory(at:dataDir,withIntermediateDirectories:true,attributes:[.posixPermissions:0o700]);try FileManager.default.setAttributes([.posixPermissions:0o700],ofItemAtPath:dataDir.path);try p.run();process=p} catch {message=error.localizedDescription}
    }
    func findCLI(_ name: String) -> String { let home=FileManager.default.homeDirectoryForCurrentUser.path;let candidates=["\(home)/.local/bin/\(name)","\(home)/.bun/bin/\(name)","/opt/homebrew/bin/\(name)","/usr/local/bin/\(name)"];return candidates.first {FileManager.default.isExecutableFile(atPath:$0)} ?? name }
    func acknowledge(_ s: AgentSession,_ action: String) {
        guard let e=s.attention,let endpoint=endpoint,let url=URL(string:endpoint.url+"/v1/attention") else{return}
        let marker=e.id+action;if seenPending.contains(marker){return};seenPending.insert(marker)
        let automatic=action=="seen" || action=="notified"
        if action=="notified"{deliveredNotifications.insert(e.id)}
        Task {
            defer {seenPending.remove(marker)}
            var req=URLRequest(url:url);req.httpMethod="POST";req.timeoutInterval=8
            req.setValue("Bearer \(endpoint.token)",forHTTPHeaderField:"Authorization")
            req.setValue("application/json",forHTTPHeaderField:"Content-Type")
            req.httpBody=try? JSONSerialization.data(withJSONObject:["sessionID":s.id,"episodeID":e.id,"action":action])
            do {
                let (_,res)=try await network.data(for:req)
                guard self.endpoint?.token==endpoint.token else{return}
                let status=(res as? HTTPURLResponse)?.statusCode ?? 0
                if status==200{return}
                if status==409 {
                    // The old episode is gone. Fetch current state without
                    // applying this acknowledgment to a different request.
                    await refreshSnapshot(from:endpoint)
                    if !automatic{actionError="That request has already changed, so the action wasn’t applied."}
                    return
                }
                if status==401 || status==403{start()}
                if !automatic{actionError="Could not apply that action. The collector returned an error (\(status))."}
            } catch {
                // Passive bookkeeping must never interrupt browsing. The
                // session stream already handles reconnects and offline UI.
                if !automatic{actionError="Could not apply that action because the collector is unavailable."}
            }
        }
    }
    private func refreshSnapshot(from endpoint:CollectorEndpoint) async {
        guard let url=URL(string:endpoint.url+"/v1/sessions") else{return}
        let revision=snapshotRevision
        var req=URLRequest(url:url);req.timeoutInterval=5;req.setValue("Bearer \(endpoint.token)",forHTTPHeaderField:"Authorization")
        do {
            let (data,response)=try await network.data(for:req)
            guard self.endpoint?.token==endpoint.token,(response as? HTTPURLResponse)?.statusCode==200 else{return}
            let snapshot=try JSON.decoder().decode(Snapshot.self,from:data)
            if snapshot.sequence==lastSequence && snapshotRevision != revision{return}
            accept(snapshot)
        } catch { /* The live stream will reconcile when the source reconnects. */ }
    }
    func saw(_ s: AgentSession) {if panelVisible && s.attention?.seen == false && (s.needsYou || expandedIDs.contains(s.id)) {acknowledge(s,"seen")}}
    func toggleExpansion(_ s:AgentSession) {if expandedIDs.contains(s.id){expandedIDs.remove(s.id)}else{expandedIDs.insert(s.id);saw(s)}}
    func reviewVisible(_ s:AgentSession)->Bool {s.isReview || (expandedIDs.contains(s.id) && s.attention?.kind=="review" && Date().timeIntervalSince(s.attention!.openedAt)<86400)}
    func reveal(_ s:AgentSession) {settings=false;search="";expandedIDs.insert(s.id);if s.older{showOlder=true};if !s.active && !reviewVisible(s){showIdle=true};revealID=s.id}
    func checkNotifications() {
        guard UserDefaults.standard.bool(forKey:"notificationsEnabled") else{return}
        let eligible=sessions.filter{s in guard let e=s.attention else{return false};return !e.seen && !e.notified && (e.kind != "review" || UserDefaults.standard.bool(forKey:"completionNotifications")) && Date().timeIntervalSince(e.openedAt)>=20 && Date().timeIntervalSince(e.openedAt)<7*86400 && e.snoozedUntil<Date() && s.connectivity=="online" && !notificationPending.contains(e.id) && !deliveredNotifications.contains(e.id)}
        if eligible.count>3 {
            let ids=eligible.compactMap{$0.attention?.id};ids.forEach{notificationPending.insert($0)}
            let c=UNMutableNotificationContent();c.title="\(eligible.count) agents have updates";c.body="Open Agents to review the requests and completed work.";c.categoryIdentifier="AGENT_SUMMARY";c.userInfo=["summary":true]
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier:"agents-summary",content:c,trigger:nil)){error in Task { @MainActor in ids.forEach{self.notificationPending.remove($0)};if error==nil{eligible.forEach{self.acknowledge($0,"notified")}} }};return
        }
        for s in eligible {guard let e=s.attention else {continue}
            notificationPending.insert(e.id);let c=UNMutableNotificationContent();c.title="\(UserDefaults.standard.bool(forKey:"notificationText") ? s.title:s.project) · \(s.status)";c.body=UserDefaults.standard.bool(forKey:"notificationText") ? s.summary : "\(s.provider.capitalized) · \(s.machine)";c.categoryIdentifier="AGENT_ATTENTION";c.userInfo=["sessionID":s.id,"episodeID":e.id];if UserDefaults.standard.bool(forKey:"notificationSound"){c.sound = .default}
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier:e.id,content:c,trigger:nil)) { error in Task { @MainActor in self.notificationPending.remove(e.id);if error==nil{self.acknowledge(s,"notified")} } }
        }
    }
    func openProject(_ s: AgentSession) {guard !s.remote,let cwd=s.cwd else{return};NSWorkspace.shared.open(URL(fileURLWithPath:cwd))}
    func revealTranscript(_ s: AgentSession) {guard !s.remote,let path=s.transcript else{return};NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath:path)])}
    func attach(_ s: AgentSession) {
        guard connected,s.connectivity=="online",!s.remote,s.provider=="claude",s.capabilities.contains("attach"),let id=s.attachID,id.range(of:"^[a-zA-Z0-9-]+$",options:.regularExpression) != nil else {actionError="This session cannot be attached from here.";return}
        let p=Process();p.executableURL=URL(fileURLWithPath:"/usr/bin/open");p.arguments=["-na","Ghostty","--args","-e",findCLI("claude"),"attach",id]
        do{try p.run()}catch{actionError=error.localizedDescription}
    }
    func installHooks(remove: Bool=false) {
        guard let binary=Bundle.main.resourceURL?.appendingPathComponent("agents-collector") else{return};let p=Process();p.executableURL=binary;p.arguments=[remove ? "hooks-remove":"hooks-install","--data-dir",dataDir.path,"--apply"]
        let pipe=Pipe();p.standardOutput=pipe;p.standardError=pipe;p.terminationHandler={ p in let data=pipe.fileHandleForReading.readDataToEndOfFile();let output=String(data:data,encoding:.utf8) ?? "";Task { @MainActor in self.actionError=p.terminationStatus==0 ? (remove ? "Agents hooks removed. Other hooks preserved.":"Hooks installed. Existing sessions remain covered by inventory; hook adoption depends on the provider.") : output }}
        do {try p.run()} catch{actionError=error.localizedDescription}
    }
    func pairHub(url:String,code:String) {
        guard let binary=Bundle.main.resourceURL?.appendingPathComponent("agents-collector") else{return}
        let p=Process();p.executableURL=binary;p.arguments=["enroll","--data-dir",dataDir.path,"--hub",url.trimmingCharacters(in:.whitespacesAndNewlines),"--machine",Host.current().localizedName ?? "Mac","--token-stdin"]
        let input=Pipe(),output=Pipe();p.standardInput=input;p.standardOutput=output;p.standardError=output
        p.terminationHandler={p in let text=String(data:output.fileHandleForReading.readDataToEndOfFile(),encoding:.utf8) ?? "";Task{@MainActor in self.actionError=p.terminationStatus==0 ? "Machine paired. Remote sessions will appear shortly.":text}}
        do{try p.run();input.fileHandleForWriting.write(Data(code.utf8));try input.fileHandleForWriting.close()}catch{actionError=error.localizedDescription}
    }
}
