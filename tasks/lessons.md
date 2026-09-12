# Lessons

## Test the explanation, not just the number

When a measurement surprises someone, check that the experiment isolates the cause before explaining it. The menu bar spike attributed 5% CPU to menu bar redraws using "pre-rendered" frames that were `NSImage` drawing handlers, which redraw on every use. Truly rasterized bitmaps and a profile later showed the cost was AppKit layout and messages to the menu bar host. Rule: before stating why something costs what it does, profile it or change exactly one variable.

## Watch what the files you watch actually do

A directory watch on `~/.codex` looked harmless, but Codex writes a logs database there continuously, which triggered a full rescan about once a second. Rule: when adding a file watch, list which writes will fire it on a real machine and filter to the ones that can change state.

## Verify against live data before calling a fix done

The hook integration had passing tests and still dropped events from new sessions, and removed sessions stayed "working" forever. Both surfaced only when a real session was started and removed. Rule: exercise the end-to-end path on the real machine, including session start and removal, not just synthetic payloads against known sessions.
