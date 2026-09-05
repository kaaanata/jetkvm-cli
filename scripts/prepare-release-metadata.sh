#!/bin/sh
set -eu
output_dir=${1:?output directory required}
mkdir -p "$output_dir"
# Preserve the complete codec license without changing the four-file archive.
awk '1' NOTICE internal/video/DECODER_LICENSES.txt > "$output_dir/NOTICE"
python3 scripts/decoder-metadata.py "$output_dir/decoder.sbom.json"
# Publish exact corresponding codec source and build material as a separate asset.
tar -czf "$output_dir/decoder-source.tar.gz" internal/video/wasmdecoder scripts/build-decoder.sh scripts/setup-wasi-sdk.sh internal/video/DECODER_LICENSES.txt
