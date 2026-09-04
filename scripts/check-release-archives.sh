#!/bin/sh
set -eu
dist_dir=${1:?release dist directory required}
dist_dir=$(CDPATH='' cd -- "$dist_dir" && pwd)
JETKVM_TEST_RELEASE_DIST="$dist_dir" \
  go test ./internal/update -run '^TestBuiltReleaseArchiveCompatibility$' -count=1
