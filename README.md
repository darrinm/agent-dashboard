# Agents

A native macOS menu bar for existing Claude Code and Codex sessions, with a Go collector and an optional durable remote hub. The implementation follows [DESIGN.md](DESIGN.md); measured coverage and remaining validation are in [implementation status](docs/implementation-status.md).

## Run on this Mac

```sh
make build
open dist/Agents.app --args --show
```

Requires macOS 26, Xcode command-line tools and Go 1.24 or newer. The first build downloads Go dependencies. The resulting app is locally signed and contains its collector; it does not require Go to run. For regular use, copy `dist/Agents.app` to `/Applications` before enabling login launch or installing lifecycle hooks. A Developer ID and notarization are needed for distribution to other Macs.

Click the dot grid or press **Option–Space**. Search by session, project, provider or machine; use the arrow keys and Return to expand or collapse a session in place, and Escape to collapse expanded sessions or close. Several sessions can stay expanded. Group headers toggle across the whole row. Settings offers alternate shortcuts, appearance, notifications, login launch and hub pairing.

The **?** button in the panel and **Setup & Help** in Settings open a native, searchable guide. Its nine topics and copyable setup commands are bundled inside the app and work offline. Recipients do not need this repository to read installation, hook, remote-machine, hub, privacy or troubleshooting instructions.

The count includes older unresolved requests, which are folded into **Older requests** after seven days. Viewing a visible row acknowledges the request without resolving it. Old imported requests do not blink or trigger a notification storm. Recently completed turns appear in **Ready to review** for 24 hours. Older output remains searchable in the idle group until retention expires.

Collection starts automatically and adopts sessions already running. It reads provider files without rewriting them. Claude background sessions expose **Attach in Ghostty** where the inventory supports it. Codex offers project and transcript navigation; it never launches `codex resume` to impersonate focusing a live thread.

The app stops a collector it launched when it quits. A separately started collector keeps running. If another collector owns the data directory, the app connects to it.

## Optional lifecycle hooks

Existing sessions are discovered without installing hooks. In Settings, **Enable live lifecycle hooks** adds Claude events and wraps Codex's existing `notify` command. Provider startup behavior determines whether an existing session adopts new hooks.

Preview from the terminal, then apply if desired:

```sh
dist/Agents.app/Contents/Resources/agents-collector hooks-install
dist/Agents.app/Contents/Resources/agents-collector hooks-install --apply
```

Remove with `hooks-remove --apply`, or the Settings button. The installer preserves unrelated hooks and TOML configuration, records the existing notify command, and forwards its original arguments. Keep the app at the installation path used when enabling hooks; remove and reinstall the hooks after moving it. Hook installation was tested against temporary fixture configurations; this implementation run did not modify your live Claude or Codex settings.

## Other machines and cloud VMs

The hub is a small Go HTTP service backed by SQLite. It can run on a persistent VM or container behind your HTTPS reverse proxy. The hub receives structured states and activity counts; prompts, transcript excerpts, paths, branches and tool arguments are excluded. Provider credentials stay on the source machines.

Build standalone collectors:

```sh
make collector
make linux
```

Outputs include the current Mac binary and Linux `amd64` and `arm64` binaries. Install the appropriate binary as `~/.local/bin/agents-collector` on each machine, including the hub host.

Start the hub on its host:

```sh
agents-collector hub --data-dir "$HOME/.local/state/agents-hub" --listen 127.0.0.1:8788
```

Keep that data directory on persistent storage. Publish the loopback service through an HTTPS reverse proxy; no public endpoint is deployed by this repository. The first start creates `admin-token` inside the hub data directory with mode `0600`. Admin credentials stay on the hub host.

Create a pairing code on the hub host. This Python snippet reads the admin secret without placing it in a process argument:

```python
import json, pathlib, urllib.request
secret = (pathlib.Path.home() / ".local/state/agents-hub/admin-token").read_text()
request = urllib.request.Request(
    "http://127.0.0.1:8788/v1/admin/enrollment", data=b"{}",
    headers={"Authorization": "Bearer " + secret}, method="POST")
print(json.load(urllib.request.urlopen(request))["token"])
```

Each code expires in ten minutes and works once. On the Mac, enter your HTTPS hub URL and a fresh code in **Settings → Other machines**. On every remote collector, use a separate code:

```sh
agents-collector enroll --hub https://YOUR-HUB --machine cloud-dev --token-stdin
# Paste the pairing code, then press Return and Ctrl-D.
agents-collector serve --machine cloud-dev
```

The source machine must already have Claude and/or Codex installed and signed in. Pass `--claude /absolute/path/to/claude` or `--codex /absolute/path/to/codex` if their executables are not on the service's PATH. `--home` selects the source user's home and `--data-dir` selects Agents' private state.

Example user services are in [deploy/systemd](deploy/systemd). Copy the desired unit into `~/.config/systemd/user`, then run `systemctl --user daemon-reload` and `systemctl --user enable --now agents-collector` or `agents-hub`. User services need lingering enabled by the host administrator to run after logout. Edit the PATH in the collector unit for that machine's CLI installation.

Do not include an enrolled state directory in a VM/container image. Enrollment binds its credential to macOS hardware identity or Linux machine-id. Containers should supply a unique persistent `AGENTS_MACHINE_ID` per instance, especially where machine-id is shared or absent. A detected identity change stops upload and requires fresh enrollment. A bit-for-bit clone of both credentials and machine identity cannot be distinguished automatically.

## Storage and diagnostics

- Mac state: `~/Library/Application Support/Agents`.
- Linux state: `~/.local/state/agents`; override with `--data-dir` or `AGENTS_DATA_DIR`.
- macOS hub device credentials use Keychain in the normal build. Linux uses protected local storage. `AGENTS_CREDENTIAL_STORE=file` explicitly selects protected files for headless Macs or isolated tests.
- Local HTTP listens on a random loopback port. `collector.json` contains its private endpoint and token. Browser-origin requests are rejected.
- SQLite stores projections, attention acknowledgment, transcript offsets and a durable outbox. Pending events are capped at 100 MB / seven days; a configured collector surfaces dropped history. Inactive summaries expire after 30 days; `AGENTS_RETENTION_DAYS` changes that limit. Unresolved requests remain.
- Source failures appear in Settings. A disconnected machine retains its last attention request; disconnection is never treated as completion.

For a diagnostic snapshot, stop any collector using that data directory first, or use a separate directory:

```sh
dist/Agents.app/Contents/Resources/agents-collector snapshot --data-dir /tmp/agents-diagnostic
```

Snapshot output contains local session text. Treat it as private. The `.local` directory used during development is ignored.

The authenticated hub admin API also supports `POST /v1/admin/revoke/{deviceID}` and `DELETE /v1/admin/sessions/{sessionID}`. Deletion installs a replay barrier and disappears from connected remote caches on their next snapshot. It does not delete provider-owned transcripts. A revoked device cannot ingest or read the fleet.

## Verify

```sh
make test
make test-ui
make build
make linux
```

Tests include real loopback HTTP between two collectors and a hub, so the test process needs permission to bind a local port. They use isolated state and never install hooks or credentials into your live configuration.

On the Mac, `make test-ui` checks the native Markdown parser and presentation model, including headings, inline styles, links, nested lists, literal code, tables, and partial messages. Session messages render Markdown in place; the compact preview retains formatting without intercepting row clicks. Buttons and settings controls provide explanatory hover tooltips.

The app can render its own native panel from a captured snapshot for visual QA:

```sh
dist/Agents.app/Contents/MacOS/Agents --render /path/to/snapshot.json /tmp/agents-panel.png
```

This initial release observes sessions and navigates to their sources. Remote replies, approvals, stop/restart controls, provider-managed cloud adapters, and a deployed hub are outside the implemented release. See the status document for exact limitations and acceptance checks still requiring real-world use.
