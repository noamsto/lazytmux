# Which lazytmux issues are upstream tmux bugs

Investigation for [#488](https://github.com/noamsto/lazytmux/issues/488). Design
spec: `docs/superpowers/specs/2026-09-03-upstream-tmux-investigation-design.md`.

**Nothing in this document has been sent to the tmux project.** Any draft report
below is a draft only; whether it is filed is the maintainer's call.

## Verdict vocabulary

| Verdict | Meaning |
|---------|---------|
| **report** | Genuine upstream defect, not already filed. |
| **already reported upstream** | Filed; the upstream number, its state, and whether its fix is in our pin are recorded. |
| **did not reproduce at the pin** | The behaviour seen on an older tmux no longer occurs at the pinned rev. |
| **do not report** | Not a tmux defect — ours, or documented tmux behaviour. Distinct from "did not reproduce". |
| **needs more work** | The evidence does not settle it. A legitimate outcome, never rounded to one of the others. |

## Ground truth

Everything below was established before any claim was tested, because a report
naming the wrong version gets closed on that alone.

### The evidence binary

| | |
|---|---|
| **Evidence binary** | `/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8/bin/tmux` |
| **`tmux -V`** | `tmux next-3.8` |
| **Built from** | tmux/tmux `40381bdcd91dac4deb177e1ed1a5794969d97d39` |
| **Source tree read for mechanisms** | `/nix/store/381m00hrpgryfvkxwnbmjkdp871qanfj-source` (same rev) |
| **Platform** | Linux 7.2.2 x86_64, NixOS 26.11 (Zokor) |

Every `file:line` citation in this document resolves at that source tree, and
every line number was verified by reading it there.

**All evidence in this document is single-platform (Linux/x86_64).** No claim
here has been checked on macOS or BSD.

### Why not `./result/bin/tmux`

`./result/bin/tmux` is a wrapper. Its last line is:

```
exec -a "$0" ".../.tmux-wrapped"  -f /nix/store/zv5h4w21rws18gw6mf5rb5zz4z76wiqv-tmux.conf "$@"
```

The baked `-f` comes **before** the user's arguments, and tmux's `-f` handling
makes the first `-f` clear the defaults while later ones *append*. So a repro's
own `-f /dev/null` does not displace the lazytmux config. Verified:

```console
$ ./result/bin/tmux -L wrapcheck -f /dev/null new-session -d
$ ./result/bin/tmux -L wrapcheck show -g aggressive-resize
aggressive-resize on          # lazytmux's setting, not a tmux default

$ /nix/store/8b8s74...-tmux-next-3.8/bin/tmux -L unwrapcheck -f /dev/null new-session -d
$ /nix/store/8b8s74...-tmux-next-3.8/bin/tmux -L unwrapcheck show -g aggressive-resize
aggressive-resize off         # the actual tmux default
```

Every reproduction below therefore runs the **unwrapped** binary on a scratch
socket with `-f /dev/null`. Where a repro sets a tmux option, that `set-option`
appears verbatim in the repro.

### Correction: the system tmux is not 3.7c

The task contract states "the system tmux on this host is **3.7c**, which is NOT
the pinned build". **Both halves are wrong at the time of this investigation.**

```console
$ tmux -V
tmux next-3.8
$ readlink -f $(which tmux)
/nix/store/ikl1vrhrqkx7d2yl4217vjnwlb0dxsl0-tmux-wrapped/bin/tmux
```

The system `tmux` is itself a **lazytmux wrapper** (an older lazytmux
generation — a different baked `tmux.conf` store path), and the binary it wraps
is `/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8` — **byte-identical
to the one the freshly built `result` wraps.**

Two consequences:

1. **There is no pinned-vs-system divergence to report.** They are the same tmux
   binary. The contract asked for a divergence finding; the finding is that there
   is none.
2. **The system tmux is wrapper-contaminated too.** Any observation taken through
   bare `tmux` on this host carries lazytmux's config. This matters for the
   contract's own claim-1a table (see claim 1a).

### The flake/lock question: not drift

The contract describes possible drift between `flake.nix` (rev `40381bdc`) and
`flake.lock` (a `tmux-upstream` node at `d5afb67a` plus a `tmux-upstream_2` node
at `40381bdc`). **This is not drift.** The lock's node graph:

```console
$ jq -r '.nodes | to_entries[] | select(.value.inputs != null) | .key as $k
       | .value.inputs | to_entries[] | select((.value|tostring)|test("tmux"))
       | "\($k) -> \(.key) = \(.value|tostring)"' flake.lock
root       -> tmux-upstream = tmux-upstream_2
tmux-remux -> tmux-upstream = tmux-upstream
```

The **root** flake's `tmux-upstream` resolves to the node Nix named
`tmux-upstream_2`, whose rev is `40381bdcd91dac4deb177e1ed1a5794969d97d39` —
exactly what `flake.nix:23` declares. The node *named* `tmux-upstream`, at
`d5afb67a`, is the **`tmux-remux` flake's own** `tmux-upstream` input. Nix
appended the `_2` suffix to disambiguate two flakes that each pin a tmux rev; it
does not indicate that the root pin drifted.

Confirmed against the built closure — exactly **one** tmux binary derivation:

```console
$ nix path-info -r ./result | grep -E 'tmux-next|/tmux-[0-9]'
/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8
```

and the root input resolves as declared:

```console
$ nix eval --impure --raw --expr '(builtins.getFlake (toString ./.)).inputs.tmux-upstream.rev'
40381bdcd91dac4deb177e1ed1a5794969d97d39
```

**Worth its own issue?** No. Nothing is wrong. The only cost is that the lock is
momentarily confusing to read, and `d5afb67a` is a rev `tmux-remux` chose, which
lazytmux does not control and does not build. Not fixed here; nothing to fix.

---

## Claim 1a — `set-hook` accepts a non-hook and stores nothing (#341)

**Verdict: do not report.** The premise is false. tmux stores the hook *and*
fires it. The in-repo root-cause note for #341 is wrong and needs revising.

### The claim under test

The contract, quoting observations attributed to "3.7c":

```
set-hook -g bogus-hook-name -> rc=1  "invalid option: bogus-hook-name"
set-hook -g pane-exited     -> rc=0, absent from show-hooks -g/-w/-p and show-options -gA
set-hook -g pane-died       -> rc=0, same
set-hook -g pane-focus-in   -> rc=0, same
set-hook -g after-kill-pane -> rc=0, stored
set-hook -g client-detached -> rc=0, stored
```

### Reproduction at the pin

```bash
T=/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8/bin/tmux
for h in bogus-hook-name pane-exited pane-died pane-focus-in after-kill-pane client-detached; do
  S="ct-$(echo $h|tr -d -)"; $T -L $S kill-server 2>/dev/null
  $T -L $S -f /dev/null new-session -d -s s
  out=$($T -L $S set-hook -g "$h" 'display -p x' 2>&1); rc=$?
  g=$($T     -L $S show-hooks -g       2>/dev/null | grep -c "^$h\[")
  w=$($T     -L $S show-hooks -w       2>/dev/null | grep -c "^$h\[")
  p=$($T     -L $S show-hooks -p       2>/dev/null | grep -c "^$h\[")
  ga=$($T    -L $S show-options -gA    2>/dev/null | grep -c "^$h")
  gw=$($T    -L $S show-hooks -g -w    2>/dev/null | grep -c "^$h\[")
  named=$($T -L $S show-hooks -g "$h"  2>/dev/null | grep -c "^$h\[")
  printf '%-16s rc=%d | -g:%s -w:%s -p:%s -gA:%s || -g-w:%s named:%s | %s\n' \
    "$h" "$rc" "$g" "$w" "$p" "$ga" "$gw" "$named" "${out:-ok}"
  $T -L $S kill-server 2>/dev/null
done
```

Output:

```
bogus-hook-name  rc=1 | -g:0 -w:0 -p:0 -gA:0 || -g-w:0 named:0 | invalid option: bogus-hook-name
pane-exited      rc=0 | -g:0 -w:0 -p:0 -gA:0 || -g-w:1 named:1 | ok
pane-died        rc=0 | -g:0 -w:0 -p:0 -gA:0 || -g-w:1 named:1 | ok
pane-focus-in    rc=0 | -g:0 -w:0 -p:0 -gA:0 || -g-w:1 named:1 | ok
after-kill-pane  rc=0 | -g:1 -w:0 -p:0 -gA:0 || -g-w:0 named:1 | ok
client-detached  rc=0 | -g:1 -w:0 -p:0 -gA:0 || -g-w:0 named:1 | ok
```

### Row-by-row diff against the contract's older-tmux table

| Behaviour | Contract ("3.7c") | Pinned `next-3.8` | Diff |
|---|---|---|---|
| `bogus-hook-name` | rc=1, `invalid option` | rc=1, `invalid option` | **same** |
| `pane-exited` | rc=0, absent from `-g`/`-w`/`-p`, `-gA` | rc=0, absent from `-g`/`-w`/`-p`, `-gA` | **same** |
| `pane-died` | rc=0, same | rc=0, same | **same** |
| `pane-focus-in` | rc=0, same | rc=0, same | **same** |
| `after-kill-pane` | rc=0, stored | rc=0, stored in `-g` | **same** |
| `client-detached` | rc=0, stored | rc=0, stored in `-g` | **same** |
| `show-hooks -g -w` | *never queried* | **`pane-exited` present** | **new** |
| `show-hooks -g <name>` | *never queried* | **`pane-exited` present** | **new** |
| Does the hook fire? | *never tested* | **yes** | **new** |

Every observation the contract recorded replicates exactly. The conclusion drawn
from them does not survive the two columns the original investigation never
queried.

(The contract attributes its table to "3.7c". As established in ground truth,
this host has no 3.7c — its `tmux` is the same `next-3.8` binary. The table was
almost certainly taken on this same build.)

### The hook fires

The decisive test the original investigation never ran:

```bash
T=/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8/bin/tmux
run_fire() {
  local scope="$1" tag="$2"
  local S="fire$tag" F="/tmp/fired-$tag.txt"
  rm -f "$F"; $T -L $S kill-server 2>/dev/null
  $T -L $S -f /dev/null new-session -d -s s
  $T -L $S set-hook $scope pane-exited "run-shell \"echo FIRED >> $F\""
  $T -L $S split-window -d -t s:0 'sleep 0.3'
  sleep 1.5
  printf 'scope=%-6s fired=%s\n' "$scope" "$( [ -s "$F" ] && echo YES || echo NO )"
  $T -L $S kill-server 2>/dev/null
}
run_fire "-g" g; run_fire "-w" w; run_fire "-g -w" gw
```

Output:

```
scope=-g     fired=YES
scope=-w     fired=YES
scope=-g -w  fired=YES
```

`set-hook -g pane-exited` **fires**. There is no silent no-op.

### Mechanism

`pane-exited` and its neighbours are **window/pane-scoped** hooks, not
session-scoped ones. The options table declares the two kinds with different
macros:

`options-table.c:242-250`
```c
#define OPTIONS_TABLE_HOOK(hook_name, default_value, hook_text) \
	{ .name = hook_name, \
	  .type = OPTIONS_TABLE_COMMAND, \
	  .scope = OPTIONS_TABLE_SESSION, \
	  .flags = OPTIONS_TABLE_IS_ARRAY|OPTIONS_TABLE_IS_HOOK, \
```

`options-table.c:252-260`
```c
#define OPTIONS_TABLE_PANE_HOOK(hook_name, default_value, hook_text) \
	{ .name = hook_name, \
	  .type = OPTIONS_TABLE_COMMAND, \
	  .scope = OPTIONS_TABLE_WINDOW|OPTIONS_TABLE_PANE, \
	  .flags = OPTIONS_TABLE_IS_ARRAY|OPTIONS_TABLE_IS_HOOK, \
```

and `pane-exited` is declared with the pane variant — `options-table.c:2005`:

```c
	OPTIONS_TABLE_PANE_HOOK("pane-exited", "",
	    "Run when the program running in a pane exits."),
```

while `client-detached` is session-scoped — `options-table.c:1974`:

```c
	OPTIONS_TABLE_HOOK("client-detached", "",
	    "Run when a client is detached."),
```

The whole behaviour follows from **which of two scope resolvers a command uses**:

- **`options_scope_from_name()`** (`options.c:1014`) looks the *name* up in
  `options_table` and dispatches on the entry's declared `.scope`. A
  window-scoped name under `-g` lands in `global_w_options`.
- **`options_scope_from_flags()`** (`options.c:1085`) ignores the name and uses
  the *flags alone*. With `-g` and no `-w`/`-p` it falls through to
  `options.c:1123-1125`:

```c
	} else {
		if (args_has(args, 'g')) {
			*oo = global_s_options;
			return (OPTIONS_TABLE_SESSION);
		}
```

`cmd-show-options.c` picks between them on whether an option name was given —
`cmd-show-options.c:102-103` (no name → list-all):

```c
	if (args_count(args) == 0) {
		scope = options_scope_from_flags(args, window, target, &oo,
		    &cause);
```

versus `cmd-show-options.c:135` (name given):

```c
	scope = options_scope_from_name(args, window, name, target, &oo,
	    &cause);
```

`set-hook` always has a name, so it always takes `options_scope_from_name`
(`cmd-set-option.c:171`, `:282`).

So:

| Command | Resolver | Table consulted | Sees `pane-exited`? |
|---|---|---|---|
| `set-hook -g pane-exited …` | `_from_name` | `global_w_options` | writes it |
| `show-hooks -g pane-exited` | `_from_name` | `global_w_options` | **yes** |
| `show-hooks -g -w` | `_from_flags` | `global_w_options` | **yes** |
| `show-hooks -g` | `_from_flags` | `global_s_options` | no |

`bogus-hook-name` is rejected because `options_match()` finds no table entry at
all, and `cmd-set-option.c:269` raises `invalid option: %s`. That is the same
lookup the accepted names pass — which is precisely why the middle group is
accepted: **they are real options.**

### Assessment

The "silent no-op" reading was an artifact of asking `show-hooks -g`, whose
documented job is the global *session* table, about a *window*-scoped hook.
`set-hook -g` routes by the name's declared scope; the list-all form of
`show-hooks` routes by flags. The two disagree only for the list-all form, and
that behaviour is uniform across every option in tmux, not special to hooks.

Asking by name — `show-hooks -g pane-exited` — returns it, so the state is not
even hidden from introspection; only the *enumeration* omits it.

There is a real ergonomic wart here: `set-hook -g X` succeeds and works, yet
`show-hooks -g` cannot show you that it did. But it is consistent, principled,
and shared by all options. It does not rise to a reportable defect, and a report
built on the "stores nothing" framing would be factually wrong on its first line.

### Consequence for the repo — follow-up, not fixed here

`docs/superpowers/plans/2026-08-10-reap-pane-state.md` records #341's root cause
as tmux accepting the `set-hook` call but never storing or firing it. **That is
disproved above**, on this exact tmux build. The prior worker on this branch
flagged this, and the flag is correct.

Per the spec's non-goals this PR does not edit that document. Recorded as a
follow-up: the #341 note needs revising, and the question of whether lazytmux
should simply register `pane-exited` at window scope (where it demonstrably
fires) should be reopened on its own merits.

---

## Claim 1c — `refresh-client -C @N:WxH` under `aggressive-resize on` (#478, #481)

**Verdict: needs more work.** The silence is real and precisely located, but the
contract's description of the outcome ("discarded") is wrong — the cap is
*stored and deferred* — and that materially weakens the case for reporting it.

### Two corrections to the claim as stated

**1. `-C @id:WxH` is control-mode-client only.** A normal attached client gets
rc=1 and `not a control client`. There is no silence for ordinary clients.
`cmd-refresh-client.c:269-272`:

```c
	if (args_has(args, 'C')) {
		if (~tc->flags & CLIENT_CONTROL)
			goto not_control_client;
		return (cmd_refresh_client_control_client_size(self, item));
	}
```

This narrows the claim considerably — but it narrows it *onto* lazytmux, because
the remote-bridge daemon's client is exactly a control-mode client. The repro
therefore requires a real `-CC` client, not a pty-attached normal one.

**2. The cap is not discarded — it is stored and deferred.** See step 6 below.

### Reproduction at the pin

Scratch socket, unwrapped binary, control-mode client attached via
`tmux -C attach-session -t s` reading commands from a fifo. Session `s` with
windows `@0` and `@1`, both 200x50; the client's current window is `@1`.

```console
$ tmux -L sock -f /dev/null set -g aggressive-resize on
$ tmux -L sock -f /dev/null show -gv aggressive-resize
on
```

**Cap on the client's SELECTED window** (`@1` → 120x30):

```
refresh-client -C @1:120x30
%begin 1788428287 343 1
%end 1788428287 343 1            <- rc=0
```
`list-windows` → `@1 120x30 active=1`. **Applied immediately.**

**Cap on the UNSELECTED window** (`@0` → 100x20), `aggressive-resize` still on:

```
refresh-client -C @0:100x20
%begin 1788428293 346 1
%end 1788428293 346 1            <- rc=0, no %error anywhere on the stream
```
`list-windows` → `@0 200x50 active=0`. **Size unchanged, no diagnostic.**

**Isolating the discriminator** — `set -g aggressive-resize off`, same call on
the still-unselected `@0`:

```
refresh-client -C @0:90x18
%begin 1788428300 353 1
%end 1788428300 353 1
```
`list-windows` → `@0 90x18 active=0`. **Applied.** So `aggressive-resize`, not
unselected-ness alone, is the discriminator.

**Does the cap survive?** `aggressive-resize` back on; set `@0` → 77x21 while
unselected (rc=0, no effect, size stays 90x18); then `select-window -t @0` with
**no `-C` resent**:

```
%session-window-changed $0 @0
%layout-change @0 a99d,77x21,0,0,0 ...
```
`list-windows` → `@0 77x21 active=1`. **The cap fired on selection.** It was
stored the whole time.

**Is a pending cap readable?** No. `format.c` exposes nothing backed by
`control_get_window_size`/`control_windows`; no `list-clients` field surfaces a
control client's pending per-window cap. It is invisible until it takes effect.

### Mechanism

`cmd-refresh-client.c:70-105` parses `@%u:%ux%u`, calls `control_set_window_size()`
(a per-client RB tree, `control.c:218-234`), sets `CLIENT_WINDOWSIZECHANGED`, and
calls `recalculate_sizes_now(1)`. Its only error branch is a bad or oversized
argument; nothing checks whether the window is selected.

The sizing engine `clients_calculate_size()` (`resize.c:132`) has **two** loops
and both are gated by the same `skip_client()` callback — including the second
loop, whose entire job is to clamp the result down to a stored per-window cap
(`resize.c:229-249`):

```c
	if (w != NULL) {
		TAILQ_FOREACH(loop, &clients, entry) {
			if (loop != c && ignore_client_size(loop))
				continue;
			if (loop != c && skip_client(loop, type, current, s, w))
				continue;

			/* Look up per-window size if any. */
			if (~loop->flags & CLIENT_WINDOWSIZECHANGED)
				continue;
			if (!control_get_window_size(loop, w->id, &cx, &cy))
				continue;
```

The predicate is `recalculate_size_skip_client` (`resize.c:342-356`) — and tmux's
own comment states the intent plainly:

```c
	/*
	 * If the current flag is set, then skip any client where this window
	 * is not the current window - this is used for aggressive-resize.
	 * Otherwise skip any session that doesn't contain the window.
	 */
	if (loop->session->curw == NULL)
		return (1);
	if (current)
		return (loop->session->curw->window != w);
	return (session_has(loop->session, w) == 0);
```

with `current` bound directly to the option (`resize.c:378`):

```c
	current = options_get_number(w->options, "aggressive-resize");
```

So under `aggressive-resize on`, a client whose current window is not `w` is
skipped from **both** loops — including the clamp loop that would have applied
its cap. Under `aggressive-resize off` the predicate only asks whether the
session contains the window, so the clamp loop runs and the cap applies.

There is a `log_debug` at `resize.c:243` when a cap *is* applied, but that is a
debug-log line, not a user-facing diagnostic, and nothing logs the skip.

### What documented behaviour is NOT being disputed

`tmux.1` on `aggressive-resize` (lines 5655-5669):

> Aggressively resize the chosen window. This means that tmux will resize the
> window to the size of the smallest or largest session (see the window-size
> option) for which it is the current window, rather than the session to which
> it is attached. The window may resize when the current window is changed on
> another session […]

**We do not dispute that.** That a window's size is driven only by clients for
which it is *currently the selected window* is the documented, intended
behaviour of `aggressive-resize`, and steps 3-5 above confirm it works as
documented. We are not claiming `aggressive-resize` is buggy, wrongly specified, or
under-documented, and we are not asking for the sizing outcome to change.

`tmux.1` on `refresh-client -C` (lines 1485-1496):

> `-C` sets the width and height of a control mode client or of a window for a
> control mode client, size must be one of `widthxheight` or
> `window ID:widthxheight`, for example `80x24` or `@0:80x24`.

The gap is that this says what `-C` *sets* and never says *when* it applies.

### Assessment — argued both ways

**For reporting.** A documented, validated API call returns success with no
`%error` while its effect is silently deferred, and there is no way for the
caller to tell "applied" from "pending". A control-mode consumer — the exact
audience for this flag — has no signal at all. That is an observability gap
independent of whether the resize policy is right.

**Against reporting.** The docs never promise immediate effect, and
`aggressive-resize`'s own documentation already tells the reader that sizing is
bound to current-window status. A reader who has both paragraphs can derive this
behaviour. More decisively: **the cap is honoured the moment the window is
selected.** "Accepted then silently ignored forever" would be a defect;
"accepted, and applied when it becomes applicable" is a queue, and queues do not
usually owe the caller a warning. The contract's own instruction not to oversell
this one is well taken, and the deferral finding weakens it further than the
contract anticipated.

**Uncertain.** Whether upstream would want a diagnostic or a format field
exposing a pending-but-inactive cap is genuinely unclear, and no upstream
discussion of this specific gap was searched for. That uncertainty is why this
carries **needs more work** rather than a verdict in either direction — the
honest next step is to search upstream for prior discussion before deciding,
not to file on what we have.

**Platform:** Linux/x86_64 only.
