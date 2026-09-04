#!/bin/sh
set -eu

usage() {
  echo "usage: $0 <release-tag> [output-directory]" >&2
  exit 2
}

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  usage
fi

release_tag=$1
output_dir=${2:-dist}

printf '%s\n' "$release_tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' || {
  echo "release tag must be a v-prefixed semantic version" >&2
  exit 2
}

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$output_dir"

render() {
  source_file=$1
  destination_file=$2
  sed "s/__JETKVM_RELEASE_TAG__/$release_tag/g" "$source_file" >"$destination_file"
}

render "$script_dir/install.sh" "$output_dir/install.sh"
render "$script_dir/install.ps1" "$output_dir/install.ps1"
chmod 0755 "$output_dir/install.sh"
