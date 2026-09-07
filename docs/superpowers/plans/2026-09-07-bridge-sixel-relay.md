# Plan — #320: relay sixel through the remote bridge

Implements [the design spec](../specs/2026-09-07-bridge-sixel-relay-design.md)
(R1–R10) along the split in `2026-09-07-bridge-sixel-relay-decomposition.md` (C1–C11). Every step below maps
to exactly one component and stays inside its `may-touch` list.

The order is the decomposition's dependency order with **one sanctioned
rearrangement**: C1 stands alone and C2 runs before C3, which
`2026-09-07-bridge-sixel-relay-decomposition.md` explicitly permits ("[C2] may be pulled forward to run
directly after C1 (before C3)"). Everything else follows its stages.

**Where `2026-09-07-bridge-sixel-relay-decomposition.md` and the spec disagree, the spec governs.** There is
one such place: the decomposition's `interfaces` section still names
`graphics.IdentityLocalizer{}`, which spec R7 (revision 2) rules out in favour
of a relay-only proxy. Step 9 follows the spec. Do not "fix" it back.

Two things this change is, which the issue title does not say:

1. **A bug fix, not only a feature.** The scanner's overflow arm drops a partial
   sixel's held prefix and leaves `s.held` nil, so the sequence's tail — which
   carries no ESC — is emitted as `Literal` on the next `Feed` and painted as
   pane text. Confirmed by experiment against the current tree. The smallest
   real chafa sixel measured is 478 KB against a 64 KiB budget, so this fires on
   every real image: #319's protection is already broken for real payloads.
2. **Sixel is forwarded bare, never re-wrapped.** The issue proposed a `\ePtmux;`
   passthrough. tmux's own `tty_cmd_sixelimage` clamps to the pane and positions
   the cursor; `tty_cmd_rawstring` (the passthrough sink) does neither. See the
   spec's evidence table.

## Stage 1 — leaves (parallel-safe)

- [ ] **Step 1: `wire` gains a frame-splitting stream writer** (C4)
      In `picker/remotebridge/wire/protocol.go`, add `wire.WriteStream` — a
      writer that emits a payload as one or more frames each strictly below
      `maxFrameSize`, in order. `maxFrameSize` stays unexported and stays `16 << 20`;
      `WriteFrame`/`ReadFrame` keep their signatures. Valid for `FrameOutput`
      and `FrameSeed` only — `FrameCtl` carries NUL-separated argv and
      `FrameResize`/`FrameHello` structured payloads, and splitting those
      corrupts them; the helper must reject or be undefined for them.
      Test: a payload above the cap round-trips as N frames whose concatenation
      is the input, byte-identical; a payload below the cap emits exactly one
      frame.

- [ ] **Step 2: launcher reads the invoking client's termfeatures** (C8)
      In `scripts/lztmux-remote-open.sh`, replace the bare
      `tmux display-message -p '#{client_termname}'` with a single
      `-t "$TMUX_PANE"`-targeted call reading `#{client_termname}|#{client_termfeatures}`
      — `|`-delimited, never a tab (`tmux-format-delimiter-assertions` rejects a
      tab, and a non-UTF-8 client locale collapses it). Export
      `LZTMUX_BRIDGE_TERMFEATURES` beside the existing `LZTMUX_BRIDGE_TERM`.
      Pass the **raw** feature list; the shell reduces nothing (R6).
      Empty — no client, no `$TMUX_PANE` — is a valid value meaning "no
      capability", the contract `LZTMUX_BRIDGE_TERM` already has.
      Fixing the missing `-t` is in scope: it is the same read this step
      depends on, and the file already documents the bug 15 lines below.
      The script runs under `set -u`, so build the `-t` argument with the
      guarded-array pattern the file already uses twice (`cur_target=()` at the
      `COLORTERM` read), never a bare `"$TMUX_PANE"` interpolation.
      Verify: `shellcheck` + `shfmt` clean.

- [ ] **Step 3: a declarable sixel terminal-feature** (C9)
      The option is a **list of TERM strings** (a `*` suffix is appended to
      each, the house convention at `'${terminalTerm}*:RGB:extkeys'`), not a
      boolean and not globs — a user writing `"foot*"` would otherwise get
      `foot**`. `programs.lazytmux.sixelTerminals` (default `[]`), each entry
      emitting `set -as terminal-features '<term>*:sixel'` beside the existing
      `RGB:extkeys` line in `config/tmux.conf.nix`.
      **Placement note:** this is a *top-level* option, outside C9's stated
      may-touch region (`the startupSession.terminal option block and the
      terminalTerm plumbing`). Taken deliberately, and flagged here rather than
      silently: the declaration is not startup-session state — it describes the
      terminal that paints, and the conf line it drives is emitted whether or not
      `startupSession` is enabled. The module already carries top-level scalars
      (`processIcons`, `prefix`, `defaultShell`, …), so the placement is
      conventional.
      A boolean was rejected. It would have to attach to a term string, and both
      candidates fail: `terminalTerm` is null unless a `ghostty`/`kitty` preset
      is active — neither of which speaks sixel — so a boolean hung off it is
      inert for exactly the foot/WezTerm/iTerm2 users this is for; and
      `startupSession.terminal.term` is `types.str` **defaulting to
      `"xterm-256color"`**, so it is never unset and any "did the user set it?"
      guard is vacuous. A pattern list has no null case to detect and no default
      to mistake for a choice — the error is designed out rather than asserted
      against.
      Keep the surrounding `#`-doubling and `'`-quoting conventions, **and the
      join convention**: `terminalConfig` emits `"…\n    "` so the interpolation
      at its call site keeps the generated conf's indentation; N entries must
      reproduce that or the conf comes out ragged. A pattern containing `'`
      would break the single-quoted line — the same exposure `extraConfig`
      already has, so note it in the `mkOption` description rather than adding an
      escape pass.
      Verify: `nix build .#lint`, plus a **new conf assertion check** in
      `flake.nix` in the `float-conf-assertions` shape (C9's may-touch allows
      `flake.nix` "only if a conf assertion check is added") that imports
      `tmux.conf.nix` with the option set and greps for the emitted line.
      `nix build .#default` alone cannot verify this: `flake.nix` imports
      `tmux.conf.nix` with no terminal options at all, so `terminalConfig` is
      already empty there and the check would be green whether or not the line
      is ever emitted.

## Stage 2 — the safety layer

- [ ] **Step 4: scanner reports a complete sixel and drops every partial**
      (C1) *(implement: escalated)*
      `picker/remotebridge/graphics/{scan.go,seq.go}` only — this step may not
      touch `proxy.go`. That boundary is the enforcement: policy lives one layer
      up and structurally cannot weaken partial handling.
      - `Chunk` gains a raster field carrying a **complete bare sixel verbatim**,
        terminator included. The scanner no longer decides its fate.
      - **`Chunk` also gains `Raw []byte`** — the sequence's verbatim input
        bytes — set for every `Seq` chunk. Step 6's relay-only mode needs it to
        forward a kitty sequence byte-identically; without it that mode would
        re-encode, and `EncodeWrapped` would wrap a **bare** APC, which is
        exactly the R7 regression the relay-only resolution exists to prevent.
        This field is owned here because Step 6 may not touch `seq.go`/`scan.go`.
      - The scanner's only new input is an **integer hold budget**. No bool, no
        capability, no policy branch. Overflow, `Flush`, and the post-overflow
        tail all end in a drop at *every* budget value; the budget decides when,
        never what.
      - **Fix the tail leak (R10).** On overflow, enter a discard state and
        consume until the next `\x1b`, which re-arms normal scanning; if that
        ESC is the sequence's ST, consume it as the terminator. A byte-budget
        re-arm is wrong: re-arming into *forwarding* drops back into the leak.
        The wrapped (`\ePtmux;`) partial leaks identically and must be covered —
        its outer terminator is a lone `\e\\` while payload ESCs are doubled.
      - **Make the hold amortised O(1) per byte.** Replace
        `s.held = append([]byte(nil), buf...)`: the defensive copy is needed only
        when the held buffer aliases the caller's `p`; when the scanner already
        owns the backing array it must retain it. At a 3–8 MB hold delivered in
        kilobyte `%output` lines the current re-copy is quadratic memcpy on the
        pane's own pump goroutine.
      - `maxPartial` stays `64 << 10` and stays the bound for every non-raster
        hold. A passthrough-wrapped *complete* sixel keeps today's drop (it
        routes to `tty_cmd_rawstring`, the mechanism the spec rules out).
      Tests: no byte of a partial sixel — prefix, held remainder, or
      post-overflow tail — ever appears in a `Literal`; the tail-leak case
      specifically (RED against current code); a complete sixel is reported
      verbatim; the discard state exits at the next ESC and does not latch;
      existing `TestScanDropsSixel` cases updated where the complete-sixel
      assertion moves up a layer.

## Stage 3 — the second protocol

- [ ] **Step 5: recognise and drop `OSC 1337;File=`** (C2)
      Same files as Step 4 plus one log line in `proxy.go` for the counter.
      Reuses Step 4's hold and discard machinery, held at the **non-relay**
      budget — these are never relayed.
      - Discriminator is `\x1b]1337;File=`, **12 bytes** (verified by `len()`).
        An `\x1b]` prefix at a `Feed` boundary is **held** until the
        discriminator resolves or is ruled out; forward-verbatim applies only at
        `Flush`.
      - **Four exits: ST, BEL, CAN/SUB, and a bare ESC.** ST and BEL are
        terminators and are consumed; CAN/SUB are aborts and are consumed; a
        bare ESC is **not** consumed — it introduces whatever follows.
        `keyneg`'s `regionAPC`/`regionOSC`/`isCancel` is the in-repo statement of
        this rule. Sixel, being DCS, still ends on ST alone.
      - Every other `\x1b]1337;` verb (`CurrentDir=`, `SetUserVar=`,
        `RemoteHost=` — routine shell-integration output) keeps forwarding
        verbatim.
      - The drop is counted on the scanner as `InlineImage` (the `Malformed`
        precedent) and logged by the proxy in Step 6.
      Tests: a complete `File=` is dropped; a partial one is dropped, not
      leaked; each of the four exits; a BEL-terminated `CurrentDir=` is
      forwarded untouched (the regression this scoping exists to prevent).

## Stage 4 — policy

- [ ] **Step 6: the relay gate and the one capability value** (C3)
      `proxy.go` + a new `relay.go`; this step may not touch `scan.go`/`seq.go`.
      - `RelayFromTermFeatures(feats string)` takes tmux's **raw**
        `#{client_termfeatures}` (e.g. `bpaste,focus,RGB,sixel,title`) and sets
        sixel iff a `sixel` token is present. It is the only constructor from
        external input, so nothing wider than tmux's own render condition can be
        injected. `String()` renders the cross-repo grammar (`sixel`, or empty).
      - **`const DefaultRasterHold = 16 << 20`** (16 MiB) lives here, and is the
        default for Step 9's `-gfx-relay-max-bytes`. Justification: the largest
        measured chafa sixel is 7.64 MB (240x60 cells), so this is a little over
        2x the worst case actually observed, and payload size scales with cell
        count so a larger terminal needs the headroom. It is deliberately
        **independent of `wire.maxFrameSize`** — Step 1's split means a held
        payload never has to fit in one frame — so the two constants being equal
        is a coincidence, not a constraint.
      - The predicate the proxy gates on is `Relay.Sixel()`.
      - **The relay-enabling constructor is
        `func NewRelay(loc Localizer, logf func(string, ...any), rel Relay, hold int64) *Proxy`.**
        `loc == nil` is relay-only mode. `graphics.New` stays the two-arg
        relay-off form so every existing `graphics/` and `daemon/` test compiles
        untouched. Naming it here is load-bearing: it is defined in this step and
        called in Step 9, which may not touch `graphics/`.
      - `Filter` forwards a raster chunk **bare** when the gate allows, and drops
        it with a distinct log line otherwise. Never `EncodeWrapped`, never
        retained for `Replay` — a sixel is cursor-positioned, so replaying one
        after a reseed would paint it in the wrong place.
      - The relay-off log fires **once per pane**, not once per sequence (a
        viewer repaints at frame rate), names the cause, and quotes the raw
        termfeatures it was derived from.
      - **A proxy with no Localizer is relay-only** (R7): raster policy only,
        kitty sequences forwarded **byte-identically**, `Coalesce` not run. It
        forwards verbatim from `Chunk.Raw`, which Step 4 adds. This is what lets a proxy exist on a same-machine transport without
        changing the kitty path — no `t=s` drop, no `t=t`→`t=f` temp-file leak,
        no bare APC gaining a wrapper, no coalescing.
      - `graphics.New` keeps its signature and its relay-off semantics, so every
        existing `graphics/` and `daemon/` test compiles and passes untouched.
      - **One exception to "byte-identical", state it in the doc comment:** a
        kitty APC the scanner classifies `dropMalformed` is consumed and emits
        no chunk, so relay-only mode does not forward it either. That is
        deliberate (it is the #319-class protection) and is why the
        byte-identical test must use a **well-formed** APC.
      Tests: gate on/off against the same input; the byte-identical kitty
      assertion in relay-only mode (well-formed APC, bare **and** wrapped, plus
      a `t=s` and a `t=t` that must pass through unchanged);
      `RelayFromTermFeatures` table including a `sixel`-free list and a token
      that merely *contains* "sixel" as a substring; a >64 KiB sixel survives
      `keyneg.Feed` → `Filter` byte-identically (`keyneg.maxRegion` is also
      64 KiB, so `walkRegion` leaves the DCS region mid-body and rescans the
      rest as literal — benign, because a sixel body holds no ESC, but the relay
      path depends on it and nothing pins it today).

## Stage 5 — daemon (parallel-safe pair)

- [ ] **Step 7: route every sink write through the splitter** (C5)
      - **`daemon.Config` gains `Relay graphics.Relay`** (value type; zero value
        = no relayable capability). This step owns that field because Step 8 is
        scoped to `relayenv.go` + the `Run()` site and Step 9 to
        `cmd/daemon/main.go`, so without it both fail to compile: Step 8 reads
        `cfg.Relay.String()` and Step 9 writes it in the `Config` literal.
      `daemon.go`'s `outputSink` region only, plus that one `Config` field.
      There are **three**
      `wire.WriteFrame` call sites in the pump covering four logical paths, and
      one of them is polymorphic — that is the trap:
      - the tail flush on channel close (`FrameOutput`) — split;
      - the post-seed `Replay` write (`FrameOutput`) — split;
      - the main `WriteFrame(conn, f.typ, f.payload)`, which carries
        `FrameOutput` **and** `FrameSeed` **and** `FrameResize`. Split only the
        two byte-stream types here: splitting a `FrameResize` corrupts it.
      The fourth `WriteFrame` in the file is the `FrameCtlAck` write outside the
      pump; it is structured and must not be touched.
      Missing a site turns a large image into a `ReadFrame` error, which kills
      the renderer — the dead-pane path `healDeadRenderers` exists to repair,
      i.e. the failure R4 exists to prevent. The pump's order is frozen:
      `drainOutput` → `kn.Feed` → `gfx.Filter`, and `kn.Flush` → `gfx.Filter` →
      `gfx.Close` on teardown.
      Test: a `FrameResize` still round-trips unsplit, and a modest
      multi-frame `FrameOutput` arrives byte-identical. The **oversized**
      (>16 MiB) case stays at the wire level in Step 1: `maxFrameSize` is
      unexported and fixed, so a daemon-level test would have to push >16 MiB
      through the sink's conn, and a pump can park inside `wire.WriteFrame`
      (`reconcilezoom_test.go` documents exactly that) — a write-then-read test
      would deadlock rather than fail.

- [ ] **Step 8: publish the capability to the remote session** (C6)
      A new `daemon/relayenv.go` in the `PassthroughAllCmd` shape emitting
      `set-environment -t '<session>' LZTMUX_RELAY_GRAPHICS '<value>'`, both
      arguments through `tmuxQuote`. Sent once per `Run()` at the session-scoped
      setup point; not re-sent on repair (a reattach is identity-verified against
      the same server, whose session table persists).
      - **Always sent, including when the value is empty.** A previous bridge
        from a sixel-capable terminal leaves `sixel` in that session's table, and
        skipping the write would let it stand and make the remote emit megabytes
        we then drop.
      - **Unset on teardown**, in `Run`'s `teardown` closure and **before
        `hold.close()`** — that call drops the control connection, and after it
        `send` fails closed, so an unset placed later never leaves the process.
        This edit is outside C6's stated may-touch region (`relayenv.go` + the
        `Run()` setup site); it is taken deliberately because spec R5 requires
        the withdrawal and no other component may make it. Noted here rather
        than silently.
        The variable describes the *local* terminal of a bridge that no longer
        exists: a stale `sixel` read by someone later attaching to that remote
        session **directly**, from a terminal with no sixel, reproduces the #319
        symptom on a screen the bridge is not part of.
      - **Be honest about when it actually fires.** The dominant teardown is
        SIGTERM (`prefix + d` → `lztmux-remote-detach`), and the daemon's signal
        handler closes `stop` and then kills the transport *before* `Run`
        reaches `teardown` — so `send` fails closed there and the unset does
        **not** land. It lands on `Run`'s own early-return teardowns, where the
        connection is still alive. SIGKILL never runs teardown at all.
        Reordering the signal handler to send first was rejected as out of
        proportion: it would restructure shutdown for a residue the next
        bridge's unconditional write already corrects. So the honest statement —
        and what goes in the PR — is: **the stale value is corrected by the next
        bridge's write, and a direct attach in the gap can read it.** The plan
        does not claim the unset closes that window.
      Tests: command shape; empty-value case; the unset on teardown.

## Stage 6 — composition

- [ ] **Step 9: daemon flags, and a proxy on every transport** (C7)
      `cmd/daemon/main.go` only.
      - `-termfeatures` (default `LZTMUX_BRIDGE_TERMFEATURES`), documented beside
        `-term`; `-gfx-relay-max-bytes` (default `graphics.DefaultRasterHold`),
        documented beside `-gfx-max-bytes`.
      - Build the `Relay` value **once**, store it in `Config`, and let the same
        value reach both `NewGraphics` and the publish. One assignment in the
        binary — that is R6.
      - **`NewGraphics` calls `graphics.NewRelay` on every transport**, passing
        `cfg.Relay` and `-gfx-relay-max-bytes` in **both** branches. The only
        difference between them is the Localizer: `NewSSHFetcher(...)` when
        `ctlSock != ""`, `nil` otherwise (`--test-local`, `-ssh ""`).
        **The relay value is never what distinguishes the branches.** The
        existing call site reads `graphics.New(graphics.NewSSHFetcher(...), logf)`,
        and leaving it as-is — the natural reading of "the localising proxy when
        `ctlSock != \"\"`" — would ship sixel relay working *only* under
        `--test-local`, i.e. the whole user-facing feature dead on the only
        transport a real user has. Step 10 runs `--test-local` exclusively and
        would not catch it.
      - `Config.NewGraphics == nil` stays the way *tests* disable proxying.
      Test (this step's only guard against the above): a table assertion over
      both branches that the constructed proxy's gate is on when
      `Relay.Sixel()` is true and off when it is not — the ssh branch will never
      have another.

## Stage 7 — proof and prose

- [ ] **Step 10: the offline proof** (C10)
      New tests in `tests/remote-m2-integration.bats`; existing tests unchanged.
      One harness, three assertions:
      (a) **relay off — a sixel must NOT reach the mirror pane's pty.** This is
      the RED-first assertion; verified RED against the current tree (a bare
      sixel arrives on the mirror pty today, introducer and body, because
      `gfx == nil` means nothing filters).
      (b) relay on (daemon launched with a termfeatures value carrying `sixel`) —
      the same bytes appear, byte-identical.
      (c) `show-environment` on the remote session reports
      `LZTMUX_RELAY_GRAPHICS` equal to the value that gated (b), and empty in (a).
      - **Size alone does not reach the R10 discard path**, and the plan must not
        pretend it does. `drainOutput` concatenates every queued `FrameOutput`
        before one `kn.Feed`/`gfx.Filter`, so a burst can arrive as a *single*
        `Feed` with the terminator already present — `decodeSeq` then returns a
        complete sixel and the `n == 0` overflow arm R10 fixes is never entered.
        Force the straddle: write the introducer plus >64 KiB of body, sleep past
        a pump drain, then write the tail and the ST in a second command. Say in
        the test comment that the **split**, not the size, is what reaches the
        overflow arm.
      - **Generate the payload in the pane**; `send-keys` cannot carry 64 KB as a
        literal argument. Shape:
        `printf '\033Pq'; head -c 70000 /dev/zero | tr '\0' '~'` for the first
        write, then `printf '~~~\033\\'` for the second. No `chafa` dependency —
        the assertion is about bytes crossing, not about a decodable image.
      - **Step 4's scanner unit test is the primary R10 net.** This bats test is
        the integration witness; it must not be described as the proof, because
        its timing is not fully under the test's control.
      - **Assertion (b) is vacuously green today** (nothing filters under
        `--test-local`), so it proves nothing alone. It has meaning only as (a)'s
        paired opposite on the same harness with the gate flipped. Say so in the
        test comment.
      - Instrument is `pipe-pane -o` on the mirror pane, the pattern the keyneg
        query test uses. `capture-pane` cannot see any of this — tmux's DCS
        parser eats a sixel, so it is green whether or not the bytes crossed.
      - Poll for a marker before asserting an absence, as the keyneg test does;
        `pipe-pane` writes asynchronously.
      - Observe (a) RED before making it green.

- [ ] **Step 11: documentation** (C11)
      CLAUDE.md's "Bridge Graphics" section says the proxy drops sixel; that
      stops being true. Record the relay, the gate, the bare-not-wrapped
      mechanism and why, the OSC 1337 exclusion and why, and the reseed
      limitation. Commit this plan and the spec with the code, per repo
      convention. Spec R1 also requires the PR to **name `${sock}.log`** as
      where the relay-off diagnostic lands, since a user cannot be expected to
      know that file exists — put it in the PR body and in the CLAUDE.md note.

## Verification

Per component, one instrument:

| Steps | Instrument |
|---|---|
| 1, 4, 5, 6 | `go test ./remotebridge/{graphics,wire}/...` in `picker/` |
| 7, 8, 9 | `go test ./remotebridge/{daemon,cmd/...}/...` in `picker/` |
| 2 | `nix build .#lint` (shellcheck, shfmt) |
| 3 | `nix flake check` → the new conf-assertion check, `nix build .#lint` (**not** `nix build .#default`; see Step 3) |
| 10 | `nix flake check` → `remote-m2-integration-tests` |
| 11 | `nix build .#lint` (typos) |

Full gate before push: `nix build .#default`, `nix flake check`,
`nix build .#lint` — none subsumes another (CLAUDE.md).

## Not done, and stated in the PR

- **Acceptance bullet 1 is not run.** It needs a real foot/WezTerm/iTerm2 client
  against a real bridge; none is available, and iTerm2 additionally means macOS.
  Named as unproven, not implied.
- **OSC 1337 is not relayed** — structurally impossible into a tmux pane
  (`tty_cmd_rawstring` positions and clips nothing). It gets the safety half
  instead. All three named terminals are served by the sixel path.
- **The aeye half is not in this PR.** Until `chooseRelayBackend` reads
  `LZTMUX_RELAY_GRAPHICS`, this change is inert but correct.
- **Sixel over the bridge is megabytes per repaint**, where the kitty path sends
  a short path. A residual risk, not parity.
