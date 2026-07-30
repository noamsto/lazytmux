# Remote bridge M2.3 — structural input parity

**Issue:** [#230](https://github.com/noamsto/lazytmux/issues/230) — milestone M2.3 of #167
**Status:** design locked 2026-07-30, pre-implementation. **Revised 2026-07-30** after adversarial
review: the layout reconcile is now one general path rather than a switch with new cases (F6/F7),
echo suppression is a popped-through FIFO plus a sequence guard rather than a multiset, the ctl
protocol is explicitly versioned, `MouseDrag1Border` is out of scope, and #204/#231 are folded in.
**Revised again 2026-07-30** after a second adversarial review, all five blocking findings
measured rather than argued: both remote-current beliefs are now **invalidated to a sentinel**
by the verbs that stale them and **re-learned from ground truth** by every reconcile (one extra
format field, no extra round-trip); the reconcile applies focus from that ground truth rather
than from a belief; F7's swap claim is replaced by a **measured `-d` matrix** that lets the swap
focus problem be designed out entirely; the pane-death deferral is re-keyed onto a
`pendingFocus`; and the general reconcile carries an explicit guard against emptying the local
mirror window.
**Builds on:** M2.2 (`docs/superpowers/specs/2026-07-17-remote-bridge-m2-design.md`, the
authoritative M2 design; `2026-07-16-remote-pseudo-session-bridge-design.md` for the
architecture rationale; `2026-07-22-bridge-window-name-design.md` for the
`@window_bridge_name` precedent this milestone reuses)
**tmux:** all tmux source citations are against the pinned `tmux-upstream` input,
rev `29bf7fe2f70559d574c84c068d90d173891907d0` (`flake.nix:23`), i.e. `next-3.8`.

## Goal

A structural action performed with normal local keybinds **inside a `@bridge_win`
window** must act on the **remote** tmux, and the mirror must repaint from the remote's
own state — never from an optimistic local mutation. Non-bridge keybind behavior must be
byte-identical to today.

Today the input direction is per-pane keystrokes only: `pumpInput`
(`picker/remotebridge/daemon/daemon.go:681-694`) reads a renderer's `FrameInput` frames off
the unix socket and forwards them to the remote pane as `send-keys -H`. So you can *type*
into a mirrored pane, but a local split / new-window / rename / kill / resize / swap only
mutates the local mirror; the remote never learns, and the next remote `%layout-change`
snaps the mirror back (`daemon.go:805-810` re-applies the remote's `FitWindowCmd` +
`select-layout`).

## Requirements

1. **Discrete structural verbs** performed locally in a bridge window (`prefix c`, `|`,
   `_`, `,`, `x`, `&`, the four prefix `M-<arrow>` resizes, `{`, `}`) translate to the
   equivalent remote command over the existing `-CC` control connection.
2. **The remote stays the structural source of truth.** The mirror is updated by
   re-reading remote state, never by applying the local gesture locally.
3. **Local pane focus follows to the remote** (`select-pane`), echo-suppressed so it
   cannot ping-pong.
4. **Input routing follows local focus** — the third clause of the design's M2.3 line
   (`m2-design.md:146-149`). Satisfied **by construction**, not by new machinery: there is one
   renderer process per local pane reading *its own* stdin, and `pumpInput` forwards to
   `send-keys -H -t %N` with the pane captured by value (`daemon.go:681-694`). Whichever local
   pane tmux gives the keystrokes to is therefore the pane whose remote counterpart receives
   them, and the target never depends on the remote's active pane. Recorded as a requirement so
   a reader of both documents can see it was checked, not skipped.
5. **Zero blast radius on non-bridge use.** A gate that misfires on a normal window is a
   milestone failure — this is the whole reason the bridge was chosen over the
   grouped-session route (`2026-07-16-…-bridge-design.md`, "Why the bridge").
6. **Continuous interactions** (border-drag resize) do not corrupt the mirror. Satisfied by the
   design invariant itself rather than by a new binding: the remote stays authoritative, so a
   border drag in a bridge window is a **self-reverting gesture** (the next reconcile re-applies
   the remote's `select-layout`). M2.3 does not take ownership of the mouse binding — see
   "Border-drag resize: the M2.4 hand-off".
7. **The ctl↔daemon protocol is explicitly versioned.** The three binaries are built from one
   derivation, but they are **not** pinned to one another at runtime (see D2): ctl's path is
   baked into the tmux config and swaps on `prefix + r`, while the daemon is launched from the
   caller's PATH (`scripts/lztmux-remote-open.sh:87`) and is long-lived. So a version skew is
   reachable in normal use and must fail with a legible message, never a silent hang or a
   mystery disconnect.
8. **A verb that moves the remote's current window as a side effect leaves the mirror following
   it.** `prefix c` is the case that forces this: the remote's `new-window` makes the new window
   active, the daemon's own mirror create path is `new-window -d` (`daemon.go:391`), and under F4
   the remote's `%session-window-changed` is folded into our own reply body — so without this
   requirement a bridge `prefix c` would leave the human on the old window while the remote moved.
   Satisfied by `reconcileWindows` re-learning the remote's active window from its own
   `list-windows` read (`#{window_active}`) and `select-window`ing that window's local mirror
   **when it added at least one window** (D3). Deliberately scoped to "added ≥ 1" so a `,` or `&`
   intent cannot yank the human's window selection away from wherever they navigated with the
   ungated `M-H`/`M-L` binds.

## Empirical ground truth

These were probed directly on private `-L` servers with the pinned next-3.8 binary and are
**not** re-derived here. Where a claim was asked to be double-checked against tmux source,
the source citation follows.

### F1 — pane options are the pane→remote-pane carrier

`set-option -p -t <win>.<idx> @bridge_pane '%N'` sets a per-pane user option and
`#{@bridge_pane}` resolves from pane scope in formats. Code: `cmd-set-option.c:43` accepts
`p`; `options.c:1075-1084` resolves `-p` to `wp->options`. It **survives `respawn-pane -k`**
— confirmed in source: `SPAWN_RESPAWN` reuses `sc->wp0` as the pane rather than creating a
new one (`spawn.c:334`), so `wp->options` is untouched. It survives `select-layout` (no pane
is recreated), it travels with the pane through `swap-pane` (the option lives on the
`window_pane`, and swap moves the struct), and a freshly-split pane correctly has it empty.

### F2 — a window-option gate in a real key binding works, with zero blast radius

With `@bridge_win 1` on a window,
`bind-key -T prefix Z if-shell -F '#{@bridge_win}' '<ctl branch>' '<local branch>'` took the
ctl branch in the bridge window and the local branch in a plain window — verified by
injecting a *real* prefix keypress into an attached pty client. Inside the ctl branch,
`#{@bridge_pane}` (pane scope), `#{window_index}`, and a session-scope `#{@bridge_sock}` all
resolve. A gated `split-window` did **not** run locally in the bridge window and **did** run
locally in the plain window.

Source confirms the cost model: `if-shell -F` **does not fork** — it expands the format and
inserts one branch's command list (`cmd-if-shell.c:87-102`). Truth test is
`*shellcmd != '0' && *shellcmd != '\0'`, so an unset option (empty) is false. A non-bridge
window therefore pays exactly one format expansion per gated keypress and spawns no process.

### F3 — `after-select-pane` is the reliable local-focus seam; `pane-focus-in` is not

`after-select-pane` fired on every real focus change **whether or not a client was
attached**, and correctly did **not** fire on a redundant `select-pane` at the
already-active pane (5 issued → 4 fired). `pane-focus-in` fired erratically and only with an
attached client (it needs terminal focus reporting).

Source confirms the no-op behavior exactly: `cmd_select_pane_exec` returns at
`cmd-select-pane.c:267` (`if (wp == w->active) return`) **before**
`cmdq_insert_hook(s, item, current, "after-select-pane")` at `cmd-select-pane.c:274`. The
hook is a session-scope hook (`options-table.c:1936`), so `set-hook -g after-select-pane[N]`
is the right setter, and `#{@bridge_pane}` resolves in the hook's format scope (the hook's
target state is the newly-active pane).

### F4 — notifications caused by a control client's own command are emitted INSIDE that command's `%begin`/`%end` block

Verified in the stream and in source. `cmd_queue.c`:

```c
flags = !!(state->flags & CMDQ_STATE_CONTROL);   /* cmd-queue.c:591 */
cmdq_guard(item, "begin", flags);                /* cmd-queue.c:592 */
… entry->exec(cmd, item);
cmdq_guard(item, "end", flags);                  /* cmd-queue.c:652 */
```

`CMDQ_STATE_CONTROL` is set in exactly one place — `control_read_callback`, for commands
parsed off the control client's own input (`control.c:553`). So the third field of
`%begin`/`%end` **is** a provenance bit: `1` = this control client issued the command,
`0` = otherwise. Observed:

```
%begin <t> 370 1
%window-pane-changed @0 %0        <- our own select-pane
%end <t> 370 1
%begin <t> 372 1
%session-window-changed $0 @1
%window-add @1                    <- our own new-window
%end <t> 372 1
%begin <t> 373 1
%window-pane-changed @0 %4
%layout-change @0 70ba,100x29,…   <- our own split-window
%end <t> 373 1
```

Externally-caused changes arrive as bare top-level notifications.

**Consequence A.** `controlmode.Reader.Next()` accumulates everything between `%begin` and
`%end` into the reply block's `Data` (`controlmode/parse.go:117-134`), so a notification
caused by one of *our* commands is folded into a reply body and never surfaces as a `Line`.
**Any design where the daemon commands the remote and waits for the remote's notification to
drive the mirror does not work today.** This is the single constraint that shapes M2.3.

**Consequence B (verified in source, as asked).** `%output` can never appear inside a block.
`control_write_output` only *queues* a block onto `all_blocks` + `pending_list`
(`control.c:481-497`); the literal `%output %N …` text is printf'd in `control_write_pending`
(`control.c:658`), which is reached only from `control_write_callback` — the bufferevent
**write** callback (`control.c:761-790`). That callback cannot run inside
`cmdq_fire_command` (single-threaded, no event-loop reentry). Ordering is additionally
enforced by `control_write` itself: while `all_blocks` is non-empty, even a plain
notification line is *stored* as a block rather than written inline (`control.c:400-410`),
which is what the file's header comment describes (`control.c:32-46`). So `%output` lines are
strictly outside `%begin`/`%end`. Confirmed.

**Caveat on the flags bit.** `tmux.1:9003-9004` documents the third `%begin`/`%end` field as
"flags (currently not used)". The code computes it as the control-provenance bit, but the
manual disclaims it, on a moving `next-3.8` pin. This repo's own history (the M0
grouped-session spike) rejected building on undocumented behavior. So the flags bit is
recorded here as the *exact discriminator available to a future refactor* — a hint to probe
and pin, not a contract to depend on.

### F5 — next-3.8 default bindings (needed verbatim for any `if-shell` else-branch)

Read from `key-bindings.c` at the pinned rev, byte-exact:

| Key | next-3.8 default | This repo |
|---|---|---|
| `,` | `bind -N 'Rename current window' , { command-prompt -I'#W' { rename-window -- '%%' } }` (`:402`) | not bound → default applies |
| `c` | `bind -N 'Create a new window' c { new-window }` (`:426`) | overridden (`config/tmux.conf.nix:609-611`) |
| `{` / `}` | `swap-pane -U` (`:445`) / `swap-pane -D` (`:446`) | not bound → default applies |
| `%` / `"` | `split-window -h` (`:395`) / `split-window` (`:392`) | `unbind`ed (`tmux.conf.nix:605,607`); `\|`/`_` used instead |
| `x` | `bind -N 'Kill the active pane' x { confirm-before -p"kill-pane #P? (y/n)" kill-pane }` (`:443`) | overridden (`tmux.conf.nix:693-695`) |
| `&` | `bind -N 'Kill current window' & { confirm-before -p"kill-window #W? (y/n)" kill-window }` (`:396`) | overridden (`tmux.conf.nix:696`) |
| `M-Up`/`M-Left`/… | floating-pane-aware (`:468-470`) | all four overridden with plain `resize-pane -U/-D/-L/-R 5` (`tmux.conf.nix:619-622`) |
| `;` | `bind -N 'Move to the previously active pane' \; { last-pane }` (`:417`) | not bound → default applies |
| `MouseDrag1Border` | `resize-pane -M` (`:528`); `M-MouseDrag1Border` → `move-pane -M` (`:529`) | not bound; `set -g mouse on` (`tmux.conf.nix:540`) |
| `MouseDown3Pane` | the pane mega-menu (`:555`) | not bound → default applies |

**Note the `-N` notes.** Rebinding a default drops its note unless `-N` is re-supplied, and
this config ships tmux-which-key. Every rebound default in M2.3 must carry its original
`-N` text. F5 is a hand transcription of tmux source at one pin; B9's `list-keys` golden is
what converts it into a machine-checked claim (see "Testing strategy").

### F6 — a split or kill of a *non-last* pane is a mid-list insert / removal

Measured on a 3-pane window (`%0 %1 %2`) at the pinned rev. `split-window -v -t p:1.0`
produced pane order `%0 %3 %1 %2` — the new pane at **index 1**, not appended — with layout:

```
e70b,120x30,0,0{60x30,0,0[60x15,0,0,0,60x14,0,16,3],29x30,61,0,1,29x30,91,0,2}
```

Then `kill-pane -t p:1.2` produced `%0 %3 %2` — a mid-list removal.

`ParseLayout` / `collectLeaves` return panes depth-first in cell order
(`controlmode/layout.go:20-22,38-40`), so both of these reach `reconcileLayout` as a diff that
is **neither** `reflect.DeepEqual`, **nor** a tail-append, **nor** a tail-removal. They fall
into `default:` and are skipped. This is the fact that forces B1's general reconcile, and it
also means the mid-list cases are a **pre-existing M2.1/M2.2 hole**, not something M2.3
introduces: a *remote*-initiated split or kill of a non-last pane already leaves the mirror
silently stale today.

### F7 — the primitives a general reconcile needs

All measured at the pinned rev.

- **Splitting the explicit last pane appends deterministically.**
  `split-window -h -t <win>.<lastIdx>` puts the new pane at `lastIdx+1`. Today's code instead
  splits the *window* (`split-window -h -t <win>`) and relies on an implicit "the new pane
  lands at index i" assumption, which its own comment flags as unconfirmed
  (`daemon.go:747-763`). Targeting the last pane explicitly turns that assumption into a
  property of the command.
- **`swap-pane` exchanges positions and pane options ride along.** Local
  `swap-pane -s <win>.<i> -t <win>.<j>` exchanges the two panes' `pane_index` positions, and
  the per-pane options move with the pane: with `@bridge_pane` = `%r0`/`%r1`/`%r2` at indices
  0/1/2, `swap-pane -s p:1.0 -t p:1.2` yielded `0:%r2 1:%r1 2:%r0`. This is the F1 property
  observed end-to-end, and it is why a permutation needs no router changes.
- **`swap-pane` fires no `after-select-pane` hook**, on any form (hook log empty across the
  matrix below). It never routes through `cmd_select_pane_exec`, so the local swaps the reconcile
  issues in phase 3 generate **no** ctl `focus` traffic at all.
- **`swap-pane`'s effect on the active pane is flag-dependent, and `-d` means the opposite thing
  in the two target forms.** Measured on a 3-pane window (`%0 %1 %2` at indices 0/1/2) with a
  minimal `-f` conf; the middle pane `%1` active for the one-target rows, `%0` (index 0) active
  for the two-target rows:

  | Command | Resulting order | Active **index** | Active **id** |
  |---|---|---|---|
  | `swap-pane -t %1 -U` (no `-d`) | `%1 %0 %2` | `1 → 0` | **preserved** (`%1`) |
  | `swap-pane -t %1 -D` (no `-d`) | `%0 %2 %1` | `1 → 2` | **preserved** (`%1`) |
  | `swap-pane -d -t %1 -U` | `%1 %0 %2` | stays `1` | **changed** (`%1`→`%0`) |
  | `swap-pane -d -t %1 -D` | `%0 %2 %1` | stays `1` | **changed** (`%1`→`%2`) |
  | `swap-pane -s p:1.0 -t p:1.2` (no `-d`) | `%2 %1 %0` | stays `0` | **changed** (`%0`→`%2`) |
  | `swap-pane -d -s p:1.0 -t p:1.2` | `%2 %1 %0` | `0 → 2` | **preserved** (`%0`) |

  So `-d` means "keep the same *index* active", and its effect on the active **id** is
  **opposite** between the one-target relative form and the two-target explicit form.

  **Consequence — the swap focus problem is designed out of existence, not fixed up.** Pick the
  flag on each side so the two agree by construction:

  - the **remote** swap is sent **without** `-d` — `swap-pane -t %P -U` — so the remote keeps
    `%P`, the pane the human is working in, active;
  - the **local** reconcile swap is issued **with** `-d` — `swap-pane -d -s <i> -t <j>` — so the
    local mirror keeps the *pane* (and therefore the remote pane it renders) active while it
    moves to its new index.

  Both sides then stay focused on the same remote pane: the swap case needs **no** focus fix-up,
  `remoteActivePane` survives a swap unchanged, and phase 4's `select-pane` is a no-op that F3
  says fires no hook. **Getting `-d` wrong on either side silently inverts this**, which is why
  the exact flag is mandated in the binding table (remote, no `-d`) and in phase 3 (local, `-d`)
  rather than left to the implementer, and why unit test 1 asserts the argv byte-for-byte.

### F8 — one round-trip yields the layout *and* the remote's active pane

`display-message -p -t <win> -F '#{window_layout}<TAB>#{pane_id}'` returned

```
7744,120x30,0,0{60x30,0,0,0,29x30,61,0,1,29x30,91,0,2}	%1
```

— the layout exactly as today, plus `%1`. `#{pane_id}` expanded in **window** scope is that
window's **active** pane. So the daemon can learn the remote's active pane authoritatively at
**zero extra round-trip cost**, by widening the format `readLayout` already sends
(`daemon.go:493-503`) and splitting the reply on the tab. This is what makes D1's beliefs
re-learnable from ground truth instead of guessed at command time.

The window-set twin is `#{window_active}` in `reconcileWindows`' `list-windows` format, which
likewise costs nothing extra.

### F9 — `new-window -t '<sess>:'` inserts at the lowest free index and activates the new window

Measured: with windows at indices `1` and `3`, `new-window -t '<sess>:'` produced `1 2 3` — the
**lowest free index**, not one past the highest — and made the new window **active**. Works with
space-containing and numeric session names. The insert position is what a bare local `new-window`
does; the "makes it active" half is what Requirement 8 exists to follow.

## Settled decisions

### D1 — echo suppression: two guards, the first a bounded commanded-focus FIFO

**Settled.** Per mirror window the daemon keeps:

- `commanded []string` — a **bounded FIFO** of remote pane ids the daemon has commanded focus
  to and not yet seen reported back. Cap **8**, dropping oldest on overflow.
- `localActive string` — the daemon's belief of which *remote* pane the **local** active pane
  corresponds to.
- `remoteActivePane string` — the daemon's belief of which pane the **remote** considers active.
  It is the pane-scope twin of D4's `remoteActiveWin`, and both are maintained the same three
  ways: **written** at command time when the command determines the value, **invalidated to the
  sentinel `""`** at command time when the verb has an implicit side effect whose result the
  daemon cannot name, and **re-learned from ground truth** by every reconcile (F8). They are also
  updated by any *surfaced* notification, but that is the weakest of the three paths and nothing
  in the design leans on it — under F4 the notification for our own command is folded into our
  own reply body and never reaches the main loop at all.
- `pendingFocus string` — a belated `%window-pane-changed` naming a pane this window does not yet
  know about, held for the next flush point. See "Pane death" below.
- `focusSeq int64` — the highest ctl `focus` sequence number seen for this window.

**Invalidation, not guessing — the rule that keeps both beliefs honest.** `new-window` activates
the new window; `split-window` without `-d` activates the new pane; `kill-pane` and `kill-window`
promote a survivor the daemon cannot name at command time. For all four there is **no** value to
write, and F4 means there is nothing to learn from the notification either. So the verb sets the
affected belief to the sentinel `""`:

| Verb | Invalidates | Why |
|---|---|---|
| `new-window` | `remoteActiveWin` | the remote makes the new window active (F9) |
| `split-window` | `remoteActivePane[@W]` | no `-d`, so the remote makes the new pane active |
| `kill-pane` | `remoteActivePane[@W]`, **and** `remoteActiveWin` | the remote promotes a survivor pane; on the window's last pane the window dies and a survivor window is promoted (this verb already carries both intents) |
| `kill-window` | `remoteActiveWin` | the remote promotes a survivor window |
| `swap-pane` | **nothing** | designed out — F7's flag pairing preserves the remote's active id |
| `rename-window`, `resize-pane` | nothing | no remote-current side effect |
| `select-pane` (`focus`) | — | *writes* the value, per the table below |

`""` can never equal a real `%N` or `@N`, so a sentinel **never** suppresses a D4 prelude and
**never** suppresses a remote `select-pane`: every guard in this design is an equality test, and
the sentinel fails all of them. Invalidation therefore fails **open** — an unnecessary command,
never a missing one.

**`localActive` and `remoteActivePane` are two different beliefs and must not be collapsed into
one.** They answer different questions — "what is the human looking at" versus "what will the
remote treat as current" — and the whole point of the non-convergence note below is that they
**can disagree**. Collapsing them would erase the skip that keeps the no-report leak rare.

**Which belief guards which direction, since this is the easy thing to get backwards.** The *only*
reason to skip issuing the remote `select-pane` is that the **remote** is already on `p` — so the
ctl direction is guarded by `remoteActivePane`, never by `localActive`. `localActive` alone must
**not** suppress the command: `p == localActive` with `remoteActivePane != p` is precisely the
divergent state (reachable via `prefix ;` or a dropped out-of-order focus), and sending the
command is what heals it. `localActive`'s job is the other direction — dropping the echo of the
local `select-pane` the daemon itself issued. Hence still two guards, one per direction, rather
than one guard used twice.

**And which mechanism heals which belief, stated separately because they are not symmetric.**

| Belief | Guards | Healed by |
|---|---|---|
| `remoteActivePane` | the **ctl → remote** direction (skip a redundant remote `select-pane`) | D4's prelude, which writes the belief it just enforced; verb-time invalidation to `""`; and every reconcile's F8 read |
| `localActive` | the **local echo** direction (drop the ctl `focus` our own local `select-pane` provoked) | a real local focus change; and every reconcile's phase 4, which writes it from the F8 read before issuing the local `select-pane` |

D4's prelude does **not** heal `localActive` — it compares and writes `remoteActivePane` only, and
never issues a local command. A `localActive` gone stale (via `prefix ;`) is healed by the next
real focus change or by the next reconcile of that window, and by nothing else.

State machine (this is the unit-testable core; it touches no I/O):

| Event | Condition | Action | New state |
|---|---|---|---|
| ctl `focus p seq` (local hook) | `seq < focusSeq` | **drop** — a stale focus overtook a fresher one | unchanged |
| ctl `focus p seq` | `p == remoteActivePane` | **drop the command, keep the belief** — the remote is already there, so `select-pane` would emit no report (F3) and would leak a FIFO entry | `focusSeq = seq`; `localActive = p` |
| ctl `focus p seq` | otherwise | send remote `select-pane -t p` | `focusSeq = seq`; `localActive = p`; `remoteActivePane = p`; **push** `p` onto `commanded` |
| `%window-pane-changed @W p` | `@W ∉ registry` | **drop** — the standing B2 registry guard (`daemon.go:412-420`, `translate.go:5-10`); `kill`/`select` must never run against a window this daemon doesn't own | unchanged |
| `%window-pane-changed @W p` | `p` appears in `commanded` | **drop** — our own echo | **pop through and including** that entry; `remoteActivePane = p` |
| `%window-pane-changed @W p` | `p == localActive` | **drop** — local already there | `remoteActivePane = p` |
| `%window-pane-changed @W p` | `p ∉ mw.remotePanes` | **no local action** — there is no local pane to select yet, so the id→local-index map cannot answer. The report is still **authoritative about the remote**, so the belief is taken | `remoteActivePane = p`; `pendingFocus = p` |
| `%window-pane-changed @W p` | otherwise (external) | `localActive = p` **first**, then issue local `select-pane -t <localWin>.<i>` | `localActive = p`; `remoteActivePane = p`; `commanded` **cleared** |
| reconcile completes for `@W` (phase 4) | — | `select-pane` the local index holding the **freshly-read** remote active pane (F8), skipped when it is already the local active pane | `remoteActivePane` = the read value; `localActive` = same, set **before** the local command; `commanded` untouched |

**Why the FIFO, popped-through, and not a multiset.** A multiset leaks. F3 means a commanded
`select-pane` at an **already-active** remote pane emits no `%window-pane-changed` at all, so
its `commanded[p]++` is never decremented — and each leaked count then swallows one genuine
*external* report for that pane, permanently. Popping through-and-including the matched entry
flushes exactly those earlier entries that produced no report, which is what makes the
bookkeeping self-correcting rather than monotonically wrong. The `p == remoteActivePane` row is
the complementary half: it keeps the no-report case **rare** rather than normal. The hard cap
bounds the damage if some unforeseen path still leaks.

Clearing the FIFO on a genuine external report is the same reasoning from the other side: once
the remote has moved somewhere we did not ask for, every outstanding intent describes a world
that no longer exists, so keeping them would only swallow future real reports.

**Why not a single `pending` slot.** With a single slot, a fast local A→B→C produces
`pending = C` while the remote emits `%…changed B` then `%…changed C`. The `B` report matches
neither `pending` nor `localActive`, so it is misread as an *external* change and bounces local
focus back to B before `C` corrects it — a visible flicker. The FIFO absorbs both reports as
echoes and leaves `localActive = C`. It still lets a genuine external change to a *fourth* pane
through, which a plain "ignore everything while an intent is outstanding" counter would swallow
(trading a transient flicker for a lasting divergence — strictly worse).

**Ordering: `run-shell -b` stays, and out-of-order delivery is made harmless.** The
`after-select-pane` hook keeps `-b`. A non-`-b` `run-shell` would put a socket round-trip on the
server's command queue for **every** focus change — the hottest interaction path — which is
exactly what D2's deadline discipline exists to avoid. But `-b` means two focus changes can have
their ctl processes delivered to the daemon in either order, so the machine must not assume
issue order. It does not: ctl stamps each `focus` request with `time.Now().UnixNano()` and the
daemon drops a `focus` whose seq is below the highest it has seen **for that window** (row 1).

The earlier draft claimed this machine is "order-idempotent by construction". That was false and
is corrected here: it is made order-**safe** by the seq guard, and the guard's own guarantee is
narrower than it first looks. The hook fires in order, so the ctl processes *start* in order —
but two processes that start microseconds apart can still read the clock in the inverted order
if the first is descheduled between `exec` and the clock read. The seq guard therefore shrinks
the out-of-order window from "hook/`-b` scheduling" (milliseconds, routinely hit on a fast
`j`-`k`-`j`) to "clock read race" (microseconds, rare). The residual failure is one dropped
focus report leaving `localActive` stale — the *same* bounded, self-healing class as the
`prefix ;` gap documented in the binding table, corrected on the next real focus change and
re-asserted by D4's prelude. It is not a new failure mode, and it is not claimed to be absent.

**Why it terminates.** Every branch either emits nothing, or emits exactly one command
*after* recording the state that the resulting feedback event will match:

- Our remote `select-pane` → remote `%window-pane-changed p` → hits the `p ∈ commanded`
  branch → dropped.
- Our local `select-pane` → local `after-select-pane` → ctl `focus p` → hits
  `p == remoteActivePane` → dropped, because the external branch set **both** beliefs to `p`
  *before* issuing the local `select-pane`. That ordering is load-bearing, and it is now
  `remoteActivePane` that carries the weight: `localActive` alone would be the wrong guard here,
  since suppressing on it would also suppress the genuine self-heal case above.

So the composed map reaches a fixpoint in one step from any state.

**It terminates but does not always converge — and the difference matters.** "Reaches a
fixpoint" must not be read as "the two sides agree". With concurrent local and remote focus
changes the machine can settle into a state where `localActive` and the remote's actual active
pane **disagree**, and stay disagreeing until the next focus event. This is harmless for
keystroke routing (Requirement 4: every renderer targets its own `%N` explicitly and never
consults the remote's active pane) and it is self-healed for remote-current scope by D4's
prelude, which re-asserts `select-pane` whenever the tracked state differs. But it is a real
divergence in what the human sees highlighted, and the acceptance criteria say "does not
oscillate", not "always agrees".

**Pane death and remote-initiated splits: `pendingFocus`, not an intent-keyed deferral.** When a
remote pane dies or a remote split creates one, the remote emits `%window-pane-changed` naming a
pane the daemon's `mw.remotePanes` does not describe yet — F4's own transcript shows
`%window-pane-changed @0 %4` arriving *before* the `%layout-change` for the same split. Resolving
the id→local-index map against that stale list would focus the **wrong** local pane and then —
because that local `select-pane` fires `after-select-pane` — command the remote to the wrong pane
too.

An earlier draft deferred on "a ctl layout intent for `@W` is pending". **That predicate is inert
for exactly the cases that need it:** pane death and remote-initiated splits involve no local
gesture, so no intent exists — and they are precisely the changes whose
`%window-pane-changed` *does* reach the main loop as a bare top-level notification, our own
being swallowed by F4.

**Settled:** the trigger is the pane itself, not an intent. A `%window-pane-changed @W p` naming a
`p ∉ mw.remotePanes` records `pendingFocus = p` for that window (and takes the belief — the report
is authoritative about the remote either way). `pendingFocus` is flushed at **both** points where
`remotePanes` has just been re-derived:

- the **intent drain**, after `apply(batch)` has run its reconciles;
- the main loop's **`%layout-change`** handler (`daemon.go:240-245`), after `reconcileLayout`
  returns.

The flush compares `pendingFocus` against the window's now-ground-truth `remoteActivePane`
(F8): **equal** → phase 4 has already applied it, discard; **different** → the held report has
been superseded by a fresher authoritative read, discard. So the flush never fights phase 4 and
never re-applies a stale focus.

**Which makes `pendingFocus` a backstop, and that is stated rather than dressed up.** Once phase 4
applies focus from F8's ground truth, every path traced above is *also* covered by phase 4, and the
flush finds nothing to do. `pendingFocus` earns its place only where no reconcile follows the
report — a window whose previous reconcile bailed on a `readLayout` error or hit
`maxReconcilePasses`, leaving `remotePanes` stale with no pending intent. It is one string per
window and it fails closed (a discarded pending focus is a focus the next real event re-derives),
which is why it is kept rather than argued away.

**Per-window focus state is disposed with the window.** The FIFO, `localActive`,
`remoteActivePane`, `pendingFocus`, and the seq high-water mark live per mirror window and are
dropped by `closeWindow` (`daemon.go:416-429`) and by `reconcileWindows`' close path, so a window
closing mid-flight cannot leave focus state behind for a later window to inherit.

**Rejected alternative — bare sequence tag.** The M2 design's "sequence tag" option
(`m2-design.md`, Open questions) tags an intent and ignores the matching notification. That
is exactly the `commanded` FIFO alone. It does not close the *second* loop: the local
`select-pane` the daemon issues in response to a genuine external change fires
`after-select-pane`, producing a ctl `focus` the tag cannot recognise (no tag was issued for
it, because the daemon never commanded the *remote*). The belief guards are what close that
half — hence guards on both directions, not a tag on one.

**Rejected alternative — compare-before-set via a local query.** Ask local tmux "is pane i
already active?" before setting. Rejected: `Config.LocalTmux`
(`daemon.go:36`) runs commands but cannot capture output, so this needs a new output-capturing
seam; it costs a fork per focus event on the hottest interaction path; and it is redundant —
F3 proves tmux itself will not fire the hook for a no-op `select-pane`, so the daemon's own
belief is sufficient and free.

**The scheme must not depend on F4's swallowing.** Today our own `%window-pane-changed` is
folded into a reply body and never reaches the main loop at all, so the `commanded` guard is
currently belt-and-braces and the loop has three independent brakes (the two guards + F3's
no-op suppression). That swallowing is a plumbing artifact a later milestone may remove (see
D5), so the state machine is specified and tested as if every notification were surfaced.

### D2 — command channel: new `wire` frame types on the existing unix socket, plus a tiny Go ctl binary

**Settled.** Two new frame types in `picker/remotebridge/wire/protocol.go` (which today
defines 5, `:15-21`):

```go
FrameCtl    FrameType = 6 // ctl->daemon: payload = NUL-separated argv (protoVersion, verb, args…)
FrameCtlAck FrameType = 7 // daemon->ctl: empty payload = accepted; else the error text
```

Payload is **NUL-separated argv**, not a space-joined string, so a window name containing
spaces or quotes survives without a second layer of quoting (the same reason
`cmd/daemon/main.go:252-256` shell-quotes the session name and the launcher passes untrusted
values through the environment).

**argv[0] is a protocol version.** ctl sends it as the first element; the daemon rejects a
mismatch with a **descriptive `FrameCtlAck`** ("bridge daemon speaks ctl protocol N, client sent
M — reopen the bridge") rather than closing silently. This is Requirement 7, and it exists
because the trio is **not** pinned to itself at runtime — see the store-path correction below.

`acceptRenderers` (`daemon.go:521-536`) already reads exactly one frame off each accepted
connection and requires it to be `FrameHello`. M2.3 makes that first frame a **dispatch**:

- `FrameHello` → renderer; delivered to `connCh` as today.
- `FrameCtl` → one-shot control request; handled, `FrameCtlAck` written, connection closed.
- anything else → closed (unchanged).

The local client is a **new tiny Go binary**, `picker/remotebridge/cmd/ctl`, installed as
`lztmux-remote-bridge-ctl` alongside the daemon and renderer:

- `picker/default.nix:66` — add `"remotebridge/cmd/ctl"` to `subPackages`.
- `picker/default.nix:69-78` — add `mv $out/bin/ctl $out/bin/lztmux-remote-bridge-ctl`.
- `config/tmux.conf.nix:251-260` — add
  `picker-bridge-ctl-bin = "${picker-generate}/bin/lztmux-remote-bridge-ctl";`.
- `vendorHash` (`picker/default.nix:65`) is **unchanged** — it hashes the module's dependency
  set, not its package list, and ctl adds no dependency (`net`, `os`, `flag`, `time` only).

**Correction — one store path does *not* pin the trio for ctl↔daemon.** The earlier draft
claimed it did, by analogy with the renderer. The analogy fails. The renderer's path is passed
to the daemon by the launcher at spawn time (`scripts/lztmux-remote-open.sh:45-47`), so daemon
and renderer are genuinely locked together for the daemon's lifetime. **ctl is not**: its path
is interpolated into the generated tmux config, so it swaps on `prefix + r` or any home-manager
switch, while the daemon was launched as a **bare PATH lookup**
(`setsid lztmux-remote-bridge-daemon`, `scripts/lztmux-remote-open.sh:87`) and is **long-lived**
— it survives every config reload. A reload or a lazytmux bump therefore hands a **new ctl to an
old running daemon** in ordinary use.

Without versioning, that skew fails badly: the old daemon reads frame type 6, fails
`f.Type != wire.FrameHello`, and **closes the connection** (`daemon.go:527-533`) — ctl sees a
bare EOF and the human sees a keypress that silently did nothing. Hence Requirement 7:

- ctl sends the protocol version as argv[0]; a mismatching daemon answers with a descriptive
  `FrameCtlAck` and ctl prints it.
- ctl maps **EOF on the first frame** (no ack at all — the pre-M2.3 daemon's behavior) to one
  clear line: `this bridge daemon does not speak the ctl protocol — reopen the bridge`, and
  exits non-zero.
- **A protocol change requires killing running daemons**, not just `prefix + r`. Stated here
  because it is the one deploy step that is not covered by the usual "reload is enough" rule
  this repo otherwise relies on.

**Deadlines.** ctl takes a dial deadline (250 ms) and an overall deadline (2 s) via
`net.DialTimeout` + `SetDeadline`, and exits non-zero on either. This is the guarantee that a
dead or wedged daemon cannot hold tmux's command queue: a stale socket with no listener fails
instantly (`ECONNREFUSED`), a hung daemon fails at the deadline.

**What the ack means, honestly.** `FrameCtlAck` means "the daemon accepted the request **and
wrote the remote command to the control stream**", **not** "the remote applied it" — the daemon
absorbs all ssh latency behind its fire-and-forget `send`, so ctl never waits on the network.

The "and wrote it" half is a correctness requirement, not a nicety, because **`send` is a silent
no-op once `closed`** (`daemon.go:99-110`, set in `teardown` at `daemon.go:171-173`). A `submit`
racing teardown would otherwise register an intent, drop the command on the floor, and still ack
success — the ack would lie at exactly the moment the human most needs to know. **Settled:**
`submit` **returns whether the command was actually written**, and ctl exits non-zero with one
stderr line when it was not.

**Liveness, stated plainly.** The intent drain is only as live as the control stream. The wakeup
that returns the main loop to the drain point is the reply block for our own command (D3), so on
a **stalled-but-open** ssh link — no FIN, no reply — the main loop blocks in `reader.Next()` and
the intent is **stranded** indefinitely. There is no timer and M2.3 does not add one. What the
human observes: the keypress succeeds (ctl got its ack, because the write to the local buffer
succeeded), the remote never visibly changes, and the mirror is frozen — the same symptom as any
other bridge stall, resolved the same way (close the bridge and reopen). This is the accepted
cost of the no-timer design, and it is bounded by the fact that a dropped link *does* surface:
`Ctl.Close()`/EOF ends `Run` and tears the mirror down.

Because ctl now reports both failures (skew and unwritten command), the keybinds use plain
`run-shell` — ordered, with an immediate error message. Only the `after-select-pane` hook uses
`-b`, for the reasons and with the seq guard set out in D1.

**M2.3 adds no shell script.** The M2 design imagined "a thin dispatcher script"
(`m2-design.md`, Keybind translation layer). A static Go binary invoked directly by
`run-shell` is strictly better here: one exec instead of a shell that must fork and then
connect, no `nc`/`socat` dependency, and it sidesteps the `disown` + `set -e` failure class
entirely (measured ~2-2.5% failures under 16-way load).

**Rejected alternative — a second socket.** A dedicated ctl socket would need its own path
derivation, its own stale-socket/pidfile lifecycle, its own chmod, and its own teardown
branch — duplicating `daemon.go:129-146` and `:156-178` for no gain. The existing socket is
already user-only (`0o600`, `daemon.go:136`) and already has a pidfile the launcher uses for
liveness (`daemon.go:143-146`, `lztmux-remote-open.sh:51-58`). One socket, one lifecycle.

**Rejected alternative — a non-Go client.** `printf` into `/dev/tcp` cannot speak a unix
socket; `nc -U` is not guaranteed present and cannot frame or read an ack with a deadline. A
shell client would also reintroduce the `disown` class above.

### D3 — mirror updates: command-then-reconcile, with an atomic submit

Because of F4, the daemon must **not** rely on the notification its own command provokes.
Settled shape:

1. The ctl handler (on the accepted connection's goroutine) sends the remote command
   **fire-and-forget** through the existing mutex-guarded `send` (`daemon.go:99-110`) —
   exactly as `pumpInput` already does from a renderer goroutine (`daemon.go:690-692`).
2. It also registers a **reconcile intent**, which the *main loop* drains.
3. A reply block reaching the main loop is the guaranteed wakeup that returns it to the drain
   point. No timer.

**Step 3 holds by conservation, not by identity.** The naive reading — "*our* command's reply
block wakes the main loop" — is wrong, and is contradicted by the interleaving hazard documented
two paragraphs below: a nested round-trip that is in flight when our command lands may consume
*our* block and leave someone else's for the top level. The argument that actually holds is a
counting one: the remote emits **exactly one reply block per issued command**, and every nested
round-trip consumes exactly the number it awaits, so an extra command issued from a socket
goroutine necessarily leaves exactly one extra block to reach the top level. *Which* block that
is does not matter — any block reaching the top level returns the loop to the drain. The wakeup
is guaranteed; its identity is not, and nothing in the design needs it to be.

**The atomicity that makes step 3 airtight.** A naive "send, then enqueue" has a narrow but
real race: the command's reply can arrive, the main loop can drain an empty intent set, and
only then does the enqueue land — stranding the intent until some unrelated line arrives. A
naive "enqueue, then send" has the mirror-image hole: the loop can drain *before* the remote
has executed, find nothing changed, and consume the intent. The fix is to make the
**register + send one critical section on the mutex the drain also takes**:

```
// ctl handler
ok := ctlState.submit(intent, remoteCmd)  // takes ctlState.mu: intents.add(intent); send(remoteCmd)
                                          // returns false if send was a no-op (daemon closed)

// main loop, top of each iteration
batch := ctlState.take()                  // takes ctlState.mu
apply(batch)                              // outside the lock — does its own round-trips
```

Any drain that happens after the send necessarily happens after the add (they are serialized
on `ctlState.mu`), and no drain can observe the intent before the command was sent. This is a
sharpening of the intended design, not a change of direction: the intended reasoning ("a reply
is the wakeup") is correct, and this pins the ordering that makes it a proof rather than a
probability. `submit` returning `false` is the B4 honest-ack path.

**The lock order is stated once and never varied: `ctlState.mu` → `sendMu`, never the reverse.**
`submit` holds `ctlState.mu` and calls `send`, which takes `sendMu` — so `send` **must never**
take `ctlState.mu`. This single rule is what makes "register + send in one critical section"
implementable without a deadlock, and it is why the two mutexes stay distinct rather than being
merged: `sendMu` is also taken by every renderer's keystroke pump (`daemon.go:690-692`), which
must never be able to block behind reconcile bookkeeping.

**The daemon never forwards raw command text.** ctl sends a **verb** plus arguments, and the
daemon builds the remote command from a **fixed verb table** — `new-window`, `split-window`,
`rename-window`, `kill-pane`, `kill-window`, `resize-pane`, `swap-pane`, `select-pane` — with
pane/window targets it resolves itself from its own registry. An unknown verb is an error ack.
This is a requirement, not a nicety: the socket being `0o600` and same-user makes arbitrary
command forwarding *survivable*, not *acceptable*, and a fixed table means the ctl surface can
never grow into a general remote-command channel by accident.

**Intents** (a set, so bursts coalesce):

```go
type intents struct {
    windows bool             // reconcileWindows()
    layouts map[string]bool  // remoteID -> reconcileLayout()
}
```

Applied `windows` first, then `layouts` for windows still in the registry (a layout intent
for a window `reconcileWindows` just closed is dropped — `reconcileLayout` needs a live
`*mirrorWindow` anyway). After `apply(batch)` returns, the drain **flushes `pendingFocus`** for
every window it touched, per D1's flush rule. Verb-time **belief invalidation** (D1's sentinel
table) happens inside `submit`'s critical section alongside the intent registration, so a drain
can never observe a belief that describes the world before the command was sent.

- `reconcileLayout(w)` — **existing** (`daemon.go:718-829`), used for split / kill-pane /
  resize / swap. M2.3 **generalises its diff engine** — see "The general layout reconcile" — and
  **widens `readLayout`'s format** so every reconcile re-learns the remote's active pane from
  ground truth (below).
- `reconcileWindows()` — **new**: one routing-aware
  `list-windows -t <sess> -F '#{window_index} #{window_id} #{window_active} #{window_name}'`,
  then add missing windows through the existing create path, `closeWindow` vanished ones, and
  re-assert `@window_bridge_name` (+ the instant-floor `rename-window`) where it changed. Used for
  new-window / kill-window / rename. It must also honour the existing "registry emptied ⇒
  teardown" rule (`daemon.go:257-260`).

  **`#{window_active}` is new, and its position in the format is load-bearing.** It goes
  **before** `#{window_name}`, because `parseWindowList` (`windows.go:110-127`) takes the name as
  the *remainder* after the second space (`strings.Cut(rest, " ")`) so that a name containing
  spaces survives. Appending the new field at the end would silently fold it into the name. The
  parse gains one more `Cut` and `remoteWindow` gains an `active bool`; `addWindow`'s own
  `list-windows` (`daemon.go:371`) uses the same format string and changes with it.

  **The follow-through Requirement 8 asks for.** Having read `#{window_active}`,
  `reconcileWindows` knows the remote's active window for free, so: **when it added at least one
  window, it writes `remoteActiveWin` from that read and `select-window`s that window's local
  mirror.** Registered in D4's belief, so the subsequent `%session-window-changed` (if it ever
  surfaces) compares equal and is dropped. Scoped to "added ≥ 1" so a `,` or `&` intent cannot
  yank the human's selection away from a window they reached with the ungated `M-H`/`M-L` binds.

  **Known residual, named rather than left to be discovered.** `prefix &` removes a window and the
  remote promotes a survivor, but "added ≥ 1" is false, so no `select-window` runs — and the
  healing `%session-window-changed` is swallowed by F4. tmux picks the local survivor for the human
  by its own rule, which need not be the mirror of the remote's new current window. Functionally
  harmless (the D4 prelude fixes remote-current scope for the next verb, and every verb carries its
  own target), visually a possible mismatch of one window until the next window event. Same class
  as the `prefix ;` gap. Widening the trigger to "added **or** closed ≥ 1" would close it and
  cannot fight local navigation either — `reconcileWindows` only ever runs from a ctl intent — but
  it is left out of M2.3 deliberately: it needs its own "was the closed window the selected one"
  condition to avoid moving the human on an unrelated `&`, and that is scope this milestone does
  not need.

  Implementation note: extract the per-window create tail — currently duplicated between the
  startup loop (`daemon.go:183-205`) and `addWindow` (`daemon.go:390-409`) — into one helper
  used by startup, `addWindow`, and `reconcileWindows`. `addWindow`'s own B2-confirming
  `list-windows` (`daemon.go:371-388`) is then the degenerate single-window case of
  `reconcileWindows`.

**`readLayout` learns the remote's active pane, at no cost.** Change the format it sends
(`daemon.go:493-503`) from `'#{window_layout}'` to `'#{window_layout}<TAB>#{pane_id}'` — a literal
tab inside the single quotes — and split the reply on the tab: the layout parses exactly as today,
and the second field is the remote window's **active pane id** (F8). Every `reconcileLayout` and
every `setupWindow` therefore re-learns `remoteActivePane` from ground truth, which is what lets
D1 drop verb-time guessing (the sentinel) and lets the reconcile apply focus authoritatively
(phase 4). `readLayout` is called twice per reconcile pass; the belief is written from **each**
reply, and phase 4 acts on the value that came with the layout it applied. If the trailing re-read
reports a *different* active pane with an *identical* layout the loop still returns — the belief is
then fresh while the local selection is one focus event stale, which is exactly the bounded
non-convergence D1 documents, healed by the next focus change.

**Why the reader must stay single-owner.** `controlmode.Reader` wraps one
`bufio.Scanner` (`parse.go:106-112`) and is not safe for concurrent use, and reply
consumption is **positional** — `readReply` / `readReplyRouting` (`seed.go:47-57`,
`daemon.go:436-449`) each consume exactly one `%begin…%end` block and assume blocks arrive in
command-issue order. Therefore **all** round-trips stay on the main loop's goroutine, and a
socket goroutine may only `send` + register an intent, never read. This is why the ctl handler
cannot do its own `list-windows` confirm and must hand the work to the main loop.

**Pre-existing hazard this milestone must not worsen (stated honestly).** Fire-and-forget
sends from non-main goroutines already exist — `pumpInput`'s `send-keys` (`daemon.go:690-692`)
and `watchResize`'s `ConvergeCmd` (`daemon.go:83`, whose comment at `daemon.go:70-73` calls
the extra acks "consumed harmlessly by the main loop's top-level `reader.Next()`"). That is
true only while the main loop is at the top. If an interloper's command is issued *between*
two commands of a nested round-trip, the round-trip's next `reply()` consumes the
interloper's block instead of its own. The blast radius is bounded and self-healing —
`readLayout`'s `ParseLayout` fails and reconcile retries on the next `%layout-change`
(`daemon.go:723-727`); `PaneSeed`'s 4-field parse falls back to zeros (`seed.go:66-72`) or
paints one stale screen until the next re-seed. M2.3's ctl sends join an existing class
rather than creating one, but the class is now on a path whose replies matter more. It is
recorded here as a known limitation and as the strongest argument for D5.

### D4 — remote-current scope: a prelude, not a hook

`new-window -c '#{pane_current_path}'` and `split-window -c '#{pane_current_path}'` are what
this repo binds locally (`tmux.conf.nix:606,608,611`), and cwd parity is part of "the
mirror feels local". Source shows the `-c` format is expanded **against the target session's
current window/pane, not the `-t` target**: `spawn_pane` calls
`format_single(item, sc->cwd, c, ts, NULL, NULL)` (`spawn.c:290-294`), and `format_single`
→ `format_create_defaults` → `format_defaults(ft, c, s, NULL, NULL)`
(`format.c:6764-6774`, `:6795-6806`), which fills window/pane from the session's current.

So a remote `split-window -h -t %P -c '#{pane_current_path}'` would take the cwd of whatever
pane the *remote* session currently considers active — which need not be `%P`, because M2.2
mirrors remote→local window selection (`translate.go:22-30`) but nothing mirrors local→remote.

**Settled:** the daemon tracks a single `remoteActiveWin string` belief and, for the two verbs
that depend on remote-current scope (`new-window`, `split-window`), emits a **scope prelude** in
the same critical section as the verb:

- `select-window -t <remoteWinID>` if `remoteActiveWin != remoteWinID`;
- `select-pane -t %<remotePane>` if the window's `remoteActivePane != remotePane`;

Note the pane condition compares against **`remoteActivePane`, not `localActive`**. The prelude
exists to fix what the *remote* will treat as current when it expands `-c '#{pane_current_path}'`,
so the remote-side belief is the one that decides whether the command is needed; `localActive`
answers a different question (D1). The pane half registers in the D1 FIFO so its echo is dropped.
The window half needs **no counter** — `remoteActiveWin` is a single field, maintained the same
three ways as its pane-scope twin (D1): written at command time when the prelude determines it,
**invalidated to `""`** by the verbs whose remote side effect the daemon cannot name (`new-window`,
`kill-pane`, `kill-window`), and re-learned from ground truth by `reconcileWindows`'
`#{window_active}` read. The `%session-window-changed` notification is guarded against the belief:
**equal → drop**. One field, no counter, the same shape as the pane fix. The prelude is skipped
entirely when the tracked state already matches, which is the common case — and never skipped
against a sentinel, since `"" != @N` always.

**Why a counter would be actively harmful here (not merely redundant).** An earlier draft
proposed a symmetric `commandedWin` multiset. It has **no loop to break** and would regress
M2.2. Under F4 our own prelude's `%session-window-changed` is folded into the reply body and
never reaches the main loop, so the counter would increment and **never** decrement; and because
each leaked count swallows one genuine `%session-window-changed`, it would progressively disable
the notification that makes the mirror follow the remote's window selection
(`translate.go:22-30`) — a shipped, hardware-verified M2.2 behavior. A guard whose failure mode
is "silently stop mirroring window selection" is worse than no guard.

**Why dropping on equality is safe.** The prelude only ever selects the remote window
corresponding to the local window the human is acting in — so at prelude time the local mirror is
*already* on that window's mirror, and skipping a local `select-window` loses nothing. The belief
and the local selection therefore stay in agreement, which is the invariant that makes "equal →
drop" correct rather than a divergence risk. The notification path keeps them in agreement from
the other side: a genuine external switch updates the belief **and** issues the local
`select-window`.

**Rejected alternative — drop `-c` from the remote commands.** Zero machinery, but
`prefix |` in a bridge window would open in the remote session's default path instead of the
pane's cwd — a visible parity loss against the local binding it replaces. Kept as the
fallback if the prelude proves troublesome.

**Rejected alternative — full local→remote window-selection mirroring** via a
`session-window-changed` hook on the mirror session. Most faithful, and the natural companion
to M2.4's copy-mode/mouse work (which will want the remote's notion of "current" to track).
Deferred: it is a second hook, a second echo-suppression channel, and the prelude already
gives exact parity for the only M2.3 verbs that need it.

**Accepted consequence.** The prelude changes the remote's active window/pane as a side
effect. That is consistent with exclusive-attach mode, which the M2 design already accepts as
a non-goal boundary ("Shared/co-attach with a live human on the remote at a different size" —
`m2-design.md`, Non-goals). A human concurrently attached to the remote will see their window
switch.

### D5 — rejected for M2.3: the "fix the plumbing" refactor, in both its maximal and scoped forms

The maximal fix for F4 is to make `Reader` **surface** notifications nested inside
reply blocks — tagged with the `%begin` flags provenance bit — and to give the daemon a
deferred-notification queue so `readReplyRouting` stops *dropping* the async notifications it
sees mid-round-trip (`daemon.go:436-449` returns only on `End`/`Error` and routes only
`Output`; everything else is discarded). With that, the daemon would not need intents at all:
its own command's notification would drive the mirror directly, and the design would collapse
to "command the remote; the notification repaints".

It would also close the known flake documented in `tests/remote-m2-integration.bats:266-271`
— killing the **active** remote window can interleave `%window-close` with a `%layout-change`
round-trip whose reader swallows the close; the test works around it by selecting a different
window first, and the comment already labels it "an M2.3 follow-up".

**The maximal form is deliberately not M2.3:**

1. It rewrites the hottest M2.1/M2.2 code paths — `Reader.Next` (`parse.go:114-137`),
   `readReplyRouting` (`daemon.go:436-449`), and the main-loop switch (`daemon.go:237-273`) —
   with real regression risk to a mirror that has been hardware-verified twice.
2. `capture-pane` reply bodies are **arbitrary pane content** and can contain lines that look
   exactly like notifications, so surfacing body lines needs its own disambiguation design.
   The flags bit does **not** solve this: it lives on the guard line, not on each body line.
   Disambiguation has to be structural — correlate by command number so the daemon knows
   which block is a `capture-pane` — and that in turn needs `Reader` to expose `%begin`'s
   command *number*, which it currently discards: `parse.go:120-124` sets the block id from
   `Args[0]`, which is the `%begin` **timestamp**, not the number.
3. The flags bit is documented as unused (`tmux.1:9003-9004`) even though the code computes it
   (`cmd-queue.c:591`), so pinning it needs its own probe + a regression test against the
   moving `next-3.8` input.

#### The SCOPED variant, stated on its own terms

Those three objections answer the *maximal* refactor. There is a **much smaller** variant that
they do not touch, and it deserves to be engaged rather than swept up with the big one.

**The variant:** re-parse **top-level block bodies only** and dispatch notification-shaped lines
through the **existing** main-loop switch (`daemon.go:237-273`).

**Why it dodges all three objections.** By **conservation**, a `%begin`/`%end` block that reaches
the *top level* of the main loop is the reply to a **fire-and-forget** command — every nested
round-trip consumes its own block (this is the same counting argument D3 rests on). The daemon
only ever issues `capture-pane` from *inside* a round-trip (`PaneSeed`, `seed.go:47-72`), so a
`capture-pane` reply can **never** reach the top level. Therefore the scoped variant needs:

- **no command-number correlation** — the block's provenance is implied by where it arrived;
- **no `capture-pane` disambiguation** — objection 2 evaporates, because the dangerous bodies are
  structurally out of reach;
- **no flags bit** — objection 3 evaporates too.

F4's own transcript is the evidence: the three example blocks are exactly our
fire-and-forget `select-pane` / `new-window` / `split-window`, each carrying the notification
that should have driven the mirror.

**And it is refuted anyway — on a fourth ground the maximal critique never reached.** It cannot
safely take **`%window-close`** from a block body. A pane printing text shaped like
`%window-close @1` — a log line, a paste, this very spec displayed in a mirrored pane — would
make the daemon **kill a mirror window**. That failure is *destructive and does not self-heal*,
which puts it in a different class from every other risk in this milestone (the intent machinery's
worst case is a stale mirror that the next reconcile fixes). And `%window-close` is not incidental:
`prefix &`, and `prefix x` on a window's last pane, are precisely the verbs that need it.

So **intents are needed for the destructive verbs regardless**. Restricting the re-dispatch to the
additive kinds (`%layout-change`, `%window-add`, `%window-renamed`, `%window-pane-changed`) would
remove only the *layout* intents, and B1's general reconcile is required either way — after which
the remaining intent machinery is small. The trade is: keep a small amount of bookkeeping whose
failure mode is a stale mirror, or accept a code path whose failure mode is a destroyed window.

**Recommended as the next follow-up issue — the scoped variant, which is better-scoped than what
this spec's earlier draft proposed.** Scope: re-dispatch top-level block bodies through the
existing main-loop switch for the **additive** notification kinds only, on the conservation
argument above; keep intents (and the "correlate by command number" work, if it is ever wanted)
for the **destructive** kinds; add a golden-transcript test with a block body containing both
`%layout-change`-shaped and `%window-close`-shaped text, asserting the first is acted on and the
second is not; remove the `select-window` workaround in `remote-m2-integration.bats` only if the
close case is genuinely covered.

**One robustness property worth recording**, because it explains why the bats workaround must
stay. `&`'s `reconcileWindows` intent incidentally **heals** the swallowed-`%window-close` case
for *locally* initiated kills — the intent re-reads the window set and closes whatever vanished,
whether or not the notification survived. That is a real M2.3 improvement to the D5 flake. But it
does nothing for a *remote*-initiated kill (no local gesture, so no intent), which is exactly what
`tests/remote-m2-integration.bats:266-275` exercises — so the workaround there stays.

## The binding table

Notation: `GATE` = `#{&&:#{@bridge_win},#{@bridge_pane}}`; `CTL` = `${picker-bridge-ctl-bin}
--sock '#{@bridge_sock}'`. Requiring **both** options means a stray non-mirror pane inside a
bridge window (nothing creates one today, but reflow and #204 both treat a bridge window as
an ordinary window otherwise) falls through to the local branch instead of failing at the
daemon. `#{&&:X,Y}` is the same idiom already used at `tmux.conf.nix:761`.

Every remote command below targets a **remote pane id (`%N`)** or a **remote window id
(`@N`)**, never an index — the registry is keyed by id (`windows.go:100-107`), and
`remoteWinTarget` (`daemon.go:483-485`) is the quoting helper.

| Local key | Gate | ctl verb | Remote command(s) | Intent | Else-branch (must be byte-identical to today) |
|---|---|---|---|---|---|
| `c` | GATE | `new-window --pane %P` | prelude; `new-window -t '<sess>:' -c '#{pane_current_path}'` | `reconcileWindows` | today's nested `if-shell -F '#{m:scratch-*,#{session_name}}' 'display-message "scratchpad: new windows disabled"' 'new-window -c "#{pane_current_path}"'` verbatim (`tmux.conf.nix:609-611`) |
| `\|` | GATE | `split --pane %P -h` | prelude; `split-window -h -t %P -c '#{pane_current_path}'` | `reconcileLayout(win(%P))` | `split-window -h -c "#{pane_current_path}"` (`:606`) |
| `_` | GATE | `split --pane %P -v` | prelude; `split-window -v -t %P -c '#{pane_current_path}'` | `reconcileLayout(win(%P))` | `split-window -v -c "#{pane_current_path}"` (`:608`) |
| `,` | GATE | `rename --pane %P <name>` | `rename-window -t @W -- '<name>'` | `reconcileWindows` | next-3.8 default, re-stated with its note: `-N 'Rename current window'` → `command-prompt -I'#W' { rename-window -- '%%' }` (`key-bindings.c:402`) |
| `x` | GATE | `kill-pane --pane %P` | `kill-pane -t %P` | `reconcileLayout(win(%P))` **and** `reconcileWindows` | today's `if-shell '<tmux-kill-pane-guard> #{pane_id} #{pane_current_command}' kill-pane 'confirm-before -p "kill-pane #P (#{pane_current_command})? (y/n)" kill-pane'` verbatim (`:693-695`) |
| `&` | GATE | `kill-window --pane %P` | `kill-window -t @W` | `reconcileWindows` | `confirm-before -p "kill-window #W? (y/n)" kill-window` (`:696`) |
| `M-Up` (`-r`) | GATE | `resize --pane %P -U 5` | `resize-pane -t %P -U 5` | `reconcileLayout(win(%P))` | `resize-pane -U 5` (`:619`) |
| `M-Down` (`-r`) | GATE | `resize --pane %P -D 5` | `resize-pane -t %P -D 5` | `reconcileLayout(win(%P))` | `resize-pane -D 5` (`:620`) |
| `M-Left` (`-r`) | GATE | `resize --pane %P -L 5` | `resize-pane -t %P -L 5` | `reconcileLayout(win(%P))` | `resize-pane -L 5` (`:621`) |
| `M-Right` (`-r`) | GATE | `resize --pane %P -R 5` | `resize-pane -t %P -R 5` | `reconcileLayout(win(%P))` | `resize-pane -R 5` (`:622`) |
| `{` | GATE | `swap --pane %P -U` | `swap-pane -t %P -U` — **no `-d`** (F7) | `reconcileLayout(win(%P))` — permute phase | next-3.8 default with note: `-N 'Swap the active pane with the pane above'` → `swap-pane -U` (`key-bindings.c:445`) |
| `}` | GATE | `swap --pane %P -D` | `swap-pane -t %P -D` — **no `-d`** (F7) | `reconcileLayout(win(%P))` — permute phase | `-N 'Swap the active pane with the pane below'` → `swap-pane -D` (`:446`) |
| `after-select-pane[10]` hook | GATE | `focus --pane %P <seq>` | `select-pane -t %P` (subject to D1) | none | none — two-arg `if-shell -F GATE '<ctl>'`, so a non-bridge focus change costs one format expansion and no process (F2) |

Notes on the table:

- **`after-select-pane` must be added to the `set-hook -gu` idempotency block.** Checked, and it is
  **not** there today: the block at `config/tmux.conf.nix:767-787` clears `after-new-window`,
  `session-window-changed`, `client-resized`, `after-new-session`, `client-session-changed`,
  `client-attached`, `pane-exited`, `window-linked`, `window-unlinked`, `after-resize-pane`,
  `after-kill-pane`, `pane-focus-in`, `alert-bell`, `alert-activity` — no `after-select-pane`. So
  unlike `after-resize-pane` (which M2.4 can set for free — see the border-drag hand-off), M2.3's
  hook needs the `set-hook -gu after-select-pane` line **added** to that block. Without it a
  `prefix + r` after a lazytmux bump leaves the old indexed hook in place pointing at a dead
  `/nix/store` ctl path, and every focus change in a bridge window fails silently. This is one of
  the config changes, not an afterthought: the block, the setter, and the binding land together.
- **`,`, `{`, `}` are keys this repo does not currently bind.** Gating them means the config now
  owns them, so their else-branches must reproduce next-3.8's defaults byte-for-byte, `-N` note
  included. This is the highest-risk regression surface in the milestone; the `list-keys` golden
  (B9, "Testing strategy") is what makes it machine-checked, and the hardware run covers the
  which-key/`?` presentation the golden cannot see.
- **`new-window`'s target is a *session*, in a target-*window* slot.** It must be
  `-t '<sess>:'` (trailing colon, via `tmuxQuote` — `daemon.go:489`), **not** `-t '<sess>'`. A
  bare session name in a `-t` that expects a window is the ambiguity this repo has already been
  bitten by (`CLAUDE.md`, "Session Targeting Gotcha") — a numerically-named session is the case
  that breaks. The trailing colon resolves it to the session, inserting the new window at the
  session's **lowest free index** — measured, F9: windows `1 3` become `1 2 3`, so *lowest free*,
  not "after the highest". That is what a bare local `new-window` does. F9 also measured the other
  half this table depends on: the new window becomes **active** on the remote, which is what
  Requirement 8's `select-window` follow-through exists to mirror.
- **`x` and `&` keep a confirmation in bridge windows, and always confirm.** The existing `x`
  guard (`tmux-kill-pane-guard`, `tmux.conf.nix:688-695`) reads the *local* pane's
  claude-status state and `pane_current_command` — for a mirror pane those are the renderer,
  not the remote workload, so the guard's "an idle shell kills instantly" logic would be
  answering the wrong question. A bridge `x` therefore always goes through
  `confirm-before -p "kill-pane #{@bridge_pane} (remote)? (y/n)"`, which preserves the intent
  the guard was written for ("a reflexive prefix+x can't silently take down a working pane")
  without inventing a remote-state query. `&` keeps its existing unconditional confirm, with
  the prompt reading `#{@window_bridge_name}` (the remote name) instead of `#W` (the
  reflow-derived label). `confirm-before -p` is format-expanded
  (`cmd-confirm-before.c:103-111` → `status_prompt_set`), and the confirmed command is the
  ctl `run-shell`.
- **`,`'s prompt seeds from `#{@window_bridge_name}`**, not `#W`: `window_name` on a bridge
  window is derived output written by reflow (`scripts/tmux-reflow-windows.sh:131-147`,
  `2026-07-22-bridge-window-name-design.md`), so seeding from it would offer the user the
  label rather than the remote name.
- **`,` keeps its `reconcileWindows` intent** rather than writing `@window_bridge_name` locally
  from the name the human just typed. Writing it locally would be faster and would need no
  round-trip — but a remote `rename-window` **can fail** (the window closed under us, the remote
  rejected the name), and writing the option first is precisely the *optimistic local mutation*
  Requirement 2 forbids. The trade, stated so it is not mistaken for an oversight: one extra
  `list-windows` on a rare, human-initiated action, bought for ground truth.
- **`%%` interpolation into `run-shell`.** `command-prompt`'s `%%` substitution lands inside a
  string tmux hands to `/bin/sh -c`, so a `'` typed into the rename prompt breaks the
  quoting. This is a pre-existing tmux idiom already used in this config at
  `tmux.conf.nix:686` (`bind N command-prompt -p "New session name:" "new-session -s '%%'"`)
  with the same exposure, and the value is typed by the human on their own machine (not a
  remote-derived value, which is the class `lztmux-remote-open.sh:70-80` and
  `cmd/daemon/main.go:252-256` defend against). M2.3 matches existing repo practice and does
  not claim to fix it; the daemon side still receives the name as one NUL-separated argv
  element (D2) and sanitizes it before writing `@window_bridge_name`
  (`windows.go:146-155`).
- **`prefix ;` (`last-pane`) is a known gap.** It changes local focus but does **not** fire
  `after-select-pane` — `cmd_select_pane_exec`'s `-l` branch returns at
  `cmd-select-pane.c:186` region, before the `cmdq_insert_hook` at `:274`. Consequence: the
  daemon's `localActive` belief goes stale.

  **That is benign for every M2.3 verb, and the reason is not the one an earlier draft gave.** Each
  verb carries its pane explicitly (`%P` from `#{@bridge_pane}`), and the reconcile's phase 4 now
  applies focus from an **authoritative** read (F8) rather than from a belief — so a stale
  `localActive` cannot steer anything. The earlier draft claimed D4's prelude "re-asserts
  `select-pane` … self-healing the belief"; it does **not**. The prelude compares and writes
  `remoteActivePane` only and issues no local command, so it never touches `localActive` — see D1's
  guards-which/heals-which table. A stale `localActive` is healed by the next real local focus
  change or by the next reconcile of that window, and by nothing else. The D1 machine still
  terminates from a stale state (the worst case is one dropped external focus report, corrected on
  the next real change). See "Probe during implementation" for the seam that would close the gap at
  source.

### Rejected alternative — a bridge key table

`set-option -t <mirrorSess> key-table bridge` would gate in exactly one place, and the mirror
session `<host>-<sess>` contains only bridge windows. Rejected: `key-table` is
`OPTIONS_TABLE_SESSION` scope and names the table consulted **first**, i.e. it replaces
`root` (`options-table.c:858-863`), and key tables do not chain — so it would require
duplicating the whole `root`+`prefix` surface (~40 binds in this config) into the bridge
table, and every future bind would need adding twice. It also has no per-window granularity,
which the `@bridge_win` tag deliberately does (reflow, enrich, update-icons, and tmux-remux
all key off the window, not the session — `m2-design.md` C2(c)).

## Where the gate options are stamped

| Option | Scope | Written by | When |
|---|---|---|---|
| `@bridge_win` | window | daemon | already: `daemon.go:191` (startup), `daemon.go:395` (`addWindow`) |
| `@bridge_pane` | pane | daemon | **new**: immediately after each `spawnRenderer`, on both paths — the `setupWindow` spawn loop (`daemon.go:321-325`) and the general reconcile's **append phase** (phase 2, which replaces today's tail-append at `daemon.go:747-763`). Value = the remote pane id. `set-option -p -t <localWin>.<i> @bridge_pane %N` (F1) |
| `@bridge_sock` | session | daemon | **new**: once, on `cfg.LocalSess`, right after the listener binds and the pidfile is written (`daemon.go:130-146`) and **before** any window is created — so no keybind can fire against a bridge window whose session lacks the socket. Value = `cfg.SockPath` |

`@bridge_sock` is stamped by the **daemon**, not the launcher, precisely so the offline
`--test-local` harness (`cmd/daemon/main.go:44-46`) gets it too — the launcher path
(`lztmux-remote-open.sh:41-44,79`) is not the only way the daemon runs, and a launcher-side
stamp would leave the integration test unable to exercise the real gate. Both writes go
through `cfg.LocalTmux` (`daemon.go:36`); **no bare `tmux`** anywhere in the daemon, per the
standing rule (a prior bridge bug clobbered a real window that way).

`@bridge_pane` needs no re-stamping after `select-layout`, `swap-pane`, or `respawn-pane -k`
(F1, and measured end-to-end in F7) — the option rides on the `window_pane`. That is what makes
the permute phase below work without touching the router.

**Option rename, flagged explicitly.** The M2 design's gate option was a single session-scope
`@bridge_ctl` (`m2-design.md`, Keybind translation layer). This spec deliberately replaces it
with **three carriers at three scopes** — `@bridge_sock` (session), `@bridge_win` (window),
`@bridge_pane` (pane). The reason is the gate itself: `#{&&:#{@bridge_win},#{@bridge_pane}}`
needs window *and* pane granularity to reject a stray non-mirror pane, and the socket path is
per-daemon so it belongs at session scope. Anyone reading the two documents together should
expect the name change, not hunt for `@bridge_ctl`.

## The general layout reconcile

`reconcileLayout` today has a three-case switch plus a give-up branch (`daemon.go:732-799`):
`reflect.DeepEqual` (geometry-only re-seed), a pure tail-append, a pure tail-removal, and
`default:` → "unsupported pane reshuffle … skipping reconcile".

**That shape cannot honour this milestone's own binding table.** F6 measured it directly: a
`swap-pane` permutation, a split of a non-last pane, and a kill of a non-last pane are each a diff
that is none of the three cases. `{`, `}`, and any `|`/`_`/`x` aimed at a pane that is not last
would land in `default:` and leave the mirror **silently stale** — the old pane still painting at
the old cell. Shipping those bindings against the current switch is worse than not shipping them.

**Settled: replace the switch with one general reconcile.** Not a fifth and sixth case — one path
that subsumes all of them. Ordered phases over `remote` (the old pane ids) → `newRemote` (the
freshly-read ids):

0. **Full-replacement guard — do not run the phases when the removal set is everything.** If
   `remote ∩ newRemote = ∅` (every pane the mirror currently shows is gone from the remote, and
   every remote pane is new to us), phase 1 would kill **every** local pane, and killing a window's
   last pane makes tmux **destroy the window** — leaving a registry entry whose `localWin` no longer
   exists and phase 2's `split-window -t <win>.<lastIdx>` failing against it. This is reachable
   without concurrency: a remote split followed quickly enough by killing the original pane presents
   the reconcile with `[%0] → [%3]`. So for that one case, take the **documented tear-down
   fallback** for that window instead: `router.Unregister` + close every conn, `kill-pane` local
   indices `N-1 … 1` (**index 0 is kept alive**, both so the window survives and because
   `PlanWindow` requires a 1-pane window — `mirror.go:22-33`), then re-run `setupWindow`, which
   re-reads the layout, re-shapes, respawns every renderer (re-stamping `@bridge_pane`), re-collects
   hellos and re-seeds. Costs a visible flicker on a rare diff; the phases below then hold
   unconditionally, because with this guard **phase 1 provably cannot empty the window**.

   **This guard is what keeps B1 from introducing the failure class the spec used to refute D5.**
   The old `default:` branch made this diff merely **stale** (log + `return`); the general reconcile
   would make it **destructive** — a destroyed mirror window that does not self-heal, which is
   exactly the class D5 was rejected for. Generalising the reconcile without this guard would trade
   a stale mirror for a destroyed one.

1. **Remove.** For each local index, **highest → lowest**, whose remote pane is absent from
   `newRemote`: `router.Unregister(id)`, close the conn and drop it from `w.conns`, then
   `kill-pane -t <win>.<i>`. Descending order keeps the surviving indices stable as the kills
   land. (This is today's tail-removal branch, `daemon.go:783-794`, with the tail restriction
   lifted.) Phase 0 guarantees at least one pane survives, so the local window survives.
2. **Append.** For each id in `newRemote` not yet present locally, in `newRemote` order:
   `split-window -h -t <win>.<lastIdx>` — targeting the **explicit last pane**, which F7 measured
   as a deterministic append to `lastIdx+1`. This replaces today's `split-window -h -t <win>`
   plus the implicit "the new pane lands at index i" assumption its own comment flags
   (`daemon.go:747-763`). Then `spawnRenderer` at the new index, and the **new `@bridge_pane`
   stamp** (see "Where the gate options are stamped"). Wiring keeps today's **batched** shape:
   split + spawn every appended pane first, then **one** `collectHellos(len(appended))`, then
   `seedRenderer` + `go pumpInput` per pane. Batching is load-bearing — seeding is sequential over
   the single control stream, so all renderers must be connected before any is seeded
   (`daemon.go:327-330`), and a per-pane `collectHellos` would serialize a `helloTimeout` per pane.

   **The seed dims come from the pane's index in `newRemote`, not from its temporary local index.**
   Today's `seedRenderer(…, L.Panes[i])` (`daemon.go:777`) is correct only because in a pure
   tail-append the two coincide. In the general path an appended pane sits at the **end** of the
   local list between phases 2 and 3, while its cell in `L` is at its `newRemote` position — so the
   dims must be `L.Panes[indexOf(newRemote, id)]`. Getting it wrong costs one transient wrongly-sized
   seed (phase 5's `FrameResize` broadcast corrects it on the same pass), which is precisely why it
   is written down: a verbatim refactor of the existing loop will carry the bug in silently.
3. **Permute.** The local order is now the right **set**, in the wrong order. Decompose the
   permutation from the current local order to `newRemote` into transpositions and issue one local
   **`swap-pane -d -s <win>.<i> -t <win>.<j>`** per transposition. F7 measured all three properties
   this needs: positions exchange; pane options ride with the pane — so `@bridge_pane`, the router
   registration, the per-pane sinks, and each renderer's `pumpInput` binding (`daemon.go:353`,
   `:778`) are all untouched; and **`-d` on this two-target form preserves the active pane's *id***,
   so the local pane the human is in stays the local pane the human is in as it moves to its new
   index. This preserves the mirror's core invariant, *local pane at position i renders
   `remotePanes[i]`*, by moving the local pane — which is exactly what the mirror is supposed to
   show.

   **The `-d` is mandatory and is the pair to the remote swap's *absence* of `-d`** (binding table,
   F7). Together they make the two sides agree by construction. Omitting it here inverts the
   behavior — F7 measured `swap-pane -s p:1.0 -t p:1.2` without `-d` keeping the active *index* and
   changing the active *id*, i.e. leaving the human's cursor on whichever pane inherited the index.
   `swap-pane` fires no `after-select-pane` hook on any form, so phase 3 generates no ctl `focus`
   traffic either way.
4. **Apply focus from ground truth.** `select-pane` the local index holding the remote's **active
   pane as read by this reconcile's own `readLayout`** (F8) — *not* the index holding `localActive`.
   Two reasons, and the first is the decisive one: the reconcile already holds the authoritative
   value at zero extra cost, so there is no reason to act on a belief that `prefix ;` or a dropped
   out-of-order report may have staled. The second is directional: applying the remote's active pane
   makes **local focus follow the remote**, which is the direction the design invariant wants
   (Requirement 2 — the remote is the source of truth), whereas re-asserting `localActive` would
   make the mirror preserve a local opinion.

   Registered in the D1 guards so the echo is dropped: set `remoteActivePane` (from the read) and
   `localActive` (to the same value) **before** issuing the local `select-pane`, exactly as D1's
   external branch does — the resulting `after-select-pane` → ctl `focus p` then hits the
   `p == remoteActivePane` row and is dropped. The `commanded` FIFO is deliberately **not** touched:
   it tracks remote-directed commands, and clearing it here would turn an in-flight echo into a
   spurious "external" report — the exact bounce the FIFO exists to prevent.

   Skipped when that local pane is already active, which F3 makes free anyway (tmux fires no hook
   for a no-op `select-pane`). **The swap case is one of those no-ops**: F7's flag pairing keeps
   both sides on the same pane, so phase 4 has nothing to correct after a `{`/`}` — the focus
   problem an earlier draft wrote this phase to fix is designed out, and the phase remains because
   mid-list inserts, pane death, and remote-initiated changes genuinely need it.
5. **Geometry.** Today's tail, unchanged: `FitWindowCmd` + `select-layout` + per-pane
   `FrameResize` (`daemon.go:805-817`), then the existing trailing re-read loop bounded by
   `maxReconcilePasses`.

   **The geometry-only re-seed must be preserved, not quietly dropped.** When phases 0-4 are all
   no-ops — `reflect.DeepEqual(newRemote, remote)`, i.e. same pane set and order, new dims — the
   panes still need their screens re-seeded, because the painters hold no back-buffer to reflow
   (`daemon.go:733-746`). Keep that as an explicit conditional sub-step. It is easy to lose in a
   refactor that thinks in terms of set differences, and losing it would silently blank every
   mirror on a resize. **This is the branch #231 lives in** — see "Inherited dependencies".

**Every diff is covered, so there is no `default:`.** Pane ids are unique within a window, so
`remote` and `newRemote` are sets: phase 0 handles the empty-intersection case, phase 1 handles
`remote \ newRemote`, phase 2 handles `newRemote \ remote`, phase 3 handles the reordering of the
intersection. Nothing escapes, and the give-up branch is **deleted** rather than kept as dead code.

**This is a net simplification, and it fixes a pre-existing bug.** One path replaces three special
cases *plus* the two further cases the old shape would have needed (mid-list insert, mid-list
removal) *plus* the permutation case an earlier draft of this spec proposed as a fourth. And
because F6's mid-list cases are reachable from the **remote** side today — a human splitting or
killing a non-last pane in the remote session — this closes an **M2.1/M2.2 hole** that predates
M2.3 entirely, rather than merely paying for M2.3's new bindings.

**Rejected alternative — tear down and re-run `setupWindow`** on *any* diff the old switch could not
handle. About five lines and reuses proven code, but it kills and respawns every renderer (visible
flicker, a full `capture-pane` re-seed per pane, and a `collectHellos` round-trip) — and it would
now fire on ordinary remote-side edits, not just exotic ones. Rejected as the **general** path,
retained as the **specific** path for phase 0's empty-intersection case (where the phases cannot run
at all) and as the fallback if the transposition decomposition proves fiddly in review.

**Rejected alternative — re-key the router** so renderer *i* starts serving a different
remote pane. Rejected: each renderer process announced its pane id in its `FrameHello`
(`daemon.go:528-533`) and its `pumpInput` goroutine captured `remotePane` by value
(`daemon.go:681`), so re-keying means making pane identity a mutable, mutex-guarded field on
a live goroutine — more state, more races, and it leaves the *content* at the wrong cell
anyway.

## Border-drag resize: the M2.4 hand-off

**M2.3 does not bind `MouseDrag1Border` at all.** An earlier draft gated it to a no-op in bridge
windows; that is cut, on three grounds. It is **mouse**, which both the M2 design and this issue's
non-scope list put in **M2.4**. It is the only row the binding table had that is **not** in the
design's M2.3 line (`m2-design.md:146-149` lists `prefix c/%/,/x`, resize, swap, and focus).
And neutralising it would mean the config **globally takes ownership of a root-table mouse
binding it does not own today** — non-bridge blast radius, against Requirement 5, for no gain over
simply documenting the snap-back. The design's own words for M2.3 are that continuous interactions
"run local"; leaving the binding alone is what makes that true.

**So, plainly: until M2.4, a border drag in a bridge window is a self-reverting gesture.** It
resizes the local mirror pane, the next reconcile re-applies the remote's `select-layout`, and the
pane springs back. That satisfies Requirement 6 (the mirror is not corrupted — the remote stays
authoritative) while being visibly imperfect. Route pane resizing through the discrete
`M-<arrow>` verbs, which reach the same end state with no new machinery.

**There is no existing seam, and M2.3 does not build one.** Stated plainly because the M2
design's one-liner ("Continuous interactions (border-drag) run local, sync final size to
remote") reads as though a seam exists. The analysis below is the **hand-off to M2.4**, not a
deferred M2.3 task.

- `watchResize` / `ConvergeCmd` do **not** cover it: they poll `cfg.LocalArea()`
  (`daemon.go:74-88`), which is the *client content area*
  (`cmd/daemon/main.go:131-179`), and a border drag changes pane dims **inside** a window,
  not the client area.
- #204 makes it worse, not better: a mirror window's dims are pinned to the remote's under
  `window-size manual` (`mirror.go:9-19` `FitWindowCmd`), so the window's own size does not
  move either, and the next reconcile re-applies the remote's `select-layout`
  (`daemon.go:808-810`) — i.e. today a border drag in a bridge window is a **self-reverting**
  gesture.

What a faithful sync would need: (a) end-of-drag detection — `resize-pane -M` re-runs the
binding per drag step, so an `after-resize-pane` hook fires many times and needs debouncing;
and (b) a **local-layout → remote command** translation, which is a new capability (every
existing translation runs remote→local). A cheap approximation exists — debounce
`after-resize-pane`, then send `resize-pane -t %P -x W -y H` with the local pane's dims — but
it puts the local layout transiently in charge while the remote's `%layout-change` is racing
to snap it back, which is exactly the "optimistic local mutation" the design invariant
forbids.

One convenience note for whoever picks this up: `after-resize-pane` is *already* in the
`set-hook -gu` idempotency block (`tmux.conf.nix:778`), so M2.4 can add the setter without
touching that block.

**Adjacent structural surface M2.3 leaves ungated, named rather than implied.** All of these act
**locally** in a bridge window after M2.3, so each is a divergence the human can produce:

| Binding | Effect | Class |
|---|---|---|
| `MouseDrag1Border` → `resize-pane -M` (`key-bindings.c:528`) | resizes the local mirror pane | self-reverting (above) |
| `M-MouseDrag1Border` → `move-pane -M` (`key-bindings.c:529`) | moves a pane **between windows** | structural, will diverge |
| `MouseDown3Pane` mega-menu (`key-bindings.c:555`) | split / swap / kill / resize / zoom | structural; explicitly M2.4's open question (`m2-design.md`, Open questions) |
| root `-n` `C-h`/`C-j`/`C-k`/`C-l` → `tmux-smart-nav` (`tmux.conf.nix:701-704`) | `select-pane` in a direction (or `send-keys` under the `is_vim` guard) | **benign** either way — the `select-pane` fires `after-select-pane` and feeds D1 correctly; the `send-keys` reaches the remote via `pumpInput` |
| root `-n` `M-H`/`M-L` → `previous-window`/`next-window` (`:626-627`), `M-J`/`M-K` → `tmux-window-nav` (`:628-629`) | change the **local window**, no remote counterpart | same class as the `prefix ;` gap |
| root `-n` `M-l` (`:599`), `S-Enter` (`:602`) | inject keys at the renderer pane | benign — keys reach the remote via `pumpInput` (Requirement 4) |

The `-n` rows are this config's own bindings, not tmux defaults, and they are listed because the
mega-menu gap was the only ungated surface the earlier draft named — the root table is the larger
one, and most of it turns out to be harmless for a reason worth writing down.

## Daemon-side state, and the data race that shapes it

**There is a real race here today, and M2.3 is the milestone that must not walk into it.**
`mirrorWindow.remotePanes` and `.conns` are written by the main loop with **no lock at all**
(`daemon.go:315`, `:755`, `:776`, `:797`, `:818`, `:822`, `:828`); `registry.mu` guards only the
**map** of windows, never the `mirrorWindow` structs it hands out (`windows.go:28-98`). Every
existing reader is the main loop itself, so today it is safe by single-goroutine accident. M2.3
introduces a **second** goroutine that needs pane→window resolution, which is exactly what would
turn the accident into a bug.

So an earlier draft's design — a `registry.byRemotePane(paneID)` accessor scanning `remotePanes`,
plus `localActive` living on `mirrorWindow` — is **rejected**: it would have the ctl goroutine
reading and writing fields the main loop mutates unlocked, under a mutex that does not cover them.

**Settled: one `ctlState` mutex owns everything shared with the ctl goroutine, and the ctl
goroutine never touches `mirrorWindow` at all.**

| State | Type | Lives in | Mutated by | Guarded by |
|---|---|---|---|---|
| `paneIndex` | `remotePane → remoteWindowID`, plus the ordered pane list per window | new `daemon/ctl.go` | **main loop**, at every site where it changes `remotePanes` | `ctlState.mu` |
| `commanded` (bounded FIFO), `localActive`, `remoteActivePane`, `pendingFocus`, `focusSeq` | per remote window id | new `daemon/focus.go` | ctl handler, main loop | `ctlState.mu` |
| `remoteActiveWin` | `string` | new `daemon/focus.go` | main loop (`%session-window-changed`, `reconcileWindows`' `#{window_active}` read), ctl handler (prelude at command time, sentinel invalidation) | `ctlState.mu` |
| `intents` | `{windows bool; layouts map[string]bool}` | new `daemon/ctl.go` | ctl handler (add), main loop (take) | `ctlState.mu` |
| `send` | closure over `sendMu` | `daemon.go:96-110` — **unchanged** | — | `sendMu` |

Consequences of that shape, each load-bearing:

- **`paneIndex` is the ctl goroutine's only view of pane→window.** A ctl request carrying just
  `#{@bridge_pane}` resolves to its window (and thus to `@W` for the window-scoped verbs) by
  reading `paneIndex` under `ctlState.mu`. No new tmux option is needed for the window id, and no
  unlocked struct is read.
- **The main loop must update `paneIndex` under `ctlState.mu` at every one of the `remotePanes`
  write sites listed above** — including the general reconcile's phases. Missing one is the bug
  class this section exists to prevent, so it is worth grepping for `remotePanes =` as an
  implementation checklist.
- **Lock order, stated once: `ctlState.mu` → `sendMu`, never the reverse** (D3). `send` must never
  take `ctlState.mu`. Keeping `localActive` off `mirrorWindow` is part of what makes this
  achievable — there is no path where holding `ctlState.mu` requires reaching into registry state.
- **The new Go check runs with `-race`** (see "Testing strategy"). A lock-order or missed-update
  regression here is invisible to a functional test.

The decision (which prelude, which intent, whether to drop an echo) is taken **inside**
`ctlState.mu`; the side effects that fork (`cfg.LocalTmux`) run **outside** it, so a renderer's
keystroke `send` is never blocked behind a fork. New files: `daemon/ctl.go` (frame dispatch,
verb→argv translation, `paneIndex`, intent queue), `daemon/focus.go` (the D1 state machine),
`daemon/reconcile.go` (`reconcileWindows` + the extracted per-window create helper). The
`%window-pane-changed` main-loop case replaces the deliberate M2.2 no-op recorded at
`daemon/translate.go:10`.

## Inherited dependencies

Two facts about the size model changed after this design was first drafted. Both are recorded
here rather than buried in the hardware-run list, because one of them **gates an acceptance
criterion**.

**#204 is now two-machine verified (g5 → tp-g6, 2026-07-30).** The size model — per-window clamp,
concurrent attach, clamp release, teardown — has had a real two-machine run. M2.3 treats
`FitWindowCmd`'s `window-size manual` behavior as its contract (`mirror.go:9-19`), and that
contract is now **verified**, not assumed. The earlier draft's caveat ("#204 itself has not had
two-machine verification") is obsolete and is removed.

**#231 is OPEN, and it lives in the exact function M2.3's resize verb drives.** That same
hardware run filed it: on a **geometry-only** change the mirror **blanks** — the re-seed never
paints, because `PaneSeed`'s error is silently discarded. That is the
`reflect.DeepEqual` branch of `reconcileLayout` (`daemon.go:733-746`), i.e. phase 5's re-seed
sub-step in the general reconcile.

What that means for this milestone, stated honestly rather than optimistically:

- The **remote** resize works. `resize-pane -t %P -U 5` reaches the remote and the remote's layout
  changes; nothing about the ctl path or the verb table is implicated.
- The **mirror's repaint after it** is #231's bug. So the resize acceptance criterion — "the mirror
  converges" — is **gated on #231** and **must not be claimed as passing** until #231 is fixed.
  A resize that leaves a blank mirror is a #231 sighting, not an M2.3 regression, and equally not
  an M2.3 success.
- **M2.3 does not fix #231** — it is a separately filed issue with its own scope. But the general
  reconcile (B1) **must preserve the geometry-only re-seed branch** rather than dropping it while
  restructuring the switch, so that whoever fixes #231 does not have to re-find it. This is called
  out in phase 5 for that reason.

## Acceptance criteria

- [ ] In a bridge window, `prefix c` creates a window **on the remote**, and the mirror gains
      the corresponding local window; no local window is created by the keypress itself.
- [ ] **`prefix c` leaves the human on the new window's mirror** (Requirement 8): the remote makes
      the new window active (F9), and `reconcileWindows` — having added a window — `select-window`s
      its local mirror rather than leaving the human behind on the old one. Check the local *and*
      remote current window agree afterwards, and that a `prefix ,` or `prefix &` (which add no
      window) does **not** move the local selection.
- [ ] `prefix |` / `prefix _` split the **remote** pane; the mirror shows the new pane at
      matching dims, with its own renderer wired and painting.
- [ ] `prefix ,` renames the **remote** window; `@window_bridge_name` follows and the prompt
      seeds from the remote name.
- [ ] `prefix x` kills the **remote** pane (after confirming); the mirror loses the pane. If
      it was the last pane, the mirror window goes away.
- [ ] `prefix &` kills the **remote** window (after confirming); the mirror window goes away;
      the last window closing tears the daemon down cleanly (socket, pidfile, mirror session).
- [ ] The four prefix `M-<arrow>` resizes resize the **remote** pane. **Mirror convergence is
      gated on #231** (see "Inherited dependencies") — verify the remote side now; do not claim
      the mirror half until #231 is fixed.
- [ ] `prefix {` / `prefix }` swap the **remote** panes; the mirror's local pane order and its
      per-pane `@bridge_pane` mapping match the remote's, and each pane's *content* follows
      its pane. **Local focus lands on the pane the human was working in** — designed out rather
      than fixed up, via F7's flag pairing (remote swap **without** `-d`, local reconcile swap
      **with** `-d`), so this criterion is a check that the flags are right, not that a fix-up ran.
      Verify the exact flags in the shipped argv, since getting either wrong inverts the behavior
      silently.
- [ ] `prefix |` / `_` on a **non-last** pane, and `prefix x` on a **non-last** pane, both
      reconcile correctly — the mid-list insert/removal cases of F6, which the pre-M2.3 switch
      skipped. Also true for a *remote*-initiated split/kill of a non-last pane.
- [ ] **The general reconcile never empties a mirror window** (phase 0): a diff whose old and new
      pane sets are **disjoint** — reachable by a remote split followed quickly by killing the
      original pane — takes the tear-down + `setupWindow` fallback and leaves the local window
      alive with its renderers re-wired, rather than destroying it.
- [ ] Moving local pane focus in a bridge window moves the **remote** active pane, and does
      not oscillate. (Note the D1 limit: "does not oscillate" is the claim, **not** "the two
      sides always agree" — concurrent local+remote focus changes can settle disagreeing.)
- [ ] Input still routes by local focus (Requirement 4): typing in a mirror pane reaches that
      pane's remote counterpart, including immediately after a swap or a mid-list split.
- [ ] **Non-bridge behavior is unchanged** for every gated key: `c` still honours the
      scratchpad branch, `x` still runs `tmux-kill-pane-guard`, `&` still confirms, the
      `M-<arrow>` resizes still repeat (`-r`), and `,` / `{` / `}` behave exactly as
      next-3.8's defaults **including their `-N` notes** — the last of these asserted by the
      `list-keys` golden in `tests/tmux-next38-readiness.bats`, not only by eye.
- [ ] **The gate's truth table is asserted**, not assumed: `#{&&:#{@bridge_win},#{@bridge_pane}}`
      is true only with both options set, checked in a `@bridge_win`-tagged window and a plain one.
- [ ] No root-table binding the config did not already own has been added (Requirement 5) — in
      particular `MouseDrag1Border` stays unbound.
- [ ] A dead or stale daemon makes a gated keypress fail fast (non-zero, one stderr line)
      without stalling tmux's command queue.
- [ ] **Version skew fails legibly.** A ctl built at protocol N talking to a daemon at protocol
      M ≠ N prints one clear line and exits non-zero; a ctl talking to a **pre-M2.3** daemon
      (which closes the connection on frame type 6) prints the "does not speak the ctl protocol —
      reopen the bridge" line rather than a bare EOF or a hang. Exercised by pointing a new ctl at
      an old daemon binary, not only by unit test.
- [ ] **A `submit` that could not write does not ack success** — ctl exits non-zero when the
      daemon's `send` was a no-op (post-teardown race, `daemon.go:99-110`).
- [ ] `@bridge_sock` is present on the mirror session before the first mirror window exists;
      `@bridge_pane` is set on every mirror pane on **all** creation paths — startup, `addWindow`,
      and the general reconcile's append phase.
- [ ] The daemon contains **no bare `tmux` invocation**; every local command goes through
      `cfg.LocalTmux`.
- [ ] `nix flake check` passes in full; every hook M2.3 adds is cleared by the
      `set-hook -gu` idempotency block so `prefix + r` stays idempotent — specifically
      **`set-hook -gu after-select-pane` has been added** to that block
      (`config/tmux.conf.nix:767-787`), which does **not** contain it today.

## Testing strategy

### Unit (Go, table-driven)

1. **Verb translation** — `(verb, remotePane, remoteWinID, tracked scope state)` → the exact
   remote command argv, including the prelude decision. Cases: new-window, split -h, split
   -v, rename (name with a space, a `'`, and a `|`), kill-pane, kill-window, the four
   resizes, swap -U/-D, focus; plus prelude-needed vs prelude-skipped, prelude-needed **against a
   sentinel belief** (`""` must never suppress), and an unresolvable pane → error ack.

   **Assert the flags byte-for-byte, not just the shape.** The remote swap must be
   `swap-pane -t %P -U` with **no `-d`**, and — in test 5 — the local reconcile swap must be
   `swap-pane -d -s … -t …` **with** `-d`. F7 measured that `-d` inverts meaning between the two
   forms, so a flag typo here is a silent focus inversion that no geometry assertion can see.
   Also assert the **sentinel invalidation** each verb performs (D1's invalidation table): which
   belief each verb sets to `""`, and that `swap-pane` sets none.
2. **Echo-suppression state machine** — the D1 table, driven to a fixpoint.

   **The finite model is pinned so "every (state × event) pair" is actually enumerable:** panes
   `{A, B, C}`, FIFO depth `0..2`, `localActive` and `remoteActivePane` each ranging over
   `{A, B, C, ""}` **independently** — their disagreement is the interesting half, and `""` is the
   sentinel D1's invalidation table writes, so it must be in the domain — sequence numbers `0..2`.
   That is a finite state space the test can exhaust rather than sample, and the bounded-step-count
   assertion is meaningful over it.

   Cases it must cover: our remote echo; our local-select echo; the rapid A→B→C sequence (no
   flicker); a genuine external change during an outstanding command; a stale `localActive`; **the
   leak case** — a `focus` issued against a **stale** `remoteActivePane` lands on an
   already-active remote pane and so produces no report, and a later matching report must pop
   **through** the orphaned entry rather than leaving it to swallow a future external report (the
   belief-skip row makes this rare, it does not make it impossible, which is why it is tested);
   **the self-heal case** — `p == localActive` but `remoteActivePane != p` must still **send**;
   **the sentinel case** — `remoteActivePane == ""` must **always** send, for every `p`;
   **an out-of-order case** — a `focus` arriving with `seq` below the
   high-water mark is dropped and does not clobber a fresher `localActive`; the `@W ∉ registry`
   drop; **the `pendingFocus` path** — a `%window-pane-changed` naming a pane outside
   `mw.remotePanes` takes the belief, takes no local action, records `pendingFocus`, and is then
   **discarded** by the flush when the reconcile's ground-truth read disagrees with it and
   **already-applied** when it agrees; and **phase 4's write ordering** — both beliefs set from the
   read *before* the local `select-pane`, with `commanded` left untouched.

   **Not asserted:** that a single-slot variant *would* flicker. The earlier draft proposed pinning
   that as a regression lock; it is dropped, because asserting it requires keeping a rejected
   implementation alive in the codebase purely to test against. The rationale for the FIFO lives in
   D1's prose, which is where a reader will look.
3. **Intent queue** — dedup (two splits in one window coalesce to one `reconcileLayout`),
   drain-empties, add-during-apply lands in the next batch, the D3 **atomicity invariant** (an
   injected `send` closure inspects the queue at call time and asserts the intent is already
   registered), and the **B4 honest-ack** path (a `send` that reports not-written makes `submit`
   return false).
4. **`wire` framing** — round-trip `FrameCtl`/`FrameCtlAck`, NUL-separated argv with empty
   elements, empty payload, oversize rejection (`maxFrameSize`, `protocol.go:45`), and
   first-frame dispatch (a `FrameCtl` first frame is not mistaken for a Hello, and an unknown
   type closes the connection). Plus **protocol-version handling**: a mismatched argv[0] yields a
   descriptive error ack, and a daemon that closes without acking maps to ctl's one-line
   "does not speak the ctl protocol" error.
5. **The general reconcile's phase decomposition** — `(remote, newRemote, remoteActive)` → the
   ordered list of local commands. Cases: identity (geometry-only, re-seed sub-step fires and
   nothing else does); tail-append and tail-removal (today's behavior preserved); **F6's mid-list
   insert** (`%0 %1 %2` → `%0 %3 %1 %2`) and **mid-list removal** (`%0 %3 %1 %2` → `%0 %3 %2`);
   adjacent and non-adjacent permutations; a simultaneous add + remove + reorder; **phase 0's
   disjoint case** (`[%0]` → `[%3]`, and the general `remote ∩ newRemote = ∅`) yielding the
   tear-down + `setupWindow` fallback rather than any phase-1 kill; the **seed-dims index** for an
   appended pane whose `newRemote` position differs from its temporary local position; and the
   **phase-4 focus index** derived from `remoteActive` for each permutation (including the swap case
   where it is the identity, i.e. no command emitted).
6. **Verb whitelist** — an unknown verb yields an error ack and issues no remote command.

**Most of these tests will not run in CI unless a check is added — and the gap is narrower than
the earlier draft claimed.** `buildGoModule`'s `checkPhase` tests exactly the `subPackages` list,
non-recursively — `getGoDirs` echoes `subPackages` when set (nixpkgs
`pkgs/build-support/go/module.nix:322-335`, consumed at `:377-381`). `picker/default.nix:66` lists
`remotebridge` **and** `remotebridge/cmd/*`. So `remotebridge` at its **root is** a `subPackage`,
which means `picker/remotebridge/seed_test.go` **does** run today. The genuinely ungated packages
are `remotebridge/{daemon,wire,controlmode,render}` — which is still where nearly all of the
M2.1/M2.2 unit tests live, and where all of M2.3's will. The one existing Go-test check,
`agent-detect-go-tests` (`flake.nix:462` → `pickerAgentDetect`), overrides `checkPhase` to
`go test ./agentdetect/...` (`flake.nix:100-105`).

**Required:** add a `remotebridge-go-tests` check mirroring the `agent-detect-go-tests`
pattern — the same derivation with `checkPhase` overridden to `go test -race ./remotebridge/...`.
The **`-race` flag is not optional** (B5): the ctl goroutine + main loop sharing `ctlState` is the
first genuinely concurrent state in this codebase, and a lock-order or missed-`paneIndex`-update
regression is invisible to a functional test.

**Two plumbing details the override needs, both easy to miss.** An overridden `checkPhase` replaces
nixpkgs' own, which means it loses the `export GOFLAGS=''${GOFLAGS//-trimpath/}` that
`go/module.nix:370-372` provides — so **copy `flake.nix:102`'s line**, exactly as
`agent-detect-go-tests` already does. And `-race` **needs cgo**: nixpkgs' `buildGoModule` inherits
`CGO_ENABLED` from `go` (`go/module.nix:225`), which is `1`, so the requirement is satisfied by
default — but it must not be turned off for this derivation, because `go test -race` fails outright
without it.

**The new check is known not to import a pre-existing failure:** `go test -race ./remotebridge/...`
was run against this tree today and is **green** — all 5 packages ok, race detector included. So a
red `remotebridge-go-tests` after this milestone means M2.3 broke something, which is the only way
the check is worth having.

### Land it as two commits, not two PRs

The deliverable is **one PR**, structured as **two clearly separated commits**:

1. **The general layout reconcile** (phases 0-5, the widened `readLayout`, `reconcileWindows`, and
   the `remotebridge-go-tests` check). By F6 this is a **fix for a pre-existing M2.1/M2.2 hole**, not
   M2.3 scaffolding: it is fully exercisable by the existing bats harness plus the new unit harness,
   with **no ctl binary, no new tmux binding, and no config change**.
2. **The ctl + binding half** (the `wire` frames, `cmd/ctl`, the verb table, D1/D4's state, the
   binding table, and the `set-hook -gu` line).

**Why the split is worth the discipline:** the resize acceptance criterion is already un-evaluable
behind #231, and the mirror half of several others is geometry-only. If a mirror regression showed up
in a combined change it would have three candidate causes — the reconcile rewrite, the ctl path, or
#231 — and no way to bisect between them by reading the diff. Two commits make the first bisectable
on its own, against a tree where every existing bats case still applies unchanged.

### Local integration in `nix flake check`

Extend `tests/remote-m2-integration.bats`, which already drives the daemon's `--test-local`
seam against two private `tmux -L` servers and **must** use the pinned next-3.8 tmux
(`mkTmux`, not `pkgs.tmux` 3.7b — `flake.nix:544-549`; `%window-close` differs). Add
`CTL = "${pickerAgentDetect}/bin/lztmux-remote-bridge-ctl"` beside `DAEMON`/`RENDERER`
(`flake.nix:550-551`) and the matching `go build` fallback in `setup()`
(`remote-m2-integration.bats:35-42`).

The test invokes the **ctl binary directly** against the daemon's socket with explicit args.
That is the correct seam: **these** servers run vanilla configs with no lazytmux keybindings, so
there is nothing to gate here. That is a property of this harness by design, not a property of CI
as a whole — see the next subsection, which is where the tmux-config half gets covered.

| Case | Assertion on the "remote" (SRC) | Assertion on the mirror (DST) | What it can see |
|---|---|---|---|
| ctl `new-window` | SRC window count grew; SRC's active window is the new one (F9) | DST window count grew, **and DST's current window is the new window's mirror** (Requirement 8) | count, plus the selection follow-through |
| ctl `split -h` | SRC window has 2 panes | DST has 2 panes at `sorted_dims` parity | geometry only |
| ctl `split -v` on a **non-last** pane of a 3-pane window (F6) | SRC pane order is a mid-list insert | DST `list-panes -F '#{pane_index} #{@bridge_pane}'` matches SRC's index→id mapping | **identity** — the general reconcile's insert path |
| ctl `kill-pane` on a **non-last** pane (F6) | SRC pane order is a mid-list removal | DST index→`@bridge_pane` mapping matches SRC | **identity** — the removal path |
| ctl `rename` | SRC `#{window_name}` == new name | DST `@window_bridge_name` follows (the *option*, per `2026-07-22` design — a vanilla server runs no reflow) | option identity |
| ctl `kill-pane` | SRC pane count dropped | DST pane count dropped | geometry only |
| ctl `resize -U 5` | SRC pane heights changed | DST `sorted_dims` == SRC | geometry only |
| ctl `swap -U` | SRC `list-panes -F '#{pane_index} #{pane_id}'` order permuted | DST `list-panes -F '#{pane_index} #{@bridge_pane}'` matches SRC's index→id mapping | **identity**, not geometry |
| ctl `swap -U`, content | write a marker into remote pane A (`send-keys 'echo MARKER_A' Enter`), then swap | `capture-pane -p` the DST pane at A's *new* index contains `MARKER_A` | **content** |
| ctl `focus` | SRC active pane == the requested pane | DST active pane still the same local pane (no bounce) | single-hop only |
| ctl against a dead socket | — | ctl exits non-zero within the deadline | the queue-safety guarantee |

**The new identity assertions need base-index normalization, or they fail for the wrong reason.**
`remote-m2-integration.bats:29-31` sets `pane-base-index 1` on **both** SRC and DST configs, while
the daemon forces `pane-base-index 0` on every mirror window it creates (`daemon.go:195`,
`daemon.go:396`, with the reason in the comment above the first). So SRC's `#{pane_index}` starts at
**1** and DST's mirror-window `#{pane_index}` starts at **0**: any assertion comparing SRC's
index→id mapping against DST's `#{pane_index} #{@bridge_pane}` is off by one out of the box. Every
row marked **identity** above must normalize — compare the *ordered sequence* of ids rather than the
index→id pairs, or subtract SRC's `pane-base-index` — otherwise the three new identity cases fail on
a harness quirk and get "fixed" by weakening them back to geometry.

**Honest limits.** Every case above except the two marked otherwise asserts **geometry
only** — pane dims and counts. That is exactly the class of assertion that let the #196
naming bug pass CI (`2026-07-22-bridge-window-name-design.md`, "Why CI misses it"), which is
why the swap case carries both an identity assertion (`@bridge_pane` mapping — the only
observable that proves renderer↔remote-pane wiring survived) and a content assertion. The
precedent for content assertions is the #183 case
(`remote-m2-integration.bats:449-489`), which does read pane content. **Loop termination is
not asserted by bats** — a single ctl `focus` proves one hop, not the absence of a cycle; the
proof lives in unit test 2.

Do **not** remove the `select-window -t rem:1` workaround at
`remote-m2-integration.bats:266-275`. It guards the D5 limitation for the *remote*-initiated kill,
which M2.3 does not fix (`&`'s intent heals only the locally-initiated case — see D5).

### The tmux-config half IS gated — `tests/tmux-next38-readiness.bats`

**Correction to the earlier draft**, which said the tmux-config half of M2.3 is "not covered by CI
at all". That is true only of the *bats bridge* harness above, whose servers are vanilla by design.
It is **not** true of CI as a whole: `checks.tmux-next38-readiness-tests` (`flake.nix:479-493`)
already drives the **wrapped** tmux headlessly inside `nix flake check` — `TMUX_BIN =
tmuxConfig.tmux-wrapped`, i.e. the full generated config, real plugin store paths, real
keybindings. Writing the config half off as unverifiable would have abandoned a working harness to
a manual sweep.

**Required additions to `tests/tmux-next38-readiness.bats`:**

1. **A `list-keys` golden for every default this milestone newly takes ownership of** — `,`, `{`,
   `}`. This is what converts F5 from a hand transcription of `key-bindings.c` into a machine-checked
   claim: it fails loudly on the next `tmux-upstream` bump if upstream changes a default or a note,
   which is precisely the drift a human sweep will miss on the third repetition. The file already
   reads `list-keys -T prefix` for a similar purpose (`tmux-next38-readiness.bats:141-142`), so the
   idiom is established.

   **Split it into two assertions, because `list-keys` prints the whole `if-shell`.** The gated bind
   is one `bind-key` whose command is an `if-shell` with both branches inline, and the `-N` note
   attaches to the **bind**, not to a branch — so a single byte-match against the upstream default
   line can never pass. Assert instead:

   1. **the note** — the bind's `-N` text equals the next-3.8 default's `-N` text for that key;
   2. **the else-branch** — the command list in the `if-shell`'s else position equals the next-3.8
      default's command list for that key.

   Together those are the whole of "behaves exactly as the default when the gate is false, and
   presents like the default in `?`/which-key", which is what the acceptance criterion claims.
2. **A gate truth-table assertion.** In a `@bridge_win`-tagged window versus a plain one, check
   `display-message -p '#{&&:#{@bridge_win},#{@bridge_pane}}'` for all four option combinations.
   This pins F2's zero-blast-radius property as a test rather than a probe memory.

**What this still cannot see**, and what therefore stays with the hardware run: real prefix
keypresses through an attached pty, `confirm-before` / `command-prompt` interaction, which-key
presentation, and the live interaction with `automatic-rename` + reflow + the 1 s icon tick.

### Two-machine hardware run (g5 → tp-g6) — human only

Nothing below can be verified by CI or by a single machine:

1. **Real prefix keypresses on the real wrapped config.** The `if-shell` gates, `-r` repeat
   behavior, `confirm-before` prompts, `command-prompt` seeding, and the interaction with
   `automatic-rename` / reflow / the 1 s `tmux-update-icons` tick exist only under the wrapped
   tmux. This is the exact blind spot that hid #196.
2. **Non-bridge regression sweep**: `c` in a `scratch-*` session, `x` on an idle shell vs a
   working Claude pane (the guard), `&`, all four `M-<arrow>` repeats, and `,` / `{` / `}` in
   a normal window including their `?`/which-key notes. (The `list-keys` golden now covers the
   binding *text*; this covers the *presentation* and the interactive prompts.)
3. **Perceived latency** of gate → ctl → ssh → remote → notification → reconcile for a split
   and for focus.
4. **Interaction with #204's size model.** #204 is now itself two-machine verified (2026-07-30 —
   see "Inherited dependencies"), so this is a check that M2.3's verbs compose with a verified
   contract, not a check of the contract itself. M2.3 does not depend on #204 beyond treating
   `FitWindowCmd`'s `window-size manual` behavior as given. **Note #231 while resizing:** a blank
   mirror after a geometry-only change is the known open bug, not an M2.3 finding.
5. **`after-select-pane` under a real attached client and real load** — specifically a fast
   `C-j`/`C-k` burst, which is the case D1's seq guard exists for.
6. **Focus after a swap** (F7's flag pairing): `prefix {`, then confirm the cursor is on the pane
   the human was working in and that typing goes to it. This is the end-to-end check that the
   remote swap went out **without** `-d` and the local reconcile swap **with** it.
7. **`prefix c` window selection** (Requirement 8): the human ends up on the new window's mirror,
   and a `prefix ,` / `prefix &` afterwards does not move them off whatever window they navigated
   to with `M-H`/`M-L`.
8. **Version skew**, once: point a freshly-built ctl at a still-running older daemon and confirm the
   one-line error rather than a silent no-op.

Do not claim CI-green or hardware-verified without having run it and seen it.

## Probe during implementation (10 minutes, may improve D1)

`window-pane-changed` exists as a **window-scope hook**
(`options-table.c:2043`), fired from `window_fire_pane_changed`
(`window.c:112-128`), which `window_set_active_pane` calls on **every genuine active-pane
change** (`window.c:711-741`) — regardless of which command caused it. On a code read that
makes it strictly more complete than `after-select-pane`: it covers `prefix ;` /
`last-pane` (which `after-select-pane` misses — see the binding-table note), pane death, and
any future path that changes the active pane without going through `select-pane`.

This is a **source read, not a probe** — F3 verified `after-select-pane` and `pane-focus-in`,
not this. So: `after-select-pane[10]` is the settled primary, and implementation should spend
ten minutes probing whether `window-pane-changed` fires reliably with and without an attached
client and is settable globally for a window-scope hook (`set-hook -g` vs `-gw`). If it is,
prefer it — the gate, the ctl verb, and the D1 machine are unchanged, and the `prefix ;` gap
closes for free. If the probe is ambiguous, keep `after-select-pane` and leave the gap
documented.

**That is the only probe item left.** An earlier draft also asked implementation to confirm
`new-window -t '<sess>:'`'s insert position; that is now measured (F9 — **lowest** free index, and
the new window becomes active), so it is a fact, not a task. Every target form and every flag in
this spec is now measured (F1, F6, F7, F8, F9) or source-cited.

## Non-goals

- **M2.4** — copy-mode, mouse passthrough, `send-keys -M` forwarding, OSC 52 via
  `%paste-buffer-changed`, focus events, and the `MouseDown3Pane` mega-menu question.
  **All of border-drag** is deferred here: M2.3 neither syncs the gesture nor binds it, so a
  border drag stays a self-reverting local resize.
- **M3** — `prefix + s` picker remote section; retiring the arch-C reverse socket (#155).
- **Reconnect after link drop** (laptop sleep) — a standing M2 non-goal.
- **kitty graphics** through the bridge — the remote tmux eats the DCS.
- **The D5 plumbing refactor** — the *scoped* variant is recommended as the next follow-up issue,
  not part of M2.3.
- **#231** (mirror blanks on a geometry-only re-seed) — separately filed; M2.3 preserves the branch
  it lives in but does not fix it, and the resize acceptance criterion is gated on it.
- **Local→remote window-selection mirroring** as a general hook (D4 ships only the prelude).
- **Shared co-attach** with a human on the remote — D4's prelude will switch their window.
- **A liveness timer** for stranded intents — a stalled-but-open ssh link freezes the drain (D2).

## What the next milestone inherits

- **Both M2 open questions are settled.** Echo suppression is a two-guard scheme, one guard per
  direction — a bounded commanded-focus **FIFO** (popped through-and-including the match, so a
  no-report command cannot leak) plus **two distinct beliefs**, `remoteActivePane` guarding what
  the daemon sends and `localActive` guarding what it acts on — made order-safe by a per-window
  sequence guard, with loop termination proved by a table-driven unit test over a pinned finite
  model rather than by tmux's current notification-swallowing. The command channel is the **existing unix socket** with two new
  `wire` frames (`FrameCtl` / `FrameCtlAck`), dispatched on the connection's first frame, spoken by
  a new `lztmux-remote-bridge-ctl` binary — **explicitly versioned**, because a config reload hands
  a new ctl to an old long-lived daemon.
- **Beliefs are invalidated and re-learned, never guessed.** The pattern any future verb must
  follow: a verb whose remote side effect the daemon cannot name at command time sets the affected
  belief to the sentinel `""` (which fails **open** through every equality guard), and the reconcile
  re-learns it from ground truth at **zero extra round-trip cost** — `readLayout` carries
  `#{pane_id}` beside `#{window_layout}` (F8) and `reconcileWindows`' `list-windows` carries
  `#{window_active}` before `#{window_name}`. That is what lets the reconcile apply focus
  authoritatively instead of re-asserting a local opinion, and it is why the `commanded` FIFO is now
  belt-and-braces rather than the primary mechanism.
- **A reusable gate idiom**: `if-shell -F '#{&&:#{@bridge_win},#{@bridge_pane}}'` costs one
  format expansion and no fork on non-bridge windows (`cmd-if-shell.c:87-102`), with
  `@bridge_sock` (session), `@bridge_win` (window) and `@bridge_pane` (pane) as the three
  daemon-owned carriers — replacing the M2 design's single `@bridge_ctl`. M2.4's mouse and
  copy-mode bindings can use it verbatim, and its truth table is now asserted in
  `tmux-next38-readiness.bats`.
- **A reusable "command-then-reconcile" contract**: a socket goroutine may only `send` +
  register an intent under `ctlState.mu`; the `controlmode.Reader` stays single-owner on
  the main loop because reply consumption is positional. **Lock order `ctlState.mu` → `sendMu`,
  never the reverse**, and the ctl goroutine never touches a `mirrorWindow` — it reads `paneIndex`
  instead, because `mirrorWindow.remotePanes`/`.conns` are mutated unlocked by the main loop
  (`daemon.go:315`, `:755`, `:776`, `:797`, `:818`, `:822`, `:828`). Any future
  non-main-goroutine command must obey all three.
- **`reconcileWindows()`**, a full window-set reconcile, alongside a **generalised
  `reconcileLayout`**: one guard→remove→append→permute→focus→geometry path replacing the old
  three-case-plus-give-up switch. It keeps the "local pane i renders `remotePanes[i]`" invariant
  under any diff, and it **fixes the pre-existing M2.1/M2.2 hole** where a remote split or kill of
  a *non-last* pane left the mirror silently stale (F6). Phase 0's disjoint-set guard is the part to
  carry forward if the phases are ever extended: generalising a reconcile is what makes "kill every
  local pane" reachable, and killing a window's last pane destroys the window.
- **The named gaps**, so M2.4 need not rediscover them: `MouseDrag1Border`,
  `M-MouseDrag1Border`, and the `MouseDown3Pane` mega-menu all still act locally (M2.3 binds none
  of them); border-drag needs end-of-drag debouncing plus a new local-layout→remote translation;
  `prefix ;` and the `M-H`/`M-L`/`M-J`/`M-K` window nav change local state with no remote
  counterpart (and `window-pane-changed` is the candidate seam for the first, code-verified but
  unprobed); D1 terminates without always converging; a stalled-but-open link strands an intent.
- **The D5 follow-up, re-scoped to the variant that actually survives scrutiny**: re-dispatch
  **top-level** block bodies through the existing main-loop switch for the **additive**
  notification kinds only — which by conservation needs no command-number correlation, no
  `capture-pane` disambiguation, and no flags bit — while keeping intents for the **destructive**
  kinds, because a pane printing `%window-close @1`-shaped text must never kill a mirror window.
- **A CI gap worth closing regardless**: `remotebridge/{daemon,wire,controlmode,render}` unit
  tests are not run by `nix flake check` (nixpkgs `go/module.nix:322-335` + `picker/default.nix:66`);
  `remotebridge` at its root **is** gated, so `seed_test.go` does run. The new check must use
  `-race`.
- **Two inherited facts about the size model**: #204 is two-machine verified as of 2026-07-30, and
  **#231 is open** in `reconcileLayout`'s geometry-only re-seed branch — the general reconcile
  preserves that branch so the fix stays findable.
