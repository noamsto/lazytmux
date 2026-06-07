# Claude Code Plugin + Issue Self-Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CC sessions self-report the issues they work on (`claude-status-update issue add ENG-123`); the ids display on tmux status line 0 and in the Go picker; the CC-side integration (hooks + skill) ships as an in-repo Claude Code plugin installable via marketplace or `--plugin-dir`.

**Architecture:** Issue ids live in `/tmp/claude-status/issues/<pane_id>` — a file **separate** from the pane state file, because state hooks fire `processing` writes around the very Bash call that runs `issue add` (sharing a file = guaranteed lost update). `claude-status` (session mode) and the Go picker read both files. The repo doubles as a CC plugin marketplace: `.claude-plugin/marketplace.json` at root points to `claude-plugin/` (plugin.json + hooks + skills).

**Tech Stack:** bash + bats (flake check), Go (bubbletea picker, `buildGoModule` runs `go test`), Nix flake, Claude Code plugin spec.

**Spec:** `docs/superpowers/specs/2026-06-07-cc-plugin-issue-tracking-design.md`
**Worktree:** `/home/noams/Data/git/noamsto/lazytmux/.worktrees/feat-8-cc-plugin` (run everything from this root)

**Conventions:** shell scripts are bash, shfmt with **tabs**; run `shellcheck` on every touched script; bats tests follow `tests/enrich.bats` style (`load helper`, `REPLY` pattern assertions).

---

### Task 1: `issue` subcommand in claude-status-update

**Files:**
- Modify: `scripts/claude-status-update.sh`
- Create: `tests/claude-issues.bats`

- [ ] **Step 1: Write the failing tests**

Create `tests/claude-issues.bats`:

```bats
#!/usr/bin/env bats

load helper

CSU="scripts/claude-status-update.sh"

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/claude-status"
	# Hermetic: ignore the tmux session the developer runs bats from
	unset TMUX TMUX_PANE
}

@test "issue add: creates issues file with id" {
	bash "$CSU" issue add ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "issue add: appends second id comma-separated" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add GH-42 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123,GH-42" ]
}

@test "issue add: dedupes existing id" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "issue done: removes id, keeps others" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add GH-42 --pane %7
	bash "$CSU" issue done ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "GH-42" ]
}

@test "issue done: removing last id removes the file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue done ENG-123 --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}

@test "issue done: missing file is a no-op" {
	run bash "$CSU" issue done ENG-123 --pane %7
	[ "$status" -eq 0 ]
}

@test "issue clear: removes the file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue clear --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}

@test "issue add: rejects id with invalid characters" {
	run bash "$CSU" issue add 'ENG#123' --pane %7
	[ "$status" -eq 1 ]
}

@test "issue add: rejects empty id" {
	run bash "$CSU" issue add --pane %7
	[ "$status" -eq 1 ]
}

@test "issue: rejects unknown action" {
	run bash "$CSU" issue frobnicate --pane %7
	[ "$status" -eq 1 ]
}

@test "issue add: no pane id exits 0 silently" {
	run bash "$CSU" issue add ENG-123
	[ "$status" -eq 0 ]
	[ ! -e "$CLAUDE_STATUS_DIR/issues" ] || [ -z "$(ls -A "$CLAUDE_STATUS_DIR/issues")" ]
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bats tests/claude-issues.bats`
Expected: all tests FAIL (script doesn't honor `CLAUDE_STATUS_DIR`, `issue` is rejected as invalid state).

- [ ] **Step 3: Implement**

In `scripts/claude-status-update.sh`:

(a) Replace the directory constants (current lines 11-12):

```bash
STATE_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
PANES_DIR="$STATE_DIR/panes"
ISSUES_DIR="$STATE_DIR/issues"
```

(b) Insert the `issue` handler immediately after `shift || true` (the line following `force=0`) and **before** the generic `while [[ $# -gt 0 ]]` option loop:

```bash
# Issue self-report: tracks which issues CC is working on, in a file SEPARATE
# from the pane state file — state hooks fire around the very Bash call that
# runs `issue add`, so sharing the pane file would lose updates.
if [[ $state == "issue" ]]; then
	action="${1:-}"
	shift || true
	id=""
	case "$action" in
	add | done)
		if [[ ${1:-} != --* ]]; then
			id="${1:-}"
			shift || true
		fi
		;;
	clear) ;;
	*)
		echo "Error: Invalid issue action '$action'. Use: add, done, clear" >&2
		exit 1
		;;
	esac
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--pane)
			pane_id="$2"
			shift 2
			;;
		*) shift ;;
		esac
	done
	if [[ $action != "clear" && ! $id =~ ^[A-Za-z0-9_-]+$ ]]; then
		echo "Error: Invalid issue id '$id' (allowed: A-Z a-z 0-9 _ -)" >&2
		exit 1
	fi
	[[ -z $pane_id ]] && exit 0
	issues_file="$ISSUES_DIR/${pane_id#%}"
	case "$action" in
	add)
		mkdir -p "$ISSUES_DIR"
		current=""
		[[ -f $issues_file ]] && IFS= read -r current <"$issues_file"
		case ",$current," in
		*",$id,"*) ;;
		*)
			[[ -n $current ]] && current+=","
			printf '%s\n' "$current$id" >"$issues_file"
			;;
		esac
		;;
	done)
		if [[ -f $issues_file ]]; then
			IFS= read -r current <"$issues_file"
			keep=()
			IFS=',' read -r -a ids <<<"$current"
			for i in "${ids[@]}"; do
				[[ $i == "$id" || -z $i ]] || keep+=("$i")
			done
			if ((${#keep[@]})); then
				(
					IFS=','
					printf '%s\n' "${keep[*]}"
				) >"$issues_file"
			else
				rm -f "$issues_file"
			fi
		fi
		;;
	clear)
		rm -f "$issues_file"
		;;
	esac
	if [[ -n ${TMUX:-} ]]; then
		tmux refresh-client -S 2>/dev/null || true
	fi
	exit 0
fi
```

Note: `issue` must NOT be added to the state-validation `case` list — the handler exits before validation runs.

- [ ] **Step 4: Run tests to verify they pass**

Run: `bats tests/claude-issues.bats`
Expected: all PASS

- [ ] **Step 5: Lint**

Run: `shellcheck scripts/claude-status-update.sh` — fix any findings.
Run: `shfmt -d scripts/claude-status-update.sh` — must be clean (tabs).

- [ ] **Step 6: Commit**

```bash
git add scripts/claude-status-update.sh tests/claude-issues.bats
git commit -m "feat(status): issue add/done/clear subcommand in separate pane file (#8)"
```

---

### Task 2: Lifecycle — clear/cleanup remove the issues file, state writes never touch it

**Files:**
- Modify: `scripts/claude-status-update.sh`
- Modify: `tests/claude-issues.bats`

- [ ] **Step 1: Write the failing tests** (append to `tests/claude-issues.bats`)

```bats
@test "state write does not touch the issues file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" processing --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "clear state removes pane and issues files" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" processing --pane %7
	bash "$CSU" clear --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/panes/7" ]
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}
```

- [ ] **Step 2: Run tests**

Run: `bats tests/claude-issues.bats`
Expected: "state write does not touch" PASSES already (separate file — that's the design working); "clear state removes" FAILS on the issues-file assertion.

- [ ] **Step 3: Implement**

(a) In the `clear` state handler (`if [[ $state == "clear" ]]`), extend the rm:

```bash
	rm -f "$PANES_DIR/$pane_file" "$ISSUES_DIR/$pane_file"
```

(replacing the existing `rm -f "$PANES_DIR/$pane_file"`).

(b) In `cleanup_stale_panes()`, inside the `if $should_remove; then` branch:

```bash
		if $should_remove; then
			rm -f "$pf" "$ISSUES_DIR/${pf##*/}"
		fi
```

and after the panes loop, add an orphan sweep over the issues dir (issues file whose pane is gone or no longer runs claude):

```bash
	# Orphaned issue files (pane gone, or no longer running claude)
	for inf in "$ISSUES_DIR"/*; do
		[[ -f $inf ]] || continue
		local issue_pane="${inf##*/}"
		if [[ -z ${pane_commands[$issue_pane]+x} ]] ||
			[[ ${pane_commands[$issue_pane]} != "claude" && ${pane_commands[$issue_pane]} != "opencode" ]]; then
			rm -f "$inf"
		fi
	done
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bats tests/claude-issues.bats`
Expected: all PASS

- [ ] **Step 5: Lint and commit**

Run: `shellcheck scripts/claude-status-update.sh && shfmt -d scripts/claude-status-update.sh`

```bash
git add scripts/claude-status-update.sh tests/claude-issues.bats
git commit -m "feat(status): issues file follows pane lifecycle (#8)"
```

---

### Task 3: `format_issue_list` + issues dir constants in lib-claude

**Files:**
- Modify: `scripts/lib-claude.sh`
- Modify: `tests/helper.bash`
- Modify: `tests/claude-issues.bats`

- [ ] **Step 1: Add helper loader** (append to `tests/helper.bash`)

```bash
setup_lib_claude() {
	# lib-claude.sh has no Nix placeholders; source directly.
	# shellcheck source=/dev/null
	source scripts/lib-claude.sh
}
```

- [ ] **Step 2: Write the failing tests** (append to `tests/claude-issues.bats`)

```bats
@test "format_issue_list: no ids yields empty" {
	setup_lib_claude
	format_issue_list 3
	[ -z "$REPLY" ]
}

@test "format_issue_list: under cap joins with spaces" {
	setup_lib_claude
	format_issue_list 3 ENG-1 GH-2
	[ "$REPLY" = "ENG-1 GH-2" ]
}

@test "format_issue_list: exactly at cap has no suffix" {
	setup_lib_claude
	format_issue_list 3 ENG-1 ENG-2 ENG-3
	[ "$REPLY" = "ENG-1 ENG-2 ENG-3" ]
}

@test "format_issue_list: over cap truncates with +N" {
	setup_lib_claude
	format_issue_list 3 ENG-1 ENG-2 ENG-3 ENG-4 ENG-5
	[ "$REPLY" = "ENG-1 ENG-2 ENG-3 +2" ]
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `bats tests/claude-issues.bats`
Expected: 4 new tests FAIL ("format_issue_list: command not found")

- [ ] **Step 4: Implement**

In `scripts/lib-claude.sh`, replace the `CLAUDE_PANES_DIR` line (current line 6) with:

```bash
# shellcheck disable=SC2034  # used by scripts that source this library
CLAUDE_STATUS_DIR="${CLAUDE_STATUS_DIR:-/tmp/claude-status}"
CLAUDE_PANES_DIR="$CLAUDE_STATUS_DIR/panes"
CLAUDE_ISSUES_DIR="$CLAUDE_STATUS_DIR/issues"
```

(keep the existing `# shellcheck disable=SC2034` comment above the block).

Append after `tally_claude_state()`:

```bash
# format_issue_list MAX [ID...]
# Joins issue ids with spaces, capped at MAX ids followed by "+N" overflow.
# Sets REPLY to e.g. "ENG-1 ENG-2 ENG-3 +2", empty if no ids.
format_issue_list() {
	local max="$1"
	shift
	if (($# == 0)); then
		REPLY=""
		return
	fi
	if (($# <= max)); then
		REPLY="$*"
		return
	fi
	local overflow=$(($# - max))
	REPLY="${*:1:max} +$overflow"
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bats tests/claude-issues.bats`
Expected: all PASS. Also run `bats tests/icons.bats tests/enrich.bats` to confirm no regression from the lib-claude edit.

- [ ] **Step 6: Lint and commit**

Run: `shellcheck scripts/lib-claude.sh tests/helper.bash && shfmt -d scripts/lib-claude.sh`

```bash
git add scripts/lib-claude.sh tests/helper.bash tests/claude-issues.bats
git commit -m "feat(status): format_issue_list helper + issues dir constant (#8)"
```

---

### Task 4: Status line 0 display — claude-status session mode appends issue ids

**Files:**
- Modify: `scripts/claude-status.sh`
- Modify: `tests/helper.bash`
- Modify: `tests/claude-issues.bats`

- [ ] **Step 1: Add script-under-test loader** (append to `tests/helper.bash`)

```bash
# Builds a runnable claude-status with the @lib_claude@ placeholder resolved.
# Sets CLAUDE_STATUS_SCRIPT to the path.
make_claude_status() {
	CLAUDE_STATUS_SCRIPT="$BATS_TEST_TMPDIR/claude-status.sh"
	sed "s|@lib_claude@|$PWD/scripts/lib-claude.sh|" scripts/claude-status.sh >"$CLAUDE_STATUS_SCRIPT"
}
```

- [ ] **Step 2: Write the failing tests** (append to `tests/claude-issues.bats`)

```bats
write_pane_fixture() {
	# write_pane_fixture PANE SESSION [ISSUES]
	mkdir -p "$CLAUDE_STATUS_DIR/panes" "$CLAUDE_STATUS_DIR/issues"
	printf 'state=processing\ntimestamp=%s\nsession=%s\n' "$(date +%s)" "$2" \
		>"$CLAUDE_STATUS_DIR/panes/$1"
	[ -n "${3:-}" ] && printf '%s\n' "$3" >"$CLAUDE_STATUS_DIR/issues/$1"
}

@test "claude-status session: appends issue ids after icon" {
	write_pane_fixture 7 work "ENG-123,GH-42"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[ "$status" -eq 0 ]
	[[ "$output" == *"ENG-123 GH-42"* ]]
}

@test "claude-status session: dedupes ids across panes and caps at 3" {
	write_pane_fixture 7 work "ENG-1,ENG-2"
	write_pane_fixture 8 work "ENG-2,ENG-3,ENG-4,ENG-5"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[[ "$output" == *"ENG-1 ENG-2 ENG-3 +2"* ]]
}

@test "claude-status session: no issues leaves output unchanged" {
	write_pane_fixture 7 work
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[ "$status" -eq 0 ]
	[[ "$output" != *"ENG"* ]]
}

@test "claude-status session: other session's issues not shown" {
	write_pane_fixture 7 work "ENG-1"
	write_pane_fixture 8 other "GH-9"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[[ "$output" == *"ENG-1"* ]]
	[[ "$output" != *"GH-9"* ]]
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `bats tests/claude-issues.bats`
Expected: the 3 tests asserting id presence FAIL; "unchanged" test passes vacuously.

- [ ] **Step 4: Implement**

In `scripts/claude-status.sh`:

(a) After the counting globals (below `any_unseen=0`), add:

```bash
issue_ids=() # union of issue ids across matched panes, insertion-ordered
declare -A _issue_seen=()

collect_pane_issues() {
	local f="$CLAUDE_ISSUES_DIR/$1" line id
	[[ -f $f ]] || return 0
	IFS= read -r line <"$f" || true
	local IFS=','
	for id in $line; do
		[[ -n $id && -z ${_issue_seen[$id]+x} ]] || continue
		_issue_seen[$id]=1
		issue_ids+=("$id")
	done
}
```

(b) In `count_for_session()`, after the `read_pane_state "$pf" || continue` line, add:

```bash
		collect_pane_issues "${pf##*/}"
```

Note: `collect_pane_issues` runs only when the pane file matched the session AND parsed — panes/sessions/windows in other modes never populate `issue_ids`, so their output is untouched.

(c) In `format_output()`, replace the `icon-color` case:

```bash
	icon-color)
		setup_claude_colors
		claude_colored_icon "$state" "$stale" "$unseen"
		local icon_out="$REPLY" issue_out=""
		if ((${#issue_ids[@]} > 0)); then
			format_issue_list 3 "${issue_ids[@]}"
			issue_out="${C_I}${REPLY}${C_R} "
		fi
		echo "${prefix}${icon_out}${issue_out}"
		;;
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `bats tests/claude-issues.bats`
Expected: all PASS

- [ ] **Step 6: Lint and commit**

Run: `shellcheck scripts/claude-status.sh tests/helper.bash && shfmt -d scripts/claude-status.sh`

```bash
git add scripts/claude-status.sh tests/helper.bash tests/claude-issues.bats
git commit -m "feat(status): show self-reported issue ids on status line 0 (#8)"
```

---

### Task 5: Wire claude-issues.bats into `nix flake check`

**Files:**
- Modify: `flake.nix` (next to `checks.icons-tests`, ~line 72)

- [ ] **Step 1: Add the check**

After the `checks.icons-tests` block in `flake.nix`:

```nix
        checks.claude-issues-tests =
          pkgs.runCommand "claude-issues-tests" {
            nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            LANG = "C.UTF-8";
            LC_ALL = "C.UTF-8";
          } ''
            cp -r ${./scripts} scripts
            cp -r ${./tests} tests
            bats tests/claude-issues.bats
            touch $out
          '';
```

- [ ] **Step 2: Run it**

Run: `nix build .#checks.x86_64-linux.claude-issues-tests`
Expected: builds successfully (tests pass in sandbox — no tmux, no TMUX_PANE; that's what the hermetic `unset` in setup guards).

- [ ] **Step 3: Commit**

```bash
git add flake.nix
git commit -m "test(flake): claude-issues bats check (#8)"
```

---

### Task 6: Go picker — read, aggregate, and render issue ids

**Files:**
- Modify: `picker/main.go`
- Modify: `picker/tui.go` (`buildSessionItems` ~line 734, `buildWindowItems` ~line 893)
- Create: `picker/issues_test.go`

- [ ] **Step 1: Write the failing test**

Create `picker/issues_test.go`:

```go
package main

import "testing"

func TestFormatIssueIDs(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		max  int
		want string
	}{
		{"empty", nil, 2, ""},
		{"under cap", []string{"ENG-1"}, 2, "ENG-1"},
		{"at cap", []string{"ENG-1", "GH-2"}, 2, "ENG-1 GH-2"},
		{"over cap", []string{"ENG-1", "GH-2", "ENG-3", "ENG-4"}, 2, "ENG-1 GH-2 +2"},
	}
	for _, c := range cases {
		if got := formatIssueIDs(c.ids, c.max); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAddIssuesDedupes(t *testing.T) {
	cc := &claudeCounts{}
	addIssues(cc, []string{"ENG-1", "GH-2"})
	addIssues(cc, []string{"GH-2", "ENG-3"})
	want := []string{"ENG-1", "GH-2", "ENG-3"}
	if len(cc.issues) != len(want) {
		t.Fatalf("got %v, want %v", cc.issues, want)
	}
	for i := range want {
		if cc.issues[i] != want[i] {
			t.Fatalf("got %v, want %v", cc.issues, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd picker && go test ./... ; cd ..`
Expected: compile FAIL (`formatIssueIDs`, `addIssues`, `claudeCounts.issues` undefined)

- [ ] **Step 3: Implement data layer** (`picker/main.go`)

(a) Add `issues []string` to both structs:

```go
type claudeCounts struct {
	waiting, compacting, processing, done, idle, errorCnt, denied int
	allStale                                                      bool
	anyUnseen                                                     bool
	issues                                                        []string // union of self-reported issue ids
}
```

and in `claudePaneInfo` (after `unseen bool`):

```go
	issues  []string
```

(b) In `collectClaudePanes()` (line ~826): the pane dir is `"/tmp/claude-status/panes"`; add next to it:

```go
	issuesDir := "/tmp/claude-status/issues"
```

and in the `result = append(result, claudePaneInfo{...})` literal add:

```go
			issues:  readPaneIssues(filepath.Join(issuesDir, e.Name())),
```

(c) Add below `collectClaudePanes`:

```go
// readPaneIssues reads the comma-separated self-reported issue id list for a
// pane. Missing file (the common case) yields nil.
func readPaneIssues(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(line, ",") {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
```

(d) In both `aggregateClaudeBySession` and `aggregateClaudeByWindow`, after the `addClaudeState(cc, p.state)` call add:

```go
		addIssues(cc, p.issues)
```

(e) Add near `addClaudeState`:

```go
func addIssues(cc *claudeCounts, ids []string) {
	for _, id := range ids {
		seen := false
		for _, e := range cc.issues {
			if e == id {
				seen = true
				break
			}
		}
		if !seen {
			cc.issues = append(cc.issues, id)
		}
	}
}
```

(f) Add near `appendClaudeIcon` (line ~1064):

```go
func formatIssueIDs(ids []string, max int) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= max {
		return strings.Join(ids, " ")
	}
	return fmt.Sprintf("%s +%d", strings.Join(ids[:max], " "), len(ids)-max)
}

// appendIssueIDs appends a dim "ENG-1 GH-2 +N" segment to the icons string.
// Ids are validated ASCII ([A-Za-z0-9_-]), so len() is the display width.
func appendIssueIDs(icons string, dw int, ids []string, cDim, reset string) (string, int) {
	s := formatIssueIDs(ids, 2)
	if s == "" {
		return icons, dw
	}
	return icons + cDim + s + reset + " ", dw + len(s) + 1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd picker && go test ./... ; cd ..`
Expected: PASS

- [ ] **Step 5: Render in the TUI**

In `picker/tui.go` `buildSessionItems` (line ~734), after the `appendClaudeIcon` call:

```go
		icons, dw = appendClaudeIcon(icons, dw, s.claude, theme, dim, reset)
		icons, dw = appendIssueIDs(icons, dw, s.claude.issues, cDim, reset)
```

In `buildWindowItems` (line ~893), same pattern:

```go
			icons, dw = appendClaudeIcon(icons, dw, w.claude, theme, dim, reset)
			icons, dw = appendIssueIDs(icons, dw, w.claude.issues, cDim, reset)
```

(`cDim` is already in scope in both functions.) Scope note: the legacy non-TUI `renderSessions`/`renderWindows` paths are intentionally NOT changed — spec covers the TUI picker rows only.

- [ ] **Step 6: Build + vet**

Run: `cd picker && go vet ./... && go build ./... && go test ./... ; cd ..`
Expected: clean. (No new module deps → `vendorHash` in `picker/default.nix` unchanged.)

- [ ] **Step 7: Commit**

```bash
git add picker/main.go picker/tui.go picker/issues_test.go
git commit -m "feat(picker): show self-reported issue ids on session/window rows (#8)"
```

---

### Task 7: Claude Code plugin — marketplace, manifest, hooks, skill, skills move

**Files:**
- Create: `.claude-plugin/marketplace.json`
- Create: `claude-plugin/.claude-plugin/plugin.json`
- Create: `claude-plugin/hooks/hooks.json`
- Create: `claude-plugin/scripts/status.sh`
- Create: `claude-plugin/skills/issue-tracking/SKILL.md`
- Move: `skills/tmux-interactive/` → `claude-plugin/skills/tmux-interactive/`
- Modify: `modules/home-manager.nix:279-283` (description), `:419-423` (source path)

- [ ] **Step 1: Marketplace manifest**

Create `.claude-plugin/marketplace.json`:

```json
{
  "name": "lazytmux",
  "owner": {
    "name": "noamsto"
  },
  "plugins": [
    {
      "name": "lazytmux",
      "source": "./claude-plugin",
      "description": "Claude Code integration for lazytmux: status-bar state hooks and issue self-report skill"
    }
  ]
}
```

- [ ] **Step 2: Plugin manifest**

Create `claude-plugin/.claude-plugin/plugin.json`:

```json
{
  "name": "lazytmux",
  "version": "0.1.0",
  "description": "Claude Code integration for lazytmux: status-bar state hooks and issue self-report skill"
}
```

- [ ] **Step 3: Hook wrapper**

Create `claude-plugin/scripts/status.sh`:

```bash
#!/usr/bin/env bash
# Degrade gracefully: lazytmux not installed, or CC running outside a lazytmux
# tmux pane → silently no-op instead of erroring on every hook event.
command -v claude-status-update >/dev/null 2>&1 || exit 0
exec claude-status-update "$@"
```

Then: `chmod +x claude-plugin/scripts/status.sh && shellcheck claude-plugin/scripts/status.sh && shfmt -d claude-plugin/scripts/status.sh`

- [ ] **Step 4: Hooks manifest**

Create `claude-plugin/hooks/hooks.json` (ports the state machine from nix-config's `--settings` overlay; `S` below abbreviates the wrapper path for reading — write it out fully in the file):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh cleanup"},
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh idle"}
        ]
      },
      {
        "matcher": "resume",
        "hooks": [
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh cleanup"},
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh idle"}
        ]
      },
      {
        "matcher": "clear",
        "hooks": [
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh cleanup"},
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh idle"}
        ]
      },
      {
        "matcher": "compact",
        "hooks": [
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh cleanup"},
          {"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing --force"}]}
    ],
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"}]}
    ],
    "Notification": [
      {"matcher": "permission_prompt", "hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh waiting"}]},
      {"matcher": "idle_prompt", "hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh done"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh done"}]}
    ],
    "StopFailure": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh error"}]}
    ],
    "PreCompact": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh compacting"}]}
    ],
    "PostCompact": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"}]}
    ],
    "PermissionDenied": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh denied"}]}
    ],
    "PostToolUseFailure": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh error"}]}
    ],
    "Elicitation": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh waiting"}]}
    ],
    "ElicitationResult": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh processing"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "\"${CLAUDE_PLUGIN_ROOT}\"/scripts/status.sh clear"}]}
    ]
  }
}
```

- [ ] **Step 5: Skill**

Create `claude-plugin/skills/issue-tracking/SKILL.md`:

```markdown
---
name: issue-tracking
description: Use when working on a Linear/GitHub issue or PR whose branch is NOT the current tmux window's branch — orchestrating from main, spawning agents into worktrees, or driving PRs via gh/MCP. Stamps issue ids into the tmux status bar.
---

# Issue Tracking

lazytmux shows which issues this Claude Code session is working on in the
tmux status bar (line 0) and in the session/window pickers.

## When to stamp

Only when the issue's branch is NOT the current window's branch — the
branch-derived stamp already covers the matching case.

- Picking up work on an issue/PR: `claude-status-update issue add <ID>`
- Issue merged / work finished: `claude-status-update issue done <ID>`
- Abandoning the whole batch: `claude-status-update issue clear`

Stamp every issue you orchestrate in parallel (one `issue add` per id).

## Id format

Ids must match `[A-Za-z0-9_-]+`:

- Linear: the key verbatim — `ENG-123`
- GitHub: `GH-<number>` — `GH-42` (never `#42`)

If `claude-status-update` is not on PATH (not inside a lazytmux tmux), skip
silently — do not report an error to the user.
```

- [ ] **Step 6: Move tmux-interactive + re-point the module**

```bash
git mv skills/tmux-interactive claude-plugin/skills/tmux-interactive
```

In `modules/home-manager.nix` change the skills install block (lines ~419-423):

```nix
          lib.optionalAttrs cfg.skills.enable (
            lib.mapAttrs' (name: _: {
              name = ".claude/skills/${name}";
              value.source = ../claude-plugin/skills/${name};
            }) (builtins.readDir ../claude-plugin/skills)
```

(only the two `../skills` → `../claude-plugin/skills` paths change; keep surrounding code identical) and update the option description (line ~283):

```nix
        description = "Whether to install Claude Code skills into ~/.claude/skills. Disable when the lazytmux Claude Code plugin is installed (marketplace or --plugin-dir) — the plugin ships the same skills.";
```

- [ ] **Step 7: Validate**

```bash
jq . .claude-plugin/marketplace.json claude-plugin/.claude-plugin/plugin.json claude-plugin/hooks/hooks.json
nix build .#default
```

Expected: jq prints all three (valid JSON); nix build succeeds (module eval + picker tests run inside `buildGoModule`).

- [ ] **Step 8: Commit**

```bash
git add .claude-plugin claude-plugin modules/home-manager.nix
git rm -r --cached skills 2>/dev/null || true
git commit -m "feat(plugin): CC plugin with status hooks + issue-tracking skill, in-repo marketplace (#8)"
```

---

### Task 8: Docs + full verification

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Step 1: CLAUDE.md updates**

(a) In the Script Roles table, replace the `claude-status-update` row's purpose with:

```
Writes state files to `/tmp/claude-status/panes/<pane_id>`; `issue add|done|clear <ID>` maintains self-reported issue ids in `/tmp/claude-status/issues/<pane_id>` (separate file — state hooks fire around the very call that stamps, sharing a file would lose updates). Called by the CC plugin's hooks / skill.
```

(b) Add a section after "### PR + Issue Enrichment":

```markdown
### Claude Code Plugin

The repo doubles as a CC plugin marketplace: `.claude-plugin/marketplace.json`
points at `claude-plugin/` (manifest, `hooks/hooks.json` with the
claude-status state machine, `skills/`). Hook commands route through
`claude-plugin/scripts/status.sh`, which no-ops when `claude-status-update`
isn't on PATH.

- Nix install: `claude --plugin-dir "${inputs.lazytmux}/claude-plugin"`
  (read-only store path is fine; plugin + scripts pinned to one revision).
- Marketplace install: `claude plugin marketplace add noamsto/lazytmux` then
  `claude plugin install lazytmux@lazytmux`.
- `programs.lazytmux.skills.enable` symlinks the same `claude-plugin/skills/`
  into `~/.claude/skills` — disable it when the plugin is installed.
- Self-reported issue ids show on status line 0 (cap 3 + `+N`) and picker
  rows (cap 2 + `+N`); they survive `/clear`/compaction and die with the pane
  or CC session.
```

(c) In Key Conventions, after the claude state-files bullet, add:

```markdown
- **Issue self-report files** at `/tmp/claude-status/issues/<pane_id>`: one line, comma-separated ids matching `[A-Za-z0-9_-]+`. Never written by the state machine — only by `claude-status-update issue`.
```

- [ ] **Step 2: README section**

Add a `## Claude Code plugin` section to `README.md` (place after the existing installation/options content, matching the README's heading style):

```markdown
## Claude Code plugin

The CC-side integration (status-bar hooks + issue-tracking skill) ships as a
Claude Code plugin in this repo.

Nix (recommended — pins plugin and tmux scripts to the same revision):

​```nix
# in your claude wrapper
claude --plugin-dir "${inputs.lazytmux}/claude-plugin"
​```

Marketplace:

​```bash
claude plugin marketplace add noamsto/lazytmux
claude plugin install lazytmux@lazytmux
​```

With the plugin installed, the tmux status bar tracks Claude state with zero
manual hook wiring, and Claude can stamp the issues it works on
(`claude-status-update issue add ENG-123`) so orchestrator sessions on `main`
show what they're actually doing.
```

(Strip the zero-width characters before the inner code fences — they're only there to nest fences in this plan.)

- [ ] **Step 3: Full verification**

```bash
nix build .
nix flake check
bats tests/claude-issues.bats tests/enrich.bats tests/icons.bats
```

Expected: all green.

- [ ] **Step 4: Manual smoke (requires the running tmux)**

```bash
./result/bin/tmux -V   # sanity: wrapper built
# In a CC pane inside tmux:
claude-status-update issue add ENG-999
# → status line 0 shows "ENG-999" dimmed next to the claude icon (≤1s)
# → prefix+s / prefix+w show the id on the session/window row
claude-status-update issue done ENG-999
# → id disappears
# Plugin loads:
claude --plugin-dir ./claude-plugin --help >/dev/null && echo plugin-ok
```

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: CC plugin install + issue self-report (#8)"
```

---

## Out of scope (tracked elsewhere)

- nix-config migration: remove status hooks from the `--settings` overlay, add `--plugin-dir`, set `skills.enable = false` (spec §8 — user's nix-config repo).
- Adding `issue add` calls to the user's orchestration skills (`/autopilot` etc. — separate repos).
- Legacy non-TUI picker render paths (`renderSessions`/`renderWindows`).
