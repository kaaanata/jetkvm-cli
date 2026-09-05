# JetKVM HIL Fixture Inventory

Status: live read-only inventory
Observed: 2026-09-05
Scope: local HTTP/SSH plus a bounded WebRTC/RPC session and neutral HID reports; no key/pointer action, serial write, media mount, reboot, or persistent device configuration mutation

## Privacy boundary

This document intentionally omits the fixture's:

- LAN IP and MAC addresses;
- CPU serial and JetKVM device ID;
- hostname where it identifies the attached host;
- SSH public key, fingerprint, and local private-key path;
- Cloud account identity and device token;
- configuration fields containing credentials.

Those values belong in ignored local configuration or the local state database, not in the repository.

## Hardware and system

| Field | Observed value |
|---|---|
| JetKVM SKU | `jetkvm-v2` |
| Device-tree model | `JetKVM` |
| SoC family | Rockchip RV1106-compatible, ARMv7 |
| Kernel | Linux `5.10.160`, system build dated 2026-04-28 |
| JetKVM app | `0.5.8` |
| JetKVM system | `0.2.8` |
| Go runtime used for app build | `go1.25.1`, Linux/ARMv7, CGO enabled |
| Physical storage | approximately 16 GB eMMC |
| Root filesystem | approximately 488 MB, 6% used |
| Userdata filesystem | approximately 13.2 GB, 2% used |
| RAM | approximately 199 MB total, 159 MB available at observation |

The installed app binary identifies the upstream module as `github.com/jetkvm/kvm` and includes Pion WebRTC v4.2.1-era dependencies. The installed binary hash and device-specific build metadata are kept out of this repository.

## Access and network

| Capability | Observed state |
|---|---|
| Local HTTP | reachable |
| Device setup | complete |
| Local auth mode | `noPassword` |
| Local TLS | disabled |
| SSH Developer Mode | enabled |
| SSH daemon | Dropbear 2025.88 |
| SSH authentication | root public-key authentication |
| Authorized SSH keys | one entry |
| IPv4/IPv6 | both present |
| JetKVM Cloud | connected to official Cloud |
| Tailscale | no running process observed |

The fixture is suitable for isolated-LAN development. Its current no-password/plain-HTTP configuration must not be treated as the production security baseline.

The running Dropbear daemon accepts public-key SSH successfully. Invoking `/sbin/dropbear -V` in a new shell reports a missing `libz.so.1`; this is a packaging/runtime diagnostic quirk, not evidence that the already-running daemon is unavailable.

## Video

| Field | Observed state |
|---|---|
| Capture chip | Toshiba TC358743 |
| Capture sleep state | awake |
| Runtime video state | active and connected |
| Current signal | 1920x1080 at 60 fps |
| Video device nodes | `/dev/video0` through `/dev/video10` present |

A later bounded HIL run established and cleanly closed the implemented WebRTC control session. The visual-control candidate subsequently negotiated H.264, decoded a 1920x1080 IDR, and returned PNG screenshots through the CLI and native MCP ImageContent. Captures remain private ignored files.

## USB gadget

| Field | Observed state |
|---|---|
| UDC state | `configured` |
| UDC speed | high-speed |
| HID functions | three, exposed as `/dev/hidg0..2` |
| Mass storage function | enabled |
| Mass storage mode | read-only CD-ROM |
| Mounted media | none |

This proves the gadget is configured, not that every attached-host BIOS/OS accepts all HID and media operations. HID and media HIL require host-side observation.

## Extension port

| Field | Observed state |
|---|---|
| Active extension | `serial-console` |
| UART device | `/dev/ttyS3` |
| UART owner | JetKVM app process |
| Custom serial settings file | absent |
| Effective defaults | 115200 baud, 8 data bits, no parity, 1 stop bit |
| Default terminator | LF |
| Echo | disabled |

No serial bytes were read or written. ATX and DC extension behavior cannot be validated using the fixture in its current serial-console configuration; those tests need the extension to be swapped or a separate fixture.

## Runtime health observation

At inventory time:

- uptime was roughly four hours;
- load average was roughly 9–10;
- instantaneous CPU was roughly 73% idle with 0% I/O wait;
- memory and filesystems had ample headroom;
- the app and native video processes were running;
- several Rockchip video/VENC/VPSS kernel workers were in D state.

The high load correlates with the video-driver D-state workers and is not, by itself, proof of CPU or storage saturation. HIL health checks must combine load average with CPU idle, I/O wait, process state, frame freshness and RPC readiness.

## Current verification matrix

| Area | State | Evidence boundary |
|---|---|---|
| HTTP identity/setup | verified | live GET responses |
| SSH diagnostics | verified | root public-key session |
| Cloud connected state | verified | local Cloud state endpoint |
| Hardware SKU/system | verified | device files and kernel interfaces |
| Video signal metadata | verified | running native process and capture device state |
| WebRTC signaling/RPC | verified | command-scoped owned session and live `getActiveExtension` RPC |
| Screenshot decode | verified at 1920x1080 | real H.264 IDR decoded with the embedded WASI codec; CLI PNG and MCP ImageContent decoded and visually inspected |
| HID neutralization | transport verified | neutral keyboard/absolute/relative reports accepted and flushed; host-side effect not independently observed |
| Pointer actions | transport verified | CLI move/click/double-click/drag/scroll and final batch screenshot; MCP observation-bound click with post-action ImageContent; accepted non-retryable receipts and neutralization checked |
| Pointer host-side GUI effect | verified in a bounded GUI test | observed search-field focus and window drag, followed by restoration of window position; screenshots remained private |
| Keyboard host-side effect | verified in a bounded GUI test | short search text appeared once after duplicate operation lookup; confirmed CMD+A and Backspace cleared the test text |
| Serial receive/transmit | not tested | no serial I/O performed |
| Virtual media mount | not tested | gadget exists but no image was mounted |
| ATX/DC power | unavailable on current setup | serial-console is active extension |
| Cloud remote control | not tested | Cloud account exists, first release remains local-first |

## Next HIL steps

1. Extend decoded-frame coverage to additional resolutions, signal loss/recovery, firmware profiles, and concurrent physical devices.
2. Use a disposable graphical host target to verify visible pointer selection/drag effects and keyboard text, in addition to accepted transport receipts and fresh screenshots.
3. Validate serial RX/TX with a disposable loopback or test target.
4. Add or swap to dedicated ATX/DC hardware before enabling destructive power HIL.

## MCP regression acceptance (2026-09-05)

The local main candidate was exercised through its actual executable and the official Go MCP SDK over stdio. Modern discovery selected `2026-07-28`; the server exposed 21 configured tools. The final three consecutive runs passed device/configuration reads, exact-target takeover elicitation, video/input control open, control readback, both native PNG observation tools at 1920x1080, Escape plus a bounded wait with post-action capture, operation deduplication, and clean control closure. The operator's persistent confirmation policy remained enabled.

Manual MCP acceptance additionally exercised click, move, double-click, drag, scroll, text, CMD+A, and Backspace. Search text, focus, and window displacement were visually observed. Test text was cleared and the window position restored. Scroll and double-click receipts establish transport acceptance; their host-side semantic effect was not independently asserted. Expired observation bindings and expired controls rejected input with `not_sent` receipts.

Competing browser sessions interrupted earlier attempts, including a session with an ambiguous input receipt and unconfirmed neutralization. That action was not replayed. After the operator closed the competing browser, independent new sessions passed. These tests do not establish simultaneous browser/MCP control, power behavior, multi-device hardware coverage, or signal-loss recovery. The installed release and the tested local candidate are distinct artifacts.

Run the confirmed binary suite with `JETKVM_HIL_MCP=1`, an absolute `JETKVM_HIL_BINARY`, a private single-device `JETKVM_HIL_CONFIG`, and a private `JETKVM_HIL_SCREEN` output path. `JETKVM_HIL_INPUT=1` additionally opts in to the bounded Escape/wait batch. Use `go test ./internal/app -run '^TestHILMCPBinary$' -v -count=1`. Close competing browser control sessions first.

## Automatic wake candidate acceptance

The post-1.0.7 local candidate distinguishes firmware sleep from `no_signal`. With existing input permission, an ordinary CLI screenshot automatically sent one Shift press/release and returned a fresh 1920x1080 PNG with an accepted, neutralized wake receipt. In an input/video MCP session, `disable_wake: true` returned `video_no_signal`; the following ordinary observation call automatically woke the host and returned native PNG content plus its wake receipt. Both observation aliases were exercised, and the owned control closed cleanly. The attached host remained locked; no credential entry or login was performed. This candidate behavior is not part of the 1.0.7 release artifact.
