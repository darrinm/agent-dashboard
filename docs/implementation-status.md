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

## UI refinements

The panel now fills its native window, uses three-quarters of the current display's available height, and anchors two points below the menu bar. Older/idle group headers toggle across the entire row. Session rows expand and collapse in place, support several open sessions, and keep expanded completed output visible while it is being reviewed. Search still reveals matching groups, which can then be collapsed normally.

A native Setup & Help window is available from the panel's question-mark button, Settings, and the Help menu. Nine searchable topics and copyable terminal instructions are bundled in the app; they do not require the repository or a network connection. The build verifies compilation and copies the guide into the signed bundle. Native panel/help renders were inspected during the refinement work.

The canonical design incorporates these user-requested interaction changes. The measured limits above supplement it rather than redefining its acceptance targets.

Session messages now render native Markdown, including emphasis, headings, links, lists, quotes, code blocks, and tables. `make test-ui` verifies parsing and presentation metadata with representative messages and incomplete input. Button/control tooltips explain their actions. Expanded details keep the row's compact activity chart without repeating a larger graph, and the source-confidence note explicitly says saved activity may differ from live status.

Automatic seen/notification acknowledgments no longer open error alerts. A stale episode triggers a silent snapshot refresh without replaying the action against a replacement request; explicit action failures remain visible. Swift model regressions cover stale responses, offline/server failures, notification bookkeeping, and older snapshots. API tests distinguish stale requests (409), unsupported actions (400), and storage failures (500).
