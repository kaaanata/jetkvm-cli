# Install and Update

JetKVM CLI uses one rule for every platform: the mechanism that installs the executable owns its updates.

## Recommended installation

macOS and Linux:

```sh
curl --proto '=https' --tlsv1.2 -LsSf \
  https://github.com/kaaanata/jetkvm-cli/releases/latest/download/install.sh | sh
```

Windows:

```powershell
irm https://github.com/kaaanata/jetkvm-cli/releases/latest/download/install.ps1 | iex
```

The release-published installer selects only a supported operating-system and architecture pair, downloads the matching archive and release verification material, verifies the mandatory SHA-256 digest, and installs a single executable. When Cosign is available it also verifies the Sigstore bundle and exact release-workflow identity. It does not download or execute an archive as a stream. The built-in `jetkvm update` path always performs Sigstore verification because its verifier ships inside the binary.

The installer adds its user-local directory to supported shell or Windows user PATH configuration when needed. Set `JETKVM_NO_MODIFY_PATH=1` to keep shell configuration untouched. Start a new terminal after a PATH change.

To audit or pin the installer, download it from an immutable release tag before running it:

```sh
JETKVM_VERSION=vX.Y.Z
curl --proto '=https' --tlsv1.2 -fLo install.sh \
  "https://github.com/kaaanata/jetkvm-cli/releases/download/${JETKVM_VERSION}/install.sh"
less install.sh
sh install.sh
```

## Supported release targets

Official releases provide a single executable for:

- macOS on AMD64 and ARM64;
- Linux on AMD64 and ARM64;
- Windows on AMD64 and ARM64.

An installer must reject an unknown operating system or architecture. It must not guess, emulate, or silently substitute another target.

## Installation ownership

The installation receipt records a closed owner value, the canonical executable path, release channel, version, repository, install identity, and installation time. The receipt is provenance, not a credential.

| Owner | Update authority |
|---|---|
| `standalone` | `jetkvm update` verifies and atomically replaces the executable |
| `homebrew` | Homebrew |
| `winget` | WinGet |
| `scoop` | Scoop |
| `deb` | APT/dpkg |
| `rpm` | DNF/RPM |
| `source` | The source build/install workflow |
| `unmanaged` | The external deployment system |
| `unknown` | No automated mutation is allowed |

The updater verifies that the receipt refers to the running executable before acting. Package-manager, source, unmanaged, and unknown installations are never overwritten by the standalone updater.

## Update commands

```text
jetkvm update
jetkvm update --check
jetkvm update --version v1.2.3
jetkvm update --channel prerelease
jetkvm update --version v1.1.0 --allow-downgrade
jetkvm update rollback
```

Update commands support the global text/JSON output contract. A non-interactive invocation never mixes an interactive package-manager prompt into result output. When another owner must act, the result is `action_required` and includes the exact next command.

Downgrades require both an exact version and `--allow-downgrade`. Prereleases require an explicit channel. The Git tag, not a release title or mutable branch, is the version authority.

## Standalone update transaction

A standalone update is one durable transaction:

1. Acquire the installation update lock.
2. Resolve an immutable release and closed platform artifact name.
3. Download beside the target filesystem, not to an unrelated mount.
4. Verify publisher identity, checksum metadata, and the archive digest.
5. Safely extract exactly one expected executable.
6. Run the candidate's machine-readable version command and compare version, OS, architecture, and commit with the plan.
7. Persist the prepared update receipt and retain the previous verified executable.
8. Atomically switch to the candidate using the platform-specific protocol.
9. Run a minimal self-check and commit the receipt.
10. Restore the previous executable if activation or self-check fails.

Windows uses a helper after the original process exits because a running executable cannot replace itself. Rollback is available only for a standalone installation with a retained, verified previous executable.

## Security properties

- Installation defaults to a user-owned binary directory and does not silently request elevation.
- Temporary files use private permissions and are removed on exit or interruption.
- Archive extraction rejects absolute paths, parent traversal, links, duplicate entries, unexpected files, and size-limit violations.
- A checksum detects corruption but does not establish publisher identity. The self-updater therefore requires Sigstore verification; bootstrap users can additionally verify the published GitHub provenance or install Cosign before running the installer.
- Update receipts contain no device credentials, MCP bearer tokens, or host secrets.
