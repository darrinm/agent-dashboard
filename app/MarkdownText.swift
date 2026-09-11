import Foundation
import SwiftUI

struct MarkdownBlock: Identifiable {
    let id: Int
    var text: AttributedString
    var heading: Int?
    var code = false
    var language: String?
    var quote = false
    var rule = false
    var listDepth = 0
    var marker: String?
    var table: Int?
    var tableRow = 0
    var tableHeader = false
}
struct MarkdownSection: Identifiable {
    var id: Int { blocks[0].id }
    var blocks: [MarkdownBlock]
    var rows: [[MarkdownBlock]] {
        var rows: [[MarkdownBlock]] = []
        for block in blocks {
            if rows.last?.first?.tableRow == block.tableRow { rows[rows.count-1].append(block) }
            else { rows.append([block]) }
        }
        return rows
    }
}

// Foundation parses the Markdown grammar; these blocks supply native layout
// for its presentation intents instead of showing the original delimiters.
// https://developer.apple.com/documentation/foundation/presentationintent
final class MarkdownDocument: NSObject {
    let sections: [MarkdownSection]
    let preview: AttributedString
    @MainActor private static let cache: NSCache<NSString, MarkdownDocument> = {
        let cache = NSCache<NSString, MarkdownDocument>()
        cache.countLimit = 256; cache.totalCostLimit = 512 * 1024
        return cache
    }()
    @MainActor static func parsed(_ source: String) -> MarkdownDocument {
        let key = source as NSString
        if let hit = cache.object(forKey: key) { return hit }
        let result = MarkdownDocument(source)
        cache.setObject(result, forKey: key, cost: source.utf8.count)
        return result
    }
    init(_ source: String) {
        let parsed = (try? AttributedString(markdown: source, options: .init(interpretedSyntax: .full, failurePolicy: .returnPartiallyParsedIfPossible))) ?? AttributedString(source)
        var blocks: [MarkdownBlock] = []
        var seenListItems = Set<Int>()
        for (intent, range) in parsed.runs[\.presentationIntent] {
            let components = intent?.components ?? []
            var text = AttributedString(parsed[range])
            text.presentationIntent = nil
            var block = MarkdownBlock(id: components.first?.identity ?? -blocks.count-1, text: text)
            var item: (Int, Int)?
            var ordered: Bool?
            for component in components {
                switch component.kind {
                case .header(let level): block.heading = level
                case .codeBlock(let language): block.code = true; block.language = language
                case .blockQuote: block.quote = true
                case .thematicBreak: block.rule = true
                case .listItem(let ordinal):
                    block.listDepth += 1
                    if item == nil { item = (component.identity, ordinal) }
                case .orderedList: if ordered == nil { ordered = true }
                case .unorderedList: if ordered == nil { ordered = false }
                case .table: block.table = component.identity
                case .tableHeaderRow: block.tableHeader = true
                case .tableRow(let row): block.tableRow = row
                default: break
                }
            }
            if let item = item, seenListItems.insert(item.0).inserted { block.marker = ordered == true ? "\(item.1)." : "•" }
            blocks.append(block)
        }
        var sections: [MarkdownSection] = []
        var preview = AttributedString()
        for block in blocks {
            if let table = block.table, sections.last?.blocks.first?.table == table { sections[sections.count-1].blocks.append(block) }
            else { sections.append(MarkdownSection(blocks: [block])) }
            if !block.rule {
                if !preview.characters.isEmpty { preview.append(AttributedString(" ")) }
                preview.append(block.text)
            }
        }
        // The collapsed row remains a single expand/collapse target.
        preview.link = nil
        self.sections = sections; self.preview = preview
    }
}

struct MarkdownText: View {
    let source: String
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(MarkdownDocument.parsed(source).sections) { section in
                if section.blocks.first?.table != nil {
                    VStack(alignment:.leading,spacing:8) {
                        ForEach(Array(section.rows.enumerated()), id: \.offset) { _, row in
                            HStack(alignment:.top,spacing:12) {
                                ForEach(row) { cell in
                                    Text(cell.text).fontWeight(cell.tableHeader ? .semibold : .regular)
                                        .frame(maxWidth:.infinity,alignment:.leading).fixedSize(horizontal:false,vertical:true)
                                }
                            }
                        }
                    }.padding(10).background(.primary.opacity(0.035), in: RoundedRectangle(cornerRadius: 8))
                } else if let block = section.blocks.first { MarkdownBlockView(block: block) }
            }
        }
        .font(.system(size: 13)).textSelection(.enabled)
        .frame(maxWidth: .infinity, alignment: .leading)
        .environment(\.openURL, OpenURLAction { url in
            ["https", "http", "mailto"].contains(url.scheme?.lowercased() ?? "") ? .systemAction : .discarded
        })
    }
}

private struct MarkdownBlockView: View {
    let block: MarkdownBlock
    var body: some View {
        if block.rule { Divider() }
        else if block.code {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    if let language = block.language { Text(language).font(.caption).foregroundStyle(.secondary) }
                    Spacer()
                    Button("Copy") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(String(block.text.characters), forType: .string)
                    }.controlSize(.mini).help("Copy this code block to the clipboard")
                }
                ScrollView(.horizontal) { Text(String(block.text.characters)).font(.system(size: 12, design: .monospaced)).fixedSize(horizontal: true, vertical: false) }.fixedSize(horizontal:false,vertical:true)
            }.padding(10).background(.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 8))
        } else {
            HStack(alignment: .top, spacing: 8) {
                if block.listDepth > 0 { Text(block.marker ?? "").monospacedDigit().frame(minWidth: 16, alignment: .trailing).foregroundStyle(.secondary) }
                Text(block.text)
                    .font(block.heading.map { .system(size: $0 == 1 ? 19 : $0 == 2 ? 16 : 14, weight: .semibold) } ?? .system(size: 13))
                    .frame(maxWidth: .infinity, alignment: .leading)
            }.fixedSize(horizontal: false, vertical: true)
                .padding(.leading,block.quote ? 10:0)
                .overlay(alignment:.leading){if block.quote{Rectangle().fill(.secondary.opacity(0.4)).frame(width:2)}}
                .padding(.leading, CGFloat(max(0, block.listDepth-1))*12)
                .padding(.top, block.heading == nil ? 0 : 4)
        }
    }
}
