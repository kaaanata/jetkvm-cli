#!/bin/sh
# Build-only toolchain, pinned by release asset SHA-256. Never run by the product.
set -eu
output=${1:?output directory required}
case "$(uname -s)-$(uname -m)" in
 Linux-x86_64) platform=x86_64-linux; digest=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4 ;;
 Darwin-arm64) platform=arm64-macos; digest=9c59398106b417f8f14913380fdf0097a8cc0ff4af9eb3ce0065a859e88d49e9 ;;
 *) echo 'Use a verified wasi-sdk-34.0 installation via WASI_SDK_PATH on this build host.' >&2; exit 1 ;;
esac
mkdir -p "$output"
archive="$output/wasi-sdk.tar.gz"
curl --fail --location --silent --show-error "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-34/wasi-sdk-34.0-$platform.tar.gz" -o "$archive"
printf '%s  %s\n' "$digest" "$archive" | shasum -a 256 -c -
tar -xzf "$archive" -C "$output"
printf '%s\n' "$output/wasi-sdk-34.0-$platform"
