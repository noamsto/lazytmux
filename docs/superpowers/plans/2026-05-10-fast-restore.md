# Fast Non-Corrupting Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `tmux-state`'s paste-buffer/send-keys restore path with resurrect's pane-creation-command pattern, eliminating multi-MB stdin dumps that corrupt and slow down restores.

**Architecture:** Each pane is born running a single shell string of the form `<self> cat-scrollback <sha>; exec <cmd-or-shell>`, baked into the trailing `shell-command` argument of `tmux new-session`/`new-window`/`split-window`. Saved scrollback renders as static terminal output (not shell input); allow-listed commands replace `cat` via `exec`. Post-creation `RelaunchCommand` and `RestoreScrollback` actions are deleted entirely.

**Tech Stack:** Go 1.22+, `cobra`, `klauspost/compress/zstd`, `google/go-cmp`. Implementation lives in `~/Data/git/noamsto/tmux-state` (separate repo from lazytmux). Spec: `~/Data/git/lazytmux/docs/superpowers/specs/2026-05-10-fast-restore-design.md`.

**Working directory for ALL tasks:** `/home/noams/Data/git/noamsto/tmux-state` unless otherwise noted.

---

## File map

**Create:**
- `internal/scrollback/store.go` — add public `Stream(ctx, sha) (io.ReadCloser, error)` method (alongside existing `Get`).
- `internal/restore/shell.go` — `DefaultShell(t Runner) (path string, isBash bool)` resolver.
- `internal/restore/shell_test.go` — unit tests for the resolver.
- `internal/restore/startup.go` — `BuildStartupCommand(opts StartupOpts) string` pure helper.
- `internal/restore/startup_test.go` — table-driven tests covering all four spec rows.
- `cmd/tmux-state/cat_scrollback.go` — hidden `cat-scrollback <sha>` cobra subcommand.
- `cmd/tmux-state/cat_scrollback_test.go` — black-box test of the subcommand.

**Modify:**
- `internal/restore/plan.go` — add `StartupCommand string` to `CreateSession`/`CreateWindow`/`SplitPane`; delete `RelaunchCommand` and `RestoreScrollback` types; change `BuildPlan` signature to take a `BuildOptions` struct; rewrite the loop body to compose startup commands inline.
- `internal/restore/apply.go` — rewrite `Apply` to emit startup commands as trailing argv; delete `ApplyWithScrollback`, `pasteScrollback`, `ScrollbackReader`, `randHex`, and the `os`/`crypto/rand`/`encoding/hex` imports.
- `internal/restore/plan_test.go` — adapt three existing tests to new shape; add a fresh table-driven test for the four startup-command rows in BuildPlan output.
- `internal/restore/apply_test.go` — replace paste-buffer test with one verifying trailing shell-command argv presence/absence; drop `sbReader` mock.
- `cmd/tmux-state/main.go` — register new subcommand; in `newRestoreCmd`/`newUndoCmd`/`newPickCmd` resolve `self`+`defaultShell` once, call `BuildPlan` with the new options, drop scrollback wiring from Apply path.

**Untouched (verify still pass):**
- `integration_test.go` — only tests save roundtrip, no restore.
- `internal/restore/plan.go::SetLayout` — unchanged.

---

### Task 1: Add `Stream` method to scrollback store

**Files:**
- Modify: `internal/scrollback/store.go`
- Test: `internal/scrollback/store_test.go`

**Why first:** `cat-scrollback` (Task 2) needs a memory-bounded reader. Today's `Get` loads the entire decompressed scrollback into a `[]byte`. For a multi-MB pane, we want streaming.

- [ ] **Step 1: Write the failing test**

Add to `internal/scrollback/store_test.go` (preserve existing imports):

```go
func TestStreamReadsExistingSha(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := scrollback.New(dir)
	content := []byte("hello scrollback")
	sha, _, err := store.Put(ctx, content)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := store.Stream(ctx, sha)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestStreamMissingShaReturnsNotExist(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := scrollback.New(dir)
	_, err := store.Stream(ctx, "deadbeef00000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for missing sha")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}
```

Add to existing imports:
```go
import (
	"errors"
	"io"
	"io/fs"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/scrollback/ -run 'TestStream' -v
```

Expected: FAIL — `store.Stream undefined`.

- [ ] **Step 3: Implement `Stream`**

In `internal/scrollback/store.go`, add after the existing `Get` method (around line 93):

```go
// Stream returns a streaming reader of the decompressed scrollback identified
// by sha. The caller MUST Close the returned ReadCloser. If the file does not
// exist, the returned error wraps fs.ErrNotExist.
func (s *Store) Stream(_ context.Context, sha string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(sha))
	if err != nil {
		return nil, fmt.Errorf("open scrollback: %w", err)
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	return &streamReader{file: f, dec: dec}, nil
}

// streamReader couples a zstd.Decoder with its backing file so Close releases both.
type streamReader struct {
	file *os.File
	dec  *zstd.Decoder
}

func (r *streamReader) Read(p []byte) (int, error) { return r.dec.Read(p) }

func (r *streamReader) Close() error {
	r.dec.Close()
	return r.file.Close()
}
```

Add `"io"` to the file's imports (alongside the existing `io.ReadAll` usage).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/scrollback/ -v
```

Expected: ALL pass, including the two new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/scrollback/
git commit -m "feat(scrollback): add streaming Stream(sha) reader

Bounded-memory alternative to Get() for callers that pipe scrollback
contents to stdout. Returned ReadCloser owns the underlying file plus
zstd decoder; Close releases both. Wraps fs.ErrNotExist on miss so
callers can branch silently."
```

---

### Task 2: Add hidden `cat-scrollback <sha>` subcommand

**Files:**
- Create: `cmd/tmux-state/cat_scrollback.go`
- Create: `cmd/tmux-state/cat_scrollback_test.go`
- Modify: `cmd/tmux-state/main.go` (register the command)

- [ ] **Step 1: Write the failing test**

Create `cmd/tmux-state/cat_scrollback_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/noamsto/tmux-state/internal/scrollback"
)

func TestCatScrollbackStreamsExistingSha(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := scrollback.New(dir)
	content := []byte("history line one\nhistory line two\n")
	sha, _, err := store.Put(ctx, content)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := runCatScrollback(ctx, store, sha, &stdout); err != nil {
		t.Fatalf("runCatScrollback: %v", err)
	}
	if got := stdout.String(); got != string(content) {
		t.Errorf("stdout = %q, want %q", got, content)
	}
}

func TestCatScrollbackMissingShaSilentExitZero(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := scrollback.New(dir)
	missingSha := strings.Repeat("0", 64)

	var stdout bytes.Buffer
	if err := runCatScrollback(ctx, store, missingSha, &stdout); err != nil {
		t.Fatalf("runCatScrollback should swallow %v but got error", fs.ErrNotExist)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
}

func TestCatScrollbackMalformedShaErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := scrollback.New(dir)

	var stdout bytes.Buffer
	if err := runCatScrollback(ctx, store, "not-a-sha", &stdout); err == nil {
		t.Fatal("expected error for malformed sha")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./cmd/tmux-state/ -run 'TestCatScrollback' -v
```

Expected: FAIL — `runCatScrollback undefined`.

- [ ] **Step 3: Implement the subcommand**

Create `cmd/tmux-state/cat_scrollback.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/noamsto/tmux-state/internal/config"
	"github.com/noamsto/tmux-state/internal/scrollback"
)

var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// newCatScrollbackCmd is an INTERNAL helper used by restore plans. It is
// hidden from --help and the API is not stable. The pane-creation command
// emitted by restore.BuildPlan invokes:
//
//	<tmux-state-binary> cat-scrollback <sha>
//
// to render saved scrollback as static terminal output before exec'ing the
// pane's interactive program. See spec 2026-05-10-fast-restore-design.md.
func newCatScrollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cat-scrollback <sha>",
		Short:  "Stream stored scrollback to stdout (internal helper)",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			if err := cfg.EnsureDirs(); err != nil {
				return err
			}
			store := scrollback.New(cfg.ScrollbackDir)
			return runCatScrollback(cmd.Context(), store, args[0], os.Stdout)
		},
	}
}

// runCatScrollback streams the scrollback identified by sha to w.
//
// Behavior contract (must match spec):
//   - Valid sha + file present  → stream content, return nil.
//   - Valid sha + file missing  → write nothing, return nil (silent degrade).
//   - Mid-stream I/O error      → return nil after partial write
//     (restore must never fail because of stale scrollback).
//   - Malformed sha             → return error (BuildPlan bug, not user-facing).
func runCatScrollback(ctx context.Context, store *scrollback.Store, sha string, w io.Writer) error {
	if !shaPattern.MatchString(sha) {
		return fmt.Errorf("invalid sha: %q", sha)
	}
	rc, err := store.Stream(ctx, sha)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil // any other store error: degrade silently
	}
	defer rc.Close()
	_, _ = io.Copy(w, rc) // mid-stream errors swallowed by design
	return nil
}
```

In `cmd/tmux-state/main.go`, add `newCatScrollbackCmd(),` to the `root.AddCommand(...)` call (around line 49-60), preserving existing alphabetical-ish ordering — append at the end is fine:

```go
root.AddCommand(
    newVersionCmd(),
    newSaveCmd(),
    newRestoreCmd(),
    newUndoCmd(),
    newPickCmd(),
    newCaptureEventCmd(),
    newIndexUpdateCmd(),
    newListCmd(),
    newPruneCmd(),
    newGCCmd(),
    newCatScrollbackCmd(),
)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/tmux-state/ -run 'TestCatScrollback' -v
go build ./...
```

Expected: tests PASS; build succeeds.

- [ ] **Step 5: Smoke-test against a real stored scrollback**

```bash
go run ./cmd/tmux-state list 2>/dev/null | head -5
```

If you have an existing snapshot in your store, you can extract a sha by hand:

```bash
sha=$(ls $HOME/.local/share/tmux-state/scrollbacks/00/ 2>/dev/null | head -1 | sed 's/\.zst$//')
test -n "$sha" && go run ./cmd/tmux-state cat-scrollback "$sha" | head -3
```

Expected: prints the first three lines of a real saved scrollback (or nothing if you have no `00/` shard yet — that's fine, the unit tests cover correctness).

Verify the command does NOT appear in user-facing help:

```bash
go run ./cmd/tmux-state --help | grep -c cat-scrollback
```

Expected: `0` (hidden flag works).

- [ ] **Step 6: Commit**

```bash
git add cmd/tmux-state/
git commit -m "feat(cmd): add hidden cat-scrollback subcommand

Internal helper invoked by restore plans to render saved scrollback as
static pane stdout before exec'ing the interactive program. Hidden from
--help; behavior contract documented in runCatScrollback godoc.

Silent degrade on missing/corrupt scrollback so restore never fails
because of stale references in old snapshots."
```

---

### Task 3: Default-shell resolver

**Files:**
- Create: `internal/restore/shell.go`
- Create: `internal/restore/shell_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/restore/shell_test.go`:

```go
package restore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/noamsto/tmux-state/internal/restore"
)

type stubRunner struct {
	out string
	err error
}

func (s stubRunner) Run(_ context.Context, _ []string) (string, error) {
	return s.out, s.err
}

func TestDefaultShellPrefersTmuxOption(t *testing.T) {
	got, isBash := restore.DefaultShell(context.Background(), stubRunner{out: "/usr/bin/zsh\n"}, "")
	if got != "/usr/bin/zsh" {
		t.Errorf("path = %q, want /usr/bin/zsh", got)
	}
	if isBash {
		t.Error("isBash should be false for zsh")
	}
}

func TestDefaultShellDetectsBashByBasename(t *testing.T) {
	_, isBash := restore.DefaultShell(context.Background(), stubRunner{out: "/usr/bin/bash"}, "")
	if !isBash {
		t.Error("isBash should be true for bash")
	}
}

func TestDefaultShellFallsBackToShellEnv(t *testing.T) {
	got, _ := restore.DefaultShell(context.Background(), stubRunner{out: ""}, "/bin/fish")
	if got != "/bin/fish" {
		t.Errorf("path = %q, want /bin/fish (from $SHELL fallback)", got)
	}
}

func TestDefaultShellFallsBackToShWhenAllEmpty(t *testing.T) {
	got, _ := restore.DefaultShell(context.Background(), stubRunner{out: ""}, "")
	if got != "/bin/sh" {
		t.Errorf("path = %q, want /bin/sh", got)
	}
}

func TestDefaultShellSurvivesTmuxError(t *testing.T) {
	got, _ := restore.DefaultShell(context.Background(), stubRunner{err: errors.New("no server")}, "/bin/zsh")
	if got != "/bin/zsh" {
		t.Errorf("path = %q, want /bin/zsh (fallback after tmux error)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/restore/ -run 'TestDefaultShell' -v
```

Expected: FAIL — `restore.DefaultShell undefined`.

- [ ] **Step 3: Implement the resolver**

Create `internal/restore/shell.go`:

```go
package restore

import (
	"context"
	"path/filepath"
	"strings"
)

// DefaultShell resolves the user's preferred login shell for restored panes.
//
// Resolution order:
//  1. tmux's own default-shell option (`tmux show-option -gqv default-shell`)
//  2. shellEnv (caller passes os.Getenv("SHELL"))
//  3. /bin/sh
//
// Returns the resolved path and whether its basename is "bash" (so callers
// can prepend -l to the relaunch arg list, matching tmux-resurrect behavior).
func DefaultShell(ctx context.Context, t Runner, shellEnv string) (string, bool) {
	path := ""
	if out, err := t.Run(ctx, []string{"show-option", "-gqv", "default-shell"}); err == nil {
		path = strings.TrimSpace(out)
	}
	if path == "" {
		path = strings.TrimSpace(shellEnv)
	}
	if path == "" {
		path = "/bin/sh"
	}
	return path, filepath.Base(path) == "bash"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/restore/ -run 'TestDefaultShell' -v
```

Expected: ALL five tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/restore/shell.go internal/restore/shell_test.go
git commit -m "feat(restore): add DefaultShell resolver

Resolves the shell for restored panes via tmux's default-shell option
with fallbacks to \$SHELL then /bin/sh. Returns isBash so callers can
prepend -l to relaunch args (matches tmux-resurrect)."
```

---

### Task 4: Pure `BuildStartupCommand` helper

**Files:**
- Create: `internal/restore/startup.go`
- Create: `internal/restore/startup_test.go`

**Why separate:** The startup-command composition has four distinct branches (per spec table). Isolating it as a pure function gives us cheap exhaustive table tests; `BuildPlan` (Task 5) just wires arguments in.

- [ ] **Step 1: Write the failing test**

Create `internal/restore/startup_test.go`:

```go
package restore_test

import (
	"testing"

	"github.com/noamsto/tmux-state/internal/restore"
)

func TestBuildStartupCommand(t *testing.T) {
	tests := []struct {
		name string
		opts restore.StartupOpts
		want string
	}{
		{
			name: "empty: no scrollback no relaunch",
			opts: restore.StartupOpts{Self: "/usr/bin/tmux-state", DefaultShell: "/bin/zsh"},
			want: "",
		},
		{
			name: "relaunch only",
			opts: restore.StartupOpts{
				Self: "/usr/bin/tmux-state", DefaultShell: "/bin/zsh",
				RelaunchCmd:  "nvim",
				RelaunchArgs: []string{"file.go"},
			},
			want: `nvim "file.go"`,
		},
		{
			name: "scrollback only",
			opts: restore.StartupOpts{
				Self: "/usr/bin/tmux-state", DefaultShell: "/bin/zsh",
				ScrollbackSHA: "abc123",
			},
			want: `'/usr/bin/tmux-state' cat-scrollback abc123; exec /bin/zsh`,
		},
		{
			name: "scrollback + relaunch",
			opts: restore.StartupOpts{
				Self: "/usr/bin/tmux-state", DefaultShell: "/bin/zsh",
				ScrollbackSHA: "abc123",
				RelaunchCmd:   "htop",
			},
			want: `'/usr/bin/tmux-state' cat-scrollback abc123; exec htop`,
		},
		{
			name: "scrollback + bash gets -l",
			opts: restore.StartupOpts{
				Self: "/usr/bin/tmux-state", DefaultShell: "/usr/bin/bash", IsBash: true,
				ScrollbackSHA: "abc123",
			},
			want: `'/usr/bin/tmux-state' cat-scrollback abc123; exec /usr/bin/bash -l`,
		},
		{
			name: "self path with single quote gets escaped",
			opts: restore.StartupOpts{
				Self: "/weird'path/tmux-state", DefaultShell: "/bin/zsh",
				ScrollbackSHA: "abc",
			},
			want: `'/weird'\''path/tmux-state' cat-scrollback abc; exec /bin/zsh`,
		},
		{
			name: "relaunch with multiple quoted args",
			opts: restore.StartupOpts{
				Self: "/usr/bin/tmux-state", DefaultShell: "/bin/zsh",
				RelaunchCmd:  "ssh",
				RelaunchArgs: []string{"-p", "2222", "host"},
			},
			want: `ssh "-p" "2222" "host"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restore.BuildStartupCommand(tt.opts)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/restore/ -run 'TestBuildStartupCommand' -v
```

Expected: FAIL — `restore.StartupOpts` / `restore.BuildStartupCommand` undefined.

- [ ] **Step 3: Implement**

Create `internal/restore/startup.go`:

```go
package restore

import (
	"strconv"
	"strings"
)

// StartupOpts is the input to BuildStartupCommand. Fields not relevant to a
// given branch may be left zero.
type StartupOpts struct {
	// Self is the absolute path of the running tmux-state binary, used to
	// invoke the cat-scrollback subcommand. Single-quoted on output.
	Self string
	// DefaultShell is the resolved shell to exec when no allow-listed
	// command is being relaunched.
	DefaultShell string
	// IsBash adds "-l" to DefaultShell's exec line when true.
	IsBash bool
	// ScrollbackSHA, if non-empty, prepends a cat-scrollback step.
	ScrollbackSHA string
	// RelaunchCmd, if non-empty, becomes the exec target instead of DefaultShell.
	RelaunchCmd string
	// RelaunchArgs are appended to RelaunchCmd, each strconv.Quote'd.
	RelaunchArgs []string
}

// BuildStartupCommand composes the shell-command string passed to tmux as the
// trailing argument of new-session / new-window / split-window. Returns an
// empty string when no startup work is needed (caller omits the trailing arg
// so tmux uses its default-command).
//
// Output forms (matches spec §"Plan composition" table):
//
//	scrollback=no  relaunch=no   ""
//	scrollback=no  relaunch=yes  `<cmd> <quoted-args...>`
//	scrollback=yes relaunch=no   `'<self>' cat-scrollback <sha>; exec <shell> [-l]`
//	scrollback=yes relaunch=yes  `'<self>' cat-scrollback <sha>; exec <cmd> <quoted-args...>`
func BuildStartupCommand(opts StartupOpts) string {
	relaunch := buildExecTarget(opts)
	if opts.ScrollbackSHA == "" {
		// Without scrollback, an exec wrapper adds nothing; just emit the
		// raw command (tmux runs it via /bin/sh -c).
		if opts.RelaunchCmd == "" {
			return ""
		}
		return relaunch
	}
	return shellQuoteSingle(opts.Self) + " cat-scrollback " + opts.ScrollbackSHA + "; exec " + relaunch
}

// buildExecTarget returns the program-and-args portion that follows `exec`,
// or that stands alone when no scrollback is involved.
func buildExecTarget(opts StartupOpts) string {
	if opts.RelaunchCmd != "" {
		var b strings.Builder
		b.WriteString(opts.RelaunchCmd)
		for _, a := range opts.RelaunchArgs {
			b.WriteByte(' ')
			b.WriteString(strconv.Quote(a))
		}
		return b.String()
	}
	if opts.IsBash {
		return opts.DefaultShell + " -l"
	}
	return opts.DefaultShell
}

// shellQuoteSingle wraps s in single quotes, escaping any embedded single
// quote via the standard `'\''` close-quote / escaped-quote / re-open-quote
// trick. Safe for arbitrary filesystem paths.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/restore/ -run 'TestBuildStartupCommand' -v
```

Expected: ALL seven sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/restore/startup.go internal/restore/startup_test.go
git commit -m "feat(restore): add BuildStartupCommand helper

Pure function that composes the shell-command string passed to
tmux new-window / split-window. Covers the four scrollback x relaunch
cases from the spec, with single-quote escaping for the self-path and
strconv.Quote for relaunch args. Bash gets -l on its exec line."
```

---

### Task 5: Action model + BuildPlan rewrite

**Files:**
- Modify: `internal/restore/plan.go` (entire file)
- Modify: `internal/restore/plan_test.go` (entire file)

**Why now:** Tasks 1-4 give us the building blocks. This task locks in the new public API of the package.

- [ ] **Step 1: Update tests first (TDD against the new shape)**

Replace the entire contents of `internal/restore/plan_test.go` with:

```go
package restore_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/noamsto/tmux-state/internal/filter"
	"github.com/noamsto/tmux-state/internal/restore"
	"github.com/noamsto/tmux-state/internal/snapshot"
)

var defaultOpts = restore.BuildOptions{
	Self:         "/usr/bin/tmux-state",
	DefaultShell: "/bin/zsh",
	AllowList:    []string{"nvim"},
}

func TestBuildPlanForFreshServer(t *testing.T) {
	m := snapshot.Manifest{
		Sessions: []snapshot.Session{{
			Name: "s1",
			Windows: []snapshot.Window{{
				Index: 1, Name: "main", Layout: "L",
				Panes: []snapshot.Pane{
					{Index: 1, Cwd: "/a", Command: "nvim", ChildCount: 1},
					{Index: 2, Cwd: "/b", Command: "bash", ChildCount: 2},
				},
			}},
		}},
	}
	plan := restore.BuildPlan(m, filter.Filter{}, nil, defaultOpts)
	want := []restore.Action{
		restore.CreateSession{Name: "s1", Cwd: "/a"},
		restore.CreateWindow{Session: "s1", Index: 1, Name: "main", Cwd: "/a", StartupCommand: "nvim"},
		restore.SplitPane{Target: "s1:1", Cwd: "/b", StartupCommand: ""},
		restore.SetLayout{Window: "s1:1", Layout: "L"},
	}
	if diff := cmp.Diff(want, plan); diff != "" {
		t.Errorf("plan mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildPlanWithScrollbackProducesCatThenExec(t *testing.T) {
	m := snapshot.Manifest{
		Sessions: []snapshot.Session{{
			Name: "s1",
			Windows: []snapshot.Window{{
				Index: 1, Name: "main", Layout: "L",
				Panes: []snapshot.Pane{
					{Index: 1, Cwd: "/a", Command: "nvim", ChildCount: 1, ScrollbackSHA: "deadbeef"},
				},
			}},
		}},
	}
	plan := restore.BuildPlan(m, filter.Filter{}, nil, defaultOpts)
	wantStartup := `'/usr/bin/tmux-state' cat-scrollback deadbeef; exec nvim`
	for _, a := range plan {
		if cw, ok := a.(restore.CreateWindow); ok {
			if cw.StartupCommand != wantStartup {
				t.Errorf("CreateWindow.StartupCommand = %q, want %q", cw.StartupCommand, wantStartup)
			}
			return
		}
	}
	t.Fatal("CreateWindow not found in plan")
}

func TestBuildPlanScrollbackWithoutAllowedCommandUsesShell(t *testing.T) {
	m := snapshot.Manifest{
		Sessions: []snapshot.Session{{
			Name: "s1",
			Windows: []snapshot.Window{{
				Index: 1, Layout: "L",
				Panes: []snapshot.Pane{
					{Index: 1, Cwd: "/a", Command: "bash", ChildCount: 2, ScrollbackSHA: "abc"},
				},
			}},
		}},
	}
	plan := restore.BuildPlan(m, filter.Filter{}, nil, defaultOpts)
	wantStartup := `'/usr/bin/tmux-state' cat-scrollback abc; exec /bin/zsh`
	for _, a := range plan {
		if cw, ok := a.(restore.CreateWindow); ok {
			if cw.StartupCommand != wantStartup {
				t.Errorf("CreateWindow.StartupCommand = %q, want %q", cw.StartupCommand, wantStartup)
			}
			return
		}
	}
	t.Fatal("CreateWindow not found in plan")
}

func TestBuildPlanFiltersIdleShellPanes(t *testing.T) {
	m := snapshot.Manifest{
		Sessions: []snapshot.Session{{
			Name: "s1",
			Windows: []snapshot.Window{{
				Index: 1, Name: "main", Layout: "L",
				Panes: []snapshot.Pane{
					{Index: 1, Cwd: "/a", Command: "nvim", ChildCount: 1},
					{Index: 2, Cwd: "/b", Command: "bash", ChildCount: 0},
				},
			}},
		}},
	}
	f := filter.Filter{SkipIdleShells: true}
	plan := restore.BuildPlan(m, f, nil, restore.BuildOptions{})
	for _, a := range plan {
		if sp, ok := a.(restore.SplitPane); ok && sp.Cwd == "/b" {
			t.Error("idle-shell pane should be filtered out")
		}
	}
}

func TestBuildPlanFiltersDeduplicatedSessions(t *testing.T) {
	m := snapshot.Manifest{
		Sessions: []snapshot.Session{
			{Name: "s1", Windows: []snapshot.Window{{Index: 1, Panes: []snapshot.Pane{{Index: 1, Cwd: "/a", Command: "nvim"}}}}},
			{Name: "s2", Windows: []snapshot.Window{{Index: 1, Panes: []snapshot.Pane{{Index: 1, Cwd: "/c", Command: "nvim"}}}}},
		},
	}
	f := filter.Filter{DedupRunningServer: true}
	plan := restore.BuildPlan(m, f, map[string]bool{"s1": true}, restore.BuildOptions{})
	for _, a := range plan {
		if cs, ok := a.(restore.CreateSession); ok && cs.Name == "s1" {
			t.Error("running session should be deduped")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/restore/ -run 'TestBuildPlan' -v
```

Expected: COMPILE FAIL — `BuildOptions undefined`, `StartupCommand undefined`, `BuildPlan` signature mismatch, `RelaunchCommand` no longer present (in old test we're replacing — that one's gone).

- [ ] **Step 3: Rewrite `plan.go`**

Replace the entire contents of `internal/restore/plan.go` with:

```go
// Package restore plans and applies tmux-state restore operations.
package restore

import (
	"fmt"

	"github.com/noamsto/tmux-state/internal/filter"
	"github.com/noamsto/tmux-state/internal/snapshot"
)

// Action is one step of a restore plan. Concrete types are in this file.
// Apply() type-switches on the concrete type.
type Action interface {
	isAction()
}

// CreateSession creates a new tmux session. StartupCommand, when non-empty,
// is passed as the trailing shell-command argument to tmux new-session.
type CreateSession struct {
	Name           string
	Cwd            string
	StartupCommand string
}

func (CreateSession) isAction() {}

// CreateWindow creates a new tmux window inside a session. StartupCommand,
// when non-empty, is passed as the trailing shell-command argument to
// tmux new-window — the new window's first pane is born running it.
type CreateWindow struct {
	Session        string
	Index          int
	Name           string
	Cwd            string
	StartupCommand string
}

func (CreateWindow) isAction() {}

// SplitPane creates a new pane inside a window via split-window.
// StartupCommand, when non-empty, is passed as the trailing shell-command
// argument; the new pane is born running it.
type SplitPane struct {
	Target         string // <session>:<window_index>
	Cwd            string
	StartupCommand string
}

func (SplitPane) isAction() {}

// SetLayout applies a tmux layout string to a window.
type SetLayout struct {
	Window string
	Layout string
}

func (SetLayout) isAction() {}

// BuildOptions carries the values needed to compose StartupCommands. Resolved
// once per restore by the caller.
type BuildOptions struct {
	// Self is the absolute path of the running tmux-state binary
	// (os.Executable() in production). Used only when a pane has stored
	// scrollback; ignored otherwise.
	Self string
	// DefaultShell is the resolved fallback shell for panes without an
	// allow-listed command. See restore.DefaultShell.
	DefaultShell string
	// IsBash is the second return value of restore.DefaultShell; signals
	// that DefaultShell should be exec'd with -l.
	IsBash bool
	// AllowList is the set of commands eligible for relaunch as the pane's
	// initial process. Anything not in the list falls through to DefaultShell.
	AllowList []string
}

// BuildPlan builds an ordered slice of Actions to restore the manifest,
// honoring the filter and the allow-list of commands.
func BuildPlan(m snapshot.Manifest, f filter.Filter, runningSessions map[string]bool, opts BuildOptions) []Action {
	allowed := map[string]bool{}
	for _, c := range opts.AllowList {
		allowed[c] = true
	}

	startupFor := func(p snapshot.Pane) string {
		so := StartupOpts{
			Self:          opts.Self,
			DefaultShell:  opts.DefaultShell,
			IsBash:        opts.IsBash,
			ScrollbackSHA: p.ScrollbackSHA,
		}
		if allowed[p.Command] {
			so.RelaunchCmd = p.Command
			so.RelaunchArgs = p.CommandArgs
		}
		return BuildStartupCommand(so)
	}

	var plan []Action
	for _, sess := range m.Sessions {
		if f.SkipSession(sess, runningSessions) {
			continue
		}
		var sessionStarted bool
		for _, win := range sess.Windows {
			if f.SkipWindow(win) {
				continue
			}
			var firstPane *snapshot.Pane
			var keptPanes []snapshot.Pane
			for i := range win.Panes {
				p := win.Panes[i]
				if f.SkipPane(p) {
					continue
				}
				if firstPane == nil {
					firstPane = &p
				}
				keptPanes = append(keptPanes, p)
			}
			if firstPane == nil {
				continue
			}
			if !sessionStarted {
				plan = append(plan, CreateSession{Name: sess.Name, Cwd: firstPane.Cwd})
				sessionStarted = true
			}
			plan = append(plan, CreateWindow{
				Session:        sess.Name,
				Index:          win.Index,
				Name:           win.Name,
				Cwd:            firstPane.Cwd,
				StartupCommand: startupFor(*firstPane),
			})
			for _, p := range keptPanes[1:] {
				plan = append(plan, SplitPane{
					Target:         fmt.Sprintf("%s:%d", sess.Name, win.Index),
					Cwd:            p.Cwd,
					StartupCommand: startupFor(p),
				})
			}
			plan = append(plan, SetLayout{
				Window: fmt.Sprintf("%s:%d", sess.Name, win.Index),
				Layout: win.Layout,
			})
		}
	}
	return plan
}
```

- [ ] **Step 4: Run plan tests**

```bash
go test ./internal/restore/ -run 'TestBuildPlan' -v
```

Expected: ALL FIVE pass.

`apply.go` still references `RelaunchCommand`/`RestoreScrollback` — `go build ./...` will fail until Task 6. That's intentional; we fix it next.

- [ ] **Step 5: Commit**

```bash
git add internal/restore/plan.go internal/restore/plan_test.go
git commit -m "refactor(restore): fold relaunch + scrollback into StartupCommand

Action types CreateSession/CreateWindow/SplitPane gain a single
StartupCommand field; RelaunchCommand and RestoreScrollback are
deleted. BuildPlan now takes a BuildOptions struct (Self, DefaultShell,
IsBash, AllowList) and composes the startup string per pane via
BuildStartupCommand.

Apply.go still references the removed types and won't compile yet —
fixed in the next commit."
```

---

### Task 6: Apply rewrite

**Files:**
- Modify: `internal/restore/apply.go` (entire file)
- Modify: `internal/restore/apply_test.go` (entire file)

- [ ] **Step 1: Replace tests with the new shape**

Replace `internal/restore/apply_test.go` with:

```go
package restore_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/noamsto/tmux-state/internal/restore"
)

type recordingTmux struct {
	calls [][]string
}

func (r *recordingTmux) Run(_ context.Context, args []string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func TestApplyEmitsTmuxCallsWithoutStartup(t *testing.T) {
	rt := &recordingTmux{}
	plan := []restore.Action{
		restore.CreateSession{Name: "s1", Cwd: "/a"},
		restore.CreateWindow{Session: "s1", Index: 1, Name: "main", Cwd: "/a"},
		restore.SplitPane{Target: "s1:1", Cwd: "/b"},
		restore.SetLayout{Window: "s1:1", Layout: "L"},
	}
	if err := restore.Apply(context.Background(), rt, plan); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"new-session", "-d", "-s", "s1", "-c", "/a"},
		{"new-window", "-t", "s1:1", "-n", "main", "-c", "/a"},
		{"split-window", "-t", "s1:1", "-c", "/b"},
		{"select-layout", "-t", "s1:1", "L"},
	}
	if diff := cmp.Diff(want, rt.calls); diff != "" {
		t.Errorf("calls mismatch (-want +got):\n%s", diff)
	}
}

func TestApplyAppendsStartupCommandWhenPresent(t *testing.T) {
	rt := &recordingTmux{}
	startup := `'/usr/bin/tmux-state' cat-scrollback abc; exec /bin/zsh`
	plan := []restore.Action{
		restore.CreateWindow{Session: "s1", Index: 1, Name: "main", Cwd: "/a", StartupCommand: startup},
		restore.SplitPane{Target: "s1:1", Cwd: "/b", StartupCommand: "htop"},
	}
	if err := restore.Apply(context.Background(), rt, plan); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"new-window", "-t", "s1:1", "-n", "main", "-c", "/a", startup},
		{"split-window", "-t", "s1:1", "-c", "/b", "htop"},
	}
	if diff := cmp.Diff(want, rt.calls); diff != "" {
		t.Errorf("calls mismatch (-want +got):\n%s", diff)
	}
}

func TestApplyContinuesPastIndividualFailures(t *testing.T) {
	calls := 0
	failOn := 1
	rt := failingTmux{
		runFn: func(args []string) (string, error) {
			calls++
			if calls == failOn+1 {
				return "", context.Canceled
			}
			return "", nil
		},
	}
	plan := []restore.Action{
		restore.CreateSession{Name: "s1", Cwd: "/a"},
		restore.CreateWindow{Session: "s1", Index: 1, Cwd: "/a"},
		restore.SetLayout{Window: "s1:1", Layout: "L"},
	}
	if err := restore.Apply(context.Background(), rt, plan); err != nil {
		t.Fatalf("Apply should swallow per-action errors, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempted calls (best-effort), got %d", calls)
	}
}

type failingTmux struct {
	runFn func(args []string) (string, error)
}

func (f failingTmux) Run(_ context.Context, args []string) (string, error) {
	return f.runFn(args)
}
```

- [ ] **Step 2: Run tests to verify they fail / don't compile**

```bash
go test ./internal/restore/ -run 'TestApply' -v
```

Expected: COMPILE FAIL — `Apply` still has `RelaunchCommand`/`RestoreScrollback` cases referencing removed types.

- [ ] **Step 3: Rewrite `apply.go`**

Replace `internal/restore/apply.go` with:

```go
package restore

import (
	"context"
	"fmt"
)

// Runner is the subset of tmux.Client used by Apply (lets tests inject a fake).
type Runner interface {
	Run(ctx context.Context, args []string) (string, error)
}

// Apply executes the plan via the Runner. Best-effort: individual failures
// are swallowed so the rest of the plan still runs.
//
// CreateSession / CreateWindow / SplitPane each pass StartupCommand as the
// trailing shell-command argument when non-empty (tmux runs it via /bin/sh -c
// for the new pane). When empty, the trailing arg is omitted and tmux uses
// its default-command. Scrollback rendering is the responsibility of the
// startup command itself — see restore.BuildStartupCommand.
func Apply(ctx context.Context, t Runner, plan []Action) error {
	for _, a := range plan {
		var args []string
		switch v := a.(type) {
		case CreateSession:
			args = []string{"new-session", "-d", "-s", v.Name, "-c", v.Cwd}
			if v.StartupCommand != "" {
				args = append(args, v.StartupCommand)
			}
		case CreateWindow:
			args = []string{"new-window", "-t", fmt.Sprintf("%s:%d", v.Session, v.Index), "-n", v.Name, "-c", v.Cwd}
			if v.StartupCommand != "" {
				args = append(args, v.StartupCommand)
			}
		case SplitPane:
			args = []string{"split-window", "-t", v.Target, "-c", v.Cwd}
			if v.StartupCommand != "" {
				args = append(args, v.StartupCommand)
			}
		case SetLayout:
			args = []string{"select-layout", "-t", v.Window, v.Layout}
		default:
			return fmt.Errorf("unknown action: %T", a)
		}
		if _, err := t.Run(ctx, args); err != nil {
			continue
		}
	}
	return nil
}
```

(argv flags exactly match the pre-existing `apply.go`: `new-session -d`, `new-window` no `-d`, `split-window` no `-d`. The only behavioral change is the conditional trailing `<startup>` argument.)

- [ ] **Step 4: Run all restore tests**

```bash
go test ./internal/restore/ -v
go build ./...
```

Expected: all pass; build succeeds.

- [ ] **Step 5: Verify dead code is gone**

```bash
grep -nE 'RelaunchCommand|RestoreScrollback|ApplyWithScrollback|pasteScrollback|ScrollbackReader|randHex' internal/restore/
```

Expected: NO matches.

```bash
grep -nE 'os/exec|crypto/rand|encoding/hex' internal/restore/apply.go
```

Expected: NO matches (these imports were only needed for `pasteScrollback`).

- [ ] **Step 6: Commit**

```bash
git add internal/restore/apply.go internal/restore/apply_test.go
git commit -m "refactor(restore): drop paste-buffer apply path

Apply now emits StartupCommand as the trailing shell-command arg of
new-session/new-window/split-window; deleted ApplyWithScrollback,
pasteScrollback, ScrollbackReader interface, and the
load-buffer/paste-buffer/delete-buffer dance.

Restore plans no longer pipe scrollback into live shells; saved history
is rendered by the pane's birth process (cat-scrollback then exec)."
```

---

### Task 7: Update main.go call sites

**Files:**
- Modify: `cmd/tmux-state/main.go` (three subcommand bodies + helper)

- [ ] **Step 1: Add a small helper to resolve plan options**

In `cmd/tmux-state/main.go`, add this helper near `loadConfig()` (around line 401):

```go
// resolveBuildOptions builds the BuildOptions consumed by restore.BuildPlan.
// Caller is responsible for any error logging — failures here degrade to
// reasonable defaults (empty Self disables scrollback rendering, /bin/sh
// fallback for shell). All resolution happens once per restore invocation.
func resolveBuildOptions(ctx context.Context, t restore.Runner, allowList []string) restore.BuildOptions {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	shell, isBash := restore.DefaultShell(ctx, t, os.Getenv("SHELL"))
	return restore.BuildOptions{
		Self:         self,
		DefaultShell: shell,
		IsBash:       isBash,
		AllowList:    allowList,
	}
}
```

Add `"os"` to the imports if not already present (it already is — used by `os.Stdout` etc.).

- [ ] **Step 2: Update `newRestoreCmd`**

In `newRestoreCmd` (around lines 124-173), replace the body of the `RunE` closure's tail (from the `t := tmux.NewClient...` line to the end of the closure) with:

```go
				t := tmux.NewClient("tmux")
				running := map[string]bool{}
				rows, _ := t.ListSessions(ctx)
				for _, s := range rows {
					running[s.Name] = true
				}

				opts := resolveBuildOptions(ctx, t, cfg.CommandAllowList)
				plan := restore.BuildPlan(m, f, running, opts)
				return restore.Apply(ctx, t, plan)
```

Drop the `sb := scrollback.New(cfg.ScrollbackDir)` line — no longer needed in this command body.

- [ ] **Step 3: Update `newUndoCmd`**

In `newUndoCmd` (around lines 175-213), replace the tail (from `t := tmux.NewClient...` through the `restore.ApplyWithScrollback` line) with:

```go
				t := tmux.NewClient("tmux")
				opts := resolveBuildOptions(ctx, t, cfg.CommandAllowList)
				plan := restore.BuildPlan(m, filter.Filter{}, nil, opts)
				if err := restore.Apply(ctx, t, plan); err != nil {
					return err
				}
				_, err = db.DB().ExecContext(ctx, "DELETE FROM events WHERE id = ?", evs[0].ID)
				return err
```

Drop `sb := scrollback.New(cfg.ScrollbackDir)`.

- [ ] **Step 4: Update `newPickCmd`**

In `newPickCmd` (around lines 216-273), replace the tail (from `t := tmux.NewClient...` through `restore.ApplyWithScrollback`) with:

```go
				t := tmux.NewClient("tmux")
				opts := resolveBuildOptions(ctx, t, cfg.CommandAllowList)
				plan := restore.BuildPlan(m, filter.Filter{}, nil, opts)
				return restore.Apply(ctx, t, plan)
```

Drop `sb := scrollback.New(cfg.ScrollbackDir)`.

- [ ] **Step 5: Verify imports are clean**

```bash
goimports -w cmd/tmux-state/main.go
go build ./...
go vet ./...
```

`scrollback` is still used by `newSaveCmd` (the saver writes scrollbacks), so the import stays. `restore` and `os` remain. No new packages should be imported.

- [ ] **Step 6: Run all tests**

```bash
go test ./... -short
```

Expected: ALL pass. (Integration test is `-short`-skipped; covered separately.)

- [ ] **Step 7: End-to-end smoke test against your live tmux**

Build the binary and exercise the restore path against a real saved snapshot. **Save your current tmux state first** (auto-saves are running, but take a manual one):

```bash
go install ./cmd/tmux-state
tmux-state save --reason=pre-test-manual
```

Open a new throwaway tmux session in a different socket so we don't disturb your main one:

```bash
tmux -L test-restore new-session -d -s probe
tmux -L test-restore list-sessions
```

Now have tmux-state restore the latest snapshot — but on the test socket. (tmux-state doesn't currently flag a custom socket, so as a smoke test, look at one specific path: the cat-scrollback subcommand against an existing sha. This tests the new code path without restoring across sockets.)

```bash
sha=$(ls $HOME/.local/share/tmux-state/scrollbacks/*/ 2>/dev/null | grep -E '^[a-f0-9]{64}\.zst$' | head -1 | sed 's/\.zst$//')
test -n "$sha" || { echo "no saved scrollback to test against"; exit 0; }
tmux-state cat-scrollback "$sha" | wc -c
```

Expected: prints a positive byte count. The full restore-into-real-server test happens when you adopt the new binary in your daily setup — covered in the lazytmux flake-bump follow-up (out of scope).

Verify the restore command's argv shape by tracing it (no execution):

```bash
go run ./cmd/tmux-state restore --auto 2>&1 | head -20
```

Expected: this WILL try to restore. If you don't want that side-effect, skip; the unit tests above cover the argv composition.

- [ ] **Step 8: Commit**

```bash
git add cmd/tmux-state/main.go
git commit -m "refactor(cmd): wire BuildOptions through restore subcommands

restore, undo, and pick now resolve self-path + default-shell once and
pass them through restore.BuildOptions to BuildPlan. Apply path no
longer constructs a ScrollbackReader; scrollback rendering happens
inside each pane's birth command.

End-to-end behavior change: snapshots restored from this version
forward render via the cat-scrollback subcommand instead of paste-buffer
into live shells."
```

---

### Task 8: Final verification

**Files:** none modified — verification only.

- [ ] **Step 1: Full test suite**

```bash
go test ./...
go test -tags=integration ./...
```

Expected: ALL pass on both runs.

- [ ] **Step 2: Lint**

```bash
go vet ./...
test -x "$(command -v golangci-lint)" && golangci-lint run ./...
```

Expected: clean. If `golangci-lint` isn't installed, skip — `go vet` is the floor.

- [ ] **Step 3: Confirm no regressions in old API surface**

```bash
grep -rnE 'RelaunchCommand|RestoreScrollback|ApplyWithScrollback|ScrollbackReader' . --include="*.go"
```

Expected: NO matches anywhere in the repo.

- [ ] **Step 4: Inspect a built plan against a real snapshot** (manual sanity check)

Add a temporary one-off Go file `cmd/tmux-state-debug/main.go` (or use `go run` with a small inline script) that loads the latest snapshot manifest, runs `BuildPlan`, and prints each action. Confirm:

- All `CreateWindow` / `SplitPane` actions for panes that had scrollback show `StartupCommand` containing `cat-scrollback`.
- Allow-listed commands (e.g., `nvim`, `htop`) appear after `exec`.
- Idle plain shells produce `StartupCommand == ""` (when no scrollback was captured for them) or `cat-scrollback ...; exec /bin/zsh` (when scrollback was captured).

Delete the debug file before final commit (or `git checkout` it).

- [ ] **Step 5: Tag and ship a release** (out of scope here — handled by tmux-state's release process)

Once merged in the tmux-state repo, the lazytmux follow-up is a single `nix flake update tmux-state` + commit. That's outside this plan.

---

## Self-review

**Spec coverage:**
- §"Action model" → Task 5 (struct edits + deletions)
- §"Plan composition" table → Task 4 (BuildStartupCommand, all four rows tested) + Task 5 (BuildPlan wires it in)
- §"Apply" argv → Task 6 (rewrites + tests trailing-arg presence/absence)
- §"`cat-scrollback`" subcommand → Task 2 (impl + test, hidden flag)
- §"Default-shell discovery" → Task 3 (resolver + 5 tests covering all fallbacks)
- §"Edge cases" → covered across Tasks 2 (missing sha silent), 3 (empty-shell fallback), 4 (special chars in self-path test row), 6 (best-effort failure swallowing test)
- §"Tests" listed in spec → all matched 1-to-1 by tests in Tasks 1, 2, 3, 4, 5, 6
- §"Risks" → addressed where applicable in Task 7 (resolveBuildOptions degrades gracefully on `os.Executable()` error)

**Placeholder scan:** searched for "TBD", "TODO", "fill in", "similar to" — none.

**Type consistency:**
- `BuildOptions.Self` (Task 5) ↔ `StartupOpts.Self` (Task 4) — same name, both string ✓
- `BuildOptions.DefaultShell` ↔ `StartupOpts.DefaultShell` ✓
- `BuildOptions.IsBash` ↔ `StartupOpts.IsBash` ✓
- `restore.DefaultShell(ctx, t, shellEnv)` (Task 3) ↔ called as `restore.DefaultShell(ctx, t, os.Getenv("SHELL"))` (Task 7) ✓
- `runCatScrollback(ctx, store, sha, w io.Writer)` (Task 2) ↔ called as `runCatScrollback(cmd.Context(), store, args[0], os.Stdout)` ✓
- `restore.Runner` (Task 6) ↔ stub used in `TestDefaultShell*` (Task 3) ✓ (same interface — a single `Run(ctx, []string) (string, error)`)
