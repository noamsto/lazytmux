# Manifest-driven multi-agent status detection

- **Date:** 2026-07-05
- **Issue:** [#128](https://github.com/noamsto/lazytmux/issues/128)
- **Status:** Design — awaiting review (revised after adversarial spec review)

## Problem

Claude-status detection is Claude-Code-specific. State comes entirely from Claude
Code hooks writing `/tmp/claude-status/panes/<id>` via `claude-status-update`.
Consequences:

1. **Other agents get nothing.** A pane running Codex/Gemini/Cursor shows no
   status icon, no reflow label, no picker state.
2. **The interrupt gap needs a bespoke hack.** No hook fires on an Esc-interrupt,
   so `read_pane_state` reclassifies a long-quiet `processing` pane to
   `interrupted` by tailing the transcript for a marker line. That is a
   Claude-specific patch for one hole in a hook-only model.

herdr (a Rust agent multiplexer) solves the general case with *screen-manifest
detection*: it reconstructs each pane's rendered screen with a VT parser and
matches declarative per-agent rule manifests against screen regions. This spec
adapts that idea to lazytmux without owning the multiplexer.

## Goals

- Detect agent status for **any** terminal agent via declarative manifests, with
  no cooperation required from the agent.
- Emit lazytmux's **existing 8 states** into the **existing state-file model**, so
  every downstream renderer (icons, reflow, pickers, statusline, kill-guard)
  works unchanged.
- Keep the Claude common path **byte-for-byte unchanged** when its hooks are
  healthy: the merge is a strict no-op whenever the hook state is fresh.
- Event-driven, no polling of pane content, and **no per-tick fork** on the render
  hot path. (Arming issues at most one `tmux` command per agent pane, once.)

## Non-goals (v1)

- Central daemon or socket API (per-pane parser instead).
- `wait agent-status` CLI primitive.
- Removing the transcript-tail interrupt hack (the merge supersedes it where a
  parser is present; full removal is a follow-up).
- Live pane-resize tracking in the parser (see Component 1); deferred to phase 2.
- Desktop notifications / sound cues.
- Agents beyond Claude and Codex (further agents are additive manifest files).

## Background: current architecture (verified against the repo)

- State files: `/tmp/claude-status/panes/<pane_id>` (pane id sans `%`),
  `key=value` lines, keys `state|timestamp|unseen|session|transcript`.
- Eight states, fixed priority (`claude_priority_state`):
  `error > waiting > denied > compacting > interrupted > processing > done > idle`.
- Fade thresholds (`lib-claude.sh`): only `waiting|compacting|processing|done|
  error|denied` have a `CLAUDE_STALE_*` value; `idle` has none, and `interrupted`
  has none (it is a *derived* state computed in `read_pane_state`, never written
  to disk).
- Writers: `claude-status-update` (from CC hooks) only. Timestamps are bash
  `%(%s)T` — whole seconds.
- Readers: `claude-status`, `tmux-update-icons`, pickers, statusline — all via
  `read_pane_state` (`scripts/lib-claude.sh`), which also computes the fade and
  runs the interrupt reclassifier (`lib-claude.sh:75-83`).
- Cleanup sites: the inline `pane-exited` hook `rm` (`config/tmux.conf.nix:684`);
  `cleanup_stale_panes` (`claude-status-update.sh:37-70`, per-file `rm` at line 59);
  and the per-pane teardown (`claude-status-update.sh:380`).
- Go module `picker/`: on the **`charm.land/*` v2 fork** (`bubbletea/v2`,
  `lipgloss/v2`, `bubbles/v2`) plus `github.com/charmbracelet/ultraviolet` and
  `charmbracelet/x/{ansi,term,termios,windows}` (all indirect). Built via
  `picker/default.nix` with `subPackages = ["." "splash" "statusline"
  "enrichcard"]`, a `postInstall` rename per binary, and a `vendorHash`.

## Design overview

Scope **B — all agents, screen backfills stale hooks**. A per-pane Go parser reads
the pane's byte stream via `tmux pipe-pane`, maintains a headless VT screen model,
matches embedded TOML manifests, and writes a **separate** screen-state file. The
single read path (`read_pane_state`) merges hook state and screen state:
**hook wins while fresh; screen wins only when the hook is stale or absent.**

```
arm (existing 1s tick, gated on #{pane_pipe}==0)
   └─► pipe-pane -o -t %id 'agent-detect %id'
pane bytes ─stdin─► agent-detect (Go, one per piped pane, lives for pane life)
                      VT emulator: Write() → screen model + OSC title
                      debounce ~80ms → manifest match → write screen/<id> on change
                      stdin EOF (pane closed) → exit
read_pane_state: merge(panes/<id> hook, screen/<id>) → downstream renderers
```

## Components

### 1. `agent-detect` (Go, `picker/` module)

A new `main` package alongside the existing picker/splash/statusline/enrichcard
binaries.

- **Invocation:** `agent-detect <pane_id>` (id without `%`). Launched by
  `tmux pipe-pane`, so the pane's raw output arrives on **stdin**.
- **VT model — explicit dependency decision (was B1).** The parser needs a
  headless terminal emulator: feed a PTY byte stream, read back the rendered
  screen text + OSC title. This is **a new dependency**, not something the module
  already has. The delta is real: a new direct `require`, a `vendorHash`
  regeneration in `picker/default.nix`, and (per Component build-wiring below)
  new `subPackages`/`postInstall` entries.
  - **Leading candidate:** `github.com/charmbracelet/x/vt` — a purpose-built
    headless emulator whose lower deps (`x/ansi`) the module already vendors.
    Risk: it lives on the upstream `charmbracelet/x/*` line, while this module
    standardized on the `charm.land/*` v2 fork; a spike must confirm it composes
    without version skew.
  - **Evaluate first:** `github.com/charmbracelet/ultraviolet` is *already*
    vendored. If it can parse an arbitrary byte stream into a readable screen
    grid (not just composite for rendering), prefer it — zero new ecosystem
    surface. If it cannot (it is a rendering/cell primitive, not an emulator),
    fall back to the leading candidate.
  - **Fallback:** `github.com/hinshun/vt10x` (mature, dependency-light) if neither
    charm option is clean.
  - The plan phase MUST run a spike resolving this before implementation; the
    manifest engine and merge (the bulk of the work) are independent of which
    emulator wins, since they consume plain screen text + title string.
- **Screen access needed from whichever lib:** feed bytes; read plain screen text;
  read the OSC 0/2 title (Claude's braille spinner lives here); detect alt-screen
  (to skip pagers/editors).
- **Pane size (was B3 — resize handling made explicit):** read the pane size
  **once at startup** via a single `tmux display -p -t %id '#{pane_width}
  #{pane_height}'` (one fork at arm time — not on the render hot path, consistent
  with the fork-free goal). **v1 does not track live resize**: the parser keeps
  its startup dimensions for the pane's life. To stay resize-tolerant, matching
  avoids width-sensitive constructs — it prefers the width-independent `title`
  region and `contains`/`regex` over the whole screen with soft-wrapped lines
  joined into logical lines before matching. Live resize (a tmux
  `after-resize-pane` hook signaling parsers with new dims) is a documented phase-2
  improvement.
- **Loop:**
  1. Read a chunk from stdin. On EOF → run a final match, then exit 0.
  2. Feed the chunk to the emulator.
  3. Mark dirty; when no further bytes arrive for the **debounce window (~80 ms)**,
     snapshot screen + title and run the manifest.
  4. If the resulting state differs from the last-written state, atomically write
     `screen/<pane_id>` (`state`, `timestamp` epoch seconds).

### 2. Arming and lifecycle (was B2, B4)

**Correct `pipe-pane` semantics.** `tmux pipe-pane -o` means *"only open a new pipe
if one is not already open"* (a toggle-guard) — it does **not** mean "pipe only
while a command runs." A pipe opened with `pipe-pane` stays attached to the
**pane** for the pane's whole life, **across foreground-process changes**. So:

- The parser is **not** torn down when the agent exits back to a shell; it keeps
  receiving the shell's (and any later program's) bytes and only gets **stdin EOF
  when the pane itself closes**. "Agent exited" and "pane closed" are distinct.
- **Steady-state cost is accepted and bounded:** once a pane has ever run an agent,
  its parser lives for the pane's life, feeding all subsequent bytes through the
  VT model. The parser self-suppresses when no manifest matches (writes nothing),
  and `IsAltScreen`/alt-screen output is skipped. Cost is one small mostly-idle Go
  process per *ever-was-agent* pane — acceptable given how few such panes exist.

**Arming gate keyed on `#{pane_pipe}` (verified to exist; returns 0/1).** The
existing 1 s `tmux-update-icons` tick already iterates panes. Extend it: when a
pane's `pane_current_command` is in the **known-agent set** (union of all
manifests' `match_commands`) **and** `#{pane_pipe}` is `0`, run
`tmux pipe-pane -o -t %id 'agent-detect %id'`.

- Using tmux's own `#{pane_pipe}` (not a hand-maintained `@agent_piped` option)
  makes the gate reflect **actual pipe liveness**, survive config reload / HM
  activation (which does not restart the server), and **self-heal (resolves R3)**:
  if a parser dies, tmux closes the pipe → `pane_pipe` → `0` → the next tick
  re-arms automatically. No disarm logic and no stale gate.

### 3. Manifest format (trimmed per N3)

Files: `config/agent-manifests/*.toml`, embedded into `agent-detect` via
`//go:embed` (matching the picker `icons_generated` / splash tips pattern — no
runtime file dependency).

```toml
id = "codex"
match_commands = ["codex"]     # pane_current_command values that bind this manifest

[[rules]]
state = "processing"           # one of the 8 states, or "skip" (hold prior)
priority = 100                 # higher evaluated first; first match wins
region = "title"               # title | whole | last_lines:N
contains = ["esc to interrupt"]        # every listed substring must be present (AND)
regex = "..."                  # optional; single regex over the region text
not = [ { contains = ["do you want to proceed?"] } ]  # none of these may match
```

- **Predicate grammar (v1, minimal):** `contains` (AND of substrings), `regex`
  (single regex over the region), and `not` (a list of sub-predicates, none may
  match). The richer `line_regex`/`any`/`all` forms are **deferred** until a
  concrete manifest needs one — `regex` alternation covers v1 OR-cases. This
  applies the same "add only when needed" discipline as regions.
- **Region selectors (v1):** `title` (OSC title), `whole` (full visible screen,
  soft-wrapped lines joined), `last_lines:N` (last N non-empty visible lines).
- **Rule match:** a rule matches when all present predicates pass; matching is
  case-insensitive on plain screen text. Rules sorted by `priority` descending;
  first match wins and emits its `state`. `state = "skip"` recognizes a transient
  overlay (model picker, transcript viewer) → **hold** prior state, write nothing.
- **No match:** hold prior state; if none, write nothing (file stays absent).
- **State vocabulary:** manifests emit lazytmux states directly. herdr's `blocked`
  maps to `waiting` (permission prompt pending) or `denied` (declined).

### 4. State-file integration and the merge (was B5, N1)

- The parser writes **`/tmp/claude-status/screen/<pane_id>`** (`state`,
  `timestamp`), never `panes/<id>`. Rationale: CC hooks and the parser both target
  a pane; co-writing one file races (the codebase already keeps state/issue/task/
  name separate for exactly this reason). Separate files, one merge point.
- **The merge and the existing interrupt reclassifier are made mutually
  exclusive** so they can never disagree on a `processing` pane. In
  `read_pane_state`:

  ```
  hook   = read panes/<id>   → (state_h, ts_h)  or absent
  screen = read screen/<id>  → (state_s, ts_s)  or absent

  if hook present:
      max_age = CLAUDE_STALE_<state_h>          # 0 if the state has no threshold
      if max_age == 0 or (now - ts_h) <= max_age:  # hook FRESH
          use (state_h, ts_h)                    # EXACT current behavior — no-op merge
          # (interrupt reclassifier does NOT run: state is fresh)
      elif screen present:                        # hook STALE and a parser is present
          use (state_s, ts_s)                     # screen is the live ground truth
      else:                                       # hook STALE, no parser
          run interrupt reclassifier as today, then use (state_h, ts_h) faded
  elif screen present:
      use (state_s, ts_s)
  else:
      return 1                                    # no state
  ```

- **Precedence rule (explicit):** where a screen state is present and the hook is
  stale, screen wins and the transcript-tail reclassifier is **skipped**; where no
  screen state exists (parser absent / not yet armed), the current reclassifier
  runs unchanged. They never both fire. No timestamp tie-break is needed because
  the branch is only reached when the hook is already stale, and the screen file
  reflects the current render.
- **`interrupted` (N1):** it is never a stored hook state — it only arises from the
  reclassifier, which now runs solely on the no-parser fallback path. Where a
  parser is present, "the turn was interrupted / returned to prompt" is expressed
  directly as the screen's `idle`/prompt state.
- **Consequences:** Claude with healthy hooks → fresh → path unchanged
  (byte-for-byte, goal met). Non-Claude → no hook file → screen used directly.
  Claude stuck at `processing` past 300 s → screen shows the returned prompt →
  screen `idle` wins. `idle` (no threshold) is never overridden — a hook resting
  state stays authoritative.
- Fade for the chosen `(state, ts)` is computed exactly as today.

### 5. Cleanup (was N2 — concrete sites)

Add `screen/<id>` removal at all three existing teardown sites:
`config/tmux.conf.nix:684` (inline `pane-exited` `rm`),
`claude-status-update.sh:59` (`cleanup_stale_panes` per-file `rm`), and
`claude-status-update.sh:380` (per-pane teardown). The parser exiting on EOF is the
primary lifecycle; these are the backstop for a parser that died without EOF.

## Data flow

1. Tick sees an agent command and `#{pane_pipe}==0` → arms `pipe-pane`.
2. Pane output streams to `agent-detect` stdin (for the pane's whole life).
3. Emulator builds the screen; debounce coalesces bursts.
4. Manifest match → `screen/<id>` written on state change.
5. Any renderer calls `read_pane_state` → merge → state + fade.
6. Pane closes → EOF → parser exits; cleanup sites remove `screen/<id>`.

## Error handling and edge cases

- **Parser crash:** only that pane loses screen state; `#{pane_pipe}` → 0 →
  self-heal re-arm next tick. Claude falls back to its hook. Blast radius: one pane.
- **Alt-screen program** (pager/editor/full-screen TUI): detected → skip matching,
  hold prior state.
- **Binary / high-rate output:** the emulator absorbs it; debounce prevents
  matching mid-frame.
- **Numeric / odd pane ids:** passed literally as argv (not via `tmux source -`),
  so the numeric-session ambiguity does not apply.
- **macOS:** `pipe-pane` + a Go binary are portable; no `setsid`/`flock`.

## Build wiring (was N5)

- Add `"agent-detect"` to `subPackages` in `picker/default.nix` and a `postInstall`
  rename for its binary.
- Regenerate `vendorHash` after the new emulator dependency lands.
- Inject the built binary path into the tick script via the existing
  `mkScript`/`@…@` placeholder machinery (`config/tmux.conf.nix:222-261`, as with
  `@claude_status_bin@`).

## Testing

- **Manifest engine — pure function, no timing (N4).** The matcher is
  `(screen_text, title) → state`; test it table-driven on fixtures captured from
  real Claude and Codex sessions (`tmux capture-pane -e`, recorded pipe-pane
  output), covering each rule, `skip` overlays, and no-match hold.
- **Debounce — separate, deterministic (N4).** Drive the debounce with an
  injectable clock so the coalescing boundary is exercised without wall-clock
  flakiness; do not couple it to the matcher tests.
- **Merge (`read_pane_state`):** bats cases (like `tests/enrich.bats`) — fresh hook
  wins (and reclassifier skipped); stale hook + screen → screen (reclassifier
  skipped); stale hook + no screen → reclassifier + faded hook; no hook + screen →
  screen; neither → no state.
- **Gate:** `nix flake check` (build + pre-commit hooks) must pass.

## Out of scope / phasing

- **Phase 2:** more agent manifests (Gemini, Cursor, …) — pure TOML; live
  pane-resize tracking; richer manifest predicates as needed; retire the
  transcript-tail interrupt hack once the merge is proven.
- **Later:** central daemon + socket API; `wait agent-status`; notifications/sound.

## Risks and open questions

- **R1 — emulator dependency (spike required).** Resolve the Component 1 decision
  before implementation: evaluate `ultraviolet` (already vendored) for byte-stream
  emulation; else `charmbracelet/x/vt` with a version-skew check against the
  `charm.land/*` fork; else `hinshun/vt10x`. Pin the exact version and confirm
  screen-text + OSC-title + alt-screen access on real agent output.
- **R2 — manifest fragility.** Screen-scraping tracks each agent's TUI wording;
  agent updates can break rules. Mitigation: manifests are versioned data and only
  *back up* fresh Claude hooks — a broken manifest degrades to hook-only for Claude
  and to no-icon for others, never to a wrong Claude state on the fresh path.
- **R3 — RESOLVED.** Re-arm after parser death is handled by the `#{pane_pipe}`
  gate (self-healing); no hand-maintained flag.
- **R4 — debounce tuning.** 80 ms is a starting point; tune against real sessions.
  Tested deterministically via the injectable clock (see Testing), independent of
  the tuned value.
