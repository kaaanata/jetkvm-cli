# JetKVM Workflows

Use these patterns with the live MCP tool schemas. Tool discovery remains authoritative for available arguments and capabilities.

## Read-only inspection

1. Call `jetkvm_list_devices` when the target is not already a stable device ID.
2. Call `jetkvm_get_status` for current source-attributed basic status.
3. Call `jetkvm_get_capabilities` when the request depends on control, HID, ATX, or observation readiness.
4. Report the evidence source and distinguish configured, firmware-supported, and currently ready capability.

Read-only HTTP inspection must not open a WebRTC session.

## One input action

1. Establish the exact stable device ID and capability.
2. Open a control handle with only the capabilities needed for the request.
3. Preserve the returned handle and generation.
4. Generate a fresh operation ID and call the narrowest input tool.
5. Inspect the operation receipt. Stop on ambiguity or a non-retry-safe result.
6. Close the owned control handle when finished.

Opening control may trigger confirmation because it can displace another session. Do not conceal that effect by opening a handle during a read-only task.

## Bounded multi-step input

Use `jetkvm_run_actions` only when the steps are deterministic from current trusted context. A batch contains at most 16 actions and remains bounded by the server duration limit.

- Group adjacent mechanical input when no observation is needed between steps.
- End the batch before the next decision point.
- Do not put power or administration in an input batch.
- Do not describe the batch as transactional; preserve partial receipts.
- Acquire a new observation before making another visual decision.

If observation tooling is unavailable, limit work to actions whose target does not depend on unseen screen state, or ask the user for another trustworthy observation path.

## Pointer input

Pointer calls require an observation ID, dimensions, capture time, device ID, and generation from one fresh server observation. Use those exact values. If the session generation, resolution, or acceptable freshness changes, discard the binding and observe again.

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
