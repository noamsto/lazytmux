# Bridge Window Names via `@window_bridge_name` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make mirrored remote-bridge windows show the remote window's name instead of the pane cwd basename (`lazytmux`).

**Architecture:** The daemon captures each remote window's name and writes it to a dedicated, daemon-owned window option `@window_bridge_name` (on seed, window-add, and `%window-renamed`). reflow's existing `@bridge_win` branch reads that option instead of the volatile `#{window_name}`, breaking the `window_name → @window_label_short → automatic-rename → window_name` clobber loop. `automatic-rename` stays on.

**Tech Stack:** Go (`picker/remotebridge/daemon`, module root `picker/`), bash (`scripts/tmux-reflow-windows.sh`), bats + `go test`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-22-bridge-window-name-design.md`.
- Go module root is `picker/` — run Go tests as `cd picker && go test ./remotebridge/daemon/ -run <Name> -v`.
- bats run in the devshell: `nix develop -c bats tests/<file>.bats` (the integration bats go-builds the daemon/renderer itself).
- `@window_bridge_name` is written ONLY by the daemon and read ONLY by reflow (mirrors the `@window_ai_name`/`@window_task` ownership rule).
- Remote-derived names are untrusted: sanitize before writing (strip `|`, newlines, control chars).
- Commit from inside the devshell so pre-commit hooks run: `nix develop -c git commit -m "…"`.
- Every commit must leave `nix flake check` green — task ordering below guarantees this.

---

### Task 1: Daemon captures the remote window name

**Files:**
- Modify: `picker/remotebridge/daemon/windows.go` (`remoteWindow` struct ~line 60; `parseWindowList` ~line 66)
- Modify: `picker/remotebridge/daemon/daemon.go:121` (seed list-windows format), `:363` (window-add list-windows format)
- Test: `picker/remotebridge/daemon/windows_test.go` (`TestParseWindowList` ~line 39)

**Interfaces:**
- Produces: `remoteWindow` gains `name string`; `parseWindowList` fills it from a 3-field row (`index id name`), `name == ""` when absent.

- [ ] **Step 1: Update the failing test**

In `windows_test.go`, replace `TestParseWindowList` with:

```go
func TestParseWindowList(t *testing.T) {
	// index and id are distinct namespaces: window at index 3 has id @5.
	// A name may contain spaces; a `|` is preserved here (sanitized at write time).
	got := parseWindowList("1 @1 shell\n2 @2 my window\n3 @5 a|b\n4 @7\n")
	want := []remoteWindow{
		{"1", "@1", "shell"},
		{"2", "@2", "my window"},
		{"3", "@5", "a|b"},
		{"4", "@7", ""}, // no name field -> empty
	}
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestParseWindowList -v`
Expected: FAIL — `remoteWindow` has only 2 fields, so the 3-field composite literals don't compile / don't match.

- [ ] **Step 3: Add the `name` field and 3-field parse**

In `windows.go`, add `name` to the struct:

```go
type remoteWindow struct {
	index string
	id    string
	name  string
}
```

Replace the `parseWindowList` body's row loop with a two-`Cut` parse:

```go
func parseWindowList(body string) []remoteWindow {
	var wins []remoteWindow
	for _, row := range strings.Split(body, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		idx, rest, ok := strings.Cut(row, " ")
		if !ok {
			continue
		}
		id, name, _ := strings.Cut(rest, " ") // name is optional; "" when absent
		wins = append(wins, remoteWindow{index: idx, id: id, name: name})
	}
	return wins
}
```

- [ ] **Step 4: Fetch the name in both list-windows calls**

In `daemon.go`, change the format string at BOTH `:121` (seed) and `:363` (window-add) from:

```go
send(fmt.Sprintf("list-windows -t %s -F '#{window_index} #{window_id}'", tmuxQuote(cfg.RemoteSession)))
```
to:
```go
send(fmt.Sprintf("list-windows -t %s -F '#{window_index} #{window_id} #{window_name}'", tmuxQuote(cfg.RemoteSession)))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd picker && go test ./remotebridge/daemon/ -v`
Expected: PASS (all daemon unit tests, including `TestParseWindowList`). No consumer uses `name` yet — behavior unchanged.

- [ ] **Step 6: Commit**

```bash
nix develop -c git commit -am "feat(remotebridge): capture remote window name in parseWindowList (#196)"
```

---

### Task 2: `sanitizeWindowName` helper

**Files:**
- Modify: `picker/remotebridge/daemon/windows.go` (add function)
- Test: `picker/remotebridge/daemon/windows_test.go` (add test)

**Interfaces:**
- Produces: `sanitizeWindowName(s string) string` — drops `|`, `\r`, `\n`, and control chars (`< 0x20`, `0x7f`); keeps everything else including spaces.

- [ ] **Step 1: Write the failing test**

Add to `windows_test.go`:

```go
func TestSanitizeWindowName(t *testing.T) {
	cases := map[string]string{
		"shell":      "shell",
		"my window":  "my window", // spaces preserved
		"a|b":        "ab",        // FMT delimiter stripped
		"a\nb\r":     "ab",        // newlines stripped
		"tab\tend":   "tabend",    // control char stripped
	}
	for in, want := range cases {
		if got := sanitizeWindowName(in); got != want {
			t.Errorf("sanitizeWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestSanitizeWindowName -v`
Expected: FAIL — `undefined: sanitizeWindowName`.

- [ ] **Step 3: Implement the helper**

Add to `windows.go` (uses the already-imported `strings`):

```go
// sanitizeWindowName strips characters that would break the reflow FMT
// delimiter ('|') or a tmux command line (newlines/control chars) from a
// remote-derived window name before it is written to @window_bridge_name.
func sanitizeWindowName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '|' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestSanitizeWindowName -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
nix develop -c git commit -am "feat(remotebridge): sanitizeWindowName helper for @window_bridge_name (#196)"
```

---

### Task 3: `%window-renamed` writes `@window_bridge_name`

**Files:**
- Modify: `picker/remotebridge/daemon/translate.go` (`WindowRenamed` case ~line 13)
- Test: `picker/remotebridge/daemon/translate_test.go` (~line 22)
- Modify: `tests/remote-m2-integration.bats:249-257` (rename assertion)

**Interfaces:**
- Consumes: `sanitizeWindowName` (Task 2), `remoteWindow.name` (Task 1).
- Produces: `WindowRenamed` now emits `["set-option","-w","-t",<localWin>,"@window_bridge_name",<sanitized name>]` instead of `rename-window`.

- [ ] **Step 1: Update the failing unit test**

In `translate_test.go`, change the `WindowRenamed` expectation (~line 22) and add a sanitize case. The line's remote `Data` is the new name; find the existing case producing `{"rename-window", "-t", "h-s:2", "my name"}` and replace its `want` with:

```go
[]string{"set-option", "-w", "-t", "h-s:2", "@window_bridge_name", "my name"}, true},
```

Add a new case in the same test table for a `|` in the name (registry must map its id to `h-s:2`):

```go
// a '|' in the remote name is stripped before it reaches the reflow FMT.
{controlmode.Line{Kind: controlmode.WindowRenamed, Args: []string{"@9"}, Data: []byte("a|b")},
	[]string{"set-option", "-w", "-t", "h-s:2", "@window_bridge_name", "ab"}, true},
```

(If the test's registry doesn't already map `@9`→`h-s:2`, reuse the id the existing rename case uses instead of `@9`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestTranslate -v`
Expected: FAIL — still emits `rename-window`.

- [ ] **Step 3: Change the WindowRenamed case**

In `translate.go`, replace the `return` in the `WindowRenamed` case:

```go
	case controlmode.WindowRenamed:
		if len(l.Args) == 0 {
			return nil, false
		}
		w, ok := reg.byRemoteID(l.Args[0])
		if !ok {
			return nil, false
		}
		return []string{"set-option", "-w", "-t", w.localWin, "@window_bridge_name", sanitizeWindowName(string(l.Data))}, true
```

- [ ] **Step 4: Run unit test to verify it passes**

Run: `cd picker && go test ./remotebridge/daemon/ -run TestTranslate -v`
Expected: PASS.

- [ ] **Step 5: Update the integration rename assertion (same commit — behavior changed)**

In `tests/remote-m2-integration.bats`, replace lines 249-257 with:

```bash
	# Rename it remotely -> local @window_bridge_name follows (window_name is
	# derived by reflow, which this vanilla tmux -L server does not run).
	newwin="$($SRC list-windows -t rem -F '#{window_id}' | tail -1)"
	$SRC rename-window -t "$newwin" bridged-name
	for _ in $(seq 1 40); do
		names="$($DST list-windows -t host-sess -F '#{@window_bridge_name}' 2>/dev/null)"
		[[ $names == *bridged-name* ]] && break
		sleep 0.1
	done
	[[ $names == *bridged-name* ]]
```

- [ ] **Step 6: Run the integration test to verify it passes**

Run: `nix develop -c bats tests/remote-m2-integration.bats`
Expected: PASS (all tests, including "daemon reflects remote new-window / rename-window / kill-window").

- [ ] **Step 7: Commit**

```bash
nix develop -c git commit -am "feat(remotebridge): %window-renamed writes @window_bridge_name (#196)"
```

---

### Task 4: Daemon seeds `@window_bridge_name` on window creation

**Files:**
- Modify: `picker/remotebridge/daemon/daemon.go` (seed loop ~186-204; `addWindow` ~380-387)
- Test: `tests/remote-m2-integration.bats` (add seed assertion in the 3-window test ~line 182)

**Interfaces:**
- Consumes: `remoteWindow.name` (Task 1), `sanitizeWindowName` (Task 2).
- Produces: every mirror window has `@window_bridge_name` set from its remote name at creation, plus an instant-floor `rename-window`.

- [ ] **Step 1: Add the failing seed assertion**

In `tests/remote-m2-integration.bats`, in the test `"daemon mirrors a 3-window remote session into 3 local windows"` (~line 182), the SRC session's windows already have names. After the existing window-count assertion (`dst_wins` == 3, ~line 204), append:

```bash
	# Each mirror window carries its remote name in @window_bridge_name.
	src_names="$($SRC list-windows -t rem -F '#{window_name}' | sort | tr '\n' ',')"
	dst_names="$($DST list-windows -t host-sess -F '#{@window_bridge_name}' | sort | tr '\n' ',')"
	[ "$dst_names" = "$src_names" ]
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop -c bats tests/remote-m2-integration.bats -f "3 local windows"`
Expected: FAIL — `@window_bridge_name` is empty on the mirror windows.

- [ ] **Step 3: Write the seed option + instant-floor rename**

In `daemon.go` seed loop, after the `pane-base-index` set-option (~line 198) and before/after `mw := reg.add(rw.id, localWin)` (`rw` and `localWin` are in scope), add:

```go
		if name := sanitizeWindowName(rw.name); name != "" {
			cfg.LocalTmux("set-option", "-w", "-t", localWin, "@window_bridge_name", name)
			cfg.LocalTmux("rename-window", "-t", localWin, name) // instant floor; reflow self-heals window_name
		}
```

- [ ] **Step 4: Write the same for window-add**

In `addWindow`, capture the name in the lookup loop (~line 370):

```go
	inSession := false
	var addedName string
	for _, rw := range parseWindowList(string(lw.Data)) {
		if rw.id == remoteID {
			inSession = true
			addedName = rw.name
			break
		}
	}
```

Then after the `pane-base-index` set-option (~line 386):

```go
	if name := sanitizeWindowName(addedName); name != "" {
		cfg.LocalTmux("set-option", "-w", "-t", localWin, "@window_bridge_name", name)
		cfg.LocalTmux("rename-window", "-t", localWin, name) // instant floor; reflow self-heals
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop -c bats tests/remote-m2-integration.bats`
Expected: PASS (all, including the new seed assertion).
Run: `cd picker && go test ./remotebridge/daemon/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
nix develop -c git commit -am "feat(remotebridge): seed @window_bridge_name on window create/add (#196)"
```

---

### Task 5: reflow reads `@window_bridge_name`

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh:105` (FMT), `:111` (read vars), `:123-137` (bridge branch)
- Test: `tests/reflow-fanout.bats` (add test)

**Interfaces:**
- Consumes: `@window_bridge_name` written by the daemon (Tasks 3-4).
- Produces: for a `@bridge_win` window, `@window_label_short` = `@window_bridge_name` (falling back to `window_name` when the option is empty).

- [ ] **Step 1: Write the failing test**

Add to `tests/reflow-fanout.bats` (harness already builds `$REFLOW`, a config-less tmux server `S`, base-index 0):

```bash
@test "a bridge window labels from @window_bridge_name, not the clobbered window_name" {
	# Simulate the real-config clobber: window_name is the wrong cwd-derived
	# name, but the daemon-owned @window_bridge_name holds the remote name.
	tmux set -wq -t S:0 @bridge_win 1
	tmux set-window-option -t S:0 automatic-rename off
	tmux rename-window -t S:0 lazytmux           # the wrong name
	tmux set -wq -t S:0 @window_bridge_name shell # the remote name

	bash "$REFLOW" S 200 --force >/dev/null 2>&1

	[ "$(tmux show -wv -t S:0 @window_label_short)" = "shell" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `nix develop -c bats tests/reflow-fanout.bats -f "bridge window labels"`
Expected: FAIL — `@window_label_short` is `lazytmux` (bridge branch still reads `#{window_name}`).

- [ ] **Step 3: Add `@window_bridge_name` to FMT and read it**

In `scripts/tmux-reflow-windows.sh`, append `#{@window_bridge_name}` to the END of `FMT` (line 105):

```bash
FMT='#{window_index}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@issue_branch}|#{@crew_name}|#{@window_ai_name}|#{@bridge_win}|#{window_name}|#{@window_task}|#{@window_bridge_name}'
```

Add the read var to the `while IFS='|' read -r …` list (line 111), appending `bname` at the end:

```bash
while IFS='|' read -r idx branch pane_path zoomed iprov iid ititle prnum prstate prcheck prmerge ibranch crew wai bridge wname wtask bname; do
```

- [ ] **Step 4: Use the option in the bridge branch**

In the `if [[ $bridge == 1 ]]; then` block (lines 123-137), introduce a resolved name and use it for every label field. Replace the block's body so the label source is `bname` with a `wname` fallback:

```bash
	if [[ $bridge == 1 ]]; then
		# Remote-bridge mirror window (#167 @bridge_win opt-out): label it with the
		# daemon-owned remote name (@window_bridge_name), NOT #{window_name} — the
		# latter is clobbered by automatic-rename on the real config (#196). Fall
		# back to window_name before the daemon's first write.
		local bwname="${bname:-$wname}"
		win_short[$idx]="$bwname"
		win_id[$idx]=""
		win_rest_short[$idx]="$bwname"
		win_pr[$idx]=""
		measure_display_width "$bwname"
		win_short_dw[$idx]=$REPLY_DW
		win_id_dw[$idx]=0
		win_pr_dw[$idx]=0
		win_crew[$idx]=""
		win_crew_dw[$idx]=0
		win_rest_long[$idx]="$bwname"
		win_long_dw[$idx]=$REPLY_DW
		((total++))
		continue
	fi
```

(If the surrounding function does not already use `local`, drop the `local` keyword and assign `bwname="${bname:-$wname}"` plainly to match the file's style.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `nix develop -c bats tests/reflow-fanout.bats`
Expected: PASS (all, including the new bridge-label test).

- [ ] **Step 6: Commit**

```bash
nix develop -c git commit -am "fix(reflow): label @bridge_win windows from @window_bridge_name (#196)"
```

---

### Task 6: Full check + docs cross-reference

**Files:**
- Modify: `CLAUDE.md` (Claude status state files / enrichment options note — add `@window_bridge_name`)

- [ ] **Step 1: Run the full flake check**

Run: `nix flake check`
Expected: PASS (build + all bats + go tests + hooks).

- [ ] **Step 2: Add the option to CLAUDE.md's enrichment-options note**

In `CLAUDE.md`, in the "Enrichment window options" / Key Conventions area that enumerates `@window_ai_name`/`@window_task`, add one line: `@window_bridge_name` — daemon-owned remote window name for a `@bridge_win` mirror window; read only by reflow's bridge branch (#196).

- [ ] **Step 3: Commit**

```bash
nix develop -c git commit -am "docs: note @window_bridge_name option (#196)"
```

---

## Self-Review

**Spec coverage:**
- Daemon captures remote name → Task 1. ✓
- Sanitize `|`/control chars → Task 2. ✓
- Write `@window_bridge_name` on seed/add/rename → Tasks 3 (rename) + 4 (seed/add). ✓
- Instant-floor `rename-window` at seed/add → Task 4. ✓
- reflow reads option, `window_name` fallback → Task 5. ✓
- `|`-safety: sanitize (Task 2, applied Tasks 3-4) + end-of-FMT (Task 5). ✓
- Tests: reflow bats (Task 5), integration seed+rename asserting the option (Tasks 3-4), parse unit (Task 1). ✓

**Placeholder scan:** none — every code/test step shows complete code and an exact run command.

**Type/name consistency:** `remoteWindow.name` (Task 1) consumed in Tasks 3-4; `sanitizeWindowName` (Task 2) consumed in Tasks 3-4; option key `@window_bridge_name` identical across daemon writes (Tasks 3-4) and reflow read (Task 5); reflow var `bname`/`bwname` defined and used within Task 5.
