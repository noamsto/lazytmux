# Decomposition — #320 sixel relay through the remote bridge

Governing document: `docs/superpowers/specs/2026-09-07-bridge-sixel-relay-design.md`
(R1–R9, evidence table, non-goals, cross-repo contract). Everything below is a
split of that spec into independently verifiable components; nothing here
re-opens a requirement. Where a component's boundary is drawn to *enforce* a
requirement rather than merely implement it, the note says so.

Two facts found in the code that the split is built around:

- **The current overflow drop is not the whole invariant.** `Scanner.Feed`
  discards the *held prefix* of a partial sixel on `maxPartial` overflow and
  then rescans from the next byte. A real sixel body carries no ESC, so every
  byte of it after the drop point is emitted as `Literal` and lands on the
  mirror pane as printable text — the #319 symptom, at a size (>64 KiB) the
  existing tests never reach. R2's stated invariant ("only a sequence the
  scanner has seen a terminator for is ever forwarded") therefore needs a
  discard-to-terminator state, not just a prefix drop. That state is
  unconditional partial handling and lives in the safety component.
- **The frame cap is a leaf concern.** `wire.maxFrameSize` is unexported, and
  `render/renderer.go` writes `FrameSeed`/`FrameOutput` payloads verbatim and
  in order. Splitting a stream payload across frames is byte-identical at the
  pty, so the split belongs in `wire` as a helper the sink calls — the graphics
  package never learns what a frame is, and the daemon never learns the cap.

## components

### C1 `scanner-raster-hold`

Purpose: make the scanner emit a *complete* bare sixel as its own chunk kind
and make every partial-raster outcome a drop, with no policy input at all.

- `boundaries`
  - may-touch: `picker/remotebridge/graphics/scan.go`, `seq.go`,
    `scan_test.go`, `seq_test.go`
  - must-not-touch: `proxy.go`, `rewrite.go`, `coalesce.go`, `fetch.go`,
    anything under `daemon/`, `wire/`, `cmd/`
- `risk`: **high** — this is the #319 invariant. Also a performance edge:
  `Feed` today re-copies the whole held buffer on every call
  (`append(s.held, p...)` then `append([]byte(nil), buf...)`); at a
  multi-megabyte hold delivered in kilobyte `%output` lines that is quadratic
  memcpy per image and must not be inherited.
- Notes that the boundary enforces:
  - The scanner's only new input is an integer hold budget for raster
    sequences. It has **no** relay flag, no capability value, no policy
    branch. Every code path that handles a partial raster (budget overflow,
    `Flush`, and the post-overflow tail up to the terminator) ends in a drop
    regardless of the budget's value; the budget decides *when*, never
    *what*. A component that may not edit these files cannot weaken partial
    handling — that is the structural guarantee the dispatcher asked for.
  - A complete bare sixel becomes `Chunk{Raster: …}` (bytes verbatim, ST
    included). The scanner no longer "drops" a complete bare sixel; it
    reports it. What happens to a reported one is C3's decision.
  - A **passthrough-wrapped** sixel (`\ePtmux;` around `\ePq…`) keeps today's
    drop. The wrapper routes the local tmux to `tty_cmd_rawstring`, the
    unpositioned/unclipped mechanism the spec rules out; aeye's raster path
    emits bare, so nothing legitimate is lost. Widening this is a spec
    change, not a plan choice.
  - `TestScanDropsSixel`'s safety assertion moves up one layer (to C3) for the
    complete case; this component must keep and extend the scanner-level
    assertions that no byte of a partial sixel — prefix, held remainder, or
    post-overflow tail — ever appears in a `Literal`.
  - `Coalesce` sees `Raster` chunks as `Seq == nil` and skips them; no edit.
  - `keyneg` runs upstream in the pump and forwards sixel regions verbatim
    (`classifyDCS` → `walkRegion`); its `maxRegion` re-arm cannot match a
    query inside a sixel body (no ESC there). No change to `keyneg`. PR #550
    is already merged (`b648e7a`), so there is no in-flight conflict.

### C2 `osc1337-drop`

Purpose: R3 — recognise `\x1b]1337;File=` and drop it whole or partial,
accepting BEL as well as ST; leave every other OSC 1337 verb forwarding
verbatim.

- `boundaries`
  - may-touch: `picker/remotebridge/graphics/scan.go`, `seq.go`,
    `scan_test.go`, `seq_test.go`; one log line in `proxy.go` for the drop
    counter
  - must-not-touch: `rewrite.go`, `coalesce.go`, `fetch.go`, `daemon/`,
    `wire/`, `cmd/`
- `risk`: **medium** — scope addition (the spec argues it as deliberate). Its
  own hazard is the terminator set: an OSC hold that waits for ST alone
  swallows real pane text up to the budget, which is strictly worse than
  today. Per spec R3 the set is **ST, BEL and CAN/SUB** (tmux maps CAN/SUB out
  of an OSC but deliberately not out of a DCS, so sixel still ends on ST
  alone; `keyneg`'s `isCancel` is the in-repo precedent). The undecided-prefix
  rule is part of the contract, not an optimisation: the discriminator is
  **12** bytes (`\x1b]1337;File=`, verified by `len()` — an earlier draft of
  this document said 13), and the rule that matters is the steady-state one:
  an `\x1b]` prefix at a `Feed` boundary is HELD until the discriminator
  resolves or is ruled out. Forward-verbatim applies only at `Flush`.
- Notes:
  - Unconditional — no policy input, same as C1's partial handling. Uses the
    same hold and discard-to-terminator machinery C1 builds, held at the
    **non-relay** budget (we never relay these, so no reason to spend the
    raster budget on them).
  - The drop is observable via a scanner counter the proxy logs, the
    `Malformed` precedent; that log line is the only reason `proxy.go` is in
    the may-touch list.

### C3 `relay-policy`

Purpose: R1 gate + R1 observability + the shared capability type. Decide, per
complete `Raster` chunk, forward-verbatim or drop-with-distinct-log; hand the
scanner its raster hold budget; define the one `Relay` value both consumers
read (R6).

- `boundaries`
  - may-touch: `picker/remotebridge/graphics/proxy.go`, `proxy_test.go`, a
    new `picker/remotebridge/graphics/relay.go` (+ test)
  - must-not-touch: `scan.go`, `seq.go` (policy may not reach the scanner —
    see C1), `rewrite.go`, `coalesce.go`, `fetch.go`, `daemon/`, `wire/`,
    `cmd/`
- `risk`: **medium** — this is where "relays images tmux will draw as
  `SIXEL IMAGE (WxH)` placeholder text" would come from if the gate were
  anything other than tmux's own render condition. The gate must be derived
  from the `sixel` terminal-feature token and nothing else.
- Notes:
  - `graphics.New` keeps its signature and its relay-off semantics so every
    existing proxy/daemon test stays untouched (R7 names "their present
    tests"). Relay is opted into through a second constructor (or an options
    value) — the plan picks the shape, the interface section pins what it
    must carry.
  - Relay-off drop of a complete sixel logs a reason distinct from every
    other drop (R1). A viewer repainting at frame rate makes this a per-image
    line; the plan decides once-per-proxy vs per-drop, but the line must name
    the cause (no `sixel` terminal-feature on the local client) and the raw
    termfeatures value it was derived from.
  - Raster chunks are forwarded **bare** — never `EncodeWrapped`, never
    retained for `Replay` (spec non-goal: a sixel is cursor-positioned and a
    replay after reseed would paint it in the wrong place).
  - Hands the scanner `DefaultRasterHold` (the named relay budget) when relay
    is on and the 64 KiB non-relay budget when off — "spent only on a
    sequence we intend to relay" (spec Risks).

### C4 `wire-frame-split`

Purpose: R4's "must not be written as one oversized frame", solved where the
cap lives.

- `boundaries`
  - may-touch: `picker/remotebridge/wire/protocol.go`, `wire/*_test.go`
  - must-not-touch: everything else; `maxFrameSize` stays unexported and its
    value stays 16 MiB; `WriteFrame`/`ReadFrame` keep their signatures
- `risk`: **low** — a pure function with a round-trip test (a payload above
  the cap written, read back as N frames whose concatenation is the input).
- Notes: valid for the byte-stream frame types only (`FrameSeed`,
  `FrameOutput`); the helper must refuse or be undefined for `FrameResize`,
  `FrameCtl`, `FrameCtlAck`, whose payloads are structured.

### C5 `sink-wiring`

Purpose: route every `FrameOutput`/`FrameSeed` write in the sink pump through
C4's split, and thread the relay-aware proxy into the pump unchanged.

- `boundaries`
  - may-touch: `picker/remotebridge/daemon/daemon.go` — the `outputSink`
    region only (`start`, the tail flush on channel close, the `Replay`
    write after a seed, and `seedRenderer`/`wireRenderer`'s seed write);
    `daemon.Config` gains one field; `daemon/*_test.go` for the sink
  - must-not-touch: the pump's *order* (`drainOutput` → `kn.Feed` →
    `gfx.Filter`; `gfx.Filter` then `gfx.Close` on the tail), `reseedDropped`,
    pause/resume, `reconcile*`, `graphics/`, `wire/`, `cmd/`
- `risk`: **medium** — the write sites are three and easy to miss one
  (`Replay` output, tail flush). Miss one and a large image kills the
  renderer through `ReadFrame`'s error path, which `healDeadRenderers` then
  "repairs" by rebuilding the pane — the failure R4 exists to prevent.
- Notes: with the split in `wire`, this component is a call-site change plus
  a `Config` field; it has no arithmetic of its own. That is deliberate: the
  frame-splitting logic does *not* cross into the daemon awkwardly because
  the daemon never sees a size.

### C6 `capability-publish`

Purpose: R5 — publish the same `Relay` value to the bridged remote session's
environment with a control-mode `set-environment`.

- `boundaries`
  - may-touch: a new `picker/remotebridge/daemon/relayenv.go` (command
    builder + test, the `PassthroughAllCmd` shape), the `Run()` site in
    `daemon.go` immediately beside `themeToggleAvailable`
  - must-not-touch: `sshControlArgs` / the ssh env prefix (spec: deliberately
    not that channel), `conn.go`'s repair path, `graphics/`, `wire/`
- `risk`: **low**.
- Notes:
  - **Always sent, including when the value is empty.** A previous bridge
    from a sixel-capable terminal leaves `sixel` in the remote session's
    table; a later bridge from an incapable one must overwrite it, not skip.
    Empty means "none" under the grammar, so an empty set is the correct
    overwrite.
  - Once per `Run()`, like the theme probe: a reconnect is identity-verified
    against the same server (`conn.go`), whose session table persists, so the
    repair path needs no re-send. Not sent from repair.
  - Session-scoped, so only panes created after the bridge attaches inherit
    it. The carousel is always such a split (CLAUDE.md, Bridge Graphics), so
    that is sufficient; existing remote shells will not see it — a stated
    limitation, not a bug.
  - Runs under `--test-local` too (the src server is a real tmux), which is
    what makes it assertable in C10.

### C7 `daemon-flags-and-relay-only-proxy`

Purpose: the daemon's edge — accept the raw termfeatures and the relay
budget, compute the `Relay` value **once** into `Config`, feed it to both
`NewGraphics` and the publish, and put a real proxy in the pump on every
transport (R8).

- `boundaries`
  - may-touch: `picker/remotebridge/cmd/daemon/main.go` (flag table,
    `NewGraphics` closure, `Config` literal)
  - must-not-touch: `Rewrite`'s body, `sshControlArgs`, `--test-local`'s
    transport construction, `daemon.go`
- `risk`: **medium** — it is the composition point where R6's "computed once"
  becomes true, and it is what puts a proxy on transports that have never had
  one.
- **Superseded**: an earlier draft of this document proposed an
  `IdentityLocalizer` here. Spec revision 2 rules that out under R7: a proxy
  is not only a Localizer, and placing a full one on a same-machine transport
  would change four kitty behaviours no identity localisation neutralises —
  `t=s` dropped though shared memory is reachable there, `t=t` rewritten to
  `t=f` leaking the sender's own temp file, a **bare** APC gaining a
  `\ePtmux;` wrapper, and `Coalesce` discarding stores that reach the
  terminal today.
- **Resolution (spec R7): a proxy built with no Localizer is relay-only.** It
  applies the raster policy (R1/R3/R10) and nothing else: kitty sequences are
  forwarded byte-identically and `Coalesce` does not run. So `NewGraphics`
  returns a relay-only proxy when `ctlSock == ""` and the full localising
  proxy otherwise, and the kitty path on `--test-local` / `-ssh ""` is
  byte-for-byte what it is today. There is no `t=t` leak to accept and no
  bats sweep for kitty-APC assertions to do.

### C8 `launcher-termfeatures-read`

Purpose: read the invoking client's `#{client_termfeatures}` (and fix the
adjacent bare `#{client_termname}` read the spec calls out) and hand both to
the daemon by environment.

- `boundaries`
  - may-touch: `scripts/lztmux-remote-open.sh`, the block at the
    `client_termname` read (~line 461) and the exports beside
    `LZTMUX_BRIDGE_TERM`
  - must-not-touch: `scripts/lib-remote.sh`, daemon launch lines, session
    creation, `read_session_env` calls
- `risk`: **low**. Instrument is `nix build .#lint` (shellcheck/shfmt) plus
  C7's flag default; the m2 bats suite bypasses the launcher, so there is no
  offline test of this read beyond the env-name contract.
- Notes: one `display-message -p -t "$TMUX_PANE"` for both fields, split on
  `|` — never a tab (`tmux-format-delimiter-assertions` rejects it and the
  client's locale would collapse it). The shell reduces nothing: it passes
  the raw feature list and Go derives the value (C3), so the mapping is unit
  tested and the daemon cannot be handed a wider capability than tmux's own.

### C9 `nix-sixel-declaration`

Purpose: R1's declaration — a way for a lazytmux user to say "my terminal does
sixel" that emits `set -as terminal-features '<term>*:sixel'` beside the
existing `RGB:extkeys` line.

- `boundaries`
  - may-touch: `config/tmux.conf.nix` (`terminalConfig` and its call site),
    `modules/home-manager.nix` (the `startupSession.terminal` option block
    and the `terminalTerm` plumbing into `tmux.conf.nix`), `flake.nix` only
    if a conf assertion check is added
  - must-not-touch: `update-environment`, `allow-passthrough`, the
    `progressbar`/`hyperlinks` lines, any script
- `risk`: **medium** on design, low on code. `terminalTerm` is `null` unless
  the `ghostty`/`kitty` emulator preset is active, and neither of those
  speaks sixel — so a bare "sixel = true" has no term pattern to attach to and
  is inert for exactly the users it is for (foot/WezTerm/iTerm2). The plan
  must choose how the term pattern becomes declarable without a preset (new
  presets, or letting `terminal.term` feed `terminalTerm` when a feature is
  requested) and must keep the `#`-doubling and `'`-quoting conventions of
  the surrounding config.
- Instrument: `nix build .#default` (the line appears in the generated
  conf), `nix build .#lint` (alejandra/statix/deadnix).

### C10 `bats-proof`

Purpose: R8 — the offline proof, RED-first on the drop.

- `boundaries`
  - may-touch: `tests/remote-m2-integration.bats` (new tests only; existing
    tests unchanged), `flake.nix`'s `remote-m2-integration-tests` check only
    if a new input is needed
  - must-not-touch: any Go, any script, `SRC_CONF`/`DST_CONF` setup
- `risk`: **medium** — flakiness (pipe-pane is asynchronous; poll for a
  marker as the keyneg test does), and proof validity: the RED drop test must
  emit a sixel **larger than the 64 KiB non-relay budget**, or it cannot
  observe the post-overflow tail C1 exists to stop and is green against
  today's prefix-only drop.
- Notes:
  - Three assertions on one harness: (a) relay off — a sixel on the remote
    pane does not appear on the mirror pane's pty (RED today); (b) relay on
    (daemon launched with the termfeatures flag carrying `sixel`) — the same
    bytes do appear, byte-identical; (c) `show-environment` on the remote
    session reports `LZTMUX_RELAY_GRAPHICS` equal to the value that gated
    (b), and empty in (a). (a)+(b) prove the gate; (c) is R6 made observable.
  - `capture-pane` cannot see any of this (tmux's DCS parser eats a sixel);
    `pipe-pane -o` on the mirror pane is the instrument.
  - The DST server must carry the `sixel` terminal-feature for nothing here —
    the daemon gates on the flag it was given, and the bats DST has no real
    terminal. The feature line is C9's, verified by nix, not by bats.
  - Acceptance bullet 1 (a live non-kitty terminal) is **not** run and must
    be named as unproven in the PR body.

### C11 `docs`

Purpose: R9 — falsified prose fixed, spec + plan committed with the code.

- `boundaries`
  - may-touch: `CLAUDE.md` ("Bridge Graphics" — the proxy no longer drops
    complete sixel; the remote-needs table; the "What the Remote Host Needs
    on PATH" row), `docs/superpowers/plans/2026-09-07-bridge-sixel-relay.md`
    (new), the spec itself only for typos
  - must-not-touch: code
- `risk`: **low**. Instrument: `nix build .#lint` (typos).

## ordering

Arrows are hard dependencies (an interface or a shared file). `∥` marks
components with no shared files and no interface dependency between them.

```
stage 1:  C1  ∥  C4  ∥  C8  ∥  C9
stage 2:  C3            (needs C1's Chunk.Raster and the scanner hold budget)
stage 3:  C2            (needs C1's hold/discard machinery; edits the same
                          files as C1 and one line of C3's proxy.go, so it
                          follows both rather than running beside either)
stage 4:  C5  ∥  C6     (C5 needs C4 + C3's Relay type; C6 needs C3's type;
                          both touch daemon.go but in disjoint regions —
                          the sink block vs the Run() probe site)
stage 5:  C7            (needs C3, C5, C6: it is the composition point that
                          computes Relay once and hands it to both)
stage 6:  C10           (needs C7's flags to flip the gate, C6 to assert the
                          publish; C8 is not on its path — bats bypasses the
                          launcher)
stage 7:  C11           (last; documents what landed)
```

Notes on the order:

- **C2 is not independent of C1.** It is a separate component so its risk and
  its scope-addition status stay visible in review, but it reuses the hold,
  the discard-to-terminator state, and the overflow/`Flush` rules C1
  establishes, and it adds BEL to the terminator set those rules consult. It
  may be pulled forward to run directly after C1 (before C3) if the reviewer
  wants the safety half in early; it may not run beside C1.
- **C3 may not start before C1 lands**, and the two must never be one
  change: the whole enforcement of the invariant is that policy edits and
  scanner edits are reviewed as different diffs against different
  may-touch lists.
- **C4, C8, C9 are leaf work** with fixed contracts (a `wire` helper, an env
  name, a conf line) and can be done first or by anyone; nothing else waits
  on C8 or C9 except the PR itself.
- **C7 is deliberately last among the Go components.** It is where "computed
  once" becomes true — a `Relay` value built in one place and passed to both
  C3's constructor and C6's command. Landing it before either consumer
  exists would leave a value with one reader, which is how the two later
  drift.

## interfaces

Contracts that must survive the split. Names are binding unless marked
"(plan picks the name)"; shapes are binding either way.

### Graphics package (C1 / C2 / C3)

- `type Chunk struct { Literal []byte; Seq *Seq; Raster []byte }` — exactly one
  field non-nil per chunk. `Raster` is a complete bare sixel, verbatim,
  terminator included. `Coalesce` treats `Raster` as it treats `Literal`
  (skips it; `Seq == nil`), and its signature `func Coalesce(in []Chunk) []Chunk`
  is unchanged.
- `func NewScanner() *Scanner` unchanged, and `Feed(p []byte) []Chunk` /
  `Flush() []Chunk` unchanged. The scanner gains **one** configurable input,
  an integer raster hold budget (field or setter; plan picks the name), with
  the documented invariant: for a partial raster, overflow of that budget,
  `Flush`, and the remainder of the sequence up to its terminator are all
  drops, at every budget value. The scanner has no bool, string, or
  capability input. `maxPartial` stays `64 << 10` and stays the bound for
  every non-raster hold (kitty APC, other DCS, OSC 1337).
- `const DefaultRasterHold` (exported; plan states the number and justifies
  it against the 3–8 MB measurement) — the relay budget. Independent of
  `wire.maxFrameSize` by construction (C4), so it may exceed 16 MiB if the
  measurement warrants.
- OSC 1337 (C2): recognised only as `\x1b]1337;File=` (**12** bytes);
  terminated by ST (`\x1b\\`), BEL (`0x07`), or CAN/SUB; dropped whole or
  partial; an `\x1b]` prefix at a `Feed` boundary is HELD until the
  discriminator resolves or is ruled out, and forwarded verbatim only at
  `Flush`. Every other `\x1b]1337;` verb forwards verbatim, as today.
  A scanner counter (the `Malformed` precedent; plan picks the name) records
  the drops for the proxy's log.
- `type Relay struct` (C3, in `graphics`): the one capability value. Pinned
  surface:
  - `func RelayFromTermFeatures(feats string) Relay` — input is the raw
    `#{client_termfeatures}` string (comma-separated; e.g.
    `bpaste,focus,RGB,sixel,title`); sets sixel iff a `sixel` token is
    present. This is the only constructor from external input.
  - `func (r Relay) String() string` — the cross-repo grammar
    (`LZTMUX_RELAY_GRAPHICS` below), canonical: `sixel` or the empty string
    today. What the daemon publishes is exactly this string of exactly the
    value that gates.
  - a predicate for "relays sixel" the proxy gates on (plan picks the name).
- `func New(loc Localizer, logf func(string, ...any)) *Proxy` — frozen:
  signature and relay-off semantics unchanged, so every existing test in
  `graphics/` and `daemon/` compiles and passes untouched (R7). Relay is
  enabled through a second constructor or an options value (plan picks the
  shape) that carries a `Relay` and a raster hold budget. `Filter`, `Replay`,
  `Close` signatures unchanged; `Replay` never includes raster bytes.
- `type Localizer interface { Localize(ctx, remotePath) (localPath, error) }`
  and `Rewrite`'s signature and body are frozen (R7). There is no identity
  Localizer: a `Proxy` whose Localizer is nil is **relay-only** — it applies
  the raster policy and forwards kitty sequences byte-identically, skipping
  `Rewrite`, `Coalesce`, `EncodeWrapped` and `retain`. To forward verbatim it
  needs the sequence's original bytes, so a `Seq` chunk must carry them
  (a `Raw` field on `Chunk`, or equivalent — the plan picks the shape).

### Wire (C4 / C5)

- `func WriteFrame(w io.Writer, t FrameType, payload []byte) error` and
  `func ReadFrame(r io.Reader) (Frame, error)` unchanged; `maxFrameSize`
  stays unexported and `16 << 20`.
- A new stream writer (plan picks the name; shape:
  `func(w io.Writer, t FrameType, payload []byte) error`) that emits `payload`
  as one or more frames each strictly below the cap, in order, valid for
  `FrameSeed` and `FrameOutput` only. The sink (C5) uses it at every
  seed/output write site — steady-state output, the tail flush on channel
  close, the post-seed `Replay` write, and the seed itself — and nowhere else
  in the daemon does a size decision appear.

### Daemon (C5 / C6 / C7)

- `daemon.Config` gains `Relay graphics.Relay` (value type; zero value = no
  relayable capability). Read by `Run()` for the publish and by whoever
  builds the proxy — but the proxy is built in `main.go`'s `NewGraphics`
  closure, which captures the *same* value it stored in `Config`. There is one
  assignment of that value in the daemon binary.
- `NewGraphics func(paneID string) *graphics.Proxy` keeps its type; after C7
  it returns a non-nil proxy on **every** transport: the ssh fetcher when
  `ctlSock != ""`, `graphics.IdentityLocalizer{}` otherwise (`--test-local`,
  `-ssh ""`). `Config.NewGraphics == nil` remains the way *tests* disable
  proxying; it is no longer what `main.go` produces.
- Publish command (C6; plan picks the Go name, `PassthroughAllCmd` shape):
  `set-environment -t '<session>' LZTMUX_RELAY_GRAPHICS '<value>'`, both
  arguments through `tmuxQuote`, `<value>` = `cfg.Relay.String()`. Sent by
  `send` once in `Run()`, at the `themeToggleAvailable` site, unconditionally
  (empty value included — it is an overwrite).
- The sink pump's order is frozen: `drainOutput` → `kn.Feed` → `gfx.Filter`;
  on close `kn.Flush` → `gfx.Filter` → `gfx.Close`; `Replay` only after a
  `FrameSeed` write. The `a=d` handling, `retain`, `reseedDropped`, and
  pause/resume are untouched (R7 and CLAUDE.md's Bridge Graphics rules).

### Flags and environment (C7 / C8)

- `-termfeatures` (string), default `os.Getenv("LZTMUX_BRIDGE_TERMFEATURES")`,
  documented beside `-term`: the raw `#{client_termfeatures}` of the client
  that will paint. The daemon derives `Relay` from it; it accepts no
  pre-reduced capability, so nothing wider than tmux's own render condition
  can be passed in.
- `-gfx-relay-max-bytes` (int64), default `graphics.DefaultRasterHold`,
  documented beside `-gfx-max-bytes`: the raster hold budget handed to the
  proxy when relay is on.
- Shell → daemon handoff (C8): `lztmux-remote-open` exports
  `LZTMUX_BRIDGE_TERMFEATURES` beside `LZTMUX_BRIDGE_TERM`, both read from the
  invoking client with `tmux display-message -p -t "$TMUX_PANE"` using a
  `|`-delimited two-field format (`#{client_termname}|#{client_termfeatures}`),
  never a bare `display-message` and never a tab. Empty (no client, no
  `$TMUX_PANE`) is a valid value meaning no capability — the same contract
  `LZTMUX_BRIDGE_TERM` already has.

### tmux configuration (C9)

- Generated line: `set -as terminal-features '<term>*:sixel'`, emitted beside
  (or merged into) the existing `set -as terminal-features
  '<term>*:RGB:extkeys'` line in `config/tmux.conf.nix`'s `terminalConfig`,
  under whatever option the plan chooses in `modules/home-manager.nix`'s
  `startupSession.terminal` block. `<term>` must be resolvable for a user
  with no emulator preset. Nothing else in the conf changes: `allow-passthrough`
  stays as it is (both ends, per CLAUDE.md) because this change neither uses
  nor weakens it.

### Cross-repo contract (binding; from the spec, restated so it is not lost)

- **Variable:** `LZTMUX_RELAY_GRAPHICS`, in the bridged remote **session**
  environment (`tmux show-environment -t <session>` shows it; panes created
  after the bridge attaches inherit it).
- **Grammar:** a comma-separated set of protocol tokens the *local* terminal
  can paint. Exactly one token is defined: `sixel`. The list shape is for
  additive growth, not a promise of more tokens now.
- **Absent, empty, or unrecognised** all mean no relayable raster capability.
  An unrecognised token is never permission.
- **Producer:** the daemon, from `graphics.Relay.String()`, of the same value
  that gates the local relay — R6 is this sentence.
- **Consumer (noamsto/aeye#186, out of repo):** `chooseRelayBackend` reads it;
  `sixel` present → `backendRaster` with `formatSixel`; never `formatITerm` on
  the relay path. Until that lands, this PR is inert but correct.

### Test instruments per component

| Component | Instrument |
|---|---|
| C1, C2, C3, C4 | `go test` in `picker/` (`picker-go-tests` check) — `graphics/`, `wire/` |
| C5, C6, C7 | `go test` in `picker/` — `daemon/` sink and command-builder tests; `cmd/daemon` flag-default test |
| C8 | `nix build .#lint` (shellcheck, shfmt); env-name contract via C7's flag default |
| C9 | `nix build .#default` (generated conf carries the line), `nix build .#lint` |
| C10 | `nix flake check` → `remote-m2-integration-tests` (`tests/remote-m2-integration.bats`) |
| C11 | `nix build .#lint` (typos) |
