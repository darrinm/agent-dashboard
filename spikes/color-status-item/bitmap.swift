// Round 4: truly pre-rasterized frames. Does swapping the status item image really cost 5%?
// Modes: bitmap8 | bitmap1 | handler8 (drawing-handler image rebuilt each tick, for comparison)
import AppKit

let amber = NSColor(srgbRed: 1.0, green: 0.66, blue: 0.0, alpha: 1)
let cyan = NSColor(srgbRed: 0.0, green: 0.90, blue: 0.80, alpha: 1)
let red = NSColor(srgbRed: 1.0, green: 0.23, blue: 0.19, alpha: 1)
let colors = [red, amber, amber, amber, amber, amber, cyan, cyan, cyan]

func paint(dim: Int) {
  NSAttributedString(string: "6", attributes: [
    .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: amber,
  ]).draw(at: NSPoint(x: 5, y: 2))
  for (i, c) in colors.enumerated() {
    c.withAlphaComponent(i == dim ? 0.3 : 1).setFill()
    NSBezierPath(ovalIn: NSRect(x: 22 + CGFloat(i % 3) * 6, y: 5 + CGFloat(i / 3) * 6, width: 4, height: 4)).fill()
  }
}

// Rasterize once at 2x into a bitmap-backed NSImage.
func bitmapFrame(dim: Int) -> NSImage {
  let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 80, pixelsHigh: 44, bitsPerSample: 8,
                             samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                             colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
  rep.size = NSSize(width: 40, height: 22)
  NSGraphicsContext.saveGraphicsState()
  let ctx = NSGraphicsContext(bitmapImageRep: rep)!
  NSGraphicsContext.current = ctx
  ctx.cgContext.translateBy(x: 0, y: 22)
  ctx.cgContext.scaleBy(x: 1, y: -1)
  ctx.cgContext.scaleBy(x: 1, y: 1)
  paint(dim: dim)
  NSGraphicsContext.restoreGraphicsState()
  let img = NSImage(size: rep.size)
  img.addRepresentation(rep)
  img.isTemplate = false
  return img
}

func handlerFrame(dim: Int) -> NSImage {
  let img = NSImage(size: NSSize(width: 40, height: 22), flipped: true) { _ in paint(dim: dim); return true }
  img.isTemplate = false
  return img
}

final class AppDelegate: NSObject, NSApplicationDelegate {
  var item: NSStatusItem!
  var tick = 0
  let mode = CommandLine.arguments.dropFirst().first ?? "bitmap8"

  func applicationDidFinishLaunching(_ note: Notification) {
    item = NSStatusBar.system.statusItem(withLength: 44)
    let frames = (0..<9).map(bitmapFrame)
    item.button!.image = frames[0]
    let fps = mode.hasSuffix("8") ? 8.0 : 1.0
    Timer.scheduledTimer(withTimeInterval: 1.0 / fps, repeats: true) { [weak self] _ in
      guard let self else { return }
      self.tick += 1
      self.item.button!.image = self.mode.hasPrefix("bitmap") ? frames[self.tick % 9] : handlerFrame(dim: self.tick % 9)
    }
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
