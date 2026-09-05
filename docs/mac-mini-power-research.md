# Mac mini power control through JetKVM

Research date: 2026-09-05. This is a source-based feasibility study, not a claim that additional power hardware or firmware has been installed. No target commands or physical power tests were performed for this study. Product contracts remain in `docs/design.md`.

## Findings

JetKVM can transport a USB keyboard power usage, and its ATX serial protocol can hold an output for a custom duration. The current high-level RPC exposes less than those underlying mechanisms. Neither finding proves that a USB report can substitute for the Apple Silicon physical power button during cold boot.

| Desired outcome | Available or plausible path | Evidence boundary |
| --- | --- | --- |
| Graceful shutdown while macOS runs | Use normal keyboard/pointer UI; a future authenticated host adapter could request OS shutdown | Depends on OS responsiveness, authorization, and dialogs; input acceptance is not shutdown proof |
| Wake a sleeping Mac/display | Ordinary HID input or JetKVM USB remote wake | Existing mechanisms; sleep differs from shutdown |
| USB power-key event while the OS is running | Keyboard page `0x07`, usage `0x66`; separate System Control support is also implementable | Existing keyboard transport can encode it; exact Mac behavior needs controlled testing |
| Normal startup after shutdown | Physical button actuator; on supported newer Macs, AC restoration with Apple's configured startup policy | Apple documents the AC policy; specific fixture setting and external switching remain unverified |
| Startup Options from shutdown | Sustain the actual power-button action through boot | Apple documents physical-button hold; USB equivalence remains unproven |
| Force shutdown of an unresponsive host | Physical button actuator, or separately authorized external power removal | Different effects; removing AC is not a button hold or graceful shutdown |

## USB: what the existing firmware can actually send

The inspected [JetKVM source](https://github.com/jetkvm/kvm/tree/6d4843eb44555b4915d88ee50fcf356855ce811d) declares an eight-byte boot-keyboard report with Keyboard/Keypad page `0x07` and an array usage maximum of `0xff`. `KeyboardReport` pads/truncates to six keys and writes their bytes; it does not exclude `0x66`. The JSON-RPC `keyboardReport` and HID-RPC keyboard-report paths reach this writer. Thus **Keyboard Power is encodable without adding a new USB interface**. A CLI symbolic-key omission is a client limitation, not proof of firmware inability.

Source: [descriptor and report writer](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/internal/usbgadget/hid_keyboard.go), [RPC bridge](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/usb.go), [HID-RPC dispatch and keepalive](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/hidrpc.go).

A sustained report needs the existing HID-RPC `0x09` keepalive and final zero report. The upstream keepalive targets 50 ms intervals and rejects stale arrivals; sending key-down and merely sleeping for ten seconds does not establish a ten-second hold. A transport receipt still cannot establish physical button state.

These USB usages must remain distinct:

| Usage | Meaning and implementation |
| --- | --- |
| Keyboard/Keypad `0x07:0x66` | Keyboard Power in an existing keyboard report |
| Generic Desktop `0x01:0x81` | System Power Down within System Control collection `0x01:0x80`; requires an appropriate descriptor/report path |
| Consumer `0x0c:0x30` | Consumer Power; a different usage page and report contract |
| Vendor `0xff00:0x01` | JetKVM's dedicated wake interface; not any of the power usages above |

The [USB-IF HID Usage Tables 1.7](https://www.usb.org/sites/default/files/hut1_7.pdf), sections 4.5.1, 10 and 15, define the standard usages. In particular System Power Down is a one-shot system power request, not a standardized ten-second physical-switch assertion. Report bytes obtain their usage page from the descriptor; inserting `0x81` into the keyboard array does not emit Generic Desktop System Power Down.

JetKVM's wake interface sets `wakeup_on_write=1`. `rpcWakeHost` returns without writing when USB is not attached and otherwise sends three vendor-report pulses. Its success therefore must not be reported as proof of cold startup. `rpcHidReport` also returns nil when USB is not ready and suppresses transient rebind errors; this reinforces the need for readiness and independent outcome evidence.

Apple's open-source [IOHIDFamily at 777ccd9](https://github.com/apple-oss-distributions/IOHIDFamily/tree/777ccd9698845aadf711e32d843c8c9b777431d9) recognizes these power-related usages. [IOHIDConsumer.cpp](https://github.com/apple-oss-distributions/IOHIDFamily/blob/777ccd9698845aadf711e32d843c8c9b777431d9/IOHIDFamily/IOHIDConsumer.cpp) maps Consumer and Generic Desktop power events to `NX_POWER_KEY`; [IOHIDEventDriver.cpp](https://github.com/apple-oss-distributions/IOHIDFamily/blob/777ccd9698845aadf711e32d843c8c9b777431d9/IOHIDFamily/IOHIDEventDriver.cpp) parses Keyboard Power, with special semantics for feature reports. These are OS input paths, not evidence about the Apple Silicon boot ROM, always-on power controller, or a particular installed OS build.

USB power, USB enumeration, host polling, remote-wake eligibility and host power state are separate facts. An independently powered KVM can remain online while the host's USB controller stops servicing reports. Conversely, USB VBUS can remain present without an active configured HID data path. The [Linux USB gadget lifecycle](https://docs.kernel.org/driver-api/usb/gadget.html) documents enumeration and endpoint activation. Exact shutdown-state USB behavior must be measured on the target model; this research does not assert that every Mac mini disables every USB port at shutdown.

## ATX: custom duration is feasible, but needs a physical route

[`rpcSetATXPowerAction`](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/jsonrpc.go) chooses 200 ms for short press and five seconds for long press. However, [`pressATXPowerButton(duration)`](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/serial.go) sends `BTN_PWR_ON`, waits the supplied duration, and sends `BTN_PWR_OFF`. Five seconds is a high-level RPC choice, not a fundamental serial-protocol or GPIO limit.

The [ATX MCU firmware at 9118d40](https://github.com/jetkvm/atx-extension-firmware/blob/9118d405096df578f3401925451f89ac7ab1a860/jetkvm-atx.c) handles independent on/off commands and reports button-output and LED states. Its watchdog is refreshed by the main loop even when UART is disconnected. It does **not** provide a per-press expiration or serial-disconnect release, so an RPC with a larger sleep alone is insufficient for reliable bounded actuation.

A suitable extension should own a bounded timed press in the MCU, expire it independently of the network/daemon, release on reset, serialize competing requests and return identity/timing/release evidence. The KVM daemon should expose a versioned capability and typed bounded RPC. CLI and MCP should share the existing policy, operation ledger, generation fence and non-retry-safe action semantics. Report `button_hold`, `keyboard_power`, `usb_wake` and `power_supply_switch` as different routes; do not turn a configured route into proof that the Mac is wired to it.

The test fixture has been reported as using the Serial Console extension, not a verified Mac power-button connection. An ATX board does not automatically acquire control of the Mac button. A non-invasive mechanical actuator pressing the real button is a plausible route; a professionally verified isolated contact connection is another. Both require mechanical/electrical validation. No unverified Mac internal pinout or mains modification is proposed here.

## A documented alternative for ordinary cold startup

Apple now explicitly supports **Start up when power is connected → Always** on Mac mini models introduced in 2024 or later with macOS 26.5 or later. It works on connection/restoration of power; Apple recommends about 30 seconds between disconnecting and reconnecting power after shutdown. This enables a possible normal-startup route using a separately managed AC outlet and an independently powered JetKVM. The exact model, OS and configured setting must first be checked. [Apple support](https://support.apple.com/en-ca/125517).

JetKVM's current [DC extension](https://jetkvm.com/products/dc-power-control) switches 12–20 V DC. The [2024 Mac mini](https://support.apple.com/en-us/121555) accepts 100–240 V AC. Therefore the standard DC extension is not directly compatible with the Mac's AC inlet. An AC outlet adapter would be a distinct supported integration, not the existing DC command renamed.

Apple also documents [scheduled startup using pmset](https://support.apple.com/guide/mac-help/schedule-your-mac-to-turn-on-or-off-mchl40376151/mac). That requires prior OS configuration and is not an arbitrary off-state JetKVM command. Neither scheduled startup, AC restoration nor Wake-on-LAN proves entry to Recovery.

Apple's [Startup Options instructions](https://support.apple.com/en-ie/102603) require maintaining the power-button press during startup and releasing when the options screen appears. No primary source examined here establishes an external USB power usage as an equivalent on Apple Silicon.

## Smallest useful validation sequence

1. Read model/OS and actual USB descriptors/configuration; establish independent KVM power. Do not infer them from the hostname.
2. On a disposable/test-ready running host, separately test Keyboard Power press/release and bounded hold, observing host HID events and screen outcome. Do not begin with Recovery or force-shutdown combinations.
3. During an authorized normal shutdown, record KVM reachability, USB configured/suspended state and host-side power. If USB is unconfigured, classify cold USB power as unavailable for that state instead of treating a nil RPC result as success.
4. For routine cold startup, validate the documented AC startup setting and an independently managed outlet, with normal shutdown completed before power removal.
5. For Recovery, validate a real-button actuator's duration and independent release on a bench first; include daemon loss, UART loss, cancellation and duplicate requests. Only then test Mac Startup Options with video confirmation.

Until these tests pass, expose transport capability and readiness precisely, and leave Mac-specific physical outcomes unverified. This research adds no installed capability and changes no public API.
