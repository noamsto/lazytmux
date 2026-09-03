# Plan: fix picker probe classification for a Tailscale-check remote host (#486)

## Symptom

A remote host running Tailscale SSH (`tailscale set --ssh`) under an ACL rule
with `"action": "check"` rendered in the picker's Remote section as
**unreachable**. TCP connect and ssh auth both succeed; `tailscaled`
intercepts the established session on the remote, writes a check-required
banner (plus a login URL) over stdout, then blocks — so the probe always hit
`remoteProbeTimeout` (3s) rather than exiting 255, and `classifyProbeErr`
discarded stdout on the timeout path before ever looking at it.

## Approach decision

Introduced a **distinct probe state** (`remoteProbeTailscaleCheck` /
`errRemoteTailscaleCheck`) rather than reusing `errRemoteNeedsAuth`, and kept
the row **refuse-to-act** (mirrors `remoteProbeHostKeyChanged`'s shape)
rather than driving an interactive clear-the-check flow through
`tea.ExecProcess`.

- `lztmux-remote-auth`'s remedy (`ssh-copy-id`, a `ControlMaster`) is
  meaningless against a server-side Tailscale ACL check — it re-arms on the
  ACL's `checkPeriod` regardless of keys or multiplexing, so wiring Enter to
  that script would show a broken/no-op flow.
- A bare `ssh <host>` through `tea.ExecProcess` (the mechanism
  `remoteNeedsAuth` uses) would show tailscaled's live banner and let the
  check complete in place, but it re-enters the same blocking wait the
  probe's 3s timeout exists to bound — no timeout once the pty is handed
  over. Turning "Enter" into "now sit and wait on an ssh session that may
  hang indefinitely" is a worse default than telling the user the exact
  command to run themselves, on their own time.
- The remedy text is **always** `run: ssh <host>` — correct regardless of
  whether a login URL was captured, since the captured URL is per-attempt
  and the probe that captured it was already killed by
  `remoteProbeTimeout` before showing it to anyone. The captured URL, when
  present, is shown only as supplementary detail in the preview pane
  (explicitly caveated as possibly stale), never depended on for
  correctness.

**Live verification attempted, inconclusive.** `tailscale status --json`
shows `naspi` with Tailscale SSH enabled (`sshHostKeys` non-null) and `halo`
with it disabled (`sshHostKeys` null — its ACL config apparently changed
since #486 was filed, or key visibility differs per peer). A live probe
against both returned exit 0 immediately: this device has already cleared
whatever check either host's ACL demands, so neither reproduces the blocking
banner right now. The task doc's `halo` transcript (marked "verified, do not
re-derive") is used verbatim as the test fixture in place of a live specimen.

## Steps

- [x] **`classifyProbeErr` threads stdout, detects the banner** (`picker/remote.go`)
  - `tailscaleCheckPattern` / `tailscaleCheckURLPattern` (URL-safe character
    class, length-capped — this is remote-controlled stdout and the
    remote-row preview is not run through `stripStringEscapes`) /
    `detectTailscaleCheck`.
  - `errRemoteTailscaleCheck` sentinel, `remoteProbeTailscaleCheck` enum
    value (appended last).
  - `classifyProbeErr(err error, stdout, stderr string, timedOut bool) error`
    — the `timedOut` branch checks `detectTailscaleCheck(stdout)` before
    falling back to the existing `errRemoteUnreachable` timeout path.
  - Both call sites (`sshListRemoteSessions`, `sshListRestorableSessions`)
    pass `stdout.String()`.
  - `tailscaleCheckURL(err) string` recovers the captured URL from a
    classified error.

- [x] **`remoteSessionsForHost` maps the sentinel** (`picker/remote.go`) —
  `errRemoteTailscaleCheck` → `remoteProbeTailscaleCheck`.

- [x] **`listItem` gets the new fields** (`picker/tui.go`) —
  `remoteTailscaleCheck bool`, `remoteTailscaleURL string`.

- [x] **Wire it up** (`picker/remote.go`, `picker/tui.go`)
  - `collectRemoteItems`: `hostResult.tailscaleURL`, the
    `(tailscale check — run: ssh <host>)` note, and
    `hostRow.remoteTailscaleCheck`/`remoteTailscaleURL`.
  - `activateCurrent`: refuse-to-act branch (same shape as `remoteInert`),
    `statusMsg` names the command to run.
  - `loadPreviewCmd`: matching preview case, captured URL shown as
    supplementary/possibly-stale detail.

- [x] **Unit tests** (`picker/remote_test.go`, `picker/tui_test.go`)
  - `TestClassifyProbeErr`: threaded the new `stdout` argument through every
    case; added the real captured banner+URL transcript, a banner-with-no-URL
    case, an ANSI/control-byte injection case (proves the URL-safe character
    class actually restricts capture), and an explicit positive-control
    regression guard (a plain empty-stdout timeout stays
    `errRemoteUnreachable`).
  - `TestRemoteSessionsForHostNewStates`: added the tailscale-check case.
  - `TestCollectRemoteItemsTailscaleCheck`, `TestActivateTailscaleCheckRowRefuses`
    (mirror the existing needs-auth/host-key-changed tests).

- [x] **Docs** — `README.md` (new row-state paragraph, alongside the existing
  auth-needed/host-key-changed ones) and `CLAUDE.md` (`tmux-session-picker`
  row entry: the new state, and that `^o`/browse still dials into the same
  hang on this row — a pre-existing gap shared with the host-key-changed row,
  not new).

## Verified

- `go build ./...`, `go vet ./...`, `go test ./...` (full `picker/` module,
  every subpackage) — all green.
- `gofmt -l` on every touched file — clean.
- `nix build .#default`, `nix flake check`, `nix build .#lint` — run before
  push (see PR).

## Out of scope

- Driving an interactive `ssh <host>` handshake through `tea.ExecProcess`
  from Enter (considered and rejected above).
- Changing `lztmux-remote-auth` itself.
- Closing the pre-existing `resolveEmitPick`/`^o`-browse refuse-to-act gaps
  shared with `remoteInert` (documented inline, not new regressions).
