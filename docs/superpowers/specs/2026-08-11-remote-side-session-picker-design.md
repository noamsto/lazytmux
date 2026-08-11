# Remote-side session picker (issue #356) — design

Status: accepted
Issue: #356

## Problem

`prefix + s`'s Remote section is a flat, locally-derived list, and it is not
authoritative:

- `remoteSessionsForHost` (`picker/remote.go:89`) drops every session already
  bridged locally as `<host>-<sess>`, so a mirror you closed but whose daemon is
  still alive, or a session whose mirror name collides, simply is not offered.
- A probe timeout or a briefly-unreachable host degrades the whole host to one
  `(unreachable — open default)` row — no session list at all.
- The `tmux-remux` snapshot fallback only fires on `remoteProbeNoServer`, and
  only after `restorableFromProbeOutput`'s host check and
  `filterThrowawaySessions` both pass. Either rejecting leaves nothing.
- **There are no remote zoxide suggestions anywhere.** Locally, top-30 zoxide
  dirs let you *start* a session somewhere new. There is no remote equivalent,
  so "the remote server is dead and I want a new session in `~/foo`" has no
  path through the picker at all.

The ask is an escape hatch that gives the *full* picker experience for one
remote host — the remote's own sessions **and** its own zoxide dirs — rendered
by the remote's own picker, in a local floating pane, ending in a locally
bridged mirror.

## Solution shape

One new key in the session picker opens a local **floating pane** whose command
is a local wrapper. The wrapper SSHes to the host, runs *that host's* picker in
a new emit mode, reads back one typed line, and hands it to the existing
`lztmux-remote-open` — the only supported bridging path.

```
prefix+s popup                     floating pane (local)               remote host
─────────────────────              ──────────────────────              ───────────
^o on a Remote row
  ├─ mirror-window gate
  ├─ tmux new-pane … ─────────────► lztmux-remote-pick <host>
  └─ tea.Quit (popup closes)         ├─ leg 1  ssh bash -s ──────────► resolve lztmux-pick-session
                                     │                                 → script=/emit_dir=/tmpdir=
                                     │                                   (or exit 3 = unsupported)
                                     ├─ leg 2  ssh -t <script> <tok> ─► picker --emit <file>   (TUI)
                                     │                                 writes one typed line
                                     ├─ leg 3  ssh bash -s ──────────► cat + rm <file>
                                     └─ exec lztmux-remote-open <host> …
                                          (LZTMUX_REMOTE_TMPDIR forwarded;
                                           LZTMUX_REMOTE_NEW_DIR for a dir pick)
```

## Decisions

### D1 — How the choice crosses back: a file on the remote, read by a third ssh leg

The remote picker is a full-screen TUI, so it owns the remote's stdout. `ssh -t`
allocates a pty, and **the pty merges remote stdout and stderr into the local
ssh's stdout** — so neither stream can carry an out-of-band answer. Every
"print the result" variant was checked and rejected:

| Variant | Why it fails |
|---|---|
| TUI on stdout, answer on stderr | `-t` merges both into the pty. |
| TUI on `/dev/tty`, answer on stdout | Same fd under `-t`. |
| `ssh -t … > local.file` | Redirects the pty stream, so the TUI paints into the file and the screen stays blank. |
| Answer in a remote tmux option | Requires a remote server. The serverless host is the headline case. |
| Remote SSHes back to the local host | Breaks the bridge's outbound-SSH-only invariant. |
| Sentinel line scraped with `capture-pane` | Depends on the sentinel surviving alt-screen teardown and not scrolling away. Fragile. |

So: the remote picker writes exactly one line to a file **on the remote**, and a
third, non-interactive ssh leg reads and removes it. The cost is one extra round
trip *after* the human has already chosen — no interactive latency. (No
`ControlMaster` is assumed: no bridge daemon exists for this host yet at pick
time, so only a user-configured `ControlMaster` in `~/.ssh/config` would share a
connection.)

**Emit file location.** `<emit_dir>/<token>`, where
`emit_dir = ${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/lztmux-pick`.
`XDG_RUNTIME_DIR` is set for ssh sessions on systemd hosts and absent on macOS,
so the `/tmp` fallback has to be made safe rather than assumed safe.

`mkdir -m 700 -p` alone does **not** do that: `-p` treats an existing directory
as success, does not apply `-m` to it, and checks no ownership — a pre-existing
`/tmp/lztmux-pick` owned by another uid exits 0 silently, and the picker's later
write then fails EACCES. So the remote script asserts explicitly: `mkdir -p`,
then `stat` the directory's uid and mode and **exit 4** unless it is ours and
`700`.

**Write failure must not read as a cancel.** An EACCES write would otherwise be
indistinguishable from the user pressing `esc`. The remote script therefore
pre-creates the emit file and `test -w`s it *before* exec'ing the picker, and the
picker exits non-zero if its write fails. ssh propagates a remote command's exit
status, so leg 2's status is readable by the wrapper even though leg 2's stderr
is not (D6).

Because the file is pre-created, **existence cannot be the discriminator** — a
cancel leaves it present and empty. The discriminator is therefore
**non-empty content**:

| leg 2 status | file | meaning |
|---|---|---|
| 0 | non-empty | a choice was made |
| 0 | empty or absent | cancelled (`esc`/`q`/`^c`) |
| non-zero | either | error — report it |

"Absent" stays in the cancel row so the wrapper is also correct against a remote
that failed before pre-creating.

### D2 — A dir pick is created by `lztmux-remote-open`, not by the remote picker

The first draft had the remote picker create the session itself
(`tmux new-session -d`) and emit its name. Two defects killed that:

1. **Lifetime.** `README.md:206-211` documents, for the neighbouring cold-start
   path, that without lingering the remote tmux server *"dies the moment the
   bridge disconnects"* — it lives in the ssh session's systemd user slice. A
   session created at pick time has to survive leg 2's teardown, leg 3, and
   `lztmux-remote-open`'s own three round trips (`lztmux-remote-open.sh:41`,
   `:49`, `:141`) — and leg 2 lasts as long as the human browses. That is a
   *much* wider window than the existing cold-start path accepts.
2. **Two socket resolvers.** `lztmux-remote-open.sh:41-47` resolves the socket
   dir client-side from `uname -s`/`id -u` and honours `LZTMUX_REMOTE_TMPDIR`.
   A second, independent resolver in the remote script could create the session
   on a socket the bridge never reads — `list-windows` (`:141`) then returns
   nothing and the mirror opens broken.

So the emit payload is **typed**, and creation moves to the launcher:

- `kind=session` → `lztmux-remote-open <host> <name>` (today's path, unchanged).
- `kind=dir` → `LZTMUX_REMOTE_NEW_DIR=<path> lztmux-remote-open <host> <name>`,
  which creates the session immediately before launching the daemon.

`LZTMUX_REMOTE_RESTORE` (`lztmux-remote-open.sh:105-135`) is the exact
precedent: "the session doesn't exist on the remote yet, so the launcher must
restore it before there's anything to bridge into." A dir pick is the same shape
with `new-session -d -c <path>` instead of `tmux-remux restore`. This yields
**one creator, one socket resolver**.

The session name for a dir pick is derived by the remote picker
(`sessionNameFromPath`, `picker/zoxide.go:28`) and travels in the payload's
`name` field, so the local side never invents a name.

#### The NEW_DIR branch mirrors the restore branch, including its cold start

Both `start_remote_server` call sites are gated on `[[ -z $sess ]]`
(`lztmux-remote-open.sh:88-99`). On a dir pick `$sess` is the emitted name, so
**neither gate fires** — a bare `new-session` over ssh would bring a server into
existence parented in a transient ssh session's user slice, with panes inheriting
the non-interactive ssh environment instead of the startup unit's. That is a
*different* lifecycle from the shipped cold start (`:69-86`: unit-owned,
`RemainAfterExit`, the whole subject of #287/#345), and borrowing
`README.md:206-211`'s lifetime story for it would be misattribution.

The branch therefore takes the shipped lifecycle explicitly, structurally
mirroring the restore branch:

```
if ! has-session -t $(shell_quote "=$sess"):
    [[ -z "$(first_remote_session)" ]] && start_remote_server   # unit-owned server
    new-session -d -s $(shell_quote "$sess") -c $(shell_quote "$NEW_DIR")
    has-session -t $(shell_quote "=$sess") || fatal   # message shape of :131
```

Every remote-crossing value is `shell_quote`d (`:14`), as at `:107`/`:125`/`:141`
— the remote login shell is fish and both the session name and the directory are
picker-derived. `[[ -z … ]] && start_remote_server` is errexit-safe: a failing
left operand of an AND-list is exempt, which the script already relies on at
`:10` and `:157-160`.

So the server is unit-owned whenever one had to be created, and the session is
added inside it. Lingering remains a precondition exactly as for the existing
cold start — on by default (`startupSession.linger`), and it gets a README line.

#### An absent remote session must not open a blank mirror

The earlier draft claimed `lztmux-remote-open` already fails on a `has-session`
check for a missing session. That is **false**, and the reason matters: the only
remote `has-session` calls (`:107`, `:125`) sit inside
`if [[ -n ${LZTMUX_REMOTE_RESTORE:-} && -n $sess ]]` (`:105`), and `:167`'s is
*local* (about the mirror). With `$sess` non-empty, control goes straight to the
`win` resolution at `:137-142`, which **cannot fail**: the remote command is a
pipeline ending in `awk`, so a failed `list-windows` still exits 0 with empty
stdout, and the remote shell carries none of our `pipefail`. `win=""` is exported
as `LZTMUX_BRIDGE_WINDOW` (`:210`) and the daemon launches against a session that
is not there — precisely the half-started bridge AC4 forbids.

Two guards, both required:

1. The post-create `has-session` in the block above (our branch).
2. **Empty `win` becomes fatal** at `:137-142`. This also hardens the
   pre-existing `lztmux-remote-open <host> <typo>` path, which silently opens a
   blank mirror today; a live session always has an active window, so an empty
   result means the session is gone. Small, in scope, and it is the guard that
   stops the blank mirror on *every* path rather than only ours.

### D3 — The remote script owns remote-side resolution and *reports* it

`lztmux-pick-session` is a bash script **on the remote**, so it resolves
`TMUX_TMPDIR` and the tmux binary with full bash, locally, where the answer
actually lives. Three things follow, all of which the first draft left implicit:

1. **PATH.** The Go picker calls `tmux` **and `zoxide`** by bare name and
   `picker/main.go:1168` says so outright ("Bare-name exec relies on the tmux
   wrapper's PATH"). An ssh session has no such PATH. `dirname "$resolved_tmux"`
   (the precedent at `lztmux-remote-open.sh:120`) resolves `tmux` but says
   nothing about `zoxide`, which is a separate binary — and remote zoxide dirs
   are Required property 1, so it cannot be left incidental. Guessing at profile
   bin dirs and then absorbing the miss would only *degrade* that failure class;
   instead it is **deleted**. `pkgs.zoxide` is already in this repo's closure
   (`config/tmux.conf.nix:1113`) and this script is already build-substituted, so
   the script prepends `@zoxide@/bin` alongside `dirname "$resolved_tmux"`. The
   zoxide db is per-user (`db.zo`), so a store-path binary reads exactly the same
   data the user's own `zoxide` would. Because the script and its substitutions
   ship from one store path on that host, this cannot drift.
2. **Which picker binary.** `tmux-picker-generate` is not in `home.packages`
   (`modules/home-manager.nix:962-981`), so a bare name will not resolve. The
   script execs it through the build-time `@picker_generate@` substitution the
   `scriptsWithIcons` list already provides (`config/tmux.conf.nix:355-358`), as
   `scripts/tmux-session-picker.sh:11` does. Because the script and the binary
   ship from the same store path on that host, they can never disagree.
3. **The resolved tmpdir is reported on leg 1** and forwarded by the wrapper as
   `LZTMUX_REMOTE_TMPDIR`, so `lztmux-remote-open` uses the value the remote
   itself computed instead of re-guessing from `uname -s`.

**Empty view is never blank.** With no server *and* no `zoxide` on the remote's
PATH, `collectSessions` returns nil and `collectZoxide` silently degrades to nil
(`picker/zoxide.go:216-224`) — a blank box, which is the "silent empty view"
Required property 4 forbids. Emit mode therefore renders an explicit
unselectable row when it has nothing to offer:
`(no sessions, and no zoxide on this host)`.

### D4 — Sequencing: the picker issues `new-pane` itself, then quits

The picker runs inside `display-popup -E`; a second full-screen TUI cannot be
drawn in that popup's client, and `display-popup` from a shell does not block
the issuing client, so "launcher reads a pending-action file after the popup
returns" has no join point to read at.

The codebase already issues structural tmux commands from inside the popup and
then quits — `activateCurrent` → `tmux switch-client` → `tea.Quit`
(`picker/tui.go:1082-1098`). `new-pane` takes the same shape.

**Probed on the pinned `tmux next-3.8`**, driving a real attached pty client: a
command inside `display-popup -E` running

```
tmux new-pane -x 60% -y 40% -X 10% -Y 10% -B heavy <cmd>
tmux set -p @pane_label probe
```

exits 0, and after the popup closes the window holds
`%1 active=1 float=FLOAT label=probe`. So the pane is created, is **floating**,
is **active**, and `set -p` targets it (it is current). The same probe against
PATH `tmux 3.7b` rejects `-B` — which is why the geometry form is taken from the
repo's own working `bind-key y` / `bind-key p` sites rather than guessed.

Floating pane, not `display-popup`: #353/#354 moved tool popups to floating
panes, and #346 is an open tmux-server SEGV in
`cmd_display_popup_exec → popup_modify → tty_resize`.

**The wrapper is reached by store path, not by bare name.** A bare name resolves
against the tmux server's *frozen* PATH, so a brand-new script is absent until a
server restart — `lztmux-remote-open.sh:148-152` documents exactly this trap
(#336). The prdash floating pane is the store-path precedent
(`config/tmux.conf.nix:571`); the yazi one at `:801` is a *bare* name that
reaches its binary through the wrapper's own `--prefix PATH` (`:1113`), which a
brand-new repo script does not get. The picker cannot hold a store path
directly, so `tmux.conf.nix` sets a global option

```
set -g @remote_pick_bin '${script.lztmux-remote-pick}/bin/lztmux-remote-pick'
```

which `readTmuxOpts` already collects (`picker/main.go:1151`) and the picker
reads via `envOrMap("REMOTE_PICK_BIN", opts, "@remote_pick_bin", "")`. An option
repoints on a config reload alone, which is the property the frozen PATH lacks.
Empty option → the key reports `remote picker not configured — reload tmux`
rather than spawning a pane that dies with "command not found".

### D5 — Version skew: probe for a *script name*, never execute the old binary

`picker/main.go:105-110` builds `flags := map[string]bool{}` from bare args and
**ignores unknown args**, then unconditionally calls `runTUI`. So asking an old
remote picker binary a new flag would start its interactive TUI instead of
answering — over ssh that is exactly the hang the acceptance criteria forbid.

The capability probe is therefore the **presence of a script name** that exists
on no older build: `lztmux-pick-session`. Resolution uses the established
non-interactive-PATH pattern
(`command -v X 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/X`, as
in `lztmux-remote-open.sh:49`) followed by `[ -x ]`. Nothing is executed until
that test passes.

### D6 — ssh options and the failure taxonomy

`classifyProbeErr` (`picker/remote.go:116-125`) needs *two* signals, not one: it
returns `errRemoteUnreachable` for a timed-out probe **before** it looks at exit
255, because a dead host does not reliably produce 255. Every ssh in this
codebase carries `-o BatchMode=yes -o ConnectTimeout=2 -T` inside a 3s bound
(`remote.go:20`, `:132-142`, `:283-293`). The legs follow suit:

| Leg | argv | bound |
|---|---|---|
| 1 (probe) | `ssh -o BatchMode=yes -o ConnectTimeout=2 -T <host> bash -s` | `timeout 8` |
| 2 (TUI) | `ssh -o BatchMode=yes -o ConnectTimeout=2 -t <host> -- <script> <token>` | none (interactive) |
| 3 (collect) | `ssh -o BatchMode=yes -o ConnectTimeout=2 -T <host> bash -s` | `timeout 8` |

`BatchMode=yes` on leg 2 too: the bridge already requires non-interactive auth,
and without it a key-less host parks a password prompt in a floating pane —
a hang, not a message.

The taxonomy is **per leg**, because leg 2's stderr is not capturable: `-t`
merges it into the pty, which is the floating pane's own terminal (D1). Legs 1
and 3 capture stderr; leg 2 has only its exit status.

Legs 1 and 3:

| Outcome | Meaning | Message |
|---|---|---|
| `timeout` fires | no answer in 8s | `<host>: timed out` |
| exit 255 | ssh could not reach the host | `<host>: unreachable` |
| exit 3 | reached, no `lztmux-pick-session` | `remote lazytmux too old — rebuild <host>` |
| exit 4 | emit dir exists but is not ours / not 0700 | `<host>: emit dir unusable` |
| other non-zero | reached, remote command failed | last non-empty stderr line |

Leg 2 (status only, per D1's disambiguation table):

| Outcome | Meaning | Message |
|---|---|---|
| 0 | chose or cancelled — decided by the file | — |
| 255 | link dropped mid-session | `<host>: connection lost` |
| other non-zero | remote picker failed | `remote picker failed on <host> (status N)` |

### D7 — The mirror-window gate lives in the picker, and refuses in place

A floating pane inside a bridged window is its own problem space (#348/#351).
The gate is the same conjunction `bridgeGate` uses
(`config/tmux.conf.nix:302`) — window `@bridge_win` **and** pane `@bridge_pane`
— evaluated with one
`tmux display-message -p '#{&&:#{@bridge_win},#{@bridge_pane}}'`.

When gated the picker does **not** quit: it sets `statusMsg` (already rendered
red by `renderHints`, `picker/render_list.go:74`) and stays open, so the user can
switch to a local window and retry.

**Stated assumption**, because it is the risky half and a unit test cannot reach
it: `display-message -p` with no `-t`, run from inside `display-popup -E`,
resolves against the *client's* current window and pane — a popup does not shift
them. A unit test can only cover the format-string parse ("1" / "0" / empty); the
targeting is verified by the same two-host pass that verifies the rest.

### D8 — Emit mode reuses the session picker, minus the Remote section and `^x`

The ask is explicitly "something like we have in the session picker" — sessions
*and* zoxide dirs — so emit mode is the existing session picker with three
changes:

- **No Remote section.** A remote-of-remote section would offer bridging from a
  host we are not attached to. `newPickerModel` gates `pendingRemoteItems` on
  `!windowMode` (`picker/tui.go:197-202`); emit mode gates it off too, and
  `Init` skips `remoteCmd` (`:239`).
- **Enter emits instead of switching.** Emit mode never calls `switch-client`:
  the picker runs in an ssh pty, not an attached tmux client, so switching is
  meaningless and (with no server) an error.
- **`^x` is disabled.** Inherited unchanged it would `tmux kill-session` against
  the *remote* server (`picker/tui.go:461-475`) and `zoxideForget` against the
  *remote* zoxide db (`:453-459`), advertised by a hint line reading `^x:kill`
  with nothing saying the target is another machine. #356 asks to *reach* or
  *start* a remote session; a remote-kill capability arriving as a side effect
  of code reuse is scope creep with teeth. The hint line omits it in emit mode.

`^a`/`^s` stay: they are pure view filters over rows already on screen and
mutate nothing.

Serverless is not an error path: with no remote server `collectSessions` returns
nil and `readTmuxOpts` returns nil (theme falls back to defaults), so the view
still comes up with the remote's zoxide dirs — the headline case. See D3 for the
both-empty guard.

**Honest limitations**, both going in the README:

- Restore rows come from `sshListRestorableSessions` on the *local* side
  (`picker/remote.go:445-450`, `:491-505`); the remote's own picker builds none
  for itself. So for a serverless host the remote view trades the
  `(restore — saved …)` rows for the remote's zoxide dirs. Both views stay
  reachable.
- A remote session named `scratch-*` is still hidden by default, because emit
  mode inherits the scratch split (`picker/zoxide.go:130`, `itemVisible`). `^s`
  reveals it. This is a real dent in "every remote session is listed", and it is
  cheaper to document than to special-case — the toggle already exists and is on
  the hint line.

### D9 — Already-bridged and cancelled paths need no new code

- **Already bridged.** `lztmux-remote-open.sh:165-170` already handles it: a live
  daemon that answers `ctl ping` and whose local session exists →
  `tmux switch-client`, `exit 0`. Picking an already-bridged session focuses the
  existing mirror rather than erroring, for free. (`:171-185` is the
  orphan-daemon reap, a different branch.)
- **Cancelled** (`esc`/`q`/`^c`). The remote picker writes nothing and exits 0,
  so leg 2 status 0 + an **empty** (pre-created) file means cancel per D1's
  table, and the wrapper exits 0 having done nothing. The floating pane closes
  with its command; every ssh leg is a child that exits with it; no bridge daemon
  was ever started.

### D10 — Discoverability

`^o` on a Remote row, appended to the picker's hint line **only** when the
cursor is on such a row — following the conditional-append precedent of the
window-mode `^g` block (`picker/render_list.go:113-119`), so the footer never
advertises a key that would no-op. No `prefix` bind is added, so `splash.tips`
is untouched and the scope stays tight.

`^o` is inert in `prefix + w` / `prefix + W` because no window-mode row carries
`remoteHost` — the required no-op is structural, not a special case.

## Contracts

### `lztmux-remote-pick <host>` — local, the floating pane's command

Generates the token (`[A-Za-z0-9]{16}` from `/dev/urandom`), owns validation of
everything coming back, and forwards `LZTMUX_REMOTE_TMPDIR` /
`LZTMUX_REMOTE_NEW_DIR` to `lztmux-remote-open`. Exit 0 on success, on cancel,
and on a legible refusal.

Errors surface via `tmux display-message` — that is the channel that actually
delivers AC4's message. They are *not* "printed in the pane": a floating pane is
destroyed when its command exits and neither `config/tmux.conf.nix:571` nor
`:801` sets `remain-on-exit`, so pane output at exit is unobservable. It does
print a one-line progress note *before* handing off, because
`lztmux-remote-open` makes 3+ round trips before `switch-client` and the pane
would otherwise sit blank.

It reaches `lztmux-remote-open` by **build-substituted store path**, not bare
name. The wrapper is spawned by the tmux server, so a bare name resolves against
that same frozen PATH (#336, `lztmux-remote-open.sh:148-152`) — and a *stale*
launcher would silently ignore `LZTMUX_REMOTE_NEW_DIR` and land straight in the
blank-mirror failure D2 exists to prevent. `@reflow@`
(`config/tmux.conf.nix:369-384`) is the precedent for the substitution, including
its reason for living in a script-specific `mk*`: a script must never reference
its own store path.

**It does not `exec`.** The two fatals this design adds to the launcher
(`LZTMUX_REMOTE_NEW_DIR` create failure, empty `win`) would otherwise print into
a pane destroyed microseconds later — the same unobservability argued above. The
wrapper runs the launcher as a child, captures stderr, and surfaces the last
non-empty line via `tmux display-message`. `openRemoteBridge` already has this
exact shape to copy (`picker/remote.go:321-326` with `lastNonEmptyLine` at
`:332-340`).

**Validation of leg-1 values.** `script` and `emit_dir` must be absolute paths;
`tmpdir` must be an absolute path with no whitespace and no shell
metacharacters, because `lztmux-remote-open.sh` interpolates
`LZTMUX_REMOTE_TMPDIR` **unquoted** into remote command strings (`:56`, `:107`,
`:120`, `:125`, `:141`). That variable is human-supplied today and
remote-derived after this change; there is no local privilege gain (we already
run arbitrary commands on that host), but the shape check keeps a malformed value
from producing an unreadable failure.

**Token-file cleanup has exactly one owner**: the remote script's 60-minute
prune. A local `trap` cannot remove a file on the remote without a fourth ssh
leg — unbounded, and useless on the unreachable arm — so no trap is specified.

### `lztmux-pick-session [--probe] <token>` — remote, on the remote's `home.packages`

`--probe`: print `key=value` lines and exit 0, **mutating nothing** (a probe that
created directories would be a probe with side effects):

```
script=<absolute path to this script>
emit_dir=<${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/lztmux-pick>
tmpdir=<resolved TMUX_TMPDIR>
```

Order-independent, parsed by key. Unknown keys are ignored, so a newer remote can
add fields. The parser must also **skip lines containing no `=`**: the remote
login shell is fish, `ssh host bash -s` is still `fish -c 'bash -s'` and so
sources `config.fish`, and any greeting lands on leg 1's stdout.
`remoteListSessionsCmd`'s comment (`picker/remote.go:26-28`) documents the
neighbouring fish hazard. "Unknown keys are ignored" covers `k=v` noise; bare
lines need this separate rule.

`<token>`: validate against `^[A-Za-z0-9]+$` and reject otherwise (so it can
never escape the path join); `mkdir -p "$emit_dir"` then assert its uid and mode
per D1, **exit 4** if unusable; prune entries older than 60 minutes; pre-create
the emit file and `test -w` it so a write failure is a non-zero leg-2 status
rather than a phantom cancel; then
`exec env TMUX_TMPDIR=<tmpdir> PATH=<resolved dirs>:$PATH <picker> --emit
"$emit_dir/$token"`.

### `tmux-picker-generate --emit <path>`

`--emit` is a **valued** flag, so `main()`'s bare-arg set (`picker/main.go:105-110`)
gains a small positional pass that consumes the following argument. `--emit`
implies session mode and is rejected together with `--windows`/`--wall`; `--tui`
remains ignored as today.

Behaviour: no Remote section, `^x` disabled, and the selection path is
**side-effect-free** — not merely `switch-client`-free. Today a dir row's Enter
runs `createAndSwitch` (`picker/zoxide.go:247-257`), which also does
`tmux new-session -d` *and* `zoxide add`; reusing it would walk the
two-creators defect of D2 straight back in through code reuse. Emit mode writes
the payload and quits, creating nothing.

On selection write the payload to `<path>` and exit 0; if the write fails exit
non-zero. On cancel write nothing and exit 0 — `<path>` and its parent are
pre-created by the remote script, so on cancel the file is left **empty**, which
is what D1's table reads as cancel.

### The emit payload

One `key=value` block — the **same shape and the same parser** as leg 1, so there
is one format in this design rather than two:

```
kind=session          kind=dir
name=<session name>   path=<directory>
                      name=<derived session name>
```

The earlier draft used a positional line (`dir <path> <name>`) split at the last
space, justified by "`sessionNameFromPath` never emits one". That is **false**:
`sessionNameFromPath` is `filepath.Base(filepath.Clean(p))` with a replacer
touching only `.` and `:` (`picker/zoxide.go:18`, `:26-34`), and
`zoxideSuggestions` filters only the *empty* name (`:111`). So
`/home/x/My Docs` yields name `My Docs`, the line becomes
`dir /home/x/My Docs My Docs`, and a last-space split gives path
`/home/x/My Docs My` — AC2's headline path, silently wrong. It was also
self-inconsistent with this section's own argument that a session may be named
`my notes`. `key=value` removes the ambiguity: the value is everything after the
first `=`, so spaces and tabs are safe. A literal newline in a path or name is
the documented limit.

Validation is **transport-only**: known keys, required keys present per `kind`,
non-empty values, no NUL, length bounded. No charset filter — tmux session names
are near-arbitrary (only `.`/`:` are constrained) and today's flat Remote section
already bridges them: the name crosses as an argv element
(`picker/remote.go:307-312`), and the launcher
`shell_quote`s it wherever it must cross a shell
(`lztmux-remote-open.sh:14`, used at `:107`, `:125`, `:141`). The one place a
charset *is* applied is the socket filename (`:146`), which is proof arbitrary
names are expected. A charset filter here would list a session named `my notes`
in the "authoritative" view, let it be selected, and then refuse it locally —
breaking the very acceptance criterion this view exists to satisfy.

### `LZTMUX_REMOTE_NEW_DIR` — new input to `lztmux-remote-open`

When set (with a session name argument), and that session does not already
exist, create it on the remote before bridging (per D2's block), then fall
through to the existing window resolution and daemon launch. Failure to create is
fatal with a legible message, the same shape as the restore branch's.

Mutually exclusive with `LZTMUX_REMOTE_RESTORE`, **enforced in the launcher** with
a one-line reject rather than left to callers: `lztmux-remote-open` is a public
entry point (the README tells people to call it directly), and the wrapper
happening never to set both is not an invariant the launcher can assume.

## Files touched

`config/tmux.conf.nix` is the flagged shared-with-#355 file; the hunks are kept
to three, and the branch rebases on `origin/main` before push:

1. `scriptNames` += `lztmux-remote-pick`, `lztmux-pick-session`
2. two script-specific builders in the `mkScript*` dispatch (`:495-530`), one per
   new script — **not** entries in `scriptsWithIcons`. The dispatch reaches
   `mkScriptIcons` only for `name == "tmux-update-icons"` (`:514-515`) and sends
   every `scriptsWithIcons` member to `mkScriptFull` (`:516-517`), so adding
   `@remote_open@`/`@zoxide@` to `mkScriptIcons`'s list would leave the
   placeholders literal. Separate builders are also what keeps
   `lztmux-remote-pick` from referencing its own store path — the reason `@reflow@`
   lives where it does (`:364-368`).
3. one `set -g @remote_pick_bin '<store path>'` line

Also: `picker/{remote,tui,main,render_list}.go`, `scripts/lztmux-remote-open.sh`
(the `LZTMUX_REMOTE_NEW_DIR` branch + the empty-`win` guard), two new
`scripts/*.sh`, `modules/home-manager.nix` (a `remote.exposePickOnPath` toggle,
default true — every other non-core `home.packages` entry is option-gated, so an
unconditional one would break the file's pattern; it is *not* gated on
`remote.hosts`, which is set on the local host while the script is needed on the
remote), `flake.nix` (one `checks` entry), `README.md`, `CLAUDE.md`, and this
spec plus its plan.

## Out of scope

- Redesigning the flat Remote section — its live and restore rows stay.
- `prefix + w` / `prefix + W`.
- Fetching remote zoxide dirs into the *local* picker as inline child rows.
- Remote **window** picking. Sessions only, per the issue.

## Documentation obligations

- `CLAUDE.md` script table: update the **`tmux-session-picker`** row (it must
  describe `^o`) and the **`lztmux-remote-open`** row (it must describe
  `LZTMUX_REMOTE_NEW_DIR`), and add rows for both new scripts.
- `CLAUDE.md`: state which binaries the *remote* needs on PATH, sited next to the
  Bridge Graphics precedent ("Remote host needs `tmux-claude-images` and `resvg`
  on PATH").
- `README.md` "Remote tmux bridge": the `^o` escape hatch, the remote-host
  requirement, the lingering precondition for a dir pick, and the
  restore-rows-vs-zoxide trade of D8.

## Verification honesty

The end-to-end two-host flow cannot be verified here — it needs two real
machines (the human's pair is `g5` ↔ `tp-g6`). What *is* verifiable here, and
must be:

- Go unit tests for every pure function added: the shared `key=value` parser
  (leg-1 probe output *and* the emit payload), the exit/timeout taxonomy per leg,
  payload parsing for both `kind`s **including a spaced path and a spaced name**
  (`/home/x/My Docs` → `My Docs`, the case that killed the positional format),
  transport validation, the `new-pane` argv, the gate parse, emit-target
  resolution, `--emit` flag parsing and its rejection with `--windows`/`--wall`.
- bats coverage for both new scripts with `ssh` stubbed — token validation, the
  failure classes including exit 4, the cancel-vs-write-failure disambiguation,
  the `LZTMUX_REMOTE_NEW_DIR` branch's cold-start + post-create verification, the
  empty-`win` fatal guard, and `--probe`'s no-mutation property — plus its own
  `checks` entry in `flake.nix`, following `tests/remote.bats` and
  `tests/picker-launcher.bats`.
- The `display-popup` → floating-pane sequencing probe of D4, on the pinned tmux.
- Single-host degradation: unreachable host, capability-absent host, cancelled
  view.

A live two-host test belongs behind the same guard as
`picker/remote_live_test.go`'s `TestSSHListRemoteSessionsLive` and must not enter
the default `nix flake check`.
