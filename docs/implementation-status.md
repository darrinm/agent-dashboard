# Implementation status — September 10, 2026

The native app, local collector and remote hub protocol are implemented. This is a usable initial build, not a claim that the full-day and multi-machine acceptance gates in DESIGN.md have been completed. No hub has been deployed or machine enrolled into a public service.

## Delivered

- **Native Mac app:** bundled Go helper; fixed-width colored dot grid and attention count; overflow and missing-coverage indication; selective slow blinking; searchable attention, working, recent-review and idle groups; older requests folded after seven days; real output-token activity lines and tool spans; detail/source navigation; configurable global shortcut; keyboard navigation; appearance, notification, hook and remote-pairing settings; local snapshot cache.
- **Local state:** separate execution, outcome, attention episode, reachability and evidence; persistent seen/notified/snooze state; stale observations cannot resolve a newer hook request; completion and interruption distinguished from failure.
- **Collectors:** Claude inventory plus session files/transcript tails; read-only Codex SQLite/rollout fallback; file watching with bounded polling reconciliation; partial-record and file-replacement handling; stored byte offsets; bounded initial/incremental reads and oversized-record recovery.
- **Hooks:** explicit opt-in; preservation/removal of only Agents-owned Claude hooks; TOML-aware wrapping and restoration of an existing Codex notify command; existing command arguments preserved. Live user configuration was not changed during implementation.
- **Remote service:** durable SQLite hub, expiring single-use enrollment, per-device credentials and revocation, ownership validation, transactional sequence watermarks and replay handling, initial-state backfill, backoff/jitter, heartbeats and stale/offline projection, bounded outbox, structured upload allowlist, deletion barriers and remote cache reconciliation. The Mac pairing interface uses Keychain; Linux credentials use protected files. Enrollment checks a local machine fingerprint before upload.
- **Packaging:** locally signed macOS app, Linux amd64/arm64 collector builds, example systemd user units, setup and diagnostics documentation.

## Compatibility reproduced on this Mac

| Source | Observed version/surface | Implemented coverage and limits |
| --- | --- | --- |
| Claude Code | 2.1.268; `agents --json --all` | Existing interactive/background sessions are inventoried without new hooks. Observed `blocked`, `working` and `done`, including the six old blocked sessions. |
| Claude local records | `.claude/sessions/<pid>.json` | Uses `sessionId`, cwd/status and `bridgeSessionId`; aliases attach to one local session rather than creating another row. No separate account-wide cloud inventory exists in this adapter. |
| Claude transcripts | JSONL user/assistant records, tool uses/results, usage | Incremental tailing; bookkeeping entries do not reset wait age. API-error and refusal classifications have synthetic behavior tests. Text-only assistant output is reconciled with inventory rather than assumed to mean attention. |
| Codex | 0.154.0; shared-daemon CLI/proxy | Read-only initialization could not reach the default `app-server-control.sock`; no usable alternate endpoint was verified. No replacement daemon or thread was started to force coverage. |
| Codex fallback | `state_5.sqlite`, rollout JSONL | Discovers Desktop/CLI/subagent threads; reads recorded turns, output-token deltas and tool spans. State is labeled inferred. Pending approvals that are not persisted in rollouts are unavailable. The panel explicitly reports limited Codex coverage. |
| Codex Cloud | `codex cloud list --json --limit 1` | Command verified against existing login; returned `{"tasks":[],"cursor":null}`. No nonempty task format or live cloud transitions could be validated. Managed-cloud collection is not enabled. |
| Source navigation | Claude attach and Ghostty CLI help | The supported background-session attach command is exposed. It was not launched into a live user session during testing. Codex uses project/transcript/ID fallbacks; no fake “open” through resume. |
| Native shell | macOS 26 SDK, Swift 6.3.3 | AppKit status item/panel hosts SwiftUI. The installed SwiftUI MenuBarExtra API lacks a public presentation binding for the global-shortcut requirement. Existing color-spike results remain in DESIGN.md. |

Go used for the build: 1.26.1. Provider integrations depend on installed formats; unsupported/busy data sources surface health failures rather than silent empty success. Current Claude inventory was 44 sessions; the latest checked dashboard had **six attention requests and three working sessions**. These are observations during implementation, not fixed fixture totals.

## Verification completed

`make test` passes with the Go race detector. Thirteen tests cover attention identity/acknowledgment, stale observation rejection, hook request reconciliation, failure recovery, persisted state through restart, offline attention preservation, partial records and file rotation, oversized-record recovery, usage deduplication/counter resets, upload privacy, hook preservation, authenticated APIs, single-use enrollment, foreign device rejection, replay/reordering, revocation, deletion barriers, and the hard outbox byte cap.

The integration test runs an actual loopback HTTP hub and two separate collector stores. A source projection reaches the viewer even after its original outbox is deleted, and private text/paths are absent. Test enrollment uses temporary credential files, not the user's Keychain.

`go vet ./...`, native compilation/signing, and both Linux cross-builds pass. The SDK reports a deprecation warning for the C Keychain API's noninteractive-read option; the call remains available. Actual remote enrollment against a user's Keychain has not been exercised.

The latest packaged app was launched on this Mac and its authenticated collector endpoint checked. The app's own SwiftUI panel renderer produced a native panel image, which was visually inspected with real session data. Local captures are in ignored `.local/` and contain private session text. A native UI-automation attempt timed out, so it does not count as successful keyboard/accessibility or multi-display verification.

One live `ps` sample showed approximately **107 MiB app + 34 MiB collector RSS**, with 0.4% and 0.6% reported CPU respectively. This is a spot check during live work, not the design's idle CPU, 50-active-session, latency or full-day benchmark. The signed app bundle is approximately 11 MB.

## Remaining acceptance work and limits

- Run the full-day acceptance exercise with real permission, question, failure and recovery transitions. The initial app successfully adopts the existing backlog; it has not resolved the six requests on the user's behalf.
- Exercise one other workstation and one cloud VM/container. Linux binaries compile but have not been executed on those targets. The hub still needs a chosen host, persistent volume and HTTPS endpoint. Local monitoring works without it.
- Validate native arrow/Return handling, VoiceOver, notification permission and actions, login launch, Reduce Motion/Transparency, sleep/wake, hidden menu bars and multiple displays through actual UI interaction. The implementations are present; those end-to-end checks are not all complete.
- Validate a real Mac Keychain enrollment, revoke/re-pair workflow, and attach in the installed terminal. Moving or rebuilding a locally signed app can affect Keychain/login-item trust; production distribution needs a stable signing identity.
- Measure latency and resource budgets under load. Local file events coalesce at one second, reconciliation falls back to three seconds, and Claude inventory polls every fifteen seconds unless a hook requests a fresh inventory. Two independent remote polling intervals can yield roughly three to six seconds of propagation; the under-three-second target is not established.
- Source cursors recover from truncation and inode changes, but there is no complete provider generation protocol for every undocumented rollout format. Codex App Server live subscriptions, richer pending-approval coverage and named provider-managed cloud adapters remain future connector work.
- Notifications debounce for twenty seconds and group batches larger than three; a rolling per-minute notification budget and native notification-delivery acceptance remain outstanding.
- Collector shutdown cancels delivery and leaves a durable local outbox. There is no special final-flush guarantee for disposable containers: preserve their state volume or expect possible loss of their last unsent events after abrupt deletion.
- Identity binding detects ordinary copied credentials on another machine; cloning the machine identity too is indistinguishable. Each ephemeral instance must receive a unique persistent identity and enrollment.
- The hub uses one account/fleet and one SQLite process. It is not a public multi-tenant service. Remote control, arbitrary text sharing, broader history UI, and Phase 3 reply/stop/restart actions are intentionally absent.

## Dogfooding fixes — September 10, 2026

Changes made after a whole-project review, built and installed as `/Applications/Agents.app` on the owner's Mac, with lifecycle hooks enabled there.

- **Hook episodes.** A Notification hook's request ID no longer derives from stale transcript activity, which could merge a second, different question into the first and suppress its notification. Re-announced prompts keep their episode; a different prompt or a prompt after resolution opens a new one.
- **Hooks for new sessions.** Events from a session that inventory hasn't listed yet were dropped, so a new session's first permission prompt could be missed. They are now held for up to two minutes and replayed after the inventory refresh the event triggers.
- **Vanished sessions.** A Claude session that disappears from inventory (removed or crashed) is marked ended and its pending request cleared. Previously a session last seen working stayed working indefinitely.
- **Rescans.** The collector reacted to every write under `~/.codex`, including Codex's constantly updated logs database, and reconciled everything every three seconds regardless. It now ignores unrelated Codex files, re-queries Codex's database at most every 15 seconds unless relevant files change, and reconciles every 15 seconds.
- **Outbox.** An unpaired collector wrote every session change to the remote outbox: 8,419 rows (26.8 MB) in about 2.5 hours. It now keeps no outbox until paired, since pairing exports the current projection. The live database went from 33 MB to 600 KB.
- **App work.** The panel's SwiftUI tree exists only while it is open. The UI cache is written at most once a minute. Snapshot dates are parsed directly instead of through `ISO8601DateFormatter`, cutting one live snapshot decode from 24 ms to 1.7 ms. The collector coalesces snapshot bursts to one per second.
- **Panel.** The three newest requests always show, whatever their age; the rest fold into Older requests. Idle Codex subagents appear as a count on their parent and take their Codex nickname as a title. Return toggles the selected row from the search field or the panel, and clicking a row selects it. The menu bar coverage dash no longer appears for permanently limited sources such as Codex's fallback.
- **Accuracy log.** Each expanded session has a **Wrong state?** menu that records the verdict, the session's projection and source health to local `feedback.jsonl`. `agents-collector feedback-report` summarizes it.

Measured on the owner's Mac, 2 minutes each, with this development session active:

| | App | Collector | Combined |
| --- | --- | --- | --- |
| Before the fixes, panel closed | 0.66% | 1.63% | 2.29% |
| After the fixes, panel closed, screen locked | 0.24% | 0.51% | 0.75% |
| After review fixes and cleanup, panel closed, screen locked | 0.16% | 0.30% | 0.46% |

The screen lock pauses menu bar blinking, so the app figure is a lower bound for normal use. A hook invocation takes about 10 ms.

Live checks: the collector's six attention requests and two working sessions matched `claude agents --json`; a permission-prompt hook payload sent through the installed hook binary appeared within a second as a permission request with provider-event evidence, survived the next inventory refresh, and cleared on `PostToolUse`; hook timestamps from a newly started real Claude session updated on tool use; the status item's accessibility label reported no missing sources.

Not verified: keyboard handling (Return and arrows) and the panel teardown in live interaction, because the screen was locked; a genuine interactive permission prompt, because this Mac's settings approved the test sessions' commands automatically.

First accuracy question for the acceptance run: Claude reports finished background sessions that are waiting for the next prompt as `blocked`, which Agents counts as a question. Several of the six requests read as completion reports. Use the Wrong state? log to decide whether those belong in Ready to review.

## Code review fixes — September 10, 2026

A high-effort review of the initial commit found twelve issues. Three were already fixed by the dogfooding changes above (the permanent Codex dash, the unpaired outbox, and sessions removed from Claude's inventory). The rest were fixed on the same branch:

- A permission request stayed pending through a long tool run after approval. A later inventory reporting the session working now resolves it, and only pending hook requests are pinned against snapshots.
- `SessionStart` (also sent on resume and `/compact`) no longer marks a session working.
- A Codex turn killed mid-run stayed working and marked its source stale. Silent turns stop counting as working after 30 minutes (six hours with an open tool call), and connectivity is left alone.
- A machine without Claude Code or Codex showed a missing-source dash forever. Missing providers are now reported as unsupported.
- Reconnecting to a hub replayed up to seven days of superseded events ahead of current state. The outbox now keeps the newest event per session.
- Re-enabling hooks after moving the app duplicated Claude hooks and kept the old Codex wrapper. Ownership is recognized by data directory, and reinstalling replaces the entries.
- A failed hook installation left a backup that made every retry fail. The previous backup state is restored on failure.
- The file watcher stopped reading errors after the first one, which can stall event delivery. Errors are drained.
- Every hook, including each tool call, forced `claude agents` to run. Only lifecycle and prompt events request an inventory, at most every three seconds.
- An interrupted Claude turn showed as working. “[Request interrupted by user]” now ends the turn.
- The history-gap warning never cleared. It now clears an hour after the condition stops.
- Ad-hoc signing lost Keychain access on every rebuild. The build now signs with an Apple Development or Developer ID identity when one exists.

Each has a regression test. A follow-up cleanup pass replaced the special cases that let inventory and removed sessions override a hook-opened prompt with one rule: a prompt stays pending until turn activity or a source vouching that the session isn't waiting comes after it, with inventory trusted from five seconds before it ran. Hooks no longer wait on a refresh in progress, hooks from unknown sessions are queued with only the fields that are read, and tool events from them are dropped. Live check: a permission-prompt hook on a finished session appeared immediately and cleared after the next inventory. The Go suite is 29 tests and passes with the race detector.

## UI refinements

The panel now fills its native window, uses three-quarters of the current display's available height, and anchors two points below the menu bar. Older/idle group headers toggle across the entire row. Session rows expand and collapse in place, support several open sessions, and keep expanded completed output visible while it is being reviewed. Search still reveals matching groups, which can then be collapsed normally.

A native Setup & Help window is available from the panel's question-mark button, Settings, and the Help menu. Nine searchable topics and copyable terminal instructions are bundled in the app; they do not require the repository or a network connection. The build verifies compilation and copies the guide into the signed bundle. Native panel/help renders were inspected during the refinement work.

The canonical design incorporates these user-requested interaction changes. The measured limits above supplement it rather than redefining its acceptance targets.

Session messages now render native Markdown, including emphasis, headings, links, lists, quotes, code blocks, and tables. `make test-ui` verifies parsing and presentation metadata with representative messages and incomplete input. Button/control tooltips explain their actions. Expanded details keep the row's compact activity chart without repeating a larger graph, and the source-confidence note explicitly says saved activity may differ from live status.

Automatic seen/notification acknowledgments no longer open error alerts. A stale episode triggers a silent snapshot refresh without replaying the action against a replacement request; explicit action failures remain visible. Swift model regressions cover stale responses, offline/server failures, notification bookkeeping, and older snapshots. API tests distinguish stale requests (409), unsupported actions (400), and storage failures (500).
