# Mac mini power: public experiments and forum evidence

Research date: 2026-09-05. This supplements [the protocol/source study](mac-mini-power-research.md). No device connection, HID input, power operation, firmware change, or target configuration change was performed. This report adds no verified product capability.

## Result

There is first-hand evidence that an external legacy keyboard power button **does not cold-start an M4 Mac mini**, and separate evidence that external keyboard power events can affect **sleep/wake while macOS is available**. Those results are compatible, not contradictory. No examined first-hand report demonstrates a generic USB HID power usage held for a custom duration entering Apple Silicon **paired One True recoveryOS (1TR)** from shutdown.

For Recovery work requiring Boot Policy changes, the distinction is stronger than merely reaching an Options screen: Apple documents a physical single-press-and-hold boot path, and Asahi checks whether the resulting environment actually has 1TR authority. Publicly built remote Mac mini systems solve that requirement with a physical button actuator. The defensible product position is therefore: transportable Keyboard Power is useful to investigate, but must not be advertised as an Apple Silicon Recovery button.

## First-hand reports and their limits

| Date and source | Hardware / software stated | Action and reported result | What it establishes |
| --- | --- | --- | --- |
| 2026-05-21/22, [r/macintosh: Mac Mini + M2452](https://www.reddit.com/r/macintosh/comments/1tjazw9/question_mac_mini_m2452/) | A responder explicitly identifies an M4 mini and the Apple M2452 USB keyboard; OS version unstated | Owner says they used that keyboard until recently and its power button would not turn on the mini. Another owner reports the same, without specifying mini generation. | Relevant first-hand **cold-start negative**, but no descriptor capture, hold duration, or firmware version. Does not test every HID power page. |
| 2025-10-17, [r/VintageApple: Vintage Apple KB power button](https://www.reddit.com/r/VintageApple/comments/1o7b1cr/) | M1 MacBook Air, macOS 26, ADB-USB Wombat; also a G3 iMac keyboard | Wombat-connected ADB keyboard works for typing but its power key does nothing. G3 USB keyboard power button wakes/sleeps the MacBook, without the expected power-options dialog. | **Positive runtime/sleep evidence**, and adapter-dependent behavior. Not a mini and no cold start or 1TR test. The top post's successful power dialog lacks a precise machine identification. |
| 2026-04-13, [JetKVM issue #1411](https://github.com/jetkvm/kvm/issues/1411) | M4 Pro Mac mini, macOS Tahoe; JetKVM app 0.5.6, system 0.2.7, no extension | After inactivity, HDMI turns off while USB still supplies power. Keyboard/mouse, WoL, and JetKVM reboot do not wake it; physical power button does. | Direct **sleep-wake failure** for that setup. USB electrical power does not prove useful HID wake readiness. Not evidence about off-state power or current firmware fixes. |
| 2023-04-10/12, [r/pikvm: Confirm PiKVM 3 can enter Recovery on M1?](https://www.reddit.com/r/pikvm/comments/12h8iks/) | Mac mini M1/M2; kernel-extension development; OS unstated | Requester needs Recovery after kernel boot loops. Discussion identifies physical-button adaptation/Fingerbot; another developer reports the same need despite HID macro tooling. | Highly matching use case, but a **request/discussion**, not a successful USB power experiment. |
| 2021-04-30, [PiKVM issue #289](https://github.com/pikvm/pikvm/issues/289) | M1 Mac mini, macOS 11.3, PiKVM 2.59-1 | Recovery is already entered using power hold. Remote pointer moves but Recovery refuses to proceed past its missing-mouse screen. | KVM interaction **after entry** and Recovery peripheral compatibility are independent of the power-entry path. Does not demonstrate remote power hold. |
| 2023-09-20, [Apple Community: M1 mini cannot enter Recovery](https://discussions.apple.com/thread/255139624) | M1 mini, Ventura 13.5.2, external LG monitor | Original poster confirms success after full shutdown, a pause, USB keyboard/mouse, and sustained press on the Mac's button. | First-hand **physical-button positive** on M1. A keyboard being attached is not evidence that its power usage caused entry. |
| 2025-11-09; replies 2026-01-16, [r/jetkvm: macOS Recovery](https://www.reddit.com/r/jetkvm/comments/1osls7p/macos_recovery/) | Replies mention 2017/2019 iMacs, OS and procedure unstated | User reports inability to enter Recovery with JetKVM; another mentions Fingerbot. | These are **Intel machines**, so this cannot settle the Apple Silicon power-hold question. |

Forum testimony has incomplete controls. In particular, none of the USB reports includes a USB descriptor, decoded report, sustained-down timing, release trace, or a readback proving 1TR. Their value is separating plausible outcomes and avoiding false generalization, not certifying a new capability.

## Working physical-button implementations

[Yangyu Chen's M1 Mac mini PiKVM build](https://blog.cyyself.name/pikvm-m1-mac-mini/), published 2022-07-16, is a particularly close first-hand example: a kernel-testing machine needing remote recovery after panics. The author uses PiKVM plus a servo finger actuator and a light sensor for the Mac's power LED. Its configuration has a one-second short press and a fifteen-second Recovery press, with a twenty-second maximum. The article includes a Recovery-entry demonstration. The precise macOS build is unstated. This supports the **physical actuation and custom-duration path**, not an external USB power equivalence. Its timing is a single implementation example, not a universal requirement or a change to this CLI's existing twelve-second HID hold limit.

[Ivan Kuleshov's KVMac16 build](https://uplab.pro/2023/11/kvm-rack-stand-for-mac-minis-kvmac16/), published in November 2023, describes a deployed remote Mac mini rack using PiKVM, a servo HAT, and scripts to press physical buttons. It documents multiple working mechanical revisions and explains why Recovery drives the need for button access. The article does not give a complete chip/OS inventory for all sixteen machines. This is an actual implementation account, rather than a recommendation to buy an untested button gadget. The author subsequently favors replacing the button connection for density/reliability; that is a separate electrical integration, not a standard Mac ATX header.

These examples justify designing a general timed physical-button backend. They do not prove that a consumer Fingerbot, an arbitrary ATX board, or the current test fixture has the necessary wiring, timing, and independent release behavior.

## Why a Recovery screenshot is insufficient

[Apple Platform Security](https://help.apple.com/pdf/security/en_US/apple-platform-security-guide.pdf), in LocalPolicy manifest properties, defines paired 1TR as recoveryOS reached by a physical power-button single-press-and-hold. It distinguishes ordinary Recovery reached through NVRAM, a different button gesture, or boot failures. The physical gesture increases trust that software running in the normal OS did not reach that environment on its own.

[Asahi's platform introduction](https://asahilinux.org/docs/platform/introduction/) explains that 1TR has additional Secure Enclave-granted capabilities required for custom-kernel installation. [The actual installer second stage](https://github.com/AsahiLinux/asahi-installer/blob/main/src/step2/step2.sh) checks the boot-policy utility's output for 1TR and rejects the wrong mode. Its diagnostic specifically warns that tapping then pressing again can display the boot picker without providing the required mode.

Consequently, a future actuator HIL needs two separate receipts: observed physical actuation/release, and independently verified intended boot environment. Merely seeing Options, receiving an accepted HID report, or successfully decoding HDMI does not establish 1TR authority. This is an inference from the documented boot distinction, not a claim that this report has tested the target's Secure Enclave.

## A real USB-C alternative, with a different boundary

The search also found an important exception to a blanket claim that USB cannot control Mac power: [Asahi's USB-PD research](https://asahilinux.org/docs/hw/soc/usb-pd/) documents Apple vendor-defined messages over the Type-C **CC** line. Its M1 mini traces include reboot action `0x105` and DFU/hold action `0x106`. These require PD-controller capabilities and suitable packet tokens; they are not USB HID reports. The notes report that a hard shutdown normally disables PD communications on the examined mini, with a special persistent debug mode as an exception, and explicitly leave some behavior unresolved.

[macvdmtool](https://github.com/AsahiLinux/macvdmtool) implements remote reboot, serial/debug USB, and DFU using another Apple Silicon Mac. [tuxvdmtool](https://github.com/AsahiLinux/tuxvdmtool) provides a Linux counterpart with access to the relevant Type-C controller. Neither examined command contract promises physical-button assertion or paired 1TR entry. Therefore PD reset/DFU is a potentially useful future **distinct backend**, not a discovered solution to the requested Recovery hold. A JetKVM keyboard name or HID descriptor extension alone cannot implement it.

## Confidence and next product decision

| Claim | Confidence and basis |
| --- | --- |
| A custom HID key hold can encode Keyboard Power without proving physical power-button state | High for the distinction; protocol/source basis remains in the companion report |
| External keyboard power may affect a running/sleeping Apple Silicon Mac | Moderate: concrete M1/macOS 26 testimony, but model/adapter-dependent and not reproduced here |
| M2452 power does not cold-start the reported M4 mini | Moderate: explicit first-hand owner report, incomplete test conditions |
| Generic USB HID power long-hold is a verified M-series mini 1TR route | **Not established**; no qualifying positive evidence found |
| A timed real-button actuator is a feasible remote M1 Recovery route | High: first-hand implementation with timing/configuration/demo, consistent with Apple/Asahi boot contracts |
| Apple's PD vendor messages provide reboot/DFU control | High for the documented research setup; compatibility on other models/firmware is unverified |

For a general CLI/MCP, keep `keyboard_power`, USB remote wake, a timed physical button, AC switching, and vendor PD reboot/DFU as separate effects and readiness paths. Expose an outcome such as Recovery only when independently observed; do not infer it from an action's name. A future physical-button extension needs its own maximum duration and autonomous release contract, rather than inheriting the HID hold maximum. The current release should not wait for speculative USB-to-1TR support and should not claim it.

Searches covered JetKVM/PiKVM GitHub issues and discussions, Reddit's JetKVM, PiKVM, Mac, MacOS, Mac mini, Mac sysadmin, VintageApple, and Asahi communities, Apple Community, and first-hand projects linked from those discussions. Search coverage is not proof of impossibility, especially for undocumented vendor firmware paths. No passwords, target identifiers, or private fixture data are included.
