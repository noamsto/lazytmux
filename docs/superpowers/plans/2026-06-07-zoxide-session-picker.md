# Zoxide Suggestions in the Session Picker — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `prefix+s` shows existing sessions first, then top-15 zoxide directories; Enter on a zoxide row creates a session there and switches to it. The sesh/gum tmux bindings are removed from lazytmux.

**Architecture:** Extend the Go bubbletea picker (`picker/`). New pure functions (`zoxide.go`) collect/dedupe zoxide paths against existing sessions; `buildSessionItems` appends a "Suggestions" section; the Enter handler grows a create-and-switch branch; preview shows a dir listing for suggestion rows. Nix changes pin `pkgs.zoxide` into the wrapper PATH and drop the `prefix+K`/`prefix+S` sesh bindings.

**Tech Stack:** Go (stdlib only — no new module deps, vendorHash unchanged), bubbletea (existing), Nix.

**Spec:** `docs/superpowers/specs/2026-06-07-zoxide-session-picker-design.md`

**Key constraint:** `pkgs.sesh` stays in the home-manager module's `popupTools` default — nix-config installs sesh nowhere else, and the user's `sc` fish abbreviation (`sesh connect`) depends on it. Only the tmux bindings and the wrapper-closure sesh go away.

**Build/test commands** (run from repo root):
- Unit tests: `nix develop -c go test ./...` (run inside `picker/`: `cd picker && nix develop ..# -c go test ./...` — or just `nix develop` once, then `cd picker && go test ./...`)
- Full build (also runs `go test` via `buildGoModule` check phase): `nix build`
- All checks: `nix flake check`

---

### Task 1: Pure zoxide logic (TDD)

**Files:**
- Create: `picker/zoxide.go`
- Create: `picker/zoxide_test.go`

- [ ] **Step 1: Write the failing tests**

Create `picker/zoxide_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionNameFromPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/n/Data/git/lazytmux", "lazytmux"},
		{"/home/n/proj/foo.bar", "foo_bar"},          // tmux forbids '.' in names
		{"/home/n/proj/a:b", "a_b"},                  // tmux forbids ':' in names
		{"/home/n/proj/trailing/", "trailing"},       // trailing slash trimmed
		{"/", ""},                                    // root has no usable basename
	}
	for _, c := range cases {
		if got := sessionNameFromPath(c.in); got != c.want {
			t.Errorf("sessionNameFromPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != normalizePath(real) {
		t.Errorf("normalizePath(%q) = %q, want %q", link, got, normalizePath(real))
	}
	// Nonexistent path: falls back to Clean, no error
	if got := normalizePath("/no/such/dir/../dir2/"); got != "/no/such/dir2" {
		t.Errorf("normalizePath nonexistent = %q, want /no/such/dir2", got)
	}
}

func TestZoxideSuggestions(t *testing.T) {
	paths := []string{
		"/home/n/git/covered",   // dropped: session path match
		"/home/n/git/lazytmux",  // dropped: derived name collides with session "lazytmux"
		"/home/n/git/alpha",
		"/home/n/work/alpha",    // dropped: name collides with earlier suggestion "alpha"
		"/home/n/git/beta",
		"/home/n/git/gamma",
	}
	sessionPaths := map[string]bool{"/home/n/git/covered": true}
	sessionNames := map[string]bool{"lazytmux": true}

	got := zoxideSuggestions(paths, sessionPaths, sessionNames, 15)
	want := []suggestion{
		{path: "/home/n/git/alpha", name: "alpha"},
		{path: "/home/n/git/beta", name: "beta"},
		{path: "/home/n/git/gamma", name: "gamma"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestZoxideSuggestionsTopN(t *testing.T) {
	var paths []string
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		paths = append(paths, "/tmp/dirs/"+n)
	}
	got := zoxideSuggestions(paths, nil, nil, 3)
	if len(got) != 3 {
		t.Fatalf("limit not applied: got %d, want 3", len(got))
	}
	// Rank order preserved
	if got[0].name != "a" || got[2].name != "c" {
		t.Errorf("rank order broken: %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd picker && go test ./...` (inside `nix develop`)
Expected: FAIL — `undefined: sessionNameFromPath`, `undefined: normalizePath`, `undefined: zoxideSuggestions`, `undefined: suggestion`

- [ ] **Step 3: Implement `picker/zoxide.go`**

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxZoxideSuggestions caps the suggestions section below the session list.
const maxZoxideSuggestions = 15

// suggestion is a zoxide directory offered for session creation.
type suggestion struct {
	path string // normalized absolute dir path
	name string // derived tmux session name
}

// sessionNameFromPath derives a tmux-safe session name from a directory path:
// basename with '.' and ':' replaced (tmux forbids them in session names).
func sessionNameFromPath(p string) string {
	base := filepath.Base(strings.TrimRight(p, "/"))
	if base == "/" || base == "." {
		return ""
	}
	return strings.NewReplacer(".", "_", ":", "_").Replace(base)
}

// normalizePath cleans and symlink-resolves a path for dedupe comparisons.
// Nonexistent paths keep the cleaned form.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// zoxideSuggestions filters rank-ordered, normalized zoxide paths against
// existing sessions (by path and by derived name) and cuts to limit.
// Duplicate derived names among suggestions keep only the higher-ranked dir.
func zoxideSuggestions(paths []string, sessionPaths, sessionNames map[string]bool, limit int) []suggestion {
	out := make([]suggestion, 0, limit)
	seen := make(map[string]bool)
	for _, p := range paths {
		if sessionPaths[p] {
			continue
		}
		name := sessionNameFromPath(p)
		if name == "" || sessionNames[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, suggestion{path: p, name: name})
		if len(out) == limit {
			break
		}
	}
	return out
}

// collectZoxide returns ranked zoxide dirs not already covered by a session.
// Missing zoxide binary, errors, or dead dirs degrade to no suggestions.
func collectZoxide(sessions []sessionData) []suggestion {
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	sessionPaths := make(map[string]bool, len(sessions))
	sessionNames := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		sessionPaths[normalizePath(s.path)] = true
		sessionNames[s.name] = true
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		p := normalizePath(l)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		paths = append(paths, p)
	}
	return zoxideSuggestions(paths, sessionPaths, sessionNames, maxZoxideSuggestions)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./...`
Expected: PASS (collectZoxide is exercised later via the TUI; it's a thin exec wrapper around the tested pure functions)

- [ ] **Step 5: Commit**

```bash
git add picker/zoxide.go picker/zoxide_test.go
git commit -m "feat(picker): pure zoxide suggestion logic with tests"
```

---

### Task 2: Regression anchors for existing pure helpers

**Files:**
- Create: `picker/tui_test.go`

- [ ] **Step 1: Write the tests** (these pass immediately — they anchor existing behavior before we touch tui.go)

```go
package main

import "testing"

func TestFuzzyScore(t *testing.T) {
	if got := fuzzyScore("lazytmux", ""); got != 0 {
		t.Errorf("empty pattern = %d, want 0", got)
	}
	if got := fuzzyScore("lazytmux", "ltx"); got < 0 {
		t.Errorf("subsequence ltx should match, got %d", got)
	}
	if got := fuzzyScore("lazytmux", "xyz"); got != -1 {
		t.Errorf("non-subsequence = %d, want -1", got)
	}
	// Consecutive prefix beats a scattered match
	if fuzzyScore("lazytmux", "lazy") <= fuzzyScore("lazytmux", "lzyu") {
		t.Error("consecutive prefix should outscore scattered match")
	}
}

func TestVisibleWidth(t *testing.T) {
	if got := visibleWidth("abc"); got != 3 {
		t.Errorf("plain = %d, want 3", got)
	}
	if got := visibleWidth("\033[31mabc\033[0m"); got != 3 {
		t.Errorf("ANSI-wrapped = %d, want 3", got)
	}
}

func TestPadToWidth(t *testing.T) {
	if got := padToWidth("ab", 2, 5); got != "ab   " {
		t.Errorf("padToWidth = %q, want %q", got, "ab   ")
	}
	if got := padToWidth("abcdef", 6, 5); got != "abcdef" {
		t.Errorf("wider than target should be unchanged, got %q", got)
	}
}
```

Note: if `TestPadToWidth`'s second assertion fails, read `padToWidth` (main.go:1143) and anchor whatever it actually does — these are regression anchors, not spec changes.

- [ ] **Step 2: Run tests**

Run: `cd picker && go test ./...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add picker/tui_test.go
git commit -m "test(picker): regression anchors for fuzzyScore and width helpers"
```

---

### Task 3: Suggestions section in the session list

**Files:**
- Modify: `picker/tui.go:19-28` (listItem struct), `picker/tui.go:692-829` (buildSessionItems)

- [ ] **Step 1: Add fields to `listItem`** (tui.go:19)

```go
type listItem struct {
	target          string // tmux target; "" = unselectable
	display         string // ANSI-rendered display line
	plain           string // display stripped of ANSI (cached for width)
	searchText      string // filterable text (name, branch — no paths/icons)
	isHeader        bool   // session header row
	session         string // owning session name (for kill)
	hasActiveClaude bool   // used for --claude filter
	isScratch       bool   // scratch-* session
	createPath      string // zoxide suggestion: dir to create a session at ("" = normal row)
	createName      string // zoxide suggestion: derived session name
}
```

- [ ] **Step 2: Collect zoxide in parallel and append the section**

In `buildSessionItems` (tui.go:692), right after the `resCh` goroutine (tui.go:698-699), add:

```go
	zoxCh := make(chan []suggestion, 1)
	go func() { zoxCh <- collectZoxide(sessions) }()
```

At the end of the function, replace the final `return items` (tui.go:828) with:

```go
	if sugs := <-zoxCh; len(sugs) > 0 {
		items = append(items, listItem{
			display:  cDim + " Suggestions" + reset,
			plain:    " Suggestions",
			isHeader: true,
		})
		for _, sg := range sugs {
			shortPath := sg.path
			if home != "" && strings.HasPrefix(shortPath, home) {
				shortPath = "~" + shortPath[len(home):]
			}
			display := fmt.Sprintf("%s %s  %s",
				cBlue+iDir+reset,
				sg.name,
				cDim+shortPath+reset,
			)
			plain := fmt.Sprintf("%s %s  %s", iDir, sg.name, shortPath)
			items = append(items, listItem{
				target:     sg.path,
				createPath: sg.path,
				createName: sg.name,
				display:    display,
				plain:      plain,
				searchText: sg.name + " " + shortPath,
			})
		}
	}
	return items
```

(`target` is set to the path so the row is selectable and the preview cache keys correctly; the `createPath != ""` discriminator drives behavior.)

- [ ] **Step 3: Keep sessions before suggestions when filtering**

In `withFilter` (tui.go:600), change the sort comparator:

```go
	// Sort by score descending; sessions always rank above zoxide suggestions.
	// Stable preserves original order for ties.
	sort.SliceStable(matches, func(i, j int) bool {
		ci, cj := matches[i].item.createPath != "", matches[j].item.createPath != ""
		if ci != cj {
			return !ci
		}
		return matches[i].score > matches[j].score
	})
```

(Window items never set `createPath`, so window mode is unaffected.)

- [ ] **Step 4: Build and unit-test**

Run: `cd picker && go build ./... && go test ./...`
Expected: compiles, all tests PASS

- [ ] **Step 5: Commit**

```bash
git add picker/tui.go
git commit -m "feat(picker): zoxide suggestions section below the session list"
```

---

### Task 4: Create-and-switch, kill guard, dir preview

**Files:**
- Modify: `picker/tui.go:85-93` (previewMsg), `tui.go:163-178` (previewMsg handler), `tui.go:211-225` (enter/ctrl+x), `tui.go:536-541` (currentTarget area), `tui.go:672-688` (loadPreviewCmd)
- Modify: `picker/zoxide.go` (add createAndSwitch + listDir)

- [ ] **Step 1: Add `currentItem` helper** next to `currentTarget` (tui.go:536):

```go
func (m tuiModel) currentItem() (listItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return listItem{}, false
	}
	return m.visible[m.cursor], true
}
```

- [ ] **Step 2: Add the action helpers to `picker/zoxide.go`**

```go
// createAndSwitch creates a detached session at path (unless name already
// exists) and switches the attached client to it. zoxide add keeps the dir's
// rank fresh: the new session's shell never cd's, so zoxide never sees it.
func createAndSwitch(name, path string) {
	if exec.Command("tmux", "has-session", "-t", "="+name).Run() != nil {
		exec.Command("tmux", "new-session", "-d", "-s", name, "-c", path).Run() //nolint:errcheck
	}
	exec.Command("tmux", "switch-client", "-t", "="+name).Run() //nolint:errcheck
	exec.Command("zoxide", "add", path).Run() //nolint:errcheck
}

// listDir renders a directory listing for the preview pane, preferring eza.
func listDir(path string) string {
	if eza, err := exec.LookPath("eza"); err == nil {
		if out, err := exec.Command(eza, "-la", "--color=always", "--group-directories-first", path).Output(); err == nil {
			return strings.TrimRight(string(out), "\n ")
		}
	}
	out, err := exec.Command("ls", "-la", path).Output()
	if err != nil {
		return "(no preview available)"
	}
	return strings.TrimRight(string(out), "\n ")
}
```

- [ ] **Step 3: Branch the enter handler** (tui.go:211):

```go
	case "enter":
		if item, ok := m.currentItem(); ok && item.target != "" {
			if item.createPath != "" {
				createAndSwitch(item.createName, item.createPath)
			} else {
				exec.Command("tmux", "switch-client", "-t", item.target).Run() //nolint:errcheck
			}
			return m, tea.Quit
		}
```

- [ ] **Step 4: Guard the kill key** (tui.go:217) — a suggestion row's target is a path, not a session; killing must no-op:

```go
	case "ctrl+x":
		if item, ok := m.currentItem(); ok && item.target != "" && item.createPath == "" {
			if strings.Contains(item.target, ":") {
				exec.Command("tmux", "kill-window", "-t", item.target).Run() //nolint:errcheck
			} else {
				exec.Command("tmux", "kill-session", "-t", item.target).Run() //nolint:errcheck
			}
			return m, m.refreshDataCmd()
		}
```

- [ ] **Step 5: Dir-listing preview.** Add `scrollTop bool` to `previewMsg` (tui.go:90):

```go
type previewMsg struct {
	content   string
	target    string
	scrollTop bool
}
```

In the `previewMsg` handler (tui.go:174), replace the `if !sameTarget { ... }` block:

```go
			if !sameTarget {
				if msg.scrollTop {
					m.preview.SetYOffset(0)
				} else {
					m.preview.SetYOffset(m.preview.TotalLineCount())
				}
			}
```

Replace `loadPreviewCmd` (tui.go:672):

```go
func (m tuiModel) loadPreviewCmd() tea.Cmd {
	item, ok := m.currentItem()
	if !ok || item.target == "" || !m.showPreview {
		return nil
	}
	t := item.target
	if cp := item.createPath; cp != "" {
		return func() tea.Msg {
			return previewMsg{content: listDir(cp), target: t, scrollTop: true}
		}
	}
	return func() tea.Msg {
		out, err := exec.Command("tmux", "capture-pane", "-t", t, "-p", "-e").Output()
		if err != nil {
			return previewMsg{content: "(no preview available)", target: t}
		}
		content := strings.TrimRight(string(out), "\n ")
		// Reset background at end of each line to prevent ANSI color bleeding
		// into empty viewport padding cells (e.g. opencode's black input area).
		content = strings.ReplaceAll(content, "\n", "\033[49m\n") + "\033[49m"
		return previewMsg{content: content, target: t}
	}
}
```

- [ ] **Step 6: Build and unit-test**

Run: `cd picker && go build ./... && go test ./...`
Expected: compiles, all tests PASS

- [ ] **Step 7: Commit**

```bash
git add picker/tui.go picker/zoxide.go
git commit -m "feat(picker): create-and-switch on zoxide rows, dir preview, kill guard"
```

---

### Task 5: Nix wiring — zoxide in, sesh bindings out

**Files:**
- Modify: `config/tmux.conf.nix:190-191` (dead `@fzf@` substitution), `:411-413` (sesh bindings), `:567` (wrapper PATH)
- Modify: `modules/home-manager.nix:309-324` (popupTools description)

- [ ] **Step 1: Remove the sesh bindings.** Delete tmux.conf.nix lines 411-413 entirely (the `# Sesh pickers` comment, the `bind-key "K" ... sesh ... gum ...` line, and the `bind-key "S" ... sesh ... fzf-tmux ...` line).

- [ ] **Step 2: Update the wrapper PATH** (tmux.conf.nix:567). Remove `pkgs.sesh`, add `pkgs.zoxide`:

```nix
        --prefix PATH : ${lib.makeBinPath ([pkgs.tmux] ++ scripts ++ [pkgs.lazygit pkgs.yazi pkgs.btop pkgs.zoxide pkgs.jq pkgs.util-linux pkgs.coreutils pkgs.xdg-utils])}
```

- [ ] **Step 3: Drop the dead `@fzf@` substitution pair.** No script references `@fzf@` anymore. In the two parallel lists at tmux.conf.nix:190-191, remove `"@fzf@"` from the first and `"${pkgs.fzf}/bin/fzf"` from the second (keep list order aligned). Do NOT touch `tmuxPlugins.tmux-fzf` (line 296) — that's an unrelated plugin.

- [ ] **Step 4: Update `popupTools`** (home-manager.nix:309-324). Keep `pkgs.sesh` in the default — it is the only thing installing sesh into the user's profile, and the `sc` fish abbreviation (`sesh connect`) depends on it. Fix the stale description:

```nix
    popupTools = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [pkgs.sesh pkgs.lazygit pkgs.yazi pkgs.btop];
      defaultText = lib.literalExpression "[pkgs.sesh pkgs.lazygit pkgs.yazi pkgs.btop]";
      description = ''
        Tools installed via home.packages so popup keybindings
        (prefix+g → lazygit, prefix+b → btop, prefix+y → yazi) resolve in
        shells that don't inherit the tmux wrapper's PATH prepends — e.g.
        fish login shells opened by display-popup, or direnv-loaded
        devshells. sesh has no binding anymore but stays for external
        `sesh connect` CLI workflows.

        Set to [] to opt out entirely, or drop individual entries if you
        install those tools elsewhere (home-manager errors if two
        different derivations install the same file).
      '';
    };
```

- [ ] **Step 5: Build**

Run: `nix build` (runs `go test ./...` in the picker check phase too)
Expected: success; `./result/bin/tmux` exists

Run: `nix flake check`
Expected: all checks pass

- [ ] **Step 6: Commit**

```bash
git add config/tmux.conf.nix modules/home-manager.nix
git commit -m "feat(picker): pin zoxide in wrapper PATH, drop sesh/gum bindings"
```

---

### Task 6: Docs + manual verification

**Files:**
- Modify: `CLAUDE.md` (Script Roles table rows for `tmux-session-picker` / `tmux-window-picker`; "Session Targeting Gotcha" section)

- [ ] **Step 1: Update CLAUDE.md.** The Script Roles table claims the pickers use `choose-tree` — stale; they launch the Go bubbletea TUI. Replace the two rows:

```markdown
| `tmux-session-picker` | `prefix + s` | Launches the Go bubbletea picker (`tmux-picker-generate --tui`) in a popup: sessions sorted by activity, then top-15 zoxide dir suggestions (Enter on a suggestion creates a session there and switches). |
| `tmux-window-picker` | `prefix + w` | Same TUI in window mode (`--tui --windows`), grouped by session. |
```

In "Session Targeting Gotcha", update the sentence "The pickers use `#{session_id}` (`$N` format) instead to avoid this." to reflect reality — the Go TUI targets sessions by name via `tmux switch-client -t` (and `=name` exact-match for created sessions). Check the section still makes sense; trim what no longer applies.

- [ ] **Step 2: Reload and verify manually.** Reload tmux (`prefix + r` after pointing at the new build, or `home-manager switch` if that's the active wiring), then check:

1. `prefix+s` opens the picker; sessions listed first, ` Suggestions` header below, max 15 dir rows.
2. No suggestion duplicates a dir that already has a session (path or name).
3. Highlighting a suggestion shows a dir listing in the preview (top-scrolled).
4. Enter on a suggestion creates the session at that dir (name = basename, dots → underscores) and switches to it; `zoxide query -l` rank bumps.
5. Enter on a suggestion whose session was created meanwhile just switches (no error).
6. `ctrl+x` on a suggestion row does nothing.
7. Typing filters both groups; sessions stay above suggestions; header disappears when no suggestion matches.
8. `prefix+K` and `prefix+S` are unbound (`tmux list-keys | grep -E '"(K|S)"'` shows nothing sesh-related).
9. `sc` still works in a fish shell (sesh still installed via popupTools).
10. Degradation: `PATH` without zoxide → picker shows sessions only, no errors.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: picker suggestions + drop stale choose-tree claims"
```

- [ ] **Step 4: Close out.** Comment on issue #6 with what shipped (approach changed from sesh/gum to extending the Go picker, per spec) and close it.
