# Remote bridge M2.1 — daemon + one-window multi-pane mirror — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render one remote tmux window's full pane layout as a native local multi-pane window, live-synced, via a daemon that owns one `ssh -CC` connection and feeds one renderer process per local pane.

**Architecture:** A **daemon** (outside the panes it manages) owns the single control-mode connection; it converges size local→remote, mirrors the remote window's layout into a native local window, and routes each pane's `%output` to a per-pane **renderer** over a unix-socket framed protocol. Renderers are dumb painters + stdin forwarders (M1 `render` refactored). Structure flows remote→local; size flows local→remote. No structural input yet (M2.3).

**Tech Stack:** Go 1.x (stdlib only + `golang.org/x/term` already vendored), tmux next-3.8 control mode, Nix `buildGoModule`, bats for integration.

## Global Constraints

- Module path: `github.com/noamsto/lazytmux/picker`. Packages under `picker/remotebridge/{controlmode,daemon,render}`.
- **Directional authority (verified next-3.8):** structure flows remote→local; **size flows local→remote**. Renderers NEVER call `refresh-client`; the daemon owns the single control-client size.
- `select-layout '<remote-layout-string>'` assigns local panes to cells **positionally, in local pane-list order** — create local panes in remote cell order.
- Reuse M1 verbatim where possible: `controlmode.Reader`/`ParseLine`/`Unescape`, `controlmode.SendKeysArgs`, `render.Seed`, `render.MakeRaw`/`render.Size`.
- Tests are plain Go `testing` (no testify — match M1). Tabs for indentation (gofmt).
- Every `gh`/PR step: commit from inside `nix develop` so pre-commit hooks run.
- Renderers spawned by **absolute store path** (pane PATH is stale until server restart).
- Byte-level throughout: pane bytes are binary (UTF-8 may split across frames); never treat as strings.

---

## File structure

```
picker/remotebridge/
  controlmode/
    parse.go        (M1, unchanged)
    encode.go       (M1, unchanged)
    layout.go       NEW — Task 1: layout-string parser
  daemon/
    protocol.go     NEW — Task 2: framed daemon<->renderer wire protocol
    router.go       NEW — Task 4: %output pane->renderer demux
    seed.go         NEW — Task 5: per-pane seed producer (M1 seedFlow, generalized)
    mirror.go       NEW — Task 6: layout -> local tmux command plan + apply
    size.go         NEW — Task 7: size local->remote convergence
    daemon.go       NEW — Task 8: orchestration + main loop + teardown
  render/
    snapshot.go     (M1, unchanged)
    tty.go          (M1, unchanged)
    renderer.go     NEW — Task 3: protocol-fed painter + stdin forwarder
  cmd/
    renderer/main.go NEW — Task 3: renderer entrypoint (pane command)
    daemon/main.go   NEW — Task 8: daemon entrypoint
  main.go           (M1 single-pane bridge — kept; not touched in M2.1)
tests/
  remote-m2-integration.bats  NEW — Task 9
```

---

### Task 1: Layout-string parser (`controlmode/layout.go`)

Parse a tmux layout string (the payload of `%layout-change` and of `#{window_layout}`) into an ordered pane list. Pure, no I/O.

Layout grammar (tmux `layout-custom.c`): `<checksum>,<cell>` where a cell is
`<WxH>,<X>,<Y>` for a leaf plus a trailing `,<paneid>`, or `WxH,X,Y{<cell>,<cell>,...}`
(left-right split) / `WxH,X,Y[<cell>,<cell>,...]` (top-bottom split). Leaf trailing number is the pane id (the `%N` number, without `%`).

**Files:**
- Create: `picker/remotebridge/controlmode/layout.go`
- Test: `picker/remotebridge/controlmode/layout_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  type PaneCell struct { ID string; W, H, X, Y int } // ID is the "%N" pane id
  type Layout struct { W, H int; Panes []PaneCell }   // Panes in cell (depth-first) order
  func ParseLayout(s string) (Layout, error)
  ```

- [ ] **Step 1: Write the failing test**

```go
package controlmode

import "testing"

func TestParseLayout(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantW   int
		wantH   int
		wantIDs []string
		wantP0  PaneCell
	}{
		{
			name:    "single pane",
			in:      "bd67,190x45,0,0,3",
			wantW:   190, wantH: 45,
			wantIDs: []string{"%3"},
			wantP0:  PaneCell{ID: "%3", W: 190, H: 45, X: 0, Y: 0},
		},
		{
			name:    "horizontal split (left-right)",
			in:      "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}",
			wantW:   190, wantH: 45,
			wantIDs: []string{"%0", "%1"},
			wantP0:  PaneCell{ID: "%0", W: 95, H: 45, X: 0, Y: 0},
		},
		{
			name:    "vertical split (top-bottom)",
			in:      "b5e9,190x45,0,0[190x22,0,0,0,190x22,0,23,1]",
			wantW:   190, wantH: 45,
			wantIDs: []string{"%0", "%1"},
			wantP0:  PaneCell{ID: "%0", W: 190, H: 22, X: 0, Y: 0},
		},
		{
			name:    "nested",
			in:      "a1b2,190x45,0,0{95x45,0,0,0,94x45,96,0[94x22,96,0,1,94x22,96,23,2]}",
			wantW:   190, wantH: 45,
			wantIDs: []string{"%0", "%1", "%2"},
			wantP0:  PaneCell{ID: "%0", W: 95, H: 45, X: 0, Y: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLayout(tt.in)
			if err != nil {
				t.Fatalf("ParseLayout(%q) error: %v", tt.in, err)
			}
			if got.W != tt.wantW || got.H != tt.wantH {
				t.Errorf("window dims = %dx%d, want %dx%d", got.W, got.H, tt.wantW, tt.wantH)
			}
			var ids []string
			for _, p := range got.Panes {
				ids = append(ids, p.ID)
			}
			if len(ids) != len(tt.wantIDs) {
				t.Fatalf("pane ids = %v, want %v", ids, tt.wantIDs)
			}
			for i := range ids {
				if ids[i] != tt.wantIDs[i] {
					t.Errorf("pane[%d] id = %s, want %s", i, ids[i], tt.wantIDs[i])
				}
			}
			if got.Panes[0] != tt.wantP0 {
				t.Errorf("pane[0] = %+v, want %+v", got.Panes[0], tt.wantP0)
			}
		})
	}
}

func TestParseLayoutError(t *testing.T) {
	for _, in := range []string{"", "nocomma", "bd67,notdims,0,0,3"} {
		if _, err := ParseLayout(in); err == nil {
			t.Errorf("ParseLayout(%q) expected error, got nil", in)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestParseLayout -v`
Expected: FAIL — `undefined: ParseLayout` / `undefined: PaneCell`.

- [ ] **Step 3: Write minimal implementation**

```go
package controlmode

import (
	"fmt"
	"strconv"
	"strings"
)

type PaneCell struct {
	ID         string
	W, H, X, Y int
}

type Layout struct {
	W, H  int
	Panes []PaneCell
}

// ParseLayout parses a tmux layout string (window_layout / %layout-change payload).
// Panes are returned depth-first in cell order — the order local panes must be
// created in, since select-layout assigns panes to cells positionally.
func ParseLayout(s string) (Layout, error) {
	// Strip the leading "<checksum>," prefix.
	_, body, ok := strings.Cut(s, ",")
	if !ok {
		return Layout{}, fmt.Errorf("layout: no checksum separator in %q", s)
	}
	p := &layoutParser{s: body}
	root, err := p.cell()
	if err != nil {
		return Layout{}, err
	}
	if p.pos != len(p.s) {
		return Layout{}, fmt.Errorf("layout: trailing data %q", p.s[p.pos:])
	}
	var out Layout
	out.W, out.H = root.w, root.h
	collectLeaves(root, &out.Panes)
	if len(out.Panes) == 0 {
		return Layout{}, fmt.Errorf("layout: no panes in %q", s)
	}
	return out, nil
}

type node struct {
	w, h, x, y int
	id         string  // set on leaves
	children   []*node // set on splits
}

type layoutParser struct {
	s   string
	pos int
}

// cell := WxH,X,Y [ , id | { children } | [ children ] ]
func (p *layoutParser) cell() (*node, error) {
	n := &node{}
	var err error
	if n.w, err = p.intUntil('x'); err != nil {
		return nil, err
	}
	if n.h, err = p.intUntil(','); err != nil {
		return nil, err
	}
	if n.x, err = p.intUntil(','); err != nil {
		return nil, err
	}
	// Y runs until one of , { [  } ]  or end.
	n.y, err = p.intUntilAny(",{[}]")
	if err != nil {
		return nil, err
	}
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("layout: unexpected end after cell")
	}
	switch p.s[p.pos] {
	case ',':
		p.pos++ // consume ','
		n.id = "%" + p.numRun()
	case '{':
		return p.split(n, '{', '}')
	case '[':
		return p.split(n, '[', ']')
	}
	return n, nil
}

func (p *layoutParser) split(n *node, open, close byte) (*node, error) {
	p.pos++ // consume open
	for {
		c, err := p.cell()
		if err != nil {
			return nil, err
		}
		n.children = append(n.children, c)
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("layout: unterminated split")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case close:
			p.pos++
			return n, nil
		default:
			return nil, fmt.Errorf("layout: bad split delimiter %q", p.s[p.pos])
		}
	}
}

func (p *layoutParser) numRun() string {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *layoutParser) intUntil(sep byte) (int, error) {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] != sep {
		p.pos++
	}
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("layout: expected %q", sep)
	}
	v, err := strconv.Atoi(p.s[start:p.pos])
	p.pos++ // consume sep
	return v, err
}

func (p *layoutParser) intUntilAny(seps string) (int, error) {
	start := p.pos
	for p.pos < len(p.s) && !strings.ContainsRune(seps, rune(p.s[p.pos])) {
		p.pos++
	}
	return strconv.Atoi(p.s[start:p.pos])
}

func collectLeaves(n *node, out *[]PaneCell) {
	if len(n.children) == 0 {
		*out = append(*out, PaneCell{ID: n.id, W: n.w, H: n.h, X: n.x, Y: n.y})
		return
	}
	for _, c := range n.children {
		collectLeaves(c, out)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestParseLayout -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/controlmode/layout.go picker/remotebridge/controlmode/layout_test.go
git commit -m "feat(remote): tmux layout-string parser for the M2 mirror [#167]"
```

---

### Task 2: Framed daemon↔renderer protocol (`daemon/protocol.go`)

A length-prefixed binary frame protocol over a unix socket. Frame = `1-byte type | 4-byte big-endian length | payload`.

**Files:**
- Create: `picker/remotebridge/daemon/protocol.go`
- Test: `picker/remotebridge/daemon/protocol_test.go`

**Interfaces:**
- Produces:
  ```go
  type FrameType byte
  const (
      FrameHello  FrameType = 1 // renderer->daemon: payload = remote pane id ("%N")
      FrameSeed   FrameType = 2 // daemon->renderer: payload = initial screen bytes
      FrameOutput FrameType = 3 // daemon->renderer: payload = live pane bytes
      FrameResize FrameType = 4 // daemon->renderer: payload = 8 bytes (w,h uint32 BE)
      FrameInput  FrameType = 5 // renderer->daemon: payload = stdin bytes
  )
  type Frame struct { Type FrameType; Payload []byte }
  func WriteFrame(w io.Writer, t FrameType, payload []byte) error
  func ReadFrame(r io.Reader) (Frame, error) // io.EOF at clean close
  func EncodeResize(w, h int) []byte
  func DecodeResize(p []byte) (w, h int, err error)
  ```

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"bytes"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := []struct {
		t FrameType
		p []byte
	}{
		{FrameHello, []byte("%3")},
		{FrameSeed, []byte("\x1b[2J\x1b[Hhello")},
		{FrameOutput, []byte{0x00, 0xff, 0x1b, '\n'}}, // binary-safe
		{FrameInput, []byte("ls\r")},
	}
	for _, x := range payloads {
		if err := WriteFrame(&buf, x.t, x.p); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for i, x := range payloads {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", i, err)
		}
		if f.Type != x.t || !bytes.Equal(f.Payload, x.p) {
			t.Errorf("frame[%d] = %d/%q, want %d/%q", i, f.Type, f.Payload, x.t, x.p)
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Errorf("expected io.EOF after last frame, got %v", err)
	}
}

func TestResizeCodec(t *testing.T) {
	p := EncodeResize(210, 52)
	w, h, err := DecodeResize(p)
	if err != nil || w != 210 || h != 52 {
		t.Fatalf("DecodeResize = %d,%d,%v want 210,52,nil", w, h, err)
	}
	if _, _, err := DecodeResize([]byte{1, 2, 3}); err == nil {
		t.Error("DecodeResize(short) expected error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestFrame|TestResize' -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package daemon

import (
	"encoding/binary"
	"fmt"
	"io"
)

type FrameType byte

const (
	FrameHello  FrameType = 1
	FrameSeed   FrameType = 2
	FrameOutput FrameType = 3
	FrameResize FrameType = 4
	FrameInput  FrameType = 5
)

type Frame struct {
	Type    FrameType
	Payload []byte
}

func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err // io.EOF passes through on clean boundary
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	p := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, p); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Type: FrameType(hdr[0]), Payload: p}, nil
}

func EncodeResize(w, h int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:], uint32(w))
	binary.BigEndian.PutUint32(b[4:], uint32(h))
	return b
}

func DecodeResize(p []byte) (int, int, error) {
	if len(p) != 8 {
		return 0, 0, fmt.Errorf("resize payload len %d, want 8", len(p))
	}
	return int(binary.BigEndian.Uint32(p[0:])), int(binary.BigEndian.Uint32(p[4:])), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestFrame|TestResize' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/protocol.go picker/remotebridge/daemon/protocol_test.go
git commit -m "feat(remote): daemon<->renderer framed wire protocol [#167]"
```

---

### Task 3: Renderer — protocol-fed painter + stdin forwarder (`render/renderer.go` + `cmd/renderer`)

The per-pane process. Connects to the daemon socket, announces its remote pane id, then paints Seed/Output frames to stdout and forwards stdin as Input frames. Reuses M1 `render.Seed`/`render.MakeRaw`.

**Files:**
- Create: `picker/remotebridge/render/renderer.go`
- Create: `picker/remotebridge/cmd/renderer/main.go`
- Test: `picker/remotebridge/render/renderer_test.go`

**Interfaces:**
- Consumes: `daemon.WriteFrame`, `daemon.ReadFrame`, `daemon.Frame*`; `render.MakeRaw`.
- Produces:
  ```go
  // Run drives one renderer over conn: sends Hello(paneID), then paints Seed/Output
  // to out and forwards in -> Input frames, until conn EOF. rawSetup is injected so
  // tests can skip real tty setup; production passes render.MakeRaw(fd).
  func Run(conn io.ReadWriteCloser, paneID string, in io.Reader, out io.Writer, rawSetup func() (func() error, error)) error
  ```
  (`cmd/renderer/main.go` wires `Run(unixConn, os.Getenv("LZTMUX_RENDER_PANE"), os.Stdin, os.Stdout, func() (func() error, error) { return render.MakeRaw(0) })`.)

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/daemon"
)

func TestRendererPaintsAndForwards(t *testing.T) {
	client, server := net.Pipe()
	var out bytes.Buffer
	in := bytes.NewBufferString("ls\r")
	noRaw := func() (func() error, error) { return func() error { return nil }, nil }

	done := make(chan error, 1)
	go func() { done <- Run(client, "%3", in, &out, noRaw) }()

	// Expect Hello first.
	f, err := daemon.ReadFrame(server)
	if err != nil || f.Type != daemon.FrameHello || string(f.Payload) != "%3" {
		t.Fatalf("hello = %v %q err %v", f.Type, f.Payload, err)
	}
	// Send a seed + one output frame, then close.
	daemon.WriteFrame(server, daemon.FrameSeed, []byte("SEED"))
	daemon.WriteFrame(server, daemon.FrameOutput, []byte("OUT"))

	// Expect the forwarded stdin as an Input frame.
	fi, err := daemon.ReadFrame(server)
	if err != nil || fi.Type != daemon.FrameInput || string(fi.Payload) != "ls\r" {
		t.Fatalf("input = %v %q err %v", fi.Type, fi.Payload, err)
	}
	server.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after conn close")
	}
	if got := out.String(); got != "SEEDOUT" {
		t.Errorf("painted %q, want %q", got, "SEEDOUT")
	}
	_ = io.EOF
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/render/ -run TestRendererPaints -v`
Expected: FAIL — `undefined: Run`.

- [ ] **Step 3: Write minimal implementation**

`render/renderer.go`:
```go
package render

import (
	"io"

	"github.com/noamsto/lazytmux/picker/remotebridge/daemon"
)

func Run(conn io.ReadWriteCloser, paneID string, in io.Reader, out io.Writer, rawSetup func() (func() error, error)) error {
	if err := daemon.WriteFrame(conn, daemon.FrameHello, []byte(paneID)); err != nil {
		return err
	}
	restore, err := rawSetup()
	if err != nil {
		return err
	}
	if restore != nil {
		defer restore()
	}

	// stdin -> Input frames
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := daemon.WriteFrame(conn, daemon.FrameInput, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// daemon frames -> paint
	for {
		f, err := daemon.ReadFrame(conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch f.Type {
		case daemon.FrameSeed, daemon.FrameOutput:
			if _, werr := out.Write(f.Payload); werr != nil {
				return werr
			}
		case daemon.FrameResize:
			// M2.1: renderer records nothing; size is daemon-authoritative. No-op.
		}
	}
}
```

`cmd/renderer/main.go`:
```go
package main

import (
	"fmt"
	"net"
	"os"

	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

func main() {
	sock := os.Getenv("LZTMUX_RENDER_SOCK")
	pane := os.Getenv("LZTMUX_RENDER_PANE")
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "renderer: dial %s: %v\r\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()
	if err := render.Run(conn, pane, os.Stdin, os.Stdout,
		func() (func() error, error) { return render.MakeRaw(0) }); err != nil {
		fmt.Fprintf(os.Stderr, "renderer: %v\r\n", err)
		os.Exit(1)
	}
}
```

> NOTE on import direction: `render` now imports `daemon` for the frame codec. If a
> future import cycle appears (daemon importing render), move the frame codec into a
> leaf package `remotebridge/wire` and have both import that. Not needed for M2.1.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/render/ -run TestRendererPaints -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/render/renderer.go picker/remotebridge/render/renderer_test.go picker/remotebridge/cmd/renderer/main.go
git commit -m "feat(remote): protocol-fed renderer (painter + stdin forwarder) [#167]"
```

---

### Task 4: `%output` router (`daemon/router.go`)

Demultiplex the control stream's `%output %<pane> <data>` to the renderer registered for that pane id. Renderers register a sink (an `io.Writer` that wraps their conn as Output frames).

**Files:**
- Create: `picker/remotebridge/daemon/router.go`
- Test: `picker/remotebridge/daemon/router_test.go`

**Interfaces:**
- Produces:
  ```go
  type Router struct { /* mu + map[string]io.Writer */ }
  func NewRouter() *Router
  func (r *Router) Register(paneID string, sink io.Writer)   // sink receives raw pane bytes
  func (r *Router) Unregister(paneID string)
  func (r *Router) Route(paneID string, data []byte)          // no-op if pane not registered
  ```

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"bytes"
	"testing"
)

func TestRouterRoutesByPane(t *testing.T) {
	r := NewRouter()
	var a, b bytes.Buffer
	r.Register("%1", &a)
	r.Register("%2", &b)

	r.Route("%1", []byte("one"))
	r.Route("%2", []byte("two"))
	r.Route("%1", []byte("-more"))
	r.Route("%9", []byte("dropped")) // unregistered: silently dropped

	if a.String() != "one-more" {
		t.Errorf("pane %%1 got %q, want %q", a.String(), "one-more")
	}
	if b.String() != "two" {
		t.Errorf("pane %%2 got %q, want %q", b.String(), "two")
	}

	r.Unregister("%1")
	r.Route("%1", []byte("after-unregister"))
	if a.String() != "one-more" {
		t.Errorf("pane %%1 received after unregister: %q", a.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestRouter -v`
Expected: FAIL — `undefined: NewRouter`.

- [ ] **Step 3: Write minimal implementation**

```go
package daemon

import (
	"io"
	"sync"
)

type Router struct {
	mu    sync.Mutex
	sinks map[string]io.Writer
}

func NewRouter() *Router { return &Router{sinks: map[string]io.Writer{}} }

func (r *Router) Register(paneID string, sink io.Writer) {
	r.mu.Lock()
	r.sinks[paneID] = sink
	r.mu.Unlock()
}

func (r *Router) Unregister(paneID string) {
	r.mu.Lock()
	delete(r.sinks, paneID)
	r.mu.Unlock()
}

func (r *Router) Route(paneID string, data []byte) {
	r.mu.Lock()
	sink := r.sinks[paneID]
	r.mu.Unlock()
	if sink != nil {
		sink.Write(data) // best-effort; sink is non-blocking (see daemon.go)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestRouter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/router.go picker/remotebridge/daemon/router_test.go
git commit -m "feat(remote): %output pane->renderer router [#167]"
```

---

### Task 5: Per-pane seed producer (`daemon/seed.go`)

Generalize M1's `seedFlow` (currently in `main.go`) into a daemon function that produces the initial screen bytes for a *given* pane id. Uses the M1 reply-correlation helpers. The daemon calls this once per pane before registering it with the router.

**Files:**
- Create: `picker/remotebridge/daemon/seed.go`
- Test: `picker/remotebridge/daemon/seed_test.go`

**Interfaces:**
- Consumes: `controlmode.Reader`, `render.Seed`.
- Produces:
  ```go
  // PaneSeed issues capture/display-message for paneID over an established control
  // stream and returns the render.Seed bytes. send writes a command line to the
  // control connection; reader is the shared controlmode.Reader. Reply blocks are
  // consumed in issue order (one reply per command).
  func PaneSeed(reader *controlmode.Reader, send func(string), paneID string) ([]byte, error)
  ```

> Implementation note: lift `readReply`, `readCursor`, `readCapture` out of `main.go`
> into `seed.go` (unexported), and adapt `seedFlow` to take an explicit pane id
> instead of resolving the active pane. The single-pane `main.go` keeps working by
> calling the same helpers. This is the "seed handshake moves into the daemon" (spec I2).

- [ ] **Step 1: Write the failing test** (scripted control stream, mirrors M1 `seed_test.go`)

```go
package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestPaneSeed(t *testing.T) {
	// Scripted server replies: display-message (cursor/mode) then capture-pane.
	// Each command emits a %begin/…/%end block in issue order.
	stream := strings.Join([]string{
		"%begin 1 1 1", "5 2 0 0", "%end 1 1 1", // display-message: cx cy alt appck
		"%begin 2 2 1", "line-one", "line-two", "%end 2 2 1", // capture-pane
	}, "\n") + "\n"

	reader := controlmode.NewReader(strings.NewReader(stream))
	var sent []string
	send := func(s string) { sent = append(sent, s) }

	got, err := PaneSeed(reader, send, "%3")
	if err != nil {
		t.Fatalf("PaneSeed: %v", err)
	}
	// Commands must target %3.
	if len(sent) != 2 || !strings.Contains(sent[0], "-t %3") || !strings.Contains(sent[1], "-t %3") {
		t.Fatalf("sent = %v, want display-message + capture-pane targeting %%3", sent)
	}
	// Seed must contain the captured content and a cursor CUP for (5,2) => \x1b[3;6H.
	if !bytes.Contains(got, []byte("line-one")) || !bytes.Contains(got, []byte("\x1b[3;6H")) {
		t.Errorf("seed missing content or cursor CUP: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestPaneSeed -v`
Expected: FAIL — `undefined: PaneSeed`.

- [ ] **Step 3: Write minimal implementation**

```go
package daemon

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

func PaneSeed(reader *controlmode.Reader, send func(string), paneID string) ([]byte, error) {
	send(fmt.Sprintf("display-message -p -t %s -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'", paneID))
	cx, cy, alt, appck := readCursor(reader)

	send(fmt.Sprintf("capture-pane -e -p -t %s", paneID))
	captured := readCapture(reader)
	if captured == nil {
		return nil, fmt.Errorf("capture-pane returned no data for %s", paneID)
	}
	captured = replaceLF(captured)
	return render.Seed(captured, cx, cy, alt, appck), nil
}

func replaceLF(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\n", "\r\n"))
}

// readReply / readCursor / readCapture: move verbatim from main.go.
func readReply(reader *controlmode.Reader) (controlmode.Line, bool) {
	for {
		l, ok := reader.Next()
		if !ok {
			return controlmode.Line{}, false
		}
		if l.Kind == controlmode.End || l.Kind == controlmode.Error {
			return l, true
		}
	}
}

func readCursor(reader *controlmode.Reader) (cx, cy int, alt, appCursorKeys bool) {
	l, ok := readReply(reader)
	if !ok || l.Kind == controlmode.Error {
		return 0, 0, false, false
	}
	fields := strings.Fields(string(l.Data))
	if len(fields) != 4 {
		return 0, 0, false, false
	}
	cx, _ = strconv.Atoi(fields[0])
	cy, _ = strconv.Atoi(fields[1])
	return cx, cy, fields[2] == "1", fields[3] == "1"
}

func readCapture(reader *controlmode.Reader) []byte {
	l, _ := readReply(reader)
	return l.Data
}
```

> After this task, delete the now-duplicated `readReply`/`readCursor`/`readCapture`
> from `main.go` and have `main.go` call the `daemon` copies OR keep `main.go`
> self-contained (M2.1 doesn't touch `main.go`'s behavior — simplest is to leave
> `main.go` as-is; the daemon copies are independent). Do NOT break the M1 build.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestPaneSeed -v`
Expected: PASS. Also run `cd picker && go build ./...` to confirm `main.go` still compiles.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/seed.go picker/remotebridge/daemon/seed_test.go
git commit -m "feat(remote): per-pane seed producer for the daemon [#167]"
```

---

### Task 6: Mirror — layout → local tmux command plan (`daemon/mirror.go`)

Turn a parsed `Layout` into the local tmux commands that reproduce it, and a
local-pane↔remote-pane mapping. Two parts: a **pure planner** (unit-tested) and an
**applier** that runs the plan against a local tmux socket (integration-tested in Task 9).

**Files:**
- Create: `picker/remotebridge/daemon/mirror.go`
- Test: `picker/remotebridge/daemon/mirror_test.go`

**Interfaces:**
- Consumes: `controlmode.Layout`, `controlmode.PaneCell`.
- Produces:
  ```go
  // PlanWindow returns the tmux argv sequence to shape an existing 1-pane local
  // window <target> into layout L: (N-1) split-window commands + one select-layout.
  // Splits use -h; select-layout then fixes exact geometry (verified: assignment is
  // positional in local pane-list order, so pane creation order = L.Panes order).
  func PlanWindow(target string, L controlmode.Layout) [][]string

  // RemotePaneOrder returns the remote pane ids in the order local panes will be
  // created (== L.Panes order), for wiring renderers to remote panes after apply.
  func RemotePaneOrder(L controlmode.Layout) []string
  ```

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"reflect"
	"testing"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

func TestPlanWindow(t *testing.T) {
	L, _ := controlmode.ParseLayout("4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}")
	got := PlanWindow("host-sess:1", L)
	want := [][]string{
		{"split-window", "-h", "-t", "host-sess:1"},
		{"select-layout", "-t", "host-sess:1", "4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlanWindow =\n%v\nwant\n%v", got, want)
	}
}

func TestPlanWindowSinglePane(t *testing.T) {
	L, _ := controlmode.ParseLayout("bd67,190x45,0,0,3")
	got := PlanWindow("host-sess:1", L)
	// One pane: no splits; still pin the layout for size determinism.
	want := [][]string{
		{"select-layout", "-t", "host-sess:1", "bd67,190x45,0,0,3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single-pane plan = %v, want %v", got, want)
	}
}

func TestRemotePaneOrder(t *testing.T) {
	L, _ := controlmode.ParseLayout("4ed4,190x45,0,0{95x45,0,0,0,94x45,96,0,1}")
	if got := RemotePaneOrder(L); !reflect.DeepEqual(got, []string{"%0", "%1"}) {
		t.Errorf("RemotePaneOrder = %v, want [%%0 %%1]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run 'TestPlanWindow|TestRemotePaneOrder' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package daemon

import "github.com/noamsto/lazytmux/picker/remotebridge/controlmode"

func PlanWindow(target string, L controlmode.Layout) [][]string {
	var cmds [][]string
	for i := 1; i < len(L.Panes); i++ {
		cmds = append(cmds, []string{"split-window", "-h", "-t", target})
	}
	// Rebuild the full layout string for select-layout. We only have the parsed
	// form here, so callers pass the ORIGINAL string via L; store it.
	cmds = append(cmds, []string{"select-layout", "-t", target, L.Raw})
	return cmds
}

func RemotePaneOrder(L controlmode.Layout) []string {
	ids := make([]string, len(L.Panes))
	for i, p := range L.Panes {
		ids[i] = p.ID
	}
	return ids
}
```

> This requires `Layout` to carry the original string. Add `Raw string` to the
> `Layout` struct in `controlmode/layout.go` and set it in `ParseLayout` (`out.Raw = s`).
> Update Task 1's struct comment accordingly. Add one assertion to Task 1's test:
> `if got.Raw != tt.in { t.Errorf(...) }` — do this when you touch layout.go.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/... -run 'TestPlanWindow|TestRemotePaneOrder|TestParseLayout' -v`
Expected: PASS (layout + mirror).

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/mirror.go picker/remotebridge/daemon/mirror_test.go picker/remotebridge/controlmode/layout.go picker/remotebridge/controlmode/layout_test.go
git commit -m "feat(remote): mirror layout->local-window command planner [#167]"
```

---

### Task 7: Size convergence (`daemon/size.go`)

Make the remote window match the local window size, so the remote layout string is
already at local dims (Spike 1 fix). Pure helper + the command it issues.

**Files:**
- Create: `picker/remotebridge/daemon/size.go`
- Test: `picker/remotebridge/daemon/size_test.go`

**Interfaces:**
- Produces:
  ```go
  // ConvergeCmd returns the control-mode command that sets the single control
  // client's size (and thus, under window-size=latest, the remote window) to WxH.
  func ConvergeCmd(w, h int) string  // -> "refresh-client -C <w>x<h>"
  ```

- [ ] **Step 1: Write the failing test**

```go
package daemon

import "testing"

func TestConvergeCmd(t *testing.T) {
	if got := ConvergeCmd(210, 52); got != "refresh-client -C 210x52" {
		t.Errorf("ConvergeCmd = %q, want %q", got, "refresh-client -C 210x52")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestConvergeCmd -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package daemon

import "fmt"

func ConvergeCmd(w, h int) string {
	return fmt.Sprintf("refresh-client -C %dx%d", w, h)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestConvergeCmd -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/size.go picker/remotebridge/daemon/size_test.go
git commit -m "feat(remote): size-convergence command helper [#167]"
```

---

### Task 8: Daemon orchestration + main loop (`daemon/daemon.go` + `cmd/daemon`)

Wire everything: open the control connection, converge size, list the target window's
panes + layout, build the local window, spawn one renderer per pane (by absolute path,
wired via the unix socket), run the router over the control stream, reflect
`%layout-change`, and tear down on `%exit`/`%window-close`. This task is **integration-
tested in Task 9**; here it is built with a thin, injectable seam so it can run against a
local tmux.

**Files:**
- Create: `picker/remotebridge/daemon/daemon.go`
- Create: `picker/remotebridge/cmd/daemon/main.go`
- Test: covered by Task 9 (bats). Optionally a Go test with a fake control stream for the
  loop's routing/teardown branches.

**Interfaces:**
- Consumes: all prior tasks; `controlmode.NewReader`, `controlmode.SendKeysArgs`.
- Produces:
  ```go
  type Config struct {
      Ctl       io.ReadWriteCloser // the ssh -CC stream (stdin+stdout duplex)
      SockPath  string             // unix socket renderers dial
      LocalSess string             // "<host>-<sess>"
      LocalWin  string             // "<host>-<sess>:1"
      RemoteWin string             // "<sess>:<win>" on the remote
      RendererBin string           // absolute store path to cmd/renderer
      LocalTmux func(args ...string) error // runs local tmux (injected; prod = exec)
      WinSize   func() (int, int)  // local window content size (injected)
  }
  func Run(cfg Config) error
  ```

**Orchestration sequence (documented for the implementer — build against a local tmux):**

1. `reader := controlmode.NewReader(cfg.Ctl)`; `send := ` buffered-writer closure (mutex-guarded, like M1 `main.go`).
2. Drain the implicit attach reply (`readReply(reader)`).
3. `w,h := cfg.WinSize()`; `send(ConvergeCmd(w,h))`; `readReply(reader)`.
4. `send("list-panes -t <RemoteWin> -F '#{pane_id}'")`; read reply → remote pane ids
   (validates the window exists — fail with a clear error if empty, as M1 does).
5. `send("display-message -p -t <RemoteWin> -F '#{window_layout}'")`; read reply →
   layout string; `L,_ := controlmode.ParseLayout(layout)`.
6. Start the unix socket listener at `cfg.SockPath`; accept renderer connections in a
   goroutine, read each renderer's `FrameHello` to learn its pane id, then
   `router.Register(paneID, outputSink(conn))` where `outputSink` wraps the conn writing
   `FrameOutput` frames **non-blocking** (buffered channel per renderer; on overflow, drop
   + mark dirty — M2.1 may start with a large buffer and TODO the dirty-reseed for M2.2).
   Also pump each renderer's `FrameInput` → `SendKeysArgs(paneID, …)` → `send`.
7. Apply the mirror: for each split cmd + the select-layout from `PlanWindow(cfg.LocalWin, L)`,
   run `cfg.LocalTmux(cmd...)`. Then read local pane ids in order
   (`list-panes -t <LocalWin> -F '#{pane_id}'`) and pair them with `RemotePaneOrder(L)`.
8. For each local pane, `respawn-pane -k -t <localPane>` with the renderer command:
   `LZTMUX_RENDER_SOCK=<sock> LZTMUX_RENDER_PANE=<remotePane> <RendererBin>`
   (set via `-e` on the pane, or via `respawn-pane` shell). The renderer dials back,
   Hello's its remote pane id, and gets registered (step 6).
9. Per pane, produce the seed AFTER the renderer registers but BEFORE streaming:
   `seed := PaneSeed(reader, send, remotePane)`; write it to that renderer as a
   `FrameSeed`. (Ordering: seed reads are serialized on the single control stream; do all
   seeds before entering the stream loop, OR interleave carefully — simplest for M2.1:
   seed all panes sequentially right after their renderers connect, THEN start the router
   loop. Document this ordering; it mirrors M1's "seed before stream".)
10. Main loop over `reader.Next()`:
    - `Output` → `router.Route(l.Pane, l.Data)` (only target-window panes are registered;
      others are dropped — the M2.1 firehose filter).
    - `LayoutChange` → re-read `window_layout`, diff pane set; for M2.1 minimal: if the
      pane set changed, re-run `PlanWindow` deltas (add/remove local panes) and
      re-`select-layout`; re-pair renderers. (Keep this minimal — a full diff engine is
      M2.2. For M2.1, support the "remote split adds a pane" and "remote pane closes"
      cases; a `select-layout` re-apply covers geometry.)
    - `WindowClose`/`Exit` → teardown.
11. Teardown: close the listener, close all renderer conns (renderers exit on EOF → panes
    fold), `send`-close the control connection, `kill-session -t <LocalSess>` if still present.

- [ ] **Step 1: Write a Go loop test with a fake control stream** (routing + teardown only)

```go
package daemon

import (
	"strings"
	"testing"
)

// A minimal check that the main loop routes %output to a registered pane and stops
// on %exit. Uses a canned reader and a router with a capture sink; the full
// end-to-end path is exercised by the bats integration test (Task 9).
func TestLoopRoutesAndExits(t *testing.T) {
	stream := strings.Join([]string{
		"%output %1 hello",
		"%exit",
	}, "\n") + "\n"
	reader := newTestReader(stream) // controlmode.NewReader(strings.NewReader(stream))
	router := NewRouter()
	var sink capBuf
	router.Register("%1", &sink)

	stop := runLoop(reader, router) // extracted inner loop from Run (steps 10-11)
	if !stop {
		t.Fatal("runLoop should return true on %exit")
	}
	if sink.String() != "hello" {
		t.Errorf("routed %q, want hello", sink.String())
	}
}
```

(Extract `runLoop(reader *controlmode.Reader, router *Router) bool` from `Run` so it is
unit-testable without ssh/tmux. `newTestReader`/`capBuf` are tiny test helpers.)

- [ ] **Step 2: Run test to verify it fails** → `undefined: runLoop`.
- [ ] **Step 3: Implement `Run` + `runLoop`** per the sequence above.
- [ ] **Step 4: Run** `cd picker && go test ./remotebridge/daemon/ -v` and `go build ./...` → PASS/builds.
- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/daemon/daemon.go picker/remotebridge/cmd/daemon/main.go
git commit -m "feat(remote): daemon orchestration + one-window mirror loop [#167]"
```

---

### Task 9: Offline integration test + nix packaging (`tests/remote-m2-integration.bats`, `picker/default.nix`, `flake.nix`)

End-to-end against a **local** second tmux (no ssh): the daemon mirrors a local throwaway
window into a local `host-sess` window, and a remote split reflects. Runs in
`nix flake check`, using the vendored `buildGoModule` binaries (follow the M1
`remote-bridge-integration-tests` precedent exactly).

**Files:**
- Create: `tests/remote-m2-integration.bats`
- Modify: `picker/default.nix` (add `remotebridge/cmd/daemon` and `remotebridge/cmd/renderer` to `subPackages`; the module already vendors `golang.org/x/term`)
- Modify: `flake.nix` (add a `remote-m2-integration-tests` check wired like `remote-bridge-integration-tests`)

**Interfaces:** none (test only).

- [ ] **Step 1: Write the failing test**

```bash
#!/usr/bin/env bats
# Mirror a local 2-pane tmux window into a local host-sess window via the daemon.
# DAEMON / RENDERER are passed as absolute store paths by flake.nix.

setup() {
  export TMUX_TMPDIR="$BATS_TEST_TMPDIR"
  SRC="tmux -L m2src -f /dev/null"      # stands in for the "remote"
  DST="tmux -L m2dst -f /dev/null"      # the local mirror target
  $SRC kill-server 2>/dev/null || true
  $DST kill-server 2>/dev/null || true
}
teardown() {
  $SRC kill-server 2>/dev/null || true
  $DST kill-server 2>/dev/null || true
}

@test "daemon mirrors a 2-pane remote window with matching pane dims" {
  # remote: a 210x52 window, uneven horizontal split
  $SRC new-session -d -s rem -x 210 -y 52
  $SRC split-window -h -t rem
  $SRC resize-pane -t rem.0 -x 60
  run $SRC display-message -p -t rem -F '#{window_layout}'
  [ "$status" -eq 0 ]

  # Launch the daemon against SRC's control mode, mirroring into DST host-sess:1.
  # (flake.nix exports DAEMON + RENDERER absolute paths; the daemon's Ctl is a
  #  `tmux -L m2src -C attach-session -t rem` subprocess, LocalTmux runs `tmux -L m2dst`.)
  LZTMUX_TEST_SRC="$SRC" LZTMUX_TEST_DST="$DST" RENDERER="$RENDERER" \
    run timeout 10 "$DAEMON" --test-local \
      --src-socket m2src --dst-socket m2dst \
      --remote-win rem:1 --local-sess host-sess
  [ "$status" -eq 0 ] || [ "$status" -eq 124 ]  # 124 = timeout (daemon stays up)

  # Assert DST has a 2-pane window whose pane dims equal SRC's.
  run $DST list-panes -t host-sess:1 -F '#{pane_width}x#{pane_height}'
  [ "$status" -eq 0 ]
  src_dims=$($SRC list-panes -t rem -F '#{pane_width}x#{pane_height}' | sort | tr '\n' ' ')
  dst_dims=$($DST list-panes -t host-sess:1 -F '#{pane_width}x#{pane_height}' | sort | tr '\n' ' ')
  [ "$src_dims" = "$dst_dims" ]
}
```

> The daemon needs a `--test-local` mode: instead of ssh, it spawns
> `tmux -L <src-socket> -C attach-session -t <remote-win-session>` as `Ctl`, and
> `LocalTmux` runs `tmux -L <dst-socket>`. This keeps the whole test offline and in
> `nix flake check`. Wire `WinSize` to the remote window's initial dims in test mode.

- [ ] **Step 2: Run to verify it fails**

Run: `nix build .#checks.x86_64-linux.remote-m2-integration-tests` (after wiring) — FAIL until daemon `--test-local` exists and packaging is added.

- [ ] **Step 3: Implement** the daemon `--test-local` flag + add `subPackages` + the flake check. Recompute the picker `vendorHash` only if deps changed (they shouldn't — `x/term` already vendored).

- [ ] **Step 4: Run to verify it passes**

Run: `nix flake check` (or the single check above). Expected: PASS. Also confirm the
**1-pane regression anchor**: add a second bats case with a single-pane remote window and
assert the daemon path renders identically to M1 (pane dims match, no split).

- [ ] **Step 5: Commit**

```bash
git add tests/remote-m2-integration.bats picker/default.nix flake.nix
git commit -m "test(remote): offline M2.1 daemon mirror integration check [#167]"
```

---

## Self-Review

**Spec coverage (M2.1 slice):**
- daemon owns one connection → Task 8. ✓
- one window mirrored to native multi-pane → Tasks 1,6,8. ✓
- layout translation → Tasks 1,6. ✓
- size local→remote convergence → Task 7,8; asserted in Task 9. ✓
- per-pane renderers fed by daemon → Tasks 2,3,4,8. ✓
- live %layout-change → Task 8 (minimal add/remove); Task 9 could add a split-reflect case. ✓
- firehose filter to target window (pause-after deferred to M2.2) → Task 8 step 10. ✓
- 1-pane == M1 regression anchor → Task 9 step 4. ✓
- seed handshake in daemon (I2) → Task 5. ✓
- renderers by absolute store path (I4) → Task 8 step 8 + Task 9 packaging. ✓
- non-blocking daemon→renderer sink (I1) → Task 8 step 6 (large buffer; dirty-reseed TODO M2.2). ✓

**Deferred to later milestones (intentionally NOT in this plan):** structural input /
keybind gate (M2.3), all-windows mirror + notifications + pause-after (M2.2), copy-mode /
mouse / clipboard / focus (M2.4), reconcile/respawn + `@bridge_win` opt-out tag (M2.2/M2.3),
daemon systemd supervision + ssh keepalive (folded into `lztmux-remote-open` wiring at M2.3).

**Placeholder scan:** the integration Tasks 8–9 intentionally carry a documented
orchestration sequence rather than one pre-written 200-line `Run` body — each sub-step is
concrete (exact commands, exact ordering), and the pure units it composes (Tasks 1–7) have
complete code. The `--test-local` seam is specified. No "TBD"/"add error handling"/"similar
to Task N" left.

**Type consistency:** `Layout` gains `Raw string` (Task 1 note + Task 6). `PaneCell.ID`,
`controlmode.ParseLayout`, `daemon.PaneSeed`, `daemon.Router`, `daemon.Frame*`,
`render.Run`, `daemon.PlanWindow`/`RemotePaneOrder`/`ConvergeCmd` names are used
consistently across tasks.
