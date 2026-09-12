import SwiftUI
import AppKit
import ServiceManagement
import UserNotifications

struct PanelView: View {
    @ObservedObject var model: AgentModel
    var forceOpaque=false
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @FocusState private var searchFocused: Bool
    @AppStorage("shortcut") private var shortcut="optionSpace"
    @AppStorage("appearance") private var appearance="system"
    @State private var keyboardID:String?
    @State private var lastReturn=Date.distantPast
    var body: some View {
        VStack(spacing:0) {
            header
            if model.settings { settings } else { list }
            Divider().opacity(0.6)
            HStack(spacing:8) {Image(systemName:"magnifyingglass").foregroundStyle(.secondary);TextField("Jump to session",text:$model.search).textFieldStyle(.plain).focused($searchFocused).onChange(of:model.search){ _,query in keyboardID=nil;if !query.isEmpty{model.showOlder=true;model.showIdle=true} }.onSubmit{_=returnPressed()};Text(shortcut=="controlOptionSpace" ? "⌃⌥ Space":shortcut=="commandShiftSpace" ? "⇧⌘ Space":"⌥ Space").font(.system(size:10,design:.monospaced)).foregroundStyle(.secondary)}.padding(14)
        }
        .frame(width:412)
        .frame(maxHeight:.infinity,alignment:.top)
        .background { if reduceTransparency || forceOpaque {Color(nsColor:.windowBackgroundColor)} else {VisualEffect()} }
        .clipShape(RoundedRectangle(cornerRadius:20))
        .preferredColorScheme(appearance=="dark" ? .dark:appearance=="light" ? .light:nil)
        .overlay(RoundedRectangle(cornerRadius:20).strokeBorder(.white.opacity(0.13),lineWidth:1))
        .onAppear{searchFocused=true} // The view tree is rebuilt each time the panel opens.
        // Return can arrive here or through the search field's onSubmit, depending on focus.
        .onKeyPress(.return){ returnPressed() ? .handled:.ignored }
        .onKeyPress(keys:[.upArrow,.downArrow]){press in
            let rows=navigationRows
            guard !model.settings,!rows.isEmpty else{return .ignored}
            let current=rows.firstIndex(where:{$0.id==keyboardID}) ?? (press.key == .downArrow ? -1:rows.count)
            let next=max(0,min(rows.count-1,current+(press.key == .downArrow ? 1:-1)))
            keyboardID=rows[next].id;return .handled
        }
        .onExitCommand { if !model.expandedIDs.isEmpty {model.expandedIDs.removeAll()} else {NotificationCenter.default.post(name:Notification.Name("AgentsClosePanel"),object:nil)} }
        .alert("Agents",isPresented:Binding(get:{model.actionError != nil},set:{if !$0{model.actionError=nil}})){Button("OK"){model.actionError=nil}.help("Dismiss this message")} message:{Text(model.actionError ?? "")}
    }
    /// Toggles the keyboard selection, or the first row when nothing is selected.
    /// A single key press can reach both handlers, so repeats within 0.2 s are ignored.
    private func returnPressed()->Bool {
        let rows=navigationRows
        guard !model.settings,let s=rows.first(where:{$0.id==keyboardID}) ?? rows.first else{return false}
        if Date().timeIntervalSince(lastReturn)<0.2 {return true}
        lastReturn=Date();keyboardID=s.id;model.toggleExpansion(s);return true
    }
    private var header: some View {
        VStack(alignment:.leading,spacing:14) {
            HStack(spacing:14) {Text("AGENTS").font(.system(size:12,weight:.semibold,design:.monospaced)).tracking(3);Spacer();Button {NotificationCenter.default.post(name:Notification.Name("AgentsShowHelp"),object:nil)} label:{Image(systemName:"questionmark.circle")}.buttonStyle(.plain).accessibilityLabel("Setup & Help").help("Open setup instructions and help");Button {model.settings.toggle()} label:{Image(systemName:"gearshape")}.buttonStyle(.plain).accessibilityLabel("Settings").help(model.settings ? "Return to your sessions":"Open appearance, notification, source, and connection settings")}
            HStack(alignment:.center,spacing:14){Text(model.needs.isEmpty ? "—":"\(model.needs.count)").font(.system(size:48,weight:.light,design:.monospaced)).foregroundStyle(model.needs.isEmpty ? .secondary:Palette.amber).contentTransition(.numericText());VStack(alignment:.leading,spacing:3){Text(model.needs.isEmpty ? "All clear":"need your attention").font(.system(size:16,weight:.semibold));let count=Set(model.sessions.map(\.machineID)).count;Text(model.connected ? "\(model.sessions.filter(\.active).count) active · \(count) \(count==1 ? "machine":"machines")" : "Last known state").font(.system(size:12)).foregroundStyle(.secondary)};Spacer()}
            if !model.connected {Label(model.message,systemImage:"wifi.slash").font(.system(size:11)).foregroundStyle(Palette.amber)}
            if let issue=model.sources.first(where:{$0.status=="unavailable"||$0.status=="error"||$0.status=="degraded"}) {Label(issue.name+(issue.status=="degraded" ? " · limited coverage":" not reporting"),systemImage:"exclamationmark.circle").font(.system(size:11)).foregroundStyle(Palette.amber)}
        }.padding(20)
    }
    // Nested subagents appear as a count on their parent row, and still match a search.
    private var filtered:[AgentSession] {model.sessions.filter {s in model.search.isEmpty ? !model.nestedIDs.contains(s.id) : "\(s.title) \(s.project) \(s.machine) \(s.provider)".localizedCaseInsensitiveContains(model.search)}}
    private var navigationRows:[AgentSession] {
        let sessions=filtered
        let folded=Set(partitionRequests(sessions.filter(\.needsYou)).folded.map(\.id))
        return sessions.filter{s in s.needsYou ? (!folded.contains(s.id) || model.showOlder) : (s.execution=="working" || model.reviewVisible(s) || model.showIdle)}
    }
    private var list: some View {
        ScrollViewReader{proxy in
        ScrollView {
            LazyVStack(spacing:4) {
                let sessions=filtered // Filtering runs a text match per session; do it once per update.
                if model.sessions.isEmpty {ContentUnavailableView {Label("No sessions yet",systemImage:"antenna.radiowaves.left.and.right")} description:{Text("Start Claude Code or Codex using your usual workflow. Sources appear here automatically.")} actions:{Button("Set up Agents"){NotificationCenter.default.post(name:Notification.Name("AgentsShowHelp"),object:nil)}.help("Open the getting-started guide")}.padding(.vertical,20)}
                let needs=sessions.filter(\.needsYou)
                if !needs.isEmpty { sectionLabel("NEEDS YOU",count:needs.count,tint:Palette.amber)
                    let requests=partitionRequests(needs)
                    ForEach(requests.shown) { row($0) }
                    let older=requests.folded
                    if !older.isEmpty {DisclosureGroup(isExpanded:$model.showOlder){ForEach(older){row($0)}} label:{HStack{Text("Older requests");Spacer();Text("\(older.count) more · over 7 days").foregroundStyle(.secondary)}.font(.system(size:12)).padding(.vertical,8)}.disclosureGroupStyle(FullRowDisclosureStyle()).padding(.horizontal,12)}
                }
                let working=sessions.filter{!$0.needsYou&&$0.execution=="working"}
                if !working.isEmpty {sectionLabel("WORKING",count:working.count,tint:Palette.activity);ForEach(working){row($0)}}
                let reviews=sessions.filter{!$0.needsYou&&$0.execution != "working"&&model.reviewVisible($0)}
                if !reviews.isEmpty {sectionLabel("READY TO REVIEW",count:reviews.count);ForEach(reviews){row($0)}}
                let idle=sessions.filter{!$0.needsYou&&$0.execution != "working" && !model.reviewVisible($0)}
                if !idle.isEmpty {DisclosureGroup(isExpanded:$model.showIdle){ForEach(idle){row($0)}} label:{Text("Idle and disconnected · \(idle.count)").font(.system(size:12)).foregroundStyle(.secondary).padding(.vertical,10)}.disclosureGroupStyle(FullRowDisclosureStyle()).padding(.horizontal,12)}
                if sessions.isEmpty && !model.sessions.isEmpty{Text("No matching sessions").foregroundStyle(.secondary).padding(24)}
            }.padding(.horizontal,8).padding(.bottom,12)
        }.frame(maxHeight:.infinity).onChange(of:keyboardID){_,id in if let id=id{proxy.scrollTo(id,anchor:.center)}}
            .onChange(of:model.revealID){_,id in if let id=id{DispatchQueue.main.async{proxy.scrollTo(id,anchor:.top)}}}
            .onAppear{if let id=model.revealID{DispatchQueue.main.async{proxy.scrollTo(id,anchor:.top)}}}
        }
    }
    private func sectionLabel(_ title:String,count:Int,tint:Color = .secondary)->some View {HStack{Text(title).tracking(1.4);Spacer();Text(String(format:"%02d",count)).monospacedDigit()}.font(.system(size:10,weight:.semibold)).foregroundStyle(tint).padding(.horizontal,12).padding(.top,10).padding(.bottom,5)}
    private func row(_ s:AgentSession)->some View {
        VStack(spacing:0) {
        Button {keyboardID=s.id;searchFocused=true;model.toggleExpansion(s)} label:{
            HStack(alignment:.top,spacing:10) {
                Image(systemName:s.symbol).font(.system(size:12)).foregroundStyle(s.color).frame(width:16).padding(.top,3)
                VStack(alignment:.leading,spacing:5) {
                    HStack(alignment:.firstTextBaseline){Text(s.title).font(.system(size:13,weight:.medium)).lineLimit(model.expandedIDs.contains(s.id) ? nil:1);Spacer(minLength:4);Text(s.provider=="claude" ? "Claude":"Codex").font(.system(size:10,weight:.medium)).foregroundStyle(.secondary);Image(systemName:model.expandedIDs.contains(s.id) ? "chevron.up":"chevron.down").font(.system(size:9,weight:.semibold)).foregroundStyle(.tertiary)}
                    let subagents=model.subagentCounts[s.id] ?? 0
                    Text("\(s.project) · \(s.machine == model.machine ? "This Mac":s.machine)\(s.parentID == nil ? "":" · subagent")\(subagents>0 ? " · \(subagents) subagent\(subagents==1 ? "":"s")":"")").font(.system(size:11)).foregroundStyle(.secondary).lineLimit(1)
                    if !model.expandedIDs.contains(s.id) && !s.summary.isEmpty{Text(MarkdownDocument.parsed(s.summary).preview).font(.system(size:12)).foregroundStyle(s.needsYou ? s.color:.secondary).lineLimit(2).multilineTextAlignment(.leading)}
                    HStack {Text(s.connectivity=="online" ? (s.needsYou ? "Waiting \(age(s.attention!.openedAt))":s.status):"\(s.connectivity.capitalized) · last seen \(age(s.observedAt)) ago").font(.system(size:10)).foregroundStyle(.secondary);Spacer();if s.execution=="working" && !s.needsYou {if s.hasActivity {ActivityLine(buckets:s.buckets).frame(width:85,height:20)} else {Text("No activity data").font(.system(size:10)).foregroundStyle(.secondary)}}}
                }
            }.padding(11).background(s.needsYou ? s.color.opacity(0.065):Color.clear,in:RoundedRectangle(cornerRadius:11)).contentShape(Rectangle())
        }.buttonStyle(.plain).accessibilityValue(model.expandedIDs.contains(s.id) ? "Expanded":"Collapsed").accessibilityHint("Expand or collapse session details").help(model.expandedIDs.contains(s.id) ? "Collapse this session’s message and actions":"Expand this session’s message and actions")
        if model.expandedIDs.contains(s.id) {inlineDetail(s)}
        }.padding(.vertical,1).background(keyboardID==s.id ? Color.accentColor.opacity(0.12):.clear,in:RoundedRectangle(cornerRadius:11)).id(s.id).modifier(VisibleAcknowledgement(model:model,session:s)).accessibilityLabel("\(s.title), \(s.provider), \(s.status), \(s.machine)")
    }
    private func inlineDetail(_ s:AgentSession)->some View {
        VStack(alignment:.leading,spacing:12){
            Divider()
            if s.needsYou {Label(s.status,systemImage:s.symbol).foregroundStyle(s.color).font(.system(size:13,weight:.medium))}
            MarkdownText(source:s.summary.isEmpty ? "No message available from this source.":s.summary).padding(14).background(.primary.opacity(0.045),in:RoundedRectangle(cornerRadius:12))
            if s.execution=="working"&&s.openTools==0&&Date().timeIntervalSince(s.lastActivity)>600{Text("Quiet \(age(s.lastActivity)) · no open tool call observed").font(.system(size:12)).foregroundStyle(.secondary)}
            HStack {if s.capabilities.contains("attach"){Button("Attach in Ghostty"){model.attach(s)}.disabled(!model.connected||s.connectivity != "online").help(!model.connected || s.connectivity != "online" ? "Attach is unavailable while this session’s source is offline":"Attach to the existing Claude session in Ghostty")};if !s.remote,s.cwd != nil {Button("Open project"){model.openProject(s)}.help("Open this session’s project folder in Finder")}}
            HStack {if !s.remote,s.transcript != nil{Button("Reveal transcript"){model.revealTranscript(s)}.help("Show this session’s transcript file in Finder")};Button("Copy session ID"){NSPasteboard.general.clearContents();NSPasteboard.general.setString(s.nativeID,forType:.string)}.help("Copy the provider’s session ID to the clipboard")}.controlSize(.small)
            HStack {
                if s.needsYou{Button("Snooze notifications for 1 hour"){model.acknowledge(s,"snooze")}.help("Pause notifications for this request for one hour; the request stays pending")}
                Menu("Wrong state?") {
                    if s.needsYou {Button("It isn’t waiting for me"){model.reportWrongState(s,verdict:"not_waiting")}}
                    else {Button("It is waiting for me"){model.reportWrongState(s,verdict:"is_waiting")}}
                    Button("Something else is wrong"){model.reportWrongState(s,verdict:"wrong_state")}
                }.menuStyle(.borderlessButton).fixedSize().help("Record this session’s shown state and evidence so wrong states can be reviewed")
            }.controlSize(.small)
            if s.evidence=="inferred"{Label("Based on saved activity; live status may differ.",systemImage:"info.circle").font(.system(size:11)).foregroundStyle(.secondary).help("Agents reads recorded session activity. It may not see a newer live state or a pending approval until the source records it.")}
            Text("Last activity \(age(s.lastActivity)) ago · \(s.connectivity)").font(.system(size:11)).foregroundStyle(.secondary)
        }.padding(.leading,37).padding(.trailing,11).padding(.bottom,16).onAppear{model.saw(s)}
    }
    private var settings:some View {
        ScrollView{VStack(alignment:.leading,spacing:18){
            Text("Settings & sources").font(.headline)
            Button {NotificationCenter.default.post(name:Notification.Name("AgentsShowHelp"),object:nil)} label:{Label("Setup & Help",systemImage:"book.closed")}.help("Open the setup and troubleshooting guide")
            PreferencesView(model:model)
            Divider()
            ForEach(model.sources){s in VStack(alignment:.leading,spacing:4){HStack{Circle().fill(s.status=="online" ? Palette.activity:.secondary).frame(width:6,height:6);Text(s.name).font(.system(size:12,weight:.medium));Spacer();Text(s.status).font(.system(size:10)).foregroundStyle(.secondary)};Text(s.detail).font(.system(size:11)).foregroundStyle(.secondary)}}
            HStack{Button("Restart collector"){model.process?.terminate();model.process=nil;model.launchCollectorIfNeeded();model.start()}.help("Restart the app-managed collector and reconnect to local sessions");Button("Quit Agents"){NSApp.terminate(nil)}.help("Quit Agents and the collector it started")}.controlSize(.small)
        }.padding(20)}.frame(maxHeight:.infinity)
    }
}
struct PreferencesView: View {
    @ObservedObject var model:AgentModel
    @AppStorage("notificationsEnabled") var notifications=false
    @AppStorage("notificationText") var text=false
    @AppStorage("notificationSound") var sound=false
    @AppStorage("completionNotifications") var completion=false
    @AppStorage("blinkingEnabled") var blinking=true
    @AppStorage("shortcut") var shortcut="optionSpace"
    @AppStorage("appearance") var appearance="system"
    @State private var confirmHooks=false
    @State private var login=SMAppService.mainApp.status == .enabled
    @AppStorage("hubURL") private var hubURL=""
    @State private var pairingCode=""
    var body:some View {VStack(alignment:.leading,spacing:12){
        Picker("Appearance",selection:$appearance){Text("System").tag("system");Text("Dark").tag("dark");Text("Light").tag("light")}.help("Choose a light, dark, or system appearance")
        Picker("Shortcut",selection:$shortcut){Text("⌥ Space").tag("optionSpace");Text("⌃⌥ Space").tag("controlOptionSpace");Text("⇧⌘ Space").tag("commandShiftSpace")}.onChange(of:shortcut){_,_ in NotificationCenter.default.post(name:Notification.Name("AgentsShortcutChanged"),object:nil)}.help("Choose the global keyboard shortcut that opens the panel")
        Toggle("Notify when an agent needs me",isOn:$notifications).onChange(of:notifications){_,value in if value{UNUserNotificationCenter.current().requestAuthorization(options:[.alert,.sound]){_,_ in}}}.help("Send a notification when an agent needs your attention")
        Toggle("Include message text",isOn:$text).help("Include session titles and message excerpts in notifications");Toggle("Play notification sounds",isOn:$sound).help("Play a sound when an individual notification arrives");Toggle("Notify on completed turns",isOn:$completion).help("Also notify when a turn finishes and is ready to review");Toggle("Blink unseen requests",isOn:$blinking).help("Blink new, unseen requests in the menu bar; Reduce Motion disables blinking")
        Toggle("Launch at login",isOn:$login).onChange(of:login){_,value in do{if value{try SMAppService.mainApp.register()}else{try SMAppService.mainApp.unregister()}}catch{model.actionError=error.localizedDescription;login=SMAppService.mainApp.status == .enabled}}.help("Start Agents automatically when you sign in to this Mac")
        Button("Enable live lifecycle hooks…"){confirmHooks=true}.help("Review and enable hooks for faster lifecycle updates").confirmationDialog("Add Agents hooks to Claude settings and wrap Codex's existing notify command? Existing handlers and arguments are preserved.",isPresented:$confirmHooks){Button("Enable hooks"){model.installHooks()}.help("Install Agents lifecycle hooks while preserving existing handlers")}
        Button("Remove Agents hooks"){model.installHooks(remove:true)}.controlSize(.small).help("Remove only Agents-owned hooks and restore the previous Codex notify command")
        Divider()
        Text("Other machines").font(.system(size:12,weight:.semibold))
        Text("Connect this Mac to your hub using a single-use pairing code.").foregroundStyle(.secondary)
        TextField("https://your-hub.example.com",text:$hubURL).textFieldStyle(.roundedBorder)
        SecureField("Pairing code",text:$pairingCode).textFieldStyle(.roundedBorder)
        Button("Connect to hub"){model.pairHub(url:hubURL,code:pairingCode);pairingCode=""}.disabled(hubURL.isEmpty || pairingCode.isEmpty).help(hubURL.isEmpty || pairingCode.isEmpty ? "Enter your hub URL and a fresh pairing code to connect":"Pair this Mac with your hub using the single-use code")
    }.font(.system(size:12))}
}
struct VisibleAcknowledgement: ViewModifier {
    @ObservedObject var model:AgentModel
    let session:AgentSession
    @State private var visible=false
    func body(content:Content)->some View {content.onScrollVisibilityChange(threshold:0.5){value in visible=value;if value{model.saw(session)}}.onChange(of:model.panelVisible){_,value in if value&&visible{model.saw(session)}}.onChange(of:session.attention?.id){_,_ in if visible{model.saw(session)}}}
}
struct FullRowDisclosureStyle: DisclosureGroupStyle {
    func makeBody(configuration:Configuration)->some View {
        VStack(alignment:.leading,spacing:4) {
            Button {configuration.isExpanded.toggle()} label:{
                HStack(spacing:8) {
                    Image(systemName:configuration.isExpanded ? "chevron.down":"chevron.right").font(.system(size:11,weight:.semibold)).frame(width:12)
                    configuration.label.frame(maxWidth:.infinity,alignment:.leading)
                }.contentShape(Rectangle())
            }.buttonStyle(.plain).accessibilityValue(configuration.isExpanded ? "Expanded":"Collapsed").help(configuration.isExpanded ? "Collapse this group":"Expand the sessions in this group")
            if configuration.isExpanded {configuration.content}
        }
    }
}
struct ActivityLine: View {
    let buckets:[ActivityBucket]
    var body:some View {Canvas{context,size in guard buckets.count>1 else{return};let peak=Double(max(500,buckets.map(\.tokens).max() ?? 0));var path=Path();for (i,b) in buckets.enumerated(){let x=Double(i)*size.width/Double(buckets.count-1);let y=size.height-2-Double(b.tokens)/peak*(size.height-4);if i==0{path.move(to:CGPoint(x:x,y:y))}else{path.addLine(to:CGPoint(x:x,y:y))};if b.tool {var span=Path();span.move(to:CGPoint(x:x,y:size.height-1));span.addLine(to:CGPoint(x:min(size.width,x+size.width/30),y:size.height-1));context.stroke(span,with:.color(.secondary),style:StrokeStyle(lineWidth:1,dash:[1,2]))}};context.stroke(path,with:.color(Palette.activity),lineWidth:1.3)}.accessibilityLabel("\(buckets.reduce(0){$0+$1.tokens}) output tokens in the last 15 minutes")}
}
struct VisualEffect:NSViewRepresentable {
    func makeNSView(context:Context)->NSVisualEffectView{let v=NSVisualEffectView();v.material = .popover;v.blendingMode = .behindWindow;v.state = .active;return v}
    func updateNSView(_ view:NSVisualEffectView,context:Context){}
}
