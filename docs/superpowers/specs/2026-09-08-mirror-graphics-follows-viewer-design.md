# #574 — the mirror's graphics identity follows the viewing client

## Problem

The launcher samples the **invoking** client's graphics identity once, at launch
(`scripts/lztmux-remote-open.sh:475`, one `display-message -t $TMUX_PANE
'#{client_termname}|#{client_termfeatures}'`), and the daemon freezes it for its
whole life. Two consumers then run on a value that stops being true the moment
the user views that mirror from another terminal:

| frozen value | route | consequence when the viewer differs |
|---|---|---|
| `LZTMUX_BRIDGE_TERM` | `-term` → `sshControlArgs`' `TERM=` prefix → remote `client_termname` → aeye's `chooseRelayBackend` | the remote emits **kitty** placements to a terminal that cannot paint them: a grid of `U+10EEEE` tofu (#574's symptom) |
| `LZTMUX_BRIDGE_TERMFEATURES` | `-termfeatures` → `graphics.RelayFromTermFeatures` → `Config.Relay` → the local sixel drop gate (`Proxy.Filter`) **and** `LZTMUX_RELAY_GRAPHICS` (#565) | a sixel the viewer *could* paint is dropped, because the launching terminal had no `sixel` feature — the same defect in #565's half |

One cause: the bridge advertises the terminal that *opened* it, not the one
*looking* at it. Measured on this system, all three live bridges carried
`LZTMUX_BRIDGE_TERM=xterm-kitty` while the viewing client was foot.

## Evidence (measured, not assumed)

All on tmux 3.7c, the version this repo pins.

**E1 — a control client's `client_termname` is exactly its `TERM`, and there is
no runtime setter.** A scratch control client attached with `TERM=xterm-kitty`
reports `client_control_mode|client_termname|client_termfeatures` =
`1|xterm-kitty|`; the same attach with `TERM` unset reports `1|unknown|`.
`refresh-client`'s own usage string, read out of the pinned binary, is
`[-cDlLRSU] [-A pane:state] [-B name:what:format] [-C XxY] [-f flags] [-r
pane:report] [-t target-client] [adjustment]` — no termname among them, and no
client-scoped option carries one. **The only way to change the termname the
remote sees is a new control client.**

Corollaries this settles:
- `client_termfeatures` is *always* empty for a control client, so the relay
  capability can only ever be sourced from a **local** client — which is what
  the launcher already does and what this change keeps doing.
- A second control client held *alongside* the first is not a way to change the
  advertised value: aeye's `parseRelayTermName` takes the **first non-empty**
  termname among all-control clients, so which one wins depends on
  `list-clients` order. (It is still the right way to *replace* it — E6.)

**E2 — aeye picks the backend once per *viewer launch*, from a live read.**
`runGallery` (aeye `gallery.go:1135-1148`) computes `relayTermName()` → `tmux
list-clients -t $TMUX_PANE -F '#{client_control_mode} #{client_termname}'`; when
**every** client of that session is control-mode it overrides `termName()` and
feeds `chooseRelayBackend` (aeye `gallery_render.go:361`), which returns
`backendKitty` only for an `xterm-kitty`/`xterm-ghostty` prefix and
`backendSymbols` (block art) otherwise. It is never re-evaluated for the life of
that viewer process.

**E2a — injecting `TERM` into the viewer's own launch environment does not
work.** The daemon already composes that command line and already injects env
into it (`carouselResolveScript`, `ctl.go:393`: `exec env TMUX_PANE="$src"
AEYE_BRIDGED=1 "$bin"`), so this is the cheapest imaginable channel — but per
E2 the `client_termname` read overrides `termName()`, whose `TERM` fallback is
reached only when the tmux query *fails*. This channel cannot steer the backend
without an aeye change.

**E3 — session-scoped hooks on the mirror session, measured.** A real client
moved between two sessions where only session `B` carried hooks, plus a genuine
`detach-client`:

| event | switch a client **to** B | fresh **attach** to B | switch **away** from B | genuine **detach** from B |
|---|---|---|---|---|
| `client-session-changed` | **fires** | **fires** | — | — |
| `client-attached` | — | **fires** | — | — |
| `client-resized` | — | **fires** | — | — |
| `client-detached` | — | — | — | **fires** |

`client-session-changed` covers **both** ways a client arrives; `client-detached`
covers a real departure (a closed terminal); only a `switch-client` *away* is
unobservable on the session being left.

**E4 — `LZTMUX_RELAY_GRAPHICS` has no consumer yet.** `rg` over
`/home/noams/Data/git/noamsto/aeye` finds no reference, and #565's own plan says
so outright ("the aeye half is not in this PR"). A fix for the **kitty** symptom
routed through a *new* env var would therefore be inert.

**E5 — the control client's `TERM` steers nothing tmux itself renders.** Remote
*panes* take `TERM` from the remote tmux's `default-terminal`, not from the
attached client, and a control client renders no status line. The value is
observable only to a program that reads `#{client_termname}` /
`client_termfeatures` (or the remote session env `TERM`, which
`update-environment` refreshes per attach) — in practice, on a bridged host,
aeye. This is what lets the two halves below have different triggers; it is a
statement about who *does* read it, not a proof that nothing else could.

**E6 — a second, parallel control client is structurally harmless.** Measured on
a scratch server configured like the real one (`window-size latest`,
`aggressive-resize on` globally, `off` on the mirrored window, the window capped
at 200x50 by `refresh-client -C`): attaching a second control client with no
size of its own left the window at **200x50** and produced, on the first
client's stream, exactly one line — `%client-session-changed <client> $0 rem` —
with **no** `%layout-change` and no resize. Two hazards ruled out at once:
- The second client does not become the sizing authority, so there is no
  80-column resize storm on the live connection.
- `%client-session-changed` cannot be mistaken for `%session-changed`:
  `controlmode/parse.go:162` switches on the **exact** token, so the new
  notification falls through to the unknown default and never reaches
  `sessionPin.apply`, where it would have looked like the bridge being switched
  off its own session.

**E7 — closing the sized client does not shrink an existing window, but the
per-client caps really are gone.** Same harness, carrying E6 one step further —
the step that moves `w->latest`. With the sized+capped client killed and the
unsized second control client left as the *sole* client, the window stayed
**200x50** and the survivor's stream saw **no** `%layout-change`. So a control
client that was never given a size does not drag an existing window down: the
80-column default bites a window *born* under it (#449's own measurement, and
what `ClientSizeCmd`'s doc records), not one already sized.

What is nonetheless true is that the client size and every per-window cap are
**per-client** state — `cv.reset()`'s comment says so outright ("a fresh control
client has been told nothing: not the client size, not one per-window cap",
`size.go:100-106`) — while `AggressiveResizeOffCmd` is a window *option*
(`set-option -w`, `size.go:35`) and survives. So between the swap and
`repair()`'s size sends there is a real, narrow #449 window: a window the remote
*creates* in that interval is born at 80x23. R7 orders the size sends ahead of
the close to close it.

## The seam, and why

**A purely local seam cannot fix the kitty symptom.** Per CLAUDE.md's Bridge
Graphics section, kitty Unicode placeholders are *ordinary grid text*: they cross
on the normal text path and the proxy never sees them as a sequence. Dropping the
store APC locally therefore leaves the tofu cells exactly where they are — it
does not even buy a blank area. Nor can the proxy transcode: it holds the image
bytes, but a kitty placement is a position-independent placeholder grid while a
sixel is painted at the sender's cursor, and `graphics` has no encoder. **The
remote has to pick a different backend.**

**Two halves, two mechanisms.** E5 is what makes this clean.

**Half 1 — the relay capability (sixel): fully local, continuous, no transport
surgery.** The `Proxy.Filter` drop gate and the published
`LZTMUX_RELAY_GRAPHICS` both derive from the *current* set of clients attached to
the mirror session, and a change re-publishes at once. This half serves *any*
remote program using #565's relay, needs nothing on the remote, and is where
most of the defect's real cost lies.

**Half 2 — the advertised termname (kitty): replace the control client, on the
carousel gesture.** Per E1 a new termname means a new control client; per E2 the
backend is chosen per *viewer launch*; per E5 nothing else reads the value. So
the replacement belongs at the one moment a viewer is about to launch: the
`carousel` ctl verb (`ctl.go:243`), which the daemon already sees for every
`prefix + I` across a mirror.

Triggering there rather than from a poll is what makes the fix **deterministic
rather than racy** — a polled re-dial can still be in flight when `prefix + I`
lands, and aeye would read the stale termname and paint tofu, which is the
symptom itself. It also bounds the cost: nothing for a user who never opens a
carousel, nothing on ordinary session switching, and at most one replacement per
human gesture.

Rejected alternatives, on the record:
- *Local re-select only* — cannot work (above).
- *Injecting `TERM` into the viewer's launch env* — cannot work (E2a).
- *A polled re-dial on client events* — racy against `prefix + I`, and pays a
  disconnect badge, an input-loss window and a full reseed per terminal switch.
- *Close-then-dial (`reattach`'s own order) for the voluntary case* — rejected on
  safety: see R7. `reattach` closes the live connection before it dials
  (`conn.go:206-209`) and returns `nil` on an exhausted budget or a failed
  identity read, after which `Run` reaches `teardown()` and its `kill-session -t
  LocalSess` (`daemon.go:736`). A voluntary re-dial on that order would destroy a
  working mirror whenever a *fresh* handshake fails — a strictly larger set than
  "the remote just died", since the current transport's `ControlMaster` is
  `ControlPersist=no` and the new dial re-authenticates from scratch: a lapsed
  agent key or a re-armed Tailscale ACL `"check"` rule (both documented in
  CLAUDE.md) passes any probe of the old master and then fails to authenticate
  with no tty. **No probe of the existing connection is evidence about the next
  handshake**, which is why R7 dials first instead of probing.
- *A new capability env var + an aeye `chooseRelayBackend` change* — the
  consistent successor to #565, and the issue's own sketch ("pushing an updated
  `TERM` to the remote session... and having the viewer re-evaluate"), but inert
  against the tofu until aeye lands (E4), so it cannot satisfy this issue's
  acceptance. **Recorded as the follow-up**: it is the only mechanism that could
  re-pick for an *already-open* viewer, and it would let Half 2 be deleted.

## Requirements

**R1 — one resolved viewing identity, from local clients only.** The daemon
resolves, from the clients currently attached to its **own mirror session**
(`LocalSess`), a *viewing identity*: an advertised termname plus a
relayable-graphics capability. **Control-mode clients are excluded** — their
`client_termfeatures` is always empty (E1), so including one would force the
capability false and could win the termname; aeye's analogous reader filters the
same way (`gallery.go:1262`), and `lztmux-remote-open.sh:464` already
contemplates a control-mode invoker.

The advertised termname is **seeded from this same resolver at startup**, not
from the launcher's `-term` flag. The launcher samples the invoking client via
`display-message -t $TMUX_PANE` (`lztmux-remote-open.sh:475`) while R1/R2 resolve
the clients of `LocalSess` by the lexicographic-smallest rule; they agree in the
common case, and seeding from one resolver is what makes acceptance 2's "never
nacked" a property of the code rather than a coincidence. The flag stays the
fallback for the startup instant before the mirror session has a client (R3).

**R2 — the multi-client rule is capability intersection.** Whatever is
advertised must be paintable by **every** attached client, so each capability is
the **AND** across them:
- `kitty` = every client's termname is `xterm-kitty`/`xterm-ghostty`-prefixed.
- `sixel` = every client's `client_termfeatures` carries a whole `sixel` token.

The advertised **termname** is the lexicographically smallest termname among the
clients that *witness* the AND'd kitty capability — the smallest overall when all
are kitty-capable, the smallest non-kitty-capable one otherwise (which exists by
construction). Lexicographic, not `list-clients` order: the identity must be a
pure function of the client *set*, or an unstable order alone would look like a
change.

**R3 — no attached client keeps the last identity.** A mirror nobody is looking
at resolves to nothing, and nothing is not evidence: the daemon keeps what it
last advertised rather than degrading a mirror to block art because the user
switched away. This is also what makes E3's unobservable `switch-client`-away
harmless.

**R4 — the relay gate follows the viewer, locally and immediately.** The sixel
drop policy in `Proxy.Filter` and the value published as
`LZTMUX_RELAY_GRAPHICS` must both read the *current* resolved capability and
must remain **one single value** (#565's R6: the two can never disagree —
`graphics/relay.go:5-9`).

**R5 — a capability change re-publishes immediately, independent of any
attach.** One fire-and-forget `set-environment`; no re-dial. Without this, R4 is
unsatisfiable: clients `{foot(sixel), xterm-kitty(no sixel)}` advertise
`sixel=false`, and the kitty client leaving flips the capability true while the
*termname* is unchanged — so no attach and no re-dial occur, and a
per-attach-only write would leave `LZTMUX_RELAY_GRAPHICS` stale while
`Proxy.Filter` relays. `RelayEnvCmd` must **also** still be re-sent on every
attach, for the reconnect-repair case (it is sent once per `Run()` today,
`daemon.go:602`).

**R6 — Half 2 is raised by the carousel verb without deferring any command
across goroutines, and without blocking under `ctlState.mu`.** Two constraints
force the shape:

- *No deferred command.* `runConn`'s select has exactly two arms —
  `<-c.pump.lines` and `<-loopTick.C` (`daemon.go:970-986`) — and
  `mainLoopTickInterval` is 5s (`windowlabels.go:29`). Every existing ctl
  request needs no wake-up only because its command is sent, and "the reply
  block for our own command is itself the line that wakes the loop"
  (`ctl.go:19-23`). Withholding the carousel command removes exactly that, so
  the raise must add its **own** select arm: a session-lifetime buffered
  channel, session-lifetime for the same reason `loopTick` is
  (`daemon.go:1077-1080`).
- *No blocking under the lock.* `submit` sends inside `ctlState.mu`
  (`ctl.go:472-506`), and the main loop takes that same mutex during a
  replacement — `repair()` → `reconcileWindows` → `cst.forgetWindow`
  (`daemon.go:1038`, `ctl.go:76`). A handler that waited for main-loop progress
  while holding `mu` would deadlock, inverting `ctlState`'s documented
  `mu -> sendMu` order with a third edge. So the resolve/compare/raise happens
  **before** `submit` and outside `mu`, and no ctl handler may block while
  holding it. `ctlState`'s lock-order comment says so.

Therefore this press is **nacked, not queued**: the handler resolves the
identity, and when it differs it raises the replacement and returns an error
telling the user to press again — a distinct message, never the generic
`bridge has no live connection to the remote`, which reads as "your bridge is
broken" when the truth is "your viewer identity is being updated". The *next*
press is deterministic, and one keypress is a far smaller price than a
cross-goroutine command queue with an owner, a flush point after `repair()`, and
a defined behaviour for a re-dial that fails after the keybind was already
acked.

**R7 — the replacement dials, verifies, then swaps; it never closes a working
connection it cannot replace.** Order: dial the new control connection, read its
identity on it while it is still **unbound** from the Router (`newCtlConn`
exists for exactly this, `conn.go:27-43`), check it against the recorded
identity, then — still unbound — `cv.reset()` and send `ClientSizeCmd` plus each
mirrored window's `ConvergeCmd` on the new connection, then **close the old
connection, then bind and publish the new one**, then run the existing
`repair()`.

The size sends lead the close for the reason E7 measures: they are per-client
state the new client has never been told, and a window the remote *creates*
between the close and `repair()`'s own sends would be born at tmux's 80x23
control-client default (#449). They are safe to send early — they assert exactly
the size already in force, so even if `refresh-client -C` makes the new client
`w->latest` immediately, nothing moves (E6/E7 both measured no movement). They
are also cheap: round-trips already run on the unbound connection, which is how
`readIdentity` works. `repair()`'s own size sends then collapse to `cv.need`
no-ops, so `cv.reset()` must not be repeated inside `repair()` on this path.

Note what E7 does **not** license: this ordering is not protection against an
existing window shrinking, because that does not happen. An implementer must not
write a test asserting a resize the measurement says tmux never performs.

- Dialling first is what removes the hazard R7's rejected alternative documents:
  a failed dial or a mismatched identity aborts the replacement with the old
  connection still live and streaming, so the press degrades to the old backend
  for this one gesture and the mirror is untouched. No `@bridge_state
  disconnected`, no input-loss window, no `kill-session`.
- Closing the old one *before* binding the new one is equally load-bearing: both
  connections stream the same remote panes, so a window where both are bound
  routes duplicate `%output` into one sink. The gap between close and bind is
  two statements on one goroutine; output the remote drops in it is what
  `repair()`'s reseed already exists to restore.
- E6 is what licenses the overlap at all.
- `repair()` is reused wholesale rather than trimmed. Its reseed is largely
  redundant here (little or no output was dropped), but the fresh client
  genuinely has no subscriptions, no `pause-after` and no converger state, and a
  bespoke partial repair would be a second, untested copy of the most
  order-sensitive code in the daemon.

**R8 — a replacement is attempted only when it can help.** All of:
- the advertised termname actually differs from the resolved one (an unchanged
  termname — the same terminal, any number of session switches — costs nothing);
- the resolution is non-empty (R3);
- the daemon has reconnect capability — reuse `Run`'s existing
  `reconnect := cfg.Dial != nil && pin.identityKnown` (`daemon.go:565`) rather
  than re-deriving it;
- no replacement is already in flight. One at a time — and a press that lands
  during the window gets the **same** press-again message as the discovering
  press, never `bridge has no live connection to the remote` (`daemon.go:648`).
  A second press is the likeliest user behaviour, and `submit`'s contract is
  "reports whether the command was actually written" (`ctl.go:468-471`), so both
  nacks travel one channel and only the text distinguishes them.

The nack is returned when the replacement is **raised**, not when it completes:
a message that arrives after a multi-second pause reads as a timeout rather than
an instruction.

**R9 — the ssh `ControlPath` becomes per-dial, reached through an accessor.** It
is pid-derived and captured in closures today (`cmd/daemon/main.go:201-205`,
`:307-320`, `:346`) precisely because it had to be stable across re-dials — but
R7 needs two live masters at once, so the path must be per-dial and every
consumer (the graphics fetcher, the paste upload, and `cleanup`) must read the
current one through the same accessor R11 introduces. This also retires, rather
than merely avoids, the documented "a stale socket silently disables
multiplexing" hazard: a fresh path per dial has nothing stale to meet.

Three consequences the accessor has to carry:
- The unconditional `os.Remove(ctlSock)` at every dial (`main.go:234-236`) was
  also the garbage collector for a socket left by an ssh child killed without
  catching a signal, and `ControlPersist=no` unlinks only on a clean exit. So
  the old path is unlinked when the old connection is closed, and `cleanup`
  (`main.go:219-223`) covers the current one — otherwise `/tmp` gains a socket
  per replacement.
- The path must stay short: a `ControlPath` at or over 108 bytes fails, and a
  per-dial suffix is being added to a `TempDir`-rooted, pid-derived name.
- The accessor has to be read **inside** the fetcher and the paste closure, not
  merely passed to `newGraphics`. `graphics.NewSSHFetcher` takes the path by
  value (`main.go:370`) and reads it in `fetch` (`graphics/fetch.go:170`), and
  proxies are built per pane at wiring time — so a proxy constructed before a
  replacement would hold a dead path forever unless the fetcher itself reads the
  accessor. Same for `pasteUpload` (`main.go:307-320`).

**R10 — the observer is the existing local-client watcher.** Half 1's capability
re-resolution rides the tick the daemon already runs: a hook-touched nudge file
polled by `os.Stat` once a second, forking a query only when the mtime advanced,
with a 30s fallback (`watchResize`, `resizeNudgeSuffix`, `registerResizeHook`).
`client-session-changed` and `client-detached` join `resizeHookEvents` per E3.
Stated side effect: every session switch and detach now also runs
`watchResize`'s `area()` fork and converge pass — `cv.need` dedupes the sends,
so it is cheap but no longer nothing. Half 2 does **not** ride this tick: it
resolves synchronously in the ctl handler, which is fresher than any poll and is
what makes R6 deterministic.

**R11 — shared values are read atomically.** The termname is written by the
resolver and by the ctl handler, and read by the dial closure on the main loop;
the capability is written by the resolver and read by every `Proxy` on its own
sink pump; the `ControlPath` is written per dial and read by the fetcher and the
paste upload on their own goroutines. `flake.nix:130` runs `go test -race
./remotebridge/...`, so each needs an explicit owner and an atomic accessor, and
`sshControlArgs`' argv must be built from the accessor rather than from the
flag's `*term`. The atomic capability carries `Relay.raw` too — `Proxy.Filter`'s
drop diagnostic interpolates it (`graphics/proxy.go:138`). And `Proxy` is
documented as lock-free and confined to its pump goroutine: it must read the
shared capability once per `Filter` call and update every field derived from it —
including the scanner's raster hold, which `NewRelay` sets once from
`rel.Sixel()` today — only from inside that pump.

**R12 — any new tmux `-F` format is `|`-delimited.** CLAUDE.md's rule; enforced
for `picker/**` by `go test ./tmuxformat/...`.

**R13 — the stale comments stop lying.** `sshControlArgs`'
`cmd/daemon/main.go:61-65` states the frozen assumption outright ("A non-kitty
local terminal therefore degrades the remote carousel to block art on its own"),
and CLAUDE.md's Bridge Graphics section describes the frozen design. Both
describe whatever ships.

## Non-requirements (explicit scope edges)

- **An already-open viewer does not re-pick.** aeye chooses once at startup
  (E2); nothing in this repo changes that. The guarantee is "the *next* viewer
  paints", which is what `prefix + I` produces.
- **A real client attached to the remote session directly defeats Half 2, and
  that is accepted.** `relayTermName` requires *every* client of the pane's
  session to be control-mode (aeye `gallery.go:1236-1241`); someone sitting at
  the remote host makes it false, and aeye then reads `termName()`'s untargeted
  `display-message`, whose answer this daemon does not control. Detecting that
  state would cost a remote round-trip per carousel press to change nothing we
  can steer, so Half 2 pays its cost and achieves nothing in that case. A
  documented limitation, not a guarded path.
- **A non-carousel remote graphics emitter gets Half 1 only.** Its sixel relay
  follows the viewer continuously (R4/R5); it does not get a fresh
  `client_termname` (E5).
- **`COLORTERM`/`TERM_PROGRAM` stay at their launch values.** They are #543
  truecolor concerns read from the invoking *session's* environment table, not
  from a client format; following them is a different change. A replacement
  re-sends whatever they were at launch — unchanged behaviour.
- **No new cross-repo env-var contract.** `LZTMUX_RELAY_GRAPHICS` keeps its
  name, grammar and meaning; only its freshness changes.
- **An image fetch in flight across a swap fails, and that is correct.** It dies
  inside its existing 2s `context.WithTimeout` when the old master goes away;
  the store is dropped, which renders blank and self-heals on the next repaint
  (CLAUDE.md's Bridge Graphics). Called out in the PR so it is not read as a
  regression.
- **`%client-session-changed` gets no parser case.** It now appears routinely on
  the surviving connection's stream during a swap, parses to the unknown default
  and is dropped, and still claims its ordinal through `claimSeq` like any
  notification — which is already correct. Adding a case for it, or letting it
  near `sessionPin`, is the bug E6 rules out.
- **noamsto/aeye#227 / #228** (the block-art backend's own quality) stay out.

## Acceptance

1. Viewing a mirror from a terminal other than the one that launched the bridge
   makes the **next** `prefix + I` select a backend that terminal can paint: a
   non-kitty viewer receives no kitty placements. The press that *discovers* the
   change is nacked with a press-again message (R6); the one after it launches
   the viewer against the new termname. Cost of the discovery press: one local
   `list-clients` fork, one ssh dial plus identity read, and one `repair()` pass
   — the same work a reconnect already does, with no disconnected badge.
2. Same-terminal case unregressed: kitty → kitty keeps the kitty path, is never
   nacked, and performs **no** replacement, because the resolved termname never
   changed (R8).
3. The relay gate follows the viewer: a raw sixel written into a mirror pane —
   the emitter the existing `send_straddled_sixel` bats helper already uses,
   since `chooseRelayBackend` never returns a raster backend and the carousel
   therefore cannot emit one — is relayed for a sixel-capable viewer and dropped
   for one that cannot paint it, with `LZTMUX_RELAY_GRAPHICS` agreeing in both
   cases.
4. A capability change with an unchanged termname re-publishes
   `LZTMUX_RELAY_GRAPHICS` with no replacement (R5).
5. The multi-client rule (R2) is directly tested, including the
   mixed-capability case degrading to block art, and control-mode clients being
   excluded (R1).
6. Ordinary session switching on one terminal triggers no replacement (R8).
7. A replacement whose dial or identity check fails leaves the mirror live and
   the old identity advertised (R7) — no teardown, no killed session.
8. `nix build .#default`, `nix flake check` and `nix build .#lint` all green,
   output pasted in the PR; and the plan document is committed under
   `docs/superpowers/plans/YYYY-MM-DD-slug.md` in the same PR, per
   `WORKER_TASK.md` and CLAUDE.md.
