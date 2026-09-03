# Decomposition — bridge carries crew codename and issue/PR label state onto mirror windows (#462)

## components

### daemon-label-shipper
One-line: a `labelShipper` (sibling of `agentShipper`) that polls the remote's window
options through `rt` on the main loop, sanitizes them, and stamps them onto the local
mirror windows under the `@bridge_*` namespace, skipping unchanged rows, forcing one
reflow per changed pass, and unsetting everything at teardown.

- boundaries:
  - may-touch: `picker/remotebridge/daemon/windowlabels.go` (new),
    `picker/remotebridge/daemon/windowlabels_test.go` (new),
    `picker/remotebridge/daemon/daemon.go` (shipper construction next to
    `agents = newAgentShipper(...)` ~line 571, poll call next to `agents.poll(cfg, rt)`
    ~line 677, `clear` call inside the `teardown` closure ~line 479),
    `picker/remotebridge/daemon/windows.go` (only to share/rename the existing
    `stripWindowName` sanitizer — no behavior change to `@window_bridge_name`).
  - must-not-touch: `picker/remotebridge/daemon/agentstatus.go` (pattern source,
    read-only), `scripts/**`, `config/**`, `picker/main.go`, `picker/tui.go`,
    `picker/statusline/**`, `picker/zoxide.go`, `picker/remotepick.go`,
    `picker/remotebridge/cmd/daemon/main.go` (no new flags/env — `Config.Reflow`
    is already wired).
- risk: high — control-stream ordinal discipline (round-trips on the main loop only,
  never nested), the fork-per-window-per-tick economy of the unchanged-row rule, and
  teardown completeness.

### reflow-bridge-labels
One-line: `tmux-reflow-windows.sh`'s `@bridge_win` branch stops blanking and instead
feeds the daemon's `@bridge_*` copies into the same per-window arrays every other
window uses (id/rest/pr/crew + widths), so the existing floor/want math, column fit,
and `_disp` clipping apply to mirrors unchanged; the crew/PR *color* conditionals in
the grid ENTRY and the single-line global format gain a `@bridge_win`-gated variant.

- boundaries:
  - may-touch: `scripts/tmux-reflow-windows.sh` (FMT, the bridge branch, the CREW and
    PRCOLOR fragments), `config/tmux.conf.nix` (only the single-line
    `status-format[1]` crew-badge and PR-color conditionals — the analogue of ENTRY's
    CREW/PRCOLOR).
  - must-not-touch: `scripts/lib-reflow.sh` (`reflow_fit_columns` is shape-agnostic;
    it staying untouched is the width-correctness argument), `scripts/lib-enrich.sh`,
    `scripts/lib-icons.sh`, `picker/**`, `tests/reflow.bats` (no new pure math means
    no new fixtures).
- risk: high — grid width math and hand-built tmux format strings; a label wider than
  its column is a bug, and the `-F` delimiter check (`tmux-format-delimiter-assertions`)
  gates the FMT change.

### picker-bridge-labels
One-line: `collectWindows()` reads the `@bridge_*` copies and, on a `@bridge_win`
window, substitutes them into the existing `windowData` fields at collect time, so
`tui.go`'s row assembly, column math, and search text run untouched.

- boundaries:
  - may-touch: `picker/main.go` (`collectWindows` list-panes format, `winInfo`,
    normalization), one test file (`picker/enrich_test.go` or `picker/tui_test.go`)
    for coverage of the substitution.
  - must-not-touch: `picker/tui.go` (the render path consumes `windowData` as-is; the
    `^o` handler is owned by a sibling worker), `picker/zoxide.go`,
    `picker/remotepick.go`, `picker/statusline/**`, `picker/remotebridge/**`.
- risk: medium — the list-panes format's exact field-count check, and preserving the
  label-less mirror fallback chain (branch → bridgeName → name) byte-for-byte.

### statusline-bridge-labels
One-line: `tmux-statusline`'s `@bridge_win` branch stops early-returning after the
host pill and renders the bridge crew badge (tinted), the bridge label id + rest, and
a state-colored PR badge from the new volatile fields.

- boundaries:
  - may-touch: `picker/statusline/main.go` (`volatileFields`, `fetchVolatile`, `args`,
    `sessionSegment`), `picker/statusline/main_test.go`.
  - must-not-touch: `picker/statusline/claude.go`, `picker/statusline/usage.go`,
    everything outside `picker/statusline/`.
- risk: low — additive fields on a tick-constant command string; the fail-closed
  field-count check holds because every crossed value is daemon-sanitized (no `|`).

### docs-and-gates
One-line: record the mechanism and its liveness caveat in CLAUDE.md (beside "Remote
Agent Status"), commit the plan under `docs/superpowers/plans/`, and run the
end-to-end `--test-local` repro plus the three nix gates.

- boundaries:
  - may-touch: `CLAUDE.md`, `docs/superpowers/plans/*.md`.
  - must-not-touch: all code.
- risk: low.

## ordering

1. daemon-label-shipper ∥ reflow-bridge-labels ∥ picker-bridge-labels ∥
   statusline-bridge-labels — mutually parallel-safe: disjoint file sets, coupled only
   through the option-name and sanitization contracts pinned below. Integrate
   daemon-label-shipper first regardless of build order: it is the producer, and the
   `--test-local` verification of every render site depends on it.
2. docs-and-gates — strictly last (documents what landed; gates run over the union).

## interfaces

### 1. The option namespace (the producer/consumer contract)

Window-scoped tmux options, written **only** by the daemon's `labelShipper`, **only**
on `@bridge_win` mirror windows, addressed by tmux window id (`@N`, never
`<sess>:<index>`, #411):

| remote source              | bridge copy on the mirror window |
|----------------------------|----------------------------------|
| `@crew_name`               | `@bridge_crew_name`              |
| `@crew_color`              | `@bridge_crew_color`             |
| `@window_label_id`         | `@bridge_label_id`               |
| `@window_label_rest_short` | `@bridge_label_rest_short`       |
| `@window_label_rest_long`  | `@bridge_label_rest_long`        |
| `@window_pr_plain`         | `@bridge_pr_plain`               |
| `@pr_number`               | `@bridge_pr_number`              |
| `@pr_state`                | `@bridge_pr_state`               |
| `@pr_check_state`          | `@bridge_pr_check_state`         |
| `@pr_mergeable`            | `@bridge_pr_mergeable`           |

An empty or validation-failing remote value **unsets** the local option
(`set-option -w -u`), never sets it to `""` — so a mirror whose remote carries nothing
is option-free and renders exactly today's output.

**Decision — namespaced, not direct names.** Writing the real names onto a local
window was considered and rejected: `@pr_*` presence keys `tmux-pr-enrich`'s
per-branch fallback and the `prefix + i` open-URL binds (a remote PR in a repo this
host may not have); `@window_label_*` already has a local writer — reflow itself
stamps them on every window of the mirror session, so same-name would be a two-writer
race the daemon loses on every reflow pass. The namespace makes the daemon the single
writer of everything in this table.

### 2. Sanitization contract (daemon side, before anything crosses)

- Every value passes `stripWindowName` (`picker/remotebridge/daemon/windows.go`):
  drop `|` / CR / LF / control chars / DEL, strip `#[...]` markup to a fixed point.
- **No `#`-doubling** — deliberately unlike `@window_bridge_name`. These options are
  consumed on the same paths as local label options, which store raw `#`
  (`@window_pr_plain` holds `" ✓ #123"` today), so raw-parity means no consumer needs
  a decode step and reflow's width measurement needs no `##`-collapse.
  `@window_bridge_name` keeps its existing doubled convention, untouched.
- Token validation on the non-free-form fields, failure → treated as empty:
  - `@bridge_pr_state`, `@bridge_pr_check_state`, `@bridge_pr_mergeable`: `^[a-z]+$`
  - `@bridge_pr_number`: `^[0-9]+$` (a remote `none` maps to empty)
  - `@bridge_crew_color`: `^(#[0-9A-Fa-f]{6}|colour[0-9]{1,3}|[A-Za-z][A-Za-z0-9_]*)$`
    — it is interpolated into `#[fg=…]` by two format strings and into ANSI by the
    picker's `ansiFgTmux`, so it must be a bare color token, never markup.
- Length caps: `@bridge_crew_name` ≤ 24 runes; the label/pr text fields ≤ 120 runes.

### 3. Go seam (daemon)

- File `picker/remotebridge/daemon/windowlabels.go`:
  - `const windowLabelFormat = "'#{window_id}|#{@crew_name}|#{@crew_color}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@window_pr_plain}|#{@window_label_id}|#{@window_label_rest_short}|#{@window_label_rest_long}'"`
    — fixed/token fields first; the free-form fields sit last; parsed with
    `strings.SplitN(line, "|", 11)`. A `|` inside `rest_short` bleeds into
    `rest_long` and is accepted as broken-upstream: the remote's own reflow FMT has
    the identical exposure, so the value was already unrenderable there.
  - `type labelShipper struct{ written map[string]labelRow; lastPoll time.Time }`,
    keyed by **remote** window id.
  - `func newLabelShipper() *labelShipper`
  - `func (s *labelShipper) poll(cfg Config, reg *registry, rt roundTrip)` — 1s floor
    (`windowLabelPollInterval`), **main loop only** (called adjacent to
    `agents.poll(cfg, rt)`); one
    `list-windows -t <RemoteSession> -F windowLabelFormat` round-trip per pass;
    remote id → local window via `reg.byRemoteID(id).localWin`; ids not in the
    registry are skipped; entries whose id left the registry are forgotten (the
    closed local window took its options with it).
  - `func (s *labelShipper) clear(cfg Config, reg *registry)` — unsets every option
    stamped on every still-registered window; called from the `teardown` closure
    before the session kill. The kill would also delete them, but `clear` keeps the
    "teardown deletes what it wrote" guarantee on paths where the kill fails.
- Unchanged-row rule (same as `agentstatus.go`): the whole row is compared against
  `written`; on a change, only the options whose values differ are set/unset — a
  re-stamp is a fork per window per tick. If any window changed in a pass, exactly
  **one** `cfg.reflow()` runs after all stamps (a label change alters no window
  count, so reflow's `count:width:height` cache would otherwise skip it — the
  `@window_bridge_name` precedent).

### 4. Shell seam (reflow)

- `FMT` in `tmux-reflow-windows.sh` gains the ten `#{@bridge_*}` fields inserted
  **before** `#{@window_task}`, which must remain the final field (it is the only
  unsanitized free-form one); the `while IFS='|' read` variable list extends in the
  same positions. All inserted values are daemon-sanitized (never contain `|`).
- The `@bridge_win` branch populates the same arrays as the local path —
  `win_id`, `win_rest_short`, `win_rest_long`, `win_pr`, `win_crew` (+ their `_dw`
  widths, and the `pr_colw` / `crew_colw` maxima) — from the bridge copies, keeping
  `@window_bridge_name` as the rest-fallback when the bridge label fields are empty.
  Downstream, the floor/want lists, `reflow_fit_columns`, and the
  `win_id_disp` / `win_crew_disp` / `win_disp` clipping loop run **unchanged**; that
  uniformity is the whole width-correctness argument (badge dropped first, id clipped
  last, label never wider than its column).
- Reflow-owned outputs keep their names on mirror windows — `@window_label_id`,
  `@window_label_id_disp`, `@window_label_rest_short/_long`, `@window_label_disp`,
  `@window_pr_plain`, `@window_pr_disp`, `@window_crew_disp` — and stay the only
  options the row templates read for **text**.
- The two **color** reads are the only template changes: the grid ENTRY's `CREW` and
  `PRCOLOR` fragments, and their single-line analogues in `config/tmux.conf.nix`'s
  global `status-format[1]`, each wrap in `#{?#{@bridge_win},<@bridge_* variant>,<local variant>}`.
  Gate on `@bridge_win`, **not** first-non-empty: a mirror window can carry stray
  local `@branch`/`@pr_*` residue stamped by creation hooks in the launcher's cwd.

### 5. Picker seam

- `collectWindows()`'s `list-panes -a -F` format appends `#{@bridge_win}` plus the
  eight consumed bridge fields (`crew_name`, `crew_color`, `label_id`,
  `label_rest_long`, `pr_plain`, `pr_state`, `pr_check_state`, `pr_mergeable`); the
  `len(parts) != 19` guard updates to the new exact count.
- Normalization happens at collect time, gated on `@bridge_win == "1"`: the existing
  `winInfo`/`windowData` fields `crewName`, `crewColor`, `labelID`, `labelRest`,
  `prPlain`, `prState`, `prCheck`, `prMergeable` are **replaced** by the bridge
  copies (possibly empty). No decode step — the values are stored raw (§2). The
  `windowData` field set and `tui.go`'s `renderedWin` assembly are unchanged, and the
  `branch → bridgeName → name` identity fallback is untouched so a label-less mirror
  row is byte-identical to today. The picker reads `@bridge_*` directly rather than
  relying on reflow's stamped copies, so the three render sites stay symmetric and
  independent.

### 6. Statusline seam

- `volatileFields` appends, in order: `#{@bridge_crew_name}`, `#{@bridge_crew_color}`,
  `#{@bridge_label_id}`, `#{@bridge_label_rest_long}`, `#{@bridge_pr_plain}`,
  `#{@bridge_pr_state}`, `#{@bridge_pr_check_state}`, `#{@bridge_pr_mergeable}` —
  the command string stays tick-constant, and sanitized values cannot carry `|`, so
  `fetchVolatile`'s fail-closed count check holds.
- `args` gains `bridgeCrewName`, `bridgeCrewColor`, `bridgeLabelID`,
  `bridgeLabelRest`, `bridgePrPlain`, `bridgePrState`, `bridgePrCheck`,
  `bridgePrMergeable`.
- `sessionSegment`'s `@bridge_win` branch keeps the host pill first, then renders the
  crew badge (bridge tint, `thmMauve` fallback — mirroring the local badge), then
  `@bridge_label_id` + `@bridge_label_rest_long` in the local issue-block colors,
  then the PR badge colored by the same precedence as `build_window_label`:
  merged→mauve, closed→overlay0, conflicting-or-failure→red, pending→peach,
  else green.

### 7. Liveness caveat (documentation contract)

What crosses live vs last-known must be stated in CLAUDE.md and the PR:
`@crew_name`/`@crew_color` (stamped at dispatch) and `@window_label_*` (stamped by
the remote's reflow hooks) are live; `@pr_*`-derived fields refresh on the remote
only while a real client is attached there — the bridge's control-mode client renders
no status line, so those cross as last-known values. This task does not change that.
