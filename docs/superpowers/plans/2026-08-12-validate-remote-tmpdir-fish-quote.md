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

- [x] Bad `LZTMUX_REMOTE_TMPDIR` is rejected inside `lztmux-remote-open`, not only by the picker caller.
- [x] A `NEW_DIR` containing `\` is not silently corrupted by fish single-quote rules; `$sess` uses the same helper. (Superseded below — this landed as rejection, not fish-safe quoting.)
- [x] Comments no longer claim POSIX-only consumers for the open/remotepick twins.
- [x] Capability-refusal test fails if leg 2 (`--serve`) runs.
- [x] Local gate green; PR closes #365.

## Addendum: finding 2's fish-fix regressed POSIX remotes — reopened and fixed by rejection, not quoting

Step 3 above shipped fish-safe `shell_quote` (escape `\` then `'`). That closed the
fish failure but opened a POSIX one: `'/srv/a\\'` (the doubled form) decodes to the
*wrong but valid* `/srv/a\\` under sh/bash, not the correct `/srv/a\`. There is no
single-quoted form correct under both dialects for a literal backslash — the two
disagree on what `\` means inside `'…'`. Quoting was the wrong tool for this
character; the fix is to reject it instead, which the "define errors out of
existence" house principle already prescribes.

**What changed:**

- `scripts/lib-remote.sh` gained `shell_quotable()` — `[[ $1 != *\\* ]]` — next to
  `valid_remote_path`, with a comment stating the fish/POSIX disagreement and why
  reject-not-quote.
- `scripts/lztmux-remote-open.sh`: `LZTMUX_REMOTE_NEW_DIR` is screened through
  `shell_quotable` right after the existing mutual-exclusivity check (before any
  ssh round trip); `$sess` is screened once resolved (after the cold-start block,
  before the restore branch's first `shell_quote "$sess"` call — it can't be
  checked any earlier, since `$sess` may come from the remote's own
  `first_remote_session()` and isn't known before that point). Both fail loudly
  with a message naming the offending value and exit 1.
- `shell_quote` itself reverted to plain POSIX single-quote escaping (no more
  backslash-doubling) — safe again in every dialect now that backslash can never
  reach it.
- A third consumer confirmed transitively protected, no edit needed: `lztmux-remote-open.sh`
  exports `LZTMUX_BRIDGE_SESSION="$sess"` downstream of the new guard, and both
  `picker/remotebridge/cmd/daemon/main.go` and `picker/remotebridge/main.go` quote
  that env var with their own `shellQuote` — since it can no longer carry a
  backslash by the time either sees it, neither needed changing (both are out of
  scope for this fix regardless, per #368/PR #376).

**Why not the stdin/heredoc alternative** (routing values through `ssh host bash
-s` and reading them via `read` from a stdin data line, which genuinely bypasses
the remote login shell's parsing): `$sess` is interpolated at five call sites
across the script's existing control flow, each a single `ssh host "…"` string
built inline with other client-resolved variables. Converting all five to
stdin-fed heredocs would restructure every call site, duplicate the read-preamble
five times, and rewrite the `SC2029`/`SC2016` annotations and every `SSH_LOG` grep
assertion that pattern-matches today's command strings — a rewrite disproportionate
to a bug this narrow: `remote_tmpdir` already excludes `\` by charset (finding 1),
so only `$sess` and `LZTMUX_REMOTE_NEW_DIR` are exposed, and a backslash in either
is rare. This is a correctness/robustness fix, not a live security hole — revisit
the heredoc approach only if a future value legitimately needs to carry `\`.

**Exposure, precisely stated:** on a POSIX-shell (sh/bash) remote, a backslash in
`LZTMUX_REMOTE_NEW_DIR` or `$sess` was silently mangled into a different,
valid-looking path — corruption, not injection. On a **fish** remote, though, it
was more than that: `\` immediately followed by `'` breaks fish's single-quote
balance, so a crafted value (e.g. a session name ending `x\'; touch
/tmp/PWNED #`) could inject an arbitrary trailing command into the remote shell —
a real command-injection primitive, confirmed by PoC during review, not merely
theoretical. This fix closes the whole class outright (reject `\`, don't try to
quote it), so the distinction doesn't change what to do — but the practical
exposure stays narrow because the precondition is steep: `$sess`/`LZTMUX_REMOTE_NEW_DIR`
are influenced by (a) the local user's own arguments, (b) the remote's own most-recent
tmux session name, or (c) a zoxide-visible directory name — exploiting it requires
already controlling one of those on a host the user is bridging to, at which point
simpler attacks are usually available. The rejection is deliberately narrower than
`valid_remote_path`'s charset — it excludes only `\`, not spaces or punctuation —
so a legitimate zoxide directory with spaces still opens.

**Known follow-up, not fixed here (out of scope):** `picker/remotebridge/main.go`
and `picker/remotebridge/cmd/daemon/main.go` trust `LZTMUX_BRIDGE_SESSION` is
already backslash-free by the time they quote it — true today (its one producer,
`lztmux-remote-open.sh`, is guarded), but there's no independent `shell_quotable`-
equivalent check at their point of use. Latent, not live: revisit only if a future
caller sets that env var directly, bypassing the launcher.

**Tests:** `tests/remote.bats` gained a `shell_quotable` unit test; the old
"shell_quote preserves backslashes under fish" test (a guarantee this fix
deliberately drops) was replaced in `tests/remote-cold-start.bats` with a test
proving `shell_quote`'s POSIX/fish single-quote correctness plus its
backslash-inertness, and three new integration tests proving `$sess` /
`LZTMUX_REMOTE_NEW_DIR` are rejected before the call site that would have quoted
them (`list-windows`, `has-session`, or no round trip at all) ever runs.
