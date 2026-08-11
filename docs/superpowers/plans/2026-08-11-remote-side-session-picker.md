# Remote-side session picker (#356) — implementation plan

Spec: `docs/superpowers/specs/2026-08-11-remote-side-session-picker-design.md`
Issue: #356

## Deviations from the given decomposition

Recorded here because each is deliberate, not drift.

1. **`launcher-newdir` is a new component.** The decomposition declares
   `scripts/lztmux-remote-open.sh` off-limits and states "the picked session is
   live on the remote by construction." That holds only if the *remote picker*
   creates a dir-pick's session — the design the spec's critic refuted twice: the
   session would have to survive the whole interactive leg plus three more round
   trips (spec D2), and a second socket resolver could create it where the bridge
   never looks. Creation therefore moves into the launcher, which already owns
   remote pre-bridge mutation via `LZTMUX_REMOTE_RESTORE`. The launcher gets its
   own component with tight boundaries.
2. **One dual-role script**, per the decomposition, *not* the spec's two. Both
   designs are equally sound as capability markers — the spec's `lztmux-pick-session`
   was also new and also execs `@picker_generate@` — so the win is narrower than
   "airtight vs conventional": it is one fewer `config/tmux.conf.nix` hunk, in the
   file flagged as shared with #355, plus one fewer artifact to keep in step.
3. **Bare `--remote-pick` flag + `LZTMUX_PICKER_EMIT` env**, per the
   decomposition, not the spec's valued `--emit <path>`. `main()` parses args into
   `map[string]bool` (`picker/main.go:105-110`); keeping every flag value-less
   means the parser is untouched for all three entry points, which is the smaller
   blast radius. Adopted over the spec.
4. **`^o`, not `^r`** (spec). Both are free; `^r` reads as refresh/reverse-search.
5. **`home.packages` is option-gated** (`remote.exposePickOnPath`, default true),
   where DECOMPOSITION §5 demands it be unconditional. Every entry there but
   `tmux-wrapped` is option-gated (`modules/home-manager.nix:962-981`), so an
   unconditional one would break the file's pattern. The decomposition's *reason*
   still holds and is kept: it is **not** gated on `remote.hosts`, which is set on
   the local host while the script is needed on the remote.
6. **One guard is added at the head of the existing `ctrl+x` case**
   (`picker/tui.go:448-476`), which DECOMPOSITION:15-16 bans outright. The ban
   leaves that edit with **no legal owner** — `picker-launch-key`'s boundary is
   "one *new* case" — while spec D8 requires it: inherited unchanged, `^x` gives
   emit mode a remote-`kill-session` and remote-`zoxideForget` capability #356
   never asked for. Scope is exactly one early return in emit mode; no other
   existing case is touched.
7. **Three smaller shape deviations**, each already justified at its step but
   listed here so the conformance check is mechanical: the typed `key=value`
   payload replaces §1's single-line `<session-name>` (forced by deviation #1 /
   spec D2); `--serve <token>` replaces §3's `--serve <emit-path>` (token
   validated `^[A-Za-z0-9]+$`, so it cannot escape the path join); and
   `@remote_pick_bin` replaces §4's bare name (#336, Step 20).

Also adopted from the decomposition: **an error message holds the floating pane
open for one keypress.** The spec argued only that pane output at exit is
unobservable and reached for `display-message`; a keypress hold is strictly better
because it keeps the message where the user is already looking. Both are used —
`display-message` for the status line, the hold so the pane does not vanish.

## Ordering

```
remote-pick-mode ∥ remote-picker-wrapper ∥ launcher-newdir
        → picker-launch-key ∥ nix-wiring → docs
```

---

## Component: launcher-newdir

Boundaries — MAY touch `scripts/lztmux-remote-open.sh` and `tests/remote-cold-start.bats`
(or a new bats file). MUST NOT touch the daemon, `lib-remote.sh`, or any picker file.

- [ ] **Step 1: red test for the empty-`win` blank mirror.** Add a case to
  `tests/remote-cold-start.bats` (already wired into `flake.nix`'s `remote-tests`)
  driving `lztmux-remote-open <host> <nonexistent-sess>`.

  The `*list-windows*` arm at `:72` lives in the **shared `setup()`** fake
  (`:10-111`). Editing it unconditionally would route every success-path case
  (`:124-139`, `:152-161`, `:163-178`, `:180-196`, `:286-293`, `:295-312`,
  `:326-336`, `:338-354`, `:395-404`, …) into Step 2's new guard and turn
  `nix flake check`'s `remote-tests` red across the file. Gate it the way this file
  already gates its other fakes (`FAKE_UNIT_MISSING`, `FAKE_SESSION_GONE`,
  `RESTORE_TARGET_MISMATCH`):
  `*list-windows*) [ -n "${FAKE_NO_WINDOW:-}" ] || echo 1 ;;`, and set
  `FAKE_NO_WINDOW=1` only in the new case.

  The gated arm returns **empty output and exit 0**, mirroring the real remote
  pipeline; a stub exiting non-zero would go red at the `win=` assignment via
  `set -e`, and Step 2's guard would not be what turns it green. Assert it exits
  non-zero with a legible message; today it exits 0 into a daemon launch with
  `LZTMUX_BRIDGE_WINDOW=""`.

- [ ] **Step 2: make an empty `win` fatal.** Guard the `:137-142` result. Keep the
  message shape of `:131`. This is the guard that stops a blank mirror on *every*
  path, including the pre-existing typo'd-session-name one.

- [ ] **Step 3: red tests for `LZTMUX_REMOTE_NEW_DIR`.** Cases: (a) session absent
  and no server → cold-starts via `start_remote_server` *then* creates; (b)
  session absent, server live → creates without cold-starting; (c) session already
  exists → creates nothing, bridges as-is; (d) `new-session` succeeds but
  `has-session` still fails → fatal; (e) set together with
  `LZTMUX_REMOTE_RESTORE` → rejected.

- [ ] **Step 4: implement the `LZTMUX_REMOTE_NEW_DIR` branch.** Structurally mirror
  the restore branch (`:105-135`), placed after it: `has-session` → cold-start if
  `first_remote_session` is empty → `new-session -d -s <sess> -c <dir>` →
  post-create `has-session` or fatal. Every remote-crossing value `shell_quote`d
  (`:14`). Reject the RESTORE combination up front — the launcher is a public
  entry point, so this cannot be left to callers.

---

## Component: remote-pick-mode

Boundaries — MAY touch `picker/main.go` (`main()` only, one bare flag through to
`runTUI`), `picker/tui.go` (two model fields; `newPickerModel`/`runTUI` plumbing;
`Init`'s remote gating; the `zoxideMsg` case and `recombine` for Step 9's guard;
an emit branch in `activateCurrent`; **one emit-mode early return at the head of
the existing `ctrl+x` case** — see deviation #6), and new `picker/remotepick.go` +
`picker/remotepick_test.go`. MUST NOT touch `picker/render_wall.go`,
`picker/render_list.go`, window-mode item builders, `createAndSwitch`, any *other*
existing `handleKey` case, or any existing flag's semantics.

- [ ] **Step 5: emit payload builder, test-first.** The picker only ever *writes*
  the payload — both readers (leg 1's `--probe` output and leg 3's payload) live in
  the bash wrapper, so the parser belongs there (Step 11a), not here. This step is
  the builder only: `kind=session` with `name=`, and `kind=dir` with `path=` +
  `name=`. Round-trip a spaced path and a spaced name (`/home/x/My Docs` →
  `My Docs`) — the case that killed the earlier positional format. Emit-side
  validation is transport-only: non-empty values, no NUL, length-bounded. **No
  charset filter** on the session name; tmux names are near-arbitrary and today's
  Remote section already bridges them as argv (`picker/remote.go:307-312`).

- [ ] **Step 6: emit-mode target resolution, test-first.** Pure function: a
  `listItem` → payload. A session row yields `kind=session`; a zoxide row
  (`createPath`/`createName`) yields `kind=dir`. Assert it creates nothing — the
  selection path must be side-effect-free, not merely `switch-client`-free, or the
  two-creators defect walks back in through `createAndSwitch`
  (`picker/zoxide.go:247-257`).

- [ ] **Step 7: wire the mode into the model.** `--remote-pick` (bare) through
  `main()` → `runTUI` → `newPickerModel`; read `LZTMUX_PICKER_EMIT`. This changes
  `newPickerModel`'s signature (`picker/tui.go:184`) — its call sites are
  `runTUI` (`:223`) and `picker/tui_test.go:614`, both updated in this step.
  Reject `--remote-pick` together with `--windows`/`--wall`: with a bare flag
  nothing otherwise stops `--remote-pick --windows` from emitting a *window*
  target as a session name. Gate `pendingRemoteItems` (`:197-202`) and `Init`'s
  `remoteCmd` (`:239`) off in this mode — no remote-of-remote. Disable `^x` with
  one early return at the head of the case (`:448-476`) — it must neuter **both**
  branches, the `zoxideForget` one (`:453-459`) and the kill one (`:461-475`):
  inherited, they would `kill-session` on the *remote* server and `zoxideForget`
  the *remote* db. See deviation #6 for why this edit lives here.

- [ ] **Step 8: the emit branch in `activateCurrent`** (`:1077-1099`), writing
  0600. A failed write must **not** `os.Exit` from inside `Update` — that leaves
  the remote ssh pty in altscreen/raw mode. Propagate the error through the model
  to `runTUI`'s error return and let `main`'s existing `os.Exit(1)`
  (`picker/main.go:110-113`) handle it, after bubbletea has restored the terminal.

- [ ] **Step 9: the both-empty guard.** With no remote server *and* no `zoxide`,
  `collectSessions` and `collectZoxide` both return nil
  (`picker/zoxide.go:216-224`) — a blank box, which Required property 4 forbids.
  It **cannot** be computed at item-build time: zoxide rows arrive asynchronously
  via `zoxideMsg` (`picker/tui.go:141`, `:309-311`) and merge in `recombine`
  (`:1435-1439`), so a synchronous check would flash the row at first paint on
  every host. It lives in `recombine`, conditioned on the zoxide probe having
  *returned*. Test both the pre-return and both-empty states.

- [ ] **Step 10: model-level tests.** `--remote-pick` builds no Remote section;
  `^x` is inert; cancel writes nothing; and — AC2's headline case — **sessions
  empty but zoxide non-empty** yields selectable zoxide rows whose payload is
  `kind=dir`. Footer assertions belong to Step 18, which owns `render_list.go`.

---

## Component: remote-picker-wrapper (implement: escalated)

Escalated because this is the one component where untrusted remote-derived values
cross into shell command strings over ssh, against a fish login shell — the
security-sensitive seam, not merely a fiddly one.

Boundaries — MAY touch only the new `scripts/lztmux-remote-picker.sh` and a new
bats file. MUST NOT touch `lztmux-remote-open.sh` (consumed via its CLI + the env
contract `launcher-newdir` adds) or `lib-remote.sh`.

- [ ] **Step 11a: the bash `key=value` parser + leg-1 value validation,
  test-first.** This is the parser that actually runs: both readers are here, not
  in Go. A `kv_get <key>` helper over captured stdout. bats cases: order
  independence, unknown keys ignored, **lines with no `=` skipped** (the remote
  login shell is fish, `ssh host bash -s` is still `fish -c 'bash -s'` and sources
  `config.fish`, so a greeting lands on leg 1's stdout), values containing `=`,
  tabs and spaces, and a missing required key rejected.
  Then validate the received values — the check that escalated this component:
  `script` and `emit_dir` must be absolute paths; `tmpdir` must be an absolute
  path with **no whitespace and no shell metacharacters**, because
  `LZTMUX_REMOTE_TMPDIR` is interpolated *unquoted* into remote command strings
  (`lztmux-remote-open.sh:56`, `:107`, `:120`, `:125`, `:141`). A malformed value
  is a legible refusal, not an unreadable downstream failure.

- [ ] **Step 11b: the remote role (`--probe`, `--serve <token>`), test-first.**
  `--probe` prints `script=`/`emit_dir=`/`tmpdir=` and **mutates nothing** — assert
  no directory appears. `--serve` validates the token against `^[A-Za-z0-9]+$`
  (so it cannot escape the path join), `mkdir -p` the emit dir then **asserts uid
  and mode**, exit 4 if unusable (`mkdir -m 700 -p` neither applies the mode to an
  existing directory nor checks ownership), prunes entries older than 60 minutes,
  pre-creates the emit file and `test -w`s it, then execs:

  ```
  exec env TMUX_TMPDIR=<tmpdir> LZTMUX_PICKER_EMIT="$emit_dir/$token" \
       PATH=@zoxide@/bin:<dirname tmux>:$PATH \
       @picker_generate@ --tui --remote-pick
  ```

  `LZTMUX_PICKER_EMIT` is the whole point of deviation #3 and is the only thing
  that gives the picker an emit target — assert the exec line carries it, or the
  selection never crosses back and AC2/AC3 both fail silently.

- [ ] **Step 12: the local role's three legs, test-first with `ssh` stubbed.** Leg
  1 probe (`bash -s` heredoc, `timeout 8`, `BatchMode=yes ConnectTimeout=2 -T`);
  **leg 1's heredoc owns exit 3** — it is what runs `[ -x <script> ]` and reports
  "too old" — and on success it invokes `<script> --probe`, which is the only
  producer of the `script=`/`emit_dir=`/`tmpdir=` lines Step 11a parses. Leg 2
  interactive: `ssh -t <host> -- <script> --serve <token>` (bare
  argv, no `var=value`, so fish parses it); the emit path leg 3 reads is
  `emit_dir` (from leg 1) joined with the token. Leg 3 collect. Assert the per-leg
  taxonomy: timeout, 255 unreachable, 3 too-old, 4 emit-dir unusable, other → last
  non-empty stderr line. Leg 2 has status only — its stderr is merged into the pty.

- [ ] **Step 13: cancel vs. choice vs. failure.** The file is pre-created, so
  **non-empty content** is the discriminator, not existence: status 0 + non-empty
  = choice; 0 + empty-or-absent = cancel (exit 0, silent); non-zero = error. Test
  all three.

- [ ] **Step 14: handoff, test-first.** This is the local half of AC2, so assert
  the child's argv and env with `lztmux-remote-open` stubbed: `kind=session` →
  `<host> <name>` with no `LZTMUX_REMOTE_NEW_DIR`; `kind=dir` → the same plus
  `LZTMUX_REMOTE_NEW_DIR=<path>`. An **unrecognised `kind` is a legible refusal**,
  never a fall-through to a bare `<host> <name>` bridge. AC3's "focuses the
  existing mirror rather than erroring" needs no code here — it is delivered by
  `lztmux-remote-open.sh:165-170` and already covered by
  `tests/remote-cold-start.bats:163`; name that test so the criterion has a
  verification rather than an implication.

  Non-empty payload → run `@remote_open@` as a **child, not `exec`** (its two new
  fatals would otherwise print into a pane destroyed microseconds later),
  forwarding `LZTMUX_REMOTE_TMPDIR` and, for `kind=dir`,
  `LZTMUX_REMOTE_NEW_DIR`. Capture stderr, surface the last non-empty line via
  `tmux display-message`, and hold the pane for one keypress on error. Print a
  progress line before handing off — the launcher makes 3+ round trips before
  `switch-client`. `@remote_open@` is a store path with the
  `[[ $x == @* ]]` bats fallback (`lztmux-remote-open.sh:153-160`). No `disown`,
  no backgrounding: killing the pane SIGHUPs the chain, so no ssh is orphaned.

- [ ] **Step 15: `shellcheck` both roles.** Required by house rules; `shfmt` uses
  tabs here.

---

## Component: picker-launch-key

Boundaries — MAY touch `picker/tui.go` (`handleKey`: one new case),
`picker/render_list.go` (`renderHints` — **sole owner**, including emit mode's
footer, see Step 18), `picker/remotepick.go`, and their tests.
MUST NOT touch `handleWallKey`/`handleFocusedKey`/`handleWallQueryKey`, window-mode
code, `activateCurrent`'s existing branches, or `scripts/tmux-session-picker.sh`.

- [ ] **Step 16: gate parse + `new-pane` argv, test-first.** Pure functions. The
  gate reads `#{&&:#{@bridge_win},#{@bridge_pane}}` → `"1"`/`"0"`/empty. The argv
  is the house float form `-x 90% -y 85% -X 5% -Y 8% -B heavy` plus
  `set -p @pane_label`, with `;` as its own argv token, and the wrapper reached
  from `@remote_pick_bin` (empty → a legible "reload tmux" refusal, never a pane
  that dies with "command not found"). tmux shell-parses the command string, so
  single-quote the host with embedded-quote escaping — hosts come from a
  whitespace-split option (`picker/remote.go:63-78`) so a space is impossible
  today, but the escaping is one line and the quoting rule is the house one.

- [ ] **Step 17: the `^o` case.** Active only when `item.remoteHost != ""`, so it
  is structurally inert in `prefix + w`/`prefix + W`. Gated → set `statusMsg` and
  stay open. Otherwise issue `new-pane` then `tea.Quit` — the `activateCurrent`
  precedent (`:1082-1098`), verified on the pinned next-3.8: the float is created,
  is `FLOAT`, and is active once the popup closes.

- [ ] **Step 18: the hint line — both changes, since this step owns
  `render_list.go`.**
  1. Append `^o:remote` only while the cursor is on a remote row, following the
     conditional-append shape of the window-mode `^g` block
     (`picker/render_list.go:113-119`).
  2. **Emit-mode footer**: `enter:open` (`:108`) becomes `enter:pick`, and the
     hard-coded `^x` hint (`:109`) is dropped. Step 7 disables `^x`'s *behavior*
     but cannot touch this file, and a footer still advertising `^x:kill` is
     exactly what spec D8 forbids — the two must land together, in this PR, and
     this is the step that closes it.

  Test all states: remote row vs not, emit mode vs not.

---

## Component: nix-wiring

Boundaries — MAY touch `config/tmux.conf.nix` (`scriptNames`; one new dispatch
branch; one `set -g` line), `modules/home-manager.nix` (`home.packages` + one
option), `flake.nix` (one `checks` entry). MUST NOT touch any keybind block,
`bridgeGate`/`bridgeCtl`, the `prefix + s` bind, or hooks — #355 is editing
`run-shell` hooks in this file.

- [ ] **Step 19: build the script.** `scriptNames` += `lztmux-remote-picker`, plus
  its **own** dispatch branch substituting `@picker_generate@`, `@remote_open@`,
  `@zoxide@`. Not `scriptsWithIcons`: the dispatch reaches `mkScriptIcons` only for
  `tmux-update-icons` (`:514-515`) and routes `scriptsWithIcons` members to
  `mkScriptFull` (`:516-517`), so placeholders added to the wrong list stay
  literal. A script-specific builder is also what keeps this script from
  referencing its own store path — `@reflow@`'s reason for living where it does.

- [ ] **Step 20: `set -g @remote_pick_bin '<store path>'`.** An option repoints on
  a config reload alone; the tmux server's PATH is frozen until restart (#336).

- [ ] **Step 21: install on the remote's PATH.** `home.packages` +=
  the script, behind a `remote.exposePickOnPath` toggle (default true) — every
  other non-core entry there is option-gated (`modules/home-manager.nix:962-981`).
  **Not** gated on `remote.hosts`: that is set on the *local* host, while the
  script is needed on the *remote*, which typically sets nothing.

- [ ] **Step 22: `flake.nix`** — the launcher cases go in
  `tests/remote-cold-start.bats`, already wired at `:583`, so this is exactly one
  added `bats tests/<wrapper>.bats` line in the existing `remote-tests` check
  (`:574-585`). No new `checks` entry.

---

## Component: docs

- [ ] **Step 23: `CLAUDE.md`.** Update the `tmux-session-picker` row (`^o`) and the
  `lztmux-remote-open` row (`LZTMUX_REMOTE_NEW_DIR`); add a row for
  `lztmux-remote-picker`. State which binaries the *remote* needs on PATH, sited
  next to the Bridge Graphics precedent.

- [ ] **Step 24: `README.md`** "Remote tmux bridge": the `^o` escape hatch, the
  remote-host requirement, the lingering precondition for a dir pick, and both
  honest limitations (restore rows traded for zoxide dirs; `scratch-*` needs `^s`).

- [ ] **Step 25: commit the plan + spec** under `docs/superpowers/`, and
  `gtrash put` `DECOMPOSITION.md` / `WORKER_TASK.md` (scratch, not deliverables —
  and they are untracked, so `rm` would be unrecoverable).

---

## Gate

All three, none subsumes another:

```
nix build .#default
nix flake check
nix build .#lint
```

`nix build .#lint` is the one that fails CI on `alejandra`. Commit from inside
`nix develop` so the pre-commit hooks actually run.

## Not verifiable here

The two-host end-to-end flow needs real machines (`g5` ↔ `tp-g6`). The PR body
must list exactly what was proved (unit tests, bats, single-host degradation, the
sequencing probe) and what still needs a human on two machines. No live-remote
test enters the default `nix flake check`.
