// Spike: can a menu bar status item show colored, animated per-session dots on this Mac?
// Build: swiftc -O main.swift -o tower-statusitem
// Run:   ./tower-statusitem [static]
import AppKit
import QuartzCore

let amber = NSColor(srgbRed: 1.0, green: 0.71, blue: 0.28, alpha: 1)
let cyan = NSColor(srgbRed: 0.38, green: 0.89, blue: 0.81, alpha: 1)
let red = NSColor(srgbRed: 1.0, green: 0.42, blue: 0.36, alpha: 1)

enum Kind { case fault, wait, work }

final class NoVibrancyView: NSView {
  override var allowsVibrancy: Bool { false }
}

func drawGlyph(kinds: [Kind], phase: Int) -> NSImage {
  let img = NSImage(size: NSSize(width: 40, height: 22), flipped: true) { _ in
    let s = NSAttributedString(string: "6", attributes: [
      .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: amber])
    s.draw(at: NSPoint(x: 4, y: 2))
    for (i, k) in kinds.enumerated() {
      let x = 21 + CGFloat(i % 3) * 6, y = 5 + CGFloat(i / 3) * 6
      let c = (k == .fault ? red : k == .wait ? amber : cyan)
      c.withAlphaComponent((i + phase) % 4 == 0 ? 0.3 : 1).setFill()
      NSBezierPath(ovalIn: NSRect(x: x, y: y, width: 4, height: 4)).fill()
    }
    return true
  }
  img.isTemplate = false
  return img
}

final class AppDelegate: NSObject, NSApplicationDelegate {
  var viewItem: NSStatusItem!
  var imageItem: NSStatusItem!
  var phase = 0
  let kinds: [Kind] = [.fault, .wait, .wait, .wait, .wait, .wait, .work, .work, .work]

  func applicationDidFinishLaunching(_ note: Notification) {
    // Variant C: layers inside a subview that opts out of vibrancy.
    viewItem = NSStatusBar.system.statusItem(withLength: 44)
    let button = viewItem.button!
    DispatchQueue.main.async {
      let v = NoVibrancyView(frame: button.bounds)
      v.autoresizingMask = [.width, .height]
      v.wantsLayer = true
      button.addSubview(v)
      let h = v.bounds.height
      let text = CATextLayer()
      text.string = NSAttributedString(string: "6", attributes: [
        .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy), .foregroundColor: amber])
      text.contentsScale = 2
      text.alignmentMode = .right
      text.frame = CGRect(x: 2, y: (h - 18) / 2 - 1, width: 16, height: 18)
      v.layer!.addSublayer(text)
      let now = CACurrentMediaTime()
      for (i, k) in self.kinds.enumerated() {
        let l = CALayer()
        l.frame = CGRect(x: 23 + CGFloat(i % 3) * 6, y: (h - 16) / 2 + CGFloat(2 - i / 3) * 6, width: 4, height: 4)
        l.cornerRadius = 2
        l.backgroundColor = (k == .fault ? red : k == .wait ? amber : cyan).cgColor
        v.layer!.addSublayer(l)
        let a = CAKeyframeAnimation(keyPath: "opacity")
        a.values = k == .work ? [0.35, 1, 0.35] : [1, 1, 0.22, 0.22, 1]
        a.keyTimes = k == .work ? [0, 0.5, 1] : [0, 0.5, 0.68, 0.86, 1]
        a.duration = k == .work ? 1.7 : (k == .wait ? 2.6 : 1.3)
        a.repeatCount = .infinity
        a.beginTime = now + Double(i) * 0.19
        l.add(a, forKey: "pulse")
      }
      print("C allowsVibrancy=\(v.allowsVibrancy) appearance=\(button.effectiveAppearance.name.rawValue)")
      fflush(stdout)
    }

    // Variant B: non-template NSImage, frames swapped by a timer.
    imageItem = NSStatusBar.system.statusItem(withLength: 44)
    imageItem.button!.image = drawGlyph(kinds: kinds, phase: 0)
    Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
      guard let self else { return }
      self.phase += 1
      self.imageItem.button!.image = drawGlyph(kinds: self.kinds, phase: self.phase)
    }
    print("B image set")
    fflush(stdout)
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
