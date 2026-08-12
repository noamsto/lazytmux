# Plan: Codex scraper stuck on processing after idle (#251)

## Root cause — proven with real captured bytes

The task's two candidate mechanisms were tested against a live Codex 0.147.0 pane
in a throwaway tmux server (`tmux -L repro251 -f /dev/null`), with the real
`agent-detect` binary armed via `pipe-pane` exactly as the production sweep does:

- **"No matching idle rule" — ruled out.** Replaying the real working→idle byte
  stream through the emulator + matcher yields `processing` mid-turn and `idle`
  after the final repaint. Three live working→idle runs all transitioned in ~1s.
- **"No sample taken" (output-driven ticker) — ruled out as designed.** The ticker
  is time-driven (40ms) and fires the quiet-settle sample ~80ms after the last
  byte; confirmed live.

But the live harness exposed three real defects, each freezing scraper state
indefinitely — all reproduced with real Codex bytes:

### Defect A — emulator wedge on terminal queries ("no sample taken", permanent)

`charmbracelet/x/vt`'s Emulator answers terminal queries (OSC 10/11/12 color
queries, DA, DSR cursor position, DECRQM) by writing replies to an internal
`io.Pipe` (`emulator.go:102`). Nothing in `screen.Screen` reads it, so the first
query in the pane stream blocks `Feed` **forever** — the watcher's only consumer
of the select loop wedges: no samples, no emits, no liveness exit. `pane_pipe`
stays 1, so the update-icons sweep never re-arms. Last written state (e.g.
`processing`) is frozen permanently.

Codex emits `ESC]10;?` / `ESC]11;?` / `ESC[6n` / `ESC[0c` at every TUI init
(startup, trust-prompt dismissal, `codex resume`, restarting codex in the same
pane — pipe-pane keeps feeding the same watcher). Proof: 700 bytes of real
codex startup output hang `Feed` (5s timeout, never returns).

### Defect B — garbled seed ("never matches the idle rule")

`seededScreen` feeds `capture-pane -p -e` output straight into the emulator.
capture-pane joins rows with bare `\n`; a raw LF moves the cursor down **without
returning to column 0**, so every line drifts right by the previous line's
length. Proof: a real idle-codex seed scores `""` (no rule) instead of `idle`;
the live watcher's startup emit wrote nothing for a visibly idle pane. The same
garbling poisons every truncation re-seed.

### Defect C — pane growth desyncs the emulator (panic *or* silent divergence)

The emulator's geometry is fixed at watcher startup. When the pane grows
(lazytmux reflows windows constantly), Codex repaints at the new height. Two
ways this freezes state:

- **Panic:** a reverse-index inside a scroll region taller than the emulator
  panics: `index out of range [38] with length 30` (`vt.Screen.InsertLine` →
  `ultraviolet.Buffer.InsertLineArea`). Proof: 214 real bytes (`ESC[1;40r` +
  38× `ESC M`, captured live) panic a 30-row emulator standalone. The watcher
  process dies; state freezes at the last write until the sweep re-arms.
- **Silent divergence:** if the repaint happens not to hit the panicking
  sequence, nothing recovers the emulator — rows beyond its bounds are
  clamped away, and the bottom composer region (where the idle signal lives)
  is never seen. Proven live post-feedSafe: pane grown 30→40 mid-turn, watcher
  alive and sampling, state stuck at `processing` after the visibly idle turn
  end. This is the closest reproduction of the reported symptom.

## Fix (all in `picker/agentdetect/`)

1. **`screen/screen.go`** — drain the emulator's response pipe:
   `go io.Copy(io.Discard, e)` in `New`. Query replies are for the application
   side of a pty; headless, we discard them. Feed can never block again.
2. **`main.go` `seededScreen`** — feed `seedBytes(out)` which maps `\n` → `\r\n`
   (pure func, unit-tested). Also make `seededScreen` re-read pane geometry via
   `paneInfo` itself, so every re-seed re-syncs emulator size with the pane.
3. **`main.go` feed path** — `feedSafe(scr, data) (ok bool)` wraps `Feed` with
   `recover`; on panic the loop re-seeds (fresh geometry + fresh parser state)
   instead of dying. Covers Defect C's panic case and any future vt parser bug:
   bad input costs one re-seed, never the watcher.
4. **`main.go` geometry poll** — a 5s ticker re-reads pane geometry
   (`display-message`, one fork per watcher per 5s — far below update-icons'
   own fork rate); on any change, re-seed (fresh emulator at the new size) and
   emit immediately. Covers Defect C's silent-divergence case: idle after a
   resize is detected within ~5s, not minutes. The poll must only act on a
   successfully parsed geometry reply — `display-message` is CANFAIL-tolerant
   against dead panes, so a failed/empty read must mean "no change", never a
   re-seed to the 80x24 default (liveness stays with the capture-pane probe).

Deliberately untouched: `statefile.Writer` (exit path, idempotent Clear),
hook precedence (reader-side, covered by #248 tests), debounce parameters.

## Bound after the fix (requirement 2)

Sampling can neither wedge nor die. The quiet-settle sample fires
≤ ~120ms after the last output byte (80ms debounce + 40ms tick); the 500ms
ceiling caps continuously-animating panes. Idle is therefore reached within
~0.5s of the final repaint. A pane resize that desyncs the emulator is
corrected by the geometry poll within 5s (or immediately via feedSafe's
re-seed when the desync panics) — seconds, not minutes.

## Tests (golden captures from the live repro, committed as testdata)

| fixture | content | red pre-fix | green post-fix |
|---|---|---|---|
| `screen/testdata/codex_startup_queries.txt` (700B) | real codex TUI-init queries | `Feed` hangs | returns promptly |
| `manifest/testdata/codex_working_capture.txt` (6KB) + `codex_idle_capture.txt` (6KB, frame-aligned tail) | real working→idle stream | — (regression guard) | `processing` after working slice, `idle` after tail |
| `testdata/codex_resize_scroll.txt` (214B) | real `ESC[1;40r` + 38× RI | `Feed` panics | `feedSafe` recovers |
| `testdata/codex_idle_seed.txt` (1.3KB) | real capture-pane of idle codex | seed Match = `""` | seed Match = `idle` |

Plus unit tests: `seedBytes` (CRLF transform), `feedSafe` (panicking fake Screen
→ false; normal → true).

## Verification

- `go test ./agentdetect/...` (scoped fast gate).
- Live re-verification with the fixed binary against real codex: arm pre-init
  (old wedge scenario), grow pane mid-turn (old panic scenario), normal turn —
  state must track working→idle in ~1s in all three.
- Full gate: `nix build .#default`, `nix flake check`, `nix build .#lint`.

## Not verified / out of scope

- The exact historical 1337s session cannot be replayed; what is proven is that
  the normal path works and the three fixed defects are the observed freeze
  mechanisms, each sufficient to produce the reported symptom (the silent
  geometry divergence was reproduced live end-to-end: visibly idle pane, state
  stuck at `processing`, watcher alive).
- Pane *shrink* mid-stream without a geometry poll would leave stale rows; the
  geometry poll covers shrink too (any size change re-seeds).
