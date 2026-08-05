# Kitty Graphics Through The Remote Bridge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `prefix + I` inside a bridged remote window opens the aeye carousel as a mirrored remote split that renders **crisp images**, by teaching the bridge daemon to localise the file paths kitty graphics sequences carry.

**Architecture:** The graphics stream already crosses the bridge — tmux hands control clients raw pre-parse pty bytes, and kitty unicode placeholders are ordinary grid text. Only the store's `t=f` payload (a remote path) is host-bound. A new `remotebridge/graphics` package sits in each pane's output pump: it scans kitty APCs out of the byte stream, fetches the referenced remote file over a daemon-owned ssh ControlMaster, rewrites the payload to the local copy, and re-wraps once for the local tmux. Two upstream aeye changes (id namespacing, a network-cheap frame policy) ship first.

**Tech Stack:** Go 1.25 (`github.com/noamsto/lazytmux/picker`), tmux control mode, kitty graphics protocol, ssh ControlMaster, Nix (`config/tmux.conf.nix`), bats.

**Spec:** `docs/superpowers/specs/2026-08-05-bridge-graphics-design.md`. Decisions are cited as D1–D8, measurements as G1–G7.

---

## File Structure

**New — `picker/remotebridge/graphics/` (the whole feature's new logic):**

| File | Responsibility |
|------|----------------|
| `scan.go` | Byte scanner: split a pane's stream into literal runs and complete kitty APC sequences, holding a partial tail across calls. Knows both the bare and `\ePtmux;`-wrapped forms and nothing else. |
| `seq.go` | One decoded sequence: key lookup, canonical bare encoding, single-wrap encoding. |
| `rewrite.go` | Policy: which sequences need a local path, which pass through, which are dropped. Depends on the `Localizer` interface only. |
| `coalesce.go` | Drop superseded stores within one batch (D5). Pure function over `[]Chunk`. |
| `proxy.go` | `Filter([]byte) []byte` — ties scan → coalesce → rewrite together. The only type the daemon sees. |
| `fetch.go` | `SSHFetcher`: the `Localizer` implementation. Remote stat/cat over the ControlMaster socket, content cache, LRU prune, size cap. The only file that knows about ssh. |

**Modified:**

| File | Change |
|------|--------|
| `picker/remotebridge/daemon/daemon.go` | `Config.NewGraphics` seam; `newOutputSink` takes a proxy; the pump batch-drains queued output frames so coalescing has something to coalesce. |
| `picker/remotebridge/daemon/reconcile.go` | Pass the proxy at the second `seedRenderer` call site. |
| `picker/remotebridge/daemon/ctl.go` | New `carousel` verb. |
| `picker/remotebridge/cmd/daemon/main.go` | `-M -S` ControlMaster on the control ssh; `--term` steering; construct the real `SSHFetcher`. |
| `scripts/lztmux-remote-open.sh` | Pass `--term` (the local client's termname) to the daemon. |
| `config/tmux.conf.nix` | Gate `bind I` on `bridgeGate`. |
| `CLAUDE.md` | Document the proxy in the script/architecture tables. |

**Upstream (`noamsto/aeye`, separate repo, ships first):** `gallery.go` (`paneImageIDBase`), `gallery_zoom.go` (`storePreviewCrop`, `panFrameGap`).

---

## Task 0: Confirm the premise on a real ssh link

The spec's central claim (G1) was measured against a *local* second tmux server. The control-mode protocol is identical over ssh, but D1's whole architecture rests on it. Ten minutes here is cheaper than discovering it in Task 10.

**Files:** none (throwaway probe).

- [ ] **Step 1: Run the probe against the real remote host**

Replace `REMOTE` with the actual host (e.g. `tp-g6`). Run from the local machine:

```bash
ssh REMOTE 'tmux -f /dev/null new-session -d -s gfxprobe; tmux set -g allow-passthrough on'
ssh -T REMOTE 'tmux -C attach-session -t gfxprobe' > /tmp/ctl-remote.txt &
sleep 1
ssh REMOTE 'tmux send-keys -t gfxprobe "printf \"\033Ptmux;\033\033_Gi=31,a=q,q=2\033\033\\\\\\\\\033\\\\\"" Enter'
sleep 1
kill %1
ssh REMOTE 'tmux kill-session -t gfxprobe'
grep -c '_G' /tmp/ctl-remote.txt
```

Expected: a non-zero count, and a line of the form `%output %0 \033Ptmux;\033\033_Gi=31,a=q,q=2\033\033\134\033\134`.

- [ ] **Step 2: Decide**

If `_G` appears: proceed to Task 1. If it does **not**, stop — D1 is invalid and the design needs revisiting (the fallback is the spec's rejected "local carousel, synced files" option). Record the outcome in a comment on issue #280 either way.

---

## Task 1: aeye — namespace image ids by host

**Repo:** `noamsto/aeye` (issue #177). Work in its own worktree: `wt switch -c feat/177-foreign-host-store`.

**Files:**
- Modify: `gallery.go:83-89` (`paneImageIDBase`)
- Test: `gallery_test.go`

- [ ] **Step 1: Write the failing test**

Add to `gallery_test.go`:

```go
func TestPaneImageIDBaseDiffersByHost(t *testing.T) {
	a := paneImageIDBaseOn("mbp-m4-pro", "%5")
	b := paneImageIDBaseOn("tp-g6", "%5")
	if a == b {
		t.Fatalf("same id block for the same pane id on two hosts: %d", a)
	}
	if a != paneImageIDBaseOn("mbp-m4-pro", "%5") {
		t.Fatal("not deterministic for one host+pane")
	}
	if paneImageIDBaseOn("h", "%1")%(maxCellDim+1) != 0 {
		t.Fatal("block base must be a multiple of the block width")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./... -run TestPaneImageIDBaseDiffersByHost`
Expected: FAIL — `undefined: paneImageIDBaseOn`.

- [ ] **Step 3: Implement**

Replace `paneImageIDBase` in `gallery.go`, keeping its existing doc comment and extending it:

```go
// idBlocks bounds the hashed block index so the largest id stays inside the
// 24 bits a unicode placeholder's fg colour can carry.
const idBlocks = 0xFFFFFF / (maxCellDim + 1)

// paneImageIDBase maps a tmux pane id to the base of a disjoint kitty image-id
// block. Every carousel forwards its graphics to the one kitty store the
// terminal owns, so a fixed id would collide and one viewer's image would bleed
// into another's unicode placeholders. The hostname rides in the hash because
// pane ids are unique per tmux SERVER: a carousel rendered on a foreign host
// (lazytmux's remote bridge) reaches the same store through a second server,
// where %5 means something else entirely.
func paneImageIDBase(pane string) int {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return paneImageIDBaseOn(host, pane)
}

func paneImageIDBaseOn(host, pane string) int {
	h := fnv.New32a()
	h.Write([]byte(host))
	h.Write([]byte{0})
	h.Write([]byte(pane))
	return int(h.Sum32()%uint32(idBlocks)) * (maxCellDim + 1)
}
```

Add `"hash/fnv"` to the imports (`os` is already there).

- [ ] **Step 4: Run the tests**

Run: `go test ./...`
Expected: PASS. Any existing test asserting a literal id block value must be updated to compute it via `paneImageIDBaseOn` rather than hardcoding.

- [ ] **Step 5: Commit**

```bash
git add gallery.go gallery_test.go
git commit -m "fix: namespace kitty image-id blocks by host

Pane ids are unique per tmux server, so a carousel rendered on a foreign
host collides with a local one in the shared kitty store.

Refs #177"
```

---

## Task 2: aeye — network-cheap frame policy under AEYE_BRIDGED

**Repo:** `noamsto/aeye`, same worktree as Task 1.

**Files:**
- Modify: `gallery_zoom.go:251-263` (`storePreviewCrop`), `gallery_zoom.go:280` (`panFrameGap`)
- Test: `gallery_zoom_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestBridgedFramePolicy(t *testing.T) {
	if panFrameGapFor(false) != 8*time.Millisecond {
		t.Fatal("local gap changed")
	}
	if panFrameGapFor(true) <= 8*time.Millisecond {
		t.Fatal("bridged gap must be wider than the local one")
	}
	if !preferEncodedFrame(true) {
		t.Fatal("bridged must prefer the encoded (PNG) frame")
	}
	if preferEncodedFrame(false) {
		t.Fatal("local must keep the raw RGBA fast path")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./... -run TestBridgedFramePolicy`
Expected: FAIL — `undefined: panFrameGapFor`.

- [ ] **Step 3: Implement**

In `gallery_zoom.go`, replace the `panFrameGap` const block with:

```go
// panFrameGap is the minimum spacing between preview re-stores while dragging. A
// drag delivers motion faster than a frame costs, so without this the work queues
// up: the image trails the cursor and keeps moving after it stops. Throttling makes
// each frame render the CURRENT position instead of a stale backlogged one.
const panFrameGap = 8 * time.Millisecond

// bridgedPanFrameGap replaces it when the viewer renders onto a foreign host's
// terminal (lazytmux's remote bridge, AEYE_BRIDGED): there every frame is a file
// the bridge must fetch over ssh before the terminal can read it, so the cost per
// frame is a network round trip rather than a 2.6ms encode.
const bridgedPanFrameGap = 60 * time.Millisecond

// bridged reports whether this viewer's output is being relayed to another host's
// terminal. Set by lazytmux's bridge when it launches the carousel remotely.
func bridged() bool { return os.Getenv("AEYE_BRIDGED") != "" }

func panFrameGapFor(bridged bool) time.Duration {
	if bridged {
		return bridgedPanFrameGap
	}
	return panFrameGap
}

// preferEncodedFrame reports whether to spend a PNG encode to shrink the frame.
// Locally the raw RGBA write wins (2.5x faster per frame, and the terminal reads
// it straight off local disk); across a bridge the same frame is ~10-20x more
// bytes on the wire, which inverts the trade.
func preferEncodedFrame(bridged bool) bool { return bridged }
```

Then in `storePreviewCrop`, gate the raw branch:

```go
	b := dst.Bounds()
	if !preferEncodedFrame(m.bridged) {
		if out := writeRaw(m.zoomRawPath(), dst); out != "" {
			return transmitVirtualRaw(m.previewID(), out, b.Dx(), b.Dy(), m.l.previewW, m.l.previewH)
		}
	}
	return transmitVirtual(m.previewID(),
		writePNGEnc(m.zoomScratchPath(), dst, orig, fastPNG.Encode), m.l.previewW, m.l.previewH)
```

Add a `bridged bool` field to `galleryModel`, set it from `bridged()` where the model is constructed (alongside `dragNative` in `gallery.go`), and replace the `panFrameGap` use in `transmitPanFrame` with `panFrameGapFor(m.bridged)`.

- [ ] **Step 4: Run the tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit and release**

```bash
git add gallery.go gallery_zoom.go gallery_zoom_test.go
git commit -m "feat: network-cheap frame policy under AEYE_BRIDGED

Refs #177"
```

Open the PR, merge, and let release-please cut a tag — lazytmux's `flake.lock` bump (Task 14) needs a released rev.

---

## Task 3: graphics — the sequence scanner

**Files:**
- Create: `picker/remotebridge/graphics/scan.go`
- Test: `picker/remotebridge/graphics/scan_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import (
	"bytes"
	"testing"
)

const (
	bareSeq    = "\x1b_Gi=31,a=T,U=1,f=100,t=f;L3RtcC94LnBuZw==\x1b\\"
	wrappedSeq = "\x1bPtmux;\x1b\x1b_Gi=31,a=T,U=1,f=100,t=f;L3RtcC94LnBuZw==\x1b\x1b\\\x1b\\"
)

func chunkKinds(cs []Chunk) string {
	var b bytes.Buffer
	for _, c := range cs {
		if c.Seq != nil {
			b.WriteByte('S')
		} else {
			b.WriteByte('L')
		}
	}
	return b.String()
}

func TestScanBareSequence(t *testing.T) {
	cs := NewScanner().Feed([]byte("before" + bareSeq + "after"))
	if got := chunkKinds(cs); got != "LSL" {
		t.Fatalf("kinds = %q, want LSL", got)
	}
	if got := string(cs[1].Seq.Keys); got != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("keys = %q", got)
	}
	if got := string(cs[1].Seq.Payload); got != "L3RtcC94LnBuZw==" {
		t.Fatalf("payload = %q", got)
	}
	if cs[1].Seq.Wrapped {
		t.Fatal("bare sequence reported as wrapped")
	}
}

func TestScanWrappedSequenceUndoublesEscapes(t *testing.T) {
	cs := NewScanner().Feed([]byte(wrappedSeq))
	if got := chunkKinds(cs); got != "S" {
		t.Fatalf("kinds = %q, want S", got)
	}
	if !cs[0].Seq.Wrapped {
		t.Fatal("wrapped sequence not flagged")
	}
	if got := string(cs[0].Seq.Keys); got != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("keys = %q", got)
	}
}

func TestScanSequenceSplitAcrossFeeds(t *testing.T) {
	s := NewScanner()
	cut := len(bareSeq) / 2
	if cs := s.Feed([]byte(bareSeq[:cut])); chunkKinds(cs) != "" {
		t.Fatalf("partial sequence emitted early: %q", chunkKinds(cs))
	}
	cs := s.Feed([]byte(bareSeq[cut:]))
	if chunkKinds(cs) != "S" {
		t.Fatalf("kinds = %q, want S", chunkKinds(cs))
	}
}

func TestScanLiteralBeforePartialIsEmittedImmediately(t *testing.T) {
	cs := NewScanner().Feed([]byte("visible\x1b_Gi=1,a=T;abc"))
	if chunkKinds(cs) != "L" || string(cs[0].Literal) != "visible" {
		t.Fatalf("kinds = %q, first = %q", chunkKinds(cs), cs[0].Literal)
	}
}

func TestScanForwardsANonGraphicsPassthroughImmediately(t *testing.T) {
	// A clipboard escape is a passthrough too. Holding it because it isn't ours
	// would stall every later byte on the pane behind it.
	in := "\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\tail"
	cs := NewScanner().Feed([]byte(in))
	var got string
	for _, c := range cs {
		if c.Seq != nil {
			t.Fatal("OSC 52 decoded as a graphics sequence")
		}
		got += string(c.Literal)
	}
	if got != in {
		t.Fatalf("forwarded %q, want the input verbatim", got)
	}
}

func TestScanRecoversPositionAfterANonGraphicsPassthrough(t *testing.T) {
	cs := NewScanner().Feed([]byte("\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\" + bareSeq))
	if got := chunkKinds(cs); got != "LS" {
		t.Fatalf("kinds = %q, want LS — the scanner desynced", got)
	}
}

func TestScanOversizedPartialFlushesAsLiteral(t *testing.T) {
	s := NewScanner()
	s.Feed([]byte("\x1b_Gi=1,a=T;"))
	cs := s.Feed(bytes.Repeat([]byte("A"), maxPartial+1))
	if chunkKinds(cs) != "L" {
		t.Fatalf("kinds = %q, want L (give up, forward verbatim)", chunkKinds(cs))
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: FAIL — the package doesn't exist.

- [ ] **Step 3: Implement**

Create `picker/remotebridge/graphics/scan.go`:

```go
// Package graphics localises kitty graphics sequences crossing the remote
// bridge. A store sent by a program on the remote host references a file by
// path (t=f), which the LOCAL terminal cannot read; this package rewrites that
// payload to a local copy. The placement half needs nothing: kitty unicode
// placeholders are ordinary grid text and already cross the bridge.
package graphics

// maxPartial bounds the bytes held waiting for a sequence terminator. A frame
// dropped by the sink's bounded buffer can truncate a sequence mid-flight, and
// without a cap the scanner would swallow the pane's stream forever waiting for
// an ST that was already discarded. On overflow the held bytes are forwarded
// verbatim: a garbled escape beats a dead pane.
const maxPartial = 64 << 10

const (
	apcStart  = "\x1b_G"
	passStart = "\x1bPtmux;"
	st        = "\x1b\\"
)

// Chunk is one piece of a pane's byte stream: either a literal run to forward
// untouched, or one decoded graphics sequence.
type Chunk struct {
	Literal []byte
	Seq     *Seq
}

// Scanner splits a pane's byte stream into Chunks across successive Feed calls,
// holding an incomplete trailing sequence until the rest arrives.
type Scanner struct{ held []byte }

func NewScanner() *Scanner { return &Scanner{} }

// Feed consumes p and returns every chunk that completed. Bytes belonging to a
// sequence whose terminator hasn't arrived are retained for the next call.
func (s *Scanner) Feed(p []byte) []Chunk {
	buf := p
	if len(s.held) > 0 {
		buf = append(s.held, p...)
		s.held = nil
	}
	var out []Chunk
	for len(buf) > 0 {
		i := indexSeqStart(buf)
		if i < 0 {
			out = appendLiteral(out, buf)
			return out
		}
		if i > 0 {
			out = appendLiteral(out, buf[:i])
			buf = buf[i:]
		}
		seq, n := decodeSeq(buf)
		switch {
		case n == 0:
			// Incomplete: hold it, unless it has outgrown the cap.
			if len(buf) > maxPartial {
				return appendLiteral(out, buf)
			}
			s.held = append([]byte(nil), buf...)
			return out
		case seq == nil:
			// A complete passthrough carrying something else (OSC 52, …).
			// Forward it verbatim: it is already wrapped for one tmux layer,
			// which is exactly what the renderer's local tmux needs, so the
			// escape reaches the outer terminal and does what its sender meant.
			out = appendLiteral(out, buf[:n])
		default:
			out = append(out, Chunk{Seq: seq})
		}
		buf = buf[n:]
	}
	return out
}

// Flush emits any held partial sequence as a literal. Called when a pane's sink
// closes, so held bytes are never silently swallowed.
func (s *Scanner) Flush() []Chunk {
	if len(s.held) == 0 {
		return nil
	}
	out := []Chunk{{Literal: s.held}}
	s.held = nil
	return out
}

func appendLiteral(out []Chunk, b []byte) []Chunk {
	if len(b) == 0 {
		return out
	}
	return append(out, Chunk{Literal: append([]byte(nil), b...)})
}

// indexSeqStart returns the offset of the next graphics sequence start (bare or
// passthrough-wrapped), or -1.
func indexSeqStart(b []byte) int {
	best := -1
	for _, pat := range []string{apcStart, passStart} {
		if i := indexOf(b, pat); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

func indexOf(b []byte, pat string) int {
	for i := 0; i+len(pat) <= len(b); i++ {
		if string(b[i:i+len(pat)]) == pat {
			return i
		}
	}
	return -1
}
```

Create `picker/remotebridge/graphics/seq.go`:

```go
package graphics

import "bytes"

// Seq is one decoded kitty graphics APC: the control keys before the ';' and
// the payload after it. Wrapped records whether it arrived inside a tmux
// passthrough, for diagnostics only — output is always wrapped exactly once,
// because the renderer pane always sits inside the local tmux.
type Seq struct {
	Keys    []byte
	Payload []byte
	HasBody bool // a ';' was present (an empty payload is distinct from none)
	Wrapped bool
}

// decodeSeq decodes the sequence at the head of b:
//
//	seq != nil, n > 0  — a graphics sequence; consume n
//	seq == nil, n > 0  — a COMPLETE sequence that isn't ours (a passthrough
//	                     carrying something else, e.g. OSC 52); forward b[:n]
//	                     verbatim and move past it
//	seq == nil, n == 0 — incomplete; hold for more bytes
//
// The middle case is why this returns a length rather than an ok bool. "Not
// complete yet" and "complete, but not mine" both mean "no sequence here", but
// conflating them stalls the pane: a clipboard escape would hold every later
// byte behind it until the partial cap or Flush.
func decodeSeq(b []byte) (*Seq, int) {
	if bytes.HasPrefix(b, []byte(passStart)) {
		inner, n, ok := unwrapPassthrough(b)
		if !ok {
			return nil, 0
		}
		q, m, ok := decodeBare(inner)
		// The sequence must fill the wrapper exactly. A wrapper carrying
		// anything after its first sequence is treated as not-ours and forwarded
		// whole: decoding only the first would silently drop the rest, and a
		// proxy must never lose bytes it was asked to relay. The cost is that
		// such a store goes unlocalised (blank image, D7) — a case aeye cannot
		// produce, since tmuxPassthrough wraps exactly one sequence.
		if !ok || m != len(inner) {
			return nil, n
		}
		q.Wrapped = true
		return q, n
	}
	// Feed only calls this at an indexSeqStart hit, so a non-passthrough head is
	// necessarily an apcStart and decodeBare can only fail for want of the ST —
	// hence 0 (hold). Hence also no exact-fill guard on this path: only a wrapper
	// has a boundary to fill, and bytes after a bare sequence are simply the next
	// chunk to scan.
	q, n, ok := decodeBare(b)
	if !ok {
		return nil, 0
	}
	return q, n
}

func decodeBare(b []byte) (*Seq, int, bool) {
	if !bytes.HasPrefix(b, []byte(apcStart)) {
		return nil, 0, false
	}
	end := indexOf(b[len(apcStart):], st)
	if end < 0 {
		return nil, 0, false
	}
	body := b[len(apcStart) : len(apcStart)+end]
	q := &Seq{}
	if i := bytes.IndexByte(body, ';'); i >= 0 {
		q.Keys = append([]byte(nil), body[:i]...)
		q.Payload = append([]byte(nil), body[i+1:]...)
		q.HasBody = true
	} else {
		q.Keys = append([]byte(nil), body...)
	}
	return q, len(apcStart) + end + len(st), true
}

// unwrapPassthrough un-doubles the ESCs of a \ePtmux;… wrapper and returns the
// inner sequence plus the bytes consumed. Scanning for the first ST would cut at
// the INNER terminator (\e\e\\ contains \e\\ at its second byte), so the ESCs are
// un-doubled as we walk instead.
func unwrapPassthrough(b []byte) ([]byte, int, bool) {
	i := len(passStart)
	var inner []byte
	for i < len(b) {
		if b[i] == 0x1b {
			if i+1 >= len(b) {
				return nil, 0, false
			}
			if b[i+1] == 0x1b {
				inner = append(inner, 0x1b)
				i += 2
				continue
			}
			if b[i+1] == '\\' {
				return inner, i + 2, true
			}
		}
		inner = append(inner, b[i])
		i++
	}
	return nil, 0, false
}
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): scan kitty graphics sequences out of a pane stream

Refs #280"
```

---

## Task 4: graphics — key lookup and single-wrap encoding

**Files:**
- Modify: `picker/remotebridge/graphics/seq.go`
- Test: `picker/remotebridge/graphics/seq_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import "testing"

func TestSeqGet(t *testing.T) {
	cs := NewScanner().Feed([]byte(bareSeq))
	q := cs[0].Seq
	if got := q.Get("t"); got != "f" {
		t.Fatalf("t = %q, want f", got)
	}
	if got := q.Get("i"); got != "31" {
		t.Fatalf("i = %q, want 31", got)
	}
	if got := q.Get("a"); got != "T" {
		t.Fatalf("a = %q, want T", got)
	}
	if got := q.Get("zz"); got != "" {
		t.Fatalf("absent key = %q, want empty", got)
	}
}

func TestEncodeWrappedIsAlwaysExactlyOneWrapper(t *testing.T) {
	for _, in := range []string{bareSeq, wrappedSeq} {
		q := NewScanner().Feed([]byte(in))[0].Seq
		out := string(q.EncodeWrapped())
		if n := countSub(out, passStart); n != 1 {
			t.Fatalf("input %q: %d wrappers, want 1", in, n)
		}
		// Round-trips: unwrapping the output yields the canonical bare form.
		back := NewScanner().Feed([]byte(out))[0].Seq
		if string(back.Keys) != string(q.Keys) || string(back.Payload) != string(q.Payload) {
			t.Fatalf("round trip lost data: %q / %q", back.Keys, back.Payload)
		}
	}
}

// HasBody is the only field Encode branches on, and every other fixture here
// carries a payload — so without this the false path is unexercised. It matters
// for a=d deletes, which carry no payload and which the proxy re-emits through
// EncodeWrapped: a spurious ';' would hand the terminal a different sequence.
func TestEncodePreservesPresenceOfTheSeparator(t *testing.T) {
	keysOnly := NewScanner().Feed([]byte("\x1b_Ga=d,d=A\x1b\\"))[0].Seq
	if got := string(keysOnly.Encode()); got != "\x1b_Ga=d,d=A\x1b\\" {
		t.Fatalf("Encode = %q, want no separator added", got)
	}
	emptyBody := NewScanner().Feed([]byte("\x1b_Ga=d,d=A;\x1b\\"))[0].Seq
	if got := string(emptyBody.Encode()); got != "\x1b_Ga=d,d=A;\x1b\\" {
		t.Fatalf("Encode = %q, want the separator kept", got)
	}
}

func countSub(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -run 'TestSeqGet|TestEncodeWrapped' -v`
Expected: FAIL — `q.Get undefined`, `q.EncodeWrapped undefined`.

- [ ] **Step 3: Implement**

Append to `seq.go`:

```go
// Get returns the value of a comma-separated control key ("t", "i", "a", …),
// or "" when absent.
func (q *Seq) Get(key string) string {
	for _, kv := range bytes.Split(q.Keys, []byte{','}) {
		i := bytes.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		if string(kv[:i]) == key {
			return string(kv[i+1:])
		}
	}
	return ""
}

// Encode renders the canonical bare form.
func (q *Seq) Encode() []byte {
	out := append([]byte(apcStart), q.Keys...)
	if q.HasBody {
		out = append(out, ';')
		out = append(out, q.Payload...)
	}
	return append(out, st...)
}

// EncodeWrapped renders the bare form inside exactly one tmux passthrough,
// whatever the input form was: the renderer pane always sits inside the local
// tmux, which needs one wrapper to unwrap to the outer terminal.
func (q *Seq) EncodeWrapped() []byte {
	inner := q.Encode()
	out := make([]byte, 0, len(passStart)+2*len(inner)+len(st))
	out = append(out, passStart...)
	for _, c := range inner {
		if c == 0x1b {
			out = append(out, 0x1b)
		}
		out = append(out, c)
	}
	return append(out, st...)
}
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): key lookup and single-wrap encoding for graphics sequences

Refs #280"
```

---

## Task 5: graphics — the rewrite policy

**Files:**
- Create: `picker/remotebridge/graphics/rewrite.go`
- Test: `picker/remotebridge/graphics/rewrite_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import (
	"encoding/base64"
	"errors"
	"testing"
)

type fakeLocalizer struct {
	local string
	err   error
	asked []string
}

func (f *fakeLocalizer) Localize(remote string) (string, error) {
	f.asked = append(f.asked, remote)
	return f.local, f.err
}

func seqOf(t *testing.T, raw string) *Seq {
	t.Helper()
	cs := NewScanner().Feed([]byte(raw))
	if len(cs) != 1 || cs[0].Seq == nil {
		t.Fatalf("not a single sequence: %q", raw)
	}
	return cs[0].Seq
}

func TestRewriteLocalisesFilePayload(t *testing.T) {
	f := &fakeLocalizer{local: "/local/cache/abc.bin"}
	q, drop, err := Rewrite(seqOf(t, bareSeq), f)
	if err != nil || drop {
		t.Fatalf("drop=%v err=%v", drop, err)
	}
	if len(f.asked) != 1 || f.asked[0] != "/tmp/x.png" {
		t.Fatalf("asked = %v, want [/tmp/x.png]", f.asked)
	}
	got, _ := base64.StdEncoding.DecodeString(string(q.Payload))
	if string(got) != "/local/cache/abc.bin" {
		t.Fatalf("payload = %q", got)
	}
	if q.Get("i") != "31" || q.Get("c") != "" || string(q.Keys) != "i=31,a=T,U=1,f=100,t=f" {
		t.Fatalf("control keys mutated: %q", q.Keys)
	}
}

func TestRewritePassesThroughInlineAndDelete(t *testing.T) {
	for _, raw := range []string{
		"\x1b_Gi=31,a=T,U=1,f=100,t=d;aGVsbG8=\x1b\\",
		"\x1b_Ga=d,d=I,i=31,q=2\x1b\\",
	} {
		f := &fakeLocalizer{}
		q, drop, err := Rewrite(seqOf(t, raw), f)
		if err != nil || drop {
			t.Fatalf("%q: drop=%v err=%v", raw, drop, err)
		}
		if len(f.asked) != 0 {
			t.Fatalf("%q: fetched needlessly", raw)
		}
		if string(q.Encode()) != raw {
			t.Fatalf("%q: mutated to %q", raw, q.Encode())
		}
	}
}

func TestRewriteDropsSharedMemoryAndFetchFailures(t *testing.T) {
	if _, drop, _ := Rewrite(seqOf(t, "\x1b_Gi=1,a=T,t=s;c2htMQ==\x1b\\"), &fakeLocalizer{}); !drop {
		t.Fatal("t=s must be dropped: shared memory cannot cross hosts")
	}
	_, drop, err := Rewrite(seqOf(t, bareSeq), &fakeLocalizer{err: errors.New("no such file")})
	if !drop || err == nil {
		t.Fatal("a failed fetch must drop the store, never emit a stale local path")
	}
	if _, drop, _ := Rewrite(seqOf(t, "\x1b_Gi=1,a=T,t=f;!!!not-base64!!!\x1b\\"), &fakeLocalizer{}); !drop {
		t.Fatal("undecodable payload must be dropped")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -run TestRewrite -v`
Expected: FAIL — `undefined: Rewrite`.

- [ ] **Step 3: Implement**

Create `rewrite.go`:

```go
package graphics

import (
	"encoding/base64"
	"fmt"
)

// Localizer turns a path on the remote host into a path the local terminal can
// read. Injected so the policy below is testable without ssh.
type Localizer interface {
	Localize(remotePath string) (localPath string, err error)
}

// Rewrite applies the localisation policy to one sequence.
//
// The governing rule (spec D7) is that a store whose payload could not be
// localised is DROPPED, never forwarded: a stale local path renders the wrong
// image, where a missing one renders blank and self-heals on the sender's next
// repaint.
func Rewrite(q *Seq, l Localizer) (out *Seq, drop bool, err error) {
	switch q.Get("t") {
	case "f", "t":
		remote, derr := base64.StdEncoding.DecodeString(string(q.Payload))
		if derr != nil {
			return nil, true, fmt.Errorf("payload is not base64: %w", derr)
		}
		local, ferr := l.Localize(string(remote))
		if ferr != nil {
			return nil, true, fmt.Errorf("localise %s: %w", remote, ferr)
		}
		cp := *q
		cp.Payload = []byte(base64.StdEncoding.EncodeToString([]byte(local)))
		return &cp, false, nil
	case "s":
		// Shared memory is host-local by definition.
		return nil, true, nil
	default:
		// t=d carries its own bytes; a=d and friends carry no payload at all.
		return q, false, nil
	}
}

// isStore reports whether q transmits image data under an id — the sequences
// coalescing may supersede.
func isStore(q *Seq) bool {
	switch q.Get("a") {
	case "T", "t":
		return q.Get("i") != ""
	}
	return false
}

// isDelete reports whether q deletes an image by id.
func isDelete(q *Seq) bool { return q.Get("a") == "d" && q.Get("i") != "" }
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): localise t=f payloads, drop what cannot cross

Refs #280"
```

---

## Task 6: graphics — coalesce superseded stores

**Files:**
- Create: `picker/remotebridge/graphics/coalesce.go`
- Test: `picker/remotebridge/graphics/coalesce_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import "testing"

func store(id, path string) Chunk {
	return Chunk{Seq: seqLit("\x1b_Gi=" + id + ",a=T,U=1,f=100,t=f;" + path + "\x1b\\")}
}

func del(id string) Chunk {
	return Chunk{Seq: seqLit("\x1b_Ga=d,d=I,i=" + id + ",q=2\x1b\\")}
}

func seqLit(raw string) *Seq {
	return NewScanner().Feed([]byte(raw))[0].Seq
}

func TestCoalesceKeepsOnlyTheNewestStorePerID(t *testing.T) {
	in := []Chunk{store("1", "YQ=="), store("1", "Yg=="), store("2", "Yw==")}
	out := Coalesce(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if string(out[0].Seq.Payload) != "Yg==" {
		t.Fatalf("kept the stale frame: %q", out[0].Seq.Payload)
	}
}

func TestCoalesceNeverCrossesADelete(t *testing.T) {
	in := []Chunk{store("1", "YQ=="), del("1"), store("1", "Yg==")}
	if out := Coalesce(in); len(out) != 3 {
		t.Fatalf("len = %d, want 3 — a delete is a real transition, not a superseded frame", len(out))
	}
}

func TestCoalescePreservesLiteralsAndOrder(t *testing.T) {
	in := []Chunk{{Literal: []byte("a")}, store("1", "YQ=="), {Literal: []byte("b")}, store("1", "Yg==")}
	out := Coalesce(in)
	if len(out) != 3 || string(out[0].Literal) != "a" || string(out[1].Literal) != "b" {
		t.Fatalf("literals reordered or lost: %+v", out)
	}
	if string(out[2].Seq.Payload) != "Yg==" {
		t.Fatalf("wrong survivor: %q", out[2].Seq.Payload)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -run TestCoalesce -v`
Expected: FAIL — `undefined: Coalesce`.

- [ ] **Step 3: Implement**

Create `coalesce.go`:

```go
package graphics

// Coalesce drops stores that a later store in the same batch supersedes.
//
// This is safe because of how a viewer pans: each store re-places under the same
// id with a=T and no delete, so a store is a full replacement and the
// intermediate frames are pure waste — fetching them would make the image trail
// the cursor. Dropping them lets the LINK set the frame rate, with the newest
// framing always the survivor.
//
// A delete for an id ends the supersede chain: it is a real state transition,
// not a stale frame, so the store before it must still be forwarded.
func Coalesce(in []Chunk) []Chunk {
	superseded := make([]bool, len(in))
	seen := map[string]bool{} // image id -> a later store exists
	for i := len(in) - 1; i >= 0; i-- {
		q := in[i].Seq
		if q == nil {
			continue
		}
		id := q.Get("i")
		switch {
		case isDelete(q):
			delete(seen, id)
		case isStore(q):
			if seen[id] {
				superseded[i] = true
				continue
			}
			seen[id] = true
		}
	}
	out := make([]Chunk, 0, len(in))
	for i, c := range in {
		if !superseded[i] {
			out = append(out, c)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): coalesce superseded image stores within a batch

Refs #280"
```

---

## Task 7: graphics — the Proxy

**Files:**
- Create: `picker/remotebridge/graphics/proxy.go`
- Test: `picker/remotebridge/graphics/proxy_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import (
	"strings"
	"testing"
)

func TestProxyRewritesAndWrapsInOnePass(t *testing.T) {
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	out := string(p.Filter([]byte("hello " + bareSeq + " bye")))
	if !strings.HasPrefix(out, "hello ") || !strings.HasSuffix(out, " bye") {
		t.Fatalf("literals lost: %q", out)
	}
	if countSub(out, passStart) != 1 {
		t.Fatalf("want exactly one wrapper: %q", out)
	}
	if !strings.Contains(out, "L2xvY2FsL2EuYmlu") { // base64("/local/a.bin")
		t.Fatalf("payload not localised: %q", out)
	}
}

func TestProxyDropsWhatItCannotLocalise(t *testing.T) {
	var logged int
	p := New(&fakeLocalizer{err: errFake}, func(string, ...any) { logged++ })
	out := string(p.Filter([]byte("x" + bareSeq + "y")))
	if out != "xy" {
		t.Fatalf("out = %q, want the literals only", out)
	}
	if logged == 0 {
		t.Fatal("a dropped sequence must be logged")
	}
}

func TestProxyCarriesPartialSequencesAcrossCalls(t *testing.T) {
	p := New(&fakeLocalizer{local: "/local/a.bin"}, nil)
	cut := len(bareSeq) / 2
	if got := string(p.Filter([]byte(bareSeq[:cut]))); got != "" {
		t.Fatalf("emitted a partial sequence: %q", got)
	}
	if got := string(p.Filter([]byte(bareSeq[cut:]))); countSub(got, passStart) != 1 {
		t.Fatalf("second half did not complete the sequence: %q", got)
	}
}
```

Add to `rewrite_test.go`: `var errFake = errors.New("fake")`.

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -run TestProxy -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

Create `proxy.go`:

```go
package graphics

// Proxy filters one pane's output stream. It is owned by that pane's output
// sink and called only from the sink's pump goroutine, so it needs no locking —
// and it may block there: holding one pane's stream at a sequence boundary is
// what keeps a store ahead of the placements that reference it (spec D4).
type Proxy struct {
	sc   *Scanner
	loc  Localizer
	logf func(format string, args ...any)
}

func New(loc Localizer, logf func(format string, args ...any)) *Proxy {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Proxy{sc: NewScanner(), loc: loc, logf: logf}
}

// Filter returns the bytes to forward to the renderer. An incomplete trailing
// sequence is held until the next call.
func (p *Proxy) Filter(data []byte) []byte {
	chunks := Coalesce(p.sc.Feed(data))
	var out []byte
	for _, c := range chunks {
		if c.Seq == nil {
			out = append(out, c.Literal...)
			continue
		}
		q, drop, err := Rewrite(c.Seq, p.loc)
		if drop {
			if err != nil {
				p.logf("graphics: dropped i=%s: %v", c.Seq.Get("i"), err)
			} else {
				p.logf("graphics: dropped i=%s (t=%s cannot cross hosts)", c.Seq.Get("i"), c.Seq.Get("t"))
			}
			continue
		}
		out = append(out, q.EncodeWrapped()...)
	}
	return out
}

// Close flushes any held partial sequence so it isn't swallowed when the pane
// goes away.
func (p *Proxy) Close() []byte {
	var out []byte
	for _, c := range p.sc.Flush() {
		out = append(out, c.Literal...)
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): graphics proxy over a pane's output stream

Refs #280"
```

---

## Task 8: graphics — the ssh fetcher

**Files:**
- Create: `picker/remotebridge/graphics/fetch.go`
- Test: `picker/remotebridge/graphics/fetch_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graphics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetcherWritesBytesToCacheAndReturnsLocalPath(t *testing.T) {
	dir := t.TempDir()
	var gotArgs []string
	f := &SSHFetcher{
		Host: "g6", CtlSock: "/run/x.sock", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(args ...string) ([]byte, error) {
			gotArgs = args
			return []byte("1700000000 5\nHELLO"), nil
		},
	}
	local, err := f.Localize("/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(local)
	if err != nil || string(b) != "HELLO" {
		t.Fatalf("cached content = %q err=%v", b, err)
	}
	if filepath.Dir(local) != dir {
		t.Fatalf("wrote outside the cache dir: %s", local)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-S /run/x.sock") || !strings.Contains(joined, "g6") {
		t.Fatalf("did not use the ControlMaster socket: %v", gotArgs)
	}
}

func TestFetcherSecondCallIsAHitAndTransfersNothing(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{
		Host: "g6", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("1700000000 5\nHELLO"), nil
			}
			// Same mtime+size: the remote script prints the key and exits
			// without cat-ing.
			return []byte("1700000000 5\n"), nil
		},
	}
	first, err := f.Localize("/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Localize("/tmp/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cache miss on an unchanged file: %s vs %s", first, second)
	}
}

func TestFetcherTreatsAChangedMtimeAsANewFile(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{
		Host: "g6", CacheDir: dir, MaxBytes: 1 << 20,
		Run: func(args ...string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte("1700000000 1\nA"), nil
			}
			return []byte("1700000009 1\nB"), nil
		},
	}
	first, _ := f.Localize("/tmp/scratch.raw")
	second, _ := f.Localize("/tmp/scratch.raw")
	if first == second {
		t.Fatal("a rewritten scratch frame must not reuse the old cache entry")
	}
	b, _ := os.ReadFile(second)
	if string(b) != "B" {
		t.Fatalf("second content = %q", b)
	}
}

func TestFetcherRejectsOversizeAndBadReplies(t *testing.T) {
	dir := t.TempDir()
	// Over the cap the remote script exits 3 without cat-ing, which surfaces as
	// a non-zero ssh exit.
	over := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 4, Run: func(...string) ([]byte, error) {
		return nil, errors.New("exit status 3")
	}}
	if _, err := over.Localize("/tmp/big.raw"); err == nil {
		t.Fatal("oversize fetch must error so the store is dropped")
	}
	bad := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(...string) ([]byte, error) {
		return []byte("garbage"), nil
	}}
	if _, err := bad.Localize("/tmp/a.png"); err == nil {
		t.Fatal("unparsable reply must error")
	}
}

func TestFetcherRecoversWhenTheCachedCopyIsGone(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	f := &SSHFetcher{Host: "g6", CacheDir: dir, MaxBytes: 1 << 20, Run: func(...string) ([]byte, error) {
		calls++
		if calls == 2 {
			return []byte("1700000000 1\n"), nil // header only: "you already have it"
		}
		return []byte("1700000000 1\nA"), nil
	}}
	local, _ := f.Localize("/tmp/a.png")
	os.Remove(local) // pruned, or the daemon restarted
	if _, err := f.Localize("/tmp/a.png"); err == nil {
		t.Fatal("a lost cache entry must error once")
	}
	if _, err := f.Localize("/tmp/a.png"); err != nil {
		t.Fatalf("and then recover by refetching, got %v", err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/graphics/ -run TestFetcher -v`
Expected: FAIL — `undefined: SSHFetcher`.

- [ ] **Step 3: Implement**

Create `fetch.go`:

```go
package graphics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// remoteFetch runs on the remote host. It prints "<mtime> <size>" first, then
// the bytes — but only when the caller's cached key differs and the file is
// within the cap. One round trip covers stat, cache validation and transfer,
// and a cache hit transfers nothing but the header.
//
// $1 path, $2 the caller's cached "<mtime> <size>" (empty if none), $3 max bytes.
const remoteFetch = `p=$1; k=$2; m=$3
s=$(stat -c '%Y %s' -- "$p" 2>/dev/null || stat -f '%m %z' -- "$p") || exit 1
printf '%s\n' "$s"
[ "$s" = "$k" ] && exit 0
sz=${s#* }
[ "$sz" -gt "$m" ] && exit 3
exec cat -- "$p"`

// cachePrune is the total-bytes ceiling for the fetch cache, and how often
// (in fetches) it is enforced.
const (
	cacheCap      = 256 << 20
	pruneInterval = 64
)

// SSHFetcher localises a remote path by copying it into a local cache over the
// daemon's ssh ControlMaster socket, so no fetch pays a new handshake and image
// bytes never share the control stream with live terminal output.
type SSHFetcher struct {
	Host     string
	CtlSock  string
	CacheDir string
	MaxBytes int64
	// Run executes ssh; injected so tests never touch the network.
	Run func(args ...string) ([]byte, error)

	mu      sync.Mutex
	keys    map[string]string // remote path -> last seen "<mtime> <size>"
	locals  map[string]string // "<path>\x00<key>" -> local file
	fetches int
}

// NewSSHFetcher builds the production fetcher.
func NewSSHFetcher(host, ctlSock, cacheDir string, maxBytes int64) *SSHFetcher {
	f := &SSHFetcher{Host: host, CtlSock: ctlSock, CacheDir: cacheDir, MaxBytes: maxBytes}
	f.Run = func(args ...string) ([]byte, error) { return exec.Command("ssh", args...).Output() }
	_ = os.MkdirAll(cacheDir, 0o700)
	f.prune()
	return f
}

func (f *SSHFetcher) Localize(remote string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys == nil {
		f.keys, f.locals = map[string]string{}, map[string]string{}
	}
	if err := os.MkdirAll(f.CacheDir, 0o700); err != nil {
		return "", err
	}

	args := []string{}
	if f.CtlSock != "" {
		args = append(args, "-S", f.CtlSock)
	}
	args = append(args, "-T", f.Host, "--", "sh", "-c", shQuote(remoteFetch), "_",
		shQuote(remote), shQuote(f.keys[remote]), strconv.FormatInt(f.MaxBytes, 10))

	out, err := f.Run(args...)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", remote, err)
	}
	nl := strings.IndexByte(string(out), '\n')
	if nl < 0 {
		return "", fmt.Errorf("fetch %s: no header in reply", remote)
	}
	key := strings.TrimSpace(string(out[:nl]))
	if len(strings.Fields(key)) != 2 {
		return "", fmt.Errorf("fetch %s: bad header %q", remote, key)
	}
	body := out[nl+1:]

	ck := remote + "\x00" + key
	if len(body) == 0 {
		// The remote skipped the transfer because our cached key matched.
		if local, ok := f.locals[ck]; ok {
			if _, statErr := os.Stat(local); statErr == nil {
				f.keys[remote] = key
				return local, nil
			}
		}
		// We claimed a copy we no longer have (daemon restart, pruned entry).
		// Forget the key, or every later call would keep asking for the same
		// no-op transfer and this path would never recover.
		delete(f.keys, remote)
		return "", fmt.Errorf("fetch %s: cached copy is gone, refetching next time", remote)
	}
	f.keys[remote] = key

	sum := sha256.Sum256([]byte(ck))
	local := filepath.Join(f.CacheDir, hex.EncodeToString(sum[:])[:32]+".bin")
	if err := os.WriteFile(local, body, 0o600); err != nil {
		return "", err
	}
	f.locals[ck] = local
	if f.fetches++; f.fetches%pruneInterval == 0 {
		f.prune()
	}
	return local, nil
}

// prune drops the oldest cache entries until the directory is under cacheCap.
func (f *SSHFetcher) prune() {
	ents, err := os.ReadDir(f.CacheDir)
	if err != nil {
		return
	}
	type ent struct {
		path string
		size int64
		mod  int64
	}
	var all []ent
	var total int64
	for _, e := range ents {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		all = append(all, ent{filepath.Join(f.CacheDir, e.Name()), info.Size(), info.ModTime().UnixNano()})
		total += info.Size()
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod < all[j].mod })
	for _, e := range all {
		if total <= cacheCap {
			return
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// shQuote single-quotes s for the remote login shell: ssh space-joins the
// post-host argv into one string the remote shell re-parses, so every argument
// has to survive that second parse intact.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/graphics/ -v`
Expected: PASS (all tasks 3-8 tests).

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/graphics/
git commit -m "feat(bridge): fetch remote image bytes over the ControlMaster socket

Refs #280"
```

---

## Task 9: daemon — batch-drain the pump and hook the proxy in

Coalescing can only drop a superseded store if it can *see* the newer one, so the pump hands the proxy every output frame already queued behind the one it woke on. Draining stops at the first non-output frame: reordering a seed or resize past output would break the frozen wire invariant.

**Files:**
- Modify: `picker/remotebridge/daemon/daemon.go` (Config, `newOutputSink`, `seedRenderer`)
- Modify: `picker/remotebridge/daemon/reconcile.go:167`
- Test: `picker/remotebridge/daemon/daemon_test.go`

- [ ] **Step 1: Write the failing test**

Add to `daemon_test.go`:

```go
func TestOutputSinkFiltersAndCoalescesThroughTheProxy(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	p := graphics.New(&stubLocalizer{local: "/local/a.bin"}, nil)
	s := newOutputSink(remote, p)
	defer s.Close()

	// Two stores for the same id, queued before the pump can drain them.
	seq := func(payload string) []byte {
		return []byte("\x1b_Gi=7,a=T,U=1,f=100,t=f;" + payload + "\x1b\\")
	}
	s.Write(seq("L3RtcC9hLnBuZw==")) // /tmp/a.png
	s.Write(seq("L3RtcC9iLnBuZw==")) // /tmp/b.png

	got := readAllFrames(t, local, 500*time.Millisecond)
	if n := strings.Count(got, "\x1bPtmux;"); n != 1 {
		t.Fatalf("forwarded %d stores, want 1 (the newest); got %q", n, got)
	}
	if !strings.Contains(got, "L2xvY2FsL2EuYmlu") {
		t.Fatalf("payload not localised: %q", got)
	}
}

type stubLocalizer struct{ local string }

func (s *stubLocalizer) Localize(string) (string, error) { return s.local, nil }
```

Add the helper alongside it (no such helper exists yet):

```go
// readAllFrames reads frames off conn until it goes quiet for the deadline and
// returns their concatenated payloads.
func readAllFrames(t *testing.T, conn net.Conn, quiet time.Duration) string {
	t.Helper()
	var out []byte
	for {
		if err := conn.SetReadDeadline(time.Now().Add(quiet)); err != nil {
			t.Fatal(err)
		}
		f, err := wire.ReadFrame(conn)
		if err != nil {
			return string(out)
		}
		out = append(out, f.Payload...)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestOutputSinkFilters -v`
Expected: FAIL — `too many arguments in call to newOutputSink`.

- [ ] **Step 3: Implement**

In `daemon.go`, add to `Config` (after `LocalArea`):

```go
	// NewGraphics builds the per-pane kitty-graphics proxy that localises image
	// payloads crossing the bridge. nil disables proxying entirely (tests, and
	// any transport where there is no remote filesystem to fetch from).
	NewGraphics func(paneID string) *graphics.Proxy
```

Add a helper next to it:

```go
func (c Config) graphicsFor(paneID string) *graphics.Proxy {
	if c.NewGraphics == nil {
		return nil
	}
	return c.NewGraphics(paneID)
}
```

Replace `newOutputSink`:

```go
func newOutputSink(conn net.Conn, gfx *graphics.Proxy) *outputSink {
	s := &outputSink{ch: make(chan sinkFrame, outputSinkBuf)}
	go func() {
		var pending *sinkFrame
		for {
			var f sinkFrame
			if pending != nil {
				f, pending = *pending, nil
			} else {
				v, ok := <-s.ch
				if !ok {
					return
				}
				f = v
			}
			if gfx != nil && f.typ == wire.FrameOutput {
				buf := append([]byte(nil), f.payload...)
				buf, pending = drainOutput(s.ch, buf)
				f.payload = gfx.Filter(buf)
				if len(f.payload) == 0 {
					continue
				}
			}
			if err := wire.WriteFrame(conn, f.typ, f.payload); err != nil {
				return
			}
		}
	}()
	return s
}

// drainOutput appends every FrameOutput already queued to buf, so the proxy sees
// a whole burst at once and can drop stores a later frame supersedes. It stops
// at the first non-output frame and hands it back to be written next: seeds and
// resizes must not be reordered past output (frozen wire invariant).
func drainOutput(ch chan sinkFrame, buf []byte) ([]byte, *sinkFrame) {
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return buf, nil
			}
			if v.typ != wire.FrameOutput {
				return buf, &v
			}
			buf = append(buf, v.payload...)
		default:
			return buf, nil
		}
	}
}
```

Add `gfx *graphics.Proxy` as the last parameter of `seedRenderer` and pass it through:

```go
func seedRenderer(reader *controlmode.Reader, send func(string), router *Router, conn net.Conn, remotePane string, reply replyFn, dims controlmode.PaneCell, gfx *graphics.Proxy) bool {
	...
	sink := newOutputSink(conn, gfx)
```

Update both call sites (both enclosing functions already carry `cfg`):

- `daemon.go:402` → `if !seedRenderer(reader, send, router, conn, remotePane, reply, L.Panes[i], cfg.graphicsFor(remotePane)) {`
- `reconcile.go:167` → `if seedRenderer(reader, send, router, c, id, reply, L.Panes[indexOf(newRemote, id)], cfg.graphicsFor(id)) {`

Update `daemon_test.go:146` to `newOutputSink(oneLocal, nil)`. Import `"github.com/noamsto/lazytmux/picker/remotebridge/graphics"` in `daemon.go` and the test.

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/...`
Expected: PASS, including the pre-existing daemon suite.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/
git commit -m "feat(bridge): filter pane output through the graphics proxy

Refs #280"
```

---

## Task 10: daemon — ControlMaster socket, TERM steering, real fetcher

**Files:**
- Modify: `picker/remotebridge/cmd/daemon/main.go:27-77`
- Modify: `scripts/lztmux-remote-open.sh`

- [ ] **Step 1: Add the flags and wire the ssh options**

In `main.go`, after the `sshCmd` flag:

```go
	term := flag.String("term", os.Getenv("LZTMUX_BRIDGE_TERM"), "termname to advertise to the remote (steers the remote viewer's graphics backend)")
	cacheDir := flag.String("gfx-cache", envDefault("LZTMUX_BRIDGE_GFX_CACHE", filepath.Join(os.TempDir(), "lztmux-gfx")), "local cache dir for images fetched from the remote")
	gfxMax := flag.Int64("gfx-max-bytes", 8<<20, "largest single image fetched from the remote; bigger stores are dropped")
```

Replace the ssh branch (lines 68-75) with:

```go
			// ControlMaster on the control connection makes every image fetch a
			// multiplexed exec on this same TCP connection: no second handshake,
			// and image bytes never share the control stream with live output.
			// ControlPersist=no ties the master's lifetime to this process.
			ctlSock = fmt.Sprintf("%s/lztmux-bridge-%d.sock", os.TempDir(), os.Getpid())
			args := []string{"-T", "-e", "none",
				"-o", "ControlMaster=auto", "-o", "ControlPath=" + ctlSock, "-o", "ControlPersist=no",
				*host, "--", "env", "TMUX_TMPDIR=" + *tmpdir}
			// TERM decides the remote viewer's graphics backend: it reads
			// #{client_termname}, which is whatever this control client
			// advertises. A non-kitty local terminal therefore degrades the
			// remote carousel to block art on its own.
			if *term != "" {
				args = append(args, "TERM="+*term)
			}
			args = append(args, tmuxArgv...)
			args = append(args, "-C", "attach-session", "-t", shellQuote(*session))
			ctl = exec.Command(*sshCmd, args...)
```

Declare `var ctlSock string` alongside `var ctl *exec.Cmd`, and add `"path/filepath"` to the imports.

- [ ] **Step 2: Build the fetcher into the Config**

In the `cfg := daemon.Config{` literal (`cmd/daemon/main.go:110`), add:

```go
		NewGraphics: func(string) *graphics.Proxy {
			if ctlSock == "" {
				return nil // --test-local / local-tmux transport: no remote filesystem
			}
			return graphics.New(
				graphics.NewSSHFetcher(*host, ctlSock, *cacheDir, *gfxMax),
				func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
			)
		},
```

Import the graphics package.

- [ ] **Step 3: Pass the local termname from the launcher**

The daemon is launched with no argv — every parameter arrives through the environment (`scripts/lztmux-remote-open.sh:73-81`). Add one more export next to `LZTMUX_DAEMON_RENDERER`, before the launch block:

```bash
# The remote viewer picks its graphics backend from #{client_termname}, which is
# whatever the daemon's ssh advertises — so hand it the termname of the terminal
# that will actually paint the pixels. Empty (no client) is fine: the remote then
# falls back to block art, which renders anywhere.
export LZTMUX_BRIDGE_TERM="$(tmux display-message -p '#{client_termname}')"
```

Note shellcheck prefers the assignment and export split when the command can fail; if it flags SC2155, write it as two lines (`term="$(...)"` then `export LZTMUX_BRIDGE_TERM="$term"`).

- [ ] **Step 4: Verify it builds and the suite still passes**

Run: `cd picker && go build ./... && go test ./remotebridge/...`
Expected: build clean, tests PASS.

Run: `shellcheck scripts/lztmux-remote-open.sh`
Expected: no findings.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/cmd/daemon/main.go scripts/lztmux-remote-open.sh
git commit -m "feat(bridge): ControlMaster fetch channel and TERM steering

Refs #280"
```

---

## Task 11: ctl — the `carousel` verb

**Files:**
- Modify: `picker/remotebridge/daemon/ctl.go:134-188`
- Test: `picker/remotebridge/daemon/ctl_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCarouselVerbBuildsRemoteToggle(t *testing.T) {
	v, ok := verbs["carousel"]
	if !ok {
		t.Fatal("no carousel verb")
	}
	cmds, err := v.build("%5", "@2", "sess", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want one command, got %v", cmds)
	}
	for _, want := range []string{"run-shell", "-t %5", "TMUX_PANE=%5", "AEYE_BRIDGED=1", "tmux-claude-images"} {
		if !strings.Contains(cmds[0], want) {
			t.Fatalf("command %q missing %q", cmds[0], want)
		}
	}
	if !strings.Contains(cmds[0], "command -v") || !strings.Contains(cmds[0], "split-window") {
		t.Fatalf("a missing binary must surface as a visible split, not a silent no-op: %q", cmds[0])
	}
	if !v.moves || !v.layout {
		t.Fatal("the toggle opens a split that takes focus: needs moves+layout")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestCarouselVerb -v`
Expected: FAIL — "no carousel verb".

- [ ] **Step 3: Implement**

Add to the `verbs` map in `ctl.go`:

```go
	// The carousel toggle opens its own split on the remote, so this verb only
	// launches it. TMUX_PANE is injected explicitly because run-shell does not
	// export it (the local bind this replaces does the same via #{pane_id}), and
	// AEYE_BRIDGED tells the viewer its frames cross a network, not a disk.
	// Backgrounded (-b) so a slow launch never blocks the remote command queue;
	// the split arrives as a %layout-change like any other structural event.
	//
	// A missing binary surfaces as a short remote split rather than a
	// display-message: the only client attached to this remote is the daemon's
	// control client, which has no status line for a message to land on, while a
	// split mirrors back into the window the human is looking at.
	"carousel": {layout: true, moves: true, build: func(pane, _, _ string, _ []string) ([]string, error) {
		return []string{fmt.Sprintf(
			"run-shell -b -t %s 'command -v tmux-claude-images >/dev/null 2>&1 && "+
				"exec env TMUX_PANE=%s AEYE_BRIDGED=1 tmux-claude-images; "+
				`tmux split-window -t %s -l 3 "echo lazytmux: tmux-claude-images is not on PATH on this host; sleep 5"'`,
			pane, pane, pane)}, nil
	}},
```

- [ ] **Step 4: Run the tests**

Run: `cd picker && go test ./remotebridge/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/ctl.go picker/remotebridge/daemon/ctl_test.go
git commit -m "feat(bridge): carousel ctl verb

Refs #280"
```

---

## Task 12: tmux — gate `prefix + I` on a bridge window

**Files:**
- Modify: `config/tmux.conf.nix:529-531`

- [ ] **Step 1: Change the bind**

Replace `carouselBind` with:

```nix
  # Inside a mirror window the carousel must open on the REMOTE (its manifest and
  # its images live there); the local branch is the existing bind, verbatim.
  carouselBind =
    lib.optionalString (carousel-toggle != null)
    "bind I if-shell -F '${bridgeGate}' { run-shell \"${bridgeCtl} carousel '#{@bridge_pane}'\" } { run-shell 'TMUX_PANE=#{pane_id} ${carousel-toggle}/bin/tmux-claude-images' }";
```

`bridgeGate` and `bridgeCtl` are defined above it at `config/tmux.conf.nix:286-287`, so no new bindings are needed.

- [ ] **Step 2: Build**

Run: `nix build .#default`
Expected: builds clean.

- [ ] **Step 3: Verify the generated config**

Run: `grep -n "bind I" result/share/tmux/tmux.conf` (or wherever the generated conf lands in the build output)
Expected: one `bind I if-shell -F …` line with both branches present.

- [ ] **Step 4: Smoke-test the non-bridge path**

Run: `./result/bin/tmux -L gfxsmoke new-session -d; ./result/bin/tmux -L gfxsmoke list-keys | grep ' I '`
Expected: the bind exists; pressing it in a normal window still opens a local carousel (manual). Clean up: `./result/bin/tmux -L gfxsmoke kill-server`.

- [ ] **Step 5: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(bridge): route prefix+I to the remote inside a mirror window

Refs #280"
```

---

## Task 13: integration test on the `--test-local` seam

**Files:**
- Test: `picker/remotebridge/daemon/graphics_integration_test.go`

- [ ] **Step 1: Write the test**

This exercises scan → coalesce → rewrite → wrap end-to-end through a real sink, with a fetcher whose `Run` is stubbed — no ssh, so it runs in `nix flake check`.

```go
package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/graphics"
)

func TestGraphicsEndToEndThroughASink(t *testing.T) {
	cache := t.TempDir()
	png := []byte("\x89PNG fake")
	f := &graphics.SSHFetcher{
		Host: "fake", CacheDir: cache, MaxBytes: 1 << 20,
		Run: func(...string) ([]byte, error) {
			return append([]byte("1700000000 9\n"), png...), nil
		},
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	s := newOutputSink(remote, graphics.New(f, nil))
	defer s.Close()

	// base64("/remote/tmp/diagram.png")
	s.Write([]byte("\x1b_Gi=300,a=T,U=1,f=100,c=40,r=20,t=f;L3JlbW90ZS90bXAvZGlhZ3JhbS5wbmc=\x1b\\"))

	got := readAllFrames(t, local, 500*time.Millisecond)
	if strings.Count(got, "\x1bPtmux;") != 1 {
		t.Fatalf("want exactly one passthrough wrapper: %q", got)
	}
	for _, want := range []string{"i=300", "c=40", "r=20", "f=100", "t=f"} {
		if !strings.Contains(got, want) {
			t.Fatalf("control key %q was mutated away: %q", want, got)
		}
	}
	if strings.Contains(got, "L3JlbW90ZS90bXAvZGlhZ3JhbS5wbmc=") {
		t.Fatal("still carrying the remote path")
	}
	ents, _ := os.ReadDir(cache)
	if len(ents) != 1 {
		t.Fatalf("want one cached file, got %d", len(ents))
	}
	b, _ := os.ReadFile(filepath.Join(cache, ents[0].Name()))
	if string(b) != string(png) {
		t.Fatalf("cached bytes = %q", b)
	}
}
```

- [ ] **Step 2: Run it**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestGraphicsEndToEnd -v`
Expected: PASS.

- [ ] **Step 3: Run the whole check**

Run: `nix flake check`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add picker/remotebridge/daemon/graphics_integration_test.go
git commit -m "test(bridge): end-to-end graphics localisation through a sink

Refs #280"
```

---

## Task 14: bats — the keybind gate

**Files:**
- Modify: `tests/remote-m2-integration.bats`

- [ ] **Step 1: Write the test**

Use the file's existing idiom — `$SRC`/`$DST` tmux servers, `bridge_up`, `remote_pane_of`, `$CTL`, and the poll-then-assert-after-teardown shape (see `tests/remote-m2-integration.bats:788-826`, "ctl split-h splits the REMOTE pane"). A stub on PATH stands in for the real toggle, so the assertion is that the verb reached the remote pane carrying the right environment.

```bash
@test "ctl carousel runs the toggle on the REMOTE pane with TMUX_PANE and AEYE_BRIDGED" {
	# Must be exported BEFORE the first $SRC command: the "remote" tmux server
	# inherits this environment when it starts.
	stub="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$stub"
	cat >"$stub/tmux-claude-images" <<-EOF
		#!/usr/bin/env bash
		printf '%s %s\n' "\$TMUX_PANE" "\$AEYE_BRIDGED" >"$BATS_TEST_TMPDIR/toggled"
	EOF
	chmod +x "$stub/tmux-claude-images"
	export PATH="$stub:$PATH"

	$SRC new-session -d -s rem -x 150 -y 40
	$DST new-session -d -s host-sess -x 150 -y 40
	bridge_up 1 c1

	pane="$(remote_pane_of 0)"
	[ -n "$pane" ]

	run "$CTL" --sock "$sock" carousel "$pane"
	[ "$status" -eq 0 ]

	for _ in $(seq 1 60); do
		[ -f "$BATS_TEST_TMPDIR/toggled" ] && break
		sleep 0.15
	done
	got="$(cat "$BATS_TEST_TMPDIR/toggled" 2>/dev/null || true)"

	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true

	[ "$got" = "$pane 1" ]
}
```

- [ ] **Step 2: Run it**

Run: `nix flake check`
Expected: PASS.

Also assert the keybind gate itself from the generated config (cheaper than driving a keypress):

```bash
grep -q "bind I if-shell -F .*@bridge_win" result/share/tmux/tmux.conf
```

- [ ] **Step 3: Commit**

```bash
git add tests/remote-m2-integration.bats
git commit -m "test(bridge): prefix+I gate routes remote, leaves local untouched

Refs #280"
```

---

## Task 15: bump aeye and document

**Files:**
- Modify: `flake.lock`, `CLAUDE.md`

- [ ] **Step 1: Bump the aeye input to the release from Tasks 1-2**

Run: `nix flake update aeye`
Expected: `flake.lock` shows the new aeye rev.

- [ ] **Step 2: Document the proxy in `CLAUDE.md`**

Add to the Architecture section, under the remote-bridge material:

```markdown
### Bridge Graphics

Kitty graphics crossing the remote bridge are localised by
`picker/remotebridge/graphics`, in each pane's output-sink pump.

- **Placements need nothing.** Kitty unicode placeholders are ordinary grid
  text, so they cross the bridge (and survive `capture-pane` reseeds) on the
  normal text path. Only the store's `t=f` payload — a path on the remote
  filesystem — is host-bound.
- **The proxy** scans kitty APCs out of the stream (bare and `\ePtmux;`-wrapped),
  rewrites `t=f`/`t=t` payloads to a locally-fetched copy, drops `t=s` and any
  fetch it cannot satisfy (a stale path renders the *wrong* image; a blank one
  self-heals), and re-wraps exactly once for the local tmux.
- **Fetches** ride the daemon's own ssh `ControlMaster` socket, so they never
  share the control stream with live terminal output. Cache key is
  `(path, mtime, size)` — mtime matters, since viewers rewrite scratch frames at
  a stable path while panning.
- **Coalescing:** the pump batch-drains queued output frames so the proxy can
  drop stores a later frame supersedes. Safe because each store is a full `a=T`
  replacement; never coalesced across an `a=d` for the same id.
- **`prefix + I`** is gated on `bridgeGate`: inside a mirror window it runs the
  toggle on the remote (ctl verb `carousel`), so the carousel is a remote split
  mirrored back like any other.
- Remote host needs `tmux-claude-images` and `resvg` on PATH.
```

- [ ] **Step 3: Verify**

Run: `nix flake check && nix build .#default`
Expected: both pass.

- [ ] **Step 4: Commit**

```bash
git add flake.lock CLAUDE.md
git commit -m "chore(deps): bump aeye for host-namespaced image ids

Refs #280"
```

---

## Task 16: manual verification on the real link

**Files:** none.

- [ ] **Step 1: Rebuild and open a bridged session**

Run `home-manager switch` (or `nix build .#default` and use `./result/bin/tmux`), then open the remote session via `prefix + s` → remote section, on a **kitty** local terminal.

- [ ] **Step 2: Walk the acceptance criteria**

Check each, in order (spec "Acceptance criteria" 1-7):

1. `prefix + I` opens the carousel as a split in the mirror window with the remote's geometry; the same key in a local window behaves as before.
2. A d2 diagram rendered by the remote agent displays crisp.
3. Zoom sharpens (remote `resvg` present); keyboard pan tracks without a growing lag.
4. Open a local carousel at the same time; both show their own images.
5. Repeat step 1-2 from a non-kitty terminal (e.g. Alacritty): block art, no breakage.
6. `pkill -f lztmux-remote-n-daemon`; confirm no `lztmux-bridge-*.sock` remains and the cache dir is bounded.
7. `mv` an image out from under the remote carousel mid-session: blank frame plus a daemon stderr line, never a wrong image.

- [ ] **Step 3: Record the result on the issue**

```bash
gh issue comment 280 --repo noamsto/lazytmux --body "Manual pass on <local> -> <remote>: <results per criterion>"
```

- [ ] **Step 4: Open the PR**

```bash
gh pr create --assignee @me --title "feat(bridge): render kitty graphics through the remote bridge" --body "Closes #280. Depends on noamsto/aeye#177 (merged, bumped in flake.lock)."
```

---

## Notes for the implementer

- **`AEYE_BRIDGED` only reaches the carousel because the ctl verb sets it** (Task 11). A carousel the human opens by hand on the remote won't have it and will use the raw-RGBA path; the fetcher's 8 MB cap keeps that survivable rather than fast.
- **…and because aeye's toggle forwards it.** `tmux-claude-images` launches the viewer with `tmux split-window -e`, forwarding a fixed list of variables; setting `AEYE_BRIDGED` on the toggle does *not* put it in the viewer's environment. aeye's side of this ships in noamsto/aeye#177. Found during Task 2 rather than by testing, because the failure is silent and plausible: the policy simply never engages and pan looks like an ordinary slow network. Any future variable the bridge wants the viewer to see (a cell-pixel-size hint is the known candidate) hits the same trap.
- **Don't add a `t=d` conversion.** Inline payloads are self-contained and pass through; converting `t=f` to `t=d` instead of fetching would push image bytes onto the control stream, which is the option the spec rejected (D3).
- **`decodeSeq` returns a length, not an `ok` bool** — amended during Task 3, after the first implementer caught the bug. `\ePtmux;` wraps *any* passthrough, not just graphics, so "incomplete" and "complete but not ours" are different answers; the original single bool conflated them and an OSC 52 clipboard escape (which aeye itself emits) would hold every later byte on that pane until the partial cap or `Flush`. Non-graphics passthroughs are forwarded verbatim — already wrapped for exactly one tmux layer, which is what the renderer's local tmux wants. The same branch requires the decoded sequence to fill its wrapper exactly: a wrapper carrying trailing bytes is forwarded whole rather than decoded, because decoding only the first sequence would silently drop the remainder, and losing relayed bytes is worse than leaving one store unlocalised.
- **The proxy runs on the sink's pump goroutine and may block there.** That is deliberate (D4) — it holds one pane's stream so a store lands before the placements referencing it. Do not move it onto `Router.Route`, which runs on the single shared control-stream loop.
