# Power paths and bounded holds

## Verified source boundary

The upstream repositories were cloned locally for full-source review:

- JetKVM application: `jetkvm/kvm`, commit `6d4843eb44555b4915d88ee50fcf356855ce811d`.
- ATX extension MCU: `jetkvm/atx-extension-firmware`, commit `9118d405096df578f3401925451f89ac7ab1a860`.

`jsonrpc.go` maps `power-short` to 200 ms and `power-long` to 5 s. `serial.go` implements `pressATXPowerButton(duration)` using separate `BTN_PWR_ON` and `BTN_PWR_OFF` UART commands. Thus the lower layer can hold for a custom duration; the current RPC does not expose it.

The ATX MCU maps those commands directly to GPIO 19. Its watchdog monitors the MCU loop, not the controlling application's connection. A safe timed-hold extension should own a bounded pulse deadline on the MCU and acknowledge release. Adding a duration parameter solely on the client would leave the output asserted if the controller dies before OFF. That firmware extension is not deployed or hardware-accepted here.

The live fixture's extension readback is `serial-console`; ATX is not ready and power policy is disabled. Its HID descriptor contains Keyboard usage page 0x07 through usage 0xff, while hid.usb3 is vendor page 0xff00. Keyboard Power (0x66), Generic Desktop System Power (0x01/0x81), vendor wake and a physical switch are separate capabilities. No System Control collection was found in the inspected descriptor. An operating system accepting a keyboard power usage would still not prove that an Apple Silicon boot controller interprets it as the physical button held from power-off.

## Implemented client candidate

`input hold <device> SHIFT --duration 1s` and MCP `run_actions` with `type: key_hold`, `keys` and `duration_ms` provide bounded ordinary keyboard holds. Holds are limited to 12 seconds, require approval, retain the control generation and operation receipt, and always attempt input neutralization. HID-RPC keepalive 0x09 preserves a hold without release/repress. Cancellation and late keepalive fail closed. These commands do not currently expose Keyboard Power or System Control Power.

`power capabilities` and the power section of video control readback report the available protocol paths and their limits without performing a power action. The CLI remains command-scoped; the persistent MCP handle is the cross-call session authority.

## Recovery acceptance boundary

Apple documents holding the Mac's power button from the off state until startup options appear. Current fixture wiring does not provide a proven path to that button. Adding firmware or client commands cannot create missing electrical/mechanical wiring. Do not label generic USB input, Wake-on-LAN, CMD-R or a repeated short pulse as verified Recovery power-hold support.

Sources: https://github.com/jetkvm/kvm/blob/6d4843eb44555b4915d88ee50fcf356855ce811d/serial.go and https://github.com/jetkvm/atx-extension-firmware/blob/9118d405096df578f3401925451f89ac7ab1a860/jetkvm-atx.c ; Apple recovery procedure: https://support.apple.com/102518 .
