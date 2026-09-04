#!/bin/sh
set -eu

umask 077

RELEASE_TAG="__JETKVM_RELEASE_TAG__"
REPOSITORY="kaaanata/jetkvm-cli"
RELEASE_BASE_URL=${JETKVM_RELEASE_BASE_URL:-"https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"}
DEFAULT_INSTALL_DIR="${HOME:?HOME is required}/.local/bin"
INSTALL_DIR=${JETKVM_INSTALL_DIR:-"$DEFAULT_INSTALL_DIR"}
MANAGED=1
MODIFY_PATH=1

usage() {
  cat <<'EOF'
Install JetKVM CLI from a pinned GitHub release.

Usage: install.sh [--install-dir PATH] [--unmanaged]

  --install-dir PATH  Install into PATH (default: ~/.local/bin)
  --unmanaged         Do not write an installation receipt

Environment:
  JETKVM_NO_MODIFY_PATH=1  Do not add the install directory to a shell profile
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir)
      [ "$#" -ge 2 ] || { echo "missing value for --install-dir" >&2; exit 2; }
      INSTALL_DIR=$2
      shift 2
      ;;
    --unmanaged)
      MANAGED=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ "${JETKVM_UNMANAGED_INSTALL:-0}" = "1" ]; then
  MANAGED=0
fi
if [ "${JETKVM_NO_MODIFY_PATH:-0}" = "1" ]; then
  MODIFY_PATH=0
fi

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "tar is required" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

version=${RELEASE_TAG#v}
archive="jetkvm_${version}_${os}_${arch}.tar.gz"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/jetkvm-install.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

download() {
  source_url=$1
  destination=$2
  allowed_protocols='=https'
  if [ "${JETKVM_ALLOW_INSECURE_TEST_URL:-0}" = "1" ]; then
    allowed_protocols='=file,https'
  fi
  curl --proto "$allowed_protocols" --tlsv1.2 --fail --location --silent --show-error \
    "$source_url" --output "$destination"
}

download "$RELEASE_BASE_URL/$archive" "$work_dir/$archive"
download "$RELEASE_BASE_URL/checksums.txt" "$work_dir/checksums.txt"
download "$RELEASE_BASE_URL/checksums.txt.sigstore.json" "$work_dir/checksums.txt.sigstore.json"

archive_size=$(wc -c <"$work_dir/$archive" | tr -d ' ')
if [ "$archive_size" -le 0 ] || [ "$archive_size" -gt 134217728 ]; then
  echo "release archive size is outside the allowed range" >&2
  exit 1
fi

if command -v cosign >/dev/null 2>&1; then
  cosign verify-blob "$work_dir/checksums.txt" \
    --bundle "$work_dir/checksums.txt.sigstore.json" \
    --certificate-identity "https://github.com/$REPOSITORY/.github/workflows/release.yml@refs/heads/main" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" >/dev/null
else
  echo "notice: cosign is not installed; continuing with mandatory SHA-256 verification" >&2
fi

expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$work_dir/checksums.txt")
case "$expected" in
  *[!0-9a-fA-F]*|'') echo "release checksum is missing or invalid for $archive" >&2; exit 1 ;;
esac
[ "${#expected}" -eq 64 ] || { echo "release checksum has an invalid length" >&2; exit 1; }
[ "$(awk -v name="$archive" '$2 == name { count++ } END { print count + 0 }' "$work_dir/checksums.txt")" -eq 1 ] || {
  echo "release checksum entry is not unique" >&2
  exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work_dir/$archive" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$work_dir/$archive" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo "SHA-256 verification failed for $archive" >&2; exit 1; }

entries=$(tar -tzf "$work_dir/$archive")
[ "$(printf '%s\n' "$entries" | sed '/^$/d' | wc -l | tr -d ' ')" -eq 4 ] || {
  echo "release archive contains an unexpected number of entries" >&2
  exit 1
}
for required in jetkvm LICENSE NOTICE README.md; do
  [ "$(printf '%s\n' "$entries" | awk -v name="$required" '$0 == name { count++ } END { print count + 0 }')" -eq 1 ] || {
    echo "release archive does not contain exactly one $required" >&2
    exit 1
  }
done
printf '%s\n' "$entries" | awk '
  /^\// || /(^|\/)\.\.?(\/|$)/ || /\/$/ { exit 1 }
  $0 != "jetkvm" && $0 != "LICENSE" && $0 != "NOTICE" && $0 != "README.md" { exit 1 }
' || { echo "release archive contains an unsafe path" >&2; exit 1; }
tar -tvzf "$work_dir/$archive" | awk 'substr($1, 1, 1) != "-" { exit 1 }' || {
  echo "release archive contains a non-regular entry" >&2
  exit 1
}

mkdir -p "$work_dir/extract" "$INSTALL_DIR"
INSTALL_DIR=$(CDPATH='' cd -- "$INSTALL_DIR" && pwd -P)
staged_binary="$work_dir/extract/jetkvm"
tar -xOzf "$work_dir/$archive" jetkvm | dd of="$staged_binary" bs=1048576 count=65 2>/dev/null
binary_size=$(wc -c <"$staged_binary" | tr -d ' ')
if [ "$binary_size" -le 0 ] || [ "$binary_size" -gt 67108864 ]; then
  echo "release executable size is outside the allowed range" >&2
  exit 1
fi
chmod 0755 "$staged_binary"
install_tmp="$INSTALL_DIR/.jetkvm.install.$$"
cp "$staged_binary" "$install_tmp"
chmod 0755 "$install_tmp"
mv -f "$install_tmp" "$INSTALL_DIR/jetkvm"

if [ "$MANAGED" -eq 1 ]; then
  receipt_path="$INSTALL_DIR/.jetkvm-install.json"
  install_hex=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
  install_id="$(printf '%s' "$install_hex" | cut -c1-8)-$(printf '%s' "$install_hex" | cut -c9-12)-4$(printf '%s' "$install_hex" | cut -c14-16)-8$(printf '%s' "$install_hex" | cut -c18-20)-$(printf '%s' "$install_hex" | cut -c21-32)"
  installed_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  json_escape() { printf '%s' "$1" | awk '{ gsub(/\\/, "\\\\"); gsub(/\"/, "\\\""); printf "%s", $0 }'; }
  receipt_tmp="$INSTALL_DIR/.jetkvm-install.$$"
  printf '{"schema_version":1,"install_id":"%s","owner":"standalone","executable":"%s","version":"%s","repository":"%s","channel":"stable","installed_at":"%s"}\n' \
    "$install_id" "$(json_escape "$INSTALL_DIR/jetkvm")" "$(json_escape "$version")" "$REPOSITORY" "$installed_at" >"$receipt_tmp"
  chmod 0600 "$receipt_tmp"
  mv -f "$receipt_tmp" "$receipt_path"
else
  rm -f "$INSTALL_DIR/.jetkvm-install.json"
fi

case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    profile=""
    if [ "$MODIFY_PATH" -eq 1 ] && [ "$INSTALL_DIR" = "$DEFAULT_INSTALL_DIR" ]; then
      case "${SHELL:-}" in
        */zsh) profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
        */bash) profile="$HOME/.bashrc" ;;
      esac
    fi
    if [ -n "$profile" ]; then
      marker="# JetKVM CLI"
      if [ ! -f "$profile" ] || ! grep -F "$marker" "$profile" >/dev/null 2>&1; then
        {
          printf '\n%s\n' "$marker"
          # shellcheck disable=SC2016 # Expand HOME and PATH when the profile is sourced.
          printf 'export PATH="$HOME/.local/bin:$PATH"\n'
        } >>"$profile"
      fi
      echo "Added $INSTALL_DIR to PATH in $profile; start a new shell before running jetkvm."
    else
      echo "Add $INSTALL_DIR to PATH before running jetkvm." >&2
    fi
    ;;
esac

echo "Installed jetkvm $version to $INSTALL_DIR/jetkvm"
