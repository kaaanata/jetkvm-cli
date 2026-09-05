# JetKVM CLI and MCP Product Design

Status: implemented control, PNG observation, installer, updater, and agent-setup baseline

Date: 2026-09-05

Language: Go 1.27

Target MCP revision: `2026-07-28`

## 1. Product definition

JetKVM CLI is a local-first hardware execution product. One self-contained `jetkvm` binary provides a user-facing CLI and an embedded MCP server for Codex and Claude Code. Both interfaces call the same device identity, policy, confirmation, actor, operation-ledger, and receipt authorities.

The product is not a general proxy for JetKVM's internal JSON-RPC surface. It exposes a closed set of safe, typed operations between an agent or user and a physical computer:

```text
User or MCP host
  -> CLI or MCP adapter
  -> device identity and compiled policy
  -> confirmation and operation ledger
  -> per-device actor and control lease
  -> JetKVM HTTP / WebSocket / WebRTC / HID
  -> attached physical computer
```

MCP requests may be stateless at the protocol boundary. WebRTC sessions, HID state, control ownership, video state, device actors, and operation receipts remain explicitly stateful.

All repository documentation, examples, plugin metadata, and skill content are English.

## 2. Decision summary

| Area | Decision |
|---|---|
| Product surface | Complete control CLI plus embedded MCP server in one binary |
| License | Apache-2.0 |
| MCP SDK | Official `github.com/modelcontextprotocol/go-sdk` |
| MCP revision | `2026-07-28` |
| MCP transports | stdio and loopback-only stateless Streamable HTTP |
| Primary MCP hosts | Codex and Claude Code |
| Device scope | Local direct access, including user-managed VPN addresses |
| JetKVM Cloud | Outside the first release |
| Multiple devices | First-class, isolated by stable device ID |
| Write ordering | Serialized within one device; independent devices may run concurrently |
| Control lifetime | Five-minute idle timeout and 30-minute absolute lifetime by default |
| Input | Keyboard, pointer, waits, and bounded deterministic action batches |
| Power | Explicitly enabled ATX press, reset, and hold |
| Confirmation | One-time proof bound to the complete effect and target |
| Retry | Never retry automatically after the physical send boundary becomes ambiguous |
| Receipts | Operation receipts retained 30 days; security audit retained 90 days |
| Screenshot | Embedded H.264 decode to PNG; CLI files and MCP ImageContent with server-owned binding metadata |
| CLI framework | Cobra command tree; Lip Gloss v2 layouts; Huh v2 forms; Charmbracelet Log on stderr |
| Machine output | Stable JSON; non-TTY defaults to JSON; stdout is result-only |
| Release | Release Please creates SemVer tags; GoReleaser publishes artifacts and provenance |
| Install | Release-pinned standalone installers plus package-manager channels |
| Update | The original installation owner remains the update authority |
| Agent setup | Host-native plugin lifecycle by default; direct mode is explicit |

## 3. Upstream protocol boundary

The compatibility baseline was researched against JetKVM `release/0.5.8` and upstream development commit `3f7c7095a0628a305f652fb3ee5e031f91106eb4`.

Relevant upstream properties:

1. HTTP exposes authentication, device information, diagnostics, file transfer, WOL, and WebRTC signaling.
2. Most controls use an internal JSON-RPC protocol over a WebRTC `rpc` DataChannel.
3. Keyboard, pointer, serial, and terminal streams use dedicated DataChannels.
4. Video arrives as H.264/H.265 media tracks.
5. A device has one global current session; a new control connection can displace a browser session.
6. RPC registries differ between released firmware and development builds.
7. Local authentication does not provide service-account scopes or multi-user audit authority.
8. Open HTTP RPC and screenshot proposals are not stable released compatibility contracts.

Primary references:

- [JetKVM source](https://github.com/jetkvm/kvm)
- [Local access documentation](https://jetkvm.com/docs/networking/local-access)
- [HTTP RPC issue](https://github.com/jetkvm/kvm/issues/1320)
- [Screenshot API issue](https://github.com/jetkvm/kvm/issues/1555)
- [Keyboard and mouse API issue](https://github.com/jetkvm/kvm/issues/1426)

The public product must describe these integrations as compatibility with tested internal protocols, never as an official stable JetKVM API. Unknown firmware may expose conservative read-only status, but control stays unavailable until compatibility is established.

## 4. Product influences

### GitHub MCP Server

Adopt a startup capability ceiling, one compiled policy for both discovery and execution, closed schemas, explicit effect classes, and confirmation state bound to the complete request. Do not assume a downstream API supplies authorization, idempotency, or audit: the hardware execution layer owns those properties.

### Playwright and Chrome DevTools MCP

Adopt explicit target identifiers, long-lived backend state behind short MCP calls, generation-bound observations, action batches, post-action observation, native multimodal content, and distinct owned versus attached teardown. Unlike browser targets, JetKVM has no accessibility tree or stable DOM references, so pointer coordinates must be bound to a fresh observation.

### Home Assistant and infrastructure MCP servers

Keep `discovered`, `registered`, `exposed`, and `authorized` as separate states. Use stable tools with a `device_id` argument instead of generating tools per device. Compute authorization as the intersection of deployment ceiling, toolset policy, device exposure, device permission, caller scope, firmware support, and runtime readiness; deny always wins.

### OpenAI Computer Use

Use a bounded ordered `actions[]` vocabulary. The server executes deterministic actions supplied by the caller; it does not run an autonomous visual agent or arbitrary code. Batch completion is not a transaction: partial execution must return a partial receipt, and a fresh observation is required before another visual decision.

## 5. Goals and exclusions

### First-release goals

- Configure and explicitly expose one or more local JetKVM devices.
- Authenticate with local password or explicitly accepted no-password mode.
- Pin and verify the stable hardware identity.
- Read source-attributed device status and capability state without opening WebRTC.
- Open a takeover-policy-governed control handle.
- Capture fresh PNG observations through a video-capable control handle.
- Send validated keyboard and pointer input with terminal neutralization.
- Execute one to sixteen deterministic input actions within a bounded duration.
- Read and operate a compatible ATX extension when explicitly enabled.
- Provide equivalent CLI and MCP operations with the same policy and receipts.
- Support MCP stdio and loopback HTTP with current Codex and Claude Code.
- Install and manage the CLI, MCP registration, and canonical agent skill as product lifecycles.

### Exclusions

- JetKVM Cloud/OIDC login and Cloud inventory;
- remote public MCP HTTP;
- arbitrary JSON-RPC, shell, SSH, terminal, or code-execution tools;
- serial console and RFC2217;
- device network, TLS, firmware, developer-mode, or factory-reset administration;
- arbitrary-URL virtual media;
- a real-time video player;
- permission escalation based on screen, OCR, serial, or attached-host content;
- claiming that RPC acceptance proves the physical host completed an action;
- copying GPL implementation code from JetKVM;
- an autonomous agent loop inside the MCP server.

## 6. Core domain objects

```text
Device
├── device_id             stable authorization and audit identity
├── alias                 display and selection convenience
├── origin                exact HTTP(S) origin
├── exposed               explicit MCP visibility
├── permissions           static capability ceiling
├── compatibility         firmware/protocol evidence
├── takeover_policy       whether session displacement is permitted
└── session_policy        idle and absolute lifetimes

ControlHandle
├── handle_id
├── device_id
├── generation
├── ownership             owned or attached
├── capabilities
├── created_at
├── last_used_at
├── idle_expires_at
└── absolute_expires_at

Observation
├── observation_id
├── device_id
├── control_generation
├── captured_at
├── frame dimensions
├── source metadata
└── trust = untrusted_observation

OperationReceipt
├── operation_id
├── request_digest
├── device_id
├── control_generation
├── effect_class
├── policy_revision
├── stage
├── delivery
├── verification
├── terminal_claim
└── retry_safe
```

A stable device ID is the authorization subject. An alias can resolve to a device for user ergonomics, but policy evaluation, control handles, locks, confirmation, and receipts always bind the stable ID. A control generation fences calls from displaced or expired sessions.

## 7. Architecture and concurrency

The composition root creates one runtime shared by CLI or MCP adapters:

```text
config + credential providers + SQLite
                  |
          compiled policy
                  |
     automation operation service
                  |
       DeviceActor registry
          /       |       \
     device A  device B  device C
```

Each stable device ID has one in-process actor, one client, one session policy, one generation sequence, and one cross-process lock. All state-changing work for the same device passes through that actor. Different actors make progress independently.

The actor owns session creation, replacement, teardown, input lease, neutralization, and handle expiry. Transport callbacks enqueue typed events; they do not mutate actor state directly. Runtime shutdown drains automation, neutralizes input, closes owned sessions, then closes durable storage.

CLI control operations are command-scoped: open, execute, neutralize, and close happen within one process. A CLI must not print a handle intended for a later process because runtime shutdown invalidates it. MCP uses explicit handles across calls because the server process remains alive.

## 8. Policy and capabilities

Effective permission is an intersection:

```text
deployment ceiling
∩ configured toolsets
∩ individual tool allow/deny
∩ caller scope
∩ device exposure
∩ device permissions
∩ firmware compatibility
∩ runtime capability
```

Static policy controls tool discovery. The same compiled policy is evaluated again at call time, so a client cannot bypass discovery by invoking a known tool name directly. MCP annotations are descriptive hints and never authorization.

Capability reporting separates:

- `compiled`: support exists in this binary;
- `configured`: policy enables it;
- `firmware`: the tested protocol supports it;
- `ready`: the required runtime component is currently available.

Transient readiness does not constantly add and remove tools. Calls return a typed capability error with source evidence when a configured feature is temporarily unavailable.

## 9. Public MCP surface

The MCP server uses closed input and output schemas and structured content. Current public tools are:

| Tool | Effect | Purpose |
|---|---|---|
| `jetkvm_list_devices` | read | List explicitly exposed devices without contacting them |
| `jetkvm_get_status` | read | Read HTTP-only, source-attributed basic status |
| `jetkvm_get_capabilities` | read | Return compiled/configured/firmware/runtime capability state |
| `jetkvm_open_control` | control | Open a fenced WebRTC control handle under takeover policy |
| `jetkvm_get_control` | read | Read a handle without creating a session |
| `jetkvm_close_control` | control | Neutralize input and close an owned handle |
| `jetkvm_observe` | observe | Capture a PNG and binding metadata from an existing video handle |
| `jetkvm_capture_screen` | observe | Capture the screen using the same observation contract |
| `jetkvm_key_press` | input | Press and release one validated key |
| `jetkvm_key_combo` | input | Press and release one bounded chord |
| `jetkvm_type_text` | input | Type validated US-layout text |
| `jetkvm_pointer_click` | input | Click a point bound to a fresh observation |
| `jetkvm_pointer_move` | input | Move to an observation-bound point |
| `jetkvm_pointer_double_click` | input | Double-click an observation-bound point |
| `jetkvm_pointer_drag` | input | Drag through a bounded observation-bound path |
| `jetkvm_pointer_scroll` | input | Send a bounded horizontal or vertical scroll delta |
| `jetkvm_run_actions` | input | Execute a bounded deterministic action batch |
| `jetkvm_get_power_state` | read | Read supported ATX LED state |
| `jetkvm_power_action` | power | Execute a non-retryable press, reset, or hold |

Observation tools are registered when the injected decoder capability and observer service are available, subject to the tool policy ceiling. They require `device_id`, `control_handle`, and non-zero `expected_generation`; they do not open a new session. Results contain official SDK `ImageContent` with PNG bytes and structured observation metadata. Missing or unsupported video returns an error, never a placeholder image or an external FFmpeg requirement.

Pointer tools and `jetkvm_run_actions` accept `observe_after`. The adapter returns any available image alongside the structured operation and batch receipt. On partial failure it sets the MCP error result while preserving that receipt and any returned observation. An error does not imply that the action was unsent or that a post-action image exists.

### Transport

The stdio process reserves stdout exclusively for MCP frames; logs go to stderr. Loopback Streamable HTTP is stateless at the MCP request layer, requires a separate bearer credential, validates Origin and Host, and refuses non-loopback listeners. Device credentials are never HTTP bearer credentials.

Remote HTTP requires a separate future threat model covering TLS termination, caller identity, revocation, rate limits, reverse-proxy trust, and distributed control ownership.

## 10. Input and computer-use semantics

The action vocabulary includes `move`, `click`, `double_click`, `drag`, `scroll`, `keypress`, `type`, `wait`, and `screenshot`. A batch contains at most 16 actions and runs for at most 15 seconds. Input, waits, and screenshots can share a batch; power, media, and administration remain separate risk domains. Screenshots and post-action observation require video capability and observation permission in addition to the input batch's permission. The core supplies the last screenshot captured by a batch; adapters do not repeat the batch to obtain an image.

Coordinate actions (move, click, double-click, and drag) require a fresh observation binding. The session registers the observation ID, stable device ID, frame generation, dimensions, and capture time. MCP callers supply `observation_id` as the binding authority together with their device, handle, and expected generation; legacy caller dimensions and capture-time fields do not establish or replace a binding. The core resolves the issued ID against the same session's metadata and validates coordinates against its frame dimensions. Generation replacement, expiry, or unacceptable staleness invalidates the binding. Scroll uses bounded deltas rather than pixel coordinates.

Normal CLI coordinate commands capture on their own temporary control before constructing the action batch. They never rebind a caller's screenshot to a newly opened control. MCP keeps the handle alive across observe/action calls, enabling decisions based on the exact returned image. A fresh frame binding proves coordinate provenance, not the semantic identity or continued position of a UI element.

The default coordinate binding lifetime is 30 seconds from the source frame's first RTP receive timestamp. `captured_at` uses this source receive time, also recorded as `frame.received_at`; `frame.decoded_at` separately records decode completion. Decoding, transport to the model, and model reasoning consume the binding lifetime rather than resetting it. Clients must not restamp metadata. An expired binding requires a new observation. Capture freshness is a separate bound: capture requests a fresh post-call IDR frame and validates source receive time against its freshness requirement.

The default capture freshness bound is 5 seconds to accommodate IDR delivery and decoding. Callers can request a stricter bound with CLI `--freshness` or MCP `freshness_ms`; zero selects the service default. This setting does not extend the 30-second coordinate binding lifetime or replace source timestamps with decode completion time.

Keyboard and pointer share one exclusive input lease. Every terminal path attempts the same neutralization sequence:

1. neutral keyboard report;
2. neutral absolute-pointer report;
3. neutral relative-pointer report;
4. flush and bounded close.

This runs after success, validation failure after lease acquisition, cancellation, timeout, disconnect, panic recovery, lease expiry, takeover, and shutdown. Failure to prove neutral state marks input state uncertain and blocks further writes until recovery.

Text confirmation policy:

- up to 256 Unicode scalar values: no length-only confirmation;
- 257 through 4096 scalar values: require action-time confirmation;
- more than 4096 scalar values: reject;
- text followed by a commit key, function key, or sensitive modifier chord requires confirmation regardless of length.

The implementation currently supports a fully prevalidated US keyboard layout. Unsupported characters are rejected before the first HID report, preventing partial text caused by late layout failure.

## 11. Confirmation authority

Confirmation is a short-lived, one-time HMAC-sealed proof issued by a trusted interaction boundary. It binds:

- principal and transport;
- stable device ID;
- control handle and generation;
- effect class and action;
- complete argument digest;
- policy revision;
- operation ID when applicable;
- expiry and nonce.

The proof is atomically consumed at the device-send boundary. It cannot be replayed, moved to another device, reused for modified arguments, or replaced with a caller-supplied `confirmed: true` field.

MCP uses the protocol's elicitation/MRTR flow. If the host cannot complete required elicitation, the operation fails closed. CLI prompts are only proof-issuance adapters; they are not authorization authorities. Non-interactive callers must provide an approved workflow or receive an action-required result.

Opening a session may require confirmation because JetKVM can displace an existing browser session. Reset, hold, long text, commit actions, function keys, and sensitive chords require confirmation according to compiled policy.

## 12. Operations, delivery, and receipts

Each state-changing call uses a caller-visible UUID and a canonical request digest. The durable operation state machine is:

```text
not_sent
  -> send_started
  -> transport_accepted | ambiguous
  -> observation_started | completed | failed
  -> state_observed
  -> completed | failed | cancelled
```

Before `send_started`, a deterministic validation or transport failure may be retry-safe. Once sending begins, interruption cannot prove that the device did not receive the action. The receipt records `delivery_ambiguous`, and neither CLI nor MCP automatically retries it.

Reusing an operation ID with the same digest returns the existing receipt. Reusing it with different arguments is a conflict. Batch receipts identify the last completed action and the action that failed; completed actions are never rolled back or described as atomic.

CLI JSON and MCP structured results use explicit snake_case tags throughout the nested batch receipt, including `input.BatchReceipt`, `input.ActionReceipt`, and `input.Observation`. Clients must consume the declared JSON fields rather than Go field names: for example, `batch.generation`, `batch.actions`, `batch.neutralized`, and `batch.cleanup_failure`, with action fields `index`, `type`, `status`, and optional `error`. The nested batch observation is a binding receipt, distinct from the separately returned PNG observation metadata and image payload.

Transport acceptance, observed device state, and physical outcome are separate claims. HID acceptance does not prove that BIOS or the operating system reacted. ATX RPC acceptance does not prove that a motherboard completed a power transition.

## 13. Credentials, network, and storage

Configuration may reference an operating-system credential-store entry or a dedicated automation environment variable. Literal credentials are rejected. Credentials never appear in MCP arguments, output, logs, tracing attributes, receipts, or setup journals.

HTTPS validates the normal certificate chain. An optional SPKI pin is an additional check. HTTPS never falls back to HTTP. Plain HTTP is a per-device opt-in intended for an isolated trusted LAN or a user-managed VPN.

Local durable state uses private directory and file permissions. It stores identity pins, operation receipts, audit events, runtime secret material, installation receipts, and setup ownership journals. Runtime confirmation secrets are randomly generated and atomically persisted.

Default retention:

| Data | Retention |
|---|---:|
| Operation receipts | 30 days |
| Security audit | 90 days |
| Coordinate binding metadata | Latest 16 issued observations per live session; default usable age 30 seconds |
| Screenshot bytes | Not persisted by the core; CLI writes only to an explicitly requested file |

## 14. CLI contract

Cobra is the command-tree and parsing authority. Charmbracelet Log is used for human diagnostics on stderr. Every result-producing command supports `--output=json|text`; terminals default to text and non-TTY output defaults to JSON. Scripts should select JSON explicitly when the contract matters.

Human presentation uses one `internal/terminal` theme and renderer with Lip Gloss v2 tables and layouts. The CLI maps typed status, capabilities, doctor reports, input/power/control receipts, screenshots, setup plans/receipts and update plans/receipts into semantic documents. Help and usage read the live Cobra command and flag metadata. Errors retain the stable error kind; input results retain delivery, verification, terminal claims, retry safety and neutralization without claiming physical success. Fang was reviewed as a layout reference and is not a dependency.

Human documents lead with the recorded outcome and use a cyan accent, semantic success/attention colors, and muted supporting fields. Root help groups available Cobra commands into Inspect, Control, Integrate and Maintain, with a fallback for new commands and concrete getting-started examples. Help and key/value fields have no redundant column headings and wrap responsively. A no-op update is one sentence; an applied update, rollback and installer-required action remain distinct. Artifact verification and rollback availability are shown only from the receipt, and rollback does not imply a new signature check. These projections do not change machine serialization or operation authority.

Maintenance intent is explicit: `update` and `update rollback` execute without a
second confirmation. Existing `--yes` options remain hidden compatibility no-ops.
`--check`, `--dry-run`, installation ownership, trust verification, and explicit
downgrade restrictions remain unchanged. Device takeover, risky input/power and
integration approval policies are independent of this maintenance UX decision.

Context-scoped progress observations report real work stages and download bytes.
The inline terminal activity uses the existing Bubbles spinner and progress bar,
shows elapsed time and measured average transfer speed, and reports prolonged
absence of progress without retrying work or changing deadlines. Unknown totals
show received bytes, never an invented percentage. Download completion is not
installation completion: signature verification, extraction, activation, self-check
and receipt commit are separate stages. Frame waits, decoding, encoding, saving,
control cleanup and integration steps report their own observed stages.

The activity is output-only and never consumes stdin. The executable's signal
context remains the cancellation owner. The renderer is paused and joined before
forms, and finally joined after business/runtime cleanup. Logger messages are
serialized through the activity; stderr ownership and terminal dimensions survive
the output adapter. Two unconditional Bubble Tea 2.0.9 capability queries are
suppressed at this output-only boundary to prevent unconsumed replies leaking into
the next prompt; other rendering sequences remain unchanged. JSON/MCP/completion
streams never receive this UI. Explicit non-TTY text, no-color, dumb-terminal and
accessibility modes receive only stage lines.

CLI result documents are buffered until command-scoped control and runtime cleanup
complete. A cleanup or operation failure retains available results, labels the
human view as partial, and reports the error separately; JSON receipt schemas are
unchanged. Accepted input effects are not erased by a failed post-action capture or
cleanup. Normal human output hides diagnostic IDs/timestamps unless `--verbose` is
requested, but keeps delivery, verification, retry safety and neutralization. Partial
outcomes retain full details. Error guidance names safe next steps without promising
that cancellation undoes delivered work or encouraging automatic write retries.
CLI cancellation is classified as `canceled` (exit 5, not automatically retryable).
Update activation failures retain `update_apply_failed` (exit 5), and failed rollback
retains `rollback_failed` (exit 8); a wrapped cancellation must not hide that uncertain
installation state. These classifications do not add fields to the JSON envelope.

The renderer measures the destination terminal width, wraps Unicode by display width and stacks narrow tables without truncating values. It does not require emoji or color to convey meaning. Display text is stripped of injected terminal commands. Non-TTY text, a nonempty `NO_COLOR`, `TERM=dumb`, or `JETKVM_ACCESSIBLE=1` produce no control sequences, including when forced-color environment variables are present. `JETKVM_ACCESSIBLE=1` selects linear screen-reader-friendly prompts. Terminal detection uses the actual terminal descriptor, not just a character-device check.

Confirmation and maintenance choices use Huh v2, backed by Bubble Tea/Bubbles, with a default negative choice. Forms run briefly inline on stderr, never in the alternate screen. A terminal input uses the interactive form; plain/accessibility mode uses Huh's linear confirm prompt with complete action and device context. The adapter supplies cancelable reads and checks I/O errors because Huh's accessible runner does not propagate them. Only an affirmative, successfully completed interaction can return to the existing proof issuer or maintenance plan executor. Takeover/input/power policy, proof identity, installation ownership and idempotency remain domain responsibilities.

Terminal reader cancellation must join the underlying Read goroutine before cancelreader is closed. Bubble Tea v2.0.9 and its pinned Ultraviolet revision include the upstream StreamEvents join fix; older Huh-compatible minimum dependencies are not sufficient. A blocked-reader lifecycle regression and repeated real PTY race tests guard this boundary.

Interactive confirmation runs the Huh form as a Bubble Tea model. Operation context cancellation and Huh's cancel action request graceful Quit; the adapter waits for Program.Run and its cancellation callback before returning a rejected/canceled result. The program context is detached from operation cancellation, and the executable's existing signal context remains the signal authority. Direct Huh RunWithContext would trigger Bubble Tea's kill shutdown, which skips the input join even in v2.0.9. The form, key handling and display remain Huh components, and cancellation cannot authorize an operation.

JSON serialization and MCP transport bytes bypass the presentation layer entirely. Cobra-generated shell completion scripts and completion protocol directives also bypass styling. Bootstrap shell/PowerShell installers remain standalone scripts because they run before the Go binary exists; they do not acquire a Charm executable dependency. Setup host subprocess output is captured for authoritative readback, not streamed into the human UI. See [terminal UI verification and component review](terminal-ui.md).

Stdout contains one result document and no progress, prompts, or logs. JSON fields use stable snake_case names. CLI exit kinds and MCP error kinds share one taxonomy, including invalid input, not found, policy denied, confirmation required, capability unavailable, stale generation, conflict, delivery ambiguous, action required, and internal failure.

Commands call the automation service rather than JetKVM transports directly. Setup and update follow the same machine-readable receipt model.

`jetkvm screenshot <device> --file screen.png` (alias `observe`) opens a command-scoped control with only `video` capability, captures a PNG, writes the explicit path, and closes the control. It does not require input permission. Opening the video session still follows takeover policy.

`jetkvm input move|click|double-click|drag` opens `input` + `video` and obtains its coordinate binding on that same control. Scroll and keyboard commands need only `input` unless capture is requested. `--file after.png` implies post-action observation; `--observe-after` requires `--file` or explicit `--image-base64`. JSON reports observation metadata and the saved path, with image bytes omitted unless base64 is explicitly requested. File writes replace the specified path. A file-write or capture failure after input must not cause automatic input replay. See the [README examples](../README.md#screenshots-and-input) for concrete commands.

## 15. Installation and update

GoReleaser is the single release-artifact authority. Release Please creates the version and tag; release automation publishes archives for macOS, Linux, and Windows on AMD64 and ARM64, checksums, SBOMs, and provenance.

The archive compatibility contract is exactly four regular root files: `jetkvm` (`jetkvm.exe` on Windows), `LICENSE`, `NOTICE`, and `README.md`. Existing strict updaters and both bootstrap installers enforce this contract. Release preparation appends the complete codec MIT texts to generated `NOTICE`; nested decoder manifests and license sidecars must not become extra archive entries. Syft separately scans the nested decoder module into `decoder.sbom.json`, published as an independent asset covered by signed checksums and release attestation. Before signing or uploading, the release gate scans all six actual tar/ZIP outputs with the unchanged production extractors, compares extracted binaries byte-for-byte with build outputs, checks full codec attribution, and verifies archive/SBOM checksums and codec inventory.

Release assets include pinned `install.sh` and `install.ps1` entry points. Installers use a closed platform map, private temporary storage, safe archive extraction, mandatory checksum verification, optional Cosign verification when the verifier is installed, and a durable installation receipt. They install to a user-owned directory by default and do not silently elevate. The built-in self-updater always verifies the Sigstore workflow identity and bundle.

Installation owner is a closed enum:

```text
standalone | homebrew | winget | scoop | deb | rpm | source | unmanaged | unknown
```

`jetkvm update` may atomically replace only a proven `standalone` installation. Package-manager owners route through the original manager. Source, unmanaged, and unknown owners fail closed with an exact next action. Downgrades require an exact version and explicit acknowledgement; prereleases require an explicit channel.

Standalone update is a durable prepare/verify/switch/self-check/commit transaction with a verified previous executable for rollback. Windows uses a helper after the running process exits. A checksum alone is insufficient publisher authentication; signature/provenance verification is independent.

See [Install and Update](install-and-update.md) for the public lifecycle.

## 16. Codex and Claude Code setup

`jetkvm setup` is the onboarding authority for supported agent hosts. Device enrollment remains in the strict JetKVM configuration model:

```text
jetkvm setup
jetkvm setup codex
jetkvm setup claude-code
jetkvm setup doctor [codex|claude-code]
jetkvm setup uninstall codex
jetkvm setup uninstall claude-code
```

The default host integration is a native plugin that packages one MCP definition and the canonical JetKVM skill. The plugin launches the installed executable as:

```text
jetkvm mcp serve --transport=stdio
```

It never bundles another binary. CLI updates and plugin updates remain separate, observable lifecycles while sharing one runtime executable.

Direct mode is explicit MCP-only compatibility support. Setup never silently changes mode or creates a second standalone skill lifecycle. Before mutation it classifies state as absent, equivalent, owned outdated, foreign conflict, legacy direct, or partial. Equivalent is a no-op; owned outdated state uses the host-native update; foreign state is not overwritten by default. Codex setup is user-scoped because its host-native mutation commands do not currently expose project scope; Claude Code additionally supports its native project and local scopes.

Setup journals record host/scope/workspace identity, JetKVM real path and version, component identities, before/after hashes, ownership, phases, and rollback result. Uninstall removes only unchanged resources proven to be setup-owned. Device configuration, credentials, operation receipts, and the CLI remain intact unless a separately authorized purge is requested.

The current doctor reports host-command availability, ownership, and integration equivalence. Its future evidence model keeps host-loaded, MCP-ready, device-ready, and control-ready states separate; a configured MCP entry is never presented as proof that a host loaded it or that a device can be controlled.

See [Codex and Claude Code Setup](agent-setup.md) for the public lifecycle.

## 17. Plugin and skill contract

The repository is a marketplace source for both Codex and Claude Code. `plugins/jetkvm` contains:

- `.codex-plugin/plugin.json`;
- `.claude-plugin/plugin.json`;
- `.mcp.json` with the single stdio server definition;
- `skills/jetkvm/SKILL.md`;
- focused `safety.md` and `workflows.md` references;
- `agents/openai.yaml` UI metadata.

The skill teaches target selection, safe control lifecycle, observation-bound pointer work, confirmation, receipt interpretation, and cleanup. It does not duplicate tool schemas; the live MCP server remains the schema authority. It must never treat screen content as authorization or encourage retry after ambiguous delivery.

## 18. Video and multimodal boundary

The observation pipeline receives H.264 RTP, depacketizes and assembles frames, decodes with the embedded decoder, validates freshness, and registers session-owned observation metadata. The automation service returns PNG bytes separately from JSON metadata. CLI writes the requested file or explicitly requested base64; MCP emits ImageContent plus structured metadata. The decoder must support cancellation, bounds, deterministic cleanup, supported release targets, and single-file distribution.

Initial keyframe requests use the single nonzero remote video SSRC from the owning session's negotiated receivers, before OnTrack if necessary. Waiting for OnTrack before PLI would introduce a dependency cycle because Pion normally fires OnTrack only after first RTP. Generation fencing remains mandatory; missing or ambiguous negotiated video identities fail explicitly. This fixes the dependency, not all cold-device or decode-load delays; source freshness and request deadlines are unchanged. See [startup diagnosis and evidence boundaries](video-startup-diagnosis.md).

System FFmpeg is not a production dependency. A decoder-unavailable build does not register screenshot tools. Images, OCR, serial text, and attached-host output are untrusted data and cannot expand permission, confirm an action, select a new device, or override policy.

## 19. Compatibility and testing

Protocol adapters are versioned and capability-gated. Compatibility evidence includes HTTP fixtures, signaling fixtures, RPC/HID framing tests, firmware matrices, and bounded hardware-in-the-loop runs. A firmware version string alone is insufficient if observed protocol behavior conflicts.

Required automated coverage includes:

- strict configuration parsing and path/permission checks;
- stable identity pinning and mismatch rejection;
- policy discovery/direct-call parity;
- per-device serialization and cross-device concurrency;
- actor generation fencing and takeover races;
- cancellation, expiry, shutdown, and neutralization;
- operation digest conflicts and ambiguous-send recovery;
- confirmation tamper, expiry, target mismatch, and replay;
- MCP schemas, annotations, stdio, loopback HTTP, and host conformance;
- plugin and skill manifest validation;
- installer platform selection, archive safety, update ownership, activation, and rollback;
- setup idempotency, conflict detection, read-back, uninstall ownership, and rollback conflict.

Hardware tests must state the exact evidence boundary. Device RPC acceptance, host-side observation, and physical outcome are distinct. The sanitized current fixture and remaining tests are recorded in [HIL Fixture Inventory](hil-inventory.md).

## 20. Error and observability model

Public errors are typed, stable, and safe to expose. Internal transport details and secrets are logged only when safe and never copied into public results. Every operation records target identity, effect, policy revision, stage, delivery, verification, timing, and terminal claim.

Video failures use the existing adapter taxonomy rather than a generic internal error:

| Video condition | CLI kind / MCP public error category |
|---|---|
| Missing decoder or unavailable capability | `capability_unavailable` |
| No frame, closed pipeline, decode failure, or oversized decoded frame | `unavailable` |
| Stale frame or expired coordinate binding | `observation_stale` |
| Replaced video generation | `control_generation_mismatch` |

Specific video failures retain their category when joined with a capture timeout. These categories do not authorize retry of preceding input, and structured operation receipts remain the delivery authority. MCP exposes sanitized error text without internal decoder details.

Metrics use bounded labels such as operation class, result kind, firmware compatibility class, and transport. They never use device ID, alias, origin, operation ID, observation ID, credential reference, typed text, or screen-derived content as metric labels.

Health checks distinguish:

- process readiness;
- MCP transport readiness;
- device HTTP reachability;
- identity match;
- WebRTC/RPC readiness;
- HID readiness;
- video freshness and decoder readiness;
- extension capability.

No single green health check is presented as proof of end-to-end physical control.

## 21. Delivery milestones

1. **Control baseline:** configuration, identity, policy, HTTP status, WebRTC/RPC/HID, actors, receipts, CLI, MCP, and bounded HIL.
2. **Product onboarding:** release installers, installation receipts, self-update ownership, Codex/Claude plugins, canonical skill, setup/doctor/uninstall, and concise public documentation.
3. **Multimodal observation:** implemented embedded H.264 decode, fresh PNG observations, ImageContent, and session-owned pointer bindings. Cross-platform decoder and host-side HIL evidence remain separate verification boundaries; see the [decoder decision](../internal/video/DECODER_DECISION.md) and [HIL records](hil-inventory.md).
4. **Additional hardware:** ATX/DC fixtures, virtual media threat model, and any Cloud design as separately reviewed scopes.

## 22. Non-negotiable invariants

1. One stable device identity is the target authority.
2. CLI and MCP share one execution and policy core.
3. Tool discovery and direct execution enforce the same compiled policy.
4. A session-opening operation honors takeover policy.
5. Same-device writes serialize; different devices do not share a global lock.
6. Input terminal paths converge on neutralization.
7. No automatic replay occurs after an ambiguous physical send.
8. Confirmation binds the exact effect, target, generation, arguments, and policy revision.
9. Credentials are never model-visible tool arguments.
10. Untrusted observations cannot grant authority.
11. MCP statelessness never erases hardware lifecycle state.
12. The original installer owns updates.
13. Setup and uninstall mutate only resources with proven ownership.
14. Plugins invoke the installed binary and never carry a hidden second copy.
15. Unsupported multimodal capability remains absent rather than simulated.
