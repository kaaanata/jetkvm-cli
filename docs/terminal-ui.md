# Terminal UI

Human output uses Lip Gloss v2 documents/tables and Huh v2 inline forms, with
one theme in `internal/terminal`. Cobra owns command parsing, flags, completion,
and command metadata. Charm Log continues to own diagnostics on stderr.

Use `--output=json` for stable machine-readable receipts. Non-terminal stdout
defaults to JSON. `--output=text` on a pipe, a nonempty `NO_COLOR`, `TERM=dumb`,
or `JETKVM_ACCESSIBLE=1` disables control sequences. The accessible setting also
selects Huh's linear prompt. No command requires an emoji font or an alternate
screen. Narrow tables stack and long values wrap instead of being truncated.

## Verification

### Outcome and help presentation

Human results start with the recorded outcome. A current installation produces
only `Already up to date — JetKVM <version>.` Applied updates and rollbacks show
the previous and current versions. Artifact verification is reported only when
the receipt records it; rollback does not imply a new signature check. The undo
command appears only when rollback is available. Installer-owned updates show
the owning installer and its required command without claiming an update ran.
Input results preserve the terminal claim, delivery, verification, retry safety
and neutralization fields. Setup failures retain their failure kind.

Root help uses Inspect, Control, Integrate and Maintain groups, getting-started
examples and live Cobra descriptions/flags. Newly registered commands remain
visible under More commands; Cloud, when registered, belongs to Integrate.
Headerless label/value rows fit their content and wrap within the measured width;
actual data tables retain column headers and stack on narrow terminals.

The PTY fixtures cover 40 and 80 columns in color and plain modes. To retain
actual captured streams, plain text and SVG previews outside version control:

```sh
JETKVM_TEST_PTY_EVIDENCE="$PWD/.cache/ui-evidence" \
  go test ./internal/cli -run '^TestTerminalPTY$' -count=1
```

The `.ansi` files contain original PTY bytes; `.txt` and `.svg` are readable
projections of those same bytes. Update, setup, input and screenshot receipts
are deterministic fixtures, not evidence of a live device action or install.
Confirmation streams are retained without a static SVG because they include
cursor updates. The evidence directory is small and contains no Go build cache.

Run from the repository root:

```sh
go test ./...
go test -race ./internal/terminal ./internal/cli ./cmd/jetkvm ./internal/confirmation ./internal/setup ./internal/update
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
go test ./internal/cli -run '^TestTerminalPTY$' -count=1 -v
go test -race ./internal/cli -run '^TestTerminalPTY$' -count=10
go test -race ./internal/terminal -run '^TestTerminalReaderJoinsReadBeforeReturning$' -count=100
go test ./internal/cli -run 'TestJSONBytesIndependentOfPresentation|TestMCPOutputBypassesPresentation' -count=1 -v
go test ./internal/terminal -run 'TestPlainOutputAndWidth|TestConfirm' -count=1 -v
```

PTY tests run on macOS/Linux using Charm's `x/xpty`, with fake device services.
They show real help, status, error and confirmation rendering without opening
any hardware session. They verify default rejection and affirmative choices,
plain no-color/dumb/accessibility output, and absence of alternate-screen use.
Renderer tests cover widths 24, 59, 60, 80 and 120, Unicode, injected terminal
commands, `CLICOLOR_FORCE`, I/O failure and cancellation. JSON tests compare
fixed bytes across TTY and color modes; an MCP fixture writes protocol bytes
through the transport output and checks exact equality. The executable's proof
tests verify generation binding and single-use consumption after confirmation.

For isolated local verification, set `GOCACHE` to a private persistent directory
if another concurrent task is cleaning the shared Go cache.

### Terminal reader shutdown regression

The initial Bubble Tea v2.0.2 / Ultraviolet `524a6607adb8` graph could return
from `StreamEvents` on cancellation while its internal Read goroutine was still
using cancelreader's kqueue descriptor. Bubble Tea then closed cancelreader,
causing `os.File.Close` to race with `os.File.Fd`. A single green race run did not
prove the lifecycle correct: the local ten-run PTY reproduction failed in both
affirmative and default-negative confirmation.

The fix upgrades to the official stable
[Bubble Tea v2.0.9](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.9),
whose module pins Ultraviolet `f5a850f9c2b7`. That revision contains
[Ultraviolet #94 / 0b88c25](https://github.com/charmbracelet/ultraviolet/commit/0b88c25f3fff665a5f9dfd226ee71868f5e8d51a),
which waits for the internal Read goroutine on the context and error exits.
The blocked-reader regression fails immediately on the old dependency and
checks that cancellation cannot be mistaken for Read completion.

Upgrading alone fixes ordinary confirmation, but the additional timeout test
still reproduced a race in v2.0.9: its `shutdown(kill=true)` bypasses the input
join. Interactive confirmation therefore runs the Huh form as a Bubble Tea
model, converts operation cancellation and Huh abort into graceful Quit, and
joins Program.Run before returning the original cancellation or rejection.
The executable's existing signal context owns signals. A short view/model
adapter preserves Huh's components and key handling. PTY coverage exercises
affirmative input, default rejection, Ctrl-C, timeout and plain/accessibility
output. No race suppression, PTY skip, handwritten UI or upstream source fork
is used.

## Intentional output exceptions

- JSON result/error envelopes: unchanged serializer, fields and exit taxonomy.
- MCP stdout: JSON-RPC transport, never a human renderer or a form destination.
- Cobra completion scripts and `__complete` directives: executable shell or
  shell-completion protocol text, never styled.
- PNG files and explicitly requested base64 image data: artifact/protocol bytes.
- Bootstrap `install.sh` and `install.ps1`: run before the binary exists; remain
  dependency-free scripts with their existing output and installation receipt.
- Setup host process stdout/stderr: captured for parsing and ownership readback,
  then projected as JetKVM plans/receipts; never streamed into human results.

## Charm ecosystem review

Review date: 2026-09-05. The GitHub organization API listed 56 public repositories,
including forks and archived projects. The inventory below covers their stated
purpose, README overview and maintenance metadata; it is not a claim of a
complete source/security audit of every repository. UI candidates received a
closer API/source review. Sources: [organization](https://github.com/charmbracelet),
[repository API](https://api.github.com/orgs/charmbracelet/repos?per_page=100&type=public),
and each linked repository below.

| Repository | Role and decision |
| --- | --- |
| [lipgloss](https://github.com/charmbracelet/lipgloss) | Adopt v2 layout/style/table components for all human documents. |
| [huh](https://github.com/charmbracelet/huh) | Adopt v2.0.3 forms; compiles with Go 1.27 and the current graph. Accessible runner needs adapter-owned cancellation and I/O checks. |
| [bubbletea](https://github.com/charmbracelet/bubbletea) | Inline interactive lifecycle through Huh; no persistent/full-screen application. |
| [bubbles](https://github.com/charmbracelet/bubbles) | Form help/key/input primitives through Huh; static receipts use Lip Gloss tables. |
| [log](https://github.com/charmbracelet/log) | Retain v2 stderr diagnostics with the same plain-output policy. |
| [colorprofile](https://github.com/charmbracelet/colorprofile) | Per-destination color handling; no global renderer mutation. |
| [x](https://github.com/charmbracelet/x) | Use existing ANSI/terminal utilities and test-only xpty. Reviewed subpackage inventory, including teatest, golden, mosaic, vt, pony and wcwidth. Avoid adding experimental markup/image/terminal layers. |
| [ultraviolet](https://github.com/charmbracelet/ultraviolet) | Underlying v2 terminal primitives; use through the supported components. |
| [fang](https://github.com/charmbracelet/fang) | Local source reference only at b1722e95d5cc668bb1888e9e2196d0b0173d5aa7. Help/theme/execute code reviewed. Use semantic hierarchy, content-sized labels and usage hints; retain JetKVM's error taxonomy, parser and lifecycle. No copied source or dependency. |
| [glamour](https://github.com/charmbracelet/glamour) | Markdown renderer; current typed receipts/help do not require Markdown parsing. |
| [glow](https://github.com/charmbracelet/glow) | Standalone Markdown reader; not a production dependency. |
| [gum](https://github.com/charmbracelet/gum) | Shell UI executable; use the Go components directly to retain one binary. |
| [harmonica](https://github.com/charmbracelet/harmonica) | Spring animation; no animation requirement for command receipts. |
| [vhs](https://github.com/charmbracelet/vhs) | Demo/recording tool; optional development use, not a runtime dependency. |
| [vhs-action](https://github.com/charmbracelet/vhs-action) | Recording CI action; PTY tests cover the current verification need. |
| [tree-sitter-vhs](https://github.com/charmbracelet/tree-sitter-vhs) | VHS syntax highlighting; no application use. |
| [freeze](https://github.com/charmbracelet/freeze) | Code/terminal screenshot generator; optional documentation tool. |
| [sequin](https://github.com/charmbracelet/sequin) | ANSI debugging utility; useful reference for protocol-aware tests. |
| [xunicode](https://github.com/charmbracelet/xunicode) | Experimental Unicode segmentation; existing Lip Gloss width/wrap handling suffices. |
| [console](https://github.com/charmbracelet/console) | Console library fork; existing terminal/Bubble Tea infrastructure suffices. |
| [bubbletea-app-template](https://github.com/charmbracelet/bubbletea-app-template) | Starter template; this repository already has command and release architecture. |
| [wizard-tutorial](https://github.com/charmbracelet/wizard-tutorial) | Example wizard; Huh provides maintained form primitives. |
| [wish](https://github.com/charmbracelet/wish) | SSH applications; outside local CLI UI scope. |
| [ssh](https://github.com/charmbracelet/ssh) | SSH server library; outside scope. |
| [wishlist](https://github.com/charmbracelet/wishlist) | SSH directory; outside scope. |
| [promwish](https://github.com/charmbracelet/promwish) | Wish Prometheus middleware; outside scope. |
| [soft-serve](https://github.com/charmbracelet/soft-serve) | Git server/TUI; application reference, not a reusable receipt renderer. |
| [soft-serve-action](https://github.com/charmbracelet/soft-serve-action) | Git synchronization action; outside scope. |
| [git-lfs-transfer](https://github.com/charmbracelet/git-lfs-transfer) | Git LFS SSH protocol; outside scope. |
| [keygen](https://github.com/charmbracelet/keygen) | SSH key generation; not needed for UI. |
| [melt](https://github.com/charmbracelet/melt) | SSH key backup; not needed for UI. |
| [confettysh](https://github.com/charmbracelet/confettysh) | SSH confetti demo; not appropriate for device action receipts. |
| [charm](https://github.com/charmbracelet/charm) | Archived Charm Cloud/tool; no dependency. |
| [skate](https://github.com/charmbracelet/skate) | Local key-value application; do not replace domain storage. |
| [pop](https://github.com/charmbracelet/pop) | Email application; outside scope. |
| [hotdiva2000](https://github.com/charmbracelet/hotdiva2000) | Human-readable random names; do not replace stable device/operation identities. |
| [crush](https://github.com/charmbracelet/crush) | Coding-agent application; not JetKVM's command lifecycle. |
| [mods](https://github.com/charmbracelet/mods) | Archived AI CLI; no dependency. |
| [catwalk](https://github.com/charmbracelet/catwalk) | Model catalog; outside scope. |
| [fantasy](https://github.com/charmbracelet/fantasy) | Agent/model API; outside deterministic CLI action scope. |
| [go-genai](https://github.com/charmbracelet/go-genai) | Model SDK fork; outside scope. |
| [anthropic-sdk-go](https://github.com/charmbracelet/anthropic-sdk-go) | Model SDK fork; outside scope. |
| [openai-go](https://github.com/charmbracelet/openai-go) | Model SDK fork; outside scope. |
| [pi-hyper-provider](https://github.com/charmbracelet/pi-hyper-provider) | Pi model-provider extension; outside scope. |
| [a2tea](https://github.com/charmbracelet/a2tea) | Early A2UI bridge/mirror; no new agent-driven UI protocol for JetKVM. |
| [sh](https://github.com/charmbracelet/sh) | Shell parser/interpreter fork; no shell execution architecture change. |
| [homebrew-tap](https://github.com/charmbracelet/homebrew-tap) | Packaging formulas; no README, checked root inventory. |
| [scoop-bucket](https://github.com/charmbracelet/scoop-bucket) | Packaging manifests; outside UI implementation. |
| [winget-pkgs](https://github.com/charmbracelet/winget-pkgs) | Package manifest fork; outside UI implementation. |
| [nur](https://github.com/charmbracelet/nur) | Nix packaging; outside UI implementation. |
| [meta](https://github.com/charmbracelet/meta) | Shared release/workflow configuration; preserve JetKVM release authority. |
| [.github](https://github.com/charmbracelet/.github) | Community defaults; no root README, checked root inventory. |
| [inspo](https://github.com/charmbracelet/inspo) | Archived project inspiration; no dependency. |
| [runway](https://github.com/charmbracelet/runway) | 3D artwork; unrelated to terminal components. |
| [markscribe](https://github.com/charmbracelet/markscribe) | Markdown template generator; unnecessary for typed CLI output. |
| [readme-scribe](https://github.com/charmbracelet/readme-scribe) | README generation action; outside scope. |
