// Round 3: which image-based treatments keep color on the active display, and what animation costs.
import AppKit
import QuartzCore

enum Kind { case fault, wait, work }
let kinds: [Kind] = [.fault, .wait, .wait, .wait, .wait, .wait, .work, .work, .work]

struct Palette { let amber, cyan, red: NSColor }
let soft = Palette(amber: NSColor(srgbRed: 1.0, green: 0.71, blue: 0.28, alpha: 1),
                   cyan: NSColor(srgbRed: 0.38, green: 0.89, blue: 0.81, alpha: 1),
                   red: NSColor(srgbRed: 1.0, green: 0.42, blue: 0.36, alpha: 1))
let vivid = Palette(amber: NSColor(srgbRed: 1.0, green: 0.66, blue: 0.0, alpha: 1),
                    cyan: NSColor(srgbRed: 0.0, green: 0.90, blue: 0.80, alpha: 1),
                    red: NSColor(srgbRed: 1.0, green: 0.23, blue: 0.19, alpha: 1))

func glyph(_ p: Palette, capsule: Bool, dim: Set<Int> = [], dotsOnly: Bool = false) -> NSImage {
  let img = NSImage(size: NSSize(width: 40, height: 22), flipped: true) { _ in
    if capsule {
      NSColor(srgbRed: 0.08, green: 0.11, blue: 0.12, alpha: 0.9).setFill()
      NSBezierPath(roundedRect: NSRect(x: 0, y: 1, width: 40, height: 20), xRadius: 7, yRadius: 7).fill()
    }
    if !dotsOnly {
      NSAttributedString(string: "6", attributes: [
        .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: p.amber,
      ]).draw(at: NSPoint(x: 5, y: 2))
    }
    for (i, k) in kinds.enumerated() {
      let c = k == .fault ? p.red : k == .wait ? p.amber : p.cyan
      c.withAlphaComponent(dim.contains(i) ? 0.3 : 1).setFill()
      NSBezierPath(ovalIn: NSRect(x: 22 + CGFloat(i % 3) * 6, y: 5 + CGFloat(i / 3) * 6, width: 4, height: 4)).fill()
    }
    return true
  }
  img.isTemplate = false
  return img
}

final class AppDelegate: NSObject, NSApplicationDelegate {
  var items: [NSStatusItem] = []
  var tick = 0
  let mode = CommandLine.arguments.dropFirst().first ?? "all"

  func add(_ image: NSImage) -> NSStatusItem {
    let it = NSStatusBar.system.statusItem(withLength: 44)
    it.button!.image = image
    items.append(it)
    return it
  }

  func applicationDidFinishLaunching(_ note: Notification) {
    if mode == "timer" || mode == "all" {
      // D: vivid palette, frames swapped by a timer at 8 fps.
      let d = add(glyph(vivid, capsule: false))
      Timer.scheduledTimer(withTimeInterval: 1.0 / 8.0, repeats: true) { [weak self] _ in
        guard let self else { return }
        self.tick += 1
        d.button!.image = glyph(vivid, capsule: false, dim: [self.tick % 9])
      }
    }
    if mode.hasPrefix("cached") {
      // Pre-rendered frames; only the image pointer changes each tick.
      let fps = mode == "cached8" ? 8.0 : 1.0
      let frames = (0..<9).map { glyph(vivid, capsule: false, dim: [$0]) }
      let c = add(frames[0])
      Timer.scheduledTimer(withTimeInterval: 1.0 / fps, repeats: true) { [weak self] _ in
        guard let self else { return }
        self.tick += 1
        c.button!.image = frames[self.tick % frames.count]
      }
    }
    if mode == "all" {
      // E: soft palette on a dark capsule, static.
      _ = add(glyph(soft, capsule: true))
      // F: NSImageView subview holding the colored image, pulsed by Core Animation.
      let f = NSStatusBar.system.statusItem(withLength: 44)
      items.append(f)
      DispatchQueue.main.async {
        let iv = NSImageView(frame: f.button!.bounds)
        iv.image = glyph(soft, capsule: false)
        iv.wantsLayer = true
        f.button!.addSubview(iv)
        let a = CABasicAnimation(keyPath: "opacity")
        a.fromValue = 1; a.toValue = 0.3; a.duration = 1.2; a.autoreverses = true; a.repeatCount = .infinity
        iv.layer!.add(a, forKey: "pulse")
      }
    }
    print("mode=\(mode) items=\(items.count)")
    fflush(stdout)
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
