# Plan: validate remote_tmpdir at entry + fish-safe quoting (#365)

## Goal

Land the three post-merge review findings from PR #363: entry-point validation for
`remote_tmpdir`, fish-safe remote quoting for values that cross the login shell, and
the capability-refusal `--serve` assertion gap.

## Mechanism (one sentence)

Hoist the existing anchored charset check into `lib-remote.sh` and enforce it in
`lztmux-remote-open` right after `remote_tmpdir` is resolved; make `shell_quote` in
that same public entry point fish-safe (escape `\` then `'`) so `$sess` /
`LZTMUX_REMOTE_NEW_DIR` survive the fish login shell; add the missing `--serve`
grep on the capability-refusal bats case.

## Why not full argv rewrite for finding 2

`ssh host "bash -s -- $(shell_quote …)"` still crosses the **fish** login shell for
the quoted argv tokens — dialect tracking moves to `shell_quote`, it is not removed.
Charset-validated paths (picker collect) already make POSIX quoting safe; `NEW_DIR`
is not charset-gated. Fish-safe `shell_quote` is the fix that actually closes the
measured failure. Keep the existing `ssh "env TMUX_TMPDIR=… $remote_tmux …"` shape
so cold-start fakes/`SSH_LOG` assertions stay honest.

`$sess` is fixed in the same pass: it uses the same helper on the same fish path
(lines 116/134/153/161/166/177); leaving it would keep the twin of the bug the
review named.

## File list (allowed)

| File | Change |
|------|--------|
| `scripts/lib-remote.sh` | Add `valid_remote_path` (same regex as today). |
| `scripts/lztmux-remote-open.sh` | After resolving `remote_tmpdir`, reject via `valid_remote_path`; fish-safe `shell_quote`; fix POSIX→fish comment. |
| `scripts/lztmux-remote-picker.sh` | Keep local `valid_remote_path` (same regex) — picker is **not** on `scriptsWithRemote`, and `config/tmux.conf.nix` is out of scope (#367). |
| `picker/remotepick.go` | Comment-only: consumer is fish (pane shell), not “POSIX shell”. |
| `tests/remote.bats` | Unit-test `valid_remote_path` accept/reject cases. |
| `tests/remote-cold-start.bats` | Reject bad `LZTMUX_REMOTE_TMPDIR` before remote work; fish-backslash `NEW_DIR` round-trip via local `fish -c`. |
| `tests/remote-picker.bats` | Capability-refusal: `grep -c -- --serve "$SSH_LOG"` must fail (leg 2 never ran). |

## Out of scope (do not touch)

`config/tmux.conf.nix`, `picker/remotebridge/daemon/windows.go`,
`picker/remotebridge/daemon/ctl.go`, `picker/remotebridge/cmd/daemon/main.go`,
`scripts/tmux-update-icons.sh`, `scripts/lib-claude.sh`,
`tests/update-icons-resume-guard.bats`.

## Steps

1. **lib-remote: hoist `valid_remote_path`** — same body as picker:
   `[[ $1 =~ ^/[A-Za-z0-9._/@+:-]*$ ]]`. Do not redesign the regex.
2. **open: validate `remote_tmpdir`** immediately after assignment (~:56); fail loudly
   with `lztmux-remote-open: …` on stderr, exit 1.
3. **open: fish-safe `shell_quote`** — escape `\` → `\\`, then `'` → `'\''`, wrap in
   `'…'`. Update the comment (fish login shell, not POSIX). Fix `$sess` via this
   helper (same pass).
4. **remotepick.go** — comment only (fish pane shell).
5. **Tests (must fail before the matching fix):**
   - `valid_remote_path` rejects whitespace/`$(…)`/relative; accepts `/run/user/1000`.
   - open with `LZTMUX_REMOTE_TMPDIR='/run/user/1000 x'` exits 1, empty `SSH_LOG`
     beyond any pre-validation traffic (ideally before uname if we validate env when
     set — minimum: after :56 as specified; cold-start already exports a good
     tmpdir for other cases).
   - fish check: `shell_quote` output through `fish -c "printf '%s\\n' $quoted"`
     preserves `/srv/a\\b` and `/srv/a\` (or documents parse-error fail-closed).
   - capability-refusal: assert no `--serve` in `SSH_LOG`.
6. **Gate:** `shellcheck` on touched scripts; `nix build .#default`; scoped bats;
   `nix flake check`; `nix build .#lint`.

## Acceptance

- [ ] Bad `LZTMUX_REMOTE_TMPDIR` is rejected inside `lztmux-remote-open`, not only by the picker caller.
- [ ] A `NEW_DIR` containing `\` is not silently corrupted by fish single-quote rules; `$sess` uses the same helper.
- [ ] Comments no longer claim POSIX-only consumers for the open/remotepick twins.
- [ ] Capability-refusal test fails if leg 2 (`--serve`) runs.
- [ ] Local gate green; PR closes #365.
