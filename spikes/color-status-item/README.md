# Menu bar color and motion spike

Question: can the Agents status item show per-session colored dots, and what does animating them cost?

Run on September 10, 2026: macOS 26.6.2, Xcode 26.6, Mac Studio with a Studio Display (main) and an LG UltraFine. The system was in Dark Mode; the status button reported `NSAppearanceNameVibrantLight`.

## Results

| Approach | Main display | Second display | Source |
| --- | --- | --- | --- |
| Core Animation layers in the status button | Washed to near-white | Full color, animates | `main.swift`, `shots/d1a-item.png`, `shots/d2a-item.png` |
| Layers in a subview with `allowsVibrancy = false` | Washed to near-white | Full color | `variants.swift`, `shots/v1-item.png` (right item) |
| Non-template `NSImage` as the button image | Color kept, muted (amber reads tan) | Full color | `variants.swift`, `round3.swift`, `shots/r1-item.png` |
| Same image on a dark capsule | Whole image muted, capsule included | Full color | `round3.swift`, `shots/r1-item.png` (middle) |
| `NSImageView` subview pulsed with Core Animation | Washed to near-white | Full color | `round3.swift`, `shots/r1-item.png` (left) |
| Custom `draw(_:)` view, with or without vibrancy | Washed to near-white | — | `drawview.swift`, `shots/w1-item.png` |
| SwiftUI `MenuBarExtra` label, `Image(nsImage:)` with or without `.renderingMode(.original)` | Color kept, muted like the `NSStatusItem` image | Full color | `menubarextra.swift`, `shots/m1-item.png`, `shots/m2-item.png` |

CPU of the test process, averaged over 30 seconds from cumulative CPU time:

| Updates | Method | CPU |
| --- | --- | --- |
| None (static colored icon) | Layers | 0.00% |
| 8 per second | New drawing-handler image each tick | 4.8% |
| 8 per second | Pre-rasterized bitmap frames | 4.2% |
| 8 per second | Custom view, `needsDisplay` | 3.7% |
| 1 per second | Pre-rasterized bitmap frames | 0.53% |
| 1 per second | Custom view, `needsDisplay` | 0.60% |
| Reference | iStat Menus Status, all of its items, running normally | 1.3% |

A `sample` profile of the 8-per-second bitmap run shows the time in `-[NSButtonCell setImage:]` triggering Auto Layout (`NSISEngine`), Core Animation transaction commits, XPC messages, and contention across roughly 25 dispatch worker threads, not in drawing.

## Conclusions

- Draw the glyph as a non-template image. It is the only tested method that keeps color on the main display.
- Pick colors from the status button's effective appearance, not the system appearance, and tune them on the real menu bar.
- Each update costs about 0.5% CPU per update per second regardless of drawing method. Slow discrete blinks fit the CPU budget; smooth continuous animation doesn't.
- These were unbundled command-line binaries. Recheck CPU in the real app bundle.

## Build

`swiftc` wasn't on this shell's path, so the spikes were built with the toolchain driver:

```sh
T=/Applications/Xcode.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin
SDK=/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk
$T/swift-driver --driver-mode=swiftc -O -sdk "$SDK" round3.swift -o tower-round3
./tower-round3 cached1   # colored glyph, one frame change per second; Ctrl-C to quit
```

`menubarextra.swift` also needs `-parse-as-library`.
