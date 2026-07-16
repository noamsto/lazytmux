# Remote pseudo-session bridge — Milestone 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirror one live remote tmux window as one native local tmux window — output renders, keystrokes drive the real remote pane, resize propagates — via the remote tmux's control-mode (`-CC`) stream, with no inner status bar.

**Architecture:** A Go binary `lztmux-remote-bridge` runs inside a fresh local tmux pane. It wraps `ssh … tmux -C attach-session -t <sess>`, parses the control-mode stream, unescapes `%output` for the target pane and writes raw bytes to its stdout (the pane renders), forwards its raw stdin as `send-keys -H`, and maps SIGWINCH to `refresh-client -C`. An initial `capture-pane -e` snapshot (plus terminal-mode replay) seeds the pane since control mode has no scrollback replay. Pure protocol logic lives in a `controlmode` package; the tty/pump lives in a `render` package; `main.go` wires them — so Milestone 2 (daemon + per-pane renderers) reuses both halves without a rewrite.

**Tech Stack:** Go 1.25 (`github.com/noamsto/lazytmux/picker` module), tmux next-3.8 control mode, Nix (`buildGoModule`), bats (integration in `nix flake check`).

## Global Constraints

- Go module: `github.com/noamsto/lazytmux/picker`, `go 1.25.0`. New code lives under `picker/remotebridge/`.
- The binary is built as a subPackage of the existing picker `buildGoModule` (like `splash`, `statusline`); adding Go deps changes `vendorHash` in `config/tmux.conf.nix` — recompute it, don't hand-edit.
- Remote invocation MUST use `ssh -T -e none <host> -- env TMUX_TMPDIR=<runtime> <abs-tmux> …` — the remote tmux is not on the non-interactive ssh PATH and lazytmux servers live under `$XDG_RUNTIME_DIR`, not `/tmp/tmux-$UID`.
- Octal unescape is **byte-oriented** (a UTF-8 rune may split across two `%output` lines) — never decode `%output` data as a string.
- `send-keys -H` arg is hex; chunk it under tmux's command length cap.
- Shell scripts (none new here) are bash; Go follows the module's existing style (see `picker/statusline`).
- All commits use conventional-commit messages; commit after each green step.

---

## File Structure

- `picker/remotebridge/controlmode/parse.go` — pure control-line parser + octal unescape (no I/O).
- `picker/remotebridge/controlmode/encode.go` — input → `send-keys -H` hex, chunked.
- `picker/remotebridge/controlmode/parse_test.go`, `encode_test.go` — unit tests.
- `picker/remotebridge/render/snapshot.go` — build the seed byte stream from `capture-pane -e` output + cursor + mode flags.
- `picker/remotebridge/render/snapshot_test.go` — unit tests.
- `picker/remotebridge/render/tty.go` — raw-mode setup/restore + byte pump (thin; exercised by integration test).
- `picker/remotebridge/main.go` — CLI wiring: spawn ssh, connect streams, resize, teardown.
- `tests/remote-bridge-integration.bats` — bridge against a LOCAL `tmux -C` (no ssh).
- `config/tmux.conf.nix` — add the `remotebridge` binary to the wrapped-tmux PATH; add the `lztmux-remote-open` helper script.
- `scripts/lztmux-remote-open.sh` — creates the local `<host>-<sess>` session + window running the bridge.

---

### Task 1: `controlmode` — byte-oriented octal unescape

**Files:**
- Create: `picker/remotebridge/controlmode/parse.go`
- Test: `picker/remotebridge/controlmode/parse_test.go`

**Interfaces:**
- Produces: `func Unescape(data string) []byte` — decodes tmux `%output` escaping (bytes `< 0x20` and `\` are emitted as three-digit octal `\NNN`; everything else literal) back to raw bytes.

- [ ] **Step 1: Write the failing test**

```go
package controlmode

import "testing"

func TestUnescape(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`ls /`, []byte("ls /")},
		{`ls /\015\015\012`, []byte("ls /\r\r\n")},
		{`a\134b`, []byte(`a\b`)},          // \134 == backslash
		{`\033[0m`, []byte("\x1b[0m")},     // ESC then literal
		{`\342\230\203`, []byte{0xe2, 0x98, 0x83}}, // a UTF-8 rune as raw bytes
	}
	for _, c := range cases {
		got := Unescape(c.in)
		if string(got) != string(c.want) {
			t.Errorf("Unescape(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestUnescape -v`
Expected: FAIL — `undefined: Unescape`.

- [ ] **Step 3: Write minimal implementation**

```go
package controlmode

// Unescape decodes tmux control-mode %output data: bytes below 0x20 and the
// backslash are written as three-digit octal (\NNN); all else is literal.
// Operates on bytes — a UTF-8 rune may be split across two %output lines.
func Unescape(data string) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+3 < len(data)+1 && i+3 <= len(data)-1+1 {
		}
		if data[i] == '\\' && i+3 < len(data) {
			// try three octal digits
			d0, d1, d2 := data[i+1], data[i+2], data[i+3]
			if isOctal(d0) && isOctal(d1) && isOctal(d2) {
				out = append(out, (d0-'0')<<6|(d1-'0')<<3|(d2-'0'))
				i += 3
				continue
			}
		}
		out = append(out, data[i])
	}
	return out
}

func isOctal(b byte) bool { return b >= '0' && b <= '7' }
```

- [ ] **Step 4: Simplify the stray guard, re-run to verify it passes**

Remove the dead `if data[i] == '\\' && i+3 < len(data)+1 …{}` block (scaffolding). Then:
Run: `cd picker && go test ./remotebridge/controlmode/ -run TestUnescape -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/controlmode/parse.go picker/remotebridge/controlmode/parse_test.go
git commit -m "feat(remotebridge): byte-oriented octal unescape for %output"
```

---

### Task 2: `controlmode` — parse control lines

**Files:**
- Modify: `picker/remotebridge/controlmode/parse.go`
- Test: `picker/remotebridge/controlmode/parse_test.go`

**Interfaces:**
- Consumes: `Unescape` (Task 1).
- Produces:
  - `type Line struct { Kind Kind; Pane string; Args []string; Data []byte }`
  - `type Kind int` with consts `Output, Begin, End, Error, WindowClose, Exit, LayoutChange, Other`.
  - `func ParseLine(raw string) Line` — `raw` is one line with the trailing `\n` stripped. `%output %<pane> <data>` → `{Output, pane, nil, Unescape(data)}`. `%begin <n> <n> <flags>`/`%end`/`%error` → their Kind with `Args`. `%window-close @<id>`/`%exit`/`%layout-change` → Kind + Args. `%unlinked-*` and anything else → `Other`. Non-`%` lines → `Other`.

- [ ] **Step 1: Write the failing test**

```go
func TestParseLine(t *testing.T) {
	l := ParseLine(`%output %3 ab\015`)
	if l.Kind != Output || l.Pane != "%3" || string(l.Data) != "ab\r" {
		t.Fatalf("output parse wrong: %+v", l)
	}
	if ParseLine(`%begin 1700000000 1 0`).Kind != Begin {
		t.Error("begin")
	}
	if ParseLine(`%end 1700000000 1 0`).Kind != End {
		t.Error("end")
	}
	if ParseLine(`%error 1700000000 1 0`).Kind != Error {
		t.Error("error")
	}
	if ParseLine(`%window-close @5`).Kind != WindowClose {
		t.Error("window-close")
	}
	if ParseLine(`%exit`).Kind != Exit {
		t.Error("exit")
	}
	if ParseLine(`%unlinked-window-add @9`).Kind != Other {
		t.Error("unlinked should be Other")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestParseLine -v`
Expected: FAIL — `undefined: ParseLine` / `undefined: Output`.

- [ ] **Step 3: Write minimal implementation** (append to `parse.go`)

```go
import "strings"

type Kind int

const (
	Other Kind = iota
	Output
	Begin
	End
	Error
	WindowClose
	Exit
	LayoutChange
)

type Line struct {
	Kind Kind
	Pane string
	Args []string
	Data []byte
}

func ParseLine(raw string) Line {
	if !strings.HasPrefix(raw, "%") {
		return Line{Kind: Other}
	}
	verb, rest, _ := strings.Cut(raw, " ")
	switch verb {
	case "%output":
		pane, data, _ := strings.Cut(rest, " ")
		return Line{Kind: Output, Pane: pane, Data: Unescape(data)}
	case "%begin":
		return Line{Kind: Begin, Args: strings.Fields(rest)}
	case "%end":
		return Line{Kind: End, Args: strings.Fields(rest)}
	case "%error":
		return Line{Kind: Error, Args: strings.Fields(rest)}
	case "%window-close":
		return Line{Kind: WindowClose, Args: strings.Fields(rest)}
	case "%exit":
		return Line{Kind: Exit, Args: strings.Fields(rest)}
	case "%layout-change":
		return Line{Kind: LayoutChange, Args: strings.Fields(rest)}
	default:
		return Line{Kind: Other}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestParseLine -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/controlmode/parse.go picker/remotebridge/controlmode/parse_test.go
git commit -m "feat(remotebridge): parse control-mode lines"
```

---

### Task 3: `controlmode` — encode input as chunked `send-keys -H`

**Files:**
- Create: `picker/remotebridge/controlmode/encode.go`
- Test: `picker/remotebridge/controlmode/encode_test.go`

**Interfaces:**
- Produces: `func SendKeysArgs(pane string, b []byte, maxHexPerCmd int) [][]string` — returns one arg-slice per `send-keys` command (e.g. `{"send-keys","-H","-t","%3","61","62"}`), chunked so no command exceeds `maxHexPerCmd` hex tokens. Empty input → empty slice.

- [ ] **Step 1: Write the failing test**

```go
package controlmode

import (
	"reflect"
	"testing"
)

func TestSendKeysArgs(t *testing.T) {
	got := SendKeysArgs("%3", []byte{0x61, 0x62, 0x1b}, 2)
	want := [][]string{
		{"send-keys", "-H", "-t", "%3", "61", "62"},
		{"send-keys", "-H", "-t", "%3", "1b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(SendKeysArgs("%3", nil, 2)) != 0 {
		t.Error("empty input should yield no commands")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestSendKeysArgs -v`
Expected: FAIL — `undefined: SendKeysArgs`.

- [ ] **Step 3: Write minimal implementation**

```go
package controlmode

import "fmt"

func SendKeysArgs(pane string, b []byte, maxHexPerCmd int) [][]string {
	if len(b) == 0 {
		return nil
	}
	if maxHexPerCmd < 1 {
		maxHexPerCmd = 1
	}
	var cmds [][]string
	for i := 0; i < len(b); i += maxHexPerCmd {
		end := i + maxHexPerCmd
		if end > len(b) {
			end = len(b)
		}
		cmd := []string{"send-keys", "-H", "-t", pane}
		for _, by := range b[i:end] {
			cmd = append(cmd, fmt.Sprintf("%02x", by))
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestSendKeysArgs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/controlmode/encode.go picker/remotebridge/controlmode/encode_test.go
git commit -m "feat(remotebridge): chunked send-keys -H input encoder"
```

---

### Task 4: `controlmode` — reply-block reader (correlate `%begin`/`%end`)

**Files:**
- Modify: `picker/remotebridge/controlmode/parse.go` (add `Reader`)
- Test: `picker/remotebridge/controlmode/reader_test.go`

**Interfaces:**
- Consumes: `ParseLine` (Task 2).
- Produces:
  - `type Reader struct { … }` with `func NewReader(r io.Reader) *Reader`.
  - `func (rd *Reader) Next() (Line, bool)` — returns the next control `Line`, transparently accumulating command-reply blocks: lines between `%begin`/`%end` (or `%error`) are collected into the terminating `Line.Args[0]` = block id and `Line.Data` = the raw reply text (newline-joined). Notifications (`%output`, `%window-close`, …) that appear outside a block pass through unchanged. Returns `false` at EOF.

- [ ] **Step 1: Write the failing test**

```go
package controlmode

import (
	"strings"
	"testing"
)

func TestReaderInterleavesReplyAndOutput(t *testing.T) {
	in := strings.Join([]string{
		`%output %1 hi`,
		`%begin 100 7 0`,
		`captured line one`,
		`captured line two`,
		`%end 100 7 0`,
		`%output %1 bye`,
	}, "\n") + "\n"
	rd := NewReader(strings.NewReader(in))

	l, ok := rd.Next()
	if !ok || l.Kind != Output || string(l.Data) != "hi" {
		t.Fatalf("first should be output hi: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != End || l.Args[0] != "100" || !strings.Contains(string(l.Data), "captured line one") {
		t.Fatalf("second should be the completed reply block: %+v", l)
	}
	l, ok = rd.Next()
	if !ok || l.Kind != Output || string(l.Data) != "bye" {
		t.Fatalf("third should be output bye: %+v", l)
	}
	if _, ok = rd.Next(); ok {
		t.Fatal("expected EOF")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/controlmode/ -run TestReader -v`
Expected: FAIL — `undefined: NewReader`.

- [ ] **Step 3: Write minimal implementation** (append to `parse.go`)

```go
import "bufio"
import "io"

type Reader struct{ sc *bufio.Scanner }

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &Reader{sc: sc}
}

func (rd *Reader) Next() (Line, bool) {
	for rd.sc.Scan() {
		l := ParseLine(rd.sc.Text())
		if l.Kind != Begin {
			return l, true
		}
		// Accumulate the reply body until %end/%error (matching id in Args[0]).
		id := ""
		if len(l.Args) > 0 {
			id = l.Args[0]
		}
		var body []string
		for rd.sc.Scan() {
			raw := rd.sc.Text()
			t := ParseLine(raw)
			if t.Kind == End || t.Kind == Error {
				return Line{Kind: t.Kind, Args: []string{id}, Data: []byte(strings.Join(body, "\n"))}, true
			}
			body = append(body, raw)
		}
		return Line{Kind: End, Args: []string{id}, Data: []byte(strings.Join(body, "\n"))}, true
	}
	return Line{}, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/controlmode/ -v`
Expected: PASS (all controlmode tests).

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/controlmode/parse.go picker/remotebridge/controlmode/reader_test.go
git commit -m "feat(remotebridge): reply-block reader correlating begin/end"
```

---

### Task 5: `render` — build the seed snapshot byte stream

**Files:**
- Create: `picker/remotebridge/render/snapshot.go`
- Test: `picker/remotebridge/render/snapshot_test.go`

**Interfaces:**
- Produces: `func Seed(captured []byte, cursorX, cursorY int, altScreen, appCursorKeys bool) []byte` — returns the bytes to write to the local pane to reproduce the remote pane's current screen: clear + home, optional enter-alt-screen (`\x1b[?1049h`) and app-cursor-keys (`\x1b[?1h`), the captured screen (already SGR-annotated by `capture-pane -e`), then position the cursor (`\x1b[<y+1>;<x+1>H`). This is the mode-flag replay that makes a vim/alt-screen attach seed correctly.

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"strings"
	"testing"
)

func TestSeedAltScreenAndCursor(t *testing.T) {
	out := string(Seed([]byte("hello"), 2, 0, true, true))
	if !strings.Contains(out, "\x1b[?1049h") {
		t.Error("should enter alternate screen")
	}
	if !strings.Contains(out, "\x1b[?1h") {
		t.Error("should set application cursor keys")
	}
	if !strings.Contains(out, "hello") {
		t.Error("should include captured content")
	}
	if !strings.HasSuffix(out, "\x1b[1;3H") {
		t.Errorf("should end positioning cursor at row1 col3: %q", out)
	}
}

func TestSeedPlainNoAlt(t *testing.T) {
	out := string(Seed([]byte("x"), 0, 0, false, false))
	if strings.Contains(out, "1049h") || strings.Contains(out, "\x1b[?1h") {
		t.Error("plain seed must not set alt/app-cursor modes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/render/ -run TestSeed -v`
Expected: FAIL — `undefined: Seed`.

- [ ] **Step 3: Write minimal implementation**

```go
package render

import (
	"bytes"
	"fmt"
)

func Seed(captured []byte, cursorX, cursorY int, altScreen, appCursorKeys bool) []byte {
	var b bytes.Buffer
	if altScreen {
		b.WriteString("\x1b[?1049h")
	}
	if appCursorKeys {
		b.WriteString("\x1b[?1h")
	}
	b.WriteString("\x1b[2J\x1b[H") // clear + home
	b.Write(captured)
	fmt.Fprintf(&b, "\x1b[%d;%dH", cursorY+1, cursorX+1)
	return b.Bytes()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/render/ -run TestSeed -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add picker/remotebridge/render/snapshot.go picker/remotebridge/render/snapshot_test.go
git commit -m "feat(remotebridge): seed snapshot with mode-flag replay"
```

---

### Task 6: `render` — raw-mode tty helpers

**Files:**
- Create: `picker/remotebridge/render/tty.go`
- Test: `picker/remotebridge/render/tty_test.go`

**Interfaces:**
- Produces:
  - `func MakeRaw(fd int) (restore func() error, err error)` — put the fd (stdin) into raw mode (no echo, no canonical, ISIG off so Ctrl-C is a byte); `restore` reverts. Wraps `golang.org/x/term`.
  - `func Size(fd int) (w, h int, err error)` — current terminal size.

- [ ] **Step 1: Add the dependency**

Run: `cd picker && go get golang.org/x/term@latest`
Expected: `go.mod`/`go.sum` updated.

- [ ] **Step 2: Write the failing test** (behavioral — round-trips without a real tty by asserting error on a non-tty fd)

```go
package render

import (
	"os"
	"testing"
)

func TestSizeNonTTYErrors(t *testing.T) {
	// A pipe is not a tty; Size must return an error, not panic.
	r, _, _ := os.Pipe()
	defer r.Close()
	if _, _, err := Size(int(r.Fd())); err == nil {
		t.Error("expected error sizing a non-tty")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/render/ -run TestSizeNonTTY -v`
Expected: FAIL — `undefined: Size`.

- [ ] **Step 4: Write minimal implementation**

```go
package render

import "golang.org/x/term"

func MakeRaw(fd int) (func() error, error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, old) }, nil
}

func Size(fd int) (int, int, error) {
	return term.GetSize(fd)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/render/ -run TestSizeNonTTY -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add picker/remotebridge/render/tty.go picker/remotebridge/render/tty_test.go picker/go.mod picker/go.sum
git commit -m "feat(remotebridge): raw-mode tty + size helpers"
```

---

### Task 7: `main.go` — wire the bridge

**Files:**
- Create: `picker/remotebridge/main.go`

**Interfaces:**
- Consumes: `controlmode.NewReader`, `controlmode.SendKeysArgs`, `render.MakeRaw`, `render.Size`, `render.Seed`.
- Produces: binary `lztmux-remote-bridge`. Usage: `lztmux-remote-bridge --host <h> --session <s> --window <idx> --tmux <abs-remote-tmux> --tmpdir <remote-runtime>`.

Behavior (single window, active pane):
1. Open a control connection: `ctl := exec.Command("ssh", "-T", "-e", "none", host, "--", "env", "TMUX_TMPDIR="+tmpdir, remoteTmux, "-C", "attach-session", "-t", session)`; wire `ctl.Stdin` (a pipe we write commands to), `ctl.Stdout` (the control stream).
2. Resolve the target pane id: send `list-panes -t <session>:<window> -F '#{pane_active} #{pane_id}'` on the control pipe; read the reply block via `controlmode.Reader`; pick the active pane.
3. Set size: `render.Size(0)` → send `refresh-client -C -x W -y H`. Send initial `capture-pane -e -p -t <pane>`, read reply → `render.Seed(...)` → write to stdout. (Cursor/mode flags via `display-message -p -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'`.)
4. `restore, _ := render.MakeRaw(0); defer restore()`.
5. Goroutine A (render): loop `reader.Next()`; on `Output` for target pane → `os.Stdout.Write(l.Data)`; on `WindowClose`/`Exit` → exit.
6. Goroutine B (input): read `os.Stdin` chunks → `controlmode.SendKeysArgs(pane, chunk, 500)` → write each as a `send-keys …` line to the control pipe.
7. Signal handler: `SIGWINCH` (debounced 50ms) → `render.Size(0)` → `refresh-client -C -x W -y H`.
8. On exit: restore tty, close the control pipe (ssh detaches; remote session survives).

- [ ] **Step 1: Write `main.go`** with the behavior above. Key skeleton:

```go
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
	"github.com/noamsto/lazytmux/picker/remotebridge/render"
)

func main() {
	host := flag.String("host", "", "ssh host")
	session := flag.String("session", "", "remote session")
	window := flag.Int("window", 0, "remote window index")
	remoteTmux := flag.String("tmux", "tmux", "absolute remote tmux path")
	tmpdir := flag.String("tmpdir", "", "remote TMUX_TMPDIR")
	flag.Parse()

	ctl := exec.Command("ssh", "-T", "-e", "none", *host, "--",
		"env", "TMUX_TMPDIR="+*tmpdir, *remoteTmux, "-C", "attach-session", "-t", *session)
	stdin, _ := ctl.StdinPipe()
	stdout, _ := ctl.StdoutPipe()
	cmds := bufio.NewWriter(stdin)
	send := func(s string) { fmt.Fprintf(cmds, "%s\n", s); cmds.Flush() }

	if err := ctl.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "lztmux-remote-bridge: %v\r\n", err)
		os.Exit(1)
	}
	reader := controlmode.NewReader(stdout)

	// resolve active pane in target window
	send(fmt.Sprintf("list-panes -t %s:%d -F '#{pane_active} #{pane_id}'", *session, *window))
	pane := readActivePane(reader) // helper: consume next End block, pick "1 %id"

	// size + seed
	if w, h, err := render.Size(0); err == nil {
		send(fmt.Sprintf("refresh-client -C -x %d -y %d", w, h))
	}
	send(fmt.Sprintf("display-message -p -t %s -F '#{cursor_x} #{cursor_y} #{alternate_on} #{keypad_cursor_flag}'", pane))
	cx, cy, alt, appck := readCursor(reader)
	send(fmt.Sprintf("capture-pane -e -p -t %s", pane))
	captured := readCapture(reader)
	os.Stdout.Write(render.Seed(captured, cx, cy, alt, appck))

	restore, err := render.MakeRaw(0)
	if err == nil {
		defer restore()
	}

	// input goroutine
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for _, cmd := range controlmode.SendKeysArgs(pane, buf[:n], 500) {
					send(quoteArgs(cmd))
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// resize goroutine
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		var t *time.Timer
		for range winch {
			if t != nil {
				t.Stop()
			}
			t = time.AfterFunc(50*time.Millisecond, func() {
				if w, h, err := render.Size(0); err == nil {
					send(fmt.Sprintf("refresh-client -C -x %d -y %d", w, h))
				}
			})
		}
	}()

	// render loop (main goroutine)
	for {
		l, ok := reader.Next()
		if !ok {
			break
		}
		switch l.Kind {
		case controlmode.Output:
			if l.Pane == pane {
				os.Stdout.Write(l.Data)
			}
		case controlmode.WindowClose, controlmode.Exit:
			if restore != nil {
				restore()
			}
			return
		}
	}
}
```

Also implement the small helpers `readActivePane`, `readCursor`, `readCapture` (each consumes reply-block `Line`s from the reader) and `quoteArgs` (joins a `send-keys` arg slice into a control-mode command line; pane ids and hex are safe tokens so a plain space-join suffices).

- [ ] **Step 2: Build**

Run: `cd picker && go build ./remotebridge/`
Expected: builds with no errors.

- [ ] **Step 3: Vet**

Run: `cd picker && go vet ./remotebridge/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add picker/remotebridge/main.go
git commit -m "feat(remotebridge): wire ssh control-mode bridge (single window)"
```

---

### Task 8: Local integration test (bridge vs local `tmux -C`, no ssh)

**Files:**
- Create: `tests/remote-bridge-integration.bats`
- Modify: `picker/remotebridge/main.go` (add `--ssh` override so tests can bypass ssh)

**Interfaces:**
- Consumes: the bridge binary.
- Produces: a bats test proving the bridge renders a local session's window and forwards input, with no remote host.

- [ ] **Step 1: Add a `--ssh` flag** so the control command is configurable (default `ssh`; tests set it to a wrapper that runs local `tmux -C` directly). In `main.go`, replace the hardcoded `exec.Command("ssh", …)` with the value of `--ssh` split into argv, appending the tmux control args. For the local case the test passes `--ssh ""` and `--host ""` so the bridge runs `tmux -C attach-session -t <sess>` locally.

Adjust the command construction:
```go
sshCmd := flag.String("ssh", "ssh", "control transport command (empty = run tmux locally)")
// ...
var ctl *exec.Cmd
if *sshCmd == "" {
	ctl = exec.Command(*remoteTmux, "-C", "attach-session", "-t", *session)
} else {
	ctl = exec.Command(*sshCmd, "-T", "-e", "none", *host, "--",
		"env", "TMUX_TMPDIR="+*tmpdir, *remoteTmux, "-C", "attach-session", "-t", *session)
}
```

- [ ] **Step 2: Write the bats test**

```bash
#!/usr/bin/env bats

setup() {
  SOCK="$BATS_TEST_TMPDIR/t.sock"
  BRIDGE="$BATS_TEST_TMPDIR/bridge"
  ( cd "$BATS_TEST_DIRNAME/../picker" && go build -o "$BRIDGE" ./remotebridge/ )
  tmux -S "$SOCK" -f /dev/null new-session -d -s src -x 80 -y 24
  tmux -S "$SOCK" send-keys -t src "printf HELLO_BRIDGE" Enter
  sleep 0.5
}

teardown() { tmux -S "$SOCK" kill-server 2>/dev/null || true; }

@test "bridge renders the remote window's current screen" {
  run timeout 5 "$BRIDGE" --ssh "" --tmux "tmux -S $SOCK" --session src --window 0 </dev/null
  [[ "$output" == *HELLO_BRIDGE* ]]
}
```

Note: `--tmux "tmux -S $SOCK"` carries the socket; in `main.go`, split `--tmux` on spaces when building argv so `tmux -S <sock>` works for both local and (single-token) remote paths.

- [ ] **Step 3: Run the test**

Run: `cd $(git rev-parse --show-toplevel) && bats tests/remote-bridge-integration.bats`
Expected: PASS — output contains `HELLO_BRIDGE`.

- [ ] **Step 4: Wire it into `nix flake check`** — add a `remote-bridge-integration-tests` check mirroring the existing `remote-integration-tests` derivation in `flake.nix` (same `bats` + `pkgs.tmux` + `go` inputs; runs `bats tests/remote-bridge-integration.bats`).

Run: `cd $(git rev-parse --show-toplevel) && git add -A && nix flake check .#remote-bridge-integration-tests 2>&1 | tail -5`
Expected: check passes.

- [ ] **Step 5: Commit**

```bash
git add tests/remote-bridge-integration.bats picker/remotebridge/main.go flake.nix
git commit -m "test(remotebridge): local tmux -C integration test in flake check"
```

---

### Task 9: Nix packaging + `lztmux-remote-open`

**Files:**
- Create: `scripts/lztmux-remote-open.sh`
- Modify: `config/tmux.conf.nix` (build/expose the `remotebridge` binary; recompute `vendorHash` if deps changed; add the open script to PATH)

**Interfaces:**
- Consumes: the `lztmux-remote-bridge` binary.
- Produces: `lztmux-remote-open <host> [<session>] [<window>]` on the wrapped-tmux PATH.

- [ ] **Step 1: Write `scripts/lztmux-remote-open.sh`**

```bash
#!/usr/bin/env bash
# Create a local <host>-<sess> session with one window running the bridge for
# the remote window. M1: single window; resolve remote tmux path + TMUX_TMPDIR.
set -euo pipefail
host="$1"
sess="${2:-}"
win="${3:-0}"

remote_tmpdir="${LZTMUX_REMOTE_TMPDIR:-/run/user/$(ssh "$host" id -u)}"
remote_tmux="$(ssh "$host" "command -v tmux")"

if [[ -z $sess ]]; then
	sess="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-sessions -F '#{session_name}' | head -1")"
fi

local_sess="${host}-${sess}"
tmux new-session -d -s "$local_sess" -n "$sess" \
	"lztmux-remote-bridge --host '$host' --session '$sess' --window '$win' --tmux '$remote_tmux' --tmpdir '$remote_tmpdir'"
tmux switch-client -t "=$local_sess"
```

- [ ] **Step 2: shellcheck**

Run: `shellcheck scripts/lztmux-remote-open.sh`
Expected: clean (fix any warnings).

- [ ] **Step 3: Wire into nix** — in `config/tmux.conf.nix`, add `lztmux-remote-open` to the scripts set (so it becomes a `writeShellScriptBin` on PATH), and ensure the picker `buildGoModule` builds `./remotebridge` (subPackages are auto-discovered; the binary `lztmux-remote-bridge` must be added to the wrapped-tmux `--prefix PATH` list alongside the other picker binaries). If `go get golang.org/x/term` (Task 6) changed `go.mod`, recompute `vendorHash`:

Run:
```bash
cd $(git rev-parse --show-toplevel)
git add -A
nix build .#default 2>&1 | tail -20   # will print the expected vendorHash on mismatch
# paste the printed hash into config/tmux.conf.nix vendorHash, then:
nix build .#default 2>&1 | tail -5
```
Expected: `./result/bin/tmux` builds; `./result/bin/lztmux-remote-bridge` and `lztmux-remote-open` exist.

- [ ] **Step 4: Full flake check**

Run: `nix flake check 2>&1 | tail -15`
Expected: all checks pass (incl. `remote-bridge-integration-tests`).

- [ ] **Step 5: Commit**

```bash
git add config/tmux.conf.nix scripts/lztmux-remote-open.sh
git commit -m "feat(remotebridge): package bridge + lztmux-remote-open command"
```

---

## Manual verification (after deploy)

Deploy g5 (`nh home switch`) + g6 rebuild, then from a local pane:
`lztmux-remote-open tp-g6 nix-config 1` → a local `tp-g6-nix-config` session appears, its window rendering the live remote window with no inner bar; typing drives the remote pane; resizing the local pane resizes the remote. **Acceptance gate: attach a window running vim** and confirm the alt-screen/cursor seed is correct and editing works.

## Self-review notes

- Spec coverage: M1 (single window render/input/resize/snapshot) ✓; package split controlmode/render/cmd ✓; `ssh -T -e none` + abs tmux + TMUX_TMPDIR ✓; byte-oriented unescape ✓; chunked send-keys -H ✓; local-tmux integration test ✓; naming inside `<host>-<sess>` ✓. Deferred to M2 (not in this plan): firehose `pause-after`, `%extended-output`, multi-window, `refresh-client -B` subscriptions, `-f ignore-size`/mismatch stop-paint, golden transcripts — tracked in the spec.
- The `-f ignore-size` and remote>local stop-paint from the spec are **M2**; M1 assumes exclusive attach (documented in spec).
