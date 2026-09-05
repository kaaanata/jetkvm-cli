# First-frame startup diagnosis

## Confirmed dependency and candidate

Pion normally invokes OnTrack only after first RTP. The previous automation
adapter waited for trackReady before sending PLI, so a sender waiting for PLI
before its first RTP could never be prompted. The candidate selects the single
nonzero remote video SSRC from the peer's negotiated receivers and sends PLI
with the owning session generation fence, without waiting for OnTrack. Missing
or ambiguous video identity fails explicitly. Codec validation on arriving RTP,
source timestamps, 5-second freshness and 15-second observation deadline remain
unchanged. No HID is involved in this diagnostic or the keyframe request.

TestNegotiatedVideoPLIUnblocksFirstRTP uses two loopback peers. Its sender emits
no RTP until it receives a PLI for the negotiated SSRC. The test verifies the
absence of OnTrack beforehand and delivery afterwards, plus generation fencing.
This is deterministic evidence for the dependency fix, not attribution of a
historical device incident.

## Evidence and limits

The original released v1.0.3 binary's first screenshot reportedly timed out at
15.64 seconds, while later input-plus-image operations passed. That historical
failure has not been causally reproduced or attributed. No failing-session RTP
or decoder timeline was available. The same original artifact subsequently
passed six screenshot-only invocations: one in 3.307 seconds and five totaling
13.958 seconds, producing 1920x1080 PNGs. This rules out a deterministic
release-binary/toolchain failure, not an intermittent failure.

An instrumented read-only session with the previous PLI behavior measured:
session ready at 251 ms; OnTrack/first RTP/first PLI at 5.300 seconds; complete
IDR about 8 ms later; decode 2.075 seconds; observation at 7.384 seconds. There
were zero sequence gaps or assembly errors.

With negotiated-SSRC PLI, another instrumented run sent PLI at 430 ms but first
RTP still arrived at 5.410 seconds. Thus the candidate does not establish an
improvement in the device's first-media latency. During concurrent build/race
load that run's initial decode took 7.583 seconds and its source frame correctly
expired; observation returned stale at 15.431 seconds, with zero sequence gaps,
zero assembly errors and 832 received packets. This independently demonstrates
a source-age failure under load, not the cause of the original unavailable-frame
incident. The implementation does not hide it by changing timeouts or freshness.

After the concurrent build/race jobs completed, the same candidate timeline
passed: session ready/first PLI at 329 ms, first RTP at 5.403 seconds, decode
2.073 seconds, observation at 7.476 seconds with 2.073-second source age. There
were zero sequence gaps and assembly errors. Device startup latency persisted;
the differing decode durations isolate an additional host-load sensitivity.

A candidate CLI built with Go 1.27.1, CGO_ENABLED=0, trimpath and stripped symbols
passed screenshot-only HIL in 4.631 seconds with a 1920x1080 PNG. Rebuilding the
final candidate and checking it again without concurrent test/build load passed
in 3.700 seconds, producing a 73355-byte 1920x1080 PNG. Its executable SHA-256 was
`3928d946f3075549c18cef0c702ee52d977079bc8db5367bcbd7521341a00997`.
The jetkvm and automation package race suites and focused startup/fencing tests
passed. This candidate
is not a replacement for the signed published artifact until normal integration
and release acceptance. All raw captures, local configuration and logs remain
outside this document; no device identity, address or credentials are recorded.

## Reproduction

TestHILVideoStartupTimeline is opt-in via JETKVM_HIL_CONFIG. It logs timing and
counter data only, opens a video session, and sends PLI without HID. Set
JETKVM_HIL_NEGOTIATED_PLI=1 for the candidate behavior; omission reproduces the
previous OnTrack-gated timing. The stream worker is canceled and joined before
test return. Do not run concurrent hardware sessions against the same fixture.

For actual CLI acceptance, use the main task's TestHILVisualCLI with an absolute
JETKVM_HIL_BINARY path and JETKVM_HIL_INPUT=0, JETKVM_HIL_MCP=0. Its fixture uses
temporary policy/state and must not modify the operator's persistent config.
