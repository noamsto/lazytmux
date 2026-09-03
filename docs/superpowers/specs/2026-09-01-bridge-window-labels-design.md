# Spec — bridge carries crew codename and issue/PR label state onto mirror windows (#462)

## Problem

A window viewed through a remote-bridge mirror loses its whole identity. Locally
a fan-out window renders `2: moss 󰊤 #460 <title>  <PR badge>` with the colour
`dispatch` gave it. Through the mirror the same window renders only the raw
remote window name — no crew codename, no `@crew_color` tint, no issue id, no PR
badge, no title. A fan-out run watched from another machine is unreadable.

The cause is deliberate, and only half-right. `scripts/tmux-reflow-windows.sh`'s
`@bridge_win` branch blanks `win_id`, `win_pr` and `win_crew` and `continue`s
past `build_window_label`, because the *local* window options on a mirror window
describe the launcher's cwd, not the remote window. Rendering those would be
wrong. The missing third option is to carry the *remote* window's own label
state across the bridge and render that. The same reasoning left
`picker/main.go`'s `collectWindows` and `picker/statusline`'s `sessionSegment`
blank on mirrors.

## Goal

A mirror window renders the identity its remote carries — crew codename in the
remote's colour, issue id, issue title, PR badge — in every place a local window
renders it. A mirror whose remote carries none of it renders the remote window
name. A local window is unaffected.

---

## R1 — The daemon is the sole producer

The bridge daemon polls the remote's *window* options and stamps them onto the
local mirror windows it already owns. This is the Remote Agent Status mechanism
(`picker/remotebridge/daemon/agentstatus.go`) one level up — window options
rather than pane options.

- Polling runs through `rt` on the **main loop only**. `rt` reads the control
  stream, which has one consumer; a round-trip from anywhere else takes a later
  ordinal and the reply reader discards the batch hunting for it.
- One `list-windows` round-trip per pass, floored at 1s.
- Mirror windows are addressed by **tmux window id** (`@N`) through the
  registry, never `<sess>:<index>` — `renumber-windows` is on (#411).
- An **unchanged row is not rewritten**, and within a changed row only the
  options whose values differ are written. A re-stamp is a fork per window per
  tick. One window's changed options are issued as a single tmux argv command
  sequence (`set … ';' set … ';' …`), the form `tmux-reflow-windows` already
  uses, so a first pass over N windows costs N forks rather than 9N.
- **Teardown deletes what it wrote** (`clear()` from the `teardown` closure),
  while the mirror windows still exist. Unlike the agent shipper's — whose files
  live under `/tmp` and outlive tmux — this `clear` is near-vacuous in
  production, since `teardown` ends in `kill-session` and window options die with
  the session. It is kept for symmetry and for the paths where the kill fails,
  and it is therefore provable only by unit test, never end to end.
- A pass that changed at least one window ends in exactly **one** forced
  `cfg.reflow()`, **after** all of that pass's option writes. A label change
  alters no window count, so reflow's `count:width:height` cache would otherwise
  skip it — the `@window_bridge_name` precedent. The forced reflow is
  `run-shell -b`, so it costs one local fork that returns immediately.

### R1.1 — The stream is not a sufficient wake-up; add a coarse tick

`agentstatus.go` justifies having no timer with "an agent that changes state
redraws its pane first, which is what wakes that loop". **That argument does not
transfer.** A window-option change on the remote produces no control-stream
traffic at all: a `@crew_name` stamped by `dispatch` just after the
`%window-add`, a `@pr_*` refresh, a remote reflow re-stamping `@window_label_*`
— none emit `%output` or a notification. The main loop blocks in `nextLine`, so
on a quiet mirror window the label would be stale indefinitely, not for 1s. The
tree already concedes this: the existing agent-status integration test has to
manufacture remote output in a loop to make the poll run at all.

The main loop therefore selects over the pump's line channel **and** a coarse
ticker (5s). `ctlPump` is channel-backed precisely so a consumer can select over
the stream alongside other events.

Two invariants make that select safe, and both are silent and fatal if missed:

1. **Every line taken off `pump.lines` must still pass through `claimSeq`**
   before it is dispatched. `nextLine` is what guarantees that today; its doc is
   explicit that the count must stay exact. A bare `case l := <-pump.lines:` that
   skips it desynchronises the ordinal state permanently, and *every later
   round-trip in the process* fails to recognise its own reply. `waitHellos`
   already selects on this channel and calls `claimSeq` explicitly — follow it.
2. **The receive must be `l, ok := <-pump.lines`, with `!ok` leaving the loop.**
   The pump closes the channel on stream EOF. A `break` inside a `select` breaks
   the select, not the `for`, so this needs a label or a flag; without it, EOF
   spins the loop at 100% CPU on zero-value lines forever.

The ticker is declared alongside the shipper — above the `teardown` closure, so
the early-return paths can stop it — and stopped there with a nil guard.

`settle()`, `agents.poll` and `reseedDropped` are already idempotent when there
is nothing to do and already run once per pass, so a tick simply buys an extra
pass; and a round-trip can only start at the top of the loop, so a tick can
never interleave with a reply read.

Cost on a fully idle bridge: one label round-trip and one agent round-trip per
5s, against the two per second the loop already runs whenever anything moves.
The agent poll gains the same tick, which also fixes its own quiet-window
staleness — a knowing behaviour change to a shipped subsystem, so
`agentstatus.go`'s now-stale "there is no timer" comment is corrected in the same
change rather than left to mislead.

**5s is the worst-case first-appearance latency for a codename**, which is the
number that matters: a remote *rename* emits `%window-renamed` and wakes the loop
anyway, so renames stay sub-second under the 1s floor; a `@crew_name` stamped
shortly after `%window-add` emits nothing and is what the tick is for. The 1s
floor remains the cadence on a busy stream; the tick is only the ceiling.

## R2 — A daemon-owned namespace, not the real option names

**The daemon writes only `@bridge_*` names.** It never writes `@crew_*`,
`@window_label_*` or `@pr_*`. The reasons are concrete:

1. **`@window_label_*` and `@window_pr_plain` already have a local writer.**
   `tmux-reflow-windows` stamps them on *every* window of the mirror session,
   mirrors included (`indices` is appended before the `@bridge_win` branch).
   Same-name would be a two-writer race the daemon loses on every reflow pass.
2. **A mirror window carries stale local `@issue_*` / `@pr_*` / `@branch`
   residue.** `stampMirrorWindow` sets `@bridge_win` only *after*
   `createMirrorWindow`, so the `after-new-window` hook has already fired against
   the launcher's cwd. `tmux-issue-stamp` and `tmux-pr-enrich` then opt out on
   `@bridge_win`, so nothing overwrites the residue either. Reading real names on
   a mirror would surface the launcher's repo — the bug being fixed.
3. Only a namespace makes the daemon the single writer of what it ships.

**Reflow's own outputs keep their real names on mirror windows.** Once the
bridge branch feeds the shared arrays (R5.1), `tmux-reflow-windows` writes
`@window_label_id`, `@window_label_id_disp`, `@window_label_rest_short/_long`,
`@window_label_disp`, `@window_pr_plain`, `@window_pr_disp` and
`@window_crew_disp` on a mirror window carrying bridge-derived values. That is
correct and intended: reflow is their sole writer. The rule is about the
*daemon*, not about which names may ever hold a bridge-derived value.

### R2.1 — Every render site reads `@bridge_*` directly, not reflow's copies

The grid must go through reflow, which owns the column math. **The picker and
status line 0 must not.** Routing them through reflow's stamps was considered
and rejected on two counts:

- **Reflow is client-gated.** It resolves `WIDTH` from
  `display-message -t <session> -p '#{client_width}'` and exits early when that
  is not a positive integer — which is exactly a mirror session with no client
  attached. `prefix + w` lists windows across *all* sessions, so a detached
  mirror's row would show the crew badge (read bridge-direct) with no id and no
  title. That is a partial failure of acceptance 1 in an ordinary workflow, and a
  robustness regression against today, where the row's identity comes
  daemon-direct from `@window_bridge_name`.
- **Reflow's copies speak the wrong `#` dialect.** On a label-less mirror the
  bridge branch copies the `#`-doubled `@window_bridge_name` into
  `@window_label_rest_*`; a consumer that renders its own rows (the picker does)
  would draw `feat##1` for a remote window named `feat#1`. The `@bridge_*` copies
  are raw and carry no such ambiguity.

Reading bridge-direct also makes every value the picker and statusline consume
daemon-sanitized, which is what keeps their `|`-delimited field-count guards
honest (R4).

## R3 — What crosses

| remote source | bridge copy |
|---|---|
| `@crew_name` | `@bridge_crew_name` |
| `@crew_color` | `@bridge_crew_color` |
| `@pr_number` | `@bridge_pr_number` |
| `@pr_state` | `@bridge_pr_state` |
| `@pr_check_state` | `@bridge_pr_check_state` |
| `@pr_mergeable` | `@bridge_pr_mergeable` |
| `@window_pr_plain` | `@bridge_pr_plain` |
| `@window_label_id` | `@bridge_label_id` |
| `@window_label_rest_long` | `@bridge_label_rest_long` |

Nine values. `@window_label_rest_short` is **not** carried — see R4.1; the
reflow bridge arm uses the long rest for both modes, and the clipping loop
truncates to the column either way.

`@pr_draft` is deliberately absent: the draft glyph is already baked into
`@window_pr_plain` by the remote's own `build_window_label`, and no format reads
`@pr_draft` live.

An empty (or validation-failing) remote value **unsets** the local option rather
than setting it to `""`, so a mirror whose remote carries nothing is
option-free. That live-pass unset — not `clear()` — is the load-bearing one.

## R4 — Sanitization before anything crosses

Every value passes `stripWindowName` (`picker/remotebridge/daemon/windows.go`):
drop `|`, CR, LF, control bytes and DEL, then strip `#[...]` markup to a fixed
point. That function is already separate from `sanitizeWindowName`'s
`#`-doubling, so it is reusable as-is.

### R4.1 — One free-form field, and it goes last

Sanitization runs *after* the reply body is split, so it cannot repair a `|`
that shifted the fields. The framing must prevent the shift.

`@window_label_rest_long` is genuinely free-form and can contain `|`:
`sanitize_title` strips CR, LF, ESC, `'` and `#` but not `|`; git permits `|` in
a ref name; and `@window_task` (a possible source of the rest) is documented as
"printables, whitespace squeezed". Every other carried value is token-shaped —
`@crew_name` is a codename, `@window_label_id` is `"<glyph> <id>"`,
`@window_pr_plain` is `" <glyph> #<n>"`, the rest are enum or numeric.

So the read format carries **exactly one** free-form field and puts it **last**,
and the parser uses `SplitN(line, "|", 10)` — the `agentStatusFormat` idiom
exactly. Dropping `@window_label_rest_short` is what makes one free-form field
sufficient.

### R4.2 — No `#`-doubling

Deliberately unlike `@window_bridge_name`. `format_draw` specialises only `#[`
and `##`; a lone `#` is inert. The local label options already store raw `#`
(`@window_pr_plain` is `" 󰗡 #123"`), so storing the bridge copies raw gives
byte-identical treatment to the local values and no consumer needs a decode
step. The residual exposure — a remote label containing a literal `##` renders
one `#` short and is over-measured by a cell — is exactly what a local window
already does with such a title, so it is not a new defect.

`@window_bridge_name` keeps its own doubled convention untouched, which means
the reflow bridge arm carries two `#` conventions a few lines apart and must
measure piecewise (R6.2).

### R4.3 — Validation

Failure is treated as empty:

- `@bridge_pr_state`, `@bridge_pr_check_state`, `@bridge_pr_mergeable`: `^[a-z]+$`
- `@bridge_pr_number`: `^[0-9]+$` (a remote `none` maps to empty)
- `@bridge_crew_color`: `#RRGGBB` (case-insensitive — `ansiFg` accepts either,
  and a lowercase-only regex would silently drop an uppercase colour to the
  mauve fallback), `colour<0-255>`, or a bare lowercase tmux colour name
  `^[a-z]+$`. It is interpolated into `#[fg=…]` by two format
  strings and into ANSI by the picker's `ansiFgTmux`, so it must never be
  markup. (`ansiFgTmux` renders only the first two forms; a bare name renders
  untinted in the picker and tinted in tmux, exactly as a local `@crew_color`
  does today.)

Free-form fields are length-capped — `@bridge_crew_name` at 24 runes, the label
and PR text fields at 120 — so one pathological remote value cannot dominate a
column.

## R5 — Render sites

### R5.1 Reflow (`scripts/tmux-reflow-windows.sh`)

The `@bridge_win` branch stops blanking. It sets the same segment variables the
local path sets — `win_id`, `win_rest_short`, `win_rest_long`, `win_pr`,
`win_crew` — from the bridge copies, then falls through to the *same*
measurement code, so every width input is filled identically on both paths (R6).

`win_rest_short` is **derived locally** rather than carried: empty when the
bridge id is non-empty, else equal to the long rest. That reproduces
`build_window_label short`'s own behaviour for an issue window (it sets
`REPLY_REST` only in the `long` arm), which is what keeps the short/long ladder
in `reflow_pick_layout` a real compaction lever on a fan-out session where every
mirror carries an issue; setting `short = long` unconditionally would make
`total_short == total_long` and send a mirror-heavy narrow session multiline
earlier than the equivalent local one. This is why R4.1 can drop
`@window_label_rest_short` without cost.

Reflow's own `FMT` gains only the **four** bridge fields the shell consumes
(`@bridge_label_id`, `@bridge_label_rest_long`, `@bridge_pr_plain`,
`@bridge_crew_name`), inserted before `#{window_name}` so the free-form tail is
unchanged. The other five are live template reads (R5.2); naming them in the
`read -r` list would be five `SC2034` warnings and a failed lint gate.

The `${bname:-$wname}` fallback fires only when **both** the bridge id and the
bridge rest are empty. Firing it on an empty id alone would throw away the
rest-only identity a remote branch with no detected issue has.

The crew segment keeps its folded trailing space (`"${crew} "`) on the bridge arm
too — the separator space is inside the measured segment, which is what makes
`crew_colw` exact.

### R5.2 The two window formats

Both templates read some options *live* at render time; only those reads change,
each becoming `#{?#{@bridge_win},#{@bridge_X},#{@X}}`. The two are **not**
symmetric:

- Reflow's `ENTRY` — **5 distinct option names**, appearing at **9 textual
  sites**: `@crew_color` ×2 in `CREW`; `@pr_number` ×2, `@pr_state` ×2,
  `@pr_check_state` ×2, `@pr_mergeable` ×1 in `PRCOLOR`. `ENTRY` does **not**
  read `@crew_name`; the badge *text* is the reflow-stamped `@window_crew_disp`.
- The global single-line `status-format[1]` in `config/tmux.conf.nix` — **6
  names** at **11 sites**: those five plus `@crew_name` ×2 (gate and text).

The counts are stated both ways deliberately: an implementer who substitutes
"five" and stops leaves four raw reads live on the mirror path. Building each
gated expression once (a shell helper; a Nix `let` binding) makes the name the
unit and the site count a consequence.

**Comma escaping.** The surrounding `#[fg=…#,bg=…]` keeps its existing `#,`
escapes, but the nested `#{?…,…,…}` must **not** be `#,`-escaped: `format_expand`
resolves the conditional before `format_draw` parses `#[…]`, and its argument
splitter tracks `#{`/`}` nesting, so those commas are already protected. The two
conventions now sit inside one another, and nothing in the build checks this.

Gate on `@bridge_win`, never on first-non-empty: a mirror carries the local
residue of R2.2.

### R5.3 The picker (`picker/main.go`)

Reads the nine bridge copies plus `@bridge_win` as the gate, and on a
`@bridge_win` row substitutes them for `crewName`, `crewColor`, `labelID`,
`labelRest`, `prPlain`, `prState`, `prCheck`, `prMergeable`. All nine are
token-shaped or last-field-safe on the daemon's side and `|`-free by
construction, so the picker's strict field-count guard gains no new exposure.

`picker/tui.go` is not touched, so its
`labelID → branch → bridgeName → name` chain must be steered by the data:

1. **Clear `branch` and skip the git fallback on a mirror row.** `branch` is
   otherwise never empty — from the `@branch` residue of R2.2, or from
   `collectWindows`' own `git -C <pane_current_path> branch --show-current`
   fallback, whose cwd is the launcher's repo. It can then beat `bridgeName` in
   the chain and render the launcher's branch (it does not when the launcher sits
   on `main`/`master`, which the chain excludes). This is a live bug today, not
   only after this change. Clearing `branch` is exactly what *arms* the git
   fallback, so the skip needs its own carrier on the row — the two edits sit in
   different functions.
2. **Give the bridge rest somewhere to land when the bridge id is empty.** A
   remote window on a branch with no detected issue has an empty
   `@bridge_label_id` and a non-empty `@bridge_label_rest_long`. With `labelID`
   empty the chain skips `labelRest`, so the row would fall through to the window
   name. On a mirror row, `bridgeName` is therefore taken from
   `@bridge_label_rest_long` when the bridge *id* is empty and that bridge *copy*
   is non-empty. Sourcing it from the bridge copy rather than from reflow's is
   what keeps the `decodeBridgeName(@window_bridge_name)` fallback in place for a
   truly label-less mirror, and so keeps acceptance 2 true for a remote window
   name containing `#`.

### R5.4 Status line 0 (`picker/statusline/main.go`)

`sessionSegment`'s `@bridge_win` branch keeps the host pill, then:

- the crew badge from `@bridge_crew_name`/`@bridge_crew_color` (`thmMauve`
  fallback, identical to the local badge);
- when `@bridge_label_id` is non-empty, the issue block: the id **already
  contains the provider glyph** (`REPLY_ID` is `"<provider_icon> <id>"`), so it
  must be rendered as-is — prepending the local block's glyph would draw
  `󰊤 󰊤 #460`. The long rest follows in the local issue-block colours;
- when the id is empty and `@bridge_label_rest_long` is not, the branch glyph
  plus that rest, mirroring the local `else` arm;
- when both are empty, exactly today's output — the host pill alone. The gate is
  the emptiness of the bridge *copies*, which are genuinely unset on a remote
  that runs no reflow; reflow's own `@window_label_rest_long` would never be
  empty and so could not gate this.

The four new `volatileFields` entries are all daemon-sanitized and `|`-free, so
`fetchVolatile`'s fail-closed field-count check gains no new way to freeze line 0
— which it would have done had the free-form `@window_label_rest_long` (a
possible `@window_task`, printables including `|`) been read instead, on **local**
windows, breaking acceptance 3.

The second `bridgeWin` branch in that file — the one suppressing the dir segment
— is correct as-is and stays.

**No PR badge on line 0, deliberately.** `picker/statusline` renders none for
*any* window today; there is not one `@pr_*` reference in the package, and the
binary is passed no colour for the `closed` state. Adding one only for mirrors
would give a mirror an element local windows lack — the opposite of the parity
this change is for. This overrides both `WORKER_TASK.md`'s acceptance ("PR
badge … status line 0") and `DECOMPOSITION.md` §6's
`statusline-bridge-labels` one-liner ("a state-colored PR badge from the new
volatile fields"), and is called out in the PR. The PR badge appears where it
already exists: the reflowed grid and the picker.

## R6 — Column width math stays correct

The safeguard is uniformity, and uniformity means **every** width input.
`scripts/lib-reflow.sh` must not change; that it does not is the
width-correctness argument, and it only holds once the list below is complete.

### R6.1 The full set the bridge branch must fill

Filling `win_id`/`win_id_dw` from a bridge label id *without* the rest of this
list is worse than today:

- `win_short_dw` / `win_long_dw` — the width of the **composed** label
  (`win_id + win_rest_*`), which is what `build_window_label`'s `REPLY` is on the
  local path. The fit math computes `rest_short = win_short_dw - win_id_dw`;
  today the branch sets `win_short_dw = measure(name)` and `win_id_dw = 0`,
  self-consistent. A non-zero `win_id_dw` against an id-less `win_short_dw` makes
  `rest` negative → `want < floor` → a negative `demand[c]` in
  `reflow_fit_columns` → a column sized **below its own floor**. And because
  `total_demand` is summed and then divided with no zero guard — its positivity
  comes only from the caller invoking that arm when `sum_w > budget` — negative
  demands can cancel to zero and produce a bash division-by-zero error outright.
- `pr_colw` — the shared PR column maximum. The branch never raises it today, so
  a bridged PR badge would be padded into a zero-width column and overflow the
  row.
- `crew_colw` — the shared badge maximum. The `CREW` fragment is emitted **only**
  when `crew_colw > 0`. On a mirror-only session — the exact reported scenario —
  it stays 0 and the codename never renders at all. Raising it is what makes
  acceptance 1 pass.
- `win_id_dw`, `win_pr_dw`, `win_crew_dw`, `win_rest_short`, `win_rest_long`,
  `win_short` — set on both paths.

`win_zoom_dw` / `has_zoom` are set above the branch and stay put;
`win_id_disp` / `win_crew_disp` / `win_disp` / `win_pr_disp` are assigned only in
the later clipping loop, which is untouched.

Moving the measurement, the `pr_colw`/`crew_colw` maxima and the `win_short`
composition below a shared `if/else` covers all of them, and is **required**
rather than a tidiness preference.

### R6.2 Two `#` conventions in one branch, so measure piecewise

The `@window_bridge_name` fallback is `#`-doubled and must be *measured*
collapsed (`${v//##/#}`) while being *stored* doubled, because `format_draw`
collapses it at render. The new `@bridge_*` values are raw and must be measured
as-is. A composed label can mix the two — a bridge id with a fallback rest — so
the branch carries a **piecewise** measure string, collapsing only the segment
that came from `@window_bridge_name`. The local path sets its measure string
equal to its stored value.

### R6.3 Downstream is untouched

The floor/want lists, `reflow_fit_columns`, and the clipping loop are unchanged,
so a mirror inherits the same guarantees: badge dropped first, id clipped last,
label never wider than its column.

## R7 — Zero blast radius off the bridge

On a window with no `@bridge_win`, every changed format evaluates to exactly the
expression it evaluates to today, no shell array takes a different value, and
the picker/statusline substitutions do not run. The one intentional change to a
non-mirror code path is the picker's git-branch fallback, skipped only for
`@bridge_win` rows.

## R8 — Verification

**Boundary amendment.** `DECOMPOSITION.md`'s `may-touch` lists are allowlists and
none of them contains `tests/`, while `docs-and-gates` — the component nominally
running the repro — is declared code-free. Acceptance 5 is therefore
unsatisfiable as bounded. This spec amends the boundaries:
`daemon-label-shipper` gains `tests/remote-m2-integration.bats`;
`reflow-bridge-labels` gains `tests/reflow-fanout.bats` (`tests/reflow.bats`,
the pure `lib-reflow.sh` math, stays untouched as required);
`picker-bridge-labels` gains `picker/main_test.go`; and `daemon-label-shipper`
gains the `agentstatus.go` comment correction R1.1 requires.

- **Daemon unit tests**: parse/sanitize/validate; the diffing `apply` (a repeated
  identical pass issues zero tmux calls; a changed field writes only that option;
  an emptied field emits `-u`; a departed window is forgotten); and `clear`,
  which is only ever provable here.
- **Daemon end to end** (`tests/remote-m2-integration.bats`, offline
  `--test-local`): stamp the source window by hand, assert the mirror carries the
  `@bridge_*` copies, and assert a `|`- and `#[…]`-bearing value arrives
  sanitized. The bare-mirror half needs a *second* remote window created after
  the bridge is up, since the harness waits on the first and that is the one the
  test stamps. Set vs unset is distinguished by listing options, not by
  `show-options -qv`, which returns empty for both. No `clear()` assertion —
  teardown kills the session, so one would pass vacuously.
- **Grid** (`tests/reflow-fanout.bats`, which already drives the real reflow
  against a private tmux server and already has a `@bridge_win` case): a mirror
  with a full bridge identity renders the badge and the id; a bare mirror renders
  the remote name and adds no columns; a narrow client drops the badge before the
  id and overflows no column (acceptance 2 and 4).
- **Picker / statusline**: Go unit tests over a pure, extracted function —
  `collectWindows` takes no arguments and execs `tmux` and `git` inline, and
  declares `winKey`/`winInfo` in its own body, so nothing in it is reachable from
  a test until the row parse is lifted out. The picker's bare-mirror fixture must
  use a window name containing a literal `#`, or it cannot catch the doubling bug
  R5.3(2) exists to prevent.
- **Gates**: `nix build .#default`, `nix flake check`, `nix build .#lint`. Note
  that `Run`'s select/ticker has no Go coverage — `pump` is built inside `Run`
  from `cfg.Ctl` with no seam — so bats is its only net.

## Non-goals

- **Making the remote's `@pr_*` live.** A control-mode client renders no status
  line, so the remote's 1s `#()` pollers do not run for a session whose only
  client is the bridge. `@crew_name`/`@crew_color` (stamped by `dispatch` at
  creation) and `@window_label_*` (stamped by the remote's own reflow hooks,
  which do run — the daemon gives its control client a size, #455) are live;
  `@pr_*` refreshes on the remote only while a real client is attached there, so
  it crosses as last-known. Stated in CLAUDE.md and the PR; not fixed here.
- A PR badge on status line 0 (R5.4).
- Carrying `@issue_*` raw inputs, or `@window_label_rest_short` (R3/R4.1) — the
  bridge ships the remote's *built* label segments; the remote already ran
  `build_window_label`.
- Rebuilding mirror windows, changing `@window_bridge_name`'s escaping, or
  touching `picker/zoxide.go`, `picker/tui.go`'s `^o` handler, or
  `picker/remotepick.go` (sibling workers own those).

## Acceptance

1. A mirror window whose remote carries a crew codename renders that codename,
   tinted by the remote's `@crew_color`, plus the issue id and title — in the
   reflowed grid, the window picker and status line 0 — and the PR badge in the
   grid and the picker (R5.4).
2. A mirror window whose remote carries none of it renders the remote window
   name (including a name containing `#`), with no new blank columns. Not
   "unchanged from today": today such a row can render the *launcher's* branch
   in the picker, which R5.3(1) fixes deliberately.
3. A local window is unchanged.
4. Grid columns still fit at a client width narrow enough to force clipping.
5. R8's tests exist and pass.
6. `nix build .#default`, `nix flake check`, `nix build .#lint` green; the plan
   is committed under `docs/superpowers/plans/`; CLAUDE.md documents the
   mechanism, the liveness caveat, that the daemon's own forced reflow is the
   only trigger on a mirror (`tmux-update-icons`' `@crew_name`/`@crew_seen`
   trigger never fires there, since both stay empty), and the remote-PATH
   requirement — the remote must run lazytmux's own reflow, which is what stamps
   `@window_label_*`.
