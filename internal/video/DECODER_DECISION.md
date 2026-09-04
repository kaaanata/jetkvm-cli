# Embedded H.264 still-frame decoder

Status: WASI IDR backend implemented; final 1080p live HIL passed with the
5-second source-freshness default.

## Implementation and scope

`EmbeddedDecoder()` implements the existing DecoderFactory/Decoder interfaces.
It embeds Eyevinn hi264 v0.10.0 with mp4ff v0.50.0 in a WASI command and uses
wazero v1.12.0 on the host. The codec dependencies are MIT; attribution is in
DECODER_LICENSES.txt and retained inside the binary. wazero is Apache-2.0.
Sources: [hi264](https://github.com/Eyevinn/hi264/tree/v0.10.0) and
[wazero](https://github.com/tetratelabs/wazero/tree/v1.12.0).

Each decode creates a fresh module and decoder, without reference-picture
state. Compiled code is retained until Reset/Close. Stdin carries Annex-B;
stdout carries little-endian uint32 width/height followed by exact NRGBA bytes.
No filesystem, network, credentials, external executable, CGo or FFmpeg is
needed at runtime. FFmpeg only generated the independent synthetic fixtures.

This is an IDR snapshot decoder, not a P/B motion player. Each request must
contain one SPS, one matching PPS and exactly one complete IDR slice, in order.
SEI/AUD/filler NALUs are allowed. Non-IDR VCL, multiple pictures/slices, FMO,
interlacing, transform bypass and unsupported profiles/formats are rejected.
Baseline/Main/Extended/High are accepted only within the progressive 8-bit
4:2:0 subset. Right/bottom cropping smaller than one macroblock is accepted;
left/top cropping is unsupported. hi264's conversion uses VUI matrix/range
metadata, defaulting to BT.601 limited range when unspecified.

## Isolation and completion

- The host independently parses a bounded SPS prefix before decoder allocations.
  Parameter sets are capped at 4096 bytes; Exp-Golomb reads and scaling/POC loops
  are bounded. Dimensions must satisfy request limits and hard 4096x2160 bounds,
  including a bounded coded macroblock grid.
- Input is capped at the smaller of the request limit and 8 MiB. NAL count,
  exact RGBA output length, and 4096-byte stderr are bounded.
- WithMemoryLimitPages(8192) imposes a firm 512 MiB linear-memory cap per module.
  This is not a total RSS cap: compiled code and bounded host buffers are
  additional, and independent devices have independent decoder limits.
- WithCloseOnContextDone(true) terminates running WASM on cancellation. Fixed
  trusted-artifact compilation is owned by the parent context and joined before
  return; it may finish after cancellation, especially under race instrumentation.
  The separate 10-second hostile-input execution budget starts after compilation.
  Parent cancellation still applies throughout gate wait, compilation and decode.
  Infinite-WASM tests verify actual execution cancellation independently.
- Decode has no detached worker. Reset/Close cancel and join the call, then
  release the runtime. Every call uses a new module, including after failures.
- Two exact build-time source overlays make hi264 fail closed: exhausted CABAC
  input traps instead of synthesizing zero bits; premature slice termination
  fails instead of returning a partial picture. The build rejects changed
  patch contexts and never modifies module-cache files. This adds no P/B support.
- Traps, invalid output and unsupported inputs return decoder failures.
  Resource isolation does not prove semantic validity of every H.264 stream.

## Source age and lifecycle

AccessUnit.ReceivedAt is the earliest local RTP receive time belonging to its AU,
including out-of-order arrival. Observation.CapturedAt uses the same source
timestamp. Only Frame.DecodedAt records decoder completion. Receive time is a
local arrival measurement, not the remote sensor's capture clock.

ObserveRequest.NotBefore requires source receipt at or after capture invocation.
Frame Freshness defaults to 5 seconds and is measured from source receipt.
The independently owned observation-to-input binding defaults to 30 seconds
at the input integration boundary, allowing decode, model thinking and a
subsequent click. It must also use source CapturedAt and honor stricter
configured limits. The 30-second binding does not permit capture to return an
old cache entry and does not widen the independent frame freshness requirement.
Explicitly stricter request freshness values are always honored.

The first hardware decode took 2.157 seconds including compilation; warm decode
took about 1.4 seconds. Subsequent live HIL under concurrent race load still
expired a 15-second capture request with the earlier 2-second source-age limit:
device IDR cadence and a newest complete pending IDR can consume approximately
2 seconds before decode even starts. The 5-second default is an explicit source
latency budget based on that evidence, separate from fixing the blocking reader.
Await must still reject frames before NotBefore or beyond the requested source
age. Warm decoding never resets source time. Pointer authorization must recheck
source age when input executes.

Await retries rate-limited PLIs with a timer even without RTP notifications.
pushMu owns decoding; receiveMu owns bounded packet assembly; mu protects
observation metadata, so Await cancellation does not wait for decoding.
Reset/Close cancel before joining
active decoding and fence its result. The stream owner still owns cancellation
and joining of its receive worker.

For a live track, call StartLive(streamContext) once and then PushLive for every
RTP packet, stamping ReceivedAt immediately after ReadRTP. Do not use synchronous
Push on a live reader: it stops ingestion during decoding and can make buffered
frames stale before they are processed. The live path continuously depacketizes
and retains only the newest complete IDR in one pending slot, in addition to
the in-flight IDR. It never drops arbitrary packets to implement latest-frame
selection. Pending frames do not reset source age. PLI retries are suppressed
while decoding or a complete pending IDR already exists. Close cancels and joins
the sole worker; Reset discards the pending AU and cancels/fences the old decode.
Synchronous Push remains available on pipelines that have not entered live mode.

Live decode failures retain their AU source timestamp and wake Await. Failures
at or after the request's NotBefore return typed ErrDecodeFailed immediately;
older failures cannot poison a later request. Packet loss/assembly errors remain
transient receive errors, not decode failures. A successful decode clears the
last decode failure.

MatchesGeometry(generation, width, height) compares a coordinate binding with
the latest decoded frame under the metadata lock. It rejects absent/resetting/
closed state, a different generation, or changed dimensions even within one
generation. The input integration must invoke this check when resolving a real
session's observation binding; compressed SPS receipt alone is not proof of a
new decoded geometry.

## Reproduction and validation boundary

Run `sh scripts/build-decoder.sh`. It pins Go 1.27.0 and module versions, verifies
checksums, applies the two overlays in a build-owned temporary source copy,
disables CGo, and uses wasip1/wasm, trimpath, no VCS metadata and an empty build
ID. decoder.wasm is checked in; normal builds never download a runtime decoder.

Two consecutive patched builds produced identical SHA-256:
`1300c355790cf21a0f3a7696b407fd1972edfd29a7f528a5cd1ed72701ca6338`.
Both host video and nested WASI-target dependency scans with
`golang.org/x/vuln/cmd/govulncheck@v1.7.0` reported no vulnerabilities. The nested
scan runs the analyzer as a host program with GOOS=wasip1 and GOARCH=wasm in its
analysis environment, covering hi264/mp4ff that the root module scan omits.

Normal `go test ./internal/video` exercises the actual embedded decoder against
32x32 Baseline/CAVLC and High/CABAC IDRs and an independent FFmpeg gradient PNG.
It also covers malformed/truncated input, size and output limits, reset/reuse,
cancellation, deliberately infinite WASM, rejected memory growth, source
freshness, NotBefore, PLI retries and concurrent Await/Reset/Close during decode.
FuzzValidateIDR has a normal-suite seed and supports additional bounded fuzzing.

The opt-in TestEmbeddedDecoderLocalCapture uses JETKVM_H264_FIXTURE and optional
JETKVM_H264_PNG. A private 11985-byte H.264 IDR decoded to 1920x1080; the operator
confirmed the correct console visually. Initial timings were 2.157 seconds
including compilation and 1.368 seconds warm. The fail-closed patched artifact
was rechecked at 2.079 seconds cold and 1.356 seconds warm. Captures/screenshots
remain ignored local files and are never public test fixtures.

This proves one resolution and signal state. Six-platform CGO_ENABLED=0 builds
(macOS/Linux/Windows, amd64/arm64) are delegated to CI, not locally claimed.
Broader HIL resolution/signal-transition coverage and sustained multi-device
memory behavior remain release validation work.

The final current-candidate live HIL passed with the 5-second default source
freshness. The integration owner reported successful screenshot, move, click,
double-click, drag, scroll, and a move-plus-screenshot batch. MCP also passed
observe at 1920x1080, an ID-only observation-bound click with an observe-after
image at 1920x1080, and close. Total sequence time was 43.09 seconds; individual
operations took approximately 2.5 to 7.4 seconds. These operation durations
include work beyond decoding and are not measurements of frame source age.

Reported receipts were completed, transport_accepted, neutralized, and
retry_safe=false. Transport acceptance and neutralization do not by themselves
prove the target application's semantic response. The final result supersedes
the provisional 19.3-second sequence pass and subsequent stale-source failures
under the earlier 2-second freshness default. It verifies the current live
pipeline and default freshness at this hardware fixture, while the broader
resolution and signal-transition checks above remain outstanding.

## Alternatives

External FFmpeg violates the self-contained runtime requirement. Native
OpenH264/platform decoders add platform build and lifecycle paths; LGPL static
decoders add relinking/distribution obligations. The selected MIT WASI path
provides a portable bounded execution boundary while keeping IDR scope explicit.
