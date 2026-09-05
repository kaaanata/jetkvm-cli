# USB-PD hardware paths for JetKVM

Research date: 2026-09-05. This is a feasibility report, not a shipped capability or a hardware acceptance result. No target connection, firmware installation, power action, or purchase was performed.

## Findings

An independent USB-PD adapter is a concrete engineering path: open implementations already send Apple vendor messages using FUSB302 hardware. It does not require emulating a keyboard and does not require another Apple computer. However, the inspected stock JetKVM software exposes a USB gadget path, not a programmable PD transport. No evidence establishes that changing JetKVM's HID reports can transmit PD messages, or that the existing Apple reboot messages reproduce a physical power-button hold or paired 1TR entry.

The useful product abstraction is a separately identified power/debug transport attached to the same managed computer. A JetKVM can continue supplying HDMI observations and HID input while a dedicated PD adapter supplies explicitly supported reset/debug actions. Their shared target lifecycle must remain under one domain authority.

## Stock JetKVM evidence and its limits

The inspected application revision is `jetkvm/kvm@6d4843eb44555b4915d88ee50fcf356855ce811d`. Its USB implementation creates Linux configfs gadgets and HID functions. The wake RPC invokes gadget wake reports. Neither is an interface to the USB-C configuration channel. [Application USB implementation](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/usb.go), [gadget implementation](https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/internal/usbgadget/usbgadget.go).

At `jetkvm/rv1106-system@b537585a805cf302ec35444a91c09faec115eb54`, the JetKVM board DTS sets DWC3 to peripheral mode. Its board-specific I2C devices are an EEPROM, touchscreen, and HDMI capture chip; the inspected board DTS/DTSI do not describe a Type-C port controller. The board defconfig enables USB gadget/configfs and I2C, but has no explicit TYPEC/TCPM/FUSB302 selection. This establishes an absent supported software path in the inspected board configuration. It does **not** prove the physical absence of every possible PD component or undocumented test pad. [Board DTS](https://github.com/jetkvm/rv1106-system/blob/b537585a805cf302ec35444a91c09faec115eb54/sysdrv/source/kernel/arch/arm/boot/dts/rv1106g-jetkvm-v2.dts), [board DTSI](https://github.com/jetkvm/rv1106-system/blob/b537585a805cf302ec35444a91c09faec115eb54/sysdrv/source/kernel/arch/arm/boot/dts/rv1106-jetkvm-v2.dtsi), [defconfig](https://github.com/jetkvm/rv1106-system/blob/b537585a805cf302ec35444a91c09faec115eb54/sysdrv/source/kernel/arch/arm/configs/rv1106-jetkvm-v2_defconfig).

No authoritative main-board schematic/BOM was located in the public JetKVM repositories searched. Therefore the exact CC1/CC2 resistor values, routing, and presence of a programmable controller remain unverified. Reporting that CC is definitely tied through particular resistors would exceed the evidence. Resolving that question requires a matching board schematic or physical board inspection; neither was available in this read-only task.

The documented USB-C splitter separates data from a separate 5 V supply. That is a power-continuity solution, not documented PD message injection. Its documentation makes no claim that the splitter forwards arbitrary VDMs. [JetKVM power options](https://jetkvm.com/docs/peripheral-devices/alternative-power-sources).

## Implementations that actually provide a PD transport

| Path | Verified implementation boundary | What is still missing for this product |
| --- | --- | --- |
| `macvdmtool` on another Apple Silicon Mac | Uses Apple's HPM plugin/controller commands to send VDMs; supports reboot, DFU and debug routing | A constrained adapter with explicit port/target selection and receipts; not a portable JetKVM executable |
| `tuxvdmtool` on Apple Silicon Linux | Uses the CD321x controller's I2C register/command interface | Not a generic Linux USB-C API; no FUSB302 or RV1106 backend in this tool |
| Asahi `vdmtool` + Arduino + FUSB302 | Sends/receives custom PD VDMs using a separate PD PHY | Research firmware, automatic initialization effects, weak machine API |
| Central Scrutinizer + Pico + FUSB302 | Complete open board/firmware with Mac hard reset and serial routing | Product API, read-only discovery, exact model acceptance and sourcing |

`macvdmtool` discovers `AppleHPM`, loads `AppleHPMLib`, and uses the controller command `VDMs`. This is privileged access to Apple's PD controller through IOKit; it is not a request transmitted through a generic USB HID endpoint. Replacing that dependency means implementing another PD-controller transport, not renaming keyboard usages. [macvdmtool source](https://github.com/AsahiLinux/macvdmtool/blob/main/main.cpp).

At `tuxvdmtool@4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee`, `main.rs` opens an I2C bus and constructs `cd321x::Device`; `cd321x.rs` sends the `VDMs` controller command. Connector lookup selects an I2C device, not a universal USB adapter. Its README explicitly describes Apple-Silicon-to-Apple-Silicon operation. [tuxvdmtool](https://github.com/AsahiLinux/tuxvdmtool), [main](https://github.com/AsahiLinux/tuxvdmtool/blob/4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee/src/main.rs), [controller implementation](https://github.com/AsahiLinux/tuxvdmtool/blob/4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee/src/cd321x.rs).

Asahi's Arduino `vdmtool` documents explicit CC1/CC2, I2C, interrupt and supply wiring to FUSB302. It exposes custom VDM input through a serial terminal and includes captured request/response examples. Its startup performs PD negotiation and requests serial muxing automatically: merely starting this firmware is not a read-only probe. The documented Reclaimer Labs breakout does not connect SBU1/SBU2, so obtaining serial also needs that wiring. [Asahi vdmtool](https://github.com/AsahiLinux/vdmtool).

FUSB302B is an actual PD physical-layer controller, with CC attach/orientation support and host-controlled packet transport. The vendor lists automatic packet handling and reference software. A generic USB-to-I2C adapter alone does not supply the CC/BMC physical layer; an FUSB302-class PHY and correct electrical design are still necessary. [onsemi FUSB302B](https://www.onsemi.com/products/interfaces/usb-type-c/fusb302b).

## Central Scrutinizer: stronger evidence than a theoretical PHY

The author's hardware report describes working Mac reset and serial access with Pico/FUSB302, including board revisions that correct CC orientation and SBU routing. This is first-hand hardware evidence, not a prediction derived only from a datasheet. The project page currently states that previously sold leftover boards are gone. It provides open design sources; that is a buildable design, not evidence of a presently available turnkey purchase. [Author's project](https://hackaday.io/project/192826-central-scrutinizer-a-serial-adapter-for-m1m2m3), [author's build log](https://hackaday.io/project/192826/log/223997).

The inspected `cs-sw@b51d9e090b90fe5224f5fab22a3d7c303095cb73` implements `vdm_send_reboot` and a command to reset the target. A DFU sequence appears in a comment, not as an exposed DFU command; it must not be counted as a tested DFU feature of this revision. `vdm_ready` also claims serial automatically. A product backend should change that default before treating enumeration as non-mutating. [Firmware source](https://git.kernel.org/pub/scm/linux/kernel/git/maz/cs-sw.git/tree/vdmtool.c?id=b51d9e090b90fe5224f5fab22a3d7c303095cb73).

The Pico connects to a management host for USB CDC control and power. The board has a separate target USB-C connection and optional USB2 pass-through. SBU serial needs the correct target port, cable conductors and signal voltage conversion. Its documentation explicitly distinguishes alternate serial wiring from USB pass-through. Consequently a generic hub, charge trigger or ordinary passive USB adapter is not a documented substitute. [Firmware README](https://git.kernel.org/pub/scm/linux/kernel/git/maz/cs-sw.git/tree/README?id=b51d9e090b90fe5224f5fab22a3d7c303095cb73), [board sources](https://git.kernel.org/pub/scm/linux/kernel/git/maz/cs-hw.git/).

Cable requirements depend on the operation: SBU serial requires SBU conductors, while a PD VDM travels over CC. macvdmtool separately documents USB2 debug mode. Do not generalize a serial-cable requirement into a claim that every VDM requires SuperSpeed, or generalize an Apple restore cable recommendation into a serial-routing specification. [macvdmtool usage](https://github.com/AsahiLinux/macvdmtool#usage).

## Proposed backend boundary

The following is a design proposal, not a public API commitment:

1. **Separate transport identity.** Store a power route alongside the managed computer: backend type, controller serial/identity, exact port, expected peer identity evidence, and operator-attested cabling association. The JetKVM hardware ID identifies the KVM, not automatically the Mac on a different PD cable. Apple vendor ID alone is not a unique target pin.
2. **Discovery without effects.** Enumerate controller/firmware/port and read current attachment state. Do not invoke research tools whose initialization unlocks controllers, resets ports or claims a serial mux. Split passive local discovery from an explicitly authorized active protocol probe.
3. **Honest capabilities.** Keep `pd.reset`, `pd.enter_dfu`, `debug.serial`, `physical_power_hold`, and `recovery.paired_1tr` separate. An adapter implementation and a target-observed result are different evidence. Absent unique target evidence must be reported as an enrollment limitation, not silently replaced by a display alias.
4. **One target owner.** Route CLI/MCP through the existing policy, actor and operation ledger. An operation locks both the managed target and the exact PD controller/port across processes. Serialize disruptive power operations with video/HID operations, neutralize any existing input, and invalidate stale observation/control generations when target reset begins. Independent targets may progress concurrently.
5. **Bounded actions.** Expose named supported operations, not raw VDM words, arbitrary I2C/register access or generic serial commands. Apply the configured confirmation setting without changing power permissions. Bind the device, adapter/port, action, policy revision, generation and operation ID at execution time.
6. **Receipts that distinguish layers.** Record command submission, adapter acknowledgement, PD response, disconnect/reconnect and separately observed host outcome. A successful serial write or PD acknowledgement is not proof of reboot, DFU, or paired 1TR. If delivery becomes ambiguous, do not repeat the logical action. Hardware PD link-layer retransmission must be handled separately from application-level command retry.
7. **Terminal ownership.** Reset has no keyboard key to release. Cleanup closes its adapter lease and restores only mux state actually owned by the operation, with a bounded, reported outcome. Do not invent `neutralized=true` for a transport that did not assert HID; retain preceding HID cleanup as separate evidence.
8. **Independent power and wiring.** Keep JetKVM, PD adapter and management host alive during target shutdown. Prefer separate target ports for HID and PD in an initial fixture. An inline solution must explicitly own CC negotiation, VBUS policy, orientation and data routing; it cannot be a casual Y-cable combination.

## Acceptance gates and remaining questions

First validate passive adapter enumeration and reconnect behavior without sending target commands. Then validate one authorized named reset on a disposable or prepared target, recording independent boot evidence and cancellation/ambiguous-delivery behavior. DFU requires its own USB readback. Cold start after shutdown and paired 1TR require separate experiments with model/firmware-specific proof; neither follows from reset support.

For stock JetKVM, the remaining hardware question is the actual CC net/controller inventory. For an external adapter, the remaining product work is a non-mutating startup, bounded machine protocol, reliable target association and audited ownership across HID/video/PD. The available sources make an external PD reset/debug backend practical to investigate. They do not yet justify advertising a remotely held Mac mini power button or paired Recovery entry.
