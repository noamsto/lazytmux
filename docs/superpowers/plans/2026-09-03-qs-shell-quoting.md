# Plan: close the residual `#{q:}` shell-quoting classes (#379)

Design: `docs/superpowers/specs/2026-09-03-qs-shell-quoting-design.md`
Closes #379

The design settles *what* and *why*, including the per-format verdicts on both
residual axes (`~` and the empty value). This is the ordered work.

**Counting basis:** every count below is **Nix-source occurrences** in
`config/tmux.conf.nix` — what actually gets edited. The emitted `tmux.conf` has
more, because `bridgeCtl` is one source string interpolated into ~18 binds.

## Step 1: convert the 22 sites in `config/tmux.conf.nix`

Precondition, verified and re-verifiable with Step 2's new predicate: all 22 are
**bare** in their shell string. `#{qs:}` inside shell quotes is strictly weaker
than `#{q:}`, so a converted site must be unquoted.

- [ ] `#{q:session_name}` → `#{qs:…}` — **17**. None in comments. 15 land in
      `run-shell`/`if-shell` strings (`bind S`, `M-J`/`M-K` window-nav, `bind d`
      detach, six reflow hooks, two mark-seen hooks, two `prefix + r` reload
      calls); 2 land in `#(…)` bodies in `status-format[0]`.
- [ ] `#{q:hook_session_name}` → `#{qs:…}` — **3**: `session-closed[98]`
      (scratchpad reap) and the two splash hooks.
- [ ] `#{q:pane_current_command}` → `#{qs:…}` — **1**: the
      `tmux-kill-pane-guard` arm of `bind-key x`.
- [ ] `#{q:pane_current_path}` → `#{qs:…}` — **1**: `bind Y`. This one is for
      the **empty-value** axis, not tilde — an unreadable cwd otherwise makes it
      `tmux display-message -p` with no argument, which prints the default
      message-format for `wl-copy` to copy.
- [ ] Leave **every other `#{q:}` untouched.** Do not sweep. The abandoned
      earlier attempt converted all of them, which forced repointing three
      flake grep pins and deleting six `#{?…}` wrappers; the design records why
      the narrow scope is preferred and what it costs.
- [ ] Add a one-line reason comment at the four retained sites whose safety is
      **not** evident from the value's name: `client_name`/`hook_client` (tty-
      derived, and their consumers default the positional), `@bridge_sock`
      (never word-initial — every site is `--sock=…`), `@catppuccin_flavor`
      (the `x`-prefix idiom). Say why *neither axis* reaches it. Nothing at the
      id- and integer-valued sites — the value's name carries it.
- [ ] Do **not** touch the `#{?client_name,--client #{q:client_name},}`
      wrappers. They emit the flag *and* its value together or neither;
      `#{qs:}` would emit `--client ''`, a different argv contract. The issue's
      claim that `qs:` retires them is wrong.

## Step 2: strengthen the static guard — `tests/conf-shell-quoting.bats`

The rule already **accepts** `#{qs:NAME}` (added by #367), so nothing needs
relaxing — the issue's claim that it "would reject the fix" no longer holds.
What it cannot do is hold a converted site converted, or stop a `#{qs:}` landing
where it is weaker.

- [ ] Add `WRAP_REQUIRED_FORMATS=(session_name hook_session_name
      pane_current_command pane_current_path)` and a predicate in
      `scan_shell_string`: one of these as `#{q:NAME}` in a shell string is a
      violation. Every other format keeps the existing either-form rule.
- [ ] Add the second predicate: a `#{qs:…}` **inside** a single- or
      double-quoted region of the shell string is a violation, both kinds.
      `'#{qs:x}'` — the modifier emits its own opening `'`, closing the outer
      region; `"#{qs:x}"` — `format_quote_shell_single` escapes `'` and nothing
      else, so `$` and backtick stay live where `#{q:}`'s set covers them.
      Needs quote-state tracking (`out`/`single`/`double`) as the scanner walks,
      which it does not do today. Verified tractable: a prototype tracker runs
      clean over the current emitted conf, and the one existing `#{qs:1}` is
      bare, so this predicate trips nothing today.
- [ ] Extend `check_hashparen_identity` to the same wrap-required list. It
      already names `session_name`/`hook_session_name` in `IDENTITY_FORMATS` but
      flags them only when **bare**, and two converted sites live in `#(…)`
      bodies. Keep its deliberate narrowness for the presentation-only formats —
      the design records why.
- [ ] **Update the existing fixture that this flips.** `tests/conf-shell-quoting.bats`
      has a test *"accepts a quoted identity format inside a `#(…)` shell
      format"* asserting `#{q:session_name}` in a `#(…)` body is clean. Widening
      the rule makes it fail. Change its fixture to `#{qs:session_name}` and add
      the inverse case asserting the `q:` form is now flagged.
- [ ] New fixtures in the existing `bad.conf`/`good.conf` style: an allowlisted
      format as `#{q:}` → flagged; as `#{qs:}` bare → clean; `#{qs:}` inside
      `'…'` and inside `"…"` → both flagged.
- [ ] **The two existing fixture counts must not drift.** `bad.conf` asserts
      exactly 8 violations, `good.conf` exactly 0. Neither holds an allowlisted
      format as plain `#{q:NAME}` (checked), and `good.conf`'s `bind U` carries
      `client_name`, which is not allowlisted — so both should survive. Re-run
      and confirm rather than assume.
- [ ] Update the file's header comment: the rule is no longer "either form
      everywhere".

## Step 3: the anti-hollow-green guard — a new bind-level suite

`#{qs:}` returns the **raw value** on 3.7c (measured), so a behavioural
assertion run against `pkgs.tmux` can pass for the wrong reason.

- [ ] New `tests/conf-shell-quoting-integration.bats` driven by the **pinned
      wrapper** (`TMUX_BIN`), modelled on `tests/rename-bind-integration.bats`.
- [ ] **Capability assertion, in `setup()`, before anything else**: set a known
      option, require `#{qs:}` of it to come back `'`-delimited. Measured
      `'lztmux'` on next-3.8 and bare `lztmux` on 3.7c. A hard failure, never a
      `skip` — a skip reports `ok`, the one outcome this guard exists to
      prevent (the `CONF`-unset precedent in `conf-shell-quoting.bats`).
- [ ] **Tilde mechanism assertion** — the load-bearing behavioural test. A
      session named `~/src`; a `run-shell` handing `#{qs:session_name}` to a stub
      printing its own argv; assert `ARGC=1` and `argv[1]` is literally `~/src`.
      Already proven both-capable: measured `ARGC=1 <~/src>` with `qs:` and
      `ARGC=1 </home/USER/src>` with `q:`, so it is red before the fix and green
      after.
- [ ] **Empty-value mechanism assertion** — same shape, an empty option:
      `#{q:}` gives `ARGC=0`, `#{qs:}` gives `ARGC=1 <>` (measured). This pins
      the second axis, which is why `pane_current_path` converts.
- [ ] **Characterization assertion** (format level, no shell): `#{q:}` of
      `~/src` comes back with the tilde unescaped. Comment it so a future
      failure reads as "upstream widened `q:`'s escape set — revisit the
      conversion", not "the test broke". Precedent: `rename-bind-integration.bats`
      keeps its `~`/brace fixtures as exactly this kind of sentinel.
- [ ] **No end-to-end assertion through a live consumer.** Two candidates were
      probed and both failed their positive control, because each needs an
      attached client: `mark-seen` (the `unseen` flag never clears even for a
      plain session name on a detached server) and reflow (`@reflow_key` never
      stamps for either name). A test that is red for a harness reason proves
      nothing, so the mechanism assertions carry the guarantee instead. If a
      client-attached end-to-end is added later it **must** ship a positive
      control, per this suite's model.
- [ ] Wire `conf-shell-quoting-integration-tests` into `flake.nix` beside
      `rename-bind-integration-tests`:
      `TMUX_BIN = "${tmuxConfig.tmux-wrapped}/bin/tmux"`,
      `LANG`/`LC_ALL = "C.UTF-8"`, copy `tests/`, set `HOME`.

## Step 4: confirm the static assertions elsewhere still hold

- [ ] **Verified already, do not churn:** `flake.nix` greps only `q:@bridge_pane`
      and `q:window_id`, both retained, and no file outside
      `config/tmux.conf.nix` and the docs names any of the four converted
      formats **except** the `conf-shell-quoting.bats` fixture handled in
      Step 2. Re-run the grep after Step 1 to confirm nothing new appeared.
- [ ] `tests/tmux-next38-readiness.bats` pins `list-keys` output containing
      `#{q:@bridge_pane}` and `#{qs:1}` — both retained. Verify, don't assume.

## Step 5: gates

- [ ] `nix build .#default`
- [ ] `nix flake check`
- [ ] `nix build .#lint`
- [ ] `shellcheck` the new bats file; add the `SC2088`/`SC2016` disables the
      literal-`~` fixtures need, as `rename-bind-integration.bats` does.

## Step 6: commit and PR

- [ ] Commit this plan and the design doc with the code, same PR (repo
      convention).
- [ ] PR body: `Closes #379`; the corrected scope (tilde **and** empty value,
      not tilde-and-brace — braces are escaped on the shipped tmux); the
      retained-`q:` reasoning; the 3.7 regression as an accepted, documented
      cost that applies to the converted sites too. Note the `bind Y`
      double-expansion found and deliberately not fixed. Do **not** overclaim
      severity.
