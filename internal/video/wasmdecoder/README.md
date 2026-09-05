# Embedded H.264 decoder source

The checked-in reactor embeds FFmpeg 9.0.1 `libavcodec` (H.264 decoder only) and
`libavutil`, compiled with wasi-sdk 34.0 / LLVM 23.1.0. No network, format readers,
encoders, external codecs, GPL components, or nonfree components are enabled.
The binary executes in wazero with a 512 MiB linear-memory ceiling. One session
owns one reactor and serialized calls. The library is not used concurrently.

`ffmpeg-9.0.1.tar.xz` is the complete unmodified upstream corresponding source.
SHA-256 is pinned in `SOURCE.sha256`. Its detached upstream signature was verified
with FFmpeg's release key `FCF986EA15E6E293A5644F10B4322F04D67658D8` before vendoring.
`reactor.c` is the small project-owned ABI adapter. There are no codec patches.

## Rebuilding and replacing

From a checkout of the corresponding JetKVM release:

```sh
sh scripts/setup-wasi-sdk.sh "$PWD/.cache/wasi-sdk"
# On Linux x86-64; use arm64-macos on Apple Silicon.
export WASI_SDK_PATH="$PWD/.cache/wasi-sdk/wasi-sdk-34.0-x86_64-linux"
sh scripts/build-decoder.sh
CGO_ENABLED=0 go build -trimpath -o jetkvm ./cmd/jetkvm
```

You may modify or replace the library source, rebuild `decoder.wasm`, and rebuild
JetKVM with that replacement. When modifying the archive intentionally, update
`SOURCE.sha256` to the hash of your modified source. The build hash check verifies
input identity; it is not a restriction on modification. No signing key is
required to build and run a modified executable.

FFmpeg's enabled components are LGPL-2.1-or-later; the complete license is in
`../DECODER_LICENSES.txt` and inside the source archive. JetKVM's own code retains
its repository license. Releases include the full license in executable archive
NOTICE and publish `decoder-source.tar.gz` separately with signed checksums.
That asset includes the source archive, this adapter, and the build/setup scripts.
The linked wasi-libc and LLVM compiler-rt runtime notices are included in the
same license file; their exact SDK source identities are recorded in the SBOM.
The corresponding tagged JetKVM source supplies the rest of the relinkable Go
application. No extra files are added to the four-file executable archives.

## ABI and trust

`input_ptr` exposes an 8 MiB input area plus codec-required zero padding.
`decode(length, source_token, drain)` submits one complete access unit.
`output_ptr` exposes nine little-endian 32-bit words: dimensions, Y/UV strides,
three YUV plane pointers, and the original 64-bit source token.
A zero width means accepted input without output, not a decoded observation.
The host validates every range and copies all planes before the next call.
Tokens identify original AUs across codec reordering; they are not timestamps
fabricated at decoder completion. `drain` is used only for finite offline inputs.
Any codec error or corrupted-frame indication fails closed. A trap or timeout
destroys the reactor; live recovery waits for a complete independent IDR.
