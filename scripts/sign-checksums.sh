#!/bin/sh
set -eu

checksum_path=$1
bundle_path=$2
# This is a pre-sign/pre-upload gate on real GoReleaser outputs, not fixtures.
sh scripts/check-release-archives.sh "$(dirname "$checksum_path")"
cosign sign-blob --yes --bundle "$bundle_path" "$checksum_path"
checksum_path=$(cd "$(dirname "$checksum_path")" && pwd)/$(basename "$checksum_path")
bundle_path=$(cd "$(dirname "$bundle_path")" && pwd)/$(basename "$bundle_path")
JETKVM_TEST_CHECKSUM_PATH="$checksum_path" JETKVM_TEST_BUNDLE_PATH="$bundle_path" \
  go test ./internal/update -run '^TestPublishedReleaseSignature$' -count=1
