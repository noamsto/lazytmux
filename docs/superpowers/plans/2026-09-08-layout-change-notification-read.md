# Take the layout and zoom flag from `%layout-change`

Closes #570. Design: `docs/superpowers/specs/2026-09-08-layout-change-notification-read-design.md`
(read it first — the gate order, the read-routing predicates and the reasons
are there; this plan only sequences the work).

## Shape

Two code commits plus docs, so the one belief the change introduces is
revertable alone:

- **Commit A** — parse the notification; `reconcileLayoutFrom` with gates 1-5:
  a zero-round-trip no-op for a line that reports nothing new, and read-first
  routing for everything else. Nothing is *applied* from the notification.
- **Commit B** — gate 6: a geometry-only reshape (same pane set/order, same
  floats, no local floats, flag off) enters the pass loop from the
  notification's layout instead of a leading read.
- **Commit C** — m2 bats and `CLAUDE.md`.

Spec, this plan and the coalesce comment (step 4) ride in commit A. Never `git add -A`: `WORKER_TASK.md` is
untracked at the root and must not land.

## Files

| file | change |
| --- | --- |
| `picker/remotebridge/daemon/layoutnotice.go` (new) | `layoutNotice` parse of a `%layout-change` line |
| `picker/remotebridge/daemon/layoutnotice_test.go` (new) | parse table |
| `picker/remotebridge/daemon/reconcile.go` | split `reconcileLayout` into read entry + `reconcileSnapshot`; add `reconcileLayoutFrom` |
| `picker/remotebridge/daemon/reconcilenotice_test.go` (new) | gate tests, scripted round-trips |
| `picker/remotebridge/daemon/daemon.go` | `dispatch` `LayoutChange` → `reconcileLayoutFrom`; `coalesceLayoutChanges` doc comment |
| `picker/remotebridge/daemon/daemon_test.go` | coalesce test asserts `Args` survive |
| `tests/remote-m2-integration.bats` | four new `@test`s |
| `CLAUDE.md` | zoom bullet under Key Conventions |

Everything after the read site in `reconcileLayout` — the pass loop,
`applyLayout`, `reconcileFloats`, the trailing re-read — is not edited.

## Steps

- [ ] **Step 1: `layoutNotice` parser.** New `layoutnotice.go` in package
  `daemon`:

  ```go
  // layoutNotice is what a %layout-change line carries that readLayout would
  // otherwise fetch: the unzoomed layout string and the zoom flag.
  type layoutNotice struct {
      layout string
      zoomed bool
  }
  func parseLayoutNotice(l controlmode.Line) (layoutNotice, bool)
  ```

  Positional validation per the spec: `len(l.Args)` must be 3 or 4;
  `l.Args[1]` and `l.Args[2]` must be layout-shaped (`len ≥ 6`, four lowercase
  hex digits, then `,`); with 4 fields `l.Args[3]` must be non-empty and every
  byte in `#!~*-MOZ` (the alphabet of `window_printable_flags`,
  `window.c:1296-1315` in the pinned tree — confirm it there rather than
  copying this line); `zoomed` is `strings.ContainsRune(l.Args[3], 'Z')` and
  false with 3 fields. Anything else → `ok=false`. The doc comment states the
  fail-safe direction: an unrecognised flag character or an unexpected shape
  yields `ok=false` and the caller falls back to the read, so a future tmux
  flag costs one round-trip, never a wrong answer. Do **not** call
  `ParseLayout` here — the caller does, once, and treats its error as
  `ok=false` too.

  `layoutnotice_test.go`, table-driven, using `controlmode.ParseLine` on real
  line text so the test exercises `parse.go`'s field split as well:
  `%layout-change @0 c195,80x24,0,0[80x12,0,0,0,80x11,0,13,1] b25e,80x24,0,0,1 *Z`
  → zoomed; same with `*` → not zoomed; same with `##Z` (a 3.1 `window_flags`
  spelling; the pinned tree cannot confirm it existed, the test only pins that
  the alphabet accepts it) → zoomed; the 3-field form (trailing space, flags
  empty) → not zoomed, ok; 2 fields → !ok; a shifted 3-field line whose third
  field is `*Z` → !ok; a 4th field `bogus` → !ok. Run
  `go test ./remotebridge/daemon -run LayoutNotice` from `picker/`.

- [ ] **Step 2: split `reconcileLayout` without changing behaviour.** In
  `reconcile.go`, move the body from the line after the `readLayout` error
  check (`// Same tiled shape, same zoom state ...`) to the end of the function
  into

  ```go
  func reconcileSnapshot(cfg Config, w *mirrorWindow, L controlmode.Layout, remoteActive string, zoomed bool,
      send func(string), router *Router, waitHellos helloWaiter, cst *ctlState, cv *converger, rt roundTrip) (retire bool)
  ```

  verbatim — same locals, same comments, `target` recomputed inside from
  `w.remoteID` because the trailing re-read needs it. Split the function doc
  too: the paragraphs about the trailing re-read loop and the retire contract
  (`reconcile.go:24-40`) describe the moved body and go with
  `reconcileSnapshot`; `reconcileLayout` keeps one short doc — read ground
  truth for the window, then delegate. `reconcileLayout` becomes: read,
  log-and-return-false on error, `return reconcileSnapshot(...)`. Every
  existing caller and every existing test is untouched. Gate:
  `go test ./remotebridge/daemon` green with no test edits. Do not reorder,
  rename or "tidy" anything inside the moved body: this step must diff as a
  pure move.

- [ ] **Step 3: `reconcileLayoutFrom` with gates 1-5 (commit A).** In
  `reconcile.go`, below `reconcileLayout`:

  ```go
  // reconcileLayoutFrom is reconcileLayout for a %layout-change notification
  // ... (doc: the notification carries #{window_layout} and the zoom flag;
  // which lines are answered from it and why the rest still read — see the
  // spec's gate list; keep the doc to the WHY, the predicates say the what)
  func reconcileLayoutFrom(cfg Config, w *mirrorWindow, l controlmode.Line, send func(string), router *Router,
      waitHellos helloWaiter, cst *ctlState, cv *converger, rt roundTrip) (retire bool)
  ```

  Body, in this order, each miss falling through to `return
  reconcileLayout(cfg, w, send, router, waitHellos, cst, cv, rt)`:

  1. `n, ok := parseLayoutNotice(l)`; `L, err := controlmode.ParseLayout(n.layout)` — `!ok || err != nil` → read.
  2. `!slices.Equal(RemotePaneOrder(L), w.remotePanes)` → read (gate 1).
  3. `!noFloatWork(w, L)` → read (gate 2).
  4. `L.Raw == w.layout` → `local, known := localZoomed(cfg, w.localWin)`;
     `!known` → read; `n.zoomed == local` → `return false` (**the no-op**: no
     stream write, no `LocalTmux`); else → read (gate 3).
  5. otherwise (layout changed) → read. (Commit B replaces this line with
     gate 4/5/6.)

  Wire `dispatch` in `daemon.go` (`case controlmode.LayoutChange:`) to call
  `reconcileLayoutFrom(cfg, mw, l, ...)` in place of `reconcileLayout`. The
  `settle` intents path, `reconcileWindows`, repair and `retryFailedShapes`
  keep calling `reconcileLayout`.

  Tests in new `reconcilenotice_test.go`. Fixtures copy
  `reconcilededup_test.go:15-21` — `remotePanes` **and** `localPanes` set (the
  `-Z` toggle goes through `localPaneAt`, which misses silently on an empty
  list), `layout` set. Round-trips via `scriptedRT` (its `sent` buffer is the
  assertion surface for "was a read issued"); a `Config` whose `LocalTmux`
  records argv and whose `LocalTmuxOut` answers by argv — `#{window_zoomed_flag}`
  → the mirror's zoom (`"0\n"`/`"1\n"`), anything else → `""` — and counts
  calls. A helper `noticeLine(win, layout, visible, flags string)
  controlmode.Line` builds the line text and runs `controlmode.ParseLine` on
  it, so every test goes through the real field split; pass `flags == ""` to
  produce the 3-field form.

  - `TestNoticeNoOpWritesNothing`: `w.layout == layout`, flags `*`, local
    `"0\n"` → `sent.Len() == 0`, zero `LocalTmux` calls, exactly one
    `LocalTmuxOut` call. Second case flags `*Z`, local `"1\n"` → same. Run
    the cases as `t.Run` sub-tests with a fresh fixture and counter each.
    **Negative control, in the test (its own sub-test):** feed the identical
    fixture and script to `reconcileLayout` (the read-first entry) and assert
    `sent.Len() > 0` — today's path cannot keep the stream silent, which is
    what makes the first assertion bite (its dedup forks `localZoomed` again,
    hence the per-sub-test counter).
  - `TestNoticeZoomOnReads`: layout equal, flags `*Z`, local `"0\n"`; script
    = readLayout reply `layout %3 1` + trailing re-read `layout %3 1` →
    `resize-pane -Z` appears in `LocalTmux` calls (the #413 case arriving as a
    notification), and `sent` contains `window_zoomed_flag`.
  - `TestNoticeUnzoomTransientReads`: layout equal, flags `*`, local `"1\n"`;
    script = read reply `layout %3 1` + trailing `layout %3 1` (remote still
    zoomed after the bracket popped) → `sent` contains `window_zoomed_flag`,
    **no** `resize-pane -Z` in calls, no `select-layout`.
  - `TestNoticeUnknownLocalZoomReads`: `LocalTmuxOut` returns an error →
    `sent` contains `window_zoomed_flag`. The script is **empty** on purpose:
    `one()` writes the command before `readLayout` fails on EOF and returns,
    and the assertion is only that the read was issued — do not add reply
    blocks, nothing would consume them.
  - `TestNoticePaneSetChangeReads`: notification layout has a third pane;
    script read reply = the two-pane `layout %3 0` (remote already back) →
    `sent` contains `window_zoomed_flag`; no `split-window` in calls; and no
    fork preceded the read (the pure gate fired first) — have the fake
    `t.Fatal` if it is called while `sent.Len() == 0`; the dedup's fork after
    the read is legitimate.
  - `TestNoticeFloatChangeReads`: fixture `shapedMirror(t)`
    (`reconcilelayout_test.go:57` — the 2-pane `%0`/`%1` mirror already
    carrying `tiledLayout`), notification `tiledFloatLayout`: same pane order,
    same `Raw`, one float more — so this is the one test where gate 2 must
    fire before gate 3 would have declared a no-op. Read reply `tiledLayout
    %0 0`; same "no fork before the read" fatal.
  - `TestNoticeGeometryChangeReadsUntilGate6` (deleted in step 5): a same-set
    geometry change → `sent` contains `window_zoomed_flag`.

  Run `go test ./remotebridge/daemon` from `picker/`; then commit A with the
  spec and this plan: `git add picker/remotebridge/daemon/layoutnotice.go
  picker/remotebridge/daemon/layoutnotice_test.go
  picker/remotebridge/daemon/reconcile.go
  picker/remotebridge/daemon/reconcilenotice_test.go
  picker/remotebridge/daemon/daemon.go docs/superpowers/specs/2026-09-08-layout-change-notification-read-design.md
  docs/superpowers/plans/2026-09-08-layout-change-notification-read.md`,
  message `feat(bridge): answer a no-op %layout-change from the notification
  itself (#570)`. Commit from inside the devshell (direnv is loaded in this
  worktree; `.pre-commit-config.yaml` is present) so the hooks run.

- [ ] **Step 4: coalesce comment and test.** `daemon.go`
  `coalesceLayoutChanges` doc: replace the "reconcileLayout always re-reads
  the remote's current layout fresh" sentence with the truth after this
  change — the surviving line's `Args` are what `reconcileLayoutFrom` reads,
  so keeping the **last** per window is what hands the gate the newest
  snapshot; the live path stays one-line-at-a-time and relies on the trailing
  re-read. `daemon_test.go` `TestCoalesceLayoutChangesKeepsLastPerWindow`:
  give the `@1` lines distinct multi-field `Args` (`{"@1", "first-layout",
  "first-visible", "*"}` / `{"@1", "last-layout", "last-visible", "*Z"}`) and
  keep the `DeepEqual` — it now pins that the last line's `Args` survive
  intact; rewrite that test's doc comment (`daemon_test.go:171-174`) the same
  way. The sentence "reconcileLayout runs on a %layout-change, a coalesced
  batch of them, or a reattach" also lives at `reconcilewindows.go:241` and
  `retryshape_test.go:58` (and `CLAUDE.md:772`, handled in step 7): a
  notification now reaches it through `reconcileLayoutFrom` and may return
  before it — adjust each sentence. Part of commit A.

- [ ] **Step 5: gate 6 (commit B).** Replace step 3's final "otherwise →
  read" with:

  ```go
  if n.zoomed || len(w.localFloats) > 0 {
      return reconcileLayout(...)            // gates 4, 5
  }
  return reconcileSnapshot(cfg, w, L, "", false, send, router, waitHellos, cst, cv, rt)   // gate 6
  ```

  The comment carries: the two reasons the spec gives for gates 4 and 5 (a
  zoom+reshape needs the active pane for the toggle and dims; a window holding
  a mirrored float can have it dropped and re-added inside the pass, and the
  re-add's focus-follow needs the active pane); what gate 6 relies on (the
  trailing re-read heals a stale line; no push/pop bracket has a geometry-only
  middle line); and one sentence on the empty active id: with `remoteActive ==
  ""` the in-loop `-Z` toggle cannot fire (`indexOf` misses → `localPaneAt`
  misses), which is correct here because neither side reports a zoom. Say
  plainly what the `localCellsMatch` short-circuit means for a mirror that is
  still zoomed on this path: `select-layout` is skipped and the toggle cannot
  fire, so the mirror would stay zoomed against a flag-off line — and that
  the case is unreached in this tmux, because a zoomed mirror's own
  `#{window_layout}` is the saved tree it last applied (`w.layout`), which a
  changed notification layout never matches unless the window fit alone
  reproduced the new cells, and no emitter produces a flag-off geometry line
  under a zoom (the `resize_window` and `resize-pane` unzoom lines carry the
  unchanged layout and land on gate 3).

  Tests, in `reconcilenotice_test.go`, on the `reconcileordering_test.go:94-131`
  pattern: two sinks registered on a `NewRouter()` via `net.Pipe()` with `go
  io.Copy(io.Discard, peer)` draining each, `scriptedRTRouterW(script, router,
  log)` with an `orderedLog` so stream writes and `LocalTmux` argv land in one
  trace. `LocalTmuxOut` is **not** appended to the log (a naive
  `strings.Count` over one trace would then see the zoom read twice); its fake
  answers by argv — `#{window_zoomed_flag}` → `"0\n"`, recording
  `len(log.entries)` at call time into a slice; `#{window_layout}` (this is
  `localCellsMatch`'s read of the mirror window) → `localShortLayout`
  (`reconcilelayout_test.go:33`) so the #535 short-circuit **misses** and
  `select-layout` is reached. `PaneSeeds` issues,
  per pane in order, one cursor `display-message` then one `capture-pane`
  (`seed.go:86-89`), so a two-pane seed round is four reply blocks: cursor
  `%0`, capture `%0`, cursor `%1`, capture `%1`. Delete
  `TestNoticeGeometryChangeReadsUntilGate6`; add:

  - `TestNoticeGeometryOnlyAppliesFromNotification`: 2-pane `w` with
    `w.layout = tiledLayout` (`4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}`),
    notification layout the same two panes at
    `{100x45,0,0,0,89x45,101,0,1}` under any 4-hex prefix (`ParseLayout`
    never checks the checksum, and the assertion compares `select-layout`'s
    argument to `L.Raw`, which is the notification string verbatim), flags
    `*`, local `"0\n"`. Script = the four seed blocks, then **one** trailing
    read reply with the new layout. Assert: a `select-layout` entry whose
    argument is the new `Raw`; the trace contains `readLayout`'s format
    substring `#{window_layout} #{pane_id} #{window_zoomed_flag}` exactly once
    (the trailing read — the leading one is what gate 6 removes); and every
    recorded `LocalTmuxOut` zoom-read index is greater than
    `log.indexContainingAll("select-layout")` (the pass loop's own
    `localZoomed` comes after it — that one is expected; none may come
    before). **Negative control, run once and paste in the PR:** temporarily
    route gate 6 to `reconcileLayout`, run this test, confirm it goes red
    (the leading read consumes the first seed block, so the failure surfaces
    as a missing `select-layout` rather than as a count of two — report what
    it prints, not a predicted cause); revert.
  - `TestNoticeStaleGeometryHeals`: same fixture, but the trailing re-read
    reports a third geometry `C` (`%0 0` fields as usual); the script then
    carries a second four-block seed round and a second re-read reporting `C`
    (converged). Assert `select-layout` was called with `C.Raw` last and
    `w.layout == C.Raw`. **Negative control, run once and paste in the PR:**
    temporarily set `maxReconcilePasses = 1`, run this test, confirm it fails
    with `w.layout` still at the notification's `Raw`, revert.
  - `TestNoticeZoomedReshapeReads`: flags `*Z`, layout changed, `scriptedRT`
    with an **empty script** (see `TestNoticeUnknownLocalZoomReads`) → `sent`
    contains `window_zoomed_flag`, and no `select-layout` was issued before
    the read (a `LocalTmux` fake that fatals if called while `sent.Len() ==
    0`).
  - `TestNoticeReshapeWithLocalFloatReads`: `w.localFloats = {"%9": "%l9"}`,
    flags `*`, layout changed, empty script → same assertions as the previous
    test.

  Commit B: `feat(bridge): apply a geometry-only %layout-change from the
  notification (#570)`.

- [ ] **Step 6: m2 bats (commit C).** Edits to
  `tests/remote-m2-integration.bats`. All polling loops follow the house
  pattern (bounded `for _ in $(seq …)`, `sleep 0.15`, assert after the loop).
  SRC-side reads inside a measured window must **not** use `display-message`
  — use `list-windows -F` / `list-panes -F` / `show -gv`. Pane bases differ
  across the two servers (SRC `pane-base-index 1`; the daemon forces 0 on
  mirror windows — file header and `pane_map`'s comment), so name content
  targets explicitly: `rem:1.1` ↔ `host-sess:1.0`.

  - **Fold the zoom-on leg into the existing test at `:1164`** ("ctl zoom
    zooms the REMOTE window and the mirror zooms with it"): before the first
    ctl `zoom`, from the settled unzoomed 2-pane mirror, `$SRC resize-pane -Z
    -t rem:1.1` (no ctl verb — the flag arrives **on** as a bare
    notification); poll until `src_z=1 && dst_z=1 && dims equal`; then `$SRC
    resize-pane -Z -t rem:1.1` again and poll until both are 0. Then the
    existing body continues unchanged (its `:1196` leg is the unzoom
    direction against a ctl-zoomed window). Update the test's comment to name
    both parse directions.
  - `@test "a no-op %layout-change costs the remote nothing"` — the benefit,
    fails on `main`. 2-pane **`-h`** window at 150x40 (a left/right root, so
    the `-L` resize below actually moves a cell — `-L` on a `-v` split is a
    tmux no-op), `bridge_up 2 …`. `$SRC set -g @dm 0`; `$SRC set-hook -g
    after-display-message "set -gF @dm '#{e|+:#{@dm},1}'"` (verified live on
    the pinned next-3.8: the counter goes 0→1 per `display-message` and
    `select-layout` alone leaves it at 0). One comment in the test: the hook
    also fires for the daemon's own reads, and its `set` runs in the control
    client's queue as flag-0 `%begin/%end` blocks, which `claimSeq` treats as
    inert (#276) — not a desync. Quiesce: loop until `$SRC show -gv @dm` is
    unchanged across 10 samples 0.15 s apart (the seeds' cursor reads tick it
    too, which is what this waits out). `layout=$($SRC list-windows -t rem -F
    '#{window_layout}')`; `before=$($SRC show -gv @dm)`; `$SRC select-layout -t
    rem "$layout"` (verified: exactly two identical `%layout-change` lines);
    sample for 1.5 s; `after=$($SRC show -gv @dm)`; assert `[ "$after" =
    "$before" ]` and DST dims still equal SRC's. **Then the in-test positive
    control** so a dead or stalled daemon cannot pass: `pre_dims="$(sorted_dims
    "$SRC" rem)"`; `$SRC resize-pane -L 3 -t rem:1.1`; poll until `[
    "$src_dims" != "$pre_dims" ] && [ "$dst_dims" = "$src_dims" ] && [
    "$($SRC show -gv @dm)" -gt "$after" ]` (numeric `-gt`, never `>`, which is
    a redirect inside `[ ]`; gate 6 still pays the seeds' cursor reads and the
    trailing `readLayout`, on `main` and here alike); assert all three after
    the loop. Comment the standing assumption the quiesce relies on: the
    1.5 s window is shorter than the daemon's 5 s maintenance tick, and
    nothing on that tick issues a remote `display-message` today (the sweep
    and both shipper backstops read `list-windows`/`list-panes`;
    `reseedDropped`/`reseedReshaped` act only on pending work, which the
    quiesce drains) — a future addition there shows up here as a clear
    failure, not a mystery flake. **Negative control, run once:** build a
    daemon from `main` without touching this worktree's checkout — `git
    archive main picker | tar -x -C <scratch>/main-src` then `go build -o
    <scratch>/daemon-main ./remotebridge/cmd/daemon` from
    `<scratch>/main-src/picker` — and run only this test with
    `DAEMON=<scratch>/daemon-main`, `RENDERER`/`CTL` unset and the pinned tmux
    on PATH; confirm it fails at `[ "$after" = "$before" ]`; paste the failing
    lines in the PR.
  - `@test "a burst of remote geometry changes converges the mirror"` — 2-pane
    `-h` window at 150x40; paint a marker into `rem:1.1` as the #231 test does
    (retrying `send-keys` until `$DST capture-pane -p -t host-sess:1.0` shows
    it); then five `$SRC resize-pane -L 3 -t rem:1.1` with no sleep between;
    poll until `sorted_dims` match **and** `$DST capture-pane -p -t
    host-sess:1.0` equals `$SRC capture-pane -p -t rem:1.1` (content converges
    strictly after dims — `select-layout` precedes the seeds in the pass — so
    poll on the content equality itself, with dims as the pre-gate); assert
    both after the loop.
  - `@test "a split immediately killed converges the mirror, zoomed or not"` —
    fixture first: `$SRC new-session -d -s rem -x 150 -y 40`, `$SRC
    split-window -h -t rem`, `$DST new-session -d -s host-sess -x 150 -y 40`,
    `bridge_up 2 <tag>` (a 2-pane base, because `window_zoom` refuses a
    1-pane window and the ctl zoom below would never land). Then
    `new=$($SRC split-window -v -t rem -P -F '#{pane_id}')`; `$SRC kill-pane
    -t "$new"`; poll until dims match. Then `run "$CTL" --sock "$sock" zoom
    "$(remote_pane_of 0)"`, poll until both zoomed, repeat the split+kill, poll
    until `src_z = dst_z` and dims match.

  **Running locally.** `.#default` ships no bridge binaries, so leave
  `DAEMON`/`RENDERER`/`CTL` **unset** and let `setup()`'s `go build` fallback
  run; put the pinned next-3.8 on PATH first — `nix build .#default` and
  then `dirname "$(readlink -f ./result/bin/.tmux-wrapped)"` (the bare
  `tmux-next-3.8` the wrapper execs; the check runs `(mkTmux pkgs)`, and 3.7b
  differs in exactly the notifications these tests turn on). Then
  `bats tests/remote-m2-integration.bats --filter '<test name>'` from the
  worktree root. Run the new and the edited tests 3× each before calling them
  stable; the whole file runs under `nix flake check` at gate time.

- [ ] **Step 7: `CLAUDE.md`.** In the Key Conventions bullet beginning
  **Zoom crosses the bridge as a ctl verb**, replace the clause from "and the
  mirror learns the state from `#{window_zoomed_flag}`, read in
  `readLayout`'s round-trip — which is redundant …(#570)." with: the
  notification carries `#{window_layout}` and the zoom flag (`Z` in
  `window_raw_flags`), and `reconcileLayoutFrom` answers a line that reports
  nothing new from the line itself (zero round-trips) and applies a
  geometry-only reshape from it; every zoom transition, pane-set change and
  float change still reads, because `window_push_zoom`/`window_pop_zoom`
  bracket structural commands with transient unzoomed lines and a stale
  structural line would do renderer surgery on a pane the remote has already
  closed; the live dispatch path is uncoalesced and the trailing re-read is
  the corrective (#570). Keep the rest of the bullet. Also update the
  sentence at `CLAUDE.md:772` — "`reconcileLayout` runs only on remote events
  — a `%layout-change`, a coalesced batch, or a reattach" — so it names
  `reconcileLayoutFrom` as the notification entry that may answer before
  reaching it; grep `reconcileLayout` (not `readLayout`) across `CLAUDE.md`
  for any other such sentence and leave everything else untouched.

- [ ] **Step 8: gate.** From the worktree root, three separate commands, each
  to a file so the exit status is the command's own (memory: a pipe hides a
  failed check): `nix build .#default`, `nix flake check`, `nix build .#lint`.
  Paste the tail of each into the PR. If `remote-m2-integration-tests` fails
  only with `1..N` all-ok and a non-zero exit, that is the known load-
  correlated bats mode — re-run once idle before treating it as real.

## Acceptance (from the spec, checkable)

- All unit tests in step 3 and step 5 green; the two negative controls run and
  their failing output captured.
- The four m2 tests green three times running; the benefit test's negative
  control against `main` captured.
- Existing m2 zoom test (`:1164`) still green.
- `nix build .#default`, `nix flake check`, `nix build .#lint` green.
- PR body states the scope decision (spec's Decision section, condensed),
  the coalescing decision, the active-pane decision, the #568 merge point, the
  escalation note (spec-critic verdict at the cap), and the gate output.
