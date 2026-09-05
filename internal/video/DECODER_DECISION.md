# Continuous embedded H.264 decoder

Status: FFmpeg 9.0.1 H.264-only WASI reactor implemented. See the verification
record below for the distinction between synthetic replay, hardware, and release.

## Selection evidence

The previous hi264 0.10.0 backend decoded independent IDRs only. Its upstream
scope excludes general motion-compensated P/B pictures; retaining its instance
would not solve that restriction.

Two initial WASI prototypes were built using wasi-sdk 34.0:

- OpenH264 2.6.0, commit `652bdb7719f30b52b08e506645a7322ff1b2cc6f`.
- edge264, commit `2c2ab95d63c1ad89c5687f9e50b57cad772c871b`.

Both produced byte-identical planar YUV for a 90-frame 1920x1080 moving P-frame
sequence compared with FFmpeg. Including CLI startup and writing YUV, illustrative
runs took 1.56 seconds for OpenH264 and 1.40 seconds for edge264; these are not
production-pipeline or cross-machine benchmarks. More complex B-picture fixtures
exposed differences: OpenH264 produced pixel differences in both its native and
WASI builds. The edge264 prototype also failed the B-sequence comparison, with
incomplete output; that result does not distinguish adapter issues from codec
issues. Neither prototype is shipped or silently selected as a fallback.

The selected FFmpeg reactor passes exact planar-YUV comparisons for both the
12-frame P and B fixtures. Source and reproducible build instructions are in
[wasmdecoder](wasmdecoder/README.md). It retains the existing single-file, CGo-free
Go runtime and bounded WASI execution model. It does not execute an external
FFmpeg process. Licensing and source publication are part of release preparation.

## Stream and reference ownership

A session owns one decoder reactor, one RTP receiver, and one decoder worker.
Ingestion never waits for decoding. Complete AUs enter an ordered queue limited
to 32 units and the configured maximum AU byte budget in total (8 MiB by default).
An independent IDR can replace queued predecessors. Arbitrary dependent compressed
frames cannot be dropped to select the latest display image.

Packet gaps, invalid assembly, or queue overload invalidate the entire reference
chain, queued work, and in-flight publication. The worker resets reference state
before decoding an independent recovery IDR. Decoded display images may replace
older display images. The worker and observation waiters share one rate-limited
PLI timestamp; recovery does not depend on an observer currently waiting.

The depacketizer fences completed RTP sequence/timestamp identities, handles
16-bit sequence and 32-bit timestamp wrap, ignores duplicates and late packets,
and marks gaps between complete AUs. Intra-AU reordering remains bounded. A newer
AU arriving while an older AU is incomplete conservatively triggers recovery;
this is not an unlimited jitter buffer across interleaved frames.

Every decode receives an opaque source token. Delayed or reordered output is
resolved against its original AU, including generation, RTP time and earliest
local receive timestamp. Output without a known token is rejected. Presentation
must not move backwards in RTP time. Receive time is local arrival time, not a
remote sensor clock or proof that the target application has finished responding.

## Observations and lifecycle

Ordinary observations reuse a decoded frame within the requested freshness bound.
The default remains 5 seconds for compatibility; stricter requests are honored.
Every observation also respects the session's most recent completed input time.
Observe-after, batch screenshots and wake recovery retain explicit server-owned
lower time bounds, so a cached pre-input frame cannot become post-input evidence.
Coordinate bindings remain independently limited to 30 seconds from source time.

A decoded frame younger than 250 ms is sufficient readiness evidence to omit the
two diagnostic RPCs. Otherwise firmware readiness is checked as before. This
hint changes neither observation freshness nor input authorization. PNG uses
BestSpeed compression. Planar pixels are copied into immutable host-owned memory;
color conversion and PNG encoding occur only for requested observations.

Canceling an observation cancels its wait, not the stream. Reset, lease/session
closure and shutdown cancel and join decoder work. WASI execution retains a
10-second per-call limit and 512 MiB linear-memory ceiling. The latter is not a
process RSS limit; Go copies, compiled code and other devices add memory. A failed
reactor is discarded, and no ambiguous HID action is replayed by video recovery.

CLI continues to open, execute and close one command-scoped control. MCP reuses
its existing explicit handle. No daemon, second session authority, or persistent
CLI handle is introduced.

## Verification record

On the local Apple Silicon development host, three 90-frame 1920x1080 synthetic
P-picture replay runs through the production Go/WASI adapter took 1.429–1.448 s,
or 62.2–63.0 fps including compilation and pixel copies. First output took
253–261 ms. Reported cumulative Go allocations were about 385 MiB per replay,
with 35–62 MiB Go heap at the end; these figures are not process RSS measurements.
This is a demanding moving test pattern, not a guarantee of sustained throughput
on every supported host or across multiple devices.

The first live MCP binary run returned 1920x1080 PNG images with continuous
observations in 93–94 ms and source ages of 126–229 ms after local PNG validation.
Open took 265 ms; first observation took 5.46 s, which includes cold-device/video
readiness work and must not be represented as a warm capture. A bounded Escape
and wait batch with post-action image took 339 ms and retained completed,
transport-accepted, neutralized, non-retryable receipts. Deduplication and closure
passed. Screenshots and target identity remain private.

Regression tests cover exact P/B output, immutable planes, reference queue order,
overload recovery, whole-frame gaps, duplicate/wrapped sequence identities,
source-time observation barriers, cancellation, generation changes and bounded
WASM memory. Repeated race tests cover concurrent observation/reset/close.
Hardware signal/resolution switching and simultaneous physical multi-device
coverage remain separate from synthetic and single-fixture verification.

A longer 1,800-frame replay completed in 25.85 s (69.6 fps). The standalone test
process used 24.32 s user CPU and 0.26 s system CPU over 26.51 s wall time, about
93% of one core, with peak RSS 139,378,688 bytes (about 133 MiB). This included
other verification load on the host; it is not an isolated laboratory benchmark.
The complete replay allocated about 5.4 GiB cumulatively but ended with a 32 MiB
Go heap; cumulative allocation is not retained memory.

Two further MCP runs passed warm observation at 92–94 ms and the bounded input
plus post-action observation at 287–308 ms. A first-observation diagnostic
reported 5.434 s total, no wake receipt, 374 ms source-to-decode latency and
497 ms frame age after PNG validation. Most of that first-call delay precedes
the returned AU's arrival; it must not be attributed to decoding alone.
Read-only CLI binary HIL returned a 1920x1080 PNG and closed in 5.89 s.

Two consecutive production-script builds produced identical SHA-256
`22395f517ccd2af6d76167807f12c265ba91009917440bacf5b6e4fc9f59100c`.
The Grype 0.118.0 C-package SBOM scan reported zero matches against its
2026-09-05 database. Root govulncheck reported no reachable vulnerabilities.

The actual hardware stream was then received continuously for 20 seconds after
first observation. It fed the decoder at 60.8 fps, with 8.55 ms P95 decode time,
95.35 ms P95 source age, and 361.8 ms maximum source age including startup.
There were zero observed RTP sequence gaps and zero assembly errors among 4,207
packets. The standalone test process used 9.41 s user CPU and 0.30 s system CPU
over 26.88 s wall time (about 36% of one core), with peak RSS 150,224,896 bytes
(about 143 MiB). It sent only RTCP PLI, with no HID or host application changes.

The 1,219 recorded AUs contained 41 I pictures and 1,178 P pictures. Replaying
that private stream through the production WASI decoder and comparing every
planar-frame MD5 with native FFmpeg passed all 1,219 frames, including final
drain. Replay took 12.98 s (93.9 fps including hash checks). The raw stream,
reference hashes and image content remain private; only aggregate timings and
counts are public. `JETKVM_HIL_CONTINUOUS=1` extends the existing startup test;
`JETKVM_HIL_RECORD` writes a new private record file without overwriting one.
`JETKVM_VIDEO_REFERENCE_MD5` adds independent per-frame replay verification.
