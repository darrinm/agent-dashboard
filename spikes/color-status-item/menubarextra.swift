// Does SwiftUI MenuBarExtra keep a colored label image?
import SwiftUI
import AppKit

func glyphImage() -> NSImage {
  let amber = NSColor(srgbRed: 1.0, green: 0.66, blue: 0.0, alpha: 1)
  let cyan = NSColor(srgbRed: 0.0, green: 0.90, blue: 0.80, alpha: 1)
  let red = NSColor(srgbRed: 1.0, green: 0.23, blue: 0.19, alpha: 1)
  let img = NSImage(size: NSSize(width: 40, height: 22), flipped: true) { _ in
    NSAttributedString(string: "6", attributes: [
      .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: amber,
    ]).draw(at: NSPoint(x: 5, y: 2))
    let colors = [red, amber, amber, amber, amber, amber, cyan, cyan, cyan]
    for (i, c) in colors.enumerated() {
      c.setFill()
      NSBezierPath(ovalIn: NSRect(x: 22 + CGFloat(i % 3) * 6, y: 5 + CGFloat(i / 3) * 6, width: 4, height: 4)).fill()
    }
    return true
  }
  img.isTemplate = false
  return img
}

@main
struct ColorLabelTest: App {
  var body: some Scene {
    MenuBarExtra {
      Text("plain Image(nsImage:)").padding()
    } label: {
      Image(nsImage: glyphImage())
    }
    .menuBarExtraStyle(.window)

    MenuBarExtra {
      Text("renderingMode(.original)").padding()
    } label: {
      Image(nsImage: glyphImage()).renderingMode(.original)
    }
    .menuBarExtraStyle(.window)
  }
}
