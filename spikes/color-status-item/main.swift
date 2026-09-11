// Spike: can a menu bar status item show colored, animated per-session dots on this Mac?
// Build: swiftc -O main.swift -o tower-statusitem
// Run:   ./tower-statusitem [static]
import AppKit
import QuartzCore

let amber = NSColor(srgbRed: 1.0, green: 0.71, blue: 0.28, alpha: 1)
let cyan = NSColor(srgbRed: 0.38, green: 0.89, blue: 0.81, alpha: 1)
let red = NSColor(srgbRed: 1.0, green: 0.42, blue: 0.36, alpha: 1)

enum Kind { case fault, wait, work }

final class AppDelegate: NSObject, NSApplicationDelegate {
  var item: NSStatusItem!
  let animate = !CommandLine.arguments.contains("static")
  let kinds: [Kind] = [.fault, .wait, .wait, .wait, .wait, .wait, .work, .work, .work]

  func applicationDidFinishLaunching(_ note: Notification) {
    item = NSStatusBar.system.statusItem(withLength: 44)
    guard let button = item.button else { fatalError("status item has no button") }
    button.wantsLayer = true
    button.setAccessibilityLabel("6 sessions need you, 3 working")
    DispatchQueue.main.async { self.build(in: button) }
  }

  func build(in button: NSStatusBarButton) {
    let root = button.layer!
    let h = button.bounds.height
    let scale = button.window?.backingScaleFactor ?? 2

    let text = CATextLayer()
    text.string = NSAttributedString(string: "6", attributes: [
      .font: NSFont.monospacedDigitSystemFont(ofSize: 15, weight: .heavy),
      .foregroundColor: amber,
    ])
    text.contentsScale = scale
    text.alignmentMode = .right
    text.frame = CGRect(x: 2, y: (h - 18) / 2 - 1, width: 16, height: 18)
    root.addSublayer(text)

    let dot: CGFloat = 4, gap: CGFloat = 2
    let grid = dot * 3 + gap * 2
    let originX: CGFloat = 23, originY = (h - grid) / 2
    let now = CACurrentMediaTime()
    for (i, kind) in kinds.enumerated() {
      let l = CALayer()
      let col = CGFloat(i % 3), row = CGFloat(i / 3)
      l.frame = CGRect(x: originX + col * (dot + gap), y: originY + (2 - row) * (dot + gap), width: dot, height: dot)
      l.cornerRadius = dot / 2
      l.backgroundColor = (kind == .fault ? red : kind == .wait ? amber : cyan).cgColor
      root.addSublayer(l)
      guard animate else { continue }
      let a = CAKeyframeAnimation(keyPath: "opacity")
      switch kind {
      case .work: a.values = [0.35, 1, 0.35]; a.keyTimes = [0, 0.5, 1]; a.duration = 1.7
      case .wait: a.values = [1, 1, 0.22, 0.22, 1]; a.keyTimes = [0, 0.5, 0.68, 0.86, 1]; a.duration = 2.6
      case .fault: a.values = [1, 1, 0.22, 0.22, 1]; a.keyTimes = [0, 0.5, 0.68, 0.86, 1]; a.duration = 1.3
      }
      a.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
      a.repeatCount = .infinity
      a.beginTime = now + Double(i) * 0.19
      l.add(a, forKey: "pulse")
    }
    print("appearance=\(button.effectiveAppearance.name.rawValue) height=\(h) scale=\(scale) screens=\(NSScreen.screens.count) animate=\(animate)")
    fflush(stdout)
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
