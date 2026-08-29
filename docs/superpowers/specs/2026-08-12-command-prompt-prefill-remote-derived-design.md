# Design spec — `command-prompt` `%%` prefill is remote-derived (#367)

Closes #367. Split out of #355 as explicitly-accepted residual risk.

Revision 4. Revision history, because four premises were falsified in review and
each correction shaped the design. Note this exceeded the 2-revision critic cap;
see `## Escalated` in the PR body.

- **r1 → r2**: r1 claimed `rename-window` does not format-expand its argument.
  Wrong (`cmd-rename-window.c:53`). The whole `#`-fidelity section was rebuilt.
- **r2 → r3**: r2 claimed the `#` escape and `rename-window`'s expansion are
  inverses, so decode-then-re-encode is a fixed point. Wrong for a `#`-run
  followed by `[` (`format.c:6671-6694`). Three verification designs that could
  not fail were also replaced.
- **r3 → r4**: two more, both measured:
  - r3 used `#{q:1}`, which does **not** escape `~`, `{`, `}` and does not wrap
    (`format.c:4331`). Measured: a remote window named `~/src` was delivered as
    the **local** `/home/noams/src`, and `x{a,b}` split into **two** argv words.
    Replaced with `#{qs:1}` (POSIX single-quoting), which is verbatim on every
    payload and also **removes** r3's accepted empty-result regression.
  - r3 claimed dropping an unterminated `#[` made the round trip total. Measured
    false: the `|`/control drop runs *after* the strip, so `a#|[x]b` becomes
    `a##[x]b` and an unedited Enter silently renamed the window to `ab`. Fixed by
    making the strip+drop half **idempotent** (drop first, then iterate the strip
    to a fixed point) rather than patching one input pattern.

## Problem

`config/tmux.conf.nix:780` binds `prefix + ,` inside a mirror window to

```
command-prompt -I'#{@window_bridge_name}' { run-shell "<ctl> … rename #{q:@bridge_pane} '%%'" }
```

The prompt is **pre-filled** from `@window_bridge_name`, a window name the
*remote* host chose. `%%` is substituted by tmux at prompt-completion time —
*after* format expansion — so it has no `#{q:…}` analogue, which is why #355
could not close this. Pressing Enter on an unedited prefill routes
remote-controlled text into a string that `run-shell` format-expands and then
hands to `sh -c`.

A load-bearing premise, stated so it can be checked: the `-I` seed **is**
format-expanded (`prompt.c:184`, `format_expand_time`). Both the current defense
and the fix are safe only because a format *replacement value* is never
re-scanned (`format.c:4474-4544` returns an option's value without re-expanding).

### What actually reproduces (measured)

Against the pinned tmux (`next-3.8`, rev `d5afb67`), on the **shipped** bind:

- `'`, `"`, `;`, `$`, backtick — **do not** reproduce. `cmd_template_replace`
  (`cmd.c:843-899`) treats `%%` as single-quote mode and escapes `'` → `'\''`,
  and because the bind uses a `{ … }` block (`ARGS_COMMANDS` → `cmd_list_copy`,
  `arguments.c:369-371`) the substitution lands inside an already-lexed argument
  with no re-lex. The issue's premise is stale on this pin for these characters.
  **Do not overclaim them.** The same bind written with a *string* command
  argument (`ARGS_STRING`, re-parsed via `cmd_parse_from_string`,
  `arguments.c:846`) *is* exploitable via `'` — measured. The difference is
  invisible by inspection, which is why the guards below exist.
- `#(…)` — **reproduces**. `run-shell` format-expands its argument
  (`cmd-run-shell.c:144`) and `#(cmd)` is a job (`format.c:6623`), so a remote
  window name containing `#(…)` is arbitrary local command execution on Enter.

Requirement 1 is still honoured: the four named characters are asserted as
**non-regression** cases (they fire the moment someone converts the block to a
string form), while `#(…)` is the payload that discriminates pre-fix from
post-fix.

### Why the current defense is not good enough

`#(…)` is blocked today by exactly one thing: `sanitizeWindowName`'s `#` → `##`
escape (`windows.go:159-184`). One escape, in one function, on the write path of
one option, asserted by no test. It holds only because the `##` survives
un-re-scanned into `run-shell`'s expansion and collapses *there* — a coincidence
of two unrelated mechanisms. This injection class has already recurred three
times in this repo (`tests/conf-shell-quoting.bats` header).

## Goals

1. The prompt result must never become shell syntax and must never re-enter
   format expansion — the error defined out of existence, not escaped.
2. `prefix + ,` must still rename a mirror window with the current name
   prefilled, for every name a remote can legitimately have — **verified by
   observation**, and **drift-free** (renaming twice with no edit must leave the
   remote name stable).
3. Regression guards that reject the **next** instance of this class, including
   the new shape the fix itself makes dangerous (see § NQ invariant).
4. `sanitizeWindowName`'s existing `#` / `|` / control-char guarantees stay
   intact **in both directions** — on the outbound stamp of
   `@window_bridge_name` (reflow's `|`-delimited `FMT`,
   `scripts/tmux-reflow-windows.sh:127`, and the label builder depend on them)
   **and** on the inbound rename path, where the value is interpolated into a
   control-mode command line written to the remote's tmux (`ctl.go:186`) —
   `tmuxQuote` quotes but does not neutralise a newline in a line-oriented
   stream.

## Non-goals

- Rewriting the local (non-mirror) `else` branch of the `,` bind. The reason is
  *not* that `#W` is locally derived — on a mirror window whose gate has gone
  false `#{window_name}` is the daemon-written remote name (`daemon.go:89-95`).
  It is out of scope because it reproduces next-3.8's default verbatim by design
  (`tmux.conf.nix:776`), which `tests/tmux-next38-readiness.bats:254-260` pins.
- The prefill **display** wart: `@window_bridge_name` holds the `#`-escaped form,
  so a remote window named `pr#367` prefills as `pr##367`. Pre-existing and
  unchanged. Consequence worth stating because it is the argument that keeps
  requirement 2 met: the prompt is an *escaped-dialect* field, so a **lone** `#`
  round-trips correctly (typed `feature#12` → remote `feature#12`) and only a
  typed literal `##` collapses to one `#`. Fixing the display means either
  re-expanding remote-derived data (`#{E:…}`, the #355 anti-pattern, and *worse*
  than today since `#(…)` would then fire at prefill time, before Enter) or
  trusting `#{window_name}`, which this repo deliberately does not
  (`tmux-reflow-windows.sh:143-145`, #196: `automatic-rename` clobbered it).
  Filed as a follow-up.
- Broad hardening of the daemon's remote-command construction. `ctl.go` builds
  every remote command from a fixed verb table with `tmuxQuote`; unchanged.

## Design — two candidates, weighed

### Candidate A — stricter sanitizer (extend the denylist)

Add `'`, `"`, `;`, `$`, backtick, `(`, `)`, … to `sanitizeWindowName`.

Rejected as the primary fix:

- It is **a denylist on a shell** — the shape that keeps reopening. The measured
  results are the proof: a list written from the issue's four characters would
  have closed **none** of the live vector, which is `#(…)`.
- It **mangles legitimate names**: `don't-merge`, `$HOME-scratch`. The mirror's
  label would stop matching the remote's real window name, breaking the invariant
  the daemon exists to maintain.
- It does not remove the dependency on `run-shell`'s expansion behaviour.

### Candidate B — restructured bind (chosen)

`run-shell` takes **unbounded arguments** (`cmd-run-shell.c:47`) and exposes them
to its command string as `#{1}`, `#{2}`, … (`cmd-run-shell.c:140-143`). Pass the
prompt result as a **run-shell argument** and reference it with `#{qs:1}`:

```
command-prompt -I'#{@window_bridge_name}' { run-shell "<ctl> … rename #{q:@bridge_pane} #{qs:1}" %1 }
```

- `#{qs:1}` applies `format_quote_shell_single` (`format.c:4341-4360`, selected by
  the `q` modifier's `s` argument at `:6026-6030`), which wraps the value in POSIX
  single quotes and escapes an embedded `'` as `'\''` — tmux's own shell quoting,
  the mechanism #355 standardised on, now reachable for the prompt result.
- **Not** the unadorned `#{q:1}`. `format_quote_shell` (`format.c:4324-4337`)
  backslash-escapes a fixed set that omits `~`, `{` and `}`, and it never wraps,
  so the value lands as a bare shell word. Measured against the pin, with a
  recorder capturing real argv:

  | remote name | `'%%'` (today) | `#{q:1}` | `#{qs:1}` |
  |---|---|---|---|
  | `~/src` | `~/src` | **`/home/noams/src`** — local `$HOME` leaked | `~/src` |
  | `~root` | `~root` | **`/root`** | `~root` |
  | `x{a,b}` | `x{a,b}` | **two words** (`<xa> <xb>`) → arity error | `x{a,b}` |
  | `it's` / `a b` / `pr##367` / `a#(true)` | see below | verbatim | verbatim |

  Tilde expansion is POSIX (every shell, not only the configured fish), and it
  turns a hostile remote's window name into a probe that reads the local `$HOME`
  back out. `#{qs:1}` is verbatim on all of them.
- The value enters as a format *variable's value*, never re-scanned, so `#(…)`
  inside it is inert (measured: `a#(true)` arrives verbatim, no job). This closes
  the live vector **without** relying on the sanitizer.
- The task doc flags `#{1}`/`#{2}` arg expansion as a 3.7/3.8 feature #104 tracks
  as unadopted, so it was **verified, not assumed** — measured working
  end-to-end on this pin.

Measured across thirteen payloads (`'`, `"`, `;`, `$`, backtick, `#(…)`, `##(…)`,
spaces, backslash, UTF-8, `%`, `~`, `{}`): `%1` + `#{qs:1}` is safe on **all** of
them — including the one live today — and delivers every name **verbatim**.

The sanitizer is kept (goal 4 requires it; belt-and-braces is explicitly
acceptable), but it is no longer what stands between a hostile remote and `sh -c`.

#### `#{qs:}` is a next-3.8 feature that fails SILENTLY on older tmux

Measured on `pkgs.tmux` (3.7b) vs the pin, same option value `a'b c$d ~/src x{a,b}`:

| modifier | 3.7b | pinned next-3.8 |
|---|---|---|
| `#{q:}`  | `a\'b\ c\$d\ ~/src\ x{a,b}` | same |
| `#{qs:}` | `a'b c$d ~/src x{a,b}` — **raw, unquoted** | `'a'\''b c$d ~/src x{a,b}'` |
| `#{zz:}` (unknown) | empty | empty |

So on 3.7b `#{qs:}` provides **zero** quoting, and it is *not* detectable as a
failure — an unknown modifier returns empty, but `qs` returns the value. This
repo ships the pinned next-3.8, so production is safe, but two consequences are
load-bearing:

- The behavioural test must bind `TMUX_BIN` to the **wrapped** tmux, never
  `pkgs.tmux` (`reflow-fanout-tests` and `picker-go-tests` do use `pkgs.tmux`, so
  this is a live foot-gun in this repo, not a hypothetical).
- The test asserts **directly** that the tmux under test actually wraps —
  `#{qs:}` of a known value must come back single-quoted — so a future pin
  downgrade turns the suite red instead of green-but-meaningless.

#### Two invariants this design depends on — both must be asserted, not assumed

**(a) Exactly one prompt.** `args_copy_copy_value` runs `cmd_template_replace`
once per argv index over the *accumulated* string (`arguments.c:360-367`). With
one prompt there is exactly one pass, so substituted text is never rescanned —
which is why a value containing `%` (`100%-done`) is safe. A second prompt
(`-p a,b`) would have pass 2 scan pass 1's inserted text for `%2`.

**(b) The `{ … }` block form is load-bearing.** `%N` is NQ — inserted with **no
escaping at all** (`cmd.c:843-846`, `:869-871`). It is safe *only* because
`ARGS_COMMANDS` means no re-lex. If a later refactor writes this same bind with a
string command argument, `%1` injects raw text into a re-parsed tmux command
line, and even a benign name containing a space breaks the command. **The fix
therefore makes a new shape dangerous**, and goal 3 obliges a guard for it: the
scanner rule below cannot see it (traced: the trailing `%1` would sit inside a
nested tmux-command string token, and `walk_tokens`' default branch only recurses
on `run-shell|if-shell`, `conf-shell-quoting.bats:232`), so the block form is
pinned by a `list-keys -T prefix ,` assertion instead — the precedent
`tmux-next38-readiness.bats:254-260` already uses for the else branch.

### Empty prompt result — no behaviour change

Because `#{qs:1}` **wraps**, it always contributes exactly one shell word, so the
"the name is exactly one argv word" invariant survives. Measured with a recorder
capturing the real `argv` of the command `run-shell` executes (i.e. `ctl`'s own
`flag.Args()`, *not* the wire argv — the two differ by `wire.CtlProtocolVersion`
at argv[0], `cmd/ctl/main.go:80`):

| prompt result | `'%%'` (today) | `%1` + `#{q:1}` (rejected) | `%1` + `#{qs:1}` (chosen) |
|---|---|---|---|
| `my-window` | `argc=3 … <my-window>` | `argc=3 … <my-window>` | `argc=3 … <my-window>` |
| cleared (`C-u`) then Enter | `argc=3 … <>` | **`argc=2`** — word vanishes | `argc=3 … <>` |
| `@window_bridge_name` unset | `argc=3 … <>` | **`argc=2`** | `argc=3 … <>` |

So the daemon still answers `rename: empty name` (`ctl.go:183-184`), exactly as
today, rather than the arity error `#{q:1}` would have produced (`ctl.go:269-271`).
This is one of two reasons `#{qs:1}` is the right modifier; the other is § verbatim
delivery above. Escape/cancel runs no command, unchanged.

Note `"#{q:1}"` — wrapping the unadorned modifier in double quotes — is **not** an
alternative: inside double quotes `sh` keeps `format_quote_shell`'s backslashes
literal and mangles every value.

### The `#` round trip — total, not merely common-case

Notation: `E` = `sanitizeWindowName` (strip `#[…]`, drop `|`/newline/control,
then escape `#` → `##`); `S` = its strip+drop half alone; `D` = the new decode
(`##` → `#`, pairwise); `X` = `rename-window`'s format expansion.

`rename-window` **does** format-expand its argument — `cmd-rename-window.c:53`,
`format_single_from_target(item, args_string(args, 0))`. But `X` is **not** a
clean inverse of the `#` escape: a run of `#`s immediately followed by `[` is
copied through **verbatim** and left for `format_draw` (`format.c:6671-6694`);
only runs *not* followed by `[` collapse pairwise (`:6696-6704`). Measured:

| `rename-window` argument | resulting `#{window_name}` |
|---|---|
| `a##b` | `a#b` |
| `a####b` | `a##b` |
| `pr##367` | `pr#367` |
| `a##` | `a#` |
| `a#` | `a` (a lone trailing `#` is consumed) |
| `x##[fg=red` | `x##[fg=red` — **verbatim, no collapse** |
| `a####[x]b` | `a####[x]b` — **verbatim, no collapse** |

Because `E` doubles every `#`, every `#`-run in `E`'s output is even-length, so
`D` is exact on it and `E(D(E(r))) = E(S(r)) = E(r)`: the value `ctl` sends is
exactly the option value, for any input. Drift is therefore decided entirely by
whether `X(E(r)) = S(r)`:

| case | `X(E(r))` | drift per unedited rename |
|---|---|---|
| no surviving `#` followed by `[` | `S(r)` ✓ | none |
| a surviving `#` followed by `[` | `E(r)` (verbatim) ✗ | today **+4**, Candidate B alone **+2**, unbounded |

A surviving `#`-before-`[` arises whenever a `#` ends up adjacent to a `[` after
`E`'s two passes. Dropping an unterminated `#[` during the strip scan — r3's fix —
is **not sufficient**, because two later steps re-create the adjacency out of
characters the strip scan never saw together. Measured by running the real
`sanitizeWindowName`:

| remote name | today's `E` | `E(D(E(r)))` | outcome of an unedited Enter |
|---|---|---|---|
| `#\|[x]` | `##[x]` | `""` | error `rename: empty name` |
| `a#\|[x]b` | `a##[x]b` | `ab` | window **silently renamed to `ab`** |

The first arises because the `|`/control drop runs **after** the strip
(`windows.go:172-182` is a second pass over `stripped`), so the strip scan sees
`#|[` and no `#[` at all; removing the `|` then joins them. The second arises the
same way. The root cause is that `S` — strip+drop — is **not idempotent**, which is
exactly the property `E(S(r)) = E(r)` needs.

**Chosen: make `S` idempotent rather than patch one input pattern.** Three changes,
all inside `windows.go`:

1. Do the `|`/newline/control **drop first**.
2. Then **iterate the strip to a fixed point**, with an unterminated `#[` dropping
   to end-of-string (mirroring how a terminated `#[…]` removes the directive *and*
   its body).
3. Then escape `#` → `##`, unchanged.

At a fixed point no surviving `#` is followed by `[` **by construction**, and the
escape cannot create the adjacency, so `X(E(r)) = S(r)` unconditionally and
`E(D(E(r))) = E(r)` for every input. Verified by running the algorithm over the
fixture set below: all idempotent, none with a `#`-run before `[`.

This is in scope (`windows.go`) and **strengthens** requirement 3's guarantees
rather than weakening them. Every existing fixture reproduces byte-identically —
verified by running them: `windows_test.go:93-95` (`[nix-amd-ai … ##46]`,
`PR ##46`, `PR ##46`) and `ctl_test.go:88-90` / `:94-96` / `:134` (`abc`, `it's`,
`""` → `empty name`). A name consisting only of an unterminated style or only
pipes becomes empty, which `applyMirrorName` already skips and the `rename` verb
already rejects.

**Ordering is load-bearing and must be stated in the code:** `E`'s existing comment
(`windows.go:157-158`) already records that the style strip must precede the `#`
escape; the new order is `drop → strip* → escape`, and `D` runs before all of it.
`D` changes the parity of a `#`-run, which changes which `#` pairs with a following
`[`, so an implementation that reorders these silently reintroduces the bug above.

Composition, covering the `[` and `|` families (measured):

| prompt input | `D` | `E` | remote name after `X` |
|---|---|---|---|
| `pr##367` (unedited prefill) | `pr#367` | `pr##367` | `pr#367` ✓ stable |
| `x` (prefill of remote `x#[fg=red`) | `x` | `x` | `x` ✓ stable |
| `ab` (prefill of remote `a#\|[x]b`) | `ab` | `ab` | `ab` ✓ stable |
| `a#(x)` (user typed) | `a#(x)` | `a##(x)` | literal `a#(x)`, **no job** ✓ |
| `a##b` (user typed) | `a#b` | `a##b` | `a#b` — the dialect, documented above |
| `plain-name` | unchanged | unchanged | `plain-name` ✓ |

The first rename of a name carrying styles or pipes is lossy **by design** (that is
what the sanitizer is for) but lands on a fixed point, so every subsequent rename
is a no-op. That is what drift-freedom means here.

The re-encode is **kept**: without it a user-typed `a#(rm -rf ~)` would reach the
remote's `rename-window` expansion and run a job **on the remote**. And `E`'s
strip+drop half applies unconditionally on both paths, unchanged — goal 4.
`ctl_test.go:88-90` and `:134` must stay green **without modification**; that is
the test of whether this was done right.

### Rejected in-scope alternative

Have the daemon stamp a second, *decoded* option purely as the prompt seed. Its
replacement value is not re-scanned, so `#(…)` in it stays inert at prefill time
— but it plants an unescaped remote-derived value in a tmux option, which is the
#355 anti-pattern the moment any other consumer embeds it in a format. It also
would not fix the drift above at all. Rejected.

### Declared scope exception

WORKER_TASK scope is "`config/tmux.conf.nix` (the `bind-key ,` block only) and
`picker/remotebridge/daemon/windows.go` plus their tests." The decode needs **one
line** at `picker/remotebridge/daemon/ctl.go:182` — the function lives in
`windows.go`, but *which* function the rename verb calls cannot.

Declared, not smuggled: one call-site change, no new behaviour in `ctl.go`,
`rename` has exactly one caller (this bind), and nothing on the DO-NOT-TOUCH list
is involved (`cmd/daemon/main.go` is #368's territory and is untouched). Every
in-scope alternative was weighed and rejected above. The alternative to taking the
exception is shipping a fix that drifts `#`-bearing names, which requirement 2
forbids.

## Regression guards

### Static — the conf scanner

`tests/conf-shell-quoting.bats` is already the #355 scanner: a tokenizer matching
tmux's quoting, `{ … }` recursion, and tmux-lexer-accurate logical-line joining.
Its scope comment records that `command-prompt -I` seeds are "not shell and must
NOT be flagged" — correct — and it has no `%%` rule at all, which is the gap #367
sits in.

New rule, in `scan_shell_string` (reached **only** from run-shell's command
argument and if-shell's non-`-F` first argument, `conf-shell-quoting.bats:175-214`):
**a `%%` or `%N` template placeholder must never appear inside a shell string.**

Justification stronger than "today's line was bad": a placeholder is substituted
*after* format expansion, so no `#{q:…}` can ever reach it — it is unprotectable
there **by construction**. And `%N` inside a run-shell string is NQ
(`cmd.c:869-871`), i.e. raw unescaped insertion, strictly worse than today's
`'%%'`. The correct form is always to pass the value as an argument and reference
`#{q:N}`.

False-positive risk: both legitimate uses — the `,` bind's else branch
`rename-window -- '%%'` and
`bind N command-prompt -p "New session name:" "new-session -s '%%'"` — are tokens
visited by the walker's default branch and never reach `scan_shell_string`, so
they cannot be flagged. The trailing `%1` argument token must also stay inert:
run-shell's branch scans only its first non-flag token, and `%1` does not start
with `-`. The guard asserts **both** directions — a planted violation is caught,
and the real conf is clean.

### Static — the block-form pin

Per invariant (b) above, a `list-keys -T prefix ,` assertion pins that the mirror
branch is a `{ … }` block, since that is what makes NQ `%1` safe and the scanner
cannot see its loss.

## Behavioural verification

Requirement 2 says *by observation, not inspection*, so this is specified rather
than left to implementation time. Note the repo's existing answer for bind
coverage is a conf grep (`bridge-carousel-bind-assertions`, `flake.nix:724-732`),
whose own comment explains why: the m2 integration harness drives "vanilla `-L`
servers with no lazytmux keybindings for a gate to intercept"
(`flake.nix:709-711`). So a new, purpose-built check is needed; it must **not** be
a grep.

Harness: local server = the **wrapped tmux with the real emitted conf**
(`TMUX_BIN = tmuxConfig.tmux-wrapped`, the `tmux-next38-readiness-tests`
precedent, `flake.nix:640-654`). `float-conf-assertions` was the wrong precedent
to cite in r2 — it never starts a server. Gate flipped by stamping `@bridge_win`
+ `@bridge_pane` (`tmux-next38-readiness.bats:236-244`) and the **session**-scoped
`@bridge_sock` (`tmux.conf.nix:294` — it is session-scoped, not per-window; a
window stamp only appears to work via the option hierarchy).

A key binding fires only for an **attached client** — `send-keys` alone goes to
the pane's process, which is how r1's first probe produced a silent false
negative. Client comes from a second `-L` server running a real `tmux attach`
(upstream's own `regress/cmd-template-replace.sh` pattern, which is what every
measurement here used) or `util-linux`'s `script` for a pty (already used at
`flake.nix:663`, `:682`). The attach must neutralise the splash popup
(`@splash_shown`) so it cannot eat keys or the captured status line.

**1. Security half — the discriminating test.** `@window_bridge_name` =
`probe#(<side effect>)`; drive the real `prefix + ,` then Enter; assert the side
effect never happens. No daemon or socket needed: the job fires inside
`run-shell`'s format expansion, before `ctl` dials, so `ctl` failing to connect is
irrelevant.

Two things this test must get right or it cannot fail:

- **`#(…)` is asynchronous.** `format_job_get` (`format.c:6638-6645`) starts the
  job and substitutes the *previous* (empty) value, so the side effect lands after
  `run-shell` has already handed its command to `sh`. Asserting immediately after
  Enter passes on the **unfixed** conf. The assertion must be a **bounded poll**
  that fails only once the timeout expires.
- **A positive control is mandatory.** The same payload reached through a
  deliberately unprotected `run-shell` in the same harness must produce its side
  effect, proving the observation channel works at all. Without it a broken
  harness reads as "fixed".
- The payload must be **shell-agnostic**: `run-shell` uses the configured
  `default-shell`, which is fish on this host (`ctl.go:205`), and that shell must
  be in the check's sandbox closure.

**2. Works-and-faithful half.** A recording stub bound to `@bridge_sock` captures
the delivered **wire** argv. The format is trivially fakeable — one type byte, a
4-byte big-endian length, then NUL-separated argv (`wire/protocol.go:22,37-57,64-76`).
The stub **must write a `FrameCtlAck`** (type `7`, empty payload — five bytes),
otherwise `ctl` reports `does not speak the ctl protocol`
(`cmd/ctl/main.go:87-93`) via `tmux display-message -t <client>` (`:66-68`), which
would overwrite the status line the prefill assertion captures.

Assert the wire argv is exactly `2`, `rename`, the pane id, and the name
**verbatim** — "verbatim" means *the wire argv value equals the option value*,
which is the value this design controls; it is deliberately not a claim about the
remote's resulting name, which `X` and `E` transform (§ round trip). Payloads:
`'`, `$`, spaces, `#`, UTF-8. The prefill *contents* are observed by capturing the
status line after `prefix + ,` and before Enter.

**3. Drift-freedom — a test that can actually fail.** Asserting idempotence
against the recording stub would be **vacuous**: `@window_bridge_name` is only
re-derived by the daemon's reconcile (`ctl.go:177-180` → `applyMirrorName`), so
with a stub the second `prefix + ,` prefills the same value and delivers the same
argv whether decode exists, is absent, or is wrong. Two real observables replace
it:

- **Go fixed point** (unit test, `windows_test.go`): over **raw inputs** `r`,
  assert `E(D(E(r))) == E(r)`, and separately that no `#`-run in `E(r)` is
  followed by `[`. Stated over raw inputs deliberately: the alternative form
  `E(D(s)) == s` "for `s` in the image of `E`" is a trap, because a literal like
  `a####[x]b` is *not* in the image once the strip is idempotent, and an
  implementer "fixing" that failure would weaken the very test that replaced the
  vacuous observer. Fixtures: `pr#367`, `a##b`, `plain-name`, `x#[fg=red`,
  `a##[x]b`, `#|[x]`, `a#|[x]b`, `##[a][`, `a|b\nc`, `|||`, `it's`, `~/src`, and
  the real `windows_test.go:93` name. Plus a direct unit test of `D`.
- **tmux-level inverse** (bats, pinned tmux, **no client needed**):
  `rename-window -- E(r)` then `display-message -p '#{window_name}'` yields
  `S(r)`, for the same fixtures. This is the half that pins escape-vs-expansion
  and the half the `[` case broke.

Together these fail on a missing decode, on `D = strip all '#'`, on the
non-idempotent strip+drop ordering, and on a sanitizer that lets an unterminated
`#[` through.

## Acceptance criteria

- [ ] The `,` bind's mirror branch passes the prompt result as a `run-shell`
      argument (`%1`) referenced `#{qs:1}` — the single-quoting modifier, **not**
      the unadorned `#{q:1}`; no `%%`/`%N` remains in any shell string in the
      emitted conf; the bind stays single-prompt and keeps its `{ … }` block form,
      the latter pinned by a `list-keys` assertion.
- [ ] A test that **fails before the fix and passes after**, driving a
      metacharacter-bearing window name through the real rename path with a real
      attached client. `#(…)` discriminates; `'`, `"`, `;`, `$` are asserted as
      non-regression. The test uses a bounded poll and ships a positive control
      proving the channel can observe a `#(…)` side effect.
- [ ] `prefix + ,` still renames a mirror window with the current name
      prefilled, **verified by observation** — the observed prompt contents and
      the observed wire argv are stated, not inferred.
- [ ] The wire argv carries the name **verbatim** (equal to the option value) for
      `'`, `$`, spaces, `#`, UTF-8, **`~/src`, `~root` and `x{a,b}`** — the last
      three are the payloads that catch a regression back to `#{q:1}`, and
      `~/src` specifically must not arrive as the local `$HOME`.
- [ ] **Drift-freedom**, via both observables above, over the full fixture set
      including `#|[x]`, `a#|[x]b`, `##[a][`, `x#[fg=red` and the real
      `windows_test.go:93` name. A `pr#367`-only check does not satisfy this.
- [ ] `sanitizeWindowName`'s strip+drop half is **idempotent** — drop first, strip
      iterated to a fixed point, unterminated `#[` dropped — and `E(D(E(r))) == E(r)`
      holds for every fixture, with no `#`-run in `E(r)` followed by `[`.
- [ ] Empty / cleared prompt result still yields `rename: empty name` (one argv
      word preserved by `#{qs:1}`), asserted by observation.
- [ ] `sanitizeWindowName`'s `#` / `|` / control-char guarantees hold on **both**
      paths; `ctl_test.go:88-90` and `:134` pass **unmodified**; `windows_test.go`
      stays green.
- [ ] The `conf-shell-quoting` scanner rejects a planted `%%`/`%N` in a shell
      string, does not flag the two legitimate tmux-command uses, and leaves the
      trailing `%1` argument token inert.
- [ ] All three local gates pass separately: `nix build .#default`,
      `nix flake check`, `nix build .#lint`.

## Severity framing (for the PR)

Requires a user keypress (`prefix + ,`) in a mirror window on a hostile or
compromised remote. **Not** the unattended path #355 closed. But it is the same
remote → local trust boundary, and because the value is *pre-filled*, the user
need type nothing malicious themselves — Enter on what looks like the window's own
name is enough. The live vector is `#(…)`, not the shell metacharacters the issue
names; say so plainly rather than inheriting the issue's framing.
