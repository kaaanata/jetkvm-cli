#!/bin/sh
# Rebuild the H.264-only LGPL WASI reactor. No external codec is needed at runtime.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
: "${WASI_SDK_PATH:?Set WASI_SDK_PATH to wasi-sdk-34.0}"
case "$("$WASI_SDK_PATH/bin/clang" --version)" in *"clang version 23.1.0-wasi-sdk"*) ;; *) echo 'wasi-sdk-34 (LLVM 23.1.0) required' >&2; exit 1;; esac
build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT HUP INT TERM
src="$root/internal/video/wasmdecoder"
# Verify the vendored, upstream-signed source before executing its build scripts.
(cd "$src" && shasum -a 256 -c SOURCE.sha256)
tar -xf "$src/ffmpeg-9.0.1.tar.xz" -C "$build_dir"
mkdir "$build_dir/build"
cd "$build_dir/build"
"$build_dir/ffmpeg-9.0.1/configure" \
 --cc="$WASI_SDK_PATH/bin/clang" --ar="$WASI_SDK_PATH/bin/llvm-ar" --ranlib="$WASI_SDK_PATH/bin/llvm-ranlib" \
 --target-os=none --arch=wasm32 --enable-cross-compile --disable-autodetect --disable-everything \
 --disable-programs --disable-doc --disable-network --disable-pthreads --disable-w32threads --disable-os2threads \
 --disable-avdevice --disable-avformat --disable-avfilter --disable-swresample --disable-swscale --disable-iconv \
 --disable-debug --disable-asm --enable-decoder=h264 \
 --extra-cflags="-O3 -msimd128 -D_WASI_EMULATED_SIGNAL -ffile-prefix-map=$build_dir=. -ffile-prefix-map=$WASI_SDK_PATH=/wasi-sdk" \
 --extra-ldflags='-lwasi-emulated-signal -lwasi-emulated-process-clocks' > "$build_dir/configure.log" 2>&1 || { cat "$build_dir/configure.log"; exit 1; }
make -j "${DECODER_BUILD_JOBS:-4}" libavcodec/libavcodec.a libavutil/libavutil.a > "$build_dir/build.log" 2>&1 || { tail -80 "$build_dir/build.log"; exit 1; }
"$WASI_SDK_PATH/bin/clang" -O3 -msimd128 -I"$build_dir/ffmpeg-9.0.1" -I. \
 -mexec-model=reactor "$src/reactor.c" libavcodec/libavcodec.a libavutil/libavutil.a \
 -lwasi-emulated-signal -lwasi-emulated-process-clocks -Wl,--strip-all \
 -Wl,-z,stack-size=1048576 -Wl,--max-memory=536870912 -o "$build_dir/decoder.wasm"
cp "$build_dir/decoder.wasm" "$root/internal/video/decoder.wasm"
