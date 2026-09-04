# JetKVM Workflows

Use these patterns with the live MCP tool schemas. Tool discovery remains authoritative for available arguments and capabilities.

## Read-only inspection

1. Call `jetkvm_list_devices` when the target is not already a stable device ID.
2. Call `jetkvm_get_status` for current source-attributed basic status.
3. Call `jetkvm_get_capabilities` when the request depends on control, HID, ATX, or observation readiness.
4. Report the evidence source and distinguish configured, firmware-supported, and currently ready capability.

Read-only HTTP inspection must not open a WebRTC session.

## Screenshot and visual input over MCP

1. Open control with `requested_capabilities: ["video"]` for screenshots alone, or `["input", "video"]` for a visual input workflow. Honor any takeover confirmation.
2. Call `jetkvm_observe` or `jetkvm_capture_screen` with the exact `device_id`, `control_handle`, and `expected_generation` returned by the server.
3. Inspect the PNG ImageContent. Its structured observation supplies `observation_id`, `device_id`, `captured_at`, frame dimensions, `frame.generation`, and separate receive/decode timing.
4. For coordinate input, call `jetkvm_pointer_move`, `jetkvm_pointer_click`, `jetkvm_pointer_double_click`, or `jetkvm_pointer_drag` with that observation ID and the same device, handle, and generation. Use the live schema for coordinates, path, button, and operation ID. `jetkvm_pointer_scroll` uses bounded deltas and does not need a coordinate binding.
5. Set `observe_after: true` on a pointer tool or `jetkvm_run_actions` when the next decision needs a result image. Inspect the receipt even if the response is marked as an error; partial receipts and any available ImageContent remain meaningful.
6. Close the owned handle after the workflow.

The default coordinate binding lifetime is 30 seconds, measured from source frame receive time, not when the model receives the result or when decoding finishes. Capture freshness is separate: a capture requests a fresh post-call IDR frame. `decoded_at` records decode timing and does not renew the binding. Never restamp an observation or provide invented dimensions to extend its validity. If it expires, observe again on the still-valid handle; if the handle or generation changes, discard the old binding and acquire a new one.

## Command-scoped CLI

Replace `lab` and the example coordinates with the intended configured device and current target. These are independent examples, not a sequence to execute blindly.

```sh
jetkvm screenshot lab --file screen.png --output=json
jetkvm input move lab --x 320 --y 240 --file after-move.png --output=json
jetkvm input click lab --x 320 --y 240 --file after-click.png --output=json
jetkvm input double-click lab --x 320 --y 240 --file after-double-click.png --output=json
jetkvm input drag lab --path-json '[{"x":320,"y":240},{"x":480,"y":360}]' --file after-drag.png --output=json
jetkvm input scroll lab --delta-y -3 --file after-scroll.png --output=json
jetkvm input run lab --actions-json '[{"type":"keypress","keys":["ESC"]},{"type":"wait","duration_ms":250}]' \
  --observe-after --file after-batch.png --output=json
```

`observe` aliases `screenshot`; both require `--file` and open only video capability. Each coordinate command opens its own input/video control, captures the binding there, executes, and closes. A prior PNG is a visual reference, never authorization or a binding transferable to a new process. Automatic capture does not identify UI elements or prove that a target stayed in place. Use the persistent MCP loop when a decision depends on the exact bound image.

For input, `--file` implies post-action capture. `--observe-after` requires `--file` or explicit `--image-base64`; only the latter embeds PNG base64 in JSON. Choose file paths deliberately because existing contents are replaced. Capture or file-write failure after input is not permission to repeat the action. Preserve the operation receipt and report which input and capture steps succeeded.

## One keyboard action

1. Establish the exact stable device ID and capability.
2. Open a control handle with only the capabilities needed for the request.
3. Preserve the returned handle and generation.
4. Generate a fresh operation ID and call the narrowest input tool.
5. Inspect the operation receipt. Stop on ambiguity or a non-retry-safe result.
6. Close the owned control handle when finished.

Opening control may trigger confirmation because it can displace another session. HTTP status needs no control handle; a requested screenshot does require a video session and must disclose that takeover effect.

## Bounded multi-step input

Use `jetkvm_run_actions` only when the steps are deterministic from current trusted context. A batch contains at most 16 actions and remains bounded by the server duration limit.

- Group adjacent mechanical input when no observation is needed between steps.
- End the batch before the next decision point.
- Do not put power or administration in an input batch.
- Do not describe the batch as transactional; preserve partial receipts.
- Acquire a new observation before making another visual decision.
- Use `observe_after: true` for a post-action image, or include a `screenshot` action at a deterministic point. The core returns the batch's last captured screenshot; adapters do not replay actions to recover an image.

If observation tooling is unavailable, limit work to already-authorized actions whose target does not depend on unseen screen state. Report the missing capability rather than manufacturing a pointer binding.

## Pointer input

The observation ID is the coordinate binding authority. Device ID, control handle, and expected generation fence the call; the server resolves dimensions and capture time from its issued observation. Legacy caller metadata does not create or renew a binding. Use a new observation when the binding expires or the screen changes enough to invalidate the intended target.

## Power operation

1. Confirm that ATX capability is configured, firmware-supported, and ready.
2. Open a control handle with the required power capability.
3. Read power state when useful, while treating LED state as device evidence rather than guaranteed host state.
4. Submit one exact press, reset, or hold operation with a fresh operation ID and repeated expected device ID when required by the schema.
5. Complete action-time confirmation.
6. Inspect the receipt and do not retry ambiguous delivery.
7. Close the handle.

## Recovery

For a stale generation, expired handle, or closed session, do not reuse old references. Re-read status/capabilities and open a new control handle only when the user's original authorization still covers the action. For uncertain input neutralization, stop further writes until the server reports recovery.
