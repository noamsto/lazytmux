# Plan — `command-prompt` `%%` prefill is remote-derived (#367)

Design: `docs/superpowers/specs/2026-08-12-command-prompt-prefill-remote-derived-design.md`
— it carries the measured tmux behaviour this plan's fixture table depends on.

Notation (from the spec): `E` = `sanitizeWindowName`, `S` = its drop+strip half,
`D` = the new decode, `X` = `rename-window`'s format expansion.

## The fixture table — one source of truth, cited by steps 2, 6 and 7

Measured by running the step-1 algorithm; `S` values also measured through
`rename-window` on the pinned tmux.

| # | `r` (remote name) | `E(r)` | `S(r)` |
|---|---|---|---|
| 1 | `pr#367` | `pr##367` | `pr#367` |
| 2 | `a##b` | `a####b` | `a##b` |
| 3 | `plain-name` | `plain-name` | `plain-name` |
| 4 | `x#[fg=red` | `x` | `x` |
| 5 | `a##[x]b` | `a##b` | `a#b` |
| 6 | `#\|[x]` | `` (empty) | `` (empty) |
| 7 | `a#\|[x]b` | `ab` | `ab` |
| 8 | `##[a][` | `` (empty) | `` (empty) |
| 9 | `a\|b` + newline + `c` | `abc` | `abc` |
| 10 | `\|\|\|` | `` (empty) | `` (empty) |
| 11 | `it's` | `it's` | `it's` |
| 12 | `~/src` | `~/src` | `~/src` |
| 13 | `[nix-amd-ai 🧠 #[fg=#94e2d5]󰪣#[fg=default] 󰘭 #46]` | `[nix-amd-ai 🧠 󰪣 󰘭 ##46]` | `[nix-amd-ai 🧠 󰪣 󰘭 #46]` |

Rows 1-3, 11-13 are the legitimate-name cases; 4-8 are the `#`/`[`/`|` adjacency
cases that drift without step 1; 9-10 and 13 reproduce existing test expectations
(`ctl_test.go:88-90`, `:134`, `windows_test.go:93`).

**Rows 6, 8, 10 have empty `E`** and are therefore excluded from step 6's tmux
inverse (there is nothing to `rename-window`); they are covered by step 2's Go
assertions and by step 7's `rename: empty name` path instead.

---

- [ ] **Step 1: factor and reorder the sanitizer; add the decode** (implement: escalated)

  `picker/remotebridge/daemon/windows.go`. Security-sensitive, and the idempotence
  property is subtle.

  Today `sanitizeWindowName` is one function with two inline `strings.Builder`
  loops (`windows.go:159-184`). Split it so the drop+strip half is **callable**,
  because steps 2 and 6 assert on it directly:

  - `stripWindowName(s)` — `S`. Drop `|`, `\n`, `\r`, `r < 0x20`, `0x7f` **first**
    (today this is the *second* pass), then strip `#[…]` **iterated to a fixed
    point**, with an **unterminated** `#[` dropping to end-of-string (today: single
    pass, unterminated `#[` preserved).
  - `sanitizeWindowName(s)` = escape `#` → `##` applied to `stripWindowName(s)`.
    Its external contract is unchanged.
  - `decodeWindowName(s)` — `D` — `strings.ReplaceAll(s, "##", "#")`. A run of *n*
    `#` maps to ⌈n/2⌉ regardless of scan direction, so there is no left/right fork.

  The comment must state **why** the order is load-bearing (extending the existing
  note at `windows.go:157-158`): the `|`/control drop can join a `#` to a `[` the
  strip scan never saw together (`a#|[x]b` → `a##[x]b`), and a single strip pass can
  join a surviving `#` to a later `[` (`##[a][` → `#[`); either leaves a `#`-run
  before `[`, which `X` passes through **verbatim** (`format.c:6671-6694`) instead
  of collapsing, which drifts unboundedly.

  Do **not** widen what `E` drops beyond the unterminated-`#[` case, and keep
  `#` → `##`.

- [ ] **Step 2: unit-test the Go properties**

  `picker/remotebridge/daemon/windows_test.go`. Over the fixture table as **raw
  inputs** `r` (not "the image of `E`" — the spec explains why that form is a trap):

  - `E(D(E(r))) == E(r)` for every row.
  - no `#`-run in `E(r)` is immediately followed by `[`, for every row.
  - `S(S(r)) == S(r)` for every row.
  - `E(r)` and `S(r)` equal the table's values (this is what makes step 6's
    hardcoded expectations non-tautological — they are pinned here, in Go, against
    the real functions).
  - a direct table test of `D`, including an odd run (`###` → `##`) and `#` alone.

  Keep **all existing** `TestSanitizeWindowName` cases (`windows_test.go:87-95` —
  eight of them) unmodified; they must pass as-is.

- [ ] **Step 3: decode on the inbound rename path**

  `picker/remotebridge/daemon/ctl.go:182` — one line:
  `name := sanitizeWindowName(decodeWindowName(a[0]))`.

  The spec's declared scope exception. Comment it: the prompt hands back a value
  already in `@window_bridge_name`'s escaped dialect, so re-encoding without
  decoding first is a double-encode; the re-encode is **kept** because the remote's
  own `rename-window` format-expands, so a user-typed `a#(x)` must not reach it bare.

  `ctl_test.go:88-90`, `:94-96`, `:134` must pass **unmodified** — verify, don't
  edit.

- [ ] **Step 4: restructure the `,` bind — lands together with step 5**

  `config/tmux.conf.nix:780`, mirror branch only. Replace
  `run-shell "${bridgeCtl} rename #{q:@bridge_pane} '%%'"` with
  `run-shell "${bridgeCtl} rename #{q:@bridge_pane} #{qs:1}" %1`.

  Leave the `else` branch byte-identical (it pins next-3.8's default,
  `tests/tmux-next38-readiness.bats:254-260`). Keep the `{ … }` block form and the
  single prompt — both load-bearing (spec § "Two invariants").

  **No Nix-side `#` doubling.** `#{q:@bridge_pane}` already appears literally here
  and inside `bridgeCtl` (`tmux.conf.nix:304`); the `#` → `##` replace applies only
  to enrich *icon* values. Write `#{qs:1}` plainly — a later reader must not "fix"
  it into `##{qs:1}`.

  Update the comment above the bind to record: `#{qs:1}` **not** `#{q:1}` (the
  latter leaves `~`/`{`/`}` unescaped and never wraps — measured to deliver the
  local `$HOME` for a remote name `~/src`, and to split `x{a,b}` into two argv
  words); `%1` **not** `'%%'` (NQ, so `#{qs:}` is the only quoting layer); and that
  the `{ … }` block form is what makes NQ safe.

- [ ] **Step 5: widen and extend the conf-shell-quoting scanner** (implement: escalated)

  `tests/conf-shell-quoting.bats`. Two changes; this touches the #355 guard's own
  accept semantics, hence escalated. **Must land in the same commit as step 4** —
  between them the tree is red.

  1. **Widen the accept predicate.** It is currently
     `elif [[ $group != '#{q:'* ]] || ((nested))` (`:160`), so `#{qs:1}` is
     rejected — `s` sits at index 3. Accept exactly the two *shell*-quoting forms
     `#{q:` and `#{qs:` with a plain, unnested body. `#{qe:` / `#{qh:` (style
     quoting, not shell), bare `#{…}`, and nested bodies must **stay flagged**. Add
     fixtures to the file's good/bad tables asserting `#{qs:` passes while `#{qe:`
     and a bare form still fail — otherwise "widen" silently retires the guard.
  2. **Add the `%%`/`%N` rule** in `scan_shell_string`: a `%%` or `%N` (N = 1-9)
     template placeholder must never appear inside a shell string. Comment the
     justification: substituted *after* format expansion, so no `#{q:…}`/`#{qs:…}`
     can reach it — unprotectable there by construction; and `%N` inside a
     run-shell string is NQ (`cmd.c:869-871`), raw unescaped insertion.

  If the new `#{qe:` / bare / `%%` fixtures go into the shared `bad.conf` heredoc
  (`:358-366`) rather than into new `@test`s with their own conf files, bump the
  hardcoded `[ "$count" -eq 7 ]` at `:380` — a stale count reads as a widening bug.

  Test both directions: the real emitted conf is clean, and a **planted** `%%`
  inside a run-shell string is rejected. Assert the two legitimate uses stay
  unflagged — the `,` bind's else branch `rename-window -- '%%'` and
  `bind N … "new-session -s '%%'"` — and that the trailing `%1` **argument** token
  stays inert.

- [ ] **Step 6: pin the block form and the tmux-level inverse**

  `tests/tmux-next38-readiness.bats` — already runs the wrapped tmux with the real
  emitted conf and gives each test its own `-L` socket, so **no new derivation, env
  or inputs** are needed.

  - `list-keys -T prefix ,` shows the mirror branch as a `{ … }` block containing
    `run-shell` with `#{qs:1}` and a trailing `%1`. This is the guard the scanner
    structurally cannot provide: `list-keys` prints `ARGS_COMMANDS` as `{ … }` and
    `ARGS_STRING` as a quoted string, so converting the block to a string is
    visible here and nowhere else (spec § "Static — the block-form pin").
  - tmux-level inverse, **no client needed**: for each fixture row **except 6, 8,
    10** (empty `E`), `rename-window -- E(r)` then
    `display-message -p '#{window_name}'` equals `S(r)`. Expected strings are
    hardcoded from the fixture table; step 2 pins those same strings against the
    real Go functions, so the pair is not tautological.

- [ ] **Step 7a: build the behavioural harness, with a self-test** (implement: escalated)

  New bats file + new `flake.nix` `checks` entry (registration under `checks` is
  all that is needed — `nix flake check` enumerates that attrset, and
  `cp -r ${./tests}` picks up a new file automatically).

  Harness, per spec § "Behavioural verification":
  - local server = `tmuxConfig.tmux-wrapped` with the real emitted conf
    (`TMUX_BIN`, the `tmux-next38-readiness-tests` precedent, `flake.nix:640-654`);
  - gate flipped by stamping `@bridge_win` + `@bridge_pane` (window) and
    `@bridge_sock` (**session**-scoped, `tmux.conf.nix:294`);
  - client from a second `-L` server running a real `tmux attach` — a key binding
    fires **only** for an attached client, `send-keys` alone goes to the pane's
    process (this is how an early probe produced a silent false negative);
  - `@splash_shown` set so the splash popup cannot eat keys or the captured status
    line.
  - **Recording stub — vehicle chosen, not deferred:** `socat
    UNIX-LISTEN:$sock,fork EXEC:<recorder>` with a bash recorder that reads the
    5-byte header (type + 4-byte big-endian length), writes the **raw payload to a
    file**, and writes a `FrameCtlAck` — type `7`, empty payload, i.e. the five
    bytes `\x07\x00\x00\x00\x00`. The ack is **mandatory**: without it `ctl`
    reports `does not speak the ctl protocol` (`cmd/ctl/main.go:87-93`) via
    `display-message -t <client>`, which overwrites the status line step 7b's
    prefill assertion reads. `socat` is already in the wrapper closure
    (`tmux.conf.nix:1189`); add it to the check's `nativeBuildInputs` explicitly.
    The frame shape is `wire/protocol.go:22,37-57,64-76`; `cmd/ctl/main_test.go:30-40`
    is the in-repo precedent for the same listener+ack.
  - **The payload must never pass through a shell variable.** Bash cannot hold NUL,
    so any `$(…)`/`tr`/`mapfile -d ''`/`cut` route silently loses the one byte step
    7b.4 turns on. Assert against the file, at byte level.
  - Check inputs: `socat`, plus `CTL = "${pickerChecked}/bin/lztmux-remote-bridge-ctl"`
    (precedent `flake.nix:711` — 7a's self-test needs the real binary),
    `TMUX_BIN = "${tmuxConfig.tmux-wrapped}/bin/tmux"`, `LANG`/`LC_ALL` and the
    `cp -r ${./tests}` line, all from `flake.nix:640-654`. `default-shell` is null in
    `tmuxConfig` (`flake.nix:79-84`, `tmux.conf.nix:575-577`), so `run-shell` uses
    `/bin/sh` in the sandbox — do **not** add fish to the closure.
  - Stamp `@bridge_pane` with `-p` (pane option) as production and
    `tests/tmux-next38-readiness.bats:243` do. A window stamp only resolves by
    inheritance and would make the gate a possible false negative.

  **Self-test the harness before asserting anything about the bind**: feed the
  recorder a hand-rolled `FrameCtl` and assert it captures the argv, and drive one
  real `ctl` invocation and assert it does *not* error — i.e. the ack satisfies it.
  A harness that silently fails to observe reads as "fixed".

- [ ] **Step 7b: the four assertion groups**

  1. **Security (discriminating).** `@window_bridge_name` = a `#(…)` payload with an
     observable side effect. Drive `prefix + ,` then Enter; assert the side effect
     never happens via a **bounded poll** — `#(…)` is async (`format_job_get`
     substitutes the previous value and the job lands later), so an immediate
     assertion passes on the *unfixed* conf. Ship a **positive control**: the same
     payload through a deliberately unprotected `run-shell` in the same harness must
     produce its side effect. Payload must be shell-agnostic (`run-shell` uses
     `default-shell`).
  2. **Works-and-faithful.** Wire argv == `2`, `rename`, pane id, name **verbatim**
     for `'`, `$`, spaces, `#`, UTF-8, **`~/src`, `~root`, `x{a,b}`** — the last
     three catch a regression to `#{q:1}`, and `~/src` specifically must not arrive
     as the local `$HOME`.
  3. **Prefill contents** observed by capturing the status line after `prefix + ,`
     and before Enter.
  4. **Empty result — assert at byte level, or the test is vacuous.**
     `EncodeArgv` joins with NUL **between** fields (`wire/protocol.go:37-46`), so
     argv `["2","rename","%42",""]` is the 13 bytes `2\0rename\0%42\0` while
     `["2","rename","%42"]` is the 12 bytes `2\0rename\0%42` — measured. Every
     field-splitting reading (`mapfile -d ''`, `cut`, `awk RS='\0'`, `tr` into a
     variable) yields three fields for **both**, so it passes identically on the
     fixed conf and on a regression to `#{q:1}`. The only discriminator is the
     trailing NUL.

     So: clear the prefill, Enter, and assert the captured payload is byte-for-byte
     `2\0rename\0<pane>\0` — equivalently NUL count 3 and byte length 13 for a
     `%42` pane. Apply the mirror rule in 7b.2: a non-empty name is 3 NULs with a
     **non-empty** tail. Why this is the right observable: `DecodeArgv`
     (`wire/protocol.go:51-57`) turns that trailing NUL into a fourth, empty field,
     which is what yields `rename: empty name` (`ctl.go:183-184`) rather than the
     arity error (`ctl.go:269-271`).

- [ ] **Step 8: run the three gates separately, to green**

  `nix build .#default`, `nix flake check`, `nix build .#lint`. None subsumes
  another — `nix flake check` does not run the formatter, and a missed `alejandra`
  is how a formatting failure reaches CI. Fix and re-run until all three are green.

- [ ] **Step 9: record what was observed, then commit and open the PR**

  WORKER_TASK requirement 2 asks for observation *stated*, not inferred: record the
  actual prefill string and the actual wire argv seen in the 7b run, verbatim, and
  put them in the PR body. Frame severity as the spec's § "Severity framing" does —
  `#(…)` is the live vector, **not** the four characters the issue names — and
  include `Closes #367`, the `## Escalated` note (the spec needed a third critic
  revision, beyond the 2-revision cap), and any unresolved review notes.

- [ ] **Step 10: commit the design + plan docs; remove the scratch files**

  Per `CLAUDE.md` ("Plans and Specs"), both documents go in the same PR.

  `gtrash put` (not `rm`) `FINDINGS-367.md` and `WORKER_TASK.md` — scratch, not
  deliverables. **Scrub the two dangling references** in the same step: the spec's
  § "What actually reproduces" pointer and this plan's header both cite
  `FINDINGS-367.md`; fold the needed measurements inline and drop the pointers.

---

## Verification summary — what fails if the implementation is wrong

| break it this way | which step catches it |
|---|---|
| revert the bind to `'%%'` | 7b.1 (side effect fires), 5 (scanner rule), 6 (list-keys) |
| use `#{q:1}` instead of `#{qs:1}` | 7b.2 (`~/src` → local `$HOME`; `x{a,b}` splits), 7b.4 (argv word vanishes) |
| convert the block to a string command arg | 6 (list-keys shows a quoted string) |
| omit the decode | 2 (`E(D(E(r))) ≠ E(r)` on rows 1, 2, 13) |
| decode as "strip all `#`" | 2 (`D` table, and `E(r)` row values) |
| keep the old strip+drop order | 2 (rows 6, 7: `E` becomes `##[x]` / `a##[x]b`) |
| leave the unterminated `#[` in | 2 (no-`#`-before-`[` assertion, rows 4, 8), 6 |
| drop the `\|`/control filter inbound | 3 (`ctl_test.go:88-90` unmodified) |
| widen the scanner to `#{q*` | 5.1 (`#{qe:` / bare fixtures still fail) |
| write a test that cannot fail | 7a's self-test, 7b.1's positive control, 5's planted violation |

Rows deliberately **not** claimed: step 6 cannot catch an omitted decode (it asserts
`X(E(r)) == S(r)`, which never involves `D`), and `ctl_test.go`'s existing cases
cannot catch a wrong decode (none of them contains a `#`). Step 2 is the sole
observer for both.
