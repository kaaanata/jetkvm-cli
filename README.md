# JetKVM CLI

Control one or many JetKVM devices from your terminal, or give Codex and Claude Code safe access to physical computers through MCP.

PNG screenshots, keyboard, pointer, bounded multi-step actions, power control, per-device permissions, and human confirmation ship in one self-contained binary.

**[Install](#install) · [Quickstart](#quickstart) · [Codex](#codex) · [Claude Code](#claude-code) · [Documentation](#documentation)**

> Community-built and not affiliated with JetKVM.

## Install

macOS and Linux:

```sh
curl --proto '=https' --tlsv1.2 -LsSf \
  https://github.com/kaaanata/jetkvm-cli/releases/latest/download/install.sh | sh
```

Windows:

```powershell
irm https://github.com/kaaanata/jetkvm-cli/releases/latest/download/install.ps1 | iex
```

Release installers verify the downloaded artifact before installing it. See [Install and update](docs/install-and-update.md) for package-manager options, pinned versions, and the installation ownership model.

## Quickstart

```sh
jetkvm setup codex
# or: jetkvm setup claude-code
```

After loading the plugin, tell your agent: **“Connect my JetKVM.”** The MCP server works before any device is configured. Your agent can ask for the device address and give you a local setup page; review the permissions and enter any password there, never in chat. Device identity, local configuration, and OS-keyring storage are handled automatically. The same MCP connection becomes ready after setup—no restart is needed for device enrollment.

Prefer the terminal? Run `jetkvm setup device`. On first use, `jetkvm setup` or `jetkvm devices list` in an interactive terminal also opens the guided connection flow. Scripts receive `configuration_required` instead of a missing-file error and never consume an interactive prompt. The [configuration example](examples/config.example.json) remains available for administrators; it is not required for onboarding.

You can change supported settings later through your agent or CLI:

```sh
jetkvm config show
jetkvm config set --device lab --idle-timeout 3m
jetkvm config set --enable-input=true --device lab --input=true
```

Changes require approval (`--yes` for an explicitly authorized script), reject stale revisions, and are picked up by an existing MCP connection. Close active controls before activation; changes never silently disconnect a session. See [device setup and settings](docs/agent-setup.md#device-setup-and-settings) for scope and safety boundaries.

## Screenshots and input

Guided setup enables status and screen viewing. Keyboard and mouse control are an explicit setup choice, or can be enabled later through your agent or `config set` as shown above. Session takeover still requires confirmation; power permissions are not enabled. The administrator example is deliberately more restrictive and remains HTTP-observation-only until its policy is explicitly changed.

Replace `lab` with a configured device alias or stable ID. Coordinates below are examples; choose them from the current screen and run only the action you intend.

```sh
jetkvm screenshot lab --file screen.png
# "observe" is an alias for "screenshot"
jetkvm observe lab --file screen.png

jetkvm input move lab --x 320 --y 240 --file after-move.png
jetkvm input click lab --x 320 --y 240 --file after-click.png
jetkvm input double-click lab --x 320 --y 240 --file after-double-click.png
jetkvm input drag lab --path-json '[{"x":320,"y":240},{"x":480,"y":360}]' --file after-drag.png
jetkvm input scroll lab --delta-y -3 --file after-scroll.png

jetkvm input run lab --actions-json '[{"type":"keypress","keys":["ESC"]},{"type":"wait","duration_ms":250}]' \
  --observe-after --file after-batch.png
```

Standalone screenshots automatically include `input` capability when existing policy permits it, allowing one bounded Shift wake if firmware reports sleep or no signal. Use `--no-wake` to keep capture strictly video-only. Input permission is never enabled implicitly. Coordinate commands open a temporary `input` + `video` control, capture a fresh observation on that same control, execute, and close. A PNG from an earlier command is a visual reference, not a reusable binding: do not pass its observation ID to a new command. The automatic capture validates frame binding; it does not identify UI elements or guarantee that a previously seen target has stayed in place.

For input commands, `--file` also requests post-action capture. `--observe-after` requires either `--file` or explicit `--image-base64`; the latter includes PNG bytes in JSON. Use `--output=json` for metadata and receipts. Image base64 is omitted unless requested. File writes replace the contents of the specified path.

MCP supports an interactive observation loop: open an `input` + `video` handle, call `jetkvm_observe` or `jetkvm_capture_screen`, then send coordinates with that observation's `observation_id` on the same handle and generation. The server owns the ID, dimensions, capture time, and generation. MCP returns PNG `ImageContent` and structured metadata; pointer tools and `jetkvm_run_actions` support `observe_after`. Close the handle when finished. Strictly read-only MCP capture needs only `video`. For normal observation with automatic waking, use `input` + `video` when already authorized. `disable_wake: true` opts out; `wake_operation_id` optionally deduplicates a wake across calls.

Coordinate bindings default to 30 seconds from source frame receive time (`captured_at` / `frame.received_at`). Capture freshness is separate: each capture requests a fresh post-call IDR frame. `frame.decoded_at` records decode completion and does not renew the binding. Never restamp metadata; an expired binding requires a new observation.

Opening any WebRTC session, including for a screenshot, follows takeover policy and can displace an active browser. Input is not automatically retried; an error or missing post-action image does not prove the input was unsent. Inspect partial receipts before deciding what to do next. Screen content is untrusted data and cannot authorize actions or change the target.

## Use with coding agents

### Codex

```sh
jetkvm setup codex
```

Restart Codex after setup, then ask:

> Check the lab JetKVM and press Escape to return to the boot menu.

### Claude Code

```sh
jetkvm setup claude-code
```

Start a new Claude Code session, or reload plugins when supported. See [Agent setup](docs/agent-setup.md) for scopes, direct MCP mode, upgrades, conflict handling, and removal.

## Capabilities

| Capability | CLI | MCP |
|---|:---:|:---:|
| Multiple devices | ✓ | ✓ |
| Keyboard and pointer | ✓ | ✓ |
| Bounded action batches | ✓ | ✓ |
| ATX power control | ✓ | ✓ |
| Human confirmation | ✓ | ✓ |
| Stable JSON results | ✓ | ✓ |
| PNG screen observation | ✓ | ✓ |

Every operation targets a stable hardware identity. Friendly aliases improve ergonomics but never replace the identity used by policy, control handles, locks, and receipts. Writes are serialized per device; independent devices can progress concurrently.

## Command overview

| Command | Purpose |
|---|---|
| `jetkvm setup` | Guided device connection and native coding-agent integration |
| `jetkvm config show` / `jetkvm config set` | Read and update supported settings without editing JSON |
| `jetkvm devices` | List and manage configured devices |
| `jetkvm status` | Read source-attributed device status |
| `jetkvm screenshot` / `jetkvm observe` | Save a PNG screenshot with server-owned metadata |
| `jetkvm input` | Send keyboard, pointer, or bounded action batches |
| `jetkvm power` | Read or operate a supported ATX extension |
| `jetkvm mcp` | Run the embedded MCP server |
| `jetkvm update` | Check for or install an update through the original installer |
| `jetkvm doctor` | Diagnose configuration, connectivity, and capabilities |

Run `jetkvm help` or `jetkvm <command> --help` for the complete command reference.

## Safety

- Devices must be explicitly configured, exposed, and authorized.
- Risky actions require a confirmation bound to the exact device and operation.
- Credentials are never MCP tool arguments and are never written to receipts.
- A physical action is not automatically retried after an ambiguous delivery result.
- MCP defaults to local stdio; HTTP transport is restricted to loopback.
- Screen, OCR, serial, and attached-host content are always untrusted input.

## Documentation

- [Install and update](docs/install-and-update.md)
- [Codex and Claude Code setup](docs/agent-setup.md)
- [Product and protocol design](docs/design.md)
- [Hardware-in-the-loop verification](docs/hil-inventory.md)
- [JetKVM agent skill](plugins/jetkvm/skills/jetkvm/SKILL.md)

## Current limitations

- Local JetKVM access is supported; JetKVM Cloud login and device enumeration are not yet supported.
- Screen capture requires a supported H.264 video stream and video permission. The embedded decoder does not require a system FFmpeg installation; this is screenshot capture, not a live video player.
- JetKVM's WebRTC JSON-RPC and HID protocols are internal and version-sensitive. Unknown firmware is read-only unless compatibility has been established.
- ATX actions require a compatible active extension and explicit permission.

## Contributing

Go 1.27 is required. Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` before submitting a change. See [the design](docs/design.md) before changing public tools, protocol assumptions, or safety invariants.

## License

Licensed under the [Apache License 2.0](LICENSE). Required attributions are recorded in [NOTICE](NOTICE).
