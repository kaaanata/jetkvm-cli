# Codex and Claude Code Setup

JetKVM CLI treats agent integration as a managed product lifecycle, not as an instruction to paste configuration fragments into shared files.

## Quick setup

```sh
jetkvm setup codex
jetkvm setup claude-code
```

The default scope is the current user. Setup verifies the installed `jetkvm` executable, detects the selected host and supported plugin commands, installs or updates the JetKVM marketplace entry and plugin, reads the result back, and records what it owns.

The plugin contains:

- the `jetkvm` MCP server definition;
- the canonical JetKVM skill;
- focused safety and workflow references.

It does not contain another copy of the executable. Both hosts launch the same installed binary:

```text
jetkvm mcp serve --transport=stdio
```

Restart Codex after installation. Start a new Claude Code session or reload plugins when the installed host supports it.

## Installation modes

| Mode | Use case | Ownership |
|---|---|---|
| `plugin` | Codex CLI/Desktop and Claude Code | The host's marketplace and plugin manager own MCP plus skill installation |
| `direct` | A host without plugin support or a managed environment | JetKVM setup owns one exact MCP entry; it does not create a second standalone skill lifecycle |

Plugin mode is the default. Setup must not silently fall back to direct mode because the two modes have different update, conflict, and uninstall semantics.
Use plugin mode when the JetKVM skill is required. Direct mode is intentionally MCP-only so uninstall and updates do not compete with a separately copied skill.

Examples of explicit direct mode:

```sh
jetkvm setup codex --mode=direct --scope=user
jetkvm setup claude-code --mode=direct --scope=user
```

Codex setup currently uses user scope in both modes because the host-native `codex mcp add` and plugin commands do not expose a project-scope mutation. JetKVM does not silently edit `.codex/config.toml` as a competing configuration authority.

The files under [`examples/codex`](../examples/codex) and [`examples/claude-code`](../examples/claude-code) are references for administrators who intentionally manage host configuration themselves. They are not the default onboarding path.

## Idempotency and conflicts

Before changing a host, setup classifies every relevant component as one of:

- `absent`;
- `equivalent`;
- `owned_outdated`;
- `foreign_conflict`;
- `legacy_direct_install`;
- `partially_installed`.

Equivalent state is a no-op. JetKVM-owned outdated state is upgraded through the host's native lifecycle. A matching name from another source, a different MCP command, duplicate plugin/direct registrations, or a modified owned file is a conflict.

Setup does not use an ambiguous `--force`. Intentional transitions use `--migrate`; an explicit foreign replacement requires `--replace-conflict` and confirmation. Non-interactive replacement also requires `--yes`.

Each operation produces a stable receipt containing host and JetKVM versions, scope, canonical workspace, binary real path, component identities, before/after hashes, ownership, completed stages, and rollback outcome. Secrets and credential values are excluded.

## Removal

```sh
jetkvm setup uninstall codex
jetkvm setup uninstall claude-code
```

Uninstall removes only components that the matching setup receipt proves JetKVM created and that remain unchanged. It does not remove device configuration, keyring credentials, operation receipts, audit history, or the CLI binary. Pre-existing equivalent components are recorded as unowned and are left in place.

If an owned shared file changed after setup, removal stops with `rollback_conflict` instead of overwriting the user's changes.

## Doctor

```sh
jetkvm setup doctor
jetkvm setup doctor codex
jetkvm setup doctor claude-code
```

The current doctor reports the host command, integration ownership, and whether marketplace/plugin or direct MCP state is equivalent to the requested target. A future explicit deep mode may add MCP negotiation and device-readiness probes; those are not inferred from configuration presence today.

The full evidence model keeps these layers distinct:

| Layer | Evidence |
|---|---|
| `binary` | PATH resolution, real path, version, platform, and executable permission |
| `host` | Host version and required lifecycle commands |
| `marketplace` | Name, source, and update state |
| `plugin` | Installed/enabled state, version, and installation path |
| `components` | MCP definition, skill, and manifests |
| `host_loaded` | Whether a new/current session has loaded the integration |
| `mcp_ready` | MCP negotiation and tool discovery |
| `device_ready` | Configuration, identity, network, capability, and policy |
| `control_ready` | WebRTC, HID, and ATX capability gates |

Doctor is read-only and does not open a control session or send HID or power actions as a connectivity test.

Configured, loaded, MCP-ready, device-ready, and control-ready are distinct states. Doctor reports them separately rather than treating the presence of a config entry as proof that a physical device can be controlled.
