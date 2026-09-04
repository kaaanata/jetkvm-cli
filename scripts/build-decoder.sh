#!/bin/sh
# Rebuild the checked-in WASI command with the pinned Go toolchain and modules.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root/internal/video/wasmdecoder"
export GOTOOLCHAIN=go1.27.0 GOWORK=off CGO_ENABLED=0
go mod download
go mod verify
upstream=$(go list -m -f '{{.Dir}}' github.com/Eyevinn/hi264)
build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT HUP INT TERM
# Go forbids overlays inside the module cache. Copy only source packages into
# this build-owned directory, keeping the verified module cache immutable.
mkdir "$build_dir/hi264"
cp -R "$upstream/pkg" "$upstream/internal" "$upstream/go.mod" "$build_dir/hi264/"
chmod -R u+w "$build_dir/hi264"
cp main.go go.mod go.sum "$build_dir/"
go run -mod=readonly ./buildoverlay "$build_dir/hi264" "$build_dir"
cd "$build_dir"
go mod edit -replace=github.com/Eyevinn/hi264=./hi264
GOOS=wasip1 GOARCH=wasm go build -mod=readonly -overlay="$build_dir/overlay.json" -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$root/internal/video/decoder.wasm" .
