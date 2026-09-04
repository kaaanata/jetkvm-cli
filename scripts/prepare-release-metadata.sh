#!/bin/sh
set -eu
output_dir=${1:?output directory required}
mkdir -p "$output_dir"
# Preserve complete upstream MIT grants without adding archive entries.
awk '1' NOTICE internal/video/DECODER_LICENSES.txt > "$output_dir/NOTICE"
# The WASI binary is opaque to archive scanners: scan its pinned module graph
# independently, and publish the document outside the legacy four-file archive.
syft scan dir:internal/video/wasmdecoder --output "spdx-json=$output_dir/decoder.sbom.json"
