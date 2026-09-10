## components

### `wire-format`

The daemon-side producer: the nine new carried fields added to `windowLabelFormat`, `labelRow`, `parseWindowLabels`, the exact-or-drop cleaner, the per-field validators, and the nine new `bridgeLabelOptions` entries that give the stamp loop, the per-field diff and `clear` their coverage for free.

- boundaries
  - may-touch: `picker/remotebridge/daemon/windowlabels.go`, `picker/remotebridge/daemon/windowlabels_test.go`
  - must-not-touch: `picker/remotebridge/daemon/subscriptions.go` (the subscription already carries `windowLabelFormat` by reference — no edit is needed for the new fields to ride it), `picker/remotebridge/daemon/daemon.go` (the `SubscriptionChanged` dispatch and the `flush` call are format-agnostic), `picker/remotebridge/daemon/agentstatus.go`, every consumer listed under `consumers-frozen`
- risk: **high** — one positional string is read by the subscription, the backstop `list-windows -F`, the parser and the m2 fixture; a shift here stamps wrong values under the *existing* nine names and regresses three consumers that never changed a line.

### `card-read`

The card's mirror-aware read: `@bridge_win` and the eighteen `@bridge_*` label names parsed into dedicated fields, a pure resolver choosing bridge-or-local with no cross-fallback, the branch/dir block fed from the already-resolved `@bridge_dir`, and `detectBaseBranch` suppressed in a mirror.

- boundaries
  - may-touch: `picker/enrichcard/options.go`, `picker/enrichcard/options_test.go`, `picker/enrichcard/model.go` (`branchBlock`, `winState` consumers only), `picker/enrichcard/model_test.go`, `picker/enrichcard/main.go` (the base-branch guard only), a new resolver file under `picker/enrichcard/`
  - must-not-touch: `picker/enrichcard/model.go` `handleKey`/`footer`/`refreshCmd` (owned by `card-refresh`), `config/tmux.conf.nix`, anything under `picker/remotebridge/`
- risk: **medium** — depends on `wire-format` only through the option *names* (interface I2), and the card already renders every absent name as "no issue / no PR", so it is buildable and testable before the producer lands.

### `ctl-verb`

The `enrich-refresh` entry in the daemon's fixed verb table: a `run-shell -b` whose POSIX body resolves the remote window's own `@branch` and `@worktree`/`@git_root` and runs the remote's `tmux-pr-enrich --target … --force`, plus the `CtlProtocolVersion` bump the table's contract requires for any new verb.

- boundaries
  - may-touch: `picker/remotebridge/daemon/ctl.go` (the `verbs` map and a sibling of `toolResolveScript`), `picker/remotebridge/daemon/ctl_test.go`, `picker/remotebridge/wire/protocol.go` (the version constant only)
  - must-not-touch: `picker/remotebridge/daemon/windowlabels.go`, `picker/remotebridge/daemon/daemon.go` (`parseCtl`/`submit`/`handleCtl` are table-driven and need no change for a verb with no `needsView`/`probe` flag), `picker/remotebridge/cmd/ctl/main.go` (the client forwards argv verbatim), `scripts/tmux-pr-enrich.sh` (the verb speaks its existing single-target CLI)
- risk: **medium-high** — remote shell body under two layers of `tmuxQuote` and one `run-shell` format expansion, where a single `'` or an un-doubled `#` is a silent misfire on the remote; the version bump also makes *every* verb from a reloaded ctl answer "reopen the bridge" against a daemon still running the old table.

### `card-refresh`

The card's `[r]` in a mirror: the ctl handle flags (`--bridge-ctl-bin`, `--bridge-sock`, `--bridge-pane`) parsed into `cfg`, a fire-and-forget exec of the ctl binary with the `enrich-refresh` verb, the `refresh sent ↗` flash instead of the blocking spinner, and the `[r] no bridge` footer state when the handle is absent.

- boundaries
  - may-touch: `picker/enrichcard/model.go` (`cfg`, `handleKey`, `footer`, `refreshCmd` and a bridged sibling), `picker/enrichcard/main.go` (flag registration), `picker/enrichcard/model_test.go`
  - must-not-touch: `picker/enrichcard/options.go` (owned by `card-read`), `picker/remotebridge/`, `config/tmux.conf.nix`
- risk: **medium** — shares `model.go` and `main.go` with `card-read`, so it is sequenced after it rather than beside it; correctness depends on two interfaces owned elsewhere (I3's verb name and argv shape, I4's flag names).

### `bind`

The `prefix + i` bind gains the three ctl-handle flags and nothing else: still a plain `floatBind` (not `bridgeGate`d, not `bridgedFloatTool`), still stamping `@float_geom` and `remain-on-exit off` through `mkFloat`.

- boundaries
  - may-touch: `config/tmux.conf.nix` — the `floatBind "i" floatCard …` block inside `lib.optionalString enrichEnable` only; `tests/conf-shell-quoting.bats` if a fixture for the new flags is warranted
  - must-not-touch: `bridgeGate`, `bridgeCtl`, `bridgedFloatTool`, `mkFloat`, `floatNewPaneGuard`, every other bind in the file, `flake.nix`'s `float-conf-assertions`
- risk: **low-medium** — one nested `if-shell "…"` string under `esc`; the failure mode is a quoting slip that `float-conf-assertions` and `nix build .#default` catch before any hardware run.

### `integration-fixture`

The m2 bats case "daemon ships the remote's window labels onto the mirror windows" extended to stamp the nine new remote options on the source server and assert their `@bridge_*` copies on the mirror; the existing bare-mirror `bare_labels` assertion already proves the new names are unset when the remote carries nothing.

- boundaries
  - may-touch: `tests/remote-m2-integration.bats` (that one `@test` block and its immediate neighbour on subscriptions)
  - must-not-touch: `bridge_up` and the shared harness helpers, every other `@test`
- risk: **low-medium** — the check itself is the known-flaky one (darwin reconnect cases, green-TAP-nonzero-exit mode), so a failure here needs reading before it is attributed to this change.

### `consumers-frozen`

Not work — a boundary declaration. The three existing readers of the `@bridge_*` namespace are out of scope and must not change; the acceptance criterion "no regression" is satisfied by I2 holding, not by edits.

- boundaries
  - may-touch: nothing
  - must-not-touch: `scripts/tmux-reflow-windows.sh` (its `FMT` read of four `@bridge_*` names and its `bopt` live conditionals over five), `picker/main.go` (`collectWindows`' 30-field `list-panes` format with its strict `len(parts) != 30` guard, and `parseWindowPaneRows`' fixed indices), `picker/statusline/main.go` (fixed `f[13..20]` indices), `tests/reflow-fanout.bats`
- risk: **n/a** — the risk lives in `wire-format`; any temptation to add a `@bridge_*` read to the picker's or statusline's positional formats is the way to break them.

### `docs`

`CLAUDE.md` (the "Remote Window Labels" section, the `tmux-pr-enrich` and ctl-verb mentions in Script Roles, the "What the Remote Host Needs on PATH" table) and the plan document under `docs/superpowers/plans/` committed beside the spec.

- boundaries
  - may-touch: `CLAUDE.md`, `docs/superpowers/plans/2026-09-09-enrich-card-bridge-state.md`
  - must-not-touch: `docs/superpowers/specs/2026-09-09-enrich-card-bridge-state-design.md` while it is under review
- risk: **low**

## ordering

```
wire-format ∥ ctl-verb ∥ card-read
        ↓
card-refresh ∥ bind ∥ integration-fixture
        ↓
docs
```

- The first wave is parallel-safe because the three touch disjoint files and meet only at interfaces I2 (names) and I3 (verb name and argv), both of which are fixed before any of them starts. `card-read` does not wait on `wire-format`: it reads names, and an absent name is already its documented fallback.
- `card-refresh` follows `card-read` because they edit the same two files, not because of a data dependency; it follows `ctl-verb` for the verb name only. `bind` follows `card-refresh` for the flag names only (I4) and could run beside it once those are agreed. `integration-fixture` waits on `wire-format` landing, since it asserts stamped values.
- `docs` last, because the plan records what shipped.

Where the split genuinely stops:

1. **Inside `wire-format` nothing is separable.** `windowLabelFormat`, `labelRow`, `parseWindowLabels`' `SplitN` width and positional `at(i)` calls, the `bridgeLabelOptions` entries, and the `oneRow` test fixture's field count are one atomic change — a partial edit is a shifted row, and a shifted row stamps wrong values under the existing names.
2. **`ctl-verb` and the version bump are one change.** The table's own contract says a new verb bumps `CtlProtocolVersion`; landing one without the other gives an old daemon a verb it answers "unknown" instead of "reopen the bridge".
3. **`card-read` and `card-refresh` share `model.go` and `main.go`.** They are logically independent and could be one component; they are split only so the read half can ship if the refresh route is still under review.
4. **The dir field is resolved by the producer, not the consumer.** `@bridge_dir` carries the remote's `#{?@worktree,#{@worktree},#{@git_root}}` already collapsed; the card's local `worktree || gitRoot` fallback must not run over bridge fields. That is a cross-component contract (I5), and it is the one place `wire-format` and `card-read` must agree on semantics rather than merely on a name.

## interfaces

### I1 — `windowLabelFormat` (daemon-private positional wire format)

One constant, consumed by exactly four sites, all inside the daemon or its fixture: `subscribeFormats` (`refresh-client -B 'lztmux_labels:@*:<format>'`), `labelShipper.flush`'s backstop `list-windows -t <sess> -F <format>`, `parseWindowLabels`, and the m2 bats case. No consumer outside the daemon reads this format — the three existing readers consume option *names* (I2), which is what makes extending it safe at all.

Positions, `|`-delimited, `SplitN(line, "|", 19)`, read positionally with defaults so a short row still parses:

| pos | source | pos | source |
|---|---|---|---|
| 0 | `window_id` (`@N`, row key) | 10 | `@issue_url` |
| 1 | `@crew_name` | 11 | `@pr_url` |
| 2 | `@crew_color` | 12 | `@pr_draft` |
| 3 | `@pr_number` | 13 | `@branch` |
| 4 | `@pr_state` | 14 | `#{?@worktree,#{@worktree},#{@git_root}}` |
| 5 | `@pr_check_state` | 15 | `@issue_title` |
| 6 | `@pr_mergeable` | 16 | `@pr_title` |
| 7 | `@window_pr_plain` | 17 | `@window_label_id` |
| 8 | `@issue_provider` | 18 | `@window_label_rest_long` |
| 9 | `@issue_id` | | |

Constraints every edit must preserve:
- positions 0–7 byte-identical; `@window_label_rest_long` remains the trailing field (a `|` inside it lands there, not in an enum slot); every free-form field at 8–16 is wrapped `#{s/[|]/ /:…}` (the bracket expression is load-bearing — a bare `s/|/ /` is an empty alternation);
- no `'` or `"` anywhere in the format (`TestSubscribeCmdIsOneQuotedToken`); no control bytes or `\t`/`\n` escapes (`picker/tmuxformat` scan inside `picker-go-tests`);
- no `TrimSpace` per row or per field — `@window_pr_plain`'s leading space is what reflow's `pr_colw` pads against;
- a row whose position 0 is empty is skipped, never a zero row.

### I2 — the `@bridge_*` label-option namespace (the contract the three frozen consumers hold)

Window options on the local mirror window, the daemon the sole writer, every name in `bridgeLabelOptions` in one ordered list that drives the stamp loop, the per-field diff and `clear`. Semantics that must not move:

- **empty means unset** (`set-option -w -u`), never a stamped `""`; a bare mirror holds no `@bridge_*` label option at all except the daemon's own `@bridge_win` marker (asserted by the m2 `bare_labels` check);
- **an unchanged row is not rewritten**; the cache key is `(remote window id, local window id)` (`writtenLabels.localWin`), so a `retireMirror` rebuild re-stamps;
- **one argv command sequence per window**, `;`-joined, executed by `cfg.LocalTmux` without a shell — hence a value beginning with `-` or equal to `;` is dropped whole;
- **value dialect**: raw `#` (never doubled — the `@window_bridge_name` doubled-`#` dialect is that option's alone), no `|`, no `#[…]` markup, no control bytes; enums lowercase-word-matched, colours `crewColorRe`-matched, numbers `digitsRe`-matched;
- **teardown unsets every entry** via `clear` while the mirror windows still exist.

Existing names and their readers (read by name, never by this format's position):

| option | reflow `FMT` | reflow `bopt` (live) | `picker/main.go` | `statusline` |
|---|---|---|---|---|
| `@bridge_crew_name` | ✓ | | ✓ | ✓ |
| `@bridge_crew_color` | | ✓ | ✓ | ✓ |
| `@bridge_pr_number` | | ✓ | | |
| `@bridge_pr_state` | | ✓ | ✓ | |
| `@bridge_pr_check_state` | | ✓ | ✓ | |
| `@bridge_pr_mergeable` | | ✓ | ✓ | |
| `@bridge_pr_plain` | ✓ | | ✓ | |
| `@bridge_label_id` | ✓ | | ✓ | ✓ |
| `@bridge_label_rest_long` | ✓ | | ✓ | ✓ |

New names, read by the card alone: `@bridge_issue_provider`, `@bridge_issue_id`, `@bridge_issue_url`, `@bridge_pr_url`, `@bridge_pr_draft`, `@bridge_branch`, `@bridge_dir`, `@bridge_issue_title`, `@bridge_pr_title`. Two cleaning policies: **identity fields drop whole when over cap or failing their validator** (provider, id, both URLs, draft flag, branch, dir — a truncated URL or path is a wrong value, not a shorter one); **display fields truncate** (both titles). Validation is the producer's alone; no consumer re-validates.

Sibling `@bridge_*` names *outside* this shipper, which nothing here may write or repurpose: `@bridge_win` (window marker), `@bridge_pane` and `@bridge_proc` (pane, `agentShipper`/`setupWindow`), `@bridge_sock`, `@bridge_host`, `@bridge_state` (session), `@window_bridge_name` (window, daemon-owned name in its doubled-`#` dialect).

### I3 — the ctl verb table and its transport

- Table shape: `verbs[name] = verb{args int, windows, layout, moves, needsView, probe bool, build func(pane, win, sess string, args []string) ([]string, error)}`; `parseCtl` enforces `len(args) == v.args`, resolves `win` from `pane` via `ctlState.paneToWin`, and never forwards raw command text.
- Wire: `FrameCtl`, NUL-separated argv `[CtlProtocolVersion, verb, pane, args...]`; the client (`picker/remotebridge/cmd/ctl`) is `ctl --sock <path> [--display-error <client>] <verb> <remote-pane-id> [args...]` and forwards argv verbatim; the ack is an empty payload or error text shown via `display-message` on `--display-error`.
- `enrich-refresh`: `args: 0`; no `windows`/`layout`/`moves` (it changes no structure — the result arrives as `%subscription-changed` label rows, never a reconcile); no `needsView`/`probe`; `build` uses `win` for the remote `--target` and `pane` for `run-shell -b -t`.
- Quoting rules for the remote body, inherited from `toolResolveScript`/`themeApplyScript`: wrapped as `run-shell -b -t <pane> tmuxQuote("exec /bin/sh -c " + tmuxQuote(script))`, so the body carries **zero `'` characters**; `run-shell` format-expands the whole string before `/bin/sh` sees it, so every literal `#` is doubled and `${p#*=}` (never `${p#PATH=}`) is the only parameter-expansion spelling; PATH is restored from `tmux show-environment -g PATH` guarded on a non-empty `PATH=?*` match; a missing `tmux-pr-enrich` on the remote degrades to nothing visible, symmetric with `theme`.
- `wire.CtlProtocolVersion` moves `"4"` → `"5"` in the same change as the table entry.
- Bind-side spelling, unchanged: `bridgeCtl = lztmux-remote-bridge-ctl --display-error=#{q:client_name} --sock=#{q:@bridge_sock}` followed by `<verb> #{q:@bridge_pane}`; `@bridge_sock` is always passed in `--sock=` form, never word-initial.

### I4 — card launch flags (bind → card)

Existing, unchanged: `--target '#{session_id}:#{window_id}'`, `--pr-enrich-bin <store path>`, the eight `--thm-*`, the nine `--icon-*` in the **raw** (not `##`-escaped) glyph set. New: `--bridge-ctl-bin <store path of lztmux-remote-bridge-ctl>`, `--bridge-sock '#{@bridge_sock}'`, `--bridge-pane '#{@bridge_pane}'` — both format-expanded at keypress and empty on a non-mirror window, which is how the card knows it has no handle. The bind stays a plain `floatBind` with `floatCard`: `@float_geom '64 18 20% 15%'` and `remain-on-exit off` stamped by `mkFloat`, which `float-conf-assertions` enforces on every `new-pane` bind.

### I5 — the card's mirror resolution and `[r]` semantics

- Mode is `@bridge_win == "1"` from the same `show-options -w` read; the resolver is pure over parsed options and yields the effective `winState`.
- In a mirror: issue, PR, branch, dir and URLs come from `@bridge_*` **only** — no fallback to `@issue_*`/`@pr_*`/`@branch`/`@worktree`/`@git_root`, which are the launcher's residue. `@bridge_dir` is already the remote's `worktree || git_root`; the local fallback chain does not run over it. `detectBaseBranch` is skipped (its `git -C <dir>` would read a same-named local repo). `@window_task`, `@window_claude_ago`, `@active_pane_icon` stay local — they are already bridge-correct through `agentShipper` and `@bridge_proc`.
- Not in a mirror: byte-identical behaviour to today, including the blocking `refreshCmd` and its spinner.
- `[r]`: non-mirror → local `tmux-pr-enrich --target … --branch … --dir … --force`, spinner; mirror with handle → ctl `enrich-refresh`, `refresh sent ↗` flash, no spinner; mirror without handle → footer `[r] no bridge`, key inert. An empty branch still reads `[r] no branch` in either mode.

### I6 — `tmux-pr-enrich` single-target CLI (shared by the local card and the remote verb body)

`--target <sess>:<win> --branch <b> [--dir <d>] --force`; exits 0 unconditionally; exits early when the target window's `@bridge_win` is `1`; writes `@pr_number @pr_title @pr_state @pr_check_state @pr_url @pr_mergeable @pr_draft @pr_branch` via `write_pr_options`, which is exactly the set the subscription then reports back. Unchanged by this work; both callers depend on it staying so.

### I7 — test seams that must keep working

- `mirrorCfg(&calls)` captures `cfg.LocalTmux` argv; `TestLabelShipperApply`/`TestLabelShipperClear`/`TestLabelShipperRestampsRebuiltMirror` count `set-option` against `len(bridgeLabelOptions)` and so self-adjust to the new entries; `oneRow` builds its fixture at the format's field count and must move with it.
- `TestParseCtlVerbTranslation` is the table-driven home for the new verb's built command; `TestParseCtlRejects` for its argument count.
- `picker/enrichcard` tests are pure over `model`/`winState` (`render` strips ANSI); the resolver is testable without tmux.
- `remote-m2-integration-tests` stamps real names on the source server and asserts `@bridge_*` copies on the mirror after `bridge_up`; it asserts values, not dims, for this shipper alone — which is why it, and a hardware drive, are the only content-level checks this change gets.
- `float-conf-assertions`, `tmux-format-delimiter-assertions`, and `picker-go-tests` (which includes the `tmuxformat` scan over `picker/**`) all run under `nix flake check`; `nix build .#lint` is separate and not subsumed.
