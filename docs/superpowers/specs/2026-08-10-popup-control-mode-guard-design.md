# Guarding `display-popup` against control-mode clients

**Issue:** #346 — tmux server SIGSEGV in `cmd_display_popup_exec → popup_modify → tty_resize`.

## Problem

A tmux server on `tp-g6` died of SIGSEGV after ~1d17h, taking every session on the
host with it. On a bridged host that also silently destroys the remote sessions a
local mirror was showing.

## Root cause (reproduced, not hypothesised)

Reproduced on **both** `tmux 3.7b` and the pinned `next-3.8`
(`tmux/tmux@d5afb67`), so this is not a next-3.8 regression. The crash report
decoded from the local repro matches the issue's stack trace frame for frame:

```
EXC_BAD_ACCESS (SIGSEGV) KERN_INVALID_ADDRESS at 0x30
  tty_resize / popup_modify / cmd_display_popup_exec / cmdq_next / server_loop
```

The chain, all line numbers against the pinned source:

1. **A control-mode client never gets a tty.** `server_client_dispatch_identify`
   (`server-client.c:2947`) branches: `if (c->flags & CLIENT_CONTROL)
   control_start(c); else if (c->fd != -1) { tty_init(&c->tty, c); ... }`. For a
   `-CC` client `tty_init` is never called, so `c->tty.client` stays `NULL`
   (the client struct is `xcalloc`'d).
2. **A second popup on a client takes the modify path.**
   `cmd_display_popup_exec` (`cmd-display-menu.c:581`) sets
   `modify = popup_present(tc)` and, when true, calls
   `popup_modify(tc, title, style, border_style, lines, flags)` instead of
   `popup_display(...)`.
3. **`popup_modify` resizes the tty when border lines were requested.**
   `popup.c:550` — the `if (lines != BOX_LINES_DEFAULT)` branch ends with
   `tty_resize(&c->tty)`. `lines` is non-default exactly when the command carried
   `-B` (`BOX_LINES_NONE`) or `-b <lines>`.
4. **`tty_resize` dereferences the null back-pointer.** `tty.c:124` opens with
   `struct client *c = tty->client;` then `ioctl(c->fd, TIOCGWINSZ, &ws)`.
   `tty->client` is `NULL`, `c->fd` faults at offset `0x30`.

So the lethal shape is: **a popup already open on a control-mode client, plus a
second `display-popup` carrying `-B`/`-b` targeted at that same client.** One
popup alone is harmless — `popup_display` never touches `tty_resize`, which is
why `tests/tmux-next38-readiness.bats`'s existing "display-popup opens for an
attached control client" case has always passed.

### Minimal upstream repro

```bash
tmux -L segv -f /dev/null new-session -d -s s
tmux -L segv -f /dev/null -C attach-session -t s &      # control-mode client
CLIENT=$(tmux -L segv -f /dev/null list-clients -t s -F '#{client_name}')
tmux -L segv -f /dev/null display-popup -c "$CLIENT" -E 'sleep 10' &   # popup 1
sleep 1
tmux -L segv -f /dev/null display-popup -c "$CLIENT" -B -E true        # popup 2 -> SEGV
```

`-f /dev/null`, so no lazytmux config is involved: this is a plain upstream tmux
NULL deref.

## How lazytmux reaches it — and it is not only the splash

The second, load-bearing half of the mechanism is **how tmux picks the client a
popup lands on**.

Every popup in this repo that is opened by a *wrapper script* rather than by the
bound command itself forks a fresh `tmux` command client. That client has no
session, so `cmd_find_current_client` (`cmd-find.c`) falls through to
`cmd_find_best_client(s)`, which walks the session's clients and keeps the one
with the newest `activity_time`:

```c
cmd_find_client_better(c, than) { return timercmp(&c->activity_time, &than->activity_time, >); }
```

**There is no control-mode exclusion in that walk.** A `-CC` client can and does
win it. Measured against the built wrapper, with both a real pty client and a
control client attached to one session, and two untargeted
`display-popup -b rounded` calls (the picker launchers' exact shape):

| attach order | winner | outcome |
|---|---|---|
| pty first, control second | control client | **server DEAD** |
| control first, pty second | pty client | server alive |

By contrast, a popup that *is* the bound command (`bind-key g display-popup …`)
runs on the pressing client's own command queue, and
`cmd_find_current_client` returns it directly (`if (c != NULL && c->session !=
NULL) return (c);`). Those are safe, and stay untouched.

### The two reachable classes

**(a) The splash — hook-driven, no keypress needed.**
`config/tmux.conf.nix` registers the gate on two hooks that both fire on a
single attach, both backgrounded:

```
set-hook -g client-attached[50]        'run-shell -b "... tmux-splash-maybe #{hook_session}"'
set-hook -g client-session-changed[50] 'run-shell -b "... tmux-splash-maybe #{hook_session}"'
```

`tmux-splash-maybe`'s once-per-server flag is a check-then-set, so two
concurrent invocations both pass it and both run
`display-popup -E -B -w 100% -h 100% -t "$session" tmux-splash` — and `-t` is a
target *pane*, so both re-resolve their client through `cmd_find_best_client`.
Verified against the built wrapper: with a `-CC` client attached, two concurrent
gate runs killed the server **3/3**; the identical double-fire against a real
pty client left the server alive.

**Caveat, and it matters: that hook is dead right now.** `#{hook_session}`
expands to a session *id* (`$0`), `run-shell` execs `sh -c` with `argv[0]="sh"`,
and the shell re-expands the literal `$0` to `sh` — a hazard
`config/tmux.conf.nix:960-961` already documents for `#{session_id}`. Measured:
the gate receives `$1="sh"`, its first `display-message -t sh` fails, and
because that call sits inside `[ "$(…)" = "1" ] || exit 0` the failure is
discarded and the script exits 0 in silence. Confirmed end to end — a real pty
attach to a freshly built wrapper leaves `@splash_shown` unset and opens
nothing.

So the splash today is only reachable through `prefix + C-Space` (a direct
bind, and therefore safe). Passing the client as `$2` is useless while `$1` is
mangled, so this change must also switch the hooks to `#{hook_session_name}` —
which **revives** the splash on attach. That is precisely why the control-mode
guard has to land in the same commit: the fix that makes the splash work again
is the fix that would otherwise expose bridged hosts to (a).

**(b) The `run-shell` popup launchers — every one carries `-b rounded`.**
`scripts/tmux-session-picker.sh:10`, `scripts/tmux-window-picker.sh:17`,
`scripts/tmux-window-wall.sh:9`, `scripts/tmux-scratchpad.sh:42`, bound at
`config/tmux.conf.nix:715,746-752` (`prefix + S/s/w/a/W` and the
`MouseDown1StatusLeft` root bind). `-b rounded` is `lines != BOX_LINES_DEFAULT`
— precisely the condition step 3 needs. On a remote host where a human is
attached *alongside* the bridge's control client, pressing `prefix + s` twice is
enough.

`picker/remotebridge/` opens no popups (checked); `tmux-claude-images
--reconcile`, the other hook-driven script, splits a pane rather than popping up;
`lztmux-notify` explicitly rejected `display-popup -N`; tmux-remux's hooks
(`modules/home-manager.nix:93-115`) only run `save` / `capture-event` /
`restore --auto`, and its two popups are direct binds (`U`, `R`). So (a) and (b)
are the complete inventory.

## Requirements

**R1 — the splash must never open a popup for a control-mode client.** A mirror
has no business showing a splash, and it is the one popup here reachable with no
keypress at all.

**R2 — skipping must not consume `@splash_shown`.** A later real local attach on
the same server must still get its splash. Same rule `splash.remote = skip`
already follows.

**R3 — every wrapper-script popup must land on the client it was invoked for**,
not on whichever client `cmd_find_best_client` happens to prefer. This is what
actually makes R1 hold in a mixed-client session, and it independently fixes a
latent bug: today a picker opened from a human's client can render on a
different client entirely.

**R4 — the guard must fail closed.** If the invoking client cannot be resolved,
open nothing rather than fall back to best-client resolution.

**R5 — regression coverage** via bats, driven through the same fake-`tmux` seam
`tests/splash.bats` and `tests/picker-launcher.bats` already use — no live
control client required.

## Design

### Pin the client

Each wrapper script learns the client it is acting for and passes it straight
through to `display-popup -c`.

**The four launchers** take a leading `--client <name>`, parsed and shifted away
*before* any existing argument handling, so `--claude` and the scratchpad's
session name keep landing on `$1` exactly as today:

```bash
CLIENT=""
if [[ ${1:-} == --client ]]; then
	CLIENT=${2:-}
	shift 2
fi
...
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E ...
```

`tmux-scratchpad`'s `--attach` inner-mode check stays first — inner mode is
invoked from inside the popup and never carries `--client`.

An explicit option rather than a bare positional, because `tmux-window-picker`
reads `$1` as `--claude` and `tmux-scratchpad` reads `$1` as the `--attach`
sentinel / session name. `tests/picker-launcher.bats` invokes the launchers with
no arguments, so `CLIENT` stays empty, no `-c` is emitted, and those cases keep
passing byte-identically.

**Six bind sites** supply it, all in `config/tmux.conf.nix` unless noted:

| line | key | argument added |
|---|---|---|
| 715 | `prefix + S` | `--client "#{client_name}" "#{session_name}"` |
| 746 | `prefix + s` | `--client "#{client_name}"` |
| 747 | `prefix + w` | `--client "#{client_name}"` |
| 748 | `prefix + a` | `--client "#{client_name}" --claude` |
| 749 | `prefix + W` | `--client "#{client_name}"` |
| 752 | `MouseDown1StatusLeft` | `--client "#{client_name}"` |

Bind values are tmux-single-quoted with the shell string inside them, so plain
double quotes reach `sh -c` unchanged — the idiom `bind S` already uses.

**`tmux-splash-maybe`** keeps `$1 = session` (its existing signature and test
seam) and takes the client as `$2`. A bare positional is safe there because
client names never contain whitespace.

### Gate the splash on control mode

In `tmux-splash-maybe`, immediately after the `@splash_shown` check:

```bash
client="${2:-}"
# The remote bridge attaches a -CC control-mode client, which has no tty: a
# second popup on it faults the whole server (#346). A mirror has no business
# showing a splash anyway. Not setting @splash_shown is deliberate — a later
# real attach on this same server must still get the splash it never got.
control=""
while read -r name mode; do
	[ "$name" = "$client" ] && control="$mode"
done < <(tmux list-clients -t "$session" -F '#{client_name} #{client_control_mode}')
[ -n "$control" ] || exit 1
[ "$control" = 1 ] && exit 0
```

**Why a `list-clients` scan rather than `display-message -c "$client" -p
'#{client_control_mode}'`.** The obvious query is fail-*open*.
`cmd-display-message.c` resolves the format client as `if (tc != NULL &&
tc->session == s) c = tc; else if (s != NULL) c = cmd_find_best_client(s);` —
so an unresolvable `-c`, or a `-c` client whose session isn't the target
session, silently answers for the session's best client instead. And
`display-message` carries `CMD_CLIENT_CANFAIL`, so that case does not even
error. Measured on the pinned tmux with a pty client and a control client
attached: `display-message -c bogus -p '#{client_control_mode}'` printed **`1`**
and exited **0** — the guard would have read the control client's flag while
believing it read a nonexistent one, and any implementation keying off the exit
status would sail straight through.

The `list-clients` scan has no fallback to fall into: a name that isn't in the
session's client list leaves `control` empty and the script exits 1 before any
popup. R4 holds by construction rather than by exit-status convention.

The control-mode lookup lives in the script rather than being passed from the
hook as a second format, so the flag and the `-c` target are guaranteed to
describe the *same* client.

Every existing `tests/splash.bats` case invokes `bash "$GATE" s` with no client
and would now exit 1: they all gain a client argument. That is the honest
contract — in production the hook always supplies one.

### Fix and quote the hook's arguments

`#{hook_session}` must become `#{hook_session_name}` — see the caveat above; the
id form is re-expanded to `sh` by `run-shell`'s shell, which is why
`config/tmux.conf.nix:990` already uses the name form. Names *can* contain
whitespace, so the quoting is now load-bearing rather than hygiene. Measured
argv, all three forms, one real attach to session `sess one`:

| hook text | `$1` |
|---|---|
| `#{hook_session}` (today) | `sh` |
| `\"#{hook_session}\"` | `sh` — double quotes do not stop `$0` |
| `\"#{hook_session_name}\"` | `sess one` |

Both hook registrations become:

```
set-hook -g client-attached[50] 'run-shell -b "…/tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true"'
```

**`#{q:…}` used bare, not `\"…\"`.** Escaped double quotes are *not* protection:
`run-shell` format-expands its whole command string before `sh -c` sees it, so a
session name containing `"` closes the quote and everything after it executes.
Verified end to end against the real wrapper — a session named
`devbox-evil"; touch …/pwned; echo "`, which is exactly the shape
`lztmux-remote-open.sh:144` builds from a **remote** host's own session list,
achieved arbitrary local execution with the `\"…\"` form. `#{q:…}` maps to
tmux's `format_quote_shell()` (backslash-escapes ``| & ; < > ( ) $ ` \ " ' * ?
[ # space = %``) and blocks it while delivering the name as one argument.

`|| true` is appended because the gate now has a fail-closed non-zero exit, and a
`run-shell -b` job that exits non-zero pushes the hook's target pane into
view-mode (measured).

### Test seam

`tests/splash.bats`'s fake `tmux` grows a `list-clients` case that prints one
row, `${FAKE_CLIENT:-/dev/ttys0} ${FAKE_CONTROL:-0}`. A test then expresses:

- a control-mode client — `FAKE_CONTROL=1`, invoke with the matching name;
- an unresolvable client — invoke with a name the stub never prints, so the
  scan finds nothing.

Modelling the roster rather than a per-client query is what makes the
fail-closed case *real*: the stub cannot accidentally answer for a client it
does not list, which is exactly the trap real `display-message -c` falls into.
`display-popup` continues to be logged to `$POPUP_LOG`, so `-c <client>` is
assertable there.

`tests/picker-launcher.bats` gains a `tmux-scratchpad.sh` case (it has none
today) to pin that `--client` is consumed before the session positional; its
existing stub already exits 0 on `new-session`/`show`, so it needs no change.

## Non-goals

- What a remote popup should *do* over the bridge (mirror locally vs. degrade to
  inline). Separate product decision.
- Any change to `picker/remotebridge/` or the `bridgeCtl` verbs.
- Patching tmux or filing the upstream issue. The repro above is handed over in
  the PR body for someone to file from.
- The direct `display-popup` key binds (`prefix + C-Space|i|n|g|b|G|k`, and
  `U`/`R` in `modules/home-manager.nix`). They execute on the pressing client's
  own command queue, which `cmd_find_current_client` returns without consulting
  `cmd_find_best_client`, so no control client can ever be their target.
- **The `@splash_shown` check-then-set race.** It is real, and `acquire_lock`
  (`scripts/lib-log.sh:32`, atomic `mkdir`) would close it — the earlier claim
  that no primitive exists was wrong. It is still not worth doing here: once the
  popup is pinned with `-c` to a vetted client, the second firing is either
  skipped (control client) or a harmless no-op `popup_modify` on a real tty. The
  remaining cost is one redundant redraw, and closing it would mean rebuilding
  `tmux-splash-maybe` through the `mkScriptWithLog` seam
  (`config/tmux.conf.nix:522`) for a cosmetic win.
- Fixing upstream's willingness to hand a control client to `popup_modify`. A
  third party (a user's `fzf --tmux` binding inside a remote pane, a future
  hook) can still stack popups on a control client. This change removes every
  instance lazytmux itself creates.

## Acceptance criteria

- [ ] `tmux-splash-maybe <session> <control-mode-client>` opens no popup and
      leaves `@splash_shown` unset.
- [ ] `tmux-splash-maybe <session> <tty-client>` opens the popup with
      `-c <client>`, in all three `splash.remote` modes.
- [ ] `tmux-splash-maybe` with a missing or unresolvable client exits non-zero
      and opens no popup (fail closed).
- [ ] The four launchers emit `-c <client>` when `--client` is given, and are
      byte-identical in behaviour when it is not.
- [ ] `prefix + a` still reaches the picker with `--claude`, and `prefix + S`
      still computes `scratch-<session>` (not `scratch---client`) — i.e.
      `--client` is consumed before the pre-existing `$1` handling.
- [ ] Both splash hook registrations pass `#{q:hook_session_name} #{q:hook_client}`
      and end in `|| true`; a real pty attach to a freshly built wrapper opens
      the splash (it does not today), and a `-CC` attach opens nothing and
      leaves `@splash_shown` unset.
- [ ] A fail-closed exit never puts the attaching pane into view mode
      (`#{pane_mode}` stays empty).
- [ ] All six bind sites pass `--client "#{client_name}"`.
- [ ] `CLAUDE.md`'s "Welcome Buffer (splash)" → Trigger paragraph records the
      control-mode condition.
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` all green.
