# Which lazytmux issues are upstream tmux bugs

Investigation for [#488](https://github.com/noamsto/lazytmux/issues/488). Design
spec: `docs/superpowers/specs/2026-09-03-upstream-tmux-investigation-design.md`.

**Nothing in this document has been sent to the tmux project.** Any draft report
below is a draft only; whether it is filed is the maintainer's call.

## Verdicts at a glance

| Claim | lazytmux issue | Verdict | Why, in one line |
|---|---|---|---|
| **1a** `set-hook` accepts a non-hook | [#341](https://github.com/noamsto/lazytmux/issues/341) | **do not report** | Premise false — the hook is stored *and* fires; only `show-hooks -g`'s enumeration omits it. |
| **1b** `show-options -t '=name'` | [#474](https://github.com/noamsto/lazytmux/issues/474), [#476](https://github.com/noamsto/lazytmux/issues/476) | **already reported upstream** | [tmux/tmux#4594](https://github.com/tmux/tmux/issues/4594) — closed without a fix; still reproduces at the pin. |
| **1c** `refresh-client -C` under `aggressive-resize` | [#478](https://github.com/noamsto/lazytmux/issues/478), [#481](https://github.com/noamsto/lazytmux/issues/481) | **do not report** | The cap is deferred, not discarded; it fires on selection. A docs gap, not a defect. |
| **2** display-popup SEGV | [#346](https://github.com/noamsto/lazytmux/issues/346) | **already reported upstream** | [tmux/tmux#5551](https://github.com/tmux/tmux/issues/5551) — fixed by `af3e4d2e`, **not in our pin**; reproduces 3/3. |

**Is claim 1 one family?** No — see [the family judgement](#is-claim-1-one-family-or-three-things-that-only-rhyme). Three unrelated
mechanisms in three files; the shared symptom does not survive tightening.

**Nothing is recommended for filing.** No claim carries the **report** verdict.
One draft is included below for the single borderline case (1b), with a
recommendation *against* sending it.

**Nothing was sent to the tmux project.**

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

**Platform:** Linux/x86_64 only.

---

## Claim 1b — `show-options -t '=name'` (#474, #476)

**Verdict: already reported upstream.** tmux/tmux#4594 covers exactly this
mechanism (filed against `set-option`, confirmed by the maintainer as the same
root cause `show-options` hits), and was closed by the maintainer with no
linked fix commit. Our reproduction on the pinned `next-3.8` build shows the
bug still present, so the closure did not resolve it in code.

### Reproduction at the pin

Scratch socket, unwrapped binary, sessions `zz` and `zzz` (`zz` a strict
prefix of `zzz`, so exact- vs prefix-match is observable).

```bash
T=/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8/bin/tmux
S=b1b-showopt-$$
"$T" -L "$S" -f /dev/null new-session -d -s zz
"$T" -L "$S" new-session -d -s zzz
```

Output (full script run verbatim, only exit codes/output paraphrased where
identical):

```
== 1. has-session -t '=zz' (exact) ==
rc=0 out=''

== 2. has-session -t 'zz' (prefix) ==
rc=0 out=''

== 3. positive control: set-option -t 'zz' @x 1, then show-options -t 'zz' @x ==
set rc=0 out=''
show(prefix) rc=0 out=@x\ 1

== 4. show-options -t '=zz' @x (exact) ==
rc=1 out=no\ such\ session:\ =zz

== 5. show-options -q -t '=zz' @x (exact, quiet — @x DOES exist, target just can't resolve) ==
rc=0 out=''

== 5b. show-options -q -t 'zz' @doesnotexist (prefix, quiet — target resolves fine, option genuinely unset) ==
rc=0 out=''

== 6. set-option -t '=zz' @y 2   (does WRITE side accept =?) ==
rc=1 out=no\ such\ session:\ =zz
  readback via prefix form: rc=1 out=invalid\ option:\ @y   <- @y was never written

== 7. display-message -p -t '=zz' '#{session_name}' (exact) ==
rc=0 out=''

== 8. list-sessions -F '#{session_name} #{@x} #{@y}' ==
rc=0 out=$'zz 1 \nzzz  '

== 9. switch-client -t '=zz' with no client attached ==
rc=1 out=no\ current\ client        <- distinct error, not a target-resolution failure

== 10. kill-session -t '=zz' (exact) — run LAST ==
rc=0 out=''
(list-sessions afterward: only "zzz" remains)
```

Full script: `/tmp/claude-1000/-home-noams-Data-git--worktrees-noamsto-lazytmux-feat-488-investigate-which-lazytmux-issues-are-up/955e1def-f7e0-4551-9a41-299c2513ce65/scratchpad/repro-1b.sh`

Key results:

| Command | `-t '=zz'` (exact) | Honours `=`? |
|---|---|---|
| `has-session` | rc=0 | **yes** |
| `kill-session` | rc=0, session gone | **yes** |
| `switch-client` | rc=1 `no current client` (fails *before* target resolution — no client to switch) | not observable here; see Mechanism |
| `display-message -p` | rc=0, empty (matches `-t zz` prints nothing for `#{session_name}` too under `-p`'s own quirks — not further chased, out of scope) | inconclusive from this repro alone |
| `show-options` | rc=1 `no such session: =zz` | **no** |
| `set-option` | rc=1 `no such session: =zz` | **no — write side rejects it too** |

Test 6 answers the task's specific question directly: **`set-option -t '=zz'`
does not work either.** The repo's CLAUDE.md framing ("a script can stamp an
option with `set-option -t "$name"` and never read it back") is accurate about
what the repo's scripts actually do (write via the bare, unprefixed name — they
never pass `=` to `set-option`), but it is not evidence that `set-option`
*would* accept `=` if asked; it doesn't. Both commands are broken by the
identical mechanism (see below).

### Mechanism

**Root cause: which `enum cmd_find_type` a command's target is declared as,
combined with an `=`-stripping step that only looks at two of the three
target-string slots it can be routed into.**

`has-session` and `kill-session` declare a *session*-typed static target:

`cmd-new-session.c:61` (has-session):
```c
	.target = { 't', CMD_FIND_SESSION, 0 },
```

`cmd-kill-session.c:44`:
```c
	.target = { 't', CMD_FIND_SESSION, 0 },
```

`show-options` and `set-option` instead declare a *pane*-typed target — the
same declaration, verbatim:

`cmd-show-options.c:57`:
```c
	.target = { 't', CMD_FIND_PANE, CMD_FIND_CANFAIL },
```

`cmd-set-option.c:46`:
```c
	.target = { 't', CMD_FIND_PANE, CMD_FIND_CANFAIL },
```

`switch-client` has **no static `.target` at all** (`cmd-switch-client.c`, entry
struct comment: `/* -t is special */`) — it inspects the raw string itself and
picks the type at runtime, `cmd-switch-client.c:68-76`:

```c
	if (tflag != NULL &&
	    (tflag[strcspn(tflag, ":.%")] != '\0' || strcmp(tflag, "=") == 0)) {
		type = CMD_FIND_PANE;
		flags = 0;
	} else {
		type = CMD_FIND_SESSION;
		flags = CMD_FIND_PREFER_UNATTACHED;
	}
	if (cmd_find_target(&target, item, tflag, type, flags) != 0)
```

A bare `=zz` has no `:`/`.`/`%` and isn't literally `"="`, so `switch-client`
picks `CMD_FIND_SESSION` — it dodges the bug by re-deriving the type from the
string's shape, not by being declared session-typed.

All four routes end up in the same shared parser, `cmd_find_target()`
(`cmd-find.c`). Its type-dispatch switch, reached only when the string has no
`$`/`@`/`%` sigil and no `:`/`.` separator (a bare name — exactly `zz`/`=zz`),
is keyed on the caller's declared type — `cmd-find.c:1101-1111`:

```c
		switch (type) {
		case CMD_FIND_SESSION:
			session = copy;
			break;
		case CMD_FIND_WINDOW:
			window = copy;
			break;
		case CMD_FIND_PANE:
			pane = copy;
			break;
		}
```

`=zz` becomes `session = "=zz"` for `has-session`/`kill-session`, but
`pane = "=zz"` for `show-options`/`set-option`. The very next block only
strips `=` and sets the exact-match flag for the `session`/`window` slots —
`cmd-find.c:1116-1122`:

```c
	/* Set exact match flags. */
	if (session != NULL && *session == '=') {
		session++;
		fs->flags |= CMD_FIND_EXACT_SESSION;
	}
	if (window != NULL && *window == '=') {
		window++;
		fs->flags |= CMD_FIND_EXACT_WINDOW;
	}
```

There is no equivalent `pane`-slot case. So for `show-options`/`set-option`,
`pane` stays `"=zz"` — `=` and all — and falls through
(`cmd-find.c` "If just pane is present" branch) into `cmd_find_get_pane()`
(`cmd-find.c:516`), which fails to match it as a pane spec, then tries it as a
window (`cmd-find.c:539` comment: *"Otherwise try as a window itself (this
will also try as session)"*), which fails as a window and falls again
(`cmd-find.c:347` comment: *"Otherwise try as a session itself"*) into
`cmd_find_get_session(fs, "=zz")`. There, `session_find("=zz")` — a literal
string lookup, `cmd-find.c:279-281` — obviously finds nothing (no session is
named `=zz`), and because `CMD_FIND_EXACT_SESSION` was never set on this path,
the fallback prefix/glob search runs anyway (`cmd-find.c:291-295`) and also
fails, since no real session name begins with the literal character `=`.
`fs->s` ends up `NULL`.

The user-facing message is not generated in `cmd-find.c` at all (its own
`no_pane:` label would say `"can't find pane: =zz"`, and per
`CMD_FIND_CANFAIL` that failure is swallowed as a non-fatal `fs->s == NULL`).
It comes from `options.c`, which both `cmd-show-options.c` and
`cmd-set-option.c` call to resolve the option scope, and which merely observes
the empty state and echoes the *raw* `-t` argument text back:

`options.c:1014-1046` (`options_scope_from_name`, used when an option name was
given):
```c
	case OPTIONS_TABLE_SESSION:
		if (args_has(args, 'g')) {
			*oo = global_s_options;
			scope = OPTIONS_TABLE_SESSION;
		} else if (s == NULL && target != NULL)
			xasprintf(cause, "no such session: %s", target);
```

`options.c:1085-1129` (`options_scope_from_flags`, used for the no-option-name
listing form) has the identical pattern at line 1129. This is why *both*
nominally `target-pane` commands report `"no such session"` rather than
`cmd-find.c`'s own `"can't find pane"` — they share this second, options-layer
lookup that `kill-pane` or other plain pane-target commands don't go through.

### The `-q` question

`-q` does not distinguish "target failed to resolve" from "target resolved,
option genuinely unset." Both paths in `cmd_show_options_exec`
(`cmd-show-options.c`) converge on the same `out:` label and the same
`return (CMD_RETURN_NORMAL)`:

Target-resolution failure — `cmd-show-options.c:135-142`:
```c
	scope = options_scope_from_name(args, window, name, target, &oo,
	    &cause);
	if (scope == OPTIONS_TABLE_NONE) {
		if (args_has(args, 'q'))
			goto out;
		cmdq_error(item, "%s", cause);
		free(cause);
		goto fail;
	}
```

Genuinely-unset option (target resolved fine, the `@`-option just was never
set) — `cmd-show-options.c:161-165`:
```c
	} else if (*name == '@') {
		if (args_has(args, 'q'))
			goto out;
		cmdq_error(item, "invalid option: %s", argument);
		goto fail;
	}
```

Shared destination, `cmd-show-options.c:170-173`:
```c
out:
	free(name);
	free(array_key);
	free(argument);
	return (CMD_RETURN_NORMAL);
```

Both `-q` branches produce identical observable state: **rc=0, empty stdout,
empty stderr.** Test 5 (`show-options -q -t '=zz' @x` — `@x` genuinely exists,
only the target is unreachable) and test 5b (`show-options -q -t 'zz'
@doesnotexist` — target resolves, option genuinely never set) return
byte-identical output. The claim as stated holds exactly, including on return
code — there is no `rc` split to fall back on; a caller has zero signal.

### There is a working exact-match form: `-t '=name:'`

Upstream #4594 mentions in passing that `-t '=foo.'` and `-t '=foo:'` work
where a bare `-t '=foo'` does not. That follows directly from the mechanism
above — a `:` or `.` makes `cmd_find_target` split the string rather than take
the bare-name branch, so the session component lands in the `session` slot,
where the `=`-strip applies. Verified at the pin, and verified to be a *real*
exact match rather than merely parsing (session `abcdef` is the only session):

```console
$ tmux -L s show-options -t 'abc:'     @x
rc=0  @x 42            <- prefix match reaches abcdef, as expected
$ tmux -L s show-options -t '=abc:'    @x
rc=1  no such session: =abc:
                       <- exact match ENFORCED: correctly refuses to prefix-match
$ tmux -L s show-options -t '=abcdef:' @x
rc=0  @x 42            <- exact match on the real name, succeeds
$ tmux -L s set-option  -t '=abcdef:' @y 7
rc=0                   <- the write side works the same way
```

So the capability is not missing from `show-options`/`set-option` — only the
bare `=name` spelling is. This matters twice over: it weakens the report (a
documented-adjacent workaround exists and needs no upstream change), and it
gives this repo a strictly better read than the bare, prefix-matching
`-t "$name"` it currently recommends. Recorded as a follow-up.

### Assessment — argued both ways

**For reporting.** Our reproduction adds two things not in the existing
upstream thread: (1) `show-options` is affected by the *identical* mechanism
as `set-option`, not merely something a user might assume by analogy — and it
reports the misleading `"no such session"` text via the same `options.c`
codepath, not `cmd-find.c`'s `"can't find pane"` that the issue author
observed from *other* pane-target commands; (2) the `-q` collapse of "error"
and "genuinely unset" into byte-identical output, which the existing issue
thread never tests. Both are new information a fresh, narrower issue could
usefully add.

**Against reporting.** The mechanism and root cause are already filed,
already correctly diagnosed by the reporter down to the exact `cmd_find_target`
code path (their last comment independently reaches the same `session`/`pane`
variable-routing explanation as above), and already acknowledged by the
maintainer as correct (`"Because it is a pane target the = applies to it as a
pane..."`). The maintainer's own tone on record — `"does it really matter that
much?"` — signals this is a known, deliberately low-priority wart, not an
overlooked bug. Filing a second, more narrowly-scoped issue over a closed one
covering the same root cause reads as noise rather than help, especially since
the additional information (show-options specifically, `-q` behavior) is a
strict subset of the same fix `LunarLambda` already offered to write.

**On record from the existing issue** (tmux/tmux#4594, filed 2025-08-20 by
LunarLambda, closed 2025-11-12 by maintainer `nicm`, `stateReason: COMPLETED`,
**no commit linked to the close event**):

> Because it is a pane target the `=` applies to it as a pane, but because
> `set-option` is special it also tries it as a session if a pane doesn't
> work, so the error message says session rather than pane. — nicm

> I do think the fallback logic should handle `=` correctly, thus allowing
> `-t =foo` to work, since `=foo` can actually *never* refer to a pane
> (`=name` syntax is only valid for sessions and windows) [...] I could write
> a PR for this — LunarLambda

> It should only be `set-option` I think probably, but does it really matter
> that much? — nicm (final substantive comment before closure)

No PR is linked anywhere on the issue, and the close event carries
`"commit_id": null`. Combined with our reproduction showing the exact
`no such session: =zz` behavior still present on `next-3.8` (a build after the
issue's closure date), the issue was closed **without a code fix landing** —
this reads as "acknowledged, deprioritized, closed" rather than "resolved."

**On documentation.** `tmux.1` (`SRC/tmux.1:763-786`) states the `=`-exact
rule as a property of `target-session` grammar generically ("If the session
name is prefixed with an `=`, only an exact match is accepted"), and
`target-pane` (`tmux.1:858-866`) is documented as "may be a pane ID or takes a
similar form to `target-window`" — which itself says its session component
"follows the same rules as for `target-session`." Read at face value, the docs
imply `=name` should behave identically regardless of which target-type
grammar a command declares. The C implementation does not honor that implied
uniformity for the single-token (no `:`/`.`) case — a real gap between
documented and actual behavior, which is exactly what the upstream issue
thread (independently) converged on too.

**Platform:** Linux/x86_64 only.

---

## Claim 1c — `refresh-client -C @N:WxH` under `aggressive-resize on` (#478, #481)

**Verdict: do not report.** The silence is real and precisely located, but the
contract's description of the outcome ("discarded") is wrong — the cap is
*stored and deferred* — and that materially weakens the case for reporting it.

*(This section was written to a `needs more work` verdict on one named open
question. That question has since been answered; see "Resolving the open
question" at the end of this claim.)*

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

### Resolving the open question: is this discussed upstream?

The section above carried **needs more work** on one specific unknown, stated at
the time as: *"no upstream discussion of this specific gap was searched for …
the honest next step is to search upstream before deciding."* That search has
now been done, and it settles the claim.

**Searched:** the tmux issue tracker and wiki, for the per-window control-mode
size form and its interaction with `aggressive-resize`.

| Source | What it says about `refresh-client -C @id:WxH` |
|---|---|
| [tmux wiki, Control-Mode](https://github.com/tmux/tmux/wiki/Control-Mode) | Documents `refresh-client -C` for the **whole-client** size only — *"sets the size of a control mode client. If this is not used, control mode clients do not affect the size of other clients no matter the value of the window-size option."* **The `@windowid:WxH` per-window form is not mentioned at all**, nor is any interaction with `aggressive-resize`, nor any statement about windows the control client has not selected. |
| [tmux#2594](https://github.com/tmux/tmux/issues/2594) (`-CC` still determines window size after detaching) | Adjacent — control-mode clients constraining window size — but about a **detached** client, not a per-window cap on an unselected window. Not this gap. |
| [tmux#947](https://github.com/tmux/tmux/issues/947), [tmux#720](https://github.com/tmux/tmux/issues/720) | Whole-client sizing and a man-page omission for `-C`. Not this gap. |

**No upstream issue, PR, or mailing-list thread describing this behaviour was
found.** So the gap is not already reported — but nor has anyone else tripped
over it hard enough to report it, in a feature whose entire audience is
control-mode consumers.

### Verdict — settled

**Verdict: do not report.**

The claim does not survive the deferral finding. Weighing the two sides
recorded above against the search result:

- The **sizing** is documented behaviour and is not in dispute (stated in words
  above).
- The **cap is not lost.** It is stored per client in an RB tree and applied the
  moment the window becomes current. "Accepted, then applied when it becomes
  applicable" is a queue. A queue does not owe its caller a warning, and the
  contract's instruction not to oversell this one is what the evidence supports.
- What genuinely remains is **two documentation gaps and one missing
  introspection field** — the man page says what `-C` *sets* but never *when* it
  applies; the wiki does not document the per-window form at all; and no
  `format.c` field exposes a stored-but-not-yet-applied cap. Those are
  enhancement requests, not a defect, and they are a materially different thing
  from the "accepted then silently discarded" bug this claim was opened as.

A maintainer receiving this as a bug report would be right to close it. Someone
could reasonably offer the wiki a paragraph documenting `@id:WxH` and its
current-window condition; that is a docs contribution, not this investigation's
finding, and it is not drafted here.

**What was wrong in the original claim, for the record:** the claim said the cap
is *discarded*. It is not. That single correction is what moves this from a
plausible report to a non-report.

---

## Claim 2 — the display-popup SEGV (#346)

**Verdict: already reported upstream.** Filed as
[tmux/tmux#5551](https://github.com/tmux/tmux/issues/5551), fixed by
`af3e4d2e` on 2026-08-31 — three days *after* our pin, so the crash still
reproduces at the pinned rev (3/3 attempts, SIGSEGV, stack frame-for-frame
identical to the issue's).

### Upstream prior art: tmux/tmux#5551

| | |
|---|---|
| **Title** | `display-popup on a control-mode client crashes the server (popup_modify -> tty_resize NULL deref)` |
| **Reporter** | `noamsto` |
| **Opened** | 2026-08-29T20:32:04Z |
| **State** | **closed**, `state_reason: completed` |
| **Closed by** | `nicm`, 2026-08-31T07:47:04Z |
| **Closing comment** | "Applied to OpenBSD now, will be in GitHub later. Thanks!" |
| **Fixing commit** | `af3e4d2e5b63fa2db14834b17b20b40771744b91`, 2026-08-31T07:46:55Z — *"Do not allow popups for control clients, from Noam Stolero."* (reached GitHub `master` via merge `b28244e2fe44`, 2026-08-31T14:56:14Z) |

The fix is three added lines in `cmd-display-menu.c`:

```diff
@@ -592,6 +592,8 @@ cmd_display_popup_exec(struct cmd *self, struct cmdq_item *item)
 		server_client_clear_overlay(tc);
 		return (CMD_RETURN_NORMAL);
 	}
+	if (tc->flags & CLIENT_CONTROL)
+		return (CMD_RETURN_NORMAL);
 	if (!modify && tc->overlay_draw != NULL)
 		return (CMD_RETURN_NORMAL);
```

**It is not in our pin.** Our `tmux-upstream` rev `40381bdc` was committed
2026-08-28T10:32:01Z, three days before the fix. Confirmed by reading the
source rather than by date alone — `cmd-display-menu.c:589-595` at the pin:

```c
	if (args_has(args, 'C')) {
		server_client_clear_overlay(tc);
		return (CMD_RETURN_NORMAL);
	}
	if (!modify && tc->overlay_draw != NULL)
		return (CMD_RETURN_NORMAL);
```

`grep -n CLIENT_CONTROL` over `$SRC/cmd-display-menu.c` and `$SRC/popup.c`
returns **nothing** at the pin. The guard is absent from both files.

**Citations re-resolved against the pin.** The design doc
`docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md` cites
line numbers taken against tmux `d5afb67`. Two of its four moved:

| Step | Design doc (`d5afb67`) | This pin (`40381bdc`) |
|---|---|---|
| control client never gets a tty | `server-client.c:2947` | **`server-client.c:2996`** |
| `modify = popup_present(tc)` | `cmd-display-menu.c:581` | `cmd-display-menu.c:581` (unmoved) |
| `tty_resize(&c->tty)` in the `lines != BOX_LINES_DEFAULT` branch | `popup.c:550` | **`popup.c:551`** (branch opens at `popup.c:541`) |
| null deref | `tty.c:124` | `tty.c:124` (`c = tty->client` at `:126`, `ioctl(c->fd, …)` at `:130`) |

The mechanism itself is unchanged at the pin. `server-client.c:2996`:

```c
	if (c->flags & CLIENT_CONTROL)
		control_start(c);
	else if (c->fd != -1) {
		if (tty_init(&c->tty, c) != 0) {
```

so `tty->client` is never assigned (it is set only at `tty.c:111`, inside
`tty_init`), and `tty_resize` opens by dereferencing it.

### Reproduction attempts at the pin

Unwrapped binary, `-f /dev/null`, scratch sockets `c2-*`. Control-mode client
attached over a fifo (the claim 1c technique). Script:
`…/scratchpad/c2/h0.sh`.

```bash
T=/nix/store/8b8s74j9cf64fb6snq5qn4jd4237sndg-tmux-next-3.8/bin/tmux
t() { "$T" -L "$SOCK" -f /dev/null "$@"; }

mkfifo "$FIFO"
t new-session -d -s s
"$T" -L "$SOCK" -f /dev/null -C attach-session -t s < "$FIFO" > "$OUT" 2>&1 &
exec 3> "$FIFO"; sleep 1
CLIENT=$(t list-clients -t s -F '#{client_control_mode} #{client_name}' | awk '$1==1{print $2; exit}')

t display-popup -c "$CLIENT" -E 'sleep 30' &      # popup 1
sleep 1
t display-popup -c "$CLIENT" $FLAG2 -E true       # popup 2
```

**The known lethal shape — all three variants crashed.**

| # | popup 2 | server after popup 1 | popup 2 rc | server after popup 2 |
|---|---|---|---|---|
| 1 | `-B -E true` | alive (`has-session` rc=0) | 1, `server exited unexpectedly` | **DEAD** — `no server running on /run/user/1000/tmux-1000/c2-h0-v1` |
| 2 | `-b rounded -E true` | alive | 1, `server exited unexpectedly` | **DEAD** |
| 3 | `-B true` (no `-E`) | alive | 1, `server exited unexpectedly` | **DEAD** |

Each ended the control stream with:

```
%exit server exited unexpectedly
```

`coredumpctl` recorded three SIGSEGVs of
`/nix/store/8b8s74…-tmux-next-3.8/bin/tmux`, one per attempt (12:55:00,
12:55:13, 12:55:21). The backtrace of the third:

```
Stack trace of thread 2677655:
#0  tty_resize (tmux + 0xc7b0f)
#1  popup_modify (tmux + 0x97249)
#2  cmd_display_popup_exec (tmux + 0x333ae)
#3  cmdq_next (tmux + 0x4339a)
#4  server_loop (tmux + 0xb33fd)
#5  proc_loop
```

Frame for frame the stack in #346. `-E` is irrelevant (attempt 3), and `-b
<lines>` is as lethal as `-B` (attempt 2) — both satisfy `lines !=
BOX_LINES_DEFAULT` at `popup.c:541`. That matters for lazytmux specifically:
every wrapper-script launcher carries `-b rounded`, not `-B`.

**Negative control — the border flag on the *second* call is the
discriminator.** Script `…/scratchpad/c2/ctl.sh`:

```
== single popup, -B, backgrounded ==
  has-session rc=0                     <- alive
== now a SECOND popup, NO border flag (lines==DEFAULT) ==
  popup2 rc=0
  has-session rc=0                     <- still alive
```

So one popup is harmless however it is bordered (`popup_display` never calls
`tty_resize`), and a second popup is harmless too as long as it omits
`-B`/`-b` (`popup_modify` skips the `lines != BOX_LINES_DEFAULT` branch). This
is the direct confirmation of the design doc's step 3, and of why
`tests/tmux-next38-readiness.bats`'s single-popup case has always passed.

### Time-box spend

| | |
|---|---|
| Known shape (design doc's) | 3 of 3 attempts used — **3/3 crashed** |
| Negative control (not a hypothesis) | 1 run |
| Extra hypotheses | **0 of 4 used** |

The extra hypotheses were conditioned on the known shape *not* crashing. It
crashed on the first attempt, so none were needed and none were run. Not
reached, and recorded as untested rather than as absent behaviour:

- popup at a control client before it has negotiated a size;
- popup `-t` a window in a session the control client is not attached to;
- popup while the control client is mid-`refresh-client -C` resize;
- popup opened, then the control client detaching underneath it.

Whether any of those crash *independently* of the two-popup path is unknown.
It is moot for reporting — upstream's guard returns before any of them can
reach `popup_display`/`popup_modify` — but it is not moot for the pin, where
those paths remain untested.

### Mitigation coverage

Two mitigations shipped, and together they close every path lazytmux itself
creates.

**1. `-c` client pinning on the wrapper-script popups.** All four launchers
take `--client` and forward it (`scripts/tmux-session-picker.sh:22-28`, same
shape at `tmux-window-picker.sh:31`, `tmux-window-wall.sh:17`,
`tmux-scratchpad.sh:51`):

```bash
# Pin the client: unpinned, tmux re-resolves to the session's most-recently-active
# client, which on a bridged host can be the tty-less control client (#346,
# reported upstream as tmux/tmux#5551 — drop the pin once that ships).
POPUP_CLIENT=()
[[ -n $CLIENT ]] && POPUP_CLIENT=(-c "$CLIENT")
tmux display-popup "${POPUP_CLIENT[@]}" -E -w 90% -h "$HEIGHT" -b rounded …
```

fed from six bind sites (`config/tmux.conf.nix:800,848-851,854`):

```
bind s run-shell '${script.tmux-session-picker}/bin/tmux-session-picker #{?client_name,--client #{q:client_name},}'
```

This removes the `cmd_find_best_client` walk that let a `-CC` client win, which
was the whole of reachable class (b).

**One residual, not reachable from the current bind set.** The
`#{?client_name,…,}` conditional is fail-*open*: with `client_name` empty the
flag is omitted and the popup falls back to best-client resolution — the
opposite of the splash gate's fail-closed R4. All six sites are key/mouse
binds, which always run with a client, so nothing today hits it; a future
`run-shell` invocation from a hook would.

**2. The splash control-mode gate** (`scripts/tmux-splash-maybe.sh:20-26`) —
see the next section. This closes class (a).

**Does anything remain reportable? No.** The residual the design doc names as
a non-goal — "a third party can still stack popups on a control client" — is
exactly what upstream `af3e4d2e` fixes, and it is filed and closed. The
unpinned direct binds (`prefix + C-Space` at `config/tmux.conf.nix:859`, which
is itself a `display-popup -E -B`; `prefix + n` at `:888`) execute on the
pressing client's own command queue, which `cmd_find_current_client` returns
without consulting `cmd_find_best_client`, so no control client can be their
target. `grep -rn display-popup picker/` returns nothing.

The one open question is **when the pin advances past `af3e4d2e`** — at that
point the four `-c` pins and the splash gate become belt-and-braces, and the
four in-tree comments saying "drop the pin once that ships" become actionable.
Not a reportable finding; a follow-up.

### Splash gate — assessment only

**It already exists.** `scripts/tmux-splash-maybe.sh:15-26`, verbatim:

```bash
# The remote bridge attaches a -CC control-mode client. It has no tty, so a
# second popup on it dereferences NULL inside tmux and takes the whole server
# down (#346, upstream tmux/tmux#5551) — and a mirror has no business showing
# a splash. Deliberately without setting @splash_shown: a later real attach
# must still get it.
client="${2:-}"
control=""
while read -r mode name; do
	[ "$name" = "$client" ] && control="$mode" || true
done < <(tmux list-clients -t "$session" -F '#{client_control_mode} #{client_name}' 2>/dev/null)
[ -n "$control" ] || exit 1
[ "$control" = 1 ] && exit 0
```

fed by both hook registrations (`config/tmux.conf.nix:1214-1215`), which pass
the client and tolerate the fail-closed exit:

```
set-hook -g client-attached[50]        'run-shell -b "…/tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true"'
set-hook -g client-session-changed[50] 'run-shell -b "…/tmux-splash-maybe #{q:hook_session_name} #{q:hook_client} || true"'
```

The gate scans the session's client roster rather than querying
`display-message -c`, which is the fail-open trap the design doc measured, and
an unresolvable client name leaves `control` empty and exits 1 before any
popup. Both popup calls further downstream (`:70`, `:77`) carry
`-c "$client"`.

So the answer to "should it gate regardless of the crash" is yes, and it does.
**No change recommended, and none made.** One thing checked rather than
assumed: `[ "$control" = 1 ] && exit 0` under `set -euo pipefail` does *not*
abort the script when the test fails — bash exempts a `&&`-list whose failure
is not the script's last command, verified directly (`REACHED-TAIL`, rc=0), so
a non-control client falls through to the rest of the gate as intended.

**Platform:** Linux/x86_64 only. The SEGV, the three crashes and the negative
control were all taken on this host; the null deref is
platform-independent by inspection (`tty->client` is `NULL` on any platform
where a control client skips `tty_init`), but that has not been checked on
macOS or BSD.

---

## Is claim 1 one family, or three things that only rhyme?

The task opened on a hypothesis: that 1a, 1b and 1c are one defect class — *"a
tmux command that cannot take effect returns success with no diagnostic"* —
worth a single report. Having now read all three mechanisms in the source, the
answer is no.

**The framing should be abandoned.** It was a good hypothesis; it does not
survive the evidence.

### The mechanisms are unrelated

Three different files, three different subsystems, no shared code path and no
shared root cause:

| Claim | Mechanism | File |
|---|---|---|
| 1a | Two scope resolvers disagree: `set-hook` routes by the option's declared scope (`options_scope_from_name`), the list-all form of `show-hooks` routes by flags (`options_scope_from_flags`). | `options.c` |
| 1b | `cmd_find_target`'s type switch drops a bare name into the `pane` slot for `CMD_FIND_PANE`-declared commands, and the `=`-stripping block only handles the `session` and `window` slots. | `cmd-find.c` |
| 1c | `recalculate_size_skip_client`'s `current` branch gates *both* loops of `clients_calculate_size`, including the one that would apply a stored per-window cap. | `resize.c` |

Nothing links them. A patch to any one would not touch the others. A single
report describing all three would be three reports in a trenchcoat, and a
maintainer would have to split it before acting on any part.

### Worse: the shared symptom is not even shared

The framing survived as long as it did because the symptom was stated loosely.
Tightened against what each command actually does, the common thread dissolves:

| Claim | Did the command take effect? | Silent? |
|---|---|---|
| 1a | **Yes.** `set-hook -g pane-exited` stores the hook and it fires. | Nothing was suppressed — only `show-hooks -g`'s *enumeration* omits it, and asking by name returns it. |
| 1c | **Yes, deferred.** The cap is stored per client and applies the moment the window is selected. | rc=0 with no diagnostic, but nothing was lost. |
| 1b | **No.** The target genuinely fails to resolve and the option is never read or written. | **Only under `-q`** — the flag whose documented job is to suppress the error. Without `-q` it is rc=1 with a clear (if misleading) message. |

So the premise — "cannot take effect, returns success" — is true of exactly one
of the three, and there only when the caller explicitly asked for silence.

### The case *for* a family, stated fairly

There is one honest thread, and it is thinner than the original hypothesis: in
all three cases **a caller cannot distinguish "done" from "not done" by return
code alone**, and in all three the introspection tmux offers is keyed to a
different scope or slot than the mutation was. That is a real family — but it
is a family of *observability* wrinkles, not of defects, and two of its three
members are working as designed. It is not something to report; at most it is
something to know when writing scripts against tmux.

### Verdict on the framing

Three unrelated things that look alike from the outside. Report nothing on the
strength of the family; each claim stands or falls on its own, and their
individual verdicts are above.

---

## Prior art reconciled

### 1. `docs/superpowers/plans/2026-08-10-reap-pane-state.md` — REVISED

Wrong sentence, `2026-08-10-reap-pane-state.md:10-13`:

> On the pinned tmux, `pane-exited` is a silent no-op: `show-hooks -g` after
> sourcing the generated config never lists it — tmux accepts the `set-hook`
> call (no error, no warning) but never actually stores or fires it.

The first half survives unchanged: `show-hooks -g` (bare) genuinely never
lists `pane-exited` — confirmed at the pin (claim 1a, "Row-by-row diff" and
"Mechanism"). The second half — "never actually stores or fires it" — is
disproved by claim 1a's "The hook fires" (all three scopes YES) and its
resolver table (storage: `set-hook -g pane-exited` writes `global_w_options`,
`show-hooks -g pane-exited` and `show-hooks -g -w` both read it back).

**Correction:** `pane-exited` is a window/pane-scoped hook
(`OPTIONS_TABLE_PANE_HOOK`, `options-table.c:2005`). `set-hook` resolves scope
by the option's own declared scope (`options_scope_from_name`,
`options.c:1014`), so `-g pane-exited` lands in `global_w_options` and fires
normally. `show-hooks -g` with no name resolves scope by flags alone
(`options_scope_from_flags`, `options.c:1085`) and only surveys
`global_s_options` — the *session* table — so it structurally cannot see a
window-scoped hook regardless of whether one is registered. The "silent
no-op" reading was an artifact of asking the wrong table, not a broken hook.
Claim 1a's "Consequence for the repo" already logs this as a follow-up
against this exact plan doc, not yet applied to the plan doc itself.

One clause in the same plan doc is untouched by this investigation and should
not be read as confirmed either way: "There is no `pane-died` fallback
either; the hook name that would need it also doesn't fire"
(`2026-08-10-reap-pane-state.md:13`). `docs/upstream-tmux.md`'s row-by-row
table shows `pane-died` has the *identical* storage
signature to `pane-exited` (`-g-w:1 named:1`) — same pane-scope declaration,
same resolver — so by the same mechanism it should also fire. But the
explicit fire-test was only run for `pane-exited`; `pane-died`
was never independently fired. Flag as **needs more work**, not revised —
the mechanism argument is strong but nothing directly measured it firing.

### 2. `tests/tmux-next38-readiness.bats` — CONFIRMED (boundary verified)

`tests/tmux-next38-readiness.bats:381-405`, the "every hook the config
registers with set-hook -g is actually stored" test:

- It already unions **both** tables (`run t show-hooks -g` at line 385, then
  `run t show-hooks -gw` at line 392, concatenated into `stored` at line 394)
  — so the test
  itself is not blind to window-scoped hooks in general.
- The hook-name list it checks comes from `grep`ping `set-hook -g` lines out
  of the **generated config file itself** (`conf="$(store_conf)"` at line 397,
  `grep -oE 'set-hook -g ([A-Za-z-]+...)'` at line 398) — not from a fixed
  or historical list.

Since `pane-exited` was removed from `config/tmux.conf.nix` by artifact (1)
(replaced with the comment at `config/tmux.conf.nix:1135-1137`), the grep
never extracts `pane-exited` as a name to check, so the test is vacuously
silent on it — exactly as claimed. The boundary is real, but it is narrower
than "only checks the session table": the test would in fact catch a
window-scoped silent-no-op *if the config still registered the hook name*.
It just has nothing to check now that the name is gone.

### 3. `modules/home-manager.nix` — REVISED, and the citation itself moved

The literal `set-hook -g pane-exited[99] ...` line the reap-pane-state
follow-up cited at `modules/home-manager.nix:107` is no longer there as
static text. `modules/home-manager.nix:98-113` now renders the tmux-remux
hook wiring **dynamically** at config-load time via `tmux-remux triggers`
(a `pkgs.writeShellScript "tmux-remux-wire"` block), then reindexes every
`set-hook -g <name>` line the tool emits onto `[99]` with a sed pass
(`modules/home-manager.nix:106`):

```
| sed -E 's/^(set-hook -g [A-Za-z][A-Za-z-]*)( )/\1[99]\2/' \
```

tmux-remux's own trigger source (`~/git/noamsto/tmux-remux`,
`internal/triggers/triggers.go:98`) still emits `set-hook -g pane-exited
'run-shell -b "@BIN@ capture-event pane-died ..."'` — so the hook is still
live at runtime, just generated rather than hand-written. It is **registered
with `-g`** (routes to window scope per the mechanism above, same as
lazytmux's own removed hook), and index `[99]` is cosmetic (collision
avoidance with lazytmux's own `[20]`/`[98]` indices on other hooks) — it
plays no role in whether the hook stores or fires.

Given `pane-exited` demonstrably fires at window scope (established fact,
this investigation), tmux-remux's hook is **not a phantom** — and tmux-remux's
own source comments agree: `triggers.go:82-84` states "measured on 3.7b and
next-3.8 alike" that `pane-exited`'s payload behaves as expected, i.e.
tmux-remux's authors already treat it as a working hook, not a no-op. The
"unfixed phantom" framing in the reap-pane-state follow-up
(`2026-08-10-reap-pane-state.md:69-73`) is **REVISED**: there was never a
phantom here to fix, only a citation that predates the move from a static
hook line to codegen'd wiring.

### 4. `docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md` — citations partially drifted, mechanism intact

Every `file:line` cite in the doc, re-resolved against `$SRC` (pin
`40381bdc`, vs. the doc's `d5afb67`):

| Citation | Doc claims is there | Actually at that line in `$SRC` | Resolves? |
|---|---|---|---|
| `server-client.c:2947` | `server_client_dispatch_identify`'s `if (c->flags & CLIENT_CONTROL) control_start(c); else if (c->fd != -1) { tty_init(&c->tty, c); ... }` | `MSG_IDENTIFY_STDIN` case body (`c->fd = imsg_get_fd(imsg);`) — unrelated code | **No** — drifted ~48 lines; the quoted branch is actually at `server-client.c:2995-2997` |
| `cmd-display-menu.c:581` | `int modify = popup_present(tc);` | Exact match | **Yes**, byte-for-byte |
| `popup.c:550` | `if (lines != BOX_LINES_DEFAULT)` branch "ends with" `tty_resize(&c->tty)` | Branch opens at line 541; the `tty_resize(&c->tty)` call itself is at line 551 | **Close but off** — no line 550 statement matches either half of the description; nearest is 551 (off-by-1) |
| `tty.c:124` | "opens with `struct client *c = tty->client;`" | Line 124 is the `tty_resize(struct tty *tty)` function signature; `struct client *c = tty->client;` is line 126 | **Off by 2** — same function, wrong line |

Verdict: **mechanism CONFIRMED, citations mostly REVISED.** The chain the doc
describes — control-mode client has no tty (`server-client.c` ~2995-2997) →
second popup takes the `popup_modify` path (`cmd-display-menu.c:581`) →
`-B`/`-b` triggers `tty_resize(&c->tty)` (`popup.c:551`) → NULL deref inside
`tty_resize` (`tty.c:124-126`) — still holds verbatim in content at the pin.
3 of 4 line numbers have shifted (by 1, 2, and ~48 lines respectively)
between `d5afb67` and `40381bdc`; only `cmd-display-menu.c:581` is exact.
Not attempted: the repro itself (out of scope per instructions).

### 5. `docs/superpowers/plans/2026-09-02-bridge-aggressive-resize-478.md` — mechanism CONFIRMED, "discarded" language REVISED

Mechanism (skip_client / current branch, both loops) is established as
correct — not re-litigated here.

The plan doc does use "discard" language that the investigation's own
verdict (claim 1c: *"the contract's description of the outcome ('discarded')
is wrong — the cap is stored and deferred"*)
contradicts:

- `2026-09-02-bridge-aggressive-resize-478.md:8-10`: "the bridge's single
  control client is 'on' one window, so `refresh-client -C @N:WxH` is
  **silently discarded** for every other mirrored window, and
  `converger.need` records it as asserted anyway."
- `2026-09-02-bridge-aggressive-resize-478.md:94`: "`reg.add` precedes
  `setupWindow`, so `watchResize` can cap a window before its opt-out lands —
  **tmux discards that cap**, the converger records it, and setup's cap is
  then skipped..."

Both should read "defers" / "the cap is stored and fires on selection," not
"discards." This is not just wording: per claim 1c's "Does the cap survive?", a
cap that was silently "swallowed" while unselected **does not need a fresh
`refresh-client -C` resend** to take effect — it fires on the mere
`select-window` to that window, using the size already sent earlier. The
spec doc's framing ("stays stuck until a different client size arrives
while that window happens to be current," see item 6 below) describes a
narrower recovery path than what the pin actually does.

### 6. `docs/superpowers/specs/2026-09-02-bridge-window-size-latest-478-design.md` — same "discard" issue, more consequential here

Same area, same root cause, and the same word appears at the load-bearing
spot in this doc — the root-cause paragraph itself, not a peripheral note:

- `2026-09-02-bridge-window-size-latest-478-design.md:45-49`: "`converger.need`
  then records the size as asserted at the moment it returns true, before
  anything is known about the outcome. tmux **accepted and discarded** the
  command, so the record is wrong and the window is never re-sent that size.
  It stays stuck until a different client size arrives while that window
  happens to be the remote's current one."
- `2026-09-02-bridge-window-size-latest-478-design.md:130-132`: "The issue
  proposes threading `stream.send`'s discarded `bool` into `watchResize`...
  the write **succeeds** and tmux accepts the command, then **discards** it
  during..."

Verdict: **REVISED.** Per claim 1c ("Does the cap survive?" — yes, `select-window` alone, no resend, applies the earlier
120x30/100x20 cap), the sentence "the window is never re-sent that size...
stays stuck until a different client size arrives" is the part that doesn't
hold: the *original*, already-sent cap remains pending and fires on plain
selection — no new size command required. The practical consequence for
this repo's bug is the same (a straggler window shows a stale size until
selected), but the *mechanism* of recovery differs from what the doc
describes, which matters for anyone reasoning about whether the fix
(`aggressive-resize` opt-out) is even necessary versus a lighter no-op fix
that just forces a `select-window`/reselect on the remote.

---

## Upstream reports referenced in-tree

### Distinct upstream numbers found

`grep -rnE 'tmux/tmux#[0-9]+|github\.com/tmux/tmux/(issues|pull)/[0-9]+' .
--exclude-dir=.git --exclude-dir=result` plus the five numbers named in the
`#488` design spec. `WORKER_TASK.md` hits are excluded below — it's an
untracked scratch file (`git status`), not repo content.

#### #5551 — `display-popup on a control-mode client crashes the server (popup_modify -> tty_resize NULL deref)`

- **In-repo refs:** `scripts/tmux-splash-maybe.sh:17`,
  `scripts/tmux-session-picker.sh:24`, `scripts/tmux-window-picker.sh:31`,
  `scripts/tmux-window-wall.sh:17`, `scripts/tmux-scratchpad.sh:51` — all
  "reported upstream as tmux/tmux#5551 — drop the pin once that ships."
  Also `docs/superpowers/specs/2026-09-03-upstream-tmux-investigation-design.md:123,160,163,224`.
- **What repo says it's about:** the #346 SEGV (control-mode client, second
  popup with `-B`/`-b`).
- **Upstream state:** **CLOSED, `stateReason: COMPLETED`**, filed
  2026-08-29T20:32:04Z, closed 2026-08-31T07:47:04Z. **Filed by `noamsto`** —
  this is the user's own report, not a third party's, and its body is
  essentially the popup-control-mode-guard design doc's analysis (same
  `cmd-display-menu.c:581`, `popup.c:551` cites, same backtrace shape).
  Closing comment from maintainer `nicm`: *"Applied to OpenBSD now, will be
  in GitHub later. Thanks!"* — i.e. fixed first in tmux's canonical OpenBSD
  source. It **has since been mirrored to GitHub**: the fix is
  `af3e4d2e5b63` *"Do not allow popups for control clients, from Noam
  Stolero."* (2026-08-31T07:46:55Z), which reached `master` via merge
  `b28244e2fe44` (2026-08-31T14:56:14Z). Both resolve against
  `repos/tmux/tmux/commits`; a `6a64188b` SHA quoted in passing during this
  investigation does not, and was discarded.
- **Fix present in our pin (`40381bdc`)?** **No.** Pin commit date
  2026-08-28T10:32:01Z predates both the issue (2026-08-29) and the fix
  (announced fixed 2026-08-31, and not yet GitHub-mirrored even then).
- **Matches claim 2 (display-popup SEGV):** yes, exactly — this is that
  claim's own upstream report, filed by the same investigator line. Its
  existence and status were already known per the design spec
  (`2026-09-03-upstream-tmux-investigation-design.md:123`); this pass adds
  the fix-not-in-pin confirmation and the "not yet on GitHub" detail.

#### #5135 — `Floating panes discussion`

- **In-repo refs:** `scripts/tmux-float-refit.sh:8`.
- **What repo says it's about:** upstream calls float-resize-hint behaviour
  "undecided" as part of a wider floating-pane rework; cited as why
  `@float_geom` reassertion is a workaround rather than a fix (issue #371).
- **Upstream state:** **OPEN**, created 2026-05-31T10:48:02Z. A discussion
  issue, not a bug report with a fix commit — no closing state to check.
- **Fix present in pin?** N/A — nothing to have landed; still open/undecided.
- **Matches any of this investigation's four claims?** No. Not 1a/1b/1c/2 —
  it's #371's citation, a fifth, separate topic this investigation didn't
  re-examine.

#### #5336 — `display-popup repaints/flickers when a background pane redraws its full region (scroll / alternate-screen) — 3.7`

- **In-repo refs:** `flake.nix:19`, `modules/home-manager.nix:253`.
- **What repo says it's about:** the reason `tmuxPackage` defaults to a
  pinned 3.6a nixpkgs rather than tracking unstable — "Override with
  `pkgs.tmux` to track unstable once tmux/tmux#5336 is resolved."
- **Upstream state:** **CLOSED, `stateReason: COMPLETED`**, 2026-07-17T15:48:52Z.
  Closed by `nicm`'s "I think this is fixed now?", fixed via #5398 (below).
- **Fix present in pin?** **Yes** — confirmed by grepping `$SRC/screen-redraw.c`
  for the fix's own marker (`CLIENT_REDRAWOVERLAY`, present at lines 1873
  and 1882).
- **Matches any of this investigation's four claims?** No — unrelated to
  1a/1b/1c/2. Flagged because the repo's own "once resolved" condition
  (`modules/home-manager.nix:253`) is now met and the fix is confirmed
  present at the investigation's pin, but `tmuxPackage`'s default is a
  separate, older pinned nixpkgs tmux (3.6a), not the `tmux-upstream` flake
  input this investigation's evidence binary was built from — whether that
  default should actually move is outside this task's scope.

#### #5398 — `Do not repaint an open overlay on background redraws or option changes (#5336)` (PR)

- **In-repo refs:** `flake.nix:20`.
- **What repo says it's about:** "the overlay redraw fix landed as
  tmux/tmux#5398, the other was rejected upstream" (of two changes in one
  PR — see below).
- **Upstream state:** **CLOSED**, opened 2026-07-15T19:42:53Z, closed
  2026-07-16T10:52:37Z ("Applied to OpenBSD now, will be in GitHub later" —
  same `nicm` phrasing as #5551, this time long since synced). **Authored by
  `noamsto`** — the user's own PR. One correction to the repo's own comment:
  flake.nix says the dropped half "was rejected upstream," but the PR
  thread (`nicm`: "I don't know that we need to bother with it" for the
  options.c/server-fn.c option-change path; `noamsto`: "Agreed on dropping
  (1)... I've removed it and force-pushed") shows the user withdrew it
  themselves after `nicm`'s skepticism, not a hard upstream rejection of a
  standing PR. Minor wording point, not load-bearing.
- **Fix present in pin?** **Yes** — `$SRC/screen-redraw.c:1873,1882` carries
  `CLIENT_REDRAWOVERLAY`, the PR's own marker.
- **Matches any of this investigation's four claims?** No.

#### #4920 — `Fix issue where popup window gets overwritten by background updates.` (PR)

- **In-repo refs:** `config/tmux.conf.nix:6`, `modules/home-manager.nix:251`.
- **What repo says it's about:** why `tmuxPkg`/`tmuxPackage` defaults to a
  pinned tmux 3.6a instead of nixpkgs' stock tmux — "tmux 3.7 no longer
  freezes background panes under a popup," causing flicker.
- **Upstream state:** **CLOSED**, opened 2026-03-13T17:08:03Z, closed
  2026-03-23T08:48:39Z, same "Applied to OpenBSD now, will be in GitHub
  later" close. Authored by `taylorconor` (third party, not the user).
- **Fix present in pin?** **Yes** — `$SRC/tty-draw.c:55` carries
  `tty->client->overlay_check == NULL`, the PR's own marker
  (`tty-draw.c`/`tty.c` diff).
- **Matches any of this investigation's four claims?** No.

### Matching this investigation's four claims against the list above

| Claim | Existing upstream report in the list above? |
|---|---|
| 1a — `set-hook` scoping / #341 | **None.** No `tmux/tmux#N` reference anywhere in-tree ties to #341 or hook-scoping. Consistent with the doc's own verdict (`do not report` — not a defect, so nothing to have filed). |
| 1b — `show-options -t '=name'` / #474, #476 | **None in-tree** — no `tmux/tmux#N` reference in this repo ties to #474/#476. But claim 1b's own investigation (above) found one upstream that this grep could not: [tmux/tmux#4594](https://github.com/tmux/tmux/issues/4594), closed without a fix. The absence of an in-tree reference simply means nobody here had looked upstream before. |
| 1c — `refresh-client -C` under `aggressive-resize` / #478, #481 | **None.** Neither the plan (`2026-09-02-bridge-aggressive-resize-478.md`) nor the design spec (`2026-09-02-bridge-window-size-latest-478-design.md`) names any `tmux/tmux#N`, and claim 1c cites tmux source and `tmux.1` only. The upstream search claim 1c ran afterwards (see "Resolving the open question") also found nothing — so there is genuinely no upstream report for this behaviour, filed or referenced. |
| 2 — display-popup SEGV / #346 | **Yes — #5551**, and it is the user's own report (see above). Already the documented match per the design spec; this pass adds: fix not in pin, not yet GitHub-mirrored as of its close comment. |

---

## Follow-ups for this repo — recorded, not fixed here

This PR is docs-only by design (spec non-goal: *"No behaviour change to
lazytmux… where the investigation shows an in-repo document or comment is now
wrong, record it as a follow-up; do not edit it here"*). Each item below is
evidence-backed above and left for its own issue.

### 1. The #341 root-cause note is wrong in **three** places, not one

`set-hook -g pane-exited` stores the hook and fires it (claim 1a). Three
in-repo artifacts state the opposite:

| Artifact | The wrong claim |
|---|---|
| `docs/superpowers/plans/2026-08-10-reap-pane-state.md:10-13` | *"tmux accepts the `set-hook` call (no error, no warning) but never actually stores or fires it"* |
| `config/tmux.conf.nix:1135-1137` | *"The `pane-exited` hook is a silent no-op on the pinned tmux (confirmed via `show-hooks -g`)"* — and the parenthetical names the very method that produced the false negative |
| `tests/tmux-next38-readiness.bats:380-381` | *"a `set-hook -g` that tmux silently discards, e.g. `pane-exited` on the pinned tmux"* — a wrong worked example in an otherwise sound test |

The test itself is fine: it already unions `show-hooks -g` **and**
`show-hooks -gw` (`tests/tmux-next38-readiness.bats:383-390`), so it would
catch a genuine window-scoped no-op. It is only its comment's example that is
wrong, plus the fact that it has nothing to check now that `pane-exited` was
removed from the generated config.

**The reopened question:** lazytmux dropped `pane-exited` and fell back to
`tmux-update-icons`' every-5th-tick sweep. Since the hook demonstrably fires,
whether to register it again — and retire the sweep's pane-reaping role —
should be reconsidered on its own merits.

### 2. `modules/home-manager.nix:107`'s "phantom" is not a phantom

The static `pane-exited[99]` line that citation pointed at no longer exists;
the tmux-remux hook wiring is generated at config-load time and reindexed onto
`[99]` by a sed pass (`modules/home-manager.nix:98-113`). tmux-remux still
emits a `pane-exited` hook, it is registered with `-g`, it routes to window
scope, and it fires. The `[99]` index is collision-avoidance only.

### 3. There **is** a working exact-match read for session options (#474, #476)

The repo's current guidance (`CLAUDE.md`) is to read session user-options with
a **bare** `-t "$name"`, which prefix-matches. A trailing `:` or `.` restores
exact-match semantics on the very commands that reject a bare `=name`, because
the separator makes `cmd_find_target` split the string and populate the
`session` slot, where the `=`-strip applies. Verified at the pin:

```console
$ tmux -L s show-options -t '=abc:'   @x     # session is "abcdef"
rc=1  no such session: =abc:                  <- exact match enforced, correctly fails
$ tmux -L s show-options -t 'abc:'    @x
rc=0  @x 42                                   <- prefix match, as expected
$ tmux -L s show-options -t '=abcdef:' @x
rc=0  @x 42                                   <- exact match, succeeds
$ tmux -L s set-option  -t '=abcdef:' @y 7    <- write side works too
rc=0
```

So `-t "=$name:"` is strictly better than the bare `-t "$name"` the repo
recommends: it is exact where the bare form is a prefix match. Worth a
follow-up to adopt it and to correct the CLAUDE.md paragraph, which currently
presents "bare name, or `display-message -p` / `list-sessions -F`" as the only
options.

### 4. The #346 crash is **live at the pinned rev** — bump the pin

Upstream fixed it in `af3e4d2e` (2026-08-31); our `tmux-upstream` pin
`40381bdc` is 2026-08-28 and does not carry the guard. The crash reproduces
3/3 at the pin. lazytmux is protected only by its own mitigations, and one of
them (`#{?client_name,--client …,}`) is fail-*open*.

Bumping `tmux-upstream` past `af3e4d2e` closes it in tmux itself and makes the
four in-tree *"drop the pin once that ships"* comments
(`scripts/tmux-session-picker.sh:24`, `tmux-window-picker.sh:31`,
`tmux-window-wall.sh:17`, `tmux-scratchpad.sh:51`) actionable.

### 5. "Discarded" is the wrong word in two #478 documents

The cap is stored and deferred, and fires on plain `select-window` with no
resend. Two documents say it is discarded:

- `docs/superpowers/plans/2026-09-02-bridge-aggressive-resize-478.md:8-10, :94`
- `docs/superpowers/specs/2026-09-02-bridge-window-size-latest-478-design.md:45-49, :130-132`

The second is the more consequential: its root-cause paragraph says the window
*"stays stuck until a different client size arrives while that window happens
to be the remote's current one"*. In fact the original cap fires on mere
selection. That difference matters to anyone weighing the shipped
`aggressive-resize` opt-out against a lighter fix.

### 6. `#5336`'s "once resolved" condition is now met

`modules/home-manager.nix:253` pins `tmuxPackage` to a 3.6a nixpkgs *"until
tmux/tmux#5336 is resolved"*. It is resolved (closed 2026-07-17, fixed via
`#5398`), and the fix is present at this investigation's pin
(`CLIENT_REDRAWOVERLAY` at `screen-redraw.c:1873,1882`). Whether that default
should actually move is a separate call — the pinned 3.6a is a *different*
input from `tmux-upstream` — but the stated condition no longer blocks it.

### 7. Not measured: does `pane-died` fire?

`pane-died` has the identical storage signature to `pane-exited` at the pin
(`-g-w:1 named:1`, same `OPTIONS_TABLE_PANE_HOOK` declaration, same resolver),
so by mechanism it should fire too. **The fire test was only ever run for
`pane-exited`.** Recorded as unmeasured rather than inferred.

---

## Draft upstream report — claim 1b only, and I recommend **not** sending it

The spec permits a draft for a claim already filed upstream *"unless the
upstream issue was closed without a fix"* — which is exactly [tmux/tmux#4594](https://github.com/tmux/tmux/issues/4594)'s
situation, so the carve-out applies and a draft belongs here. It is included so
the decision is the maintainer's rather than mine, not because I think it should
go.

**My recommendation: do not send.** `nicm` closed #4594 having already
diagnosed the exact mechanism, and his last substantive comment was *"It should
only be `set-option` I think probably, but does it really matter that much?"* —
a deliberate deprioritisation, not an oversight. A second issue over a closed
one with the same root cause reads as pressure, not information. The new
material below (that `show-options` is affected identically, and that `-q`
collapses the two outcomes) would be better as a **comment on #4594** if
anything, and best of all as nothing, given the `=name:` workaround exists.

Draft follows. Runs on a stock tmux build — no nix, no lazytmux.

---

**Title:** `show-options -t '=name'` fails where `has-session`/`kill-session` accept it; `-q` then hides the failure

Filed as a narrower companion to #4594, which covers `set-option`. Same root
cause; this is about the read side and about `-q`.

Reproduction (tmux `next-3.8`, rev `40381bdc`, Linux x86_64):

```sh
tmux -L demo -f /dev/null new-session -d -s zz
tmux -L demo set-option -t zz @x 1

tmux -L demo has-session   -t '=zz' ; echo "has-session   rc=$?"   # rc=0
tmux -L demo show-options  -t '=zz' @x ; echo "show-options rc=$?" # rc=1, "no such session: =zz"
tmux -L demo show-options -q -t '=zz' @x ; echo "quiet rc=$?"      # rc=0, no output
tmux -L demo show-options -q -t 'zz' @nope ; echo "unset rc=$?"    # rc=0, no output
```

Two observations:

1. `has-session` and `kill-session` accept `-t '=name'`; `show-options` does
   not, and reports `no such session` for what it declares as a pane target.
2. The last two commands are byte-identical in return code, stdout and stderr —
   one is "the target could not be resolved", the other is "the option is
   genuinely unset". A script using `-q` cannot tell them apart.

Mechanism (unchanged from #4594's diagnosis, confirmed at this rev):
`show-options` declares `.target = { 't', CMD_FIND_PANE, CMD_FIND_CANFAIL }`
(`cmd-show-options.c:57`). In `cmd_find_target`, a bare name with no `:`/`.`
and no sigil is routed by the declared type (`cmd-find.c:1101-1111`), so it
lands in `pane`. The `=`-stripping block immediately after
(`cmd-find.c:1116-1122`) handles only the `session` and `window` slots, so the
`=` is never removed and `CMD_FIND_EXACT_SESSION` is never set. The eventual
fallback to `cmd_find_get_session` then looks up the literal string `=zz`.

Workaround, for anyone who finds this: append `:` or `.` — `-t '=name:'` works
and does enforce exact match.

Whether this is worth changing is entirely yours; #4594 suggests it may not be.
