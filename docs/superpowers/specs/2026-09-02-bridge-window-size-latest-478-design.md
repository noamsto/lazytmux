# Mirrored panes do not reflow: aggressive-resize hides every non-current window

Issue: #478. Design spec.

## Symptom as reported

Claude Code in a **mirrored** (remote-bridge) pane does not reflow when the pane
width changes — content "keeps expanding to the right". `btop` in a mirrored
pane on the same host, resized by the same gesture, appeared to reflow fine, as
did Claude Code in a local pane. Further observations: it is **per-window** (some
mirror windows reconcile, others do not, same session same gesture), repeating
the gesture can produce short-lived garbling that settles, and the reporter
suspected the resize path needed debouncing.

## Root cause

`ConvergeCmd` asserts a mirrored remote window's size with the per-window form
of `refresh-client`:

    refresh-client -C @N:WxH

lazytmux sets **`aggressive-resize on`** globally, and the remote host runs
lazytmux, so every remote window inherits it. tmux's own definition:

> aggressive-resize: resize the window to the size of the smallest or largest
> session **for which it is the current window**, rather than the session to
> which it is attached.

That is `clients_calculate_size_skip_client`'s `current` branch in `resize.c`
— paraphrased, not quoted: the skip is evaluated per candidate client against
that client's own session's current window. With the option on, a window is
sized **only** from clients whose session currently has that window selected.

The bridge holds exactly one control client on the mirrored remote session, so
it is "on" exactly one window at a time. For every other mirrored window our
client is skipped, no client contributes a size, and the window keeps whatever
size it last held — or tmux's `default-size` (80x24) if it never held one.

The daemon never issues `select-window` on the remote (every `select-window` in
`picker/remotebridge` targets `cfg.LocalTmux` — `daemon.go:534`,
`reconcilewindows.go:70`, `translate.go:22`). So which remote window is current
is unrelated to what the user is viewing locally, and **at most one arbitrary
mirrored window can accept a size.**

`converger.need` then records the size as asserted at the moment it returns
true, before anything is known about the outcome. tmux *accepted and discarded*
the command, so the record is wrong and the window is never re-sent that size.
It stays stuck until a different client size arrives while that window happens
to be the remote's current one.

That is the whole reported signature: per-window variance, arbitrary which
windows are hit, and partial self-healing when the gesture is repeated.

### Why the steady-state baseline in the issue looked healthy

The issue's baseline table shows five mirrored windows all agreeing exactly.
That is consistent: creating a window makes it current, so each window's
`setupWindow` cap landed at creation, and nothing resized afterwards. The defect
only appears on a **resize after setup**. The issue anticipated this — "the
panes above are ones that converged fine, sampled at rest".

The same rule explains the one window in the reproduction below that was wrong
from birth: `@12` and `@13` were created back-to-back, `@13` became current
before `@12`'s `setupWindow` issued its cap, so `@12` never converged at all.

## Evidence

### 1. Isolated in pure tmux, no lazytmux code involved

`tmux next-3.8`, two windows, one control client, window `@0` current. The only
variable is `aggressive-resize`:

    aggressive-resize OFF (tmux default)        aggressive-resize ON (lazytmux)
    refresh-client -C 100x30                    refresh-client -C 100x30
      @0 100x30   @1 100x30                       @0 100x30   @1 80x24
    refresh-client -C @0:150x40                 refresh-client -C @0:150x40
    refresh-client -C @1:160x41                 refresh-client -C @1:160x41
      @0 150x40   @1 160x41                       @0 150x40   @1 80x24  <-- IGNORED

With the option off, both forms reach a non-current window. With it on, neither
does. `window-size` was `latest` in both runs, so `window-size` is not the
variable — `aggressive-resize` is. The remote confirms both option values:
`aggressive-resize=on`, `window-size=latest`.

### 2. Reproduced on the live bridge, g5 -> tp-g6

Isolated remote session `v478`, three windows: `@11` and `@13` each running a
one-shot-repaint SIGWINCH probe, `@12` running `btop`. Remote's current window
is `@13`. One client resize 250x66 -> 200x50 -> 250x66, driven on a pty whose
winsize we set — the same signal a kitty font-zoom produces.

    SNAP  LOCALCLI  REMOTE ww 1/2/3        LOCAL pane 1/2/3
    t04   250x66    250x64/80x24/250x64    250x63/80x23/250x63
    t05   200x50    250x64/80x24/200x48    250x63/80x23/250x63
    t09   200x50    250x64/80x24/200x48    250x63/80x23/200x47
    t12   250x66    250x64/80x24/250x64    250x63/80x23/200x47

Only `@13` — the remote's current window — tracked the gesture. `@11` never
moved. `@12` sat at `default-size` for its whole life.

The probes' own SIGWINCH log is ground truth on what the remote programs learned:

    W1 start 250x63
    W3 start 250x63
    W3 winch 200x47
    W3 winch 250x63

**`W1`, in the non-current window, received no SIGWINCH at all.**

### 3. This answers the issue's decisive question: the REMOTE screen is wrong

The mirror is faithful — it shows the old width because the remote pane really
is still the old width. So this is **not** a stale/lost reseed (the
#233/#412/#417 family) and not a lost local repaint. No reseed-ordering rule is
implicated.

### 4. It is not program-specific

`btop` was in `@12` and stayed 80x23 through the entire gesture. btop is not
immune; it was never in a window that could accept a size. The
btop-vs-Claude-Code asymmetry is per-**window** variance, not per-program
behaviour. Repaint style only changes how the failure *looks*: btop repaints
fully every tick, so a wrong-width window still renders a coherent screen that
reads as "fine" at a glance, whereas Claude Code paints once per SIGWINCH into
the normal buffer, so a window that never converged shows content laid out for
the old width and never repaints.

### 5. Correction to the issue's suggested fix

The issue proposes threading `stream.send`'s discarded `bool` into `watchResize`
so a dropped write stops updating the converger. That would not have fixed this:
the write **succeeds** and tmux accepts the command, then discards it during
size recalculation. Recording on a confirmed write is still worth doing — it
closes a genuine, narrower hole — but it is not the fix.

## The fix — designed twice

### Option A (rejected): `resize-window -t @N -x W -y H`

Sizes any window unconditionally; verified working on a non-current remote
window, and it emits `%layout-change` so reconcile still fires. **Rejected**: it
sets that window's `window-size` to `manual`, which makes the remote window
unresponsive to *any* client. That inverts the contract `FitWindowCmd`
documents (`mirror.go:12-16`: "The remote can legitimately be smaller than the
mirror — another client attached to it clamps it down — so the mirror window is
what has to give"), and it breaks the green #231 regression test at
`tests/remote-m2-integration.bats:407-465`, which attaches a smaller second
client to the remote and asserts the remote shrinks. It also leaves remote
windows pinned whenever the daemon dies without a graceful teardown, which is
actively hostile to a human who later attaches.

### Option B (chosen): opt each mirrored remote window out of `aggressive-resize`

    set-option -w -t @N aggressive-resize off

Then the existing `refresh-client -C @N:WxH` cap does what its doc comment
already claims. Verified on the live remote, under the real lazytmux config,
against the window that had been stuck at 80x24 since birth:

    PRE   @11 250x64 ar=1   @12 80x24 ar=1   @13 250x64 ar=1
    set-option -w -t @N aggressive-resize off   (each window)
    refresh-client -C @N:250x64                  (each window)
    AFTER @11 250x64 ar=0   @12 250x64 ar=0  @13 250x64 ar=0   ws=latest

`@12` converged immediately, and `window-size` stays `latest` — nothing is
pinned, so Option A's whole class of problems does not arise:

- **The multi-client consequence, measured rather than asserted.** The question
  the opt-out raises is whether a human client attached to the same remote
  session can now size a mirrored window it is not viewing. Measured on the
  pinned next-3.8 tmux, two windows, remote current on window 1, bridge capping
  both at 100x30, then a *smaller* 80x24 human client attaching on window 1 —
  two arms differing only in the opt-out:

      arm        after bridge caps          after the 80x24 human attaches
      control    @0 120x40  @1 100x30       @0 120x40        @1 80x23
      opt-out    @0 100x30  @1 100x30       @0 100x30 (!)    @1 80x23

  In the opt-out arm the non-current window `@0` **kept the bridge's cap** —
  the human did not size a window it was not viewing. `window-size latest` and
  `aggressive-resize` are independent filters: dropping the aggressive-resize
  filter does not make a non-viewing client `w->latest` for that window. So the
  eligible-client set for any given window is unchanged in practice, and the
  only behavioural difference is that the bridge's cap now lands at all.
- #201's protection is therefore intact, and its test
  (`tests/remote-m2-integration.bats:516`) is the relevant one — not #231. When
  the human *is* `latest` (i.e. actually viewing the window) the cap still
  clamps and the smaller client still wins, which is exactly what that test
  asserts (`@1 80x23` in both arms above).
- The "remote may legitimately be smaller" contract (`mirror.go:12-16`) is
  preserved intact: the cap remains a clamp, nothing is pinned.
- `aggressive-resize off` is the semantically correct setting for a window whose
  size is driven by a client that is deliberately not "on" it. It is what the
  bridge has always meant.
- It is one idempotent command per mirrored window for the window's whole life,
  not per tick.

### Scope

1. Emit the opt-out for every mirrored remote window in `setupWindow`
   (`daemon.go:713`), before the cap. `setupWindow` is the single choke point —
   initial windows, `%window-add`, and `resetWindow` all pass through it. Send it
   unconditionally rather than under `cv.need`, so it does not depend on the
   converger's state.
2. Correct `ConvergeCmd`'s doc comment (`size.go:8-14`). Its claim that the
   per-window form "holds even when a human client is attached to the same
   remote session and owns the window as w->latest" is false as written: it holds
   only once the window participates in sizing at all. Correct, do not preserve.
3. Record the converger's slot only on a **confirmed write**, at all *three*
   call sites — `watchResize` (`daemon.go:230,234`), `setupWindow`
   (`daemon.go:713`), and `Run`'s startup client-size assertion
   (`daemon.go:398-400`) — so the recorded size is never ahead of what the remote
   was told. The startup one matters most, not least: it is the first record of
   the daemon's life, and `watchResize` re-sends the `clientSizeKey` slot only on
   a *change*, so a lost startup write leaves every later remote window born at
   the 80-column control-client default with nothing to correct it.
   Keep `need` atomically check-and-set under one lock and add a *conditional*
   undo (clear only if the slot still holds the value we tried to write): a
   check-then-act split would let `watchResize`'s goroutine and the main loop
   both send, which is the very invariant this item exists to protect.
   Also `cv.forget` the window in `setupWindow` right after the opt-out and
   before the cap. `reg.add` precedes `setupWindow` (`daemon.go:523-524`,
   `reconcilewindows.go:84-85`), so `watchResize` can cap a newly registered
   window before its opt-out lands; tmux discards that cap but the converger
   records it, and setup's own cap is then skipped — leaving a window opted out
   but never capped. Changing a window's sizing eligibility invalidates any
   record made against it. `watchResize` needs a sender whose result it can observe. State the
   residual gap: a written command is still not an applied one, and a
   `resize-window`/cap that draws `%error` because the window vanished latches
   the same way — acceptable only because that path is followed by
   `closeWindow`'s `cv.forget` (`daemon.go:852`).

### Tests (the reason this bug survived is structural)

Every existing size test is single-window, so the control client is always
current for the one mirrored window, and **the harness does not set
`aggressive-resize`** — so it cannot reproduce this class at all.

4. Add `set -g aggressive-resize on` to `SRC_CONF` in
   `tests/remote-m2-integration.bats` (production parity). Without it any new
   test passes vacuously. The full m2 suite must be re-run against the pinned
   **next-3.8** tmux to confirm no existing case depended on its absence.
5. Add **two** bats cases, because the startup cap and the resize path are
   different code and only the second is the reporter's actual gesture. Both use
   SRC with >= 2 windows and `select-window -t rem:1` so window 2 is the
   historically-skipped one, and both assert on **width** only — `clientArea`
   subtracts the client's status lines and `pane-border-status` takes a further
   row, so an expected height is brittle for no extra signal.
   - *Startup*: SRC and DST created at **different** sizes (SRC 120x40, DST
     100x30), so the cap is not a no-op. Every existing m2 case creates both
     servers at equal sizes, where "both windows follow the mirror" is already
     true with the bug present — the same vacuity trap as the missing
     `aggressive-resize on`. Assert every remote window reaches 100.
   - *Resize after setup*, which exercises `watchResize` — the startup case never
     touches it, and it is the path a terminal-size change actually takes: attach
     a real pty-hosted client to DST via the `OBS` pattern the #201 and #231
     cases already use, wait for the mirror to settle, **then** `resize-window`
     that client and assert every remote window follows. Needs a more generous
     poll budget than a setup gate, since `watchResize` polls once a second.
   Gate both on the MIRROR settling (one renderer pane per remote pane, session
   wide), then poll on the house budget rather than sleeping a fixed interval
   (darwin timing sensitivity) — and never use a source-side width as the settle
   proxy (SRC hits 100 as soon as the daemon sizes its own control client, #449).
6. Correct the false generalization in the comment at
   `tests/remote-m2-integration.bats:509-515` ("the per-window form is a clamp
   applied after that calculation, so it holds regardless of who is latest") —
   that is exactly the belief this bug rests on.
7. Update the tests that encode the setup command sequence —
   `size_test.go`, `daemon_test.go:465`, and the reply-block fakes at
   `setupwindow_test.go:87` and `:143` (an extra command per window setup shifts
   the reply ordinals). Add a Go test asserting the opt-out is emitted once per
   mirrored window.

### Deliberately out of scope, with reasons

- **A restore of `aggressive-resize` at teardown.** Declined, not forgotten.
  Teardown's control-stream sends are unreachable in practice: on the documented
  detach path (`lztmux-remote-detach.sh` `kill -TERM`) the handler SIGTERMs ssh
  first, so `Run`'s reader hits EOF and teardown runs against a transport that is
  already dying — and a SIGKILL, crash or ssh drop skips it entirely. So a
  restore would be theatre on the paths that matter — though note the daemon does
  hold an ssh `ControlMaster` that the graphics fetcher already uses out of band,
  so a best-effort restore is *reachable* there if the residue ever proves to
  matter; it is declined as unnecessary, not as impossible.
  Unnecessary because of the measurement above: with `window-size latest` still
  in force, a leftover `aggressive-resize off` window is still only sized by a
  client that is `w->latest` for it — i.e. one actually viewing it — which is
  what would size it with the option on too. The measured arms differ in exactly
  one place, the bridge's own cap.
  The residual difference is narrow and worth naming precisely: `aggressive-resize`
  only bites when a window lives in **more than one session** (a linked window)
  whose clients differ in size, since its whole point is to size from the
  sessions currently displaying the window rather than all sessions containing
  it. A remote window left opted out that later gets linked into a second session
  would size differently than lazytmux's global default intends. That is a real
  but narrow residue on a window nobody is mirroring any more, against a restore
  path that cannot run on SIGKILL, crash, or ssh drop regardless.
- **A debounce on the resize path.** The issue floats one. Note honestly that the
  fix *amplifies* reconcile traffic: today one nudge produces at most one remote
  `%layout-change` (the others being discarded), so after the fix a nudge on an
  N-window mirror drives N reconcile+reseed passes. That is the cost of
  correctness, not a new defect — those N-1 windows were simply broken before,
  and each genuinely needs re-seeding at its new size. The 1s
  `resizePollInterval` already collapses a burst to one pass per second. A
  debounce would trade convergence latency for traffic without fixing anything,
  and the issue is explicit that it must not paper over the real cause.
  The bound, stated rather than left vague: per due tick (at most one per second)
  the worst case is `windows x panes` `capture-pane` round-trips on the control
  stream, where before it was the panes of one window. That stream's congestion
  is the documented cause of the dropped-frame class (#412), so the claim "cost
  of correctness, not a new defect" needs an observable, not an assertion.
  Acceptance below therefore adds two: **content coherence** after settle (not
  just dims), and **no new drop/re-seed error lines** in the daemon's stderr
  across a burst of gestures. A settle-only predicate would pass identically
  whether the transient got better, stayed the same, or got materially worse.
  If the transient garbling survives on a >= 3-window mirror, it gets its own
  issue, filed with its own evidence and referenced here — not a hand-wave.
- The three adjacent findings the issue lists as out of scope. Worth noting that
  #1 and #2 (`localArea`'s detached fallback returning a size the daemon itself
  asserted, and attach firing neither nudge hook) are what let a window converge
  at the 80x24 fallback in the first place; this fix makes such a window
  *recoverable* on the next resize, where before it was permanent.
- A positive-area guard on the per-window cap. `localArea` falls through
  `clientArea` -> `sessionWinSize` -> `return 80, 24`
  (`cmd/daemon/main.go:255-258`), so the real daemon cannot emit `0x0`; guarding
  it would be speculative robustness.

## Acceptance

- **Offline:** both new multi-window bats cases (startup, and resize-after-setup)
  fail before the change and pass after, with `aggressive-resize on` in
  `SRC_CONF`. Full m2 suite green against the pinned next-3.8 tmux — not the
  3.7c on the devShell PATH, on which the bug does not reproduce at all, so a
  bare local `bats` run proves nothing here.
- **Gate:** `nix build .#default`, `nix flake check`, `nix build .#lint` all
  green, with real output pasted.
- **Hardware, g5 -> tp-g6:** on a mirror with >= 3 windows, several
  `Ctrl+-`/`Ctrl+=` steps in both directions, with the fixed daemon:
  - every mirrored window's remote `#{window_width}x#{window_height}` follows the
    gesture, not just the remote's current one;
  - both SIGWINCH probes log every step;
  - **content predicate, not judged by eye:** for each mirrored pane, after
    settle, the remote's `capture-pane -p -t %N` equals the local mirror pane's
    `capture-pane -p`;
  - a mirrored Claude Code pane reflows across the steps, with captured
    before/after pane content;
  - **no new drop/re-seed error lines** in the daemon's stderr across a burst of
    rapid gestures, and content equality reached within a stated time of the last
    gesture — the transient observable the declined debounce owes (see above).
    A settle-only predicate cannot tell a worsened transient from an unchanged
    one.
