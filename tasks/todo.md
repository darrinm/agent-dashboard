# Dogfooding fixes

Plan agreed after the whole-project review on September 10, 2026.

- [x] Install lifecycle hooks on this Mac (Claude hooks and the Codex notify wrapper), preserving existing hooks
- [x] Fix the Notification hook episode key that could merge a second question into the first
- [x] Throttle Codex database queries and ignore unrelated `~/.codex` writes
- [x] Stop panel work while it's closed
- [x] Make Return toggle the selected row regardless of focus
- [x] Keep the newest requests visible instead of folding every old request
- [x] Cut noise: nest idle subagents under their parent, drop the permanent coverage dash
- [x] Add the Wrong state? feedback log and `feedback-report`
- [ ] Full-day acceptance run with the feedback log (needs a working day of real use)

Found and fixed along the way:

- [x] Hooks from sessions not yet in inventory were dropped
- [x] Sessions removed from Claude's inventory kept their last state indefinitely
- [x] Unpaired collector wrote a 26.8 MB outbox in 2.5 hours
- [x] Snapshot decoding in the app spent 24 ms per update on date formatting

## Review

All Go tests pass with the race detector (29 tests after the code review fixes and cleanup), the Swift checks pass, and the app type-checks and builds. Combined idle CPU went from 2.29% to 0.46% (see docs/implementation-status.md for conditions). Keyboard handling still needs a live check with the screen unlocked. The first acceptance question is whether Claude's `blocked` state for finished background sessions should count as a question.
