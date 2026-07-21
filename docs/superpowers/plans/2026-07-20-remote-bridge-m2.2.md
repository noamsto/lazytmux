# Remote bridge M2.2 — mirror all windows + wire the daemon into lztmux-remote-open

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the merged M2.1 single-window daemon mirror so it mirrors *every* window of a bridged remote tmux session into one native local window each (live add/close/rename/active-changed), adds `pause-after` backpressure with a mandatory `%continue` re-seed, and rewires `lztmux-remote-open` to launch this multi-window daemon so it is user-reachable. Closes #180 (part of #167).

**Architecture:** The daemon keeps ownership of the single `ssh -T -e none -CC attach-session` control connection. M2.1 mirrored *one* remote window; M2.2 grows `daemon.Run` to enumerate all remote windows at startup, create one local window per remote window in `<host>-<sess>`, and run the existing plan → spawn → hello → seed pipeline per window against a **window registry** keyed by remote window id (`@N`). Native control-mode notifications (`%window-add`, `%window-close`, `%window-renamed`, `%session-window-changed`, `%layout-change`) drive live local structure; `%pause`/`%continue` add flow control with a fresh `capture-pane` re-seed. Directional authority is unchanged: **structure flows remote→local** (via notifications only), **size flows local→remote** (one `ConvergeCmd` on the single control client); renderers never call `refresh-client`.

**Tech Stack:** Go 1.x (`picker/remotebridge/` module, table-driven `testing`), tmux control mode (`-CC`) on tmux next-3.8, bash (`scripts/lztmux-remote-open.sh`), bats (`tests/remote-m2-integration.bats`), Nix flake checks.

## Global Constraints

Copied verbatim from WORKER_TASK.md, DECOMPOSITION.md (interfaces + regression anchors), and SPEC_RESOLUTION.md. Every task's requirements implicitly include this section.

- **Wire protocol frozen.** `wire/protocol.go` frame layout (1-byte type | 4-byte BE length | payload) and type values (`Hello=1, Seed=2, Output=3, Resize=4, Input=5`, 16 MiB cap) are unchanged. M2.2 additions are *behavioral*: `FrameSeed` may recur mid-stream (a re-seed = full repaint) and `FrameResize` becomes live. **Do not edit `wire/protocol.go`.**
- **New wire invariant (replaces M2.1's seed-before-pump ordering):** *all* daemon→conn writes for one pane are serialized through that pane's sink — no frame type (seed, output, resize) may bypass it.
- **controlmode parser contract.** `Line{Kind, Pane, Args, Data}` stays. Existing kinds keep exact semantics. New kinds are **additive** (append to the `Kind` iota — never renumber existing constants). Window/pane ids ride `Args` as raw tokens (`@N`, `%N`, `$N`). `%window-renamed`'s name may contain spaces — do not `Fields`-split it. `Reader.Next`'s `%begin`…`%end` accumulation and `readReply`'s skip-async behavior are load-bearing and unchanged.
- **daemon.Config injection seam preserved.** Field changes are additive/injection-preserving. `--test-local` / `--src-socket` / `--dst-socket` and the `LZTMUX_BRIDGE_*` / `LZTMUX_DAEMON_*` env-var flag defaults in `cmd/daemon/main.go` must keep working — bats and the launcher both stand on them.
- **Router API unchanged.** Pane-id-keyed `Register`/`Unregister`/`Route`; non-blocking `io.Writer` sinks; `Unregister` closes `Close()`-able sinks.
- **Directional authority (do not regress M2.1).** Structure flows remote→local via notifications only (a local daemon action never *originates* structure). Size flows local→remote via the daemon's single `ConvergeCmd` (`refresh-client -C WxH` on next-3.8). Renderers never call `refresh-client`. There is exactly one control client, therefore one size.
- **1-pane window ≡ M1.** The existing "1-pane remote window, no split, matching dims" bats case must stay byte-identical: no extra splits, no spurious frames.
- **Stop semantics.** `%window-close` no longer terminates the daemon; only `%exit`, control-stream EOF, or an emptied window registry end `Run`. Closing one remote window tears down only its local window. Teardown still kills the local session and closes every conn.
- **`@bridge_win 1` opt-out.** Stamp `@bridge_win 1` on every local window the daemon creates. (Gating the chrome scripts / tmux-remux themselves is out of scope here.)
- **B1 (active-changed):** `%session-window-changed` → local `select-window` on the mapped local window.
- **B2 (session filter):** `%window-add` / `%window-renamed` / `%window-close` / `%session-window-changed` act **only** on windows in the bridged session's registry; a notification whose `@N` is not owned by the bridged session is a no-op. `%unlinked-window-add` stays `Other` (ignored).
- **B3 (routing-aware reply reader):** every **steady-state** (post-startup) control-stream round-trip (layout reconcile, `%continue` re-seed, window-add seed, trailing `list-windows` re-read) routes any `%output` it encounters to the router while awaiting its `%end`/`%error`, so a mid-stream round-trip for pane A never drops live `%output` for pane B. Startup seeding keeps the existing skip-behavior (sanctioned — no live stream yet).
- **`%continue` ordering:** on `%pause %N` mark the sink paused + `refresh-client -A '%N:continue'`; on `%continue %N` write a fresh `FrameSeed` (routing-aware capture) **before** any subsequent routed output for that pane.
- **`%window-pane-changed`:** parsed but **no local action** in M2.2 (focus routing is M2.3).
- **Non-goals (do NOT implement):** structural input parity / keybind translation (M2.3); copy-mode/mouse/clipboard/focus (M2.4); picker integration / retiring arch-C (M3); reconnect-after-drop; co-attach with a live remote human.
- **CI time budget:** all poll-then-kill bats loops stay bounded (M2.1's ran ~18s; keep in that ballpark or below).
- **Verification commands** (run from the worktree root unless noted):
  - Go build + unit: `cd picker && go build ./... && go test ./remotebridge/...`
  - Single Go test: `cd picker && go test ./remotebridge/<pkg>/ -run <TestName> -v`
  - Bats integration: `bats tests/remote-m2-integration.bats`
  - Full gate: `nix flake check`
  - Shell lint: `shellcheck scripts/lztmux-remote-open.sh`
- **Commit from inside `nix develop`/direnv** so the pre-commit hooks (shfmt/shellcheck/statix/etc.) run. Deslop the branch before the PR.

---

## File Structure

| File | Responsibility | Task |
|------|----------------|------|
| `picker/remotebridge/controlmode/parse.go` | Add `WindowAdd`/`WindowRenamed`/`SessionWindowChanged`/`WindowPaneChanged`/`Pause`/`Continue` kinds (additive). | 1 |
| `picker/remotebridge/controlmode/parse_test.go` | Table cases for every new kind (space-preserving rename). | 1 |
| `picker/remotebridge/render/renderer.go` | Live `FrameResize`: decode + record dims (never resize). | 2 |
| `picker/remotebridge/render/renderer_test.go` | `FrameResize` → recorded dims; existing paint/forward test updated for the new `Run` param. | 2 |
| `picker/remotebridge/wire/protocol_test.go` | `EncodeResize`/`DecodeResize` round-trip (protocol.go itself frozen). | 2 |
| `picker/remotebridge/cmd/renderer/main.go` | Pass a no-op resize recorder to `render.Run`. | 2 |
| `picker/remotebridge/daemon/windows.go` (new) | Window registry (`@N` → local window target, remote pane order, conns), monotonic local-index allocation, `list-windows` parse. | 3 |
| `picker/remotebridge/daemon/windows_test.go` (new) | Registry add/remove/lookup, index monotonicity, `list-windows` parse. | 3 |
| `picker/remotebridge/daemon/daemon.go` | Multi-window `Run`; per-window helper refactor; routing-aware reply reader; `%pause`/`%continue`; typed-frame serialized sink; live notification dispatch. | 3,4,5,6 |
| `picker/remotebridge/daemon/mirror.go` | (unchanged API — `PlanWindow`/`RemotePaneOrder` already take a target/layout.) | 3 |
| `picker/remotebridge/daemon/seed.go` | Parametrize `PaneSeed`/`readLayout` reply reader (startup-skip vs routing-aware). | 3,4 |
| `picker/remotebridge/daemon/translate.go` (new) | Pure notification→local-argv translation (rename/active/close-target/pane-changed), B2-filtered by registry. | 4 |
| `picker/remotebridge/daemon/translate_test.go` (new) | Table cases incl. out-of-registry no-op. | 4 |
| `picker/remotebridge/daemon/router_test.go` | Multi-window routing tables; unregistered pane dropped. | 5 |
| `picker/remotebridge/daemon/daemon_test.go` | `%pause`/`%continue` re-seed ordering; routing-aware reader routes sibling `%output`. | 6 |
| `picker/remotebridge/cmd/daemon/main.go` | Drop hardcoded `localWin`; `--window` = initially-selected; `pause-after` attach flag; base-index default. | 3,6 |
| `scripts/lztmux-remote-open.sh` | Launch the detached daemon (I4), not the M1 bridge. | 7 |
| `tests/remote-m2-integration.bats` | Multi-window mirror + live add/close/rename + DST≠SRC convergence; keep 1/2-pane anchors. | 8 |
| `flake.nix` (checks attrset only) | Verify the `remote-m2-integration-tests` check still green; add any bats deps. | 9 |

**Component → task map (DECOMPOSITION.md).** Each task names exactly one component. Order respects the decomposition's `ordering` section:

1. Task 1 = **cm-notify**  ∥  Task 2 = **wire-resize**  (group 1, independent leaves)
2. Task 3 = **daemon-multiwin**  (group 2; high risk → `implement: opus`)
3. Task 4 = **notify-xlate**  →  Task 5 = **output-demux**  (group 3, on the registry). Though DECOMPOSITION marks these ∥, Task 5 **hard-depends on Task 4's `closeWindow`**, so run them in numeric order (Task 5 after Task 4) — a parallel dispatch would hit an undefined `closeWindow`.
4. Task 6 = **pause-reseed**  (group 4; high risk → `implement: opus`)
5. Task 7 = **launcher-wire**  ∥  Task 8 = **bats-multiwin**  (group 5)
6. Task 9 = **flake-check**  (group 6, last)

`unit-tests` co-lands inside its owning Go task (1–6), never as a trailing phase.

**Two justified deviations from the literal file-ownership in DECOMPOSITION.md** (the `ordering` and `interfaces` sections are still honored exactly):

- **Routing-aware reply reader + reply-fn parametrization of `PaneSeed`/`readLayout` (`seed.go`) land in Task 3 (daemon-multiwin), not Task 6.** B3 is BINDING and its first corruption point is a *multi-window live reconcile* (Task 3): a `readLayout`/`capture-pane` round-trip for window X, while windows Y/Z stream, would drop their `%output` under the M2.1 skip-behavior. So the routing-aware reader must exist as soon as multiple windows stream. Task 6 then only *reuses* it for `%continue`. `daemon.go` is in daemon-multiwin's may-touch set; the small `seed.go` signature change (adding a reply-reader parameter) is pulled in here for the same B3 reason.
- **Per-window `%layout-change` dispatch by `Args[0]` lands in Task 3, not Task 4.** A coherent multi-window daemon must route each `%layout-change` to the right registry entry the moment it mirrors >1 window; Task 4 layers add/close/rename/active + the stop-semantics flip on the same loop. Both tasks touch `daemon.go`'s loop (both list it in may-touch), so no boundary is crossed.

---

## Task 1: cm-notify — control-mode parser, new notification kinds

**Component:** cm-notify (risk: low). **implement:** sonnet.

**Files:**
- Modify: `picker/remotebridge/controlmode/parse.go`
- Test: `picker/remotebridge/controlmode/parse_test.go`

**Interfaces:**
- Consumes: nothing (leaf).
- Produces: six new `Kind` constants appended after `LayoutChange` — `WindowAdd`, `WindowRenamed`, `SessionWindowChanged`, `WindowPaneChanged`, `Pause`, `Continue`. Parse conventions:
  - `%window-add @N` → `{Kind: WindowAdd, Args: ["@N"]}`
  - `%window-renamed @N some name` → `{Kind: WindowRenamed, Args: ["@N"], Data: []byte("some name")}` (name kept whole, may contain spaces)
  - `%session-window-changed $S @N` → `{Kind: SessionWindowChanged, Args: ["$S", "@N"]}`
  - `%window-pane-changed @N %P` → `{Kind: WindowPaneChanged, Args: ["@N", "%P"]}`
  - `%pause %N` → `{Kind: Pause, Args: ["%N"]}`
  - `%continue %N` → `{Kind: Continue, Args: ["%N"]}`
  - `%unlinked-window-add @N` → `{Kind: Other}` (unchanged)

- [ ] **Step 1: Write the failing tests.** Append to `parse_test.go`:

```go
func TestParseM22Notifications(t *testing.T) {
	if l := ParseLine(`%window-add @7`); l.Kind != WindowAdd || len(l.Args) != 1 || l.Args[0] != "@7" {
		t.Errorf("window-add: %+v", l)
	}
	l := ParseLine(`%window-renamed @7 my long name`)
	if l.Kind != WindowRenamed || l.Args[0] != "@7" || string(l.Data) != "my long name" {
		t.Errorf("window-renamed (name must stay whole): %+v", l)
	}
	if l := ParseLine(`%session-window-changed $2 @7`); l.Kind != SessionWindowChanged || l.Args[0] != "$2" || l.Args[1] != "@7" {
		t.Errorf("session-window-changed: %+v", l)
	}
	if l := ParseLine(`%window-pane-changed @7 %12`); l.Kind != WindowPaneChanged || l.Args[0] != "@7" || l.Args[1] != "%12" {
		t.Errorf("window-pane-changed: %+v", l)
	}
	if l := ParseLine(`%pause %12`); l.Kind != Pause || l.Args[0] != "%12" {
		t.Errorf("pause: %+v", l)
	}
	if l := ParseLine(`%continue %12`); l.Kind != Continue || l.Args[0] != "%12" {
		t.Errorf("continue: %+v", l)
	}
	if ParseLine(`%unlinked-window-add @9`).Kind != Other {
		t.Error("unlinked-window-add must stay Other")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails.**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestParseM22Notifications -v`
Expected: FAIL — `undefined: WindowAdd` (compile error).

- [ ] **Step 3: Add the kinds and parse cases.** In `parse.go`, append to the `Kind` const block (after `LayoutChange`, preserving existing values):

```go
const (
	Other Kind = iota
	Output
	Begin
	End
	Error
	WindowClose
	Exit
	LayoutChange
	WindowAdd
	WindowRenamed
	SessionWindowChanged
	WindowPaneChanged
	Pause
	Continue
)
```

Add cases to the `switch verb` in `ParseLine` (before `default`):

```go
	case "%window-add":
		return Line{Kind: WindowAdd, Args: strings.Fields(rest)}
	case "%window-renamed":
		// name may contain spaces: id is the first token, the rest is the
		// whole name (kept in Data, not Fields-split).
		id, name, _ := strings.Cut(rest, " ")
		return Line{Kind: WindowRenamed, Args: []string{id}, Data: []byte(name)}
	case "%session-window-changed":
		return Line{Kind: SessionWindowChanged, Args: strings.Fields(rest)}
	case "%window-pane-changed":
		return Line{Kind: WindowPaneChanged, Args: strings.Fields(rest)}
	case "%pause":
		return Line{Kind: Pause, Args: strings.Fields(rest)}
	case "%continue":
		return Line{Kind: Continue, Args: strings.Fields(rest)}
```

- [ ] **Step 4: Run the tests to verify they pass.**

Run: `cd picker && go test ./remotebridge/controlmode/ -v`
Expected: PASS (`TestParseM22Notifications`, `TestParseLine`, `TestUnescape`, reader/layout/encode tests all green).

- [ ] **Step 5: Commit.**

```bash
git add picker/remotebridge/controlmode/parse.go picker/remotebridge/controlmode/parse_test.go
git commit -m "feat(remotebridge): parse M2.2 control-mode notifications"
```

---

## Task 2: wire-resize — FrameResize end-to-end (renderer side)

**Component:** wire-resize (risk: low). **implement:** sonnet.

`wire.FrameResize` + `EncodeResize`/`DecodeResize` already exist but nothing consumes them. This task makes the **renderer** decode a `FrameResize` and *record* the dims (size stays daemon-authoritative — the renderer never resizes anything). The **daemon emit** of `FrameResize` rides the serialized sink and lands in Task 6 (per the frozen wire invariant — a re-seed/resize is a second writer, so it must go through the sink built there).

**Files:**
- Modify: `picker/remotebridge/render/renderer.go`
- Modify: `picker/remotebridge/cmd/renderer/main.go`
- Test: `picker/remotebridge/render/renderer_test.go`, `picker/remotebridge/wire/protocol_test.go`

**Interfaces:**
- Consumes: `wire.DecodeResize(p []byte) (int, int, error)` (frozen).
- Produces: `render.Run(conn io.ReadWriteCloser, paneID string, in io.Reader, out io.Writer, rawSetup func() (func() error, error), recordResize func(w, h int)) error` — one new trailing param `recordResize`, invoked on each decoded `FrameResize`. Production passes a no-op.

- [ ] **Step 1: Write the failing tests.** Add to `wire/protocol_test.go`:

```go
func TestEncodeDecodeResizeRoundTrip(t *testing.T) {
	for _, c := range []struct{ w, h int }{{80, 24}, {210, 52}, {1, 1}} {
		w, h, err := DecodeResize(EncodeResize(c.w, c.h))
		if err != nil || w != c.w || h != c.h {
			t.Errorf("round-trip %dx%d -> %dx%d err %v", c.w, c.h, w, h, err)
		}
	}
	if _, _, err := DecodeResize([]byte{1, 2, 3}); err == nil {
		t.Error("short payload must error")
	}
}
```

Add to `render/renderer_test.go` (and update the existing `TestRendererPaintsAndForwards` call to pass a `nil` sixth arg):

```go
func TestRendererRecordsResize(t *testing.T) {
	client, server := net.Pipe()
	var out bytes.Buffer
	noRaw := func() (func() error, error) { return func() error { return nil }, nil }

	var gotW, gotH int
	rec := func(w, h int) { gotW, gotH = w, h }

	done := make(chan error, 1)
	go func() { done <- Run(client, "%3", bytes.NewReader(nil), &out, noRaw, rec) }()

	f, err := wire.ReadFrame(server) // Hello
	if err != nil || f.Type != wire.FrameHello {
		t.Fatalf("hello: %v %v", f.Type, err)
	}
	wire.WriteFrame(server, wire.FrameResize, wire.EncodeResize(159, 52))
	wire.WriteFrame(server, wire.FrameSeed, []byte("SEED"))
	// Give the paint loop a moment, then close.
	time.Sleep(50 * time.Millisecond)
	server.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after conn close")
	}
	if gotW != 159 || gotH != 52 {
		t.Errorf("recorded %dx%d, want 159x52", gotW, gotH)
	}
	if out.String() != "SEED" {
		t.Errorf("painted %q, want SEED", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `cd picker && go test ./remotebridge/render/ ./remotebridge/wire/ -run 'Resize' -v`
Expected: FAIL — `TestEncodeDecodeResizeRoundTrip` passes (codec already exists) but `TestRendererRecordsResize` fails to compile (`Run` takes 5 args, `recordResize` unused).

- [ ] **Step 3: Add the `recordResize` param and the decode.** In `renderer.go`, change the signature and the `FrameResize` case:

```go
func Run(conn io.ReadWriteCloser, paneID string, in io.Reader, out io.Writer, rawSetup func() (func() error, error), recordResize func(w, h int)) error {
```

```go
		case wire.FrameResize:
			// Size is daemon-authoritative — the renderer records the dims
			// for its painter but never resizes anything (no refresh-client).
			if w, h, err := wire.DecodeResize(f.Payload); err == nil && recordResize != nil {
				recordResize(w, h)
			}
```

Update the doc comment above `Run` to note the recorded-not-applied behavior. In `cmd/renderer/main.go`, pass a no-op recorder to the `render.Run(...)` call (add `func(int, int) {}` as the final argument). Update the existing `TestRendererPaintsAndForwards` in `renderer_test.go` to pass `nil` as the sixth arg.

- [ ] **Step 4: Run the tests to verify they pass.**

Run: `cd picker && go build ./... && go test ./remotebridge/render/ ./remotebridge/wire/ -v`
Expected: PASS (both new tests + the updated existing renderer test).

- [ ] **Step 5: Commit.**

```bash
git add picker/remotebridge/render/renderer.go picker/remotebridge/render/renderer_test.go picker/remotebridge/wire/protocol_test.go picker/remotebridge/cmd/renderer/main.go
git commit -m "feat(remotebridge): renderer records FrameResize dims (size stays daemon-authoritative)"
```

---

## Task 3: daemon-multiwin — multi-window orchestration core

**Component:** daemon-multiwin (risk: **high**). **implement: opus.**

Grow `daemon.Run` from "mirror one window" to "mirror every window of the remote session" over the same single `-CC` connection, backed by a window registry. This is the biggest, highest-blast-radius change; it also introduces the routing-aware reply reader (B3) because a multi-window live reconcile is the first place the M2.1 skip-behavior would corrupt sibling windows.

**Local-window identity decision (settled here — DECOMPOSITION option b):** deterministic local indices via `base-index` + monotonic allocation. `Config.LocalTmux` cannot capture output, so the daemon never reads a created window's index back — it *assigns* it. The first remote window reuses the launcher's initial window at `base-index` (default 1); each subsequent window is created at an explicit monotonically-increasing index via `new-window -d -t <sess>:<idx>` (tmux honors an explicit free target index). The counter never decrements on close, so a mid-list close leaves a hole rather than letting a later `new-window` collide into a stale mapping. The registry maps remote `@N` → local target string `"<sess>:<idx>"`.

**Files:**
- Create: `picker/remotebridge/daemon/windows.go`, `picker/remotebridge/daemon/windows_test.go`
- Modify: `picker/remotebridge/daemon/daemon.go`, `picker/remotebridge/daemon/seed.go`, `picker/remotebridge/cmd/daemon/main.go`
- (unchanged: `daemon/mirror.go` — `PlanWindow(target, L)` / `RemotePaneOrder(L)` already take a per-window target/layout.)

**Interfaces:**
- Consumes: cm-notify kinds (`LayoutChange` `Args[0]` = `@N`); `PlanWindow`, `RemotePaneOrder`, `ConvergeCmd`, `wire.*`, `Router`.
- Produces:
  - `mirrorWindow` struct: `{ remoteID string; localWin string; remotePanes []string; conns map[string]net.Conn }`.
  - `registry` struct with methods:
    - `newRegistry(baseIdx int) *registry`
    - `(*registry) allocLocalWin(sess string) string` — returns `"<sess>:<idx>"`, advancing the monotonic counter (first call returns `<sess>:<baseIdx>`).
    - `(*registry) add(remoteID, localWin string) *mirrorWindow`
    - `(*registry) byRemoteID(remoteID string) (*mirrorWindow, bool)`
    - `(*registry) remove(remoteID string) (*mirrorWindow, bool)`
    - `(*registry) empty() bool`
  - `remoteWindow` struct: `{ index string; id string }` — carries **both** the remote window index (`#{window_index}`) and its id (`#{window_id}`, `@N`). These are different tmux namespaces: `--window`/`RemoteWindow` is an *index*, the registry is keyed by *id*.
  - `parseWindowList(body string) []remoteWindow` — parses `list-windows -F '#{window_index} #{window_id}'` reply body into `[{index:"1", id:"@1"}, {index:"2", id:"@7"}, ...]`, dropping blank/malformed rows.
  - `localWinForRemoteIndex(wins []remoteWindow, reg *registry, remoteIdx string) (string, bool)` — resolves the initially-selected window: maps the remote *index* → remote *id* (via the enumerated `wins`) → local window (via `reg`). This is the seam that keeps `--window <idx>` from being misread as window id `@<idx>`.
  - Refactored helpers (per-window target, not `cfg.LocalWin`): `spawnRenderer(cfg Config, localWin string, index int, remotePane string) error`; `reconcileLayout(cfg Config, w *mirrorWindow, reader, send, router, connCh, ...)` (the router-bound routing-aware reply closure is built internally, not passed as a param).
  - Routing-aware reply reader: `readReplyRouting(reader *controlmode.Reader, router *Router) (controlmode.Line, bool)` — routes any `%output` it sees to `router` and returns on `%end`/`%error`.
  - `PaneSeed`/`readLayout` gain a trailing `reply func(*controlmode.Reader) (controlmode.Line, bool)` parameter so startup passes `readReply` (skip) and steady-state passes a router-bound closure.
  - `Config`: **remove** `LocalWin`; **add** `BaseIndex int` (default 1). `RemoteWindow` keeps its field name but its meaning becomes "initially-selected remote window index" (not a mirror filter).

- [ ] **Step 1: Write the failing registry/parse tests.** Create `windows_test.go`:

```go
package daemon

import "testing"

func TestRegistryMonotonicIndices(t *testing.T) {
	r := newRegistry(1)
	if got := r.allocLocalWin("h-s"); got != "h-s:1" {
		t.Fatalf("first alloc = %q, want h-s:1", got)
	}
	if got := r.allocLocalWin("h-s"); got != "h-s:2" {
		t.Fatalf("second alloc = %q, want h-s:2", got)
	}
	// A close must not free an index for reuse: the next alloc still advances.
	r.add("@1", "h-s:1")
	r.remove("@1")
	if got := r.allocLocalWin("h-s"); got != "h-s:3" {
		t.Fatalf("post-remove alloc = %q, want h-s:3 (no reuse)", got)
	}
}

func TestRegistryLookup(t *testing.T) {
	r := newRegistry(1)
	w := r.add("@5", "h-s:1")
	w.remotePanes = []string{"%3", "%4"}
	if got, ok := r.byRemoteID("@5"); !ok || got.localWin != "h-s:1" {
		t.Fatalf("byRemoteID(@5) = %+v %v", got, ok)
	}
	if _, ok := r.byRemoteID("@99"); ok {
		t.Fatal("byRemoteID(@99) should be false")
	}
	if r.empty() {
		t.Fatal("registry with one window must not be empty")
	}
	if _, ok := r.remove("@5"); !ok || !r.empty() {
		t.Fatal("remove(@5) then empty() should be true")
	}
}

func TestParseWindowList(t *testing.T) {
	// index and id are distinct namespaces: window at index 3 has id @5.
	got := parseWindowList("1 @1\n2 @2\n3 @5\n")
	want := []remoteWindow{{"1", "@1"}, {"2", "@2"}, {"3", "@5"}}
	if len(got) != len(want) {
		t.Fatalf("parseWindowList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseWindowList[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(parseWindowList("  \n\n")) != 0 {
		t.Fatal("blank body must yield no windows")
	}
}

// TestInitialWindowSelectsByIndexNotID pins the blocking fix: --window carries a
// window INDEX, not a window id. A remote session where index 2 has id @7 must
// select the local window mirroring @7 — never "@2" (which here is a different
// window at index 1).
func TestInitialWindowSelectsByIndexNotID(t *testing.T) {
	wins := []remoteWindow{{"1", "@2"}, {"2", "@7"}} // index 1 -> @2, index 2 -> @7
	reg := newRegistry(1)
	reg.add("@2", "h-s:1")
	reg.add("@7", "h-s:2")
	if got, ok := localWinForRemoteIndex(wins, reg, "2"); !ok || got != "h-s:2" {
		t.Fatalf("--window 2 selected %q ok=%v, want h-s:2 (mirror of @7, not @2)", got, ok)
	}
	if got, ok := localWinForRemoteIndex(wins, reg, "1"); !ok || got != "h-s:1" {
		t.Fatalf("--window 1 selected %q ok=%v, want h-s:1", got, ok)
	}
	if _, ok := localWinForRemoteIndex(wins, reg, "9"); ok {
		t.Fatal("out-of-range index must not select")
	}
}
```

- [ ] **Step 2: Run to verify it fails.**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestRegistry|TestParseWindowList|TestInitialWindow' -v`
Expected: FAIL — undefined `newRegistry`/`parseWindowList` (compile error).

- [ ] **Step 3: Implement `windows.go`.** Create it:

```go
package daemon

import (
	"fmt"
	"net"
	"strings"
)

// mirrorWindow is one remote window's local mirror: the remote window id it
// tracks, the local tmux window target it renders into, the remote pane ids in
// creation order, and the renderer conns keyed by remote pane id.
type mirrorWindow struct {
	remoteID    string
	localWin    string
	remotePanes []string
	conns       map[string]net.Conn
}

// registry maps remote window ids (@N) to their local mirror windows and hands
// out monotonically-increasing local window indices. LocalTmux can't capture a
// created window's index, so the daemon assigns indices rather than reading
// them back; the counter never decrements, so a closed window's index is never
// reused and a stale @N->index mapping can't collide.
type registry struct {
	byRemote map[string]*mirrorWindow
	nextIdx  int
}

func newRegistry(baseIdx int) *registry {
	return &registry{byRemote: map[string]*mirrorWindow{}, nextIdx: baseIdx}
}

func (r *registry) allocLocalWin(sess string) string {
	win := fmt.Sprintf("%s:%d", sess, r.nextIdx)
	r.nextIdx++
	return win
}

func (r *registry) add(remoteID, localWin string) *mirrorWindow {
	w := &mirrorWindow{remoteID: remoteID, localWin: localWin, conns: map[string]net.Conn{}}
	r.byRemote[remoteID] = w
	return w
}

func (r *registry) byRemoteID(remoteID string) (*mirrorWindow, bool) {
	w, ok := r.byRemote[remoteID]
	return w, ok
}

func (r *registry) remove(remoteID string) (*mirrorWindow, bool) {
	w, ok := r.byRemote[remoteID]
	if ok {
		delete(r.byRemote, remoteID)
	}
	return w, ok
}

func (r *registry) empty() bool { return len(r.byRemote) == 0 }

// remoteWindow pairs a remote window's index (#{window_index}) with its id
// (#{window_id}, @N). --window / Config.RemoteWindow is an *index*; the registry
// is keyed by *id* — different tmux namespaces, so both must be carried.
type remoteWindow struct {
	index string
	id    string
}

// parseWindowList turns a `list-windows -F '#{window_index} #{window_id}'` reply
// body into the ordered remote windows, dropping blank/malformed rows.
func parseWindowList(body string) []remoteWindow {
	var wins []remoteWindow
	for _, row := range strings.Split(body, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		idx, id, ok := strings.Cut(row, " ")
		if !ok {
			continue
		}
		wins = append(wins, remoteWindow{index: idx, id: id})
	}
	return wins
}

// localWinForRemoteIndex resolves the initially-selected window: it maps a
// remote window *index* (as carried by --window) to the remote window *id* via
// the enumerated windows, then to the local window via the registry. This keeps
// --window <idx> from being misread as window id "@<idx>".
func localWinForRemoteIndex(wins []remoteWindow, reg *registry, remoteIdx string) (string, bool) {
	for _, rw := range wins {
		if rw.index == remoteIdx {
			if mw, ok := reg.byRemoteID(rw.id); ok {
				return mw.localWin, true
			}
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run the registry tests to verify they pass.**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestRegistry|TestParseWindowList|TestInitialWindow' -v`
Expected: PASS.

- [ ] **Step 5: Add the routing-aware reply reader test (B3 unit anchor).** Add to `daemon_test.go`:

```go
// TestReadReplyRoutingRoutesSiblingOutput: while awaiting one command's
// %begin..%end reply, standalone %output for another pane (NOT inside the
// %begin guard) must be routed, not dropped. Reader.Next absorbs guarded lines
// into the reply body, so pane-B output is emitted between reply blocks.
func TestReadReplyRoutingRoutesSiblingOutput(t *testing.T) {
	stream := strings.Join([]string{
		"%output %2 live-B",
		"%begin 1 1 0",
		"cursor-and-capture-reply",
		"%end 1 1 0",
	}, "\n") + "\n"
	reader := newTestReader(stream)
	router := NewRouter()
	var sink capBuf
	router.Register("%2", &sink)

	l, ok := readReplyRouting(reader, router)
	if !ok || l.Kind != controlmode.End {
		t.Fatalf("readReplyRouting returned %+v ok=%v, want End", l, ok)
	}
	if sink.String() != "live-B" {
		t.Errorf("sibling pane-B output %q was dropped, want %q", sink.String(), "live-B")
	}
}
```

- [ ] **Step 6: Run to verify it fails.**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestReadReplyRoutingRoutesSiblingOutput -v`
Expected: FAIL — undefined `readReplyRouting`.

- [ ] **Step 7: Implement `readReplyRouting` and parametrize the seed helpers.** In `daemon.go` add:

```go
// readReplyRouting is the steady-state (post-startup) reply reader (B3): it
// returns the next command-reply block (End/Error) but routes any %output it
// encounters to router first, so a mid-stream round-trip for one pane never
// drops live %output for another. Startup seeding keeps readReply's plain
// skip-behavior (no live stream yet).
func readReplyRouting(reader *controlmode.Reader, router *Router) (controlmode.Line, bool) {
	for {
		l, ok := reader.Next()
		if !ok {
			return controlmode.Line{}, false
		}
		switch l.Kind {
		case controlmode.End, controlmode.Error:
			return l, true
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		}
	}
}
```

In `seed.go`, add a trailing reply-reader parameter to `PaneSeed` and `readLayout` (and thread it into their internal `readCursor`/`readCapture`):

```go
type replyFn = func(*controlmode.Reader) (controlmode.Line, bool)

func PaneSeed(reader *controlmode.Reader, send func(string), paneID string, reply replyFn) ([]byte, error) {
	// ... same body, but readCursor(reader, reply) / readCapture(reader, reply)
}

func readCursor(reader *controlmode.Reader, reply replyFn) (cx, cy int, alt, appCursorKeys bool) {
	l, ok := reply(reader)
	// ...
}

func readCapture(reader *controlmode.Reader, reply replyFn) (data []byte, isErr bool) {
	l, ok := reply(reader)
	// ...
}
```

`readLayout` in `daemon.go` gains the same `reply replyFn` parameter and uses it instead of the bare `readReply`. `readReply` stays exported-in-package for the startup callers.

- [ ] **Step 8: Rewrite `Run` for multi-window (the core change).** Replace the single-window body. Key structure (concrete, not pseudocode):

```go
func Run(cfg Config) error {
	reader := controlmode.NewReader(cfg.Ctl)
	var sendMu sync.Mutex
	closed := false
	cmds := bufio.NewWriter(cfg.Ctl)
	send := func(s string) { /* unchanged: mutex-guarded write+flush, no-op if closed */ }

	readReply(reader) // drain implicit attach reply (startup skip is sanctioned)

	w, h := cfg.WinSize()
	send(ConvergeCmd(w, h)) // one client, one size — converges ALL remote windows
	readReply(reader)

	// Enumerate every window of the bridged remote session. Read BOTH index and
	// id: --window is an *index*, the registry is keyed by *id* (@N).
	send(fmt.Sprintf("list-windows -t %s -F '#{window_index} #{window_id}'", tmuxQuote(cfg.RemoteSession)))
	lw, ok := readReply(reader)
	if !ok || lw.Kind == controlmode.Error {
		return fmt.Errorf("daemon: list-windows for %s failed", cfg.RemoteSession)
	}
	remoteWins := parseWindowList(string(lw.Data))
	if len(remoteWins) == 0 {
		return fmt.Errorf("daemon: remote session %s has no windows", cfg.RemoteSession)
	}

	router := NewRouter()
	os.Remove(cfg.SockPath)
	listener, err := net.Listen("unix", cfg.SockPath)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", cfg.SockPath, err)
	}
	connCh := make(chan helloConn, 64)
	go acceptRenderers(listener, connCh)

	reg := newRegistry(cfg.BaseIndex)
	teardown := func() {
		listener.Close()
		for _, mw := range reg.byRemote {
			for _, c := range mw.conns {
				c.Close()
			}
		}
		sendMu.Lock(); closed = true; sendMu.Unlock()
		cfg.Ctl.Close()
		if cfg.LocalSess != "" {
			cfg.LocalTmux("kill-session", "-t", cfg.LocalSess)
		}
	}

	// Mirror each remote window into its own local window. The first reuses
	// the launcher's initial window (base-index); the rest are created.
	for i, rw := range remoteWins {
		localWin := reg.allocLocalWin(cfg.LocalSess)
		if i > 0 {
			if err := cfg.LocalTmux("new-window", "-d", "-t", localWin); err != nil {
				teardown()
				return fmt.Errorf("daemon: new-window %s: %w", localWin, err)
			}
		}
		cfg.LocalTmux("set-option", "-w", "-t", localWin, "@bridge_win", "1")
		mw := reg.add(rw.id, localWin)
		if err := setupWindow(cfg, reader, send, router, connCh, mw); err != nil {
			teardown()
			return err
		}
	}

	// Select the initially-requested window. RemoteWindow is a window INDEX
	// (not an id), so resolve index -> id -> local window via the enumerated
	// list; never treat it as id "@<idx>".
	if initWin, ok := localWinForRemoteIndex(remoteWins, reg, cfg.RemoteWindow); ok {
		cfg.LocalTmux("select-window", "-t", initWin)
	}

	// Main loop.
	for {
		l, ok := reader.Next()
		if !ok {
			break // control-stream EOF
		}
		switch l.Kind {
		case controlmode.Output:
			router.Route(l.Pane, l.Data)
		case controlmode.LayoutChange:
			if len(l.Args) > 0 {
				if mw, ok := reg.byRemoteID(l.Args[0]); ok {
					reconcileLayout(cfg, mw, reader, send, router, connCh)
				}
			}
		case controlmode.Exit:
			teardown()
			return nil
		// %window-add / %window-close / %window-renamed /
		// %session-window-changed handled in Task 4 (notify-xlate).
		}
	}
	teardown()
	return nil
}
```

Add `setupWindow` — the per-window plan/spawn/hello/seed pipeline extracted from M2.1's steps 5–9, targeting `mw.localWin` and populating `mw.remotePanes` / `mw.conns`. It uses `readReply` (startup skip) for its seed round-trips because at enumeration time no window is streaming yet (B3 startup exception). Note `setupWindow` must remember the pane ids and their conns on `mw`.

**Note (B3 startup skip cost — acceptable per binding B3):** because setup is sequential and uses the plain skip reader, while windows 2..N are being set up the daemon is *not* draining the async stream, so any live `%output` for the already-seeded window 1 during that interval is dropped rather than routed. This self-heals on the pane's next `%output` (or a `%pause`/`%continue` re-seed) once the main loop starts, and the B3 ruling explicitly sanctions the startup skip-behavior — stated here so it isn't mistaken for a routing bug. Offline bats won't surface it (no concurrent live stream during setup).

**Note (regression anchor):** for a session whose windows are each 1-pane, `setupWindow` must apply exactly M1's behavior per window — no split, one renderer, matching dims. `PlanWindow` already emits zero splits for a 1-pane layout; keep that path untouched.

Refactor `spawnRenderer` to take `localWin string` and target `fmt.Sprintf("%s.%d", localWin, index)`. Refactor `reconcileLayout` to take `w *mirrorWindow`, derive the remote target from `w.remoteID` by targeting the window **id** directly (`-t @N`, e.g. `fmt.Sprintf("%s:%s", tmuxQuote(cfg.RemoteSession), w.remoteID)` where `w.remoteID` is the full `@N` token — do NOT `strings.TrimPrefix` the `@`, which would target window *index* N and reintroduce the index/id conflation), operate on `w.localWin` / `w.remotePanes` / `w.conns`, and use `readReplyRouting`-bound closures for its `readLayout`/`seedRenderer` round-trips (B3 — sibling windows are streaming during a live reconcile). Delete the now-unused single-window `handleLine`/`runLoop` only if nothing else references them; otherwise keep `runLoop`/`handleLine` **only** if Task 4 still needs them (it will re-home stop-semantics there — leave them and adjust in Task 4). Prefer folding the routing into the loop above and removing `runLoop` if the daemon_test loop tests are updated; if that widens the diff, keep them and let Task 4 reconcile. Keep the diff minimal.

- [ ] **Step 9: Update `cmd/daemon/main.go`.** Remove `localWin := *localSess + ":1"`. Add a `--base-index` flag defaulting to `envIntDefault("LZTMUX_DAEMON_BASE_INDEX", 1)` (add the helper). `WinSize` queries the session's active window (`display-message -p -t <localSess> -F '#{window_width} #{window_height}'`) instead of the removed `localWin`. Populate `cfg.BaseIndex`; drop `cfg.LocalWin`. `--window`/`LZTMUX_BRIDGE_WINDOW` stays but now feeds the initially-selected window (documented in the flag usage string). Keep `--test-local`/`--src-socket`/`--dst-socket` and all env defaults intact.

- [ ] **Step 10: Build + run the full daemon unit suite.**

Run: `cd picker && go build ./... && go test ./remotebridge/... -v`
Expected: PASS (registry, parse, routing-aware reader, existing daemon/router/seed/mirror/render/wire tests). Fix any compile fallout from the `PaneSeed`/`readLayout`/`spawnRenderer`/`reconcileLayout` signature changes and the `Config.LocalWin` removal.

- [ ] **Step 11: Verify the existing offline bats anchors still pass** (1-pane M1 anchor + 2-pane + mid-session split). These run against the rebuilt daemon; they exercise a single-window remote session and must stay green.

Run: `bats tests/remote-m2-integration.bats`
Expected: all three existing cases PASS (the daemon now enumerates a 1-window session and mirrors it identically).

- [ ] **Step 12: Commit.**

```bash
git add picker/remotebridge/daemon/windows.go picker/remotebridge/daemon/windows_test.go picker/remotebridge/daemon/daemon.go picker/remotebridge/daemon/seed.go picker/remotebridge/daemon/daemon_test.go picker/remotebridge/cmd/daemon/main.go
git commit -m "feat(remotebridge): mirror all remote windows via a window registry [#180]"
```

---

## Task 4: notify-xlate — notification → local-command translation

**Component:** notify-xlate (risk: med). **implement:** sonnet.

Wire the live window notifications into `Run`'s main loop, on top of the Task 3 registry. Pure translation for the simple verbs lives in a unit-tested `translate.go`; the add/close orchestration (spawn pipeline / teardown) lives in `daemon.go` calling registry methods. This is also where the **stop-semantics flip** lands: `%window-close` stops terminating the daemon.

**Files:**
- Create: `picker/remotebridge/daemon/translate.go`, `picker/remotebridge/daemon/translate_test.go`
- Modify: `picker/remotebridge/daemon/daemon.go` (main loop `switch`), `picker/remotebridge/daemon/windows.go` (helper if needed)

**Interfaces:**
- Consumes: cm-notify kinds; Task 3 `registry`/`mirrorWindow`; `setupWindow`, `readReplyRouting`, `Router`.
- Produces: `translateWindowNotification(l controlmode.Line, reg *registry) (argv []string, ok bool)` — pure. Returns the local tmux argv for `WindowRenamed`/`SessionWindowChanged`, and `(nil, false)` for anything out-of-registry, for `WindowPaneChanged` (M2.2 no-op), and for `WindowClose`/`WindowAdd` (handled by orchestration, not a single argv). B2 filter: any `@N` not in `reg` → `(nil, false)`.
  - `WindowRenamed` `{Args:["@N"], Data:name}`, `@N` in reg → `["rename-window", "-t", localWin, string(name)]`
  - `SessionWindowChanged` `{Args:["$S","@N"]}`, `@N` in reg → `["select-window", "-t", localWin]`
  - out-of-registry / `WindowPaneChanged` / others → `(nil, false)`

- [ ] **Step 1: Write the failing translation table test.** Create `translate_test.go`:

```go
package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestTranslateWindowNotification(t *testing.T) {
	reg := newRegistry(1)
	reg.add("@1", "h-s:1")
	reg.add("@2", "h-s:2")

	cases := []struct {
		name string
		line controlmode.Line
		argv []string
		ok   bool
	}{
		{"rename in registry", controlmode.Line{Kind: controlmode.WindowRenamed, Args: []string{"@2"}, Data: []byte("my name")},
			[]string{"rename-window", "-t", "h-s:2", "my name"}, true},
		{"active-changed in registry", controlmode.Line{Kind: controlmode.SessionWindowChanged, Args: []string{"$1", "@1"}},
			[]string{"select-window", "-t", "h-s:1"}, true},
		{"rename out of registry (B2)", controlmode.Line{Kind: controlmode.WindowRenamed, Args: []string{"@9"}, Data: []byte("x")},
			nil, false},
		{"active-changed out of registry (B2)", controlmode.Line{Kind: controlmode.SessionWindowChanged, Args: []string{"$1", "@9"}},
			nil, false},
		{"pane-changed is a no-op (M2.2)", controlmode.Line{Kind: controlmode.WindowPaneChanged, Args: []string{"@1", "%3"}},
			nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, ok := translateWindowNotification(c.line, reg)
			if ok != c.ok || !reflect.DeepEqual(argv, c.argv) {
				t.Errorf("got (%v,%v), want (%v,%v)", argv, ok, c.argv, c.ok)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails.**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestTranslateWindowNotification -v`
Expected: FAIL — undefined `translateWindowNotification`.

- [ ] **Step 3: Implement `translate.go`.**

```go
package daemon

import "github.com/noamsto/lazytmux/picker/remotebridge/controlmode"

// translateWindowNotification maps a parsed remote window notification to a
// single local tmux argv, filtered to the bridged session's registry (B2): a
// window id outside the registry yields (nil, false). WindowAdd/WindowClose are
// orchestration (spawn pipeline / teardown), not a single argv, so they return
// (nil, false) here and are handled in Run's loop. WindowPaneChanged is a
// deliberate M2.2 no-op (focus routing is M2.3).
func translateWindowNotification(l controlmode.Line, reg *registry) ([]string, bool) {
	switch l.Kind {
	case controlmode.WindowRenamed:
		if len(l.Args) == 0 {
			return nil, false
		}
		w, ok := reg.byRemoteID(l.Args[0])
		if !ok {
			return nil, false
		}
		return []string{"rename-window", "-t", w.localWin, string(l.Data)}, true
	case controlmode.SessionWindowChanged:
		if len(l.Args) < 2 {
			return nil, false
		}
		w, ok := reg.byRemoteID(l.Args[1])
		if !ok {
			return nil, false
		}
		return []string{"select-window", "-t", w.localWin}, true
	default:
		return nil, false
	}
}
```

- [ ] **Step 4: Run the translation test to verify it passes.**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestTranslateWindowNotification -v`
Expected: PASS.

- [ ] **Step 5: Wire the notifications into `Run`'s main-loop `switch`.** Extend the `switch l.Kind` from Task 3:

```go
		case controlmode.WindowRenamed, controlmode.SessionWindowChanged:
			if argv, ok := translateWindowNotification(l, reg); ok {
				cfg.LocalTmux(argv...)
			}
		case controlmode.WindowAdd:
			addWindow(cfg, reader, send, router, connCh, reg, l.Args[0]) // B2-confirmed inside
		case controlmode.WindowClose:
			closeWindow(cfg, router, reg, l.Args[0])
			if reg.empty() {
				teardown()
				return nil
			}
		case controlmode.Exit:
			teardown()
			return nil
```

Remove the M2.1 `%window-close`-terminates-daemon behavior (stop-semantics flip): the daemon now ends only on `%exit`, EOF, or an emptied registry. Delete `runLoop`/`handleLine` if Task 3 left them and nothing references them; update the two `daemon_test.go` loop tests (`TestLoopRoutesAndExits`, `TestLoopStopsOnWindowClose`, `TestLoopReturnsFalseOnEOF`) — retarget `TestLoopStopsOnWindowClose` to assert the daemon does **not** stop on a single `%window-close` when other windows remain (or drop it and cover close-teardown via bats in Task 8, noting the removal).

- [ ] **Step 6: Implement `addWindow` / `closeWindow` orchestration in `daemon.go`.**
  - `addWindow`: B2-confirm via a **routing-aware** `list-windows` re-read (the trailing re-read generalization) — if `@N` is now in the bridged session and not already in `reg`, `allocLocalWin` → `new-window -d -t <localWin>` → stamp `@bridge_win 1` → `reg.add` → `setupWindow` (seed round-trips routing-aware, since sibling windows stream). If `@N` is not in the session (e.g. it belonged elsewhere) or already registered, no-op.
  - `closeWindow`: `reg.remove(@N)`; if found, `router.Unregister` each `mw.remotePanes` id (closes each sink), close each `mw.conns` entry, `cfg.LocalTmux("kill-window", "-t", mw.localWin)`. Out-of-registry `@N` → no-op (B2).

- [ ] **Step 7: Build + run the daemon unit suite.**

Run: `cd picker && go build ./... && go test ./remotebridge/... -v`
Expected: PASS. Existing single-window bats anchors still green: `bats tests/remote-m2-integration.bats`.

- [ ] **Step 8: Commit.**

```bash
git add picker/remotebridge/daemon/translate.go picker/remotebridge/daemon/translate_test.go picker/remotebridge/daemon/daemon.go picker/remotebridge/daemon/daemon_test.go
git commit -m "feat(remotebridge): reflect live window add/close/rename/active-changed [#180]"
```

---

## Task 5: output-demux — %output routing across all windows

**Component:** output-demux (risk: low). **implement:** sonnet. **Run after Task 4** — this task's tests call `closeWindow`, which Task 4 introduces; despite the ∥ annotation, dispatch it sequentially after Task 4, never in parallel.

`Router` is already keyed by globally-unique remote pane id (`%N`), so the mechanism holds unchanged; this task is the lifecycle guarantee + tests: every pane of every mirrored window has a registered sink; window-close unregisters exactly that window's panes; output for an unregistered pane is dropped, not misrouted.

**Files:**
- Modify: `picker/remotebridge/daemon/router_test.go`
- (production sink registration already lands in Tasks 3/4 via `setupWindow`/`closeWindow`; this task verifies and hardens it. If `closeWindow` from Task 4 does not already unregister each pane, add it here.)

**Interfaces:**
- Consumes: `Router.Register`/`Unregister`/`Route` (frozen), `registry`/`mirrorWindow`.
- Produces: no new API. Tests only, plus (if missing) the per-pane `Unregister` loop in `closeWindow`.

- [ ] **Step 1: Write the failing multi-window routing test.** Add to `router_test.go`:

```go
func TestRouterDemuxAcrossWindows(t *testing.T) {
	r := NewRouter()
	var a, b capBuf // capBuf from daemon_test.go (same package)
	r.Register("%1", &a) // window @1's pane
	r.Register("%9", &b) // window @2's pane
	r.Route("%1", []byte("A1"))
	r.Route("%9", []byte("B9"))
	r.Route("%99", []byte("DROP")) // no sink registered
	if a.String() != "A1" || b.String() != "B9" {
		t.Fatalf("misrouted: a=%q b=%q", a.String(), b.String())
	}
	// Unregister %1 (its window closed); further output for it is dropped.
	r.Unregister("%1")
	r.Route("%1", []byte("X"))
	if a.String() != "A1" {
		t.Errorf("output after unregister leaked: %q", a.String())
	}
}
```

(Move the `capBuf` helper to package scope if it is defined inside a test function; it is currently a package-level type in `daemon_test.go`, so it is already visible.)

- [ ] **Step 2: Run to verify it passes or fails.**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestRouterDemuxAcrossWindows -v`
Expected: PASS if `closeWindow` (Task 4) already unregisters per pane; if this exposes a gap (e.g. `closeWindow` closed conns but forgot `Unregister`), it FAILs — fix `closeWindow` to `Unregister` each `mw.remotePanes` id, then re-run to PASS.

- [ ] **Step 3: Verify window-close unregisters all of the closed window's panes.** Add a focused test that builds a `registry` with a two-pane window, registers both panes' sinks, calls `closeWindow`, and asserts both are unregistered (route-after-close is dropped) while a *sibling* window's pane still routes. (Use a fake `Config` whose `LocalTmux` is a no-op recording closure — mirror the existing daemon_test seams; `kill-window` need not run for real.)

```go
func TestCloseWindowUnregistersOnlyItsPanes(t *testing.T) {
	reg := newRegistry(1)
	w1 := reg.add("@1", "h-s:1")
	w1.remotePanes = []string{"%1", "%2"}
	w2 := reg.add("@2", "h-s:2")
	w2.remotePanes = []string{"%9"}
	router := NewRouter()
	var s1, s2, s9 capBuf
	router.Register("%1", &s1)
	router.Register("%2", &s2)
	router.Register("%9", &s9)
	cfg := Config{LocalTmux: func(...string) error { return nil }}

	closeWindow(cfg, router, reg, "@1")

	router.Route("%1", []byte("x"))
	router.Route("%2", []byte("y"))
	router.Route("%9", []byte("z"))
	if s1.String() != "" || s2.String() != "" {
		t.Errorf("closed window's panes still routed: %q %q", s1.String(), s2.String())
	}
	if s9.String() != "z" {
		t.Errorf("sibling window's pane stopped routing: %q", s9.String())
	}
	if _, ok := reg.byRemoteID("@1"); ok {
		t.Error("@1 still in registry after close")
	}
}
```

- [ ] **Step 4: Run the demux tests to verify they pass.**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestRouterDemux|TestCloseWindowUnregisters' -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add picker/remotebridge/daemon/router_test.go picker/remotebridge/daemon/daemon.go
git commit -m "test(remotebridge): %output demux + per-window unregister across windows [#180]"
```

---

## Task 6: pause-reseed — pause-after backpressure + %continue re-seed (I1)

**Component:** pause-reseed (risk: **high**). **implement: opus.**

Now that every pane of every window streams, enable `pause-after` flow control. This breaks M2.1's "seed is written before the pump starts, so no per-conn mutex" invariant — a mid-stream re-seed is a second writer — so **all daemon→conn writes for a pane serialize through that pane's sink** (typed frames through the sink channel). `FrameResize` (Task 2's renderer side) rides the same path.

**Files:**
- Modify: `picker/remotebridge/daemon/daemon.go` (`outputSink` → typed-frame sink; `%pause`/`%continue` in the loop; seed + resize enqueued through the sink), `picker/remotebridge/daemon/seed.go` (call sites), `picker/remotebridge/cmd/daemon/main.go` (attach `pause-after`).
- Test: `picker/remotebridge/daemon/daemon_test.go`

**Interfaces:**
- Consumes: `wire.FrameSeed`/`FrameOutput`/`FrameResize`, `wire.EncodeResize`, `readReplyRouting`, `PaneSeed` (routing-aware), `registry`.
- Produces:
  - Typed-frame sink: `outputSink` carries `chan sinkFrame` where `type sinkFrame struct { typ wire.FrameType; payload []byte }`; the pump writes each via `wire.WriteFrame(conn, f.typ, f.payload)`. `Write([]byte)` (the `io.Writer` the router uses) enqueues a `FrameOutput` frame; a new method `enqueue(typ, payload)` enqueues seed/resize frames. Register-then-enqueue-seed preserves seed-first ordering (FIFO channel).
  - `(*outputSink) pause()` / `(*outputSink) resume()` gating flag: while paused, `Write` drops (tmux is discarding remote-side anyway); overflow (full buffer) marks the sink dirty for re-seed.
  - `pauseAfterSecs` default + `refresh-client -f pause-after=<n>` (or attach flag) on the control client.
  - `%pause %N` / `%continue %N` handling in `Run`'s loop.

- [ ] **Step 1: Write the failing re-seed ordering test.** Add to `daemon_test.go`. This asserts a `%continue` re-seed writes a fresh `FrameSeed` to the pane's conn **before** any subsequent routed output, and that sibling output is not dropped during the re-seed round-trip (B3):

```go
func TestPauseContinueReseedsBeforeResumingOutput(t *testing.T) {
	// A fake control stream: pane %2 keeps streaming; pane %1 is paused then
	// continued. On %continue, the daemon must capture-pane (%begin..%end) and
	// write that as a FrameSeed to %1's conn before routing %1's next output.
	stream := strings.Join([]string{
		"%pause %1",
		"%output %2 sibling",              // must still reach %2
		"%continue %1",                    // triggers re-seed round-trip for %1
		"%begin 1 1 0",
		"FRESH-CAPTURE",
		"%end 1 1 0",
		"%output %1 after-continue",       // must arrive AFTER the seed frame
		"%exit",
	}, "\n") + "\n"
	// Drive handleContinue/handlePause directly against a registry + a
	// pipe-backed sink for %1 and a capBuf for %2, then assert %1's conn sees
	// FrameSeed(FRESH-CAPTURE...) before FrameOutput(after-continue), and %2
	// recorded "sibling".
	// ... construct reader, router, reg with %1 -> pipe sink, %2 -> capBuf ...
}
```

Implement the test body concretely against whatever loop-entry seam Task 3/4 expose (drive `Run`'s per-line handlers, or a small extracted `handlePause`/`handleContinue`). Read `%1`'s conn frames with `wire.ReadFrame` and assert the order: `FrameSeed` payload contains `FRESH-CAPTURE`, then `FrameOutput` payload is `after-continue`; assert the `%2` sink recorded `sibling`.

- [ ] **Step 2: Run to verify it fails.**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestPauseContinueReseedsBeforeResumingOutput -v`
Expected: FAIL — pause/continue handling and the typed-frame sink don't exist.

- [ ] **Step 3: Convert `outputSink` to typed frames.** Rework in `daemon.go`:

```go
type sinkFrame struct {
	typ     wire.FrameType
	payload []byte
}

type outputSink struct {
	mu     sync.Mutex
	ch     chan sinkFrame
	closed bool
	paused bool
	dirty  bool // overflow / pause happened: %continue must re-seed
}

func newOutputSink(conn net.Conn) *outputSink {
	s := &outputSink{ch: make(chan sinkFrame, outputSinkBuf)}
	go func() {
		for f := range s.ch {
			if err := wire.WriteFrame(conn, f.typ, f.payload); err != nil {
				return
			}
		}
	}()
	return s
}

// Write is the router-facing io.Writer path: it enqueues a FrameOutput. While
// paused, output is dropped (tmux is discarding remote-side) and the sink is
// marked dirty so %continue re-seeds. A full buffer marks dirty and drops too.
func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return len(p), nil
	}
	if s.paused {
		s.dirty = true
		return len(p), nil
	}
	select {
	case s.ch <- sinkFrame{typ: wire.FrameOutput, payload: append([]byte(nil), p...)}:
	default:
		s.dirty = true // overflow: %continue-style re-seed instead of silent loss
	}
	return len(p), nil
}

// enqueue serializes a non-output frame (seed, resize) through the same pump so
// it never races the output writer (frozen wire invariant). It MUST NOT block:
// a stalled (not dead) renderer whose buffer is full would otherwise wedge the
// control-stream loop, violating the decomposition invariant "one wedged
// renderer must never stall the control-stream loop." So it uses the same
// bounded non-blocking select + drop-to-dirty discipline as Write; a dropped
// seed/resize marks the sink dirty so the next %continue re-seeds it.
func (s *outputSink) enqueue(typ wire.FrameType, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- sinkFrame{typ: typ, payload: append([]byte(nil), payload...)}:
	default:
		s.dirty = true
	}
}

func (s *outputSink) pause()  { s.mu.Lock(); s.paused = true; s.mu.Unlock() }
func (s *outputSink) resume() { s.mu.Lock(); s.paused = false; s.dirty = false; s.mu.Unlock() }
```

`Close()` stays (guards `closed`, closes `ch`). Both `Write` (output) and `enqueue` (seed/resize) push through the **same** bounded non-blocking `select`, so no frame type can wedge the control-stream loop on a stalled renderer — a full buffer drops and marks the sink dirty, and the next `%continue` re-seeds. Keep the buffered channel sized at `outputSinkBuf` (4096).

- [ ] **Step 4: Route seed + resize through the sink.** Change `seedRenderer` so that after `router.Register(remotePane, sink)` it calls `sink.enqueue(wire.FrameSeed, seed)` **instead of** `wire.WriteFrame(conn, wire.FrameSeed, seed)` — register first, enqueue seed as the sink's first frame (FIFO preserves seed-before-output). Immediately after, `sink.enqueue(wire.FrameResize, wire.EncodeResize(w, h))` (Task 2's live `FrameResize`).

  **Dims source (name it explicitly — `seedRenderer`/`PaneSeed` have no access to the layout `L`):** thread the pane's `controlmode.PaneCell` into the seed path. Add a `dims controlmode.PaneCell` param to `seedRenderer`; its callers `setupWindow` and `reconcileLayout` both already hold the parsed `Layout` and iterate `L.Panes` in creation order, so they pass `L.Panes[i]` for pane `i`. Then `(w, h) = (dims.W, dims.H)`. Do the same resize enqueue after each applied `select-layout` in `reconcileLayout`, sourcing `(w,h)` from that pane's `L.Panes[i]` in the freshly parsed layout. To reach the sink from `seedRenderer`, have it return or accept the `*outputSink` (it already constructs it).

- [ ] **Step 5: Handle `%pause`/`%continue` in `Run`'s loop.** Add cases:

```go
		case controlmode.Pause:
			if s := router.sink(l.Args[0]); s != nil {
				s.pause()
				send(fmt.Sprintf("refresh-client -A '%s:continue'", l.Args[0]))
			}
		case controlmode.Continue:
			if s := router.sink(l.Args[0]); s != nil {
				// Fresh capture (routing-aware — sibling panes stream) then a
				// FrameSeed BEFORE resuming output (closes the %pause hole).
				reply := func(r *controlmode.Reader) (controlmode.Line, bool) { return readReplyRouting(r, router) }
				if seed, err := PaneSeed(reader, send, l.Args[0], reply); err == nil {
					s.enqueue(wire.FrameSeed, seed)
				}
				s.resume()
			}
```

Add a `Router.sink(paneID string) *outputSink` accessor (returns the registered sink cast to `*outputSink`, or nil) — this stays within the Router contract (a read helper; `Register`/`Unregister`/`Route` are unchanged). If the reviewer prefers not to widen Router, track sinks in a daemon-side `map[string]*outputSink` populated alongside `router.Register`; either is acceptable — pick the smaller diff.

- [ ] **Step 6: Set `pause-after` on the control client — only after entering the main loop.** Add a `--pause-after` flag (default `envIntDefault("LZTMUX_DAEMON_PAUSE_AFTER", 1)` seconds) and pass it via `Config.PauseAfterSecs int`. In `Run`, send `refresh-client -f pause-after=%d` (guarded to only send when > 0) **only after all windows are set up and `Run` has entered its main loop** — i.e. immediately before / as the first action of the `for { reader.Next() }` loop, NOT right after the initial `ConvergeCmd`.

  **Why (deadlock — offline bats won't catch it):** `%pause`/`%continue` are only handled inside the main loop, and setup does blocking `collectHellos`/seed round-trips (up to ~10s per window) without draining the async stream. If `pause-after` is enabled before setup finishes, on the real g5→tp-g6 latency path panes get `%pause`'d mid-setup and are never resumed → frozen renderers (endangers the manual DoD). Enabling it only once every window is set up and the loop is draining the stream guarantees a `%pause` is always answered by a `%continue` re-seed.

  Document that `pause-after` on next-3.8 pauses a pane's `%output` after N seconds of the client not reading — the daemon reads continuously, so this is backpressure insurance, exercised in tests by forcing `%pause`/`%continue`.

- [ ] **Step 7: Run the pause/continue test + full suite.**

Run: `cd picker && go build ./... && go test ./remotebridge/... -v`
Expected: PASS, including `TestPauseContinueReseedsBeforeResumingOutput`. Existing anchors still green: `bats tests/remote-m2-integration.bats`.

- [ ] **Step 8: Commit.**

```bash
git add picker/remotebridge/daemon/daemon.go picker/remotebridge/daemon/seed.go picker/remotebridge/daemon/daemon_test.go picker/remotebridge/cmd/daemon/main.go
git commit -m "feat(remotebridge): pause-after backpressure + %continue re-seed via serialized sink [#180]"
```

---

## Task 7: launcher-wire — lztmux-remote-open runs the daemon

**Component:** launcher-wire (risk: med). **implement:** sonnet.

Rewire `scripts/lztmux-remote-open.sh` from the M1 bridge to the M2 daemon so `lztmux-remote-open tp-g6` is the user-reachable multi-window mirror. Preserve the security posture: remote-derived values ride `LZTMUX_*` env, never interpolated into a shell string. Per I4, the daemon launches **detached, outside the panes it manages** (it respawns local panes into renderers, so it cannot be a pane command).

**Files:**
- Modify: `scripts/lztmux-remote-open.sh`
- (must-not-touch: `scripts/lztmux-remote-shim.sh`, `scripts/lztmux-listener.sh`, `picker/remotebridge/main.go`, `config/tmux.conf.nix`.)

**Interfaces:**
- Consumes: `lztmux-remote-bridge-daemon` (env-driven flags from Task 3/6: `LZTMUX_BRIDGE_*`, `LZTMUX_DAEMON_LOCAL_SESS`, `LZTMUX_DAEMON_SOCK`, `LZTMUX_DAEMON_RENDERER`), `lztmux-remote-bridge-renderer` (absolute store path).
- Produces: a running detached daemon owning the `<host>-<sess>` session; `switch-client` to it.

Keep intact from the current script: numeric-window arg validation, `TMUX_TMPDIR=/run/user/$(ssh host id -u)`, the #171 remote-tmux-path fallback (`command -v tmux || /etc/profiles/per-user/$(id -un)/bin/tmux`), the #172 active-window resolution (now the initially-selected window), default-session resolution, and env passthrough.

- [ ] **Step 1: Rewrite the launch tail of the script.** Replace the `tmux new-session ... lztmux-remote-bridge` + `switch-client` block (current lines 34–42). The head (arg parse, `remote_tmpdir`, `remote_tmux`, `sess`, `win` resolution) is unchanged. New tail:

```bash
local_sess="${host}-${sess}"
sock="${TMUX_TMPDIR:-/tmp}/lztmux-daemon-${local_sess}.sock"
# Absolute store path: pane PATH is stale until server restart, and the daemon
# respawns panes into this binary, so resolve it now on the (fresh) caller PATH.
renderer="$(command -v lztmux-remote-bridge-renderer)"

# Create the local session with a single initial window; the daemon reuses it
# for the first remote window and creates the rest.
tmux new-session -d -s "$local_sess" -n "$sess"

# Launch the daemon DETACHED, outside the panes it manages (I4): it is not the
# window's command — it respawns the local panes into renderers. Remote-derived
# (untrusted) params ride the environment, never interpolated into a command
# string tmux/ssh would re-parse.
LZTMUX_BRIDGE_HOST="$host" \
	LZTMUX_BRIDGE_SESSION="$sess" \
	LZTMUX_BRIDGE_WINDOW="$win" \
	LZTMUX_BRIDGE_TMUX="$remote_tmux" \
	LZTMUX_BRIDGE_TMPDIR="$remote_tmpdir" \
	LZTMUX_DAEMON_LOCAL_SESS="$local_sess" \
	LZTMUX_DAEMON_SOCK="$sock" \
	LZTMUX_DAEMON_RENDERER="$renderer" \
	setsid lztmux-remote-bridge-daemon >/dev/null 2>&1 &

tmux switch-client -t "=$local_sess"
```

Update the script's top comment to describe the M2.2 multi-window daemon (drop the "M1: single window" line).

**Note (detach portability):** `setsid` is Linux-only (not in macOS base). The manual DoD runs on Linux (g5→tp-g6), so `setsid` is fine to ship. But since the rest of the lazytmux CLI is cross-platform, prefer a portable detach form where practical — e.g. plain `... &` with `disown`, or a `command -v setsid` guard falling back to backgrounding — and leave a comment flagging the Linux-only assumption if `setsid` is kept.

- [ ] **Step 2: Run shellcheck.**

Run: `shellcheck scripts/lztmux-remote-open.sh`
Expected: no warnings/errors. Add targeted `# shellcheck disable=` only where the existing script already does (the intentional client-side `ssh` expansions), matching the existing style.

- [ ] **Step 3: Run shfmt (tabs, project default).**

Run: `shfmt -d scripts/lztmux-remote-open.sh`
Expected: no diff (or apply `shfmt -w` then re-check).

- [ ] **Step 4: Sanity-check the daemon CLI shape the launcher relies on.** Confirm every env var the script sets maps to a flag default in `cmd/daemon/main.go` (Task 3/6): `LZTMUX_BRIDGE_HOST/SESSION/WINDOW/TMUX/TMPDIR`, `LZTMUX_DAEMON_LOCAL_SESS/SOCK/RENDERER`.

Run: `cd picker && go run ./remotebridge/cmd/daemon --help 2>&1 | grep -E 'host|session|window|local-sess|sock|renderer'`
Expected: each flag present with the documented env default.

- [ ] **Step 5: Commit.**

```bash
git add scripts/lztmux-remote-open.sh
git commit -m "feat(remotebridge): lztmux-remote-open launches the M2 multi-window daemon [#180]"
```

---

## Task 8: bats-multiwin — offline multi-window integration test

**Component:** bats-multiwin (risk: med). **implement:** sonnet.

Extend `tests/remote-m2-integration.bats` (same two-`tmux -L`-server `--test-local` seam, no ssh). Add: (a) a multi-window "remote" session mirroring to one local window per remote window with per-window pane-dim convergence; (b) live reflection — remote `new-window` / `kill-window` / `rename-window` appear locally; (c) the DST≠SRC convergence case; (d) keep the existing 1-pane M1-anchor and 2-pane cases untouched.

**Files:**
- Modify: `tests/remote-m2-integration.bats`
- (must-not-touch: `tests/remote-integration.bats`, `tests/remote-bridge-integration.bats`, `tests/enrich.bats`.)

**Critical config parity (this bit the M2.1 smoke test):** the SRC server's config must match the remote render config — `pane-border-status top`, `status`, `base-index 1`, `pane-base-index 1` — or dims won't converge. The existing `setup()` sets `SRC="tmux -L m2src -f /dev/null"` (no config) and only DST gets `base-index 1`. Add a `SRC_CONF` with the full render config and point `SRC` at it; keep DST's `base-index 1` + `remain-on-exit on`.

- [ ] **Step 1: Add `SRC_CONF` in `setup()`.** After the `DST_CONF` block:

```bash
	SRC_CONF="$BATS_TEST_TMPDIR/src.conf"
	printf 'set -g base-index 1\nset -g pane-base-index 1\nset -g status on\nset -g pane-border-status top\n' >"$SRC_CONF"
	SRC="tmux -L m2src -f $SRC_CONF" # stands in for the "remote", full render config
```

Verify the existing 1-pane/2-pane/reconcile cases still pass with SRC now carrying config (they assert dim equality, which the shared config only makes more accurate).

**Also update the three existing cases' `--window 0` → `--window 1`.** Once SRC carries `base-index 1` there is no window index 0, so `--window 0` names a nonexistent window. This is harmless for mirroring (per the Task 3 fix, `--window` is no longer a mirror filter — it only picks the initially-selected window), but `--window 1` matches the new base-index and actually exercises the initial-selection index→id resolution. The three current call sites use `--window 0` (grep confirms lines ~64/85/105); change each to `--window 1`.

- [ ] **Step 2: Write the multi-window startup mirror test.**

```bash
@test "daemon mirrors a 3-window remote session into 3 local windows" {
	$SRC new-session -d -s rem -x 100 -y 30
	$SRC new-window -t rem
	$SRC new-window -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	run timeout 12 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dm.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ]

	src_wins="$($SRC list-windows -t rem -F '#{window_id}' | wc -l)"
	dst_wins="$($DST list-windows -t host-sess -F '#{window_id}' | wc -l)"
	[ "$src_wins" -eq 3 ]
	[ "$dst_wins" -eq 3 ]
}
```

- [ ] **Step 3: Write the live add/close/rename reflection test** (poll-then-kill, bounded — mirror the existing reconcile case's structure, ~gate on renderer wired, cap loops at `seq 1 40` × 0.1s):

```bash
@test "daemon reflects remote new-window / rename-window / kill-window" {
	$SRC new-session -d -s rem -x 100 -y 30
	$DST new-session -d -s host-sess -x 100 -y 30

	"$DAEMON" --test-local --src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dr.sock" \
		>"$BATS_TEST_TMPDIR/dr.log" 2>&1 &
	daemon_pid=$!

	# Gate: wait until the first window's pane is a renderer (daemon in its loop).
	for _ in $(seq 1 40); do
		cmd="$($DST list-panes -t host-sess:1 -F '#{pane_current_command}' 2>/dev/null)"
		[[ $cmd == *renderer* ]] && break
		sleep 0.1
	done

	# Add a remote window -> a new local window appears.
	$SRC new-window -t rem
	for _ in $(seq 1 40); do
		n="$($DST list-windows -t host-sess -F '#{window_id}' 2>/dev/null | wc -l)"
		[ "$n" -eq 2 ] && break
		sleep 0.1
	done
	[ "$n" -eq 2 ]

	# Rename it remotely -> local window name follows.
	newwin="$($SRC list-windows -t rem -F '#{window_id}' | tail -1)"
	$SRC rename-window -t "$newwin" bridged-name
	for _ in $(seq 1 40); do
		names="$($DST list-windows -t host-sess -F '#{window_name}' 2>/dev/null)"
		[[ $names == *bridged-name* ]] && break
		sleep 0.1
	done
	[[ $names == *bridged-name* ]]

	# Kill the added remote window -> its local window goes away (session survives).
	$SRC kill-window -t "$newwin"
	for _ in $(seq 1 40); do
		n="$($DST list-windows -t host-sess -F '#{window_id}' 2>/dev/null | wc -l)"
		[ "$n" -eq 1 ] && break
		sleep 0.1
	done
	[ "$n" -eq 1 ]

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true
}
```

- [ ] **Step 4: Write the DST≠SRC convergence test** (M2.1-review follow-up — DST created at a *different* size so `ConvergeCmd` actually resizes the remote instead of being a no-op):

```bash
@test "daemon converges when DST size != SRC size (ConvergeCmd resizes remote)" {
	# remote starts 120x40; local mirror created at 100x30 — the daemon's
	# refresh-client -C must push 100x30 onto the remote so pane dims converge.
	$SRC new-session -d -s rem -x 120 -y 40
	$SRC split-window -h -t rem
	$DST new-session -d -s host-sess -x 100 -y 30

	run timeout 12 "$DAEMON" --test-local \
		--src-socket m2src --dst-socket m2dst \
		--session rem --window 1 --local-sess host-sess \
		--renderer "$RENDERER" --sock "$BATS_TEST_TMPDIR/dc.sock"
	[ "$status" -eq 0 ] || [ "$status" -eq 124 ]

	src_dims="$(sorted_dims "$SRC" rem)"
	dst_dims="$(sorted_dims "$DST" host-sess:1)"
	[ -n "$src_dims" ]
	[ "$src_dims" = "$dst_dims" ]
	# And the remote actually shrank to the local width (convergence, not no-op).
	[ "$($SRC display-message -p -t rem -F '#{window_width}')" -eq 100 ]
}
```

- [ ] **Step 5: Run the full bats suite.**

Run: `bats tests/remote-m2-integration.bats`
Expected: all cases PASS — the 3 existing anchors + the 3 new cases. Total wall time in the ~18s ballpark (each `timeout` is 10–12s; the poll loops break early). If it drifts higher, tighten the timeouts.

- [ ] **Step 6: Commit.**

```bash
git add tests/remote-m2-integration.bats
git commit -m "test(remotebridge): offline multi-window mirror + live add/close/rename + DST!=SRC convergence [#180]"
```

---

## Task 9: flake-check — nix flake check wiring

**Component:** flake-check (risk: low). **implement:** sonnet.

The `remote-m2-integration-tests` check already copies `tests/` and pins `DAEMON`/`RENDERER` to the prebuilt `pickerAgentDetect` binaries; `buildGoModule` runs the Go unit tests. This task is verification plus whatever the new bats cases need (extra `nativeBuildInputs`). No new subPackages; no `picker/default.nix` vendorHash change (no new Go deps).

**Files:**
- Modify (only if a new bats dependency is required): `flake.nix` (checks attrset only, around line 322–338).

**Interfaces:**
- Consumes: `pickerAgentDetect` binaries, the bats file from Task 8.
- Produces: a green `nix flake check`.

- [ ] **Step 1: Check whether the new bats cases use any binary not already in `nativeBuildInputs`.** Current list: `pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep pkgs.tmux`. The Task 8 cases use `printf`, `wc`, `tail`, `seq`, `sleep`, `awk`-free polling, `tmux`, `timeout` — all from coreutils/tmux already present.

Run: `rg -n 'awk|jq|util-linux|script ' tests/remote-m2-integration.bats`
Expected: no matches that require a new dep (if `timeout` came from coreutils, it's covered; `util-linux`'s `script` is only used by the *bridge* check, not this one).

- [ ] **Step 2: Run the isolated M2 check.**

Run: `nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').remote-m2-integration-tests -L`
Expected: builds green; bats output shows all 6 cases passing. (If it fails on a missing binary, add it to that check's `nativeBuildInputs` — the only edit this task may make.)

- [ ] **Step 3: Run the full gate.**

Run: `nix flake check`
Expected: all checks green, including `remote-m2-integration-tests` and the `buildGoModule` Go unit tests (parser, wire, render, daemon/registry/translate/router/pause-reseed).

- [ ] **Step 4: Commit (only if `flake.nix` changed).**

```bash
git add flake.nix
git commit -m "chore(flake): keep remote-m2 integration check green for multi-window bats [#180]"
```

---

## Final: deslop + PR

- [ ] **Deslop the branch** (`superpowers`/`deslop` skill or the `deslop` slash command): remove AI-slop comments, defensive blocks, and hallucinated APIs across the whole diff before the PR.
- [ ] **Full verification pass** (evidence before claiming done):
  - `cd picker && go build ./... && go test ./remotebridge/...` — green
  - `bats tests/remote-m2-integration.bats` — 6/6 green
  - `shellcheck scripts/lztmux-remote-open.sh` — clean
  - `nix flake check` — green
- [ ] **Open the PR** (commit from inside `nix develop` so hooks ran). Body references **#167**, `Closes #180`, and states the **manual test a human must run**: on g5, `lztmux-remote-open tp-g6` over the personal tailnet — assert all remote windows appear as local windows, a remote split appears live, and rename/close are reflected; confirm 1-pane windows are byte-identical to M1.

```bash
gh pr create --assignee @me --title "feat(remotebridge): M2.2 — mirror all remote windows + wire lztmux-remote-open [#180]" --body "<per above>"
```

---

## Self-Review (run against the sources before execution)

**Spec coverage (WORKER_TASK scope 1–5):**
1. All remote windows mirrored → Task 3 (registry + enumerate + per-window setup).
2. Live structure via native notifications (I5) incl. close-filter-to-target-window → Task 4 (`translate.go` + `addWindow`/`closeWindow`, B2 filter).
3. `pause-after` + mandatory `%continue` re-seed (I1) → Task 6.
4. Wire daemon into `lztmux-remote-open` → Task 7.
5. Local navigation between mirrored windows + typing → Tasks 3/4 (`select-window` reflection, `pumpInput` per pane) + Task 8 reflection test.

**M2.1-review follow-ups:** window-close filter → Task 4 (B2). DST≠SRC convergence test → Task 8 Step 4. `FrameResize` end-to-end → Task 2 (renderer) + Task 6 (daemon emit through the sink). CI-time-bounded poll-then-kill → Task 8 (bounded loops + trimmed timeouts).

**Testing requirements:** notification→command translation (Task 4); `%output` demux across windows (Task 5); layout→`select-layout` convergence (existing `mirror_test.go` + Task 8 dims); `%pause`/`%continue` re-seed (Task 6); parser cases (Task 1); `FrameResize` round-trip (Task 2); offline multi-window bats sharing SRC render config (Task 8); `nix flake check` (Task 9).

**Interface preservation:** wire frame layout/types unchanged (Task 2 touches renderer.go + tests, not protocol.go); `controlmode.Line`/`Kind` additive (Task 1 appends); `daemon.Config` injection + `--test-local`/env defaults preserved (Task 3 additive `BaseIndex`, `RemoteWindow` repurposed not removed); Router API unchanged (Task 5 tests only; Task 6's `sink()` is a read accessor or a daemon-side map); 1-pane ≡ M1 (Tasks 3/8 anchors); directional size authority (single `ConvergeCmd`, renderers never `refresh-client`); `%window-close` no longer terminates the daemon (Task 4 stop-semantics flip).

**Placeholder scan:** no "TBD"/"handle edge cases"/"similar to Task N". Every code step shows the code. Two spots deliberately hand the exact test-body wiring to the implementer (Task 6 Step 1 `TestPauseContinueReseeds…` body, Task 8 poll loops) — each names the precise seam, assertion, and ordering to encode, not a vague "write a test".

**Type consistency:** `mirrorWindow`/`registry`/`allocLocalWin`/`byRemoteID`/`remove`/`empty`/`parseWindowList` used consistently (Tasks 3–5). `readReplyRouting(reader, router)` signature identical in Tasks 3/6. `translateWindowNotification(l, reg) ([]string, bool)` identical in Task 4. `render.Run(..., recordResize func(w,h int))` identical in Task 2. `sinkFrame{typ, payload}` / `outputSink.enqueue`/`pause`/`resume` identical in Task 6.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-07-20-remote-bridge-m2.2.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration. Tag `daemon-multiwin` (Task 3) and `pause-reseed` (Task 6) `implement: opus`; the rest run on sonnet.

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints for review.

**Which approach?**
