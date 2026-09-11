import Foundation

@main struct MarkdownTests {
    @MainActor static func main() {
        let source = """
        # Update

        **The name.** Agents supports *emphasis*, ~~old text~~, and `inline code`.

        Read [the docs](https://example.com/docs) or https://example.com/help.

        3. First item
        4. Second item
           - Nested item

        > Quoted text

        ```swift
        let value = "**literal**"
        ```

        | Source | Status |
        | --- | --- |
        | Claude | Working |

        ---
        """
        let document = MarkdownDocument(source)
        let blocks = document.sections.flatMap(\.blocks)
        precondition(blocks.first?.heading == 1)
        precondition(String(blocks.first!.text.characters) == "Update")
        precondition(blocks.contains { $0.text.runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true } })
        precondition(blocks.contains { $0.text.runs.contains { $0.inlinePresentationIntent?.contains(.code) == true } })
        precondition(blocks.contains { $0.text.runs.contains { $0.link?.absoluteString == "https://example.com/docs" } })
        precondition(blocks.contains { $0.text.runs.contains { $0.link?.absoluteString == "https://example.com/help" } })
        precondition(blocks.contains { $0.marker == "3." })
        precondition(blocks.contains { $0.marker == "4." })
        precondition(blocks.contains { $0.marker == "•" && $0.listDepth == 2 })
        precondition(blocks.contains { $0.quote && String($0.text.characters) == "Quoted text" })
        precondition(blocks.contains { $0.code && $0.language == "swift" && String($0.text.characters).contains("**literal**") })
        let table = document.sections.first { $0.blocks.first?.table != nil }!
        precondition(table.rows.count == 2 && table.rows.allSatisfy { $0.count == 2 })
        precondition(table.rows[0].allSatisfy(\.tableHeader))
        precondition(blocks.contains( where: \.rule))
        precondition(document.preview.runs.allSatisfy { $0.link == nil })
        precondition(String(document.preview.characters).contains("The name."))
        precondition(!String(document.preview.characters).contains("**The name.**"))
        precondition(!MarkdownDocument("A partial **message").sections.isEmpty)
        precondition(MarkdownDocument("").sections.isEmpty)
        let cached = MarkdownDocument.parsed(source)
        precondition(cached === MarkdownDocument.parsed(source))
        print("Markdown checks passed: inline styles, headings, links, nested lists, quotes, literal code, tables, partial input, previews, and caching.")
    }
}
