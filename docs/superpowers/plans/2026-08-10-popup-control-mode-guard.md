# Plan — guard `display-popup` against control-mode clients (#346)

**Design spec:** `docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md`

**Architecture:** Five shell wrapper scripts that open tmux popups stop letting
tmux re-resolve the popup's target client (`cmd_find_best_client`, newest
`activity_time`, no control-mode exclusion) and instead pin it with
`display-popup -c <client>`, supplied by the bind/hook that invoked them. The
splash gate additionally refuses to open at all for a control-mode client,
without consuming `@splash_shown` — and its hooks stop mangling their session
argument, which revives the splash on attach.

**Tech stack:** bash (`scripts/*.sh`, `writeShellScriptBin`), Nix string
interpolation (`config/tmux.conf.nix`), bats (`tests/*.bats`, run by
`nix flake check`).

**Files touched:**

| file | change |
|---|---|
| `scripts/tmux-splash-maybe.sh` | `$2` = client; control-mode gate; `-c` on both popups |
| `scripts/tmux-session-picker.sh` | `--client` parse; `-c` |
| `scripts/tmux-window-picker.sh` | `--client` parse (before `--claude`); `-c` |
| `scripts/tmux-window-wall.sh` | `--client` parse; `-c` |
| `scripts/tmux-scratchpad.sh` | `--client` parse (after `--attach`, before session); `-c` |
| `config/tmux.conf.nix` | 6 binds gain `--client "#{client_name}"`; 2 hooks gain `#{q:hook_client}`, switch to `#{q:hook_session_name}`, and end in `\|\| true` |
| `tests/splash.bats` | `list-clients` + `set-option` stubs; client arg on every case; new cases |
| `tests/picker-launcher.bats` | `--client` cases incl. a new scratchpad case |
| `CLAUDE.md` | splash Trigger paragraph records the control-mode condition |

**Sequencing:** steps 1-3 are one logical change. Between step 2 and step 3 the
splash is fail-closed dead; that window is fine because they land in a single
commit, but do not stop to test the splash between them.

---

## Step 1: pin the client in the four popup launchers

Add the same leading-option parse to `tmux-session-picker.sh`,
`tmux-window-picker.sh`, `tmux-window-wall.sh` and `tmux-scratchpad.sh`:

```bash
CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2 || shift
fi
```

(`shift 2 || shift` because a bare `--client` with no value would make `shift 2`
return non-zero and `set -e` would kill the script silently. Not reachable from
the binds, but the script should not be a trap for hand invocation.)

Placement is per-script and load-bearing:

- **`tmux-window-picker.sh`** — before the existing `${1:-} == "--claude"` test
  (line 7), so `--claude` still lands on `$1` after the shift.
- **`tmux-scratchpad.sh`** — *after* the `--attach` inner-mode block (lines
  10-21) and *before* `SESSION=${1:-}` (line 24). Inner mode is invoked from
  inside the popup and never carries `--client`; putting the parse first would
  make `prefix + S` compute `scratch---client`.
- **`tmux-session-picker.sh`, `tmux-window-wall.sh`** — at the top, after
  `set -euo pipefail`.

Then, in each, feed it to the existing `display-popup`:

```bash
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E ...
```

`[[ … ]] && …` as a standalone statement is safe under `set -e` only because a
later statement follows it; keep the `tmux display-popup` line after it.
Empty-array expansion under `set -u` is safe: these run under nixpkgs bash 5
(`writeShellScriptBin` bakes `#!…/bash`), and the bats checks invoke them as
`bash "$launcher"` with bats' own bash on PATH.

No control-mode check in the launchers — a control client cannot press a key,
and pinning `-c` makes the question moot.

Each script gets one short comment on the `-c`: without it tmux re-resolves to
the session's most-recently-active client, which on a bridged host can be the
tty-less control client (#346).

**Verify:** step 4's bats cases. Do not try to run these scripts raw — outside
the bats harness there is no fake `tmux` on PATH and `@picker_generate@` is
still an unsubstituted Nix placeholder.

## Step 2: gate the splash on control mode and pin its popups

In `scripts/tmux-splash-maybe.sh`, after the `@splash_shown` early-exit
(line 12) and before the single-window/single-pane checks:

```bash
# The remote bridge attaches a -CC control-mode client. It has no tty, so a
# second popup on it dereferences NULL inside tmux and takes the whole server
# down (#346) — and a mirror has no business showing a splash. Deliberately
# without setting @splash_shown: a later real attach must still get it.
client="${2:-}"
control=""
while read -r mode name; do
	[ "$name" = "$client" ] && control="$mode"
done < <(tmux list-clients -t "$session" -F '#{client_control_mode} #{client_name}' 2>/dev/null)
[ -n "$control" ] || exit 1
[ "$control" = 1 ] && exit 0
```

Write the loop body as `[ "$name" = "$client" ] && control="$mode" || true` —
the `|| true` is not needed for correctness (a failing `[` inside an AND list is
exempt from `set -e`) but it is this repo's idiom for the shape, see
`scripts/claude-status.sh:59`.

Two details that are not cosmetic:

- **Mode first, name second.** `read -r mode name` puts any whitespace
  remainder into `name`, so the exact-name compare still works. The reverse
  order would spill a spaced client name into `mode` and silently classify a
  control client as non-control — fail *open*, the one direction that matters.
- **Not `display-message -c "$client" -p '#{client_control_mode}'`.** That
  command is `CMD_CLIENT_CANFAIL` and falls back to `cmd_find_best_client` when
  the `-c` client does not resolve — measured printing `1` and exiting `0` for a
  nonexistent client, i.e. fail-open with a *different* client's answer.

Then add `-c "$client"` to both `display-popup` invocations (lines 56 and 63),
keeping the existing `-t "$session"`. They are separate edit sites — the
`--static` one is easy to miss.

Update the file header comment (line 4), which still documents
`$1 = target session name (#{hook_session})`: it is `#{hook_session_name}` now,
and `$2` is the invoking client.

**Verify:** step 4's bats cases.

## Step 3: fix, quote and neutralise the hook; supply the client everywhere

### Binds — `config/tmux.conf.nix`

- line 715 `bind S` → `tmux-scratchpad --client "#{client_name}" #{q:session_name}`
  (`#{q:}` on the session name: pre-existing injection on a line this change
  already edits — see hook item 2 below)
- line 746 `bind s` → `tmux-session-picker --client "#{client_name}"`
- line 747 `bind w` → `tmux-window-picker --client "#{client_name}"`
- line 748 `bind a` → `tmux-window-picker --client "#{client_name}" --claude`
- line 749 `bind W` → `tmux-window-wall --client "#{client_name}"`
- line 752 `MouseDown1StatusLeft` → `tmux-session-picker --client "#{client_name}"`

Bind values are tmux-single-quoted with the shell string inside, so plain double
quotes reach `sh -c` — the idiom `bind S` already uses. No `''${` escaping and
no `#`→`##` escaping is needed for any of these.

### Hooks — lines 1054-1055

```
set-hook -g client-attached[50]        'run-shell -b "…/tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true"'
set-hook -g client-session-changed[50] 'run-shell -b "…/tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true"'
```

Three separate changes, each required:

1. **`#{hook_session}` → `#{hook_session_name}`.** The id form expands to `$0`,
   and `run-shell` execs `sh -c` with `argv[0]="sh"`, so the shell re-expands it
   to the literal string `sh` — the hazard `config/tmux.conf.nix:960-961`
   already documents. Measured on a real attach to a session named `sess one`:
   `#{hook_session}` → `$1="sh"`; `\"#{hook_session}\"` → still `sh` (double
   quotes do not stop `$0`); `\"#{hook_session_name}\"` → `sess one`. Today's
   form means the gate has **never** run past its first check, which is why a
   real pty attach to a freshly built wrapper opens nothing.
2. **`#{q:…}` shell-quoting, used bare.** `run-shell` format-expands its whole
   command string before `sh -c` sees it, so `\"…\"` is not protection: a
   session name containing `"` closes the quote and the rest executes. A bridged
   session's name is remote-derived (`lztmux-remote-open.sh:144`), so this is a
   trust-boundary crossing, verified end to end. `#{q:…}` maps to
   `format_quote_shell()` and blocks it.
3. **`|| true`.** Step 2 introduces the script's first non-zero exit. A
   `run-shell -b` job that exits non-zero makes tmux compose `'<cmd>' returned
   1` and — because `-b` leaves `cdata->item` NULL while `wp_id` is the hook's
   pane — push that pane into **view mode**. Measured: `#{pane_mode}` becomes
   `view-mode`. The script keeps its honest non-zero exit (spec R4); the hook
   swallows it so a fail-closed attach never hijacks the user's pane.

Note for the implementer: after this step `nix flake check`'s
`tmux-next38-readiness-tests` starts exercising the gate for real — that suite
attaches `-CC` clients (`tests/tmux-next38-readiness.bats:132,189,360`) and the
hook has been dead until now. A failure there is a gate bug, not a flake. (Those
clients are pty-less control clients, so no splash should open.)

**Verify:**

```bash
nix build .#default
CONF=$(grep -o -- '-f /nix/store/[a-z0-9]*-tmux[.]conf' result/bin/tmux | head -1 | cut -d' ' -f2)
[ "$(grep -c -- '--client "#{client_name}"' "$CONF")" = 6 ]
[ "$(grep -F -c -- 'tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true' "$CONF")" = 2 ]
```

`head -1` and `-tmux[.]conf` mirror `store_conf()` in
`tests/tmux-next38-readiness.bats:69-71`; without them a second match makes
`CONF` multi-line. Each check is wrapped in `[ … ]` so it can actually fail —
a bare `grep -c` prints `1` and exits 0 when only one hook was converted.

## Step 4: bats coverage

**`tests/splash.bats`** — `setup()` gains a second log path next to the existing
`POPUP_LOG` pair, and both must be exported (the stub is a grandchild process):

```bash
SETOPT_LOG="$STUBDIR/setopt.log"
export SETOPT_LOG
```

then two stub case arms:

```sh
list-clients) printf '%s %s\n' "${FAKE_CONTROL:-0}" "${FAKE_CLIENT:-/dev/ttys0}";;
set-option)   echo "$*" >>"$SETOPT_LOG";;
```

`set-option` currently swallows the call (`set-option) ;;`), which means the
existing skip-mode case proves only that the gate does not *read* the flag, not
that it never *wrote* it. Logging it is what makes "does not consume
`@splash_shown`" a real assertion.

Do **not** write the arm as `>>"${SETOPT_LOG:-/dev/null}"`. Without the
`setup()` export the redirection would be `>>""`, which fails, which makes the
stub exit non-zero, which makes `tmux set-option -g @splash_shown 1` fail, which
`set -e` turns into a dead gate — every currently-green positive case goes red.
The `:-/dev/null` "fix" for that would silently make `[ ! -s "$SETOPT_LOG" ]`
unconditionally true, i.e. exactly the false green this case exists to prevent.
`teardown()`'s `rm -rf "$STUBDIR"` already covers cleanup, and `[ ! -s … ]` on a
not-yet-created file is true, so no pre-creation is needed.

Every existing invocation (`run bash "$GATE" s`) gains the client argument
(`run bash "$GATE" s /dev/ttys0`) — the gate now requires one, and in production
the hook always supplies it. New cases:

1. control-mode client opens no popup — `FAKE_CONTROL=1`, assert `$POPUP_LOG`
   empty.
2. control-mode skip does not write `@splash_shown` — same case, assert
   `[ ! -s "$SETOPT_LOG" ]`.
3. control-mode skip does not consume the flag — two-call shape like the
   existing skip-mode case: control-mode call opens nothing, a following
   non-control call opens the splash.
4. unresolvable client fails closed — `run bash "$GATE" s bogus`, assert
   non-zero status and no popup.
5. the popup carries `-c <client>` — `grep -Eq -- '(^| )-c /dev/ttys0( |$)'`.
   Assert this in the existing `mode=static` and `mode=full` cases too: those
   exercise `scripts/tmux-splash-maybe.sh:56`, a different edit site from :63,
   and the acceptance criterion names all three `splash.remote` modes.

**`tests/picker-launcher.bats`** — new cases:

6. session picker with `--client foo` logs `-c foo`; without it logs no `-c`.
   The negative must be anchored: `! grep -Eq -- '(^| )-c ' "$ARGS_LOG"`, or it
   can never fail.
7. window picker with `--client foo --claude` logs `-c foo` **and** still
   reaches `--claude` (the popup command contains `--claude`).
8. **new** scratchpad case: `--client foo sess` logs `-c foo` and still treats
   `sess` as the session. Assert on what actually reaches the popup argv — the
   title (`scratch: sess`) and the trailing `--attach 'sess'` — not on
   `scratch-sess`, which only appears in the unlogged `tmux new-session -d -s`.
   The existing stub already exits 0 on `new-session`/`show`, so it needs no
   change; `BORDER_FG` ends up empty, which is harmless.

**Verify:** `nix flake check`.

## Step 5: docs + the three-command gate

- `CLAUDE.md`, "Welcome Buffer (splash)" → **Trigger** paragraph: the gate also
  skips control-mode clients (bridge `-CC` attaches) without setting
  `@splash_shown`, and the popup is pinned to the attaching client with `-c`.
- Also note in the same section that the hook passes `#{hook_session_name}` —
  the id form is re-expanded by `run-shell`'s shell.

**Verify — all three, none subsumes another:**

```
nix build .#default
nix flake check
nix build .#lint
```

## Step 6: end-to-end proof against the built wrapper

The bats cases drive the gate's logic through a fake `tmux`; these three
scratch-socket runs are what prove the real thing. Manual, not part of
`nix flake check` — they need a live control-mode client and a real pty, which
the sandbox cannot provide. Record the results in the PR body.

Common preamble for each (`W=./result/bin/tmux`, unique `-L` socket, `HOME` set
to a fresh mktemp dir, `unset TMUX`, and a PATH shim so bare `tmux` inside
`run-shell` is the wrapper on that socket).

1. **The splash comes back, and only for a real client.**
   `new-session -d -s s`; attach a real pty (`script -q /dev/null $W -L $SOCK
   attach-session -t s &`); assert `@splash_shown` becomes `1`. Repeat on a
   fresh socket with a `-CC` client instead (`$W -L $SOCK -C attach-session -t s`
   from a coproc): assert no popup, `@splash_shown` still empty, and
   `#{pane_mode}` empty (no view-mode hijack).
2. **The splash double-fire no longer kills a control-only server.** With only a
   `-CC` client attached, run the gate twice concurrently
   (`"$GATE" s <ctl-client> & "$GATE" s <ctl-client> & wait`). This killed the
   server 3/3 before the change; assert `has-session -t s` still succeeds.
3. **A pinned launcher survives a control client winning best-client.** Attach a
   pty, then a `-CC` client (so the control client has the newer
   `activity_time`), then run `tmux-session-picker --client <pty-client>` twice.
   Untargeted, this killed the server; pinned, assert the server is alive.

## Step 7: commit

Commit the code, `docs/superpowers/plans/2026-08-10-popup-control-mode-guard.md`
and `docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md`
together — `CLAUDE.md` → "Plans and Specs" requires the plan and design spec to
ship in the same PR as the code.
