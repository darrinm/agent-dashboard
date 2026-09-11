import AppKit
import SwiftUI

struct GuideTopic: Decodable, Identifiable {
    let id: String
    let title: String
    let subtitle: String
    let symbol: String
    let blocks: [GuideBlock]
}
struct GuideBlock: Decodable {
    let kind: String
    let text: String
}

struct HelpView: View {
    let topics: [GuideTopic]
    let openSettings: () -> Void
    let openDiagnostics: () -> Void
    @State private var selection: String? = "start"
    @State private var query = ""
    @AppStorage("appearance") private var appearance = "system"

    init(openSettings: @escaping () -> Void = {}, openDiagnostics: @escaping () -> Void = {}) {
        self.openSettings = openSettings
        self.openDiagnostics = openDiagnostics
        if let url = Bundle.main.url(forResource: "setup-guide", withExtension: "json"),
           let data = try? Data(contentsOf: url),
           let topics = try? JSONDecoder().decode([GuideTopic].self, from: data) {
            self.topics = topics
        } else { self.topics = [] }
    }
    private var matches: [GuideTopic] {
        topics.filter { query.isEmpty || ($0.title + " " + $0.subtitle + " " + $0.blocks.map(\.text).joined(separator: " ")).localizedCaseInsensitiveContains(query) }
    }
    var body: some View {
        HStack(spacing:0) {
            VStack(alignment:.leading,spacing:16) {
                Text("AGENTS HELP").font(.system(size:11,weight:.semibold,design:.monospaced)).tracking(2).foregroundStyle(.secondary)
                HStack {Image(systemName:"magnifyingglass").foregroundStyle(.secondary);TextField("Search help",text:$query).textFieldStyle(.plain)}
                    .padding(9).background(.primary.opacity(0.05),in:RoundedRectangle(cornerRadius:8))
                ScrollView {
                    VStack(spacing:4) {
                        ForEach(matches) { topic in
                            Button {selection=topic.id} label:{
                                Label(topic.title,systemImage:topic.symbol).font(.system(size:13))
                                    .frame(maxWidth:.infinity,alignment:.leading).padding(10)
                                    .background(selection==topic.id ? Palette.activity.opacity(0.12):.clear,in:RoundedRectangle(cornerRadius:8))
                                    .contentShape(Rectangle())
                            }.buttonStyle(.plain).accessibilityAddTraits(selection==topic.id ? .isSelected:[]).help("Read "+topic.title.lowercased())
                        }
                        if matches.isEmpty {Text("No matching topics").font(.callout).foregroundStyle(.secondary).padding(.top,12)}
                    }
                }
            }.padding(16).frame(width:220).background(Color(nsColor:.underPageBackgroundColor))
            Divider()
            if let topic = topics.first(where: { $0.id == selection }) {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        Image(systemName: topic.symbol).font(.system(size: 28)).foregroundStyle(Palette.activity)
                        Text(topic.title).font(.largeTitle.weight(.semibold))
                        Text(topic.subtitle).font(.title3).foregroundStyle(.secondary)
                        Divider().padding(.vertical, 4)
                        ForEach(Array(topic.blocks.enumerated()), id: \.offset) { _, block in
                            switch block.kind {
                            case "heading": Text(block.text).font(.headline).padding(.top, 10)
                            case "code": GuideCode(text: block.text).id(block.text)
                            case "note": Label { Text(.init(block.text)) } icon: { Image(systemName: "info.circle") }
                                .font(.callout).foregroundStyle(.secondary).padding(14)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(.primary.opacity(0.045), in: RoundedRectangle(cornerRadius: 10))
                            default: Text(.init(block.text)).textSelection(.enabled).lineSpacing(4)
                            }
                        }
                        if topic.id == "start" || topic.id == "hooks" || topic.id == "notifications" || topic.id == "remote" {
                            Button("Open Agents Settings", action: openSettings).help("Open settings in the menu bar panel").padding(.top, 8)
                        }
                        if topic.id == "troubleshooting" {
                            Button("Open diagnostic data folder", action: openDiagnostics).help("Open Agents’ private local data folder in Finder")
                        }
                    }
                    .frame(maxWidth: 640, alignment: .leading)
                    .padding(32)
                    .frame(maxWidth: .infinity, alignment: .topLeading)
                }.background(Color(nsColor:.windowBackgroundColor)).id(topic.id)
            } else {
                ContentUnavailableView("Setup & Help", systemImage: "questionmark.circle", description: Text(topics.isEmpty ? "The bundled guide is missing. Reinstall Agents from its original download." : "Choose a topic in the sidebar."))
            }
        }
        .preferredColorScheme(appearance == "dark" ? .dark : appearance == "light" ? .light : nil)
    }
}

private struct GuideCode: View {
    let text: String
    @State private var copied = false
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("TERMINAL").font(.caption2.weight(.semibold)).tracking(1).foregroundStyle(.secondary)
                Spacer()
                Button(copied ? "Copied" : "Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(text, forType: .string)
                    copied = true
                }.controlSize(.small).accessibilityLabel("Copy terminal command").help("Copy this command to the clipboard; it will not run automatically")
            }
            Text(text).font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(14).background(.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 10))
    }
}
