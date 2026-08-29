# Plan: mark-seen never clears unseen (#369)

## Root cause (from issue)

Top-level arg loop in `scripts/claude-status-update.sh` has a silent `*) shift ;;`
catch-all and no `--window` case. `--window <idx>` is eaten before the
`mark-seen` block’s second loop runs, so `win_target` stays empty and the guard
exits 0 without clearing `unseen`.

## Approach choice

**Add `--window` to the top-level loop, drop the redundant second loop, and make
unrecognised `--*` flags fail loudly.**

| Option | Fixes instance | Closes class |
|--------|----------------|--------------|
| Save original `$@` for mark-seen | yes | no — next post-drain flag still silent |
| Add `--window` only | yes | no — silent catch-all remains |
| Add `--window` + loud unknown `--*` | yes | yes |

Other early-exit subcommands (`issue` / `task` / `name` / `enrich`) parse and
exit **before** the top-level loop, so they are not victims today. Only
`mark-seen` had a second loop after the drain. Loud failure prevents the next
subcommand-scoped flag from failing silently the same way.

## Files

- `scripts/claude-status-update.sh` — parse + catch-all + collapse mark-seen loop
- `tests/mark-seen.bats` — regression that fails before the fix
- `flake.nix` — register the new bats check (same pattern as `claude-issues-tests`)

Do **not** touch: `scripts/tmux-update-icons.sh`, `scripts/lib-claude.sh`,
`tests/update-icons-resume-guard.bats`, `picker/**`, `config/tmux.conf.nix`.

## Steps

1. Near the other globals (`session_name=""`, …), initialize `win_target=""`.
2. In the top-level loop (`~348`), add:
   ```sh
   --window)
       win_target="$2"
       shift 2
       ;;
   ```
3. Replace the silent catch-all with a loud reject for unknown flags:
   ```sh
   --*)
       echo "Error: Unknown option '$1'" >&2
       exit 1
       ;;
   *)
       shift
       ;;
   ```
   (Keep bare non-flag tokens as a quiet shift only if something still relies on
   it; prefer erroring on bare unknowns too if nothing in-tree passes them — verify
   with a quick rg of callers. Default: error on `--*`, keep `*) shift` for
   positional leftovers only if callers need it; otherwise error both.)
4. In the `mark-seen` block: remove the inner `while`/`case` that re-parses
   `--session`/`--window`. Keep the
   `[[ -n $session_name && -n $win_target ]] || exit 0` guard and the pane-file
   rewrite logic unchanged.
5. Add `tests/mark-seen.bats` (load helper, hermetic `CLAUDE_STATUS_DIR`, fake
   `tmux` on PATH answering `list-panes -t <sess>:<win>` with a pane id):
   - write a pane state file with `unseen=1`
   - `bash "$CSU" mark-seen --session s1 --window 0` clears `unseen`
   - a pane outside the target window keeps `unseen=1`
   - unknown flag (`mark-seen --session s1 --window 0 --bogus x`) exits non-zero
6. Wire `mark-seen-tests` in `flake.nix` `checks` next to `claude-issues-tests`.
7. Fast gate: `nix build .#default`, `nix flake check` (at least the new check /
   full check), `nix build .#lint`; also `shellcheck` on the edited script and
   `bats tests/mark-seen.bats` locally.
8. Hook-path verification (observe, don’t change conf): confirm
   `session-window-changed[99]` / `client-session-changed[99]` still invoke
   `claude-status-update mark-seen --session … --window …`, then smoke with a
   real or fake-tmux invocation matching that argv and record what cleared.

## Acceptance

- [ ] `claude-status-update mark-seen --session <name> --window <idx>` clears
      `unseen` on matching pane files
- [ ] bats regression would fail on the pre-fix script
- [ ] unknown `--flag` after the top-level loop errors instead of silent drop
- [ ] hook argv path verified by observation (stated in PR / handoff)
- [ ] local gate green: build + flake check + lint
