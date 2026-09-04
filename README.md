# JetKVM CLI

Control one or many JetKVM devices from your terminal, or give Codex and Claude Code safe access to physical computers through MCP.

Keyboard, pointer, bounded multi-step actions, power control, per-device permissions, and human confirmation ship in one self-contained binary.

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
jetkvm setup
```

`jetkvm setup` detects installed Codex and Claude Code hosts and installs the JetKVM plugin, MCP server definition, and agent skill through each host's native plugin manager. Add devices with the [sanitized configuration example](examples/config.example.json), then verify them with `jetkvm devices list` and `jetkvm doctor <device>`. Configuration is local and credentials stay in the operating-system credential store or dedicated environment variables.

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
| Screen observation | Planned | Planned |

Every operation targets a stable hardware identity. Friendly aliases improve ergonomics but never replace the identity used by policy, control handles, locks, and receipts. Writes are serialized per device; independent devices can progress concurrently.

## Command overview

| Command | Purpose |
|---|---|
| `jetkvm setup` | Install MCP and skills into supported coding agents |
| `jetkvm devices` | List and manage configured devices |
| `jetkvm status` | Read source-attributed device status |
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
- Screen capture is withheld until an embedded H.264 decoder meets the cross-platform, single-binary release requirement.
- JetKVM's WebRTC JSON-RPC and HID protocols are internal and version-sensitive. Unknown firmware is read-only unless compatibility has been established.
- ATX actions require a compatible active extension and explicit permission.

## Contributing

Go 1.27 is required. Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` before submitting a change. See [the design](docs/design.md) before changing public tools, protocol assumptions, or safety invariants.

## License

Licensed under the [Apache License 2.0](LICENSE). Required attributions are recorded in [NOTICE](NOTICE).
