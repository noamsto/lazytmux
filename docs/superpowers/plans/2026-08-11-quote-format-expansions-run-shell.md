# Quote every format expansion reaching `run-shell` / `if-shell` (#355)

## Problem

`run-shell` format-expands its whole argument string *before* handing it to
`sh -c`. Any bare `#{session_name}` / `#{hook_session_name}` inside a
`run-shell` command is therefore command injection — the expanded text becomes
shell source, and a `;` payload needs no quote at all.

This crosses a real trust boundary. `scripts/lztmux-remote-open.sh` creates a
local session named from a **remote host's** session list, so a hostile host you
bridge to with `prefix + s` → Remote controls a string that lands in a local
session name, which the hooks then execute.

## Measured facts (tmux next-3.8)

Everything below was probed against the real binary, not reasoned about — the
Nix string layer *and* the tmux quoting layer both make eyeballing unreliable.

| Question | Answer |
|---|---|
| What does `#{q:X}` escape? | backslash-escapes `; space " ' $ ` + backtick + `\| & \ * ?`. **Not** `~`. |
| Is bare `#{session_name}` exploitable? | Yes — measured. A session named `devbox-zz; touch /tmp/pwned` ran `touch` and the script's `$1` never arrived. |
| Does `#{q:}` stop it? | Yes — same payload arrives as one intact argument, no marker file written. |
| Can `#{q:}` sit inside shell quotes? | **No.** `"=scratch-#{q:X}"` leaves the literal backslashes in the value. The expansion must land **bare**. |
| Does `#{q:#{X}}` quote? | **No** — nesting defeats it. `#{q:` must take a plain format name. |
| Bare `#{q:X}` when X is empty? | Produces **zero** shell words, not one empty word. |
| Does the `=` exact-match prefix survive? | Yes — `-t =scratch-#{q:hook_session_name}` yields exactly one correct word. |
| Are `;`, `:`, `.`, space, `"`, `'` legal in a session name? | All accepted by tmux. |
| Does `#{q:}` escape `#`, `(`, `)`, `<`, `>`? | Yes — so a bare `#{q:X}` cannot start a shell comment even when the value begins with `#` (measured: `--thm-bg \#1e1e2e` arrives as one intact argument). |

## `#(...)` is the same hole, and it was live

`#(cmd)` in a status format also runs through a shell and is format-expanded
first. Two of them carried `'#{session_name}'` in **shell single quotes**, which
a `'` in the name breaks out of. Measured, against the real wrapper:

```
session named:  x'; touch /tmp/pwned; echo '
old conf:       RCE          new conf: safe
```

This sits outside the issue's literal `run-shell`/`if-shell` site list, but it is
the same mechanism carrying the same remote-controlled value on the **1s status
path**, so leaving it would have shipped a fix that did not actually close the
trust boundary. Both `#{session_name}` occurrences are now `#{q:session_name}`.

The other expansions in those `#(...)` bodies (`@thm_*`, `@icon_*`,
`@resume_claude`, `start_time`) are deliberately left in their shell single
quotes: they are generated locally, and unquoting them would turn an empty value
into zero shell words and shift the following flag or positional — a real
regression on the hot path for no security gain.

> A caution for whoever reads this next: a first attempt at the end-to-end test
> reported the *fixed* conf as still exploitable. That was the test harness
> interpolating the hostile session name into its own `script -qfc "... -t
> '$EVIL'"` wrapper. Attach without `-t` (one session per probe server) so the
> name never enters the harness's own shell string.

## Fix

Quote every format expansion that reaches a shell command string — the command
argument of `run-shell`, and the first argument of `if-shell` when `-F` is
absent — with `#{q:...}`, and drop the surrounding shell quotes so the escaped
value lands bare.

Three sites could not simply drop their quotes, because an empty expansion would
change the shell word count:

- `bridgeCtl`'s two flag values switch to the attached form
  (`--display-error=#{q:client_name} --sock=#{q:@bridge_sock}`), which is always
  exactly one word. Go's stdlib `flag` accepts `--name=value`.
- the Catppuccin guard becomes `[ x#{q:@catppuccin_flavor} = x ]` — the `x`
  prefix keeps it one word and preserves the emptiness test exactly.

Deliberately **not** touched, because they are not shell:
`if-shell -F '<format>'` arguments, `command-prompt -I` seeds,
`confirm-before -p` prompt text, and `-c "<dir>"` args of
`split-window` / `new-window`.

## Guard

The recurrence is the real defect — this class has now been introduced twice.
`tests/conf-shell-quoting.bats` walks the **emitted** conf (tokenizing tmux
quoting and recursing into `{ }` blocks), locates every genuine shell-command
argument, and fails on any `#{...}` that is not a `#{q:NAME}`. It carries
synthetic fixtures for each way the rule can be broken — bare, shell-double-
quoted, `if-shell` without `-F`, nested inside a brace block, and the
`#{q:#{X}}` non-fix — plus fixtures for each non-shell context that must *not*
be flagged.

A second, narrower rule covers `#(...)`, where the blanket rule cannot apply
(see above): none of the identity formats an untrusted remote can steer —
`session_name`, `hook_session_name`, `window_name`, `pane_title` — may appear
there unquoted. The two rules cover each other's blind spots: one is structural
and value-agnostic, the other is value-specific and context-agnostic.

Both real-conf assertions **fail hard** rather than `skip` when `CONF` is
unbound. A skip reports `ok`, so a broken binding would retire the guard
silently — the one outcome it exists to prevent.

Red/green evidence: against the pre-fix conf the structural rule reports 91
violations and both real-conf tests fail; against the fixed conf all six tests
pass.

### It caught a live one during the rebase

PR #352 (issue #346) landed while this branch was in review. It fixed the
`session-closed`/splash sites in this same class — that resolution was taken
verbatim, `#{q:hook_session_name} #{q:hook_client}` being strictly better than
the `#{q:hook_session}` drafted here — but it also added six new
`--client "#{client_name}"` arguments to `run-shell` bindings. Shell double
quotes, which is exactly the non-protection this issue is about.

Rebasing turned the guard red on five of them within one build, before a human
looked at the diff. That is the recurrence this test was written for, happening
on the very first opportunity — so the guard is not hypothetical insurance.

Quoting them exposed a second problem that review caught: a bare `#{q:}` on an
empty value is zero shell words, so an absent client would have slid the next
argument into `--client`'s slot. For `bind a` that means
`tmux-window-picker --client --agent` — the parser takes `--agent` as the client
name, `shift 2` eats both, and agent mode silently disappears. The obvious fix,
`--client=#{q:client_name}` to match `bridgeCtl`, is **wrong here**: those four
scripts parse `$1 == --client` with the value in `$2` and have no `=` form,
unlike `bridgeCtl`'s Go `flag` consumer.

The fix is to emit the flag and its value together or not at all:

```
#{?client_name,--client #{q:client_name},}
```

Verified that a `#{?…}` conditional still applies an inner `#{q:}`, and against
a live server: no client → `picker --agent`; client attached →
`picker --client /dev/pts/44 --agent`. This removes the dependency on an
unproven "a key binding always has a client" invariant rather than resting on
it.

### The guard's one blind spot

tmux substitutes config macros (`name=value`, used as `$name`) at parse time, so
a macro body can itself be an `if-shell` shell argument — but the scanner reads
the emitted text, where the use site says only `$is_vim`. Confirmed with
`list-keys`: the stored bind carries the macro's *expanded* text.

The single macro here (`is_vim`, the vim-aware `C-h/j/k/l` gate) held a bare
`#{pane_tty}` and is now quoted like every other shell-reaching site. It is
commented in place, because the guard cannot enforce that one.

## The second-order hole: `tmux-scratchpad`

Quoting the conf delivers hostile names *intact* to the consuming scripts, so
the next question is what they do with them. Auditing that found a live RCE one
hop past the config, on the same trust boundary:

`display-popup -E` hands its argument to a shell, and `tmux-scratchpad` built
that argument by interpolating the session name into shell single quotes
(`"'${SELF}' --attach '${SESSION}'"`). A `'` in the name closes the quoting:

```
session named:  x'; touch /tmp/qp355/SCRATCHPWN; echo '
result:         touch ran; the script received only "x"
```

Pre-existing — the old conf passed a `'` through just as well — but reachable
with `prefix + S` on a bridged session the remote named, which is exactly what
this issue is about. The popup command is now built with `printf %q`, and the
inner `--attach` path no longer pipes a hand-built `set -t '$SCRATCH' …` string
through `tmux source -` (a `'` there injected *tmux* commands, which reach a
shell again via `run-shell`) — it calls `tmux set` with direct argv instead.

`tests/picker-launcher.bats` gains a case for it, red-verified against the old
interpolation. Its sibling assertion was also loosened from `--attach 'sess'` to
"the session reaches `--attach`": the exact quoting is `printf %q`'s business
and a benign name needs none, so the old assertion was testing the mechanism
rather than the behaviour.

## Out of scope (observed, not fixed)

- `tmux-window-nav` resolves a session name containing `:` as a
  `session:window` target (`can't find session: has`). The script quotes
  `"$session"` correctly and handles `;`, spaces and `"` fine once the name
  arrives intact — this is tmux target grammar, pre-existing, and not an
  injection.
- The bridge rename bind interpolates `command-prompt`'s `%%` into a shell
  string. That is the local user's own keystrokes, not a remote-controlled
  value, so it is not the trust-boundary class this change closes.
