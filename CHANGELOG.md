# Changelog

## [1.0.8](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.7...v1.0.8) (2026-09-05)


### Bug Fixes

* wake sleeping video during observation ([478b4b5](https://github.com/kaaanata/jetkvm-cli/commit/478b4b58205ca402ac3e4175c2d5622451ebdf7e))

## [1.0.7](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.6...v1.0.7) (2026-09-05)


### Bug Fixes

* Restore confirmed screen capture and control sessions, and fix batches containing timed waits.
* Apply one confirmation policy to CLI and MCP key aliases and action batches; support `CMD` as `COMMAND`/`META`.
* Reject overflowing duration inputs and return clearer key, session, and input-cleanup errors.
* Preserve durable receipts on duplicate operation lookup without reporting a fabricated batch or cleanup result.
* Add executable-level MCP hardware acceptance covering confirmation, 1920x1080 PNG capture, bounded input, deduplication, and session closure.

## [1.0.6](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.5...v1.0.6) (2026-09-05)


### Bug Fixes

* Guide first-time device connection from the CLI or an agent without requiring configuration JSON, hardware IDs, or state-file paths. MCP starts in a restricted bootstrap mode before any device is configured.
* Keep device passwords in an expiring local setup page and the operating-system credential store. Enrollment verifies the device without opening a control session or sending input.
* Add revision-bound settings updates through CLI and MCP for output, input permissions, device exposure, takeover permission, and session lifetimes. Reject stale changes and activate updates on the same MCP connection after active controls close.
* Preserve existing policy ceilings and confirmation requirements, keep machine output free of interactive prompts, and update the bundled agent guidance. Cloud integration is not included.

## [1.0.5](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.4...v1.0.5) (2026-09-05)


### Bug Fixes

* Show inline stages, measured download progress, speed and elapsed time for long operations. Unknown sizes stay indeterminate; JSON and MCP output remain free of progress UI.
* Run explicit update and rollback commands without a second confirmation. Hidden --yes flags remain compatible; ownership, signature, checksum and device-action authorization checks remain enforced.
* Wait for control and runtime cleanup before displaying final results. Preserve partial receipts and distinguish cancellation, activation failure and failed rollback without encouraging unsafe retries.
* Simplify human results, add --verbose diagnostic details and actionable recovery hints, and verify cancellation, prompt handoff and plain-output behavior through real terminal tests. Cloud candidate work is not included.

## [1.0.4](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.3...v1.0.4) (2026-09-05)


### Bug Fixes

* Lead human CLI output with the recorded outcome, group help by task, and wrap narrow-terminal content while preserving JSON and MCP contracts.
* Request the initial video keyframe from the negotiated SDP identity without waiting for first RTP; retain freshness limits, deadlines, and generation fencing. This does not establish the cause of the earlier intermittent screenshot timeout.
* Verify visual CLI hardware tests through actual released executables. Cloud integration remains an isolated candidate and is not included in this release.

## [1.0.3](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.2...v1.0.3) (2026-09-04)


### Features

* unify terminal UI with Charm components ([b31de07](https://github.com/kaaanata/jetkvm-cli/commit/b31de07cabc84d452aa51212f6f1f39d0106361d))


### Bug Fixes

* join terminal input before form shutdown ([f4250c7](https://github.com/kaaanata/jetkvm-cli/commit/f4250c724c1f98f4084468d822e9b2f30c6c9030))
* restore legacy release archive contract ([747897c](https://github.com/kaaanata/jetkvm-cli/commit/747897ca9655689e2648fbeb222db67016c4d8f8))


### Miscellaneous Chores

* prepare patch release ([7d18098](https://github.com/kaaanata/jetkvm-cli/commit/7d18098465adea1ede30606805fe123e52ac65fe))

## [1.0.2](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.1...v1.0.2) (2026-09-04)


### Bug Fixes

* complete visual controls and signed updates ([6e325b5](https://github.com/kaaanata/jetkvm-cli/commit/6e325b5afe8c03e5531174efa0a44b0b5f15b73e))
* fetch codec modules before clean rebuild ([102f083](https://github.com/kaaanata/jetkvm-cli/commit/102f083bb052108b35361e192ac42b219292ef97))
* install cosign from verified Go module ([3388742](https://github.com/kaaanata/jetkvm-cli/commit/3388742e810563f7788e790838a03c2957305d86))
* pin supported cosign installer action ([e7a105e](https://github.com/kaaanata/jetkvm-cli/commit/e7a105e6e9e710f4f102cfe4c91b653cb9349a42))

## [1.0.1](https://github.com/kaaanata/jetkvm-cli/compare/v1.0.0...v1.0.1) (2026-09-04)


### Bug Fixes

* verify complete installs and native agent readback ([c11053d](https://github.com/kaaanata/jetkvm-cli/commit/c11053d22460f7d0a7a52f072c576168f86786c3))

## 1.0.0 (2026-09-04)


### Features

* ship JetKVM CLI and agent integrations ([0ace712](https://github.com/kaaanata/jetkvm-cli/commit/0ace712c5a81c692f739d5d28e86313b9eadf30d))
