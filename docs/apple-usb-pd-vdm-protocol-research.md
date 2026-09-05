# Apple USB-PD VDM protocol research

Research date: 2026-09-05. This is a source and public-report review, not device acceptance. No Mac, JetKVM, PD controller, or serial device was operated. No product capability is added by this document.

## Findings

Apple's VDM path is a credible separate backend for remote reset, DFU entry, and debug routing. The reviewed implementations do not expose a physical power-button assertion, a duration parameter, or a corresponding button release. Neither a DFU acknowledgement nor a boot-picker image establishes paired One True recoveryOS (1TR).

The most useful additional protocol lead is a commented experimental vector in the original Arduino implementation: action `0x0105` with argument `0x80020000`, labeled `PMU Reset + DFU Hold`. This deserves further investigation, but it is not an implemented timed button or a demonstrated 1TR route.

## Reviewed source snapshots

| Repository | Exact revision | Files reviewed |
| --- | --- | --- |
| [macvdmtool](https://github.com/AsahiLinux/macvdmtool/tree/b22ae51eb43a0e1daa21d41616ac899f28e7bf8a) | `b22ae51eb43a0e1daa21d41616ac899f28e7bf8a` | Full `main.cpp`, HPM interface, README, issues and pull requests |
| [tuxvdmtool](https://github.com/AsahiLinux/tuxvdmtool/tree/4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee) | `4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee` | Full `src/main.rs`, `src/cd321x.rs`, bus implementation, README, issues and pull requests |
| [vdmtool](https://github.com/AsahiLinux/vdmtool/tree/2723606490ca9be2c2ff2169796572cc58713886) | `2723606490ca9be2c2ff2169796572cc58713886` | Arduino command path, FUSB302 transmission path, README, history and issues |

Local clones have the `research-apple-` prefix under the operator's FunProjects directory. They were inspected without building or executing controller tools.

## Wire protocol and exact operations

[Asahi's protocol notes](https://asahilinux.org/docs/hw/soc/usb-pd/) place this traffic on USB-C CC, using Apple vendor ID `0x05ac`, structured VDMs, and special debug SOP tokens. They recommend the controller side act as DFP. The target port's action list varies. Read-only discovery is `0x05ac8010`; action information is `0x05ac8011, ACTION`. Replies use command ID OR `0x40`. These are PD messages, not ordinary USB HID reports.

The [macOS implementation](https://github.com/AsahiLinux/macvdmtool/blob/b22ae51eb43a0e1daa21d41616ac899f28e7bf8a/main.cpp#L189) and [Linux implementation](https://github.com/AsahiLinux/tuxvdmtool/blob/4f3fed6a8c7b6fc6f656efd8df7620b1dbcb30ee/src/cd321x.rs#L159) agree on these VDO payloads:

| Effect | VDO words, hexadecimal | Actual contract |
| --- | --- | --- |
| Normal reset | `05ac8012 00000105 80000000` | One reset request; no down/up pair |
| DFU entry | `05ac8012 00000106 80010000` | One DFU request; no duration |
| Serial routing | `05ac8012 01840306` | Target mux change; local `DVEn` command also required for serial |
| Debug USB routing | `05ac8012 01824606` | Target debug USB mux change |

The native implementations unlock the host HPM controller, enter `DBMa`, and submit a `VDMs` command. The data prefix is `0x30 | VDO_count` (three words gives `0x33`), followed by little-endian words. This is the **host controller API envelope**, not a complete raw PD frame. Linux calls the special SOP selector `SopStar = 3`; the Arduino code makes the actual direction explicit: DFP sends `TCPC_TX_SOP_DEBUG_PRIME_PRIME`, UFP sends `TCPC_TX_SOP_DEBUG_PRIME`. Its outer header is generated as `PD_HEADER(PD_DATA_VENDOR_DEF, role, role, 0, count, PD_REV20, 0)`; FUSB302 handles ordered sets, framing and CRC. See [Arduino send path](https://github.com/AsahiLinux/vdmtool/blob/2723606490ca9be2c2ff2169796572cc58713886/vdmtool/vdmtool.ino#L377).

The action descriptor's high bits select persistence, conflict exit and pin mapping. They must not be reinterpreted as milliseconds. Waiting ten seconds between DFU and reset would only sequence two different requests; no examined source says it asserts a power button during that interval.

## What “hold” actually establishes

Asahi records that a normally hard-shut-down M1 mini can stop PD communication, while shutdown after the special DFU/hold mode can retain it; reset can then return to normal boot without losing debug modes. The same note explicitly leaves the persistence mechanism unresolved. This is evidence for retained debug connectivity under particular prior state, not unrestricted cold-start support. [Protocol observation](https://asahilinux.org/docs/hw/soc/usb-pd/#106-dfu-hold-mode).

The [commented `0x8002` vector](https://github.com/AsahiLinux/vdmtool/blob/2723606490ca9be2c2ff2169796572cc58713886/vdmtool/vdmtool.ino#L380) originates in the initial 2020-12-30 commit (`70fe49e9`). It is disabled code rather than a callable operation, and has no associated timing, response trace, model matrix or release operation. The same comment appears in [stacksmashing's iPhone 15 VDM code](https://github.com/stacksmashing/cs-sw-iphone15/blob/master/vdmtool.c), which is corroboration of propagation, not an independent Mac experiment. It remains a narrowly defined reverse-engineering lead: identify its accepted argument encoding and observed reset/DFU state before making any inference about button semantics.

The original t8012 ACE article and its linked archive could not be retrieved in this pass. Its absent contents were not used to infer undocumented commands. A source search finding no button command is not proof that all vendor firmware lacks one.

## Public experiments and generation boundaries

| Evidence | What it establishes | What it does not establish |
| --- | --- | --- |
| [macvdmtool PR 19](https://github.com/AsahiLinux/macvdmtool/pull/19), 2026-09-03 | Author tested an M5 MacBook Air host with an M1 MacBook Air target; target-directed reset/DFU/debug USB worked, while host `DVEn` serial routing failed. The report identifies delayed replies and stale-session acknowledgements. | The title mentions M4/M5, but the explicit test pair is M5-to-M1. It does not certify an M4 target or 1TR. PR was open and unreviewed at inspection. |
| [macvdmtool PR 14](https://github.com/AsahiLinux/macvdmtool/pull/14), 2026-06-30 | Author reports three Macs entering DFU from one mini using three rear ports, after adding explicit HPM RID selection. | Target models are unspecified. Port count must not substitute for an explicit model identifier. No 1TR result. |
| [tuxvdmtool PR 4](https://github.com/AsahiLinux/tuxvdmtool/pull/4) | Userspace I2C rewrite tested on A2338 M1; author explicitly could not verify serial. | “No errors” is not target-state acceptance. |
| [tuxvdmtool PR 9](https://github.com/AsahiLinux/tuxvdmtool/pull/9) | An SPMI host-access implementation is proposed using a linked kernel debugfs interface. | Source proposal alone is not proof of a supported deployed M4 host backend. |
| [Fractal project](https://github.com/jprx/fractal) | Its developer documents M4 mini support and a `macvdmtool reboot serial` development workflow; M4 support is constrained to macOS 15.1 in that project. | Does not supply an isolated M4 VDM-to-1TR trace. Fractal's OS-version constraint is not a demonstrated general VDM constraint. |

M1 has the strongest target-level protocol trace in the reviewed sources. M2 mini ports appear in tuxvdmtool documentation, but that is weaker than a target trace for each operation. For M4, separate host-controller access, correct target DFU port, target action discovery and actual operation outcome; none can stand in for the others.

The retrieved GitHub issues/PRs contain operational DFU evidence but no paired-1TR success report. Broader forum reports are covered by `mac-mini-power-forum-research.md`; the source-led result here does not promote those reports into a hardware guarantee.

## Boot authority is a separate measurement

[Apple's LocalPolicy documentation](https://support.apple.com/guide/security/contents-a-localpolicy-file-mac-apple-silicon-secc745a0845/web) defines paired 1TR through a physical single press-and-hold. It distinguishes ordinary recoveryOS and identifies `coih`, `smb1`, and several SIP settings as mutable only in 1TR. [Paired recoveryOS restrictions](https://support.apple.com/en-mide/guide/security/sec4cf9d63a6/web) also bind downgrade authority to the matching macOS installation.

Consequently, a future experiment needs a read-only boot-environment measurement, not just Options on HDMI. This is an acceptance criterion, not a claim that PD can never affect an underlying button signal. The existing evidence proves neither PD-to-button equivalence nor that the tested Mac's secure boot logic would grant 1TR to an undocumented action.

## Consequences for a general JetKVM backend

These are proposed design constraints, not released APIs:

1. Model PD reset, PD DFU, debug routing, physical button hold, USB wake and keyboard power as separate effects. Do not attach `duration_ms` to the known PD reset/DFU payloads.
2. Discovery must bind a particular bridge/controller/connector to the target identity. Action presence means protocol support, not current readiness or proven cold start. Reject ambiguous port selection; macvdmtool's current default port cannot be a multi-device authority.
3. Bind operations to connection epoch as well as target and operation ID. PD reset may invalidate the underlying link. An acknowledged command and observed boot state are distinct receipt fields.
4. Preserve ambiguous delivery without automatic replay. Current macvdmtool polls register `0x4d` only 16 times with no delay; tuxvdmtool checks host command completion but does not validate a fresh target VDM response. Neither is sufficient as an unmodified production receipt authority. PR 19's delayed/stale response evidence makes this concrete.
5. Mode cleanup must report what happened. Reset cannot be undone by HID neutralization; exiting local `DBMa` does not establish that the target left DFU or released a physical button. Persisted target mux state needs an explicit lifecycle contract.

Even `nop` is not a read-only probe in these tools: initialization enters `DBMa`, and macvdmtool's unlock failure path can issue `Gaid` to reset the controller before command dispatch. A new discovery adapter must separate non-mutating inventory from explicitly authorized controller-mode entry. Linux's I2C fallback can force access to a bound device; borrowing that implementation requires coordination with the owning kernel driver.

## A bounded experiment that could resolve the remaining questions

No stage below was executed. Run only on a separately authorized fixture with independent power and recovery access.

1. Capture host model, OS, PD controller/access backend and connector; target model and firmware; cable capability and orientation. Do not copy private identities into public reports.
2. Begin with passive CC attach state, role and connection epoch. After separately authorizing any required bridge-mode initialization, send only target-read requests: `Get Action List`, then `Get Action Info` for advertised `0x105`/`0x106`. Repeat passive readiness after normal shutdown. This tests whether there is a reachable off-state control path without assuming one; it must not be implemented by calling an upstream `nop` and labeling the whole operation read-only.
3. On a disposable fixture, capture a single known reset or DFU command end-to-end: host completion, fresh target VDM reply, disconnect/reconnect, and independent target boot/DFU state. Do not restore or erase storage. Stop on ambiguous delivery instead of retrying automatically.
4. Separately test debug-mode persistence across an authorized shutdown. Record whether CC/Rd and PD responses remain, and whether a subsequent explicitly authorized reset starts the target. This tests the documented M1 exception on the actual generation.
5. For the `0x8002` lead, first obtain protocol documentation or a controlled firmware analysis explaining its semantics. Treat unknown action/argument writes as a separate experiment, not a normal power capability or an automatic fallback.
6. If a candidate claims Recovery, compare physical-button baseline and candidate with a read-only `bputil` environment readback and paired target-volume identity. Do not downgrade policy merely to test the result. A successful DFU transition or Options screen alone fails this acceptance criterion.

The next useful research artifact is a per-generation action-info/CC-state trace and a proven host-controller response parser. More names for HID keys or a software delay cannot supply the missing evidence.
