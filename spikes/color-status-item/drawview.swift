// Round 5: draw in a custom view and mark it dirty, instead of replacing the button image.
// Modes: view8 | view1 | novib8 (same view with allowsVibrancy = false)
import AppKit

let amber = NSColor(srgbRed: 1.0, green: 0.66, blue: 0.0, alpha: 1)
let cyan = NSColor(srgbRed: 0.0, green: 0.90, blue: 0.80, alpha: 1)
let red = NSColor(srgbRed: 1.0, green: 0.23, blue: 0.19, alpha: 1)
let colors = [red, amber, amber, amber, amber, amber, cyan, cyan, cyan]
let countText = NSAttributedString(string: "6", attributes: [
  .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: amber,
])

class GlyphView: NSView {
  var dim = 0
  override var isFlipped: Bool { true }
  override func draw(_ dirtyRect: NSRect) {
    countText.draw(at: NSPoint(x: 5, y: 2))
    for (i, c) in colors.enumerated() {
      c.withAlphaComponent(i == dim ? 0.3 : 1).setFill()
      NSBezierPath(ovalIn: NSRect(x: 22 + CGFloat(i % 3) * 6, y: 5 + CGFloat(i / 3) * 6, width: 4, height: 4)).fill()
    }
  }
}
final class NoVibGlyphView: GlyphView { override var allowsVibrancy: Bool { false } }

final class AppDelegate: NSObject, NSApplicationDelegate {
  var item: NSStatusItem!
  let mode = CommandLine.arguments.dropFirst().first ?? "view8"

  func applicationDidFinishLaunching(_ note: Notification) {
    item = NSStatusBar.system.statusItem(withLength: 44)
    let button = item.button!
    let view: GlyphView = mode.hasPrefix("novib") ? NoVibGlyphView(frame: button.bounds) : GlyphView(frame: button.bounds)
    view.autoresizingMask = [.width, .height]
    button.addSubview(view)
    let fps = mode.hasSuffix("8") ? 8.0 : 1.0
    Timer.scheduledTimer(withTimeInterval: 1.0 / fps, repeats: true) { _ in
      view.dim = (view.dim + 1) % 9
      view.needsDisplay = true
    }
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
