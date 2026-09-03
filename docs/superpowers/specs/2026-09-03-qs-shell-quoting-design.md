# Design: close the residual `#{q:}` shell-quoting class (#379)

Status: accepted
Issue: #379 (residual to #355)

## Problem

`run-shell`/`if-shell` format-expand their whole argument *before* handing it to
a shell, so every `#{...}` in such a string is shell input. tmux's fix is
`#{q:}` — but `#{q:}` **escapes a fixed character set and never wraps**, so any
shell-significant character outside that set survives into the shell.

## The escape set, read from the shipped tmux

`format_quote_shell` (tmux next-3.8, `format.c:4390`, rev `d5afb67`):

```c
if (strchr("|&;<>(){}$`\\\"'*?[# =%\n\t", *cp) != NULL)
        *at++ = '\\';
```

Escaped: `|` `&` `;` `<` `>` `(` `)` `{` `}` `$` `` ` `` `\` `"` `'` `*` `?`
`[` `#` space `=` `%` newline tab.

**Not escaped, and shell-significant: `~`.**

## Measured end-to-end, on the repo's pinned next-3.8 (`.#default`)

A `run-shell` handing `#{q:session_name}` / `#{qs:session_name}` to a script
that prints its own argv:

| `session_name` | `#{q:}` → argv | `#{qs:}` → argv |
|---|---|---|
| `~/src`     | `ARGC=1 </home/USER/src>` — **tilde expanded, wrong path** | `ARGC=1 <~/src>` — correct |
| `x{a,b}`    | `ARGC=1 <x{a,b}>` — correct (`q:` emits `x\{a,b\}`) | `ARGC=1 <x{a,b}>` — correct |
| `~x{a,b}y`  | `ARGC=1 <~x{a,b}y>` — correct | correct |

### Correction to the issue's premise

The issue states that `{}` survives `#{q:}` on next-3.8. **It does not.** `{`
and `}` are in the escape set above, and `x\{a,b\}` defeats brace expansion
(measured: `bash -c '… x\{a,b\}'` → `ARGC=1`, versus `ARGC=2` unescaped). The
brace claim *is* true of tmux 3.7c, whose narrower set omits `{}` — measured:
`#{q:session_name}` of `x{a,b}~y` returns it verbatim on 3.7c.

**So the residual class on the tmux this repo ships is exactly `~`.**

### Tilde expansion needs word-initial position

Measured in the shell `run-shell` uses:

| word | argv |
|---|---|
| `~/src` | `/home/USER/src` — expanded |
| `x~/src` | `x~/src` — not expanded (mid-word) |
| `--sock=~/src` | `--sock=~/src` — not expanded (not an assignment statement) |

So a `#{q:}` site is tilde-reachable **only when the expansion begins a word**
*and* its value can begin with `~`. Both conditions must hold.

## The second residual class: an empty value delivers ZERO words

`#{q:}` does not merely fail to wrap — on an **empty** value it emits nothing,
so the word disappears and every later positional shifts. Measured end-to-end
through `run-shell`, handing the expansion to a script that prints its own argv:

| option value | `#{q:}` raw | argv | `#{qs:}` raw | argv |
|---|---|---|---|---|
| `''` (empty) | `` | `ARGC=0` | `''` | `ARGC=1 <>` |

The repo already knows this (`tests/conf-shell-quoting.bats:143`: "which a bare
`#{q:}` cannot do (empty = zero words)"), but only as the justification for one
bind's `#{?…}` wrapper. It is a general property of every `#{q:}` site, and
`#{qs:}` closes it. **So the residual class is `~` *and* the empty value** — two
axes, and a site needs auditing on both.

### Corollary: the wrappers are not retired

The issue suggests `#{qs:}` "removes the need" for
`#{?client_name,--client #{q:client_name},}`. It does not. That conditional
emits the flag **and** its value together or neither; `#{qs:}` would emit
`--client ''`, which is a different argv contract. The wrappers stay.

## Inventory

Two different measurements answer two different questions, and conflating them
is how the counts drift. **Both are stated; each row below is the first.**

- **Nix source** (`config/tmux.conf.nix`) — what actually gets edited:
  **87** `#{q:NAME}` occurrences across **22** formats, plus `#{q:}` and
  `#{q:1}`. One of the 8 `client_name` occurrences is inside a comment.
- **Emitted `tmux.conf`** — what tmux executes: ~124 by grep, of which ~117
  reach a shell string the scanner walks (the rest are comment text, the
  `is_vim=` macro body, and two `#(…)` bodies). The two counts differ mostly
  because `bridgeCtl` is **one** Nix-source string interpolated into ~18 binds.

| format | src | tilde-reachable? | empty-reachable? | verdict |
|---|---|---|---|---|
| `session_name` | 17 | **yes** — word-initial, and user-set (tmux forbids only `.` and `:`); a bridge mirror's is `${host}-${sess}`, remote-derived | n/a — a session always has a name | **→ `qs:`** |
| `hook_session_name` | 3 | **yes** — same value class | n/a | **→ `qs:`** |
| `pane_current_command` | 1 | **yes** — the foreground process' own name | no | **→ `qs:`** |
| `pane_current_path` | 1 | no — a cwd is absolute, always `/`-initial | **yes** — `format_cb_current_path` returns NULL when `wp == NULL` or `osdep_get_cwd` fails (`format.c:967-972`), e.g. a dead pane. `bind Y` then runs `tmux display-message -p` with **no argument**, which prints the default message-format — measured: `[probe] 1:branch , current pane 1 - (…)` — and `wl-copy` copies that instead of a path | **→ `qs:`** |
| `client_name` | 8 (1 in a comment) | no — `c->ttyname` (`/dev/…`) else `client-<pid>` (`server-client.c:2985-2988`) | yes, but harmless: the ~18 emitted sites are `--display-error=…`, one word either way, and the 6 word-initial ones sit inside the `#{?client_name,…}` wrapper | keep `q:` |
| `hook_client` | 2 | no — same value class as `client_name` | yes — `tmux.1` defines it as "Name of client where hook was run, **if any**". Harmless: it is the **last** positional to `tmux-splash-maybe`, which reads `client="${2:-}"` | keep `q:` |
| `client_width` | 8 | no — decimal | yes — empty with no attached client (#235). Harmless by design: `tmux-reflow-windows` reads `WIDTH=${2:-…}` with a targeted fallback **and** an explicit non-numeric guard | keep `q:` |
| `@window_per` | 2 | no — decimal | yes — unstamped before the first reflow. Harmless: last positional to `tmux-window-nav`, gated by `[[ $per =~ ^[0-9]+$ ]] … \|\| exit 0` → a clean no-op | keep `q:` |
| `@pane_keys_raw` | 4 | no — this conf sets it to a literal | yes — unset on all but the one float. Harmless: last positional to `tmux-smart-nav`, which reads `raw=${5:-}` | keep `q:` |
| `@bridge_sock` | 1 | no — every emitted site is `--sock=…`, and tilde expansion needs word-initial position | no — `bridgeGate` requires it non-empty, and `--sock=` is one word regardless | keep `q:` |
| `@bridge_pane` | 16 | no — a pane id, `%<digits>` | no — gated non-empty by `bridgeGate` | keep `q:` |
| `pane_id` | 2 | no — `%<digits>` | no | keep `q:` |
| `window_id` | 5 | no — `@<digits>` | no | keep `q:` |
| `window_index` | 4 | no — decimal | no | keep `q:` |
| `window_zoomed_flag`, `pane_at_{top,right,left,bottom}` | 8 | no — `0`/`1` | no | keep `q:` |
| `pane_tty` | 2 | no — a pty device path, `/dev/…` | no | keep `q:` |
| `@catppuccin_flavor` | 1 | no — the site is `[ x#{…} = x ]`, so it is never word-initial | no — the `x`-prefix idiom exists precisely to make empty safe | keep `q:` |

**Converted: 22 Nix-source edits across 4 formats.** Every retained `#{q:}` is
adjudicated on both axes above, and the four whose retention is not evident from
the value's name get a one-line reason at the site.

### Flagged, not fixed

`bind Y` passes the path to `tmux display-message -p`, which treats its argument
as a **format** and expands it again. A path containing `#{…}` or `#(…)` is
therefore re-expanded whichever quoting modifier is used — a double-expansion
distinct from #379's class, and out of its scope. Worth its own issue.

## Why targeted, and not a blanket `q:` → `qs:` sweep

`#{qs:}` **degrades silently on tmux 3.7**: measured on 3.7c with
`@v = '~/src; id'`, `#{q:}` gives `~/src\;\ id` while `#{qs:}` gives
`~/src; id` — the **raw** value, not empty, not an error (a genuinely unknown
modifier `#{zz:}` *does* return empty). On a pre-3.8 server a `qs:` site has no
quoting at all, where `q:` still escapes `;`, `$` and backtick.

**This cost applies symmetrically to the 22 sites being converted, not only to
the sweep being rejected.** On a resident pre-3.8 server those 22 lose
metacharacter escaping — which for `session_name` means a session named `;id`
would inject. That is a real regression, accepted here for bounded reasons, and
the whole argument for narrow scope is that it buys the fix at 22 sites of
exposure instead of 124:

- The repo **ships** next-3.8 (`mkTmux`); the only route to 3.7 is a server that
  predates a nix switch and keeps its old binary resident (#407).
- The conf **already** depends on next-3.8 for correctness: `#{next_window_index}`
  drives the per-row `│` separators (#104) and is measured **empty** on 3.7c, so
  a pre-3.8 server already misrenders the status bar.
- A pre-3.8 live server already gets a visible pane notice ending "Restart the
  server to pick up the tmux 3.8 monitor hook"
  (`modules/home-manager.nix:109`, reached with the live server's own
  `#{version}` per #407). Stated precisely, because it is easy to overclaim:
  that message is about tmux-remux's periodic-save floor, **not** about quoting,
  and it only fires when `persist.enable` is on (default true). It is a prompt
  to restart — which does resolve the quoting — not a warning about it.

**Non-goal:** a new runtime version gate in the conf. Adding a second pre-3.8
notice is scope this issue does not carry, and the honest mitigation is the
restart the existing one already prompts.

## The green-but-proves-nothing trap

Because `qs:` degrades *silently*, a bind-level test run against `pkgs.tmux`
(3.7c) can pass while proving nothing — the format returns the raw value, the
script still receives one argument, and the assertion holds for the wrong
reason. Two guards, both required:

1. **A capability assertion**, run before any `qs:` behavioural assertion: the
   tmux under test must actually wrap — `#{qs:}` of a known value must come back
   `'`-delimited. This fails loud on 3.7 rather than passing hollow.
2. **Bind-level tests bind the pinned tmux**, never `pkgs.tmux` — the
   `tmux-next38-readiness-tests` pattern (`TMUX_BIN = tmuxConfig.tmux-wrapped`).

## The static guard must be strengthened, not merely updated

`tests/conf-shell-quoting.bats` **already accepts** `#{qs:NAME}` alongside
`#{q:NAME}` (`scan_shell_string`, the `$group != '#{q:'* && $group != '#{qs:'*`
predicate) — added by #367's `%%`-prefill work. So the issue's claim that the
rule "would reject the fix" no longer holds against current `main`; nothing
there needs relaxing.

What it cannot currently do is **hold a converted site converted**. A future
edit dropping `#{qs:session_name}` back to `#{q:session_name}` is silently
accepted, and the tilde bug returns. The rule therefore gains a second,
narrower predicate: a **wrap-required allowlist** of formats that must appear
as `#{qs:}` when they reach a shell string. `q:` on such a format is a
violation; every other format keeps the existing either-form rule.

## Acceptance

- The **22** sites across `session_name`, `hook_session_name`,
  `pane_current_command` and `pane_current_path` use `#{qs:}`; every retained
  `#{q:}` is adjudicated on **both** axes in the inventory table, and the four
  whose retention is not evident from the value's name carry a one-line reason
  at the site.
- `tests/conf-shell-quoting.bats` enforces a wrap-required allowlist for those
  four formats, and rejects a `#{qs:}` placed inside shell quotes (where it is
  strictly weaker than `#{q:}`). Fixtures cover both directions.
- A capability assertion refuses to let a non-wrapping tmux report green, and
  the behavioural cases run against the pinned wrapper, never `pkgs.tmux`.
- `nix build .#default`, `nix flake check`, `nix build .#lint` pass.

## Severity

Not an unattended remote→local path like #355, and not arbitrary execution.
Two outcomes, both bounded:

- **Tilde** — needs a session named (or a process self-named) with a leading
  `~`; a helper script then receives the wrong path.
- **Empty value** — needs a pane whose cwd cannot be read; `bind Y` then copies
  tmux's default status message to the clipboard instead of a path.

Do not overclaim either in the PR body. The one place this could have been
worse is the 3.7 regression above, which is a *pre-existing-server* condition
the repo already prompts the user to resolve.
