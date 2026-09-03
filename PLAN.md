# Plan — bridge carries crew codename and issue/PR label state onto mirror windows (#462)

Constraints: `SPEC.md` (R1–R8), `DECOMPOSITION.md` (components, ordering,
interfaces). Every step names its component and stays inside that component's
`boundaries`.

## Declared deviations from `DECOMPOSITION.md`

All of them, in one place.

**Boundary amendments** (`may-touch` additions; no component's must-not-touch is
violated). `DECOMPOSITION.md`'s allowlists contain no `tests/` entry at all, and
`docs-and-gates` — the component nominally running the repro — is declared
code-free, so SPEC acceptance 5 is unsatisfiable as bounded.

- `daemon-label-shipper` gains `tests/remote-m2-integration.bats` (Step 5) and
  the `agentstatus.go` comment correction (Step 3), which the file otherwise
  lists as read-only.
- `reflow-bridge-labels` gains `tests/reflow-fanout.bats` (Step 8).
  `tests/reflow.bats` (pure `lib-reflow.sh` math) stays untouched, as required.
- `picker-bridge-labels` gains `picker/main_test.go` (Step 10) — the natural
  home for the extracted function; §5 named only `enrich_test.go`/`tui_test.go`.

**Interface deviations.**

- §6's `statusline-bridge-labels` specifies "a state-colored PR badge from the
  new volatile fields". There is none: SPEC R5.4 drops it deliberately (the
  package renders no PR badge for any window, and the binary is passed no colour
  for `closed`). §6's risk note — "every crossed value is daemon-sanitized" —
  stays true, because Step 11 reads the `@bridge_*` copies.
- §3's read format drops `@window_label_rest_short` (SPEC R3/R4.1), so exactly
  one free-form field remains and it goes last: nine carried values and
  `SplitN(…, 10)`, not ten and `SplitN(…, 11)`. This also rejects §3's stated
  acceptance of a `|` bleed as "broken upstream" — R4.1 argues the framing must
  prevent the shift instead, since sanitization runs after the split.
- §4's "the ten `#{@bridge_*}` fields" in reflow's `FMT` narrows to **four**
  (Step 6): the other five are live template reads, and naming them in the
  `read -r` list is five `SC2034` warnings that fail `nix build .#lint`.
- §5's "the `branch → bridgeName → name` identity fallback is untouched …
  byte-identical to today" no longer holds: Step 10 clears `branch` on a mirror
  row and skips the git fallback, because both currently make the picker render
  the *launcher's* branch (SPEC R5.3(1)). That is a deliberate bug fix, so
  acceptance 2 means "renders the remote window name", not "renders what it
  renders today".

**Structural, not a deviation.** Steps 10 and 11 read `@bridge_*` directly, which
is what §5 and §6 pin ("the picker reads `@bridge_*` directly rather than relying
on reflow's stamped copies, so the three render sites stay symmetric and
independent"). SPEC R2.1 records why that is load-bearing rather than stylistic.

---

## Step 1: `labelShipper` — parse, sanitize, validate (component: daemon-label-shipper)

New file `picker/remotebridge/daemon/windowlabels.go`, modelled on
`agentstatus.go`. `windows.go` needs no change: `stripWindowName` is already
separate from `sanitizeWindowName`'s `#`-doubling, so it is reusable as-is.

- [ ] `const windowLabelPollInterval = time.Second`.
- [ ] `const windowLabelFormat` — one `|`-delimited `list-windows -F`; the single
      free-form field last (SPEC R4.1):
      `'#{window_id}|#{@crew_name}|#{@crew_color}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@window_pr_plain}|#{@window_label_id}|#{@window_label_rest_long}'`
- [ ] `type labelRow struct` — remote window id plus one field per carried value;
      comparable with `==` so the unchanged-row check is a struct compare.
- [ ] `parseWindowLabels(body string) []labelRow` —
      `strings.SplitN(line, "|", 10)`, positional reads (trailing empty fields
      may or may not survive the trip), skip rows with an empty window id.
- [ ] Sanitize during the parse, so nothing unclean reaches a caller: every field
      through `stripWindowName`; **no** `#`-doubling (SPEC R4.2).
- [ ] Validate, failure → empty: `prState`/`prCheck`/`prMergeable` `^[a-z]+$`;
      `prNumber` `^[0-9]+$` (a remote `none` → ""); `crewColor` one of
      `#RRGGBB` (**case-insensitive** — `ansiFg` accepts either, and a
      lowercase-only regex would silently drop an uppercase colour to the mauve
      fallback), `colour<0-255>`, or `^[a-z]+$`. Cap `crewName` at 24 runes and
      each text field at 120.
- [ ] Reject a value beginning with `-`. `cfg.LocalTmux` execs without a shell,
      so tmux's own `args_parse` sees the value and reads `-n` as a flag. This is
      pre-existing for `@window_bridge_name`, but a branch remainder or an issue
      title is far likelier to start with `-` than a window name is.
- [ ] Trim only `\r` from the end of each line — never `TrimSpace`, per field or
      per row. `@window_pr_plain` carries a **leading** space by construction
      (`REPLY_PR=" <glyph> #<n>"`) and reflow's `pr_colw` padding assumes it. The
      template to copy is `parseAgentStatus`, not `parseWindowList` (which does
      `TrimSpace(row)`).
- [ ] `bridgeLabelOptions` — one ordered
      `[]struct{opt string; get func(labelRow) string}` mapping each value to its
      `@bridge_*` name (SPEC R3). One list, so the stamp loop, the diff and
      `clear` cannot drift apart.

## Step 2: `labelShipper` — apply, poll, clear (component: daemon-label-shipper)

Same file.

- [ ] `apply(cfg Config, reg *registry, rows []labelRow) (changed bool)` — for
      each row resolve the local window with
      `mw, ok := reg.byRemoteID(row.id)` (two return values) and use
      `mw.localWin`; skip unknown ids; skip a row equal to `s.written[id]`;
      otherwise write **only** the differing options.
- [ ] Forget entries whose remote id has left the **registry**. Note in a comment
      that this differs from `agentShipper.apply`, which forgets by absence from
      the *reply*: a remote window that vanishes from `list-windows` while its
      mirror is still registered keeps its last stamp until `reconcileWindows`
      closes it. Harmless, but a reader comparing the two files would otherwise
      read the difference as an oversight.
- [ ] Issue one window's changed options as a single tmux argv command sequence —
      `set-option -w -t @N k v ';' set-option -w -t @N -u k2 ';' …` — the form
      `tmux-reflow-windows.sh` already uses, so a first pass costs N forks, not
      9N. A non-empty value sets; an empty one unsets with `-u` (SPEC R3).
- [ ] `poll(cfg Config, reg *registry, rt roundTrip)` — throttle to
      `windowLabelPollInterval`; one
      `list-windows -t <tmuxQuote(RemoteSession)> -F windowLabelFormat` via
      `one(rt, …)`; bail on `!ok` or `controlmode.Error`; on a changed `apply`,
      call `cfg.reflow()` exactly once, **after** every option write of that pass.
- [ ] `clear(cfg Config, reg *registry)` — unset every option written, on every
      still-registered window, and empty `written`.
- [ ] Note that a bare mirror's **first** pass is reported as changed: `seen` is
      false, so the `prev == r` short-circuit cannot fire and the shipper emits
      nine `-u` for a row carrying nothing, forcing one reflow at daemon start.
      That is fine — one argv command sequence, and `reconcileWindows` already
      ends in a reflow — but surprising enough that a reader would file it as a
      bug without the sentence.

## Step 3: wire the shipper and the coarse tick into the main loop (component: daemon-label-shipper) (implement: escalated)

`picker/remotebridge/daemon/daemon.go`, plus the one-comment correction in
`agentstatus.go`. No new `Config` fields — `Reflow` and `LocalTmux` are already
there, and `reg` is a local in `Run` declared above the `teardown` closure.

- [ ] Declare `var labels *labelShipper` **and** the ticker beside
      `var agents *agentShipper`, above `teardown`. `teardown` is reached from
      three early-return paths that all precede the main loop (plus the post-loop
      call), so a ticker created at the loop could never be stopped by them;
      nil-guard `Stop()` exactly as `agents` is nil-guarded. Give the period a
      named constant beside `agentStatusPollInterval`, so SPEC R1.1's "5s
      worst-case first-appearance latency" is greppable from the code.
- [ ] In `teardown`: `if labels != nil { labels.clear(cfg, reg) }` beside
      `agents.clear()`, and stop the ticker. `clear` runs before `kill-session`,
      and the intervening `reg.all()` loop only unregisters sinks and closes
      conns — the mirror windows still exist, so the `-u` lands.
- [ ] Construct the shipper where `agents = newAgentShipper(…)` runs, and call
      `labels.poll(cfg, reg, rt)` right after `agents.poll(cfg, rt)` — **main
      loop only**.
- [ ] Replace the loop's blocking `nextLine(pump, st)` with a select over
      `pump.lines` and a 5s ticker (SPEC R1.1). Two invariants, both silent and
      fatal if missed:
      - every line received must still pass through `claimSeq(l, st)` before
        dispatch — follow `waitHellos`, which already selects on this channel and
        calls it explicitly;
      - the receive is `l, ok := <-pump.lines` and `!ok` must leave the **loop**;
        `break` inside a `select` breaks the select, so use a label or a flag.
      A tick falls through to the top of the loop, which re-runs `settle()` and
      both polls.
- [ ] Correct `agentStatusPollInterval`'s "there is no timer … an agent that
      changes state redraws its pane first" comment: it is no longer true, and
      the agent poll's new 5s ceiling is an intentional improvement to a shipped
      subsystem, not an accident.

## Step 4: unit-test the shipper (component: daemon-label-shipper)

New `picker/remotebridge/daemon/windowlabels_test.go`, table-driven in the style
of `panediff_test.go`.

- [ ] `parseWindowLabels`: a well-formed multi-row body; a title carrying
      `#[fg=red]` (stripped); a `|` inside the last field (kept whole by
      `SplitN`, then dropped by `stripWindowName`, with no field shift); short
      rows; a blank line.
- [ ] Validation: `none` → ""; a non-`^[a-z]+$` state → ""; `#[fg=x]` as a crew
      colour → ""; `colour42`, `#89b4fa` and `red` accepted; the rune caps.
- [ ] `apply` against a fake `LocalTmux` recorder: first pass sets; an identical
      second pass issues **zero** tmux calls; a changed field writes only that
      option; an emptied field emits `-u`; a row whose id left the registry is
      forgotten. `clear` unsets everything it wrote — this is the only place
      `clear` is provable (SPEC R8).

## Step 5: an offline `--test-local` proof (component: daemon-label-shipper, boundary amendment)

`tests/remote-m2-integration.bats` — one new `@test` reusing `bridge_up`, which
forwards trailing args to the daemon.

- [ ] Stamp `@crew_name`, `@crew_color`, `@window_label_id`,
      `@window_label_rest_long`, `@window_pr_plain`, `@pr_number`, `@pr_state`
      on the SRC window; poll until the DST mirror window carries the
      `@bridge_*` copies; assert each value.
- [ ] Assert a SRC value carrying `|` and `#[fg=red]` arrives sanitized.
- [ ] For the bare-mirror half, create a **second** remote window after
      `bridge_up` (the `ctl new-window` test's pattern) — `bridge_up` waits on
      the first window, which is the one the test stamps, so both halves would
      otherwise target the same window. Gate first on the *local* mirror of that
      window existing and carrying `@bridge_win`: `reconcileWindows` runs on the
      main loop's next pass, so asserting against a DST window that does not yet
      exist would pass for the wrong reason. Then assert its `@bridge_*` options
      are **unset**, distinguished by listing options (`show-options -w -t …` +
      grep), not by `show-options -qv`, which returns empty for unset *and* `""`.
- [ ] No `clear()` assertion: teardown kills the DST session, so the options go
      away with the window and any such check passes vacuously.
- [ ] Keep sending remote output while polling, as the agent-status test does.
      With Step 3's ticker the poll no longer depends on stream traffic, but the
      output keeps the test fast rather than tick-bound.

## Step 6: reflow's bridge branch feeds the shared arrays (component: reflow-bridge-labels) (implement: escalated)

`scripts/tmux-reflow-windows.sh` only. `scripts/lib-reflow.sh` must **not**
change (SPEC R6).

- [ ] Extend `FMT` with the **four** `#{@bridge_*}` fields the shell consumes —
      `@bridge_label_id`, `@bridge_label_rest_long`, `@bridge_pr_plain`,
      `@bridge_crew_name` — inserted **after** `#{@bridge_win}` and **before**
      `#{window_name}`, leaving `window_name`, `@window_bridge_name` and
      `@window_task` as the free-form tail (stricter than `DECOMPOSITION.md`
      §4's "before `#{@window_task}`", because `window_name` is the one field the
      existing comment admits may carry a `|`). Extend the
      `while IFS='|' read -r …` list in the same positions and update the
      delimiter-safety comment.
- [ ] The other five carried values (`@bridge_crew_color`, `@bridge_pr_number`,
      `@bridge_pr_state`, `@bridge_pr_check_state`, `@bridge_pr_mergeable`) must
      **not** enter `FMT`: they are read live by the templates in Step 7, exactly
      as their local counterparts are today (see the `@crew_color` note above
      `FMT`). Pulling them into the read list gives five unused variables and
      five `SC2034` warnings — the script carries no file-level disable — which
      fails `shellcheck`, `nix build .#lint` and the pre-commit hook. The two
      wrong repairs to avoid when that gate goes red are a file-level
      `# shellcheck disable=SC2034` (which blinds the rest of a 500-line script)
      and moving colour decisions out of the template into the shell (which
      breaks Step 7's liveness).
- [ ] Convert the `if [[ $bridge == 1 ]]` branch from blank-and-`continue` into
      the first arm of an `if/else` whose other arm is the existing
      `build_window_label` path. The bridge arm sets `win_id` from
      `@bridge_label_id`, `win_rest_long` from `@bridge_label_rest_long`,
      `win_pr` from `@bridge_pr_plain`, and the crew name from
      `@bridge_crew_name` — keeping the folded trailing space (`"${crew} "`),
      which is what makes `crew_colw` exact.
- [ ] Derive `win_rest_short` locally rather than carrying a second free-form
      field: **empty** when the bridge id is non-empty, else equal to the long
      rest. That reproduces `build_window_label short`'s own behaviour for an
      issue window (`REPLY_REST` is set only in the `long` arm), which is what
      keeps the short/long ladder in `reflow_pick_layout` a real compaction lever
      on a fan-out session where every mirror carries an issue. Setting
      `short = long` unconditionally would make `total_short == total_long` and
      send a mirror-heavy narrow session multiline earlier than the equivalent
      local one. The branch-arm distinction (`${branch##*/}`) is deliberately not
      reproduced — it is not recoverable from the long rest alone.
- [ ] The `${bname:-$wname}` rest fallback fires only when **both** the bridge id
      and the bridge rest are empty. On an empty id alone it would throw away the
      rest-only identity of a remote branch with no detected issue.
- [ ] Move the measurement, the `pr_colw` / `crew_colw` maxima and the
      `win_short` composition (`win_id + win_rest_short`, which
      `build_window_label` guarantees as `REPLY == REPLY_ID + REPLY_REST`) below
      the `if/else`, so both arms fill every width input identically —
      `win_short_dw`, `win_long_dw`, `win_id_dw`, `win_pr_dw`, `win_crew_dw`,
      `pr_colw`, `crew_colw`. Moving the maxima is **required**: today the branch
      pins both to 0, and `crew_colw == 0` suppresses the `CREW` fragment
      entirely, so a mirror-only session renders no badge at all.
- [ ] Handle the two `#` conventions with a single per-window **collapse flag**,
      not a parallel measure array. The fallback rule above fires only when the
      bridge id *and* the bridge rest are both empty, so a label is either wholly
      raw (`@bridge_*`) or wholly `#`-doubled (`@window_bridge_name`) — never
      mixed. The flag says which, and the shared measurement applies
      `${v//##/#}` only when it is set. (SPEC R6.2 describes this as piecewise;
      under R5.1's fallback rule the mixed case is unreachable, so the flag is
      the whole of it. Do not "fix" R5.1 into a rest-empty fallback — that would
      create the mixed case this simplification relies on being absent.)
- [ ] Leave the floor/want lists, `reflow_fit_columns` and the
      `win_id_disp`/`win_crew_disp`/`win_disp` clipping loop untouched.

## Step 7: bridge-gated colour reads in both window formats (component: reflow-bridge-labels)

Only the live-at-render reads change, each to
`#{?#{@bridge_win},#{@bridge_X},#{@X}}`. The two templates are **not**
symmetric (SPEC R5.2).

- [ ] `scripts/tmux-reflow-windows.sh`: **5 option names at 9 sites** —
      `@crew_color` ×2 in `CREW`; `@pr_number` ×2, `@pr_state` ×2,
      `@pr_check_state` ×2, `@pr_mergeable` ×1 in `PRCOLOR`. `ENTRY` does *not*
      read `@crew_name` (the badge text is `@window_crew_disp`, handled by
      Step 6). Build each gated expression once with a small shell helper, so the
      name is the unit and the site count is a consequence — substituting "five
      edits" leaves four raw reads live on the mirror path.
- [ ] `config/tmux.conf.nix` global single-line `status-format[1]`: **6 names at
      11 sites** — those five plus `@crew_name` ×2 (gate and text). Use a Nix
      `let` binding for the same reason.
- [ ] Do **not** `#,`-escape the commas inside the new nested conditionals: the
      surrounding `#[fg=…#,bg=…]` keeps its existing escapes, but
      `format_expand` resolves `#{?…,…,…}` before `format_draw` parses `#[…]`
      and its splitter tracks `#{`/`}` nesting, so those commas are already
      protected. Nothing in the build checks this.
- [ ] Gate on `@bridge_win`, never first-non-empty (SPEC R2.2 residue).
- [ ] Confirm a non-bridge window's expression is unchanged in effect: with
      `@bridge_win` empty each conditional resolves to the `#{@X}` read today.
- [ ] No change to `automatic-rename-format`, which also reads
      `@window_label_short`/`@window_pr_plain`: the daemon sets
      `automatic-rename off` on every mirror window, so it never applies.

## Step 8: prove the grid (component: reflow-bridge-labels, boundary amendment)

`tests/reflow-fanout.bats` already runs the real reflow against a private tmux
server, already stamps `@crew_name`/`@crew_color`, and already has a
`@bridge_win` case to extend.

- [ ] A mirror window (`@bridge_win=1` + the `@bridge_*` options) renders the
      badge and the id: assert `@window_crew_disp` and `@window_label_id_disp`.
- [ ] A bare mirror renders the remote name and adds no columns:
      `@window_crew_disp` empty, `@window_label_id` empty.
- [ ] At a narrow client width, two assertions that are actually observable
      (reflow stamps the per-window `_disp` values and `@window_per`, never the
      resolved `colws`), in the style of the existing zoom case which sources
      `lib-icons.sh` for `measure_display_width`: (a) `@window_crew_disp` is
      empty while `@window_label_id_disp` is not — the badge was dropped before
      the id; (b) for two windows in the same grid column,
      `measure(id_disp + label_disp)` is equal — which is what column-exactness
      means observably (acceptance 4).

## Step 9: extract a testable seam in the picker (component: picker-bridge-labels)

`picker/main.go` only. `collectWindows` takes no arguments, execs `tmux` and
`git` inline, and declares `winKey`/`winInfo` in its own body, so nothing in it
is reachable from a test.

- [ ] Hoist `winKey` and `winInfo` to package scope.
- [ ] Extract a pure
      `parseWindowPaneRows(lines []string) ([]winKey, map[winKey]*winInfo)`
      holding the field split, the exact field-count guard and the per-window
      merge. `collectWindows` keeps the `tmux` exec, the git fallback and the
      `windowData` assembly.

## Step 10: the picker prefers the bridge copies (component: picker-bridge-labels)

`picker/main.go` + `picker/main_test.go`; `picker/tui.go` stays untouched, so its
`labelID → branch → bridgeName → name` chain is steered by the data.

- [ ] Extend the `list-panes -a -F` format with `#{@bridge_win}` and the **eight**
      bridge copies the picker consumes, and update the exact field-count guard
      (19 → 28). `@bridge_pr_number` is **not** among them: `colorPRBadge` takes
      `prPlain`/`prState`/`prCheck`/`prMergeable` and never a number, and the
      guard constant should not be arithmetic nobody can re-derive. All eight are
      daemon-sanitized and `|`-free, so the strict guard gains no new exposure.
- [ ] In the pure function, when `@bridge_win == "1"`: substitute `crewName`,
      `crewColor`, `labelID`, `labelRest`, `prPlain`, `prState`, `prCheck`,
      `prMergeable` from the bridge copies, and clear `branch`.
- [ ] Carry a `bridgeWin` field on `winInfo` so `collectWindows` can skip the
      git-branch fallback for those rows. Clearing `branch` is exactly what
      *arms* that fallback (it is gated on `wi.branch == ""`), and the two edits
      sit in different functions.
- [ ] When the bridge *id* is empty and the bridge *rest* is not, set
      `bridgeName` to that bridge rest **raw — not through `decodeBridgeName`**,
      which would collapse a `##` inside a branch-derived rest. Otherwise leave
      the existing `decodeBridgeName(@window_bridge_name)`. Sourcing from the
      bridge copy — never from reflow's `@window_label_rest_long`, which holds
      the `#`-doubled name on a bare mirror — is what keeps that decode in place
      (SPEC R5.3).
- [ ] Tests over the pure function: a bridge row takes the bridge values and no
      branch; a local row is untouched; a bridge row with an empty bridge id but
      a non-empty rest renders that rest; and a bare bridge row keeps its decoded
      `@window_bridge_name` identity. The bare-mirror fixture must carry a
      doubled name (`feat##1`) in **both** its `@window_bridge_name` and its
      `@window_label_rest_long` field — reflow stamps the doubled value into the
      latter on a bare mirror, so only then does the test fail against the
      specific wrong implementation (sourcing the rest from reflow's copy) for
      the right reason rather than merely because `bridgeName` came out empty.

## Step 11: status line 0 renders the mirror's identity (component: statusline-bridge-labels)

`picker/statusline/main.go` + `main_test.go` only.

- [ ] Append four fields to `volatileFields` in a fixed order —
      `#{@bridge_crew_name}`, `#{@bridge_crew_color}`, `#{@bridge_label_id}`,
      `#{@bridge_label_rest_long}` — and read them positionally in
      `fetchVolatile`. All four are daemon-sanitized and `|`-free, so
      `fetchVolatile`'s fail-closed count check gains no new way to freeze line 0
      — which reading the free-form `@window_label_rest_long` would have done, on
      **local** windows (SPEC R5.4). The `#()` command string stays
      tick-constant, which is what stops line 0 blinking.
- [ ] Add the matching `args` fields.
- [ ] In `sessionSegment`'s `@bridge_win` branch: host pill, then the crew badge
      (bridge tint, `thmMauve` fallback — identical to the local badge), then:
      id non-empty → render `@bridge_label_id` **as-is** followed by the long
      rest in the local issue-block colours; id empty and rest non-empty →
      branch glyph + rest, mirroring the local `else` arm; both empty → return
      exactly today's output (host pill alone).
- [ ] Do **not** prepend a provider glyph to the id: `@window_label_id` is
      already `"<provider_icon> <id>"`, so reusing the local block's
      `glyph + issueID` composition would draw `󰊤 󰊤 #460`.
- [ ] Concatenate the id and the rest with **no** separator: `build_window_label`
      already sets `REPLY_REST=" ${rtitle}"` with a leading space in the
      id-bearing arm. Copying the local block's `issueID + " " + issueTitle`
      shape would render a double space. The id-less arm supplies its own space
      after the branch glyph, since its rest has none.
- [ ] **No PR badge** (SPEC R5.4) — the package renders none for any window and
      the binary is passed no colour for `closed`. Recorded in the PR.
- [ ] Leave the second `bridgeWin` branch (the dir-segment suppression) alone;
      say so in the commit message rather than implying `sessionSegment` is the
      only one.
- [ ] Test `sessionSegment`: a mirror with a full bridge identity; a mirror with
      an id-less but rest-bearing identity. The "mirror with nothing" and
      "local window unchanged" cases already exist as
      `TestSessionSegmentBridgeWinStopsAtPill` and `TestRenderLineBridgeHost`,
      which build a mirror `args` with the bridge fields empty and assert the
      exact host-pill-only string — **they must stay green unmodified**. Having
      to edit either is the signal that the both-empty arm drifted.

## Step 12: docs, plan doc, gates (component: docs-and-gates)

- [ ] CLAUDE.md: a "Remote Window Labels" section beside "Remote Agent Status" —
      the `@bridge_*` namespace and why the daemon does not write the real names
      while reflow legitimately does; why every render site reads bridge-direct
      (reflow is client-gated, and its copies speak the doubled-`#` dialect); the
      main-loop-only poll and the coarse tick that backs it, including the
      `claimSeq` invariant; the unchanged-row rule; teardown; the forced reflow —
      and that it is the *only* trigger on a mirror, since
      `tmux-update-icons`' `@crew_name`/`@crew_seen` comparison never fires there
      (both stay empty). Plus the liveness caveat: crew + label live, `@pr_*`
      last-known.
- [ ] CLAUDE.md "What the Remote Host Needs on PATH": a row for this feature —
      the remote must run lazytmux's own reflow (what stamps `@window_label_*`)
      and its fan-out harness (for `@crew_*`). It is the one requirement that
      otherwise fails silently on an older remote, with no capability probe.
- [ ] Copy `PLAN.md`/`SPEC.md` to
      `docs/superpowers/plans/2026-09-01-bridge-window-labels.md` and
      `…-design.md`; delete the worktree-root `PLAN.md`, `SPEC.md`,
      `DECOMPOSITION.md` **and `WORKER_TASK.md`** before committing — the last is
      untracked and not in `.gitignore`, so it would otherwise land in the commit.
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` green;
      `shellcheck scripts/tmux-reflow-windows.sh`.
