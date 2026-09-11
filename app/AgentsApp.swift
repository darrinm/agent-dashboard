import AppKit
import SwiftUI
import Carbon
import UserNotifications
import Combine

@main struct AgentsMain {
    @MainActor static func main() {
        let app=NSApplication.shared
        if let i=CommandLine.arguments.firstIndex(of:"--render"),i+2<CommandLine.arguments.count {
            renderPanel(snapshotPath:CommandLine.arguments[i+1],outputPath:CommandLine.arguments[i+2]);return
        }
        if let i=CommandLine.arguments.firstIndex(of:"--render-help"),i+1<CommandLine.arguments.count {
            do{try capture(HelpView(),size:NSSize(width:880,height:680),outputPath:CommandLine.arguments[i+1])}catch{fputs("Render failed: \(error)\n",stderr);exit(1)};return
        }
        let delegate=AppDelegate();app.delegate=delegate;app.setActivationPolicy(.accessory);app.run()
        _ = delegate
    }
    @MainActor static func renderPanel(snapshotPath:String,outputPath:String) {
        do {
            let snapshot=try JSON.decoder().decode(Snapshot.self,from:Data(contentsOf:URL(fileURLWithPath:snapshotPath)))
            let model=AgentModel(dataDir:URL(fileURLWithPath:NSTemporaryDirectory()).appendingPathComponent("agents-render"));model.sessions=snapshot.sessions;model.sources=snapshot.sources;model.connected=true;model.machine=snapshot.machine
            if CommandLine.arguments.contains("--expand-first"),let first=model.sessions.first{model.reveal(first)}
            let content=PanelView(model:model,forceOpaque:true)
            let height=NSScreen.main.map{floor($0.visibleFrame.height * 0.75)} ?? 640
            try capture(content,size:NSSize(width:412,height:height),outputPath:outputPath)
        } catch {fputs("Render failed: \(error)\n",stderr);exit(1)}
    }
    @MainActor static func capture<V:View>(_ content:V,size:NSSize,outputPath:String) throws {
        let host=NSHostingView(rootView:content);host.frame=NSRect(origin:.zero,size:size)
        let window=NSWindow(contentRect:host.frame,styleMask:.borderless,backing:.buffered,defer:false);window.contentView=host
        window.setFrameOrigin(NSPoint(x:-10000,y:-10000));window.orderFront(nil)
        RunLoop.current.run(until:Date(timeIntervalSinceNow:0.15));host.layoutSubtreeIfNeeded()
        defer{window.orderOut(nil)}
        guard let rep=host.bitmapImageRepForCachingDisplay(in:host.bounds) else {throw NSError(domain:"Agents.Render",code:1)}
        host.cacheDisplay(in:host.bounds,to:rep)
        guard let png=rep.representation(using:.png,properties:[:]) else {throw NSError(domain:"Agents.Render",code:2)}
        try png.write(to:URL(fileURLWithPath:outputPath));print("Rendered \(outputPath)")
    }
}
final class AgentPanel:NSPanel {override var canBecomeKey:Bool {true}}
@MainActor final class AppDelegate:NSObject,NSApplicationDelegate,UNUserNotificationCenterDelegate {
    var item:NSStatusItem!
    var panel:NSPanel!
    var helpWindow:NSWindow?
    var model:AgentModel!
    var timer:Timer?
    var hotKey:EventHotKeyRef?
    var eventHandler:EventHandlerRef?
    var eventMonitor:Any?
    var blink=false
    var asleep=false
    var signature=""
    var slotIDs:[String]=[]
    var observers:[NSObjectProtocol]=[]
    func applicationDidFinishLaunching(_ notification:Notification) {
        UserDefaults.standard.register(defaults:["blinkingEnabled":true])
        let args=CommandLine.arguments;let path:URL
        if let i=args.firstIndex(of:"--data-dir"),i+1<args.count{path=URL(fileURLWithPath:args[i+1])}else{path=FileManager.default.urls(for:.applicationSupportDirectory,in:.userDomainMask)[0].appendingPathComponent("Agents")}
        model=AgentModel(dataDir:path)
        configureMenus()
        item=NSStatusBar.system.statusItem(withLength:61);item.button?.target=self;item.button?.action=#selector(togglePanel)
        panel=AgentPanel(contentRect:NSRect(x:0,y:0,width:412,height:600),styleMask:[.borderless,.nonactivatingPanel],backing:.buffered,defer:false)
        panel.isFloatingPanel=true;panel.level = .statusBar;panel.hasShadow=true;panel.backgroundColor = .clear;panel.isOpaque=false;panel.hidesOnDeactivate=false;panel.collectionBehavior=[.canJoinAllSpaces,.fullScreenAuxiliary];panel.isReleasedWhenClosed=false
        panel.contentView=NSHostingView(rootView:PanelView(model:model))
        model.onUpdate={ [weak self] in self?.updateGlyph() }
        registerHotKey()
        observers.append(NotificationCenter.default.addObserver(forName:Notification.Name("AgentsShortcutChanged"),object:nil,queue:.main){[weak self] _ in Task{@MainActor in self?.registerHotKey()}})
        observers.append(NotificationCenter.default.addObserver(forName:Notification.Name("AgentsClosePanel"),object:nil,queue:.main){[weak self] _ in Task{@MainActor in self?.closePanel()}})
        observers.append(NotificationCenter.default.addObserver(forName:Notification.Name("AgentsShowHelp"),object:nil,queue:.main){[weak self] _ in Task{@MainActor in self?.showHelp()}})
        eventMonitor=NSEvent.addGlobalMonitorForEvents(matching:[.leftMouseDown,.rightMouseDown]){ [weak self] _ in Task { @MainActor in self?.closePanel() } }
        for name in [NSWorkspace.screensDidSleepNotification,NSWorkspace.sessionDidResignActiveNotification] {observers.append(NSWorkspace.shared.notificationCenter.addObserver(forName:name,object:nil,queue:.main){ [weak self] _ in Task { @MainActor in self?.asleep=true;self?.updateGlyph() } })}
        for name in [NSWorkspace.screensDidWakeNotification,NSWorkspace.sessionDidBecomeActiveNotification] {observers.append(NSWorkspace.shared.notificationCenter.addObserver(forName:name,object:nil,queue:.main){ [weak self] _ in Task { @MainActor in self?.asleep=false;self?.updateGlyph() } })}
        UNUserNotificationCenter.current().delegate=self
        let category=UNNotificationCategory(identifier:"AGENT_ATTENTION",actions:[UNNotificationAction(identifier:"OPEN",title:"Open",options:.foreground),UNNotificationAction(identifier:"SNOOZE",title:"Snooze 1 hour")],intentIdentifiers:[])
        let summary=UNNotificationCategory(identifier:"AGENT_SUMMARY",actions:[UNNotificationAction(identifier:"OPEN",title:"Open",options:.foreground)],intentIdentifiers:[])
        UNUserNotificationCenter.current().setNotificationCategories([category,summary])
        timer=Timer.scheduledTimer(withTimeInterval:1.3,repeats:true){ [weak self] _ in Task { @MainActor in guard let self=self else{return};self.blink.toggle();self.updateGlyph();self.model.checkNotifications() } }
        updateGlyph();model.start()
        if args.contains("--show"){DispatchQueue.main.asyncAfter(deadline:.now()+1){self.showPanel()}}
        if args.contains("--help-window"){showHelp()}
    }
    func registerHotKey(){
        if let hotKey=hotKey{UnregisterEventHotKey(hotKey);self.hotKey=nil}
        if let eventHandler=eventHandler{RemoveEventHandler(eventHandler);self.eventHandler=nil}
        var type=EventTypeSpec(eventClass:OSType(kEventClassKeyboard),eventKind:UInt32(kEventHotKeyPressed))
        let ref=Unmanaged.passUnretained(self).toOpaque()
        InstallEventHandler(GetApplicationEventTarget(),{ _,_,userData in guard let ptr=userData else{return noErr};let app=Unmanaged<AppDelegate>.fromOpaque(ptr).takeUnretainedValue();Task { @MainActor in app.togglePanel() };return noErr },1,&type,ref,&eventHandler)
        let shortcut=UserDefaults.standard.string(forKey:"shortcut") ?? "optionSpace"
        let modifiers=shortcut=="controlOptionSpace" ? controlKey|optionKey:shortcut=="commandShiftSpace" ? cmdKey|shiftKey:optionKey
        let status=RegisterEventHotKey(UInt32(kVK_Space),UInt32(modifiers),EventHotKeyID(signature:0x41474E54,id:1),GetApplicationEventTarget(),0,&hotKey)
        if status != noErr{model.actionError="That shortcut is already in use. Choose another in Settings."}
    }
    @objc func togglePanel(){if panel.isVisible{closePanel()}else{showPanel()}}
    func showPanel(){
        guard let button=item.button else{return}
        let screen=button.window?.screen ?? NSScreen.screens.first(where:{$0.frame.contains(NSEvent.mouseLocation)}) ?? NSScreen.main!
        let visible=screen.visibleFrame
        let anchor=button.window?.convertToScreen(button.convert(button.bounds,to:nil)) ?? NSRect(x:visible.maxX-16,y:visible.maxY,width:0,height:0)
        let height=floor(visible.height * 0.75);let width:CGFloat=412
        let top=min(anchor.minY,visible.maxY)-2
        panel.setFrame(NSRect(x:max(visible.minX+8,min(anchor.maxX-width,visible.maxX-width-8)),y:max(visible.minY+8,top-height),width:width,height:height),display:true)
        model.panelVisible=true;panel.makeKeyAndOrderFront(nil);NSApp.activate(ignoringOtherApps:true);NotificationCenter.default.post(name:Notification.Name("AgentsPanelOpened"),object:nil)
    }
    func closePanel(){panel.orderOut(nil);model.panelVisible=false}
    @objc func showHelp(){
        closePanel()
        if helpWindow==nil {
            let window=NSWindow(contentRect:NSRect(x:0,y:0,width:880,height:680),styleMask:[.titled,.closable,.miniaturizable,.resizable],backing:.buffered,defer:false)
            window.title="Agents Setup & Help";window.minSize=NSSize(width:740,height:500);window.isReleasedWhenClosed=false
            window.contentView=NSHostingView(rootView:HelpView(openSettings:{[weak self] in guard let self=self else{return};self.model.settings=true;self.showPanel()},openDiagnostics:{[weak self] in guard let self=self else{return};NSWorkspace.shared.open(self.model.dataDir)}))
            window.center();window.setFrameAutosaveName("AgentsHelp");helpWindow=window
        }
        helpWindow?.makeKeyAndOrderFront(nil);NSApp.activate(ignoringOtherApps:true)
    }
    @objc func openSettings(){model.settings=true;showPanel()}
    func configureMenus(){
        let menu=NSMenu()
        let appMenu=NSMenu(title:"Agents")
        let appItem=NSMenuItem();appItem.submenu=appMenu;menu.addItem(appItem)
        appMenu.addItem(withTitle:"Settings…",action:#selector(openSettings),keyEquivalent:",").target=self
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle:"Quit Agents",action:#selector(NSApplication.terminate(_:)),keyEquivalent:"q")
        let edit=NSMenu(title:"Edit"),editItem=NSMenuItem();editItem.submenu=edit;menu.addItem(editItem)
        for (title,action,key) in [("Undo","undo:","z"),("Cut","cut:","x"),("Copy","copy:","c"),("Paste","paste:","v"),("Select All","selectAll:","a")] {edit.addItem(withTitle:title,action:Selector(action),keyEquivalent:key)}
        let windowMenu=NSMenu(title:"Window"),windowItem=NSMenuItem();windowItem.submenu=windowMenu;menu.addItem(windowItem)
        windowMenu.addItem(withTitle:"Close",action:#selector(NSWindow.performClose(_:)),keyEquivalent:"w")
        let help=NSMenu(title:"Help"),helpItem=NSMenuItem();helpItem.submenu=help;menu.addItem(helpItem)
        help.addItem(withTitle:"Agents Setup & Help",action:#selector(showHelp),keyEquivalent:"?").target=self
        NSApp.mainMenu=menu;NSApp.helpMenu=help;NSApp.windowsMenu=windowMenu
    }
    func applicationWillTerminate(_ notification:Notification){model?.streamTask?.cancel();model?.process?.terminate()}
    func updateGlyph(){
        guard let button=item.button,model != nil else{return}
        let active=model.sessions.filter(\.active).sorted{a,b in let ar=a.attention?.kind=="failure" ? 0:a.needsYou ? 1:2;let br=b.attention?.kind=="failure" ? 0:b.needsYou ? 1:2;return ar==br ? (a.attention?.openedAt ?? a.lastActivity) > (b.attention?.openedAt ?? b.lastActivity):ar<br}
        if Set(slotIDs) != Set(active.map(\.id)){slotIDs=active.map(\.id)}
        let ordered=slotIDs.compactMap{id in active.first{$0.id==id}}
        let count=model.needs.count
        let dark=button.effectiveAppearance.bestMatch(from:[.aqua,.darkAqua]) == .darkAqua
        let differentiate=NSWorkspace.shared.accessibilityDisplayShouldDifferentiateWithoutColor
        let blinkAllowed=UserDefaults.standard.bool(forKey:"blinkingEnabled") && !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion && !asleep
        let blinking=blinkAllowed&&ordered.contains{!$0.older && ($0.attention.map{!$0.seen && $0.kind != "review"} ?? false)}
        let phase=blinking&&blink
        let parts=ordered.prefix(9).map{"\($0.id):\($0.status):\($0.connectivity):\($0.attention?.seen ?? true)"}.joined(separator:"|")
        let sig="\(count)|\(dark)|\(differentiate)|\(phase)|\(model.hasMissingSource)|\(model.needs.contains{$0.attention?.kind=="failure" && $0.attention?.seen==false})|\(parts)"
        guard sig != signature else{return};signature=sig
        let needs=model.needs, connected=model.connected, missing=model.hasMissingSource
        let image=NSImage(size:NSSize(width:61,height:22),flipped:true){_ in
            let amber=dark ? NSColor(srgbRed:1,green:0.71,blue:0.28,alpha:1):NSColor(srgbRed:0.78,green:0.47,blue:0,alpha:1)
            let cyan=dark ? NSColor(srgbRed:0.38,green:0.89,blue:0.81,alpha:1):NSColor(srgbRed:0.06,green:0.60,blue:0.54,alpha:1)
            let red=NSColor.systemRed;let neutral=dark ? NSColor.white:NSColor.black
            let unseenFailure=needs.contains{$0.attention?.kind=="failure" && $0.attention?.seen==false}
            var countColor=count==0 ? neutral:unseenFailure ? red:amber
            if !connected || (!needs.isEmpty && needs.allSatisfy{$0.connectivity != "online"}){countColor=countColor.withAlphaComponent(0.45)}
            if count>0 {let text=count>99 ? "99+":"\(count)";let a=NSAttributedString(string:text,attributes:[.font:NSFont.monospacedDigitSystemFont(ofSize:13,weight:.semibold),.foregroundColor:countColor]);a.draw(at:NSPoint(x:29-a.size().width,y:3))}
            for index in 0..<9 {let rect=NSRect(x:36+CGFloat(index%3)*6,y:4+CGFloat(index/3)*6,width:4,height:4)
                if index==8 && (missing || ordered.count>9){neutral.setStroke();let path=NSBezierPath();path.lineWidth=1;path.move(to:NSPoint(x:rect.minX,y:rect.midY));path.line(to:NSPoint(x:rect.maxX,y:rect.midY));if !missing{path.move(to:NSPoint(x:rect.midX,y:rect.minY));path.line(to:NSPoint(x:rect.midX,y:rect.maxY))};path.stroke();continue}
                guard index<ordered.count else{neutral.withAlphaComponent(0.20).setFill();NSBezierPath(ovalIn:rect).fill();continue}
                let s=ordered[index];var color=s.attention?.kind=="failure" ? red:s.needsYou ? amber:cyan
                if s.connectivity != "online" || !connected {color=color.withAlphaComponent(0.35)}else if phase && !s.older && s.attention?.seen==false {color=color.withAlphaComponent(0.2)}
                color.setFill();color.setStroke();let shape=differentiate&&s.attention?.kind=="failure" ? NSBezierPath(rect:rect):NSBezierPath(ovalIn:rect)
                if differentiate && !s.needsYou {shape.lineWidth=1;shape.stroke()}else{shape.fill()}
            };return true
        }
        image.isTemplate=false;button.image=image;let label="\(count) sessions need you, \(active.filter{$0.attention?.kind=="failure"}.count) failed, \(active.filter{$0.execution=="working"}.count) working\(model.hasMissingSource ? ", some sources not reporting":"")";button.setAccessibilityLabel(label);button.toolTip="Open Agents · "+label
    }
    nonisolated func userNotificationCenter(_ center:UNUserNotificationCenter,didReceive response:UNNotificationResponse,withCompletionHandler completionHandler:@escaping ()->Void){Task { @MainActor in let info=response.notification.request.content.userInfo;if let id=info["sessionID"] as? String,let session=self.model.sessions.first(where:{$0.id==id}),info["episodeID"] as? String == session.attention?.id {if response.actionIdentifier=="SNOOZE"{self.model.acknowledge(session,"snooze")}else{self.model.reveal(session);self.model.acknowledge(session,"seen");self.showPanel()}}else{self.showPanel()};completionHandler() }}
}
