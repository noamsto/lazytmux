# Plan: clipboard image paste into a mirror pane (#361)

Spec: `docs/superpowers/specs/2026-09-03-clipboard-image-paste-mirror-design.md`
(mechanism evidence lives there — read it first).

## Constraints

- Sibling work is live: **do not restructure** `daemon/daemon.go`'s
  seed/`wireRenderer` path (#487) or `daemon/router.go`'s `Register` seam
  (#468). Paste logic goes in new files; existing files get minimal additive
  edits only.
- `pumpInput` currently: `func pumpInput(conn net.Conn, remotePane string,
  send func(string))` at `daemon/daemon.go` ~line 1777; call sites at
  `daemon/daemon.go:1056` and `daemon/reconcile.go:315`.
- tmux `-F` formats are `|`-delimited, never tab/newline (repo rule).
- shfmt tabs; `shellcheck` clean for any shell embedded in Go strings is N/A,
  but the remote store script must be POSIX `sh` (remote login shell is fish
  — it never parses the script; follow `graphics/fetch.go`'s
  `sh -c <shQuote(script)> _ args...` pattern exactly).

## Steps

### 1. `picker/remotebridge/daemon/paste.go` (new)

The whole feature, testable with fakes:

- `type pasteHandler struct` with injected seams:
  - `upload func(ctx context.Context, ext string, data []byte) (string, error)`
    (from `Config.PasteUpload`; nil ⇒ handler disabled)
  - `readClipboard func() (data []byte, ext string, ok bool, err error)` —
    prod impl probes `xclip -selection clipboard -t TARGETS -o` /
    `wl-paste -l` for `image/(png|jpeg|jpg|gif|webp|bmp)`, then extracts with
    the matching `xclip -t image/<t> -o` / `wl-paste --type image/<t>`;
    `ok=false` when no image target; error only on tool malfunction. bmp-only
    ⇒ `ok=true, ext="bmp"` and the handler reports unsupported.
  - `procFor func(remotePane string) string` — prod impl: one
    `cfg.LocalTmuxOut("list-panes", "-s", "-t", cfg.LocalSess, "-F",
    "#{@bridge_pane}|#{@bridge_proc}")` fork, map lookup. (`|`-delimited per
    repo rule.)
  - `notify func(msg string)` — prod impl: `list-clients -t <sess> -F
    '#{client_name}'`, then `display-message -c <first> -d 5000 -- <msg>`;
    no-op when no client is attached.
  - `timeout time.Duration` (5s), `maxBytes int64` (8<<20).
- `agentProcs = map[string]bool{"claude": true}` — the gate set (spec:
  codex has no clipboard read; cursor unverified).
- Byte scanner: `splitAroundPaste(b []byte) (forward []byte, hits int)` —
  copies `b` dropping each `0x16` that lies **outside** `ESC[200~`…`ESC[201~`
  bracketed-paste markers; tracks the in-paste state across the scan of one
  payload (state does not need to persist across frames: tmux delivers a
  bracketed paste inside one read in practice, and a split marker is
  pathological — note this in a comment).
- `handle(remotePane string, payload []byte, send func(string)) []byte` —
  the pumpInput hook: returns the bytes to forward. For each hit: if
  `procFor` not in `agentProcs` → keep the byte; else read clipboard; no
  image → keep the byte; image → drop the byte and `go h.paste(remotePane,
  send)`.
- `paste(remotePane string, send func(string))` — size cap check →
  `upload` (ctx with timeout) → validate returned path against
  `^/tmp/lazytmux-paste/img-[A-Za-z0-9]+\.(png|jpe?g|gif|webp)$` → inject
  `path + " "` via `controlmode.SendKeysArgs(remotePane, …,
  controlmode.InputChunkBytes)` (hex — no quoting layers) → every failure
  arm calls `notify` with a specific legible message and sends nothing.

### 2. `picker/remotebridge/daemon/daemon.go` (minimal additive edits)

- `Config` gains:
  ```go
  // PasteUpload ships one clipboard image to the remote over the bridge's
  // ssh ControlMaster and returns the remote path it landed at. nil
  // disables ctrl+v image-paste interception (tests, --test-local).
  PasteUpload func(ctx context.Context, ext string, data []byte) (string, error)
  ```
- `func (c Config) paster() *pasteHandler` — nil when `PasteUpload` is nil,
  else `newPasteHandler(c)` (cheap closure struct, mirrors `graphicsFor`).
- `pumpInput` gains a `*pasteHandler` parameter; its frame loop passes each
  `FrameInput` payload through `h.handle(...)` first (nil handler ⇒ forward
  verbatim). Nothing else in the file changes.

### 3. `picker/remotebridge/daemon/reconcile.go` (one line)

- Call site ~315: `go pumpInput(c, id, send, cfg.paster())`. Same at
  `daemon.go:1056`.

### 4. `picker/remotebridge/cmd/daemon/main.go`

- `remoteStoreScript` const (the POSIX script from the spec: sweep >
  60min, `mktemp` 0600 under `/tmp/lazytmux-paste`, `cat > f`, print path).
- Build `cfg.PasteUpload` only when `ctlSock != ""` (mirrors `NewGraphics`):
  `exec.CommandContext(ctx, *sshCmd, "-S", ctlSock, "-T", *host, "--",
  "sh", "-c", shellQuote(remoteStoreScript), "_", ext)` with
  `cmd.Stdin = bytes.NewReader(data)`; capture stdout, trim, return.
  `ext` is validated against `^(png|jpe?g|gif|webp)$` before use.
- Flag/env for the cap is **not** added; the 8 MiB const lives in
  `daemon/paste.go`.

### 5. Tests

- `picker/remotebridge/daemon/paste_test.go` — table-driven, fakes only:
  - scanner: no `0x16`; bare `0x16`; `0x16` mid-chunk with surrounding
    bytes preserved; `0x16` inside `ESC[200~…ESC[201~` kept; two in one
    payload.
  - gate: shell pane forwards; agent pane with empty clipboard forwards;
    agent pane with image swallows + async paste runs (synchronize with a
    channel in the fake upload).
  - paste: success → send receives hex of `path + " "`; oversize → notify,
    no upload; bmp → notify unsupported; upload error → notify, no send;
    bad returned path → notify, no send; timeout respected.
  - nil handler ⇒ payload forwarded untouched.
- `picker/remotebridge/cmd/daemon/main_test.go` — assert the upload argv
  shape (`-S`, `-T`, `sh -c` quoting, ext position) following the existing
  `sshControlArgs` test pattern, with a fake `exec` seam if one exists
  there already (match the file's conventions; otherwise test the argv
  builder as a pure function).

### 6. Docs

- CLAUDE.md: new short subsection under the bridge docs ("Bridge Image
  Paste"), one paragraph + failure-matrix summary, cross-linking the spec;
  add a row to "What the Remote Host Needs on PATH" (paste needs nothing
  new on the remote — `sh`, `mktemp`, `find`; the *local* host wants
  `wl-paste`/`xclip`).
- Commit spec + plan with the code (repo convention).

### 7. Gate

`nix build .#default` && `nix flake check` && `nix build .#lint` — all
three, looped to green.

## Acceptance check (from the issue)

- [ ] `ctrl+v` with image on local clipboard in a mirror pane ⇒ remote
      agent receives the image (path injected; CC inlines at submit).
- [ ] Failures are visible no-ops with legible messages; pane never freezes
      (async off the input pump).
- [ ] Non-image clipboard / non-agent panes: byte forwarded unchanged.
