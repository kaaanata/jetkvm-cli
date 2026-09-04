#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repository_dir=$(CDPATH='' cd -- "$script_dir/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/jetkvm-installer-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM

"$script_dir/render-installers.sh" v9.8.7 "$test_dir/rendered"
grep -q 'RELEASE_TAG="v9.8.7"' "$test_dir/rendered/install.sh"
grep -q "\$ReleaseTag = \"v9.8.7\"" "$test_dir/rendered/install.ps1"
if grep -q '__JETKVM_RELEASE_TAG__' "$test_dir/rendered/install.sh"; then exit 1; fi
if grep -q '__JETKVM_RELEASE_TAG__' "$test_dir/rendered/install.ps1"; then exit 1; fi

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
  Darwin/x86_64) target=darwin_amd64 ;;
  Darwin/arm64) target=darwin_arm64 ;;
  Linux/x86_64) target=linux_amd64 ;;
  Linux/aarch64|Linux/arm64) target=linux_arm64 ;;
  *) echo "installer execution test is unsupported on $os/$arch" >&2; exit 1 ;;
esac

release_dir="$test_dir/release"
payload_dir="$test_dir/payload"
mkdir -p "$release_dir" "$payload_dir"
printf '#!/bin/sh\necho test\n' >"$payload_dir/jetkvm"
chmod 0755 "$payload_dir/jetkvm"
cp "$repository_dir/LICENSE" "$repository_dir/NOTICE" "$repository_dir/README.md" "$payload_dir/"
archive="jetkvm_9.8.7_${target}.tar.gz"
(cd "$payload_dir" && tar -czf "$release_dir/$archive" jetkvm LICENSE NOTICE README.md)
if command -v sha256sum >/dev/null 2>&1; then
  digest=$(sha256sum "$release_dir/$archive" | awk '{print $1}')
else
  digest=$(shasum -a 256 "$release_dir/$archive" | awk '{print $1}')
fi
printf '%s  %s\n' "$digest" "$archive" >"$release_dir/checksums.txt"
printf '{}\n' >"$release_dir/checksums.txt.sigstore.json"

shim_dir="$test_dir/shims"
mkdir -p "$shim_dir"
printf '#!/bin/sh\nexit 0\n' >"$shim_dir/cosign"
chmod 0755 "$shim_dir/cosign"
install_dir="$test_dir/install"
PATH="$shim_dir:$PATH" JETKVM_ALLOW_INSECURE_TEST_URL=1 JETKVM_RELEASE_BASE_URL="file://$release_dir" \
  "$test_dir/rendered/install.sh" --install-dir "$install_dir"
test -x "$install_dir/jetkvm"
receipt="$install_dir/.jetkvm-install.json"
canonical_install_dir=$(CDPATH='' cd -- "$install_dir" && pwd -P)
grep -q '"owner":"standalone"' "$receipt"
grep -q '"version":"9.8.7"' "$receipt"
grep -Eq '"install_id":"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}"' "$receipt"
grep -q "\"executable\":\"$canonical_install_dir/jetkvm\"" "$receipt"
grep -q '"schema_version":1' "$receipt"
grep -q '"repository":"kaaanata/jetkvm-cli"' "$receipt"
grep -q '"channel":"stable"' "$receipt"
grep -Eq '"installed_at":"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z"' "$receipt"

unmanaged_dir="$test_dir/unmanaged"
mkdir -p "$unmanaged_dir"
printf '{"stale":true}\n' >"$unmanaged_dir/.jetkvm-install.json"
PATH="$shim_dir:$PATH" JETKVM_ALLOW_INSECURE_TEST_URL=1 JETKVM_RELEASE_BASE_URL="file://$release_dir" \
  "$test_dir/rendered/install.sh" --install-dir "$unmanaged_dir" --unmanaged >/dev/null
test -x "$unmanaged_dir/jetkvm"
test ! -e "$unmanaged_dir/.jetkvm-install.json"

malicious_dir="$test_dir/malicious"
mkdir -p "$malicious_dir"
cp "$payload_dir/jetkvm" "$payload_dir/LICENSE" "$payload_dir/NOTICE" "$malicious_dir/"
ln -s /etc/passwd "$malicious_dir/README.md"
(cd "$malicious_dir" && tar -czf "$release_dir/$archive" jetkvm LICENSE NOTICE README.md)
if command -v sha256sum >/dev/null 2>&1; then
  digest=$(sha256sum "$release_dir/$archive" | awk '{print $1}')
else
  digest=$(shasum -a 256 "$release_dir/$archive" | awk '{print $1}')
fi
printf '%s  %s\n' "$digest" "$archive" >"$release_dir/checksums.txt"
if PATH="$shim_dir:$PATH" JETKVM_ALLOW_INSECURE_TEST_URL=1 JETKVM_RELEASE_BASE_URL="file://$release_dir" \
  "$test_dir/rendered/install.sh" --install-dir "$test_dir/rejected" --unmanaged >/dev/null 2>&1; then
  echo "installer accepted a symbolic-link archive entry" >&2
  exit 1
fi

echo "installer tests passed"
