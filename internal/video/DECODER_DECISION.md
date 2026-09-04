# Embedded H.264 decoder decision

Status: no production backend selected
Reviewed: 2026-09-05

## Decision

The production factory remains unavailable. The CLI and MCP server must not
advertise `observe`, `capture_screen`, or image-returning capabilities until a
decoder passes every gate below on real JetKVM output.

The package nevertheless provides the complete transport-independent boundary:

- bounded RFC 6184 single-NAL, STAP-A, and FU-A depacketization;
- out-of-order buffering, sequence-gap detection, SPS/PPS retention, and IDR
  decodability metadata;
- generation fencing, PLI requests, freshness waits, decoded-size checks, and
  immutable observation metadata;
- a decoder SPI whose request includes mandatory allocation limits.
- mandatory decoder reset on control-generation changes, preventing reference
  frames and parameter-set state from crossing a replaced WebRTC session.

This is deliberate capability gating, not an external-FFmpeg fallback.

## Candidate assessment

### Eyevinn hi264 v0.10.0

- License: MIT; dependency `github.com/Eyevinn/mp4ff v0.50.0` is MIT.
- Distribution: pure Go and compatible with a single-file Go release.
- Functional boundary: its public documentation says the decoder handles IDR
  plus P-Skip frames, not general P/B frames. An IDR-only capture path may fit a
  PLI-driven screenshot, but this has not been proved against the JetKVM
  encoder's exact profile, SPS/PPS, cropping, color metadata, and access units.
- Safety boundary: the public synchronous decode API has no `context.Context`.
  Running it in a detached goroutine would return cancellation to the caller
  while hostile decoding continued to consume CPU and memory, which violates
  this package's decoder contract.
- Maturity: first released in 2026 with a narrow thumbnail/test-stream scope.

Conclusion: promising spike candidate, not yet a production backend. Adoption
requires upstream or maintained support for cooperative work limits, corpus and
fuzz review, real-device golden frames, and bounded allocation before decode.

Required modules for an isolated spike only (not currently added to `go.mod`):

```text
github.com/Eyevinn/hi264 v0.10.0
github.com/Eyevinn/mp4ff v0.50.0
```

### thesyncim/goh264 and thesyncim/goav

- `goh264` is LGPL-2.1. Its own license describes the object/relinking duties
  associated with static linking. That conflicts with the current release goal
  of one opaque self-contained executable unless the project designs and ships
  a complete LGPL compliance mechanism.
- `goav` is Apache-2.0, but its H.264 backend is the separately licensed
  `goh264`; `goav` documentation marks that adapter experimental/build-tagged.

Conclusion: rejected for the first public release. An Apache-2.0 wrapper does
not change the backend's LGPL-2.1 obligations.

### Cisco OpenH264

- Source license: BSD-2-Clause.
- Runtime: mature and broad, but native/CGo builds complicate the promised
  reproducible cross-platform single executable.
- Patent distribution: Cisco's binary license requires the Cisco-provided
  binary to be downloaded separately to the end user's device and not combined
  with third-party software before download. Embedding that binary in `jetkvm`
  does not satisfy the condition. Building from source does not inherit Cisco's
  binary patent coverage.

Conclusion: rejected for an embedded release backend. It may only be revisited
after specialist legal review and a product decision that changes the
single-file requirement.

### FFmpeg/libavcodec

- Mature and functionally broad.
- External process use violates the production single-file requirement.
- Static LGPL integration requires relinking/source compliance and native build
  machinery; optional GPL components would be incompatible with the repository
  distribution policy.

Conclusion: allowed only in an explicitly non-production diagnostic build, as
already stated in the project design. It is not a release fallback.

### Platform decoders

VideoToolbox, Media Foundation, and platform Linux APIs avoid shipping a codec
binary but create three different behavior and lifecycle authorities. Linux
availability also depends on the host. They do not provide one reproducible,
portable release contract.

Conclusion: not selected for the first backend. Platform acceleration may be a
later optional implementation behind the same SPI after the portable authority
exists.

## Production acceptance gates

1. License and patent-distribution review recorded with exact module versions.
2. Pure in-process cancellation or deterministic work limits; no abandoned
   decode goroutines or subprocesses.
3. Allocation is bounded before output planes are created.
4. Successful decode of captured JetKVM 0.5.8 IDRs at every HIL resolution and
   signal transition, with SPS/PPS changes and stream generation replacement.
5. Corpus, malformed-input, fuzz, race, and sustained-memory tests.
6. macOS/Linux/Windows amd64 and arm64 single-file GoReleaser builds.
7. Decoder errors remain distinct from no-signal, stale-frame, and packet-loss
   errors.
8. The capability inventory is enabled only when the selected factory reports
   available and passes startup self-test.

## RTP library note

`github.com/pion/rtp v1.10.5` is MIT and remains the preferred RTP packet parser
at the WebRTC boundary. Its H264 depacketizer handles NAL payload conversion but
does not own this product's generation fencing, bounded access-unit reorder
window, loss terminality, SPS/PPS cache, or observation freshness. Therefore
this package accepts a small parsed `RTPPacket` value and owns those semantics;
it does not duplicate the RTP wire-header parser.
