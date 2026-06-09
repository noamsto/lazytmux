# Event Logging for Debugging — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in, runtime-toggleable JSON event log so we can debug what happened on which session/window/pane over time, with zero fork cost on hot paths when off.

**Architecture:** A sourced bash lib (`lib-log.sh`) gated on a `/tmp` sentinel file (`[[ -f ]]`, fork-free). When armed, instrumented scripts append one JSON line per event to `$XDG_STATE_HOME/lazytmux/events.log` (size-rotated, flock-guarded). The Go picker logs via a thin `lazytmux-log-event` CLI so there is one implementation. A `lazytmux-debug` command + `prefix + D` flips the sentinel.

**Tech Stack:** Bash (tabs, shfmt), Nix (`writeShellScript`/`writeShellScriptBin` substitution), Go (picker exec calls), bats (`nix flake check`).

**Spec:** `docs/superpowers/specs/2026-06-09-event-logging-design.md`

**Conventions for every task:**
- Shell indents with **tabs** (shfmt project default). Run `shellcheck <file>` on any changed `.sh` and fix all warnings before committing.
- Commit from inside the devshell: `nix develop --command bash -c '…'` (the `.pre-commit-config.yaml` symlink is materialized by the devShell `shellHook`).
- Work happens in the `feat/event-logging` worktree.

---

## File Structure

| File | Responsibility |
|---|---|
| `scripts/lib-log.sh` *(new)* | Gate (`log_enabled`), JSON escape (`_json_escape`), emit + rotate (`log_event`, `_log_rotate`). The single logging implementation. |
| `scripts/lazytmux-log-event.sh` *(new)* | Thin CLI: `source @lib_log@; log_event "$@"`. Lets the Go picker log without reimplementing. |
| `scripts/lazytmux-debug.sh` *(new)* | `on\|off\|toggle\|status\|tail` — flips the sentinel + `@lazytmux_debug`, reports status from the sentinel, tails the log. |
| `tests/log.bats` *(new)* | Unit tests for `lib-log.sh` pure logic. |
| `flake.nix` | Add `log-tests` check. |
| `config/tmux.conf.nix` | Build `lib-log`; route the new scripts + `claude-status-update` through a `@lib_log@`-substituting builder; add `@lib_log@` to `mkScriptFull`/`mkScriptEnrich`; bind `prefix + D`. |
| `scripts/claude-status-update.sh` | Guarded `source @lib_log@`; log state transitions (deduped) + issue ops. |
| `scripts/tmux-reflow-windows.sh` | `source @lib_log@`; log cache hit + recompute. |
| `scripts/tmux-issue-stamp.sh` | `source @lib_log@`; log provider selection / clear. |
| `scripts/tmux-pr-enrich.sh` | `source @lib_log@`; log PR writes. |
| `picker/main.go` | `logEvent(...)` helper (execs the CLI). |
| `picker/tui.go`, `picker/zoxide.go` | Call `logEvent` at switch / kill / create sites. |

---

## Task 1: `lib-log.sh` core + bats tests + flake check

**Files:**
- Create: `scripts/lib-log.sh`
- Create: `tests/log.bats`
- Modify: `flake.nix:58-96` (add a check)

- [ ] **Step 1: Write the failing tests**

Create `tests/log.bats` (the sentinel + state dir are redirected into the bats tmpdir so tests never touch the real `/tmp` sentinel):

```bash
#!/usr/bin/env bats

setup() {
	export XDG_STATE_HOME="$BATS_TEST_TMPDIR/state"
	export LAZYTMUX_DEBUG_SENTINEL="$BATS_TEST_TMPDIR/debug.on"
	source scripts/lib-log.sh
}

@test "log_event is a no-op when the sentinel is absent" {
	log_event claude event transition from idle to processing
	[ ! -f "$LAZYTMUX_LOG_FILE" ]
}

@test "log_event writes a JSON line when armed" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	log_event claude event transition from idle to processing
	run cat "$LAZYTMUX_LOG_FILE"
	[[ "$output" == *'"cat":"claude"'* ]]
	[[ "$output" == *'"event":"transition"'* ]]
	[[ "$output" == *'"from":"idle"'* ]]
	[[ "$output" == *'"to":"processing"'* ]]
}

@test "all values are quoted (numeric-looking session names stay strings)" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	log_event claude sess 10 win 2
	run cat "$LAZYTMUX_LOG_FILE"
	[[ "$output" == *'"sess":"10"'* ]]
	[[ "$output" == *'"win":"2"'* ]]
}

@test "_json_escape handles backslash, quote, tab, newline, control chars" {
	_json_escape $'a"b\\c\td\ne\x01f'
	[ "$REPLY" = 'a\"b\\c\tdef' ]
}

@test "rotation moves the log to .1 at the cap" {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	export LAZYTMUX_LOG_MAX_BYTES=200
	for i in $(seq 1 20); do log_event t k "value-$i-padding-padding-padding-padding"; done
	[ -f "$LAZYTMUX_LOG_FILE.1" ]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/noams/Data/git/noamsto/lazytmux/.worktrees/feat-event-logging && nix develop --command bats tests/log.bats`
Expected: FAIL — `scripts/lib-log.sh` does not exist (source error).

- [ ] **Step 3: Implement `scripts/lib-log.sh`**

Create `scripts/lib-log.sh` (indent with **tabs**):

```bash
#!/usr/bin/env bash
# Event logging for debugging tmux/claude/reflow/enrich/picker behavior.
# Sourced, not executed. Off unless the sentinel exists; the gate is a
# fork-free [[ -f ]] test, so hot paths pay nothing when debug is off.
# See docs/superpowers/specs/2026-06-09-event-logging-design.md
# shellcheck disable=SC2034  # exported names are used by sourcing scripts

LAZYTMUX_LOG_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/lazytmux"
LAZYTMUX_LOG_FILE="$LAZYTMUX_LOG_DIR/events.log"
# Sentinel lives in /tmp: dies on reboot, survives config reload (tmux sources
# the conf on every prefix+r, so a conf-load clear would disarm debug mid-bug).
LAZYTMUX_DEBUG_SENTINEL="${LAZYTMUX_DEBUG_SENTINEL:-/tmp/lazytmux-debug.on}"

# log_enabled: true when debug is armed. Fork-free builtin test — the hot-path gate.
log_enabled() { [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; }

# _json_escape STR -> REPLY  (JSON-safe inner string, no surrounding quotes).
# Backslash first, then quote/tab/cr; newlines stripped; remaining C0 controls stripped.
_json_escape() {
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\t'/\\t}
	s=${s//$'\r'/\\r}
	s=${s//$'\n'/}
	s=${s//[$'\x01'-$'\x08'$'\x0b'$'\x0c'$'\x0e'-$'\x1f']/}
	REPLY=$s
}

# _log_rotate: flock-guarded size rotation, keeps events.log.1. Cap is read live
# from LAZYTMUX_LOG_MAX_BYTES (default 5 MiB) so tests can shrink it.
_log_rotate() {
	[[ -f $LAZYTMUX_LOG_FILE ]] || return 0
	local cap="${LAZYTMUX_LOG_MAX_BYTES:-5242880}"
	local size
	size=$(stat -c %s "$LAZYTMUX_LOG_FILE" 2>/dev/null || echo 0)
	((size < cap)) && return 0
	(
		flock -n 9 || exit 0
		local s
		s=$(stat -c %s "$LAZYTMUX_LOG_FILE" 2>/dev/null || echo 0)
		((s >= cap)) && mv -f "$LAZYTMUX_LOG_FILE" "$LAZYTMUX_LOG_FILE.1"
	) 9>"$LAZYTMUX_LOG_DIR/.rotate.lock"
}

# log_event CATEGORY [KEY VALUE]...  No-op unless debug armed. One JSON line.
log_event() {
	log_enabled || return 0
	local cat=$1
	shift
	mkdir -p "$LAZYTMUX_LOG_DIR"
	local ts
	ts=$(date '+%FT%T.%3N')
	_json_escape "$cat"
	local line="{\"ts\":\"$ts\",\"cat\":\"$REPLY\""
	local k v ek
	while (($# >= 2)); do
		k=$1
		v=$2
		shift 2
		_json_escape "$k"
		ek=$REPLY
		_json_escape "$v"
		line+=",\"$ek\":\"$REPLY\""
	done
	line+="}"
	_log_rotate
	printf '%s\n' "$line" >>"$LAZYTMUX_LOG_FILE"
}
```

- [ ] **Step 4: Run shellcheck**

Run: `nix develop --command shellcheck scripts/lib-log.sh`
Expected: no warnings.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `nix develop --command bats tests/log.bats`
Expected: PASS (5 tests).

- [ ] **Step 6: Wire a `log-tests` check into `flake.nix`**

In `flake.nix`, inside `checks = { … }` (after the `claude-issues-tests` block, before the closing `};` at line 96), add:

```nix
          log-tests =
            pkgs.runCommand "log-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.util-linux];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/log.bats
              touch $out
            '';
```

(`util-linux` provides `flock`; `coreutils` provides `stat`/`date`/`seq`.)

- [ ] **Step 7: Verify the check runs under flake check**

Run: `nix build .#checks.x86_64-linux.log-tests -L`
Expected: builds successfully (bats passes inside the sandbox).

- [ ] **Step 8: Commit**

```bash
nix develop --command bash -c 'git add scripts/lib-log.sh tests/log.bats flake.nix && git commit -m "feat(log): add lib-log event logging core + tests"'
```

---

## Task 2: `lazytmux-log-event` + `lazytmux-debug` scripts

**Files:**
- Create: `scripts/lazytmux-log-event.sh`
- Create: `scripts/lazytmux-debug.sh`

- [ ] **Step 1: Create `scripts/lazytmux-log-event.sh`**

```bash
#!/usr/bin/env bash
# Thin CLI around lib-log's log_event, so non-bash callers (the Go picker) can
# log without reimplementing the helper. No-ops when debug is off.
# Usage: lazytmux-log-event <category> [key value]...
set -euo pipefail
# shellcheck source=/dev/null
source "@lib_log@"
log_event "$@"
```

- [ ] **Step 2: Create `scripts/lazytmux-debug.sh`**

```bash
#!/usr/bin/env bash
# Toggle / inspect event-logging debug mode. Sentinel armed => logging on.
# Usage: lazytmux-debug {on|off|toggle|status|tail}
set -euo pipefail
# shellcheck source=/dev/null
source "@lib_log@"

cmd="${1:-toggle}"
msg=""

arm() {
	: >"$LAZYTMUX_DEBUG_SENTINEL"
	tmux set -g @lazytmux_debug 1 2>/dev/null || true
	msg="lazytmux debug: ON — $LAZYTMUX_LOG_FILE"
}
disarm() {
	rm -f "$LAZYTMUX_DEBUG_SENTINEL"
	tmux set -g @lazytmux_debug 0 2>/dev/null || true
	msg="lazytmux debug: OFF"
}

case "$cmd" in
on) arm ;;
off) disarm ;;
toggle) if [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; then disarm; else arm; fi ;;
status)
	if [[ -f $LAZYTMUX_DEBUG_SENTINEL ]]; then
		size=0
		[[ -f $LAZYTMUX_LOG_FILE ]] && size=$(stat -c %s "$LAZYTMUX_LOG_FILE" 2>/dev/null || echo 0)
		msg="lazytmux debug: ON — $LAZYTMUX_LOG_FILE (${size} bytes)"
	else
		msg="lazytmux debug: OFF"
	fi
	;;
tail) exec tail -n +1 -f "$LAZYTMUX_LOG_FILE" ;;
*)
	echo "usage: lazytmux-debug {on|off|toggle|status|tail}" >&2
	exit 2
	;;
esac

printf '%s\n' "$msg"
# Surface the result in tmux when invoked from a keybinding.
[[ -n ${TMUX:-} ]] && tmux display-message -d 1500 "$msg" 2>/dev/null || true
```

- [ ] **Step 3: Run shellcheck on both**

Run: `nix develop --command shellcheck scripts/lazytmux-log-event.sh scripts/lazytmux-debug.sh`
Expected: no warnings. (The literal `@lib_log@` source resolves at build time; `# shellcheck source=/dev/null` silences the not-found note.)

- [ ] **Step 4: Commit**

```bash
nix develop --command bash -c 'git add scripts/lazytmux-log-event.sh scripts/lazytmux-debug.sh && git commit -m "feat(log): add lazytmux-log-event + lazytmux-debug scripts"'
```

---

## Task 3: Nix build wiring

**Files:**
- Modify: `config/tmux.conf.nix:108-110` (build `lib-log`), `:159` (add `mkScriptWithLog`), `:172-187` (`scriptNames`), `:196-197` (`mkScriptFull` array), `:208-225` (`mkScriptEnrich` array), `:236-249` (`script` dispatch)

- [ ] **Step 1: Build the `lib-log` derivation**

In `config/tmux.conf.nix`, after `lib-claude = mkLib "lib-claude";` (line 110), add:

```nix
  # lib-log has no build-time placeholders of its own; a plain writeShellScript.
  lib-log = pkgs.writeShellScript "lib-log" (builtins.readFile ../scripts/lib-log.sh);
```

- [ ] **Step 2: Add a `@lib_log@`-substituting builder**

After the `mkScriptWithLibs` block (ends line 159), add:

```nix
  # Scripts that source only lib-log (gated event logging). Includes
  # claude-status-update, which is run RAW by tests/claude-issues.bats — its
  # source is guarded so the raw script defines no-op stubs (see Task 4).
  scriptsWithLog = ["claude-status-update" "lazytmux-log-event" "lazytmux-debug"];

  mkScriptWithLog = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched = builtins.replaceStrings ["@lib_log@"] ["${lib-log}"] raw;
  in
    pkgs.writeShellScriptBin name patched;
```

- [ ] **Step 3: Register the two new scripts in `scriptNames`**

In the `scriptNames` list (lines 172-187), add two entries:

```nix
    "tmux-pr-enrich"
    "lazytmux-log-event"
    "lazytmux-debug"
  ];
```

- [ ] **Step 4: Add `@lib_log@` to `mkScriptFull` (for reflow)**

In `mkScriptFull` (lines 196-197), append `@lib_log@` / `${lib-log}` to the two arrays:

```nix
      ["@lib_icons@" "@lib_claude@" "@lib_enrich@" "claude-status " "@claude_status_bin@" "@ICON_MAP@" "@FALLBACK_ICON@" "@MAX_ICONS@" "@MAX_ICONS_PICKER@" "@picker_generate@" "@lib_log@"]
      ["${lib-icons}" "${lib-claude}" "${lib-enrich}" "${claude-status-bin} " claude-status-bin iconMapBash fallbackIcon maxIcons maxIconsPicker picker-generate-bin "${lib-log}"]
```

- [ ] **Step 5: Add `@lib_log@` to `mkScriptEnrich` (for issue-stamp + pr-enrich)**

In `mkScriptEnrich` (lines 209-224), append `@lib_log@` to the first array and `"${lib-log}"` to the second (keep the existing entries in order):

```nix
      [
        "@lib_enrich@"
        "@pr_refresh_seconds@"
        "@issue_stamp_linear@"
        "@issue_stamp_github@"
        "@pr_enrich@"
        "@reflow@"
        "@lib_log@"
      ]
      [
        "${lib-enrich}"
        (toString enrichPrRefreshSeconds)
        "${enrich-linear-bin}/bin/tmux-issue-stamp-linear"
        "${enrich-github-bin}/bin/tmux-issue-stamp-github"
        "${enrich-pr-bin}/bin/tmux-pr-enrich"
        "${script.tmux-reflow-windows}/bin/tmux-reflow-windows"
        "${lib-log}"
      ]
```

- [ ] **Step 6: Route `scriptsWithLog` in the `script` dispatch**

In the `script = lib.genAttrs scriptNames (name: …)` dispatch (lines 236-249), add a branch before the final `else mkScript name`:

```nix
    else if name == "claude-status"
    then claude-status-pkg
    else if builtins.elem name scriptsWithLog
    then mkScriptWithLog name
    else mkScript name);
```

- [ ] **Step 7: Build to verify wiring**

Run: `nix build .#default -L`
Expected: builds successfully. (`@lib_log@` now substitutes in the new scripts; the placeholder is a harmless no-op in scripts that don't yet contain it.)

- [ ] **Step 8: Verify the new commands are on PATH in the wrapper**

Run: `nix build .#default && ./result/bin/tmux -V >/dev/null && ls result/bin | grep lazytmux`
Expected: `lazytmux-debug` and `lazytmux-log-event` are listed.

- [ ] **Step 9: Commit**

```bash
nix develop --command bash -c 'git add config/tmux.conf.nix && git commit -m "feat(log): wire lib-log + debug scripts into the nix build"'
```

---

## Task 4: Instrument `claude-status-update.sh`

**Files:**
- Modify: `scripts/claude-status-update.sh` (guarded source near top; issue-op logs in the `issue` block; transition log before the final state write)

- [ ] **Step 1: Add the guarded source**

After `mkdir -p "$PANES_DIR"` (the directory-setup near line 16), add:

```bash
# Event logging (no-op unless debug armed). Guarded so the RAW script still runs
# under tests/claude-issues.bats, where @lib_log@ is not substituted.
# shellcheck source=/dev/null
if [[ -f "@lib_log@" ]]; then
	source "@lib_log@"
else
	log_enabled() { return 1; }
	log_event() { :; }
fi
```

- [ ] **Step 2: Log issue operations**

In the `issue` block, the `add`/`done`/`clear` cases mutate `$issues_file`. Immediately after the `case "$action" in … esac` that performs the mutation (just before the `tmux refresh-client` near the end of the issue block), add:

```bash
	log_enabled && log_event claude event issue op "$action" id "${id:-}" pane "%${pane_id#%}"
```

- [ ] **Step 3: Log the deduped state transition before the final write**

Immediately before the `# Write pane state with timestamp` / `printf -v _now '%(%s)T' -1` block at the end of the script, add:

```bash
# Log only real transitions (from != to). Pre/PostToolUse both fire "processing"
# on every tool call, so logging every write would flood. Prior-state read +
# tmux id lookups happen only when debug is armed.
if log_enabled; then
	prior_state="none"
	if [[ -f "$PANES_DIR/$pane_file" ]]; then
		while IFS='=' read -r _k _v; do
			[[ $_k == state ]] && {
				prior_state="$_v"
				break
			}
		done <"$PANES_DIR/$pane_file"
	fi
	if [[ $prior_state != "$state" ]]; then
		win_id=$(tmux display-message -t "$pane_id" -p '#{window_id}' 2>/dev/null || true)
		win_idx=$(tmux display-message -t "$pane_id" -p '#{window_index}' 2>/dev/null || true)
		log_event claude event transition from "$prior_state" to "$state" \
			pane "$pane_id" win_id "$win_id" win "$win_idx" sess "$session_name"
	fi
fi
```

- [ ] **Step 4: Run shellcheck**

Run: `nix develop --command shellcheck scripts/claude-status-update.sh`
Expected: no warnings.

- [ ] **Step 5: Verify the raw-script tests still pass (the guard works)**

Run: `nix develop --command bats tests/claude-issues.bats`
Expected: PASS (all existing tests — the guard makes `log_enabled`/`log_event` no-op stubs when run raw).

- [ ] **Step 6: Build**

Run: `nix build .#default -L`
Expected: builds (the real `@lib_log@` path is substituted via `mkScriptWithLog`).

- [ ] **Step 7: Commit**

```bash
nix develop --command bash -c 'git add scripts/claude-status-update.sh && git commit -m "feat(log): log claude state transitions + issue ops"'
```

---

## Task 5: Instrument `tmux-reflow-windows.sh`

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh` (source near the other `source @lib_*@` lines; cache-hit log at the early return ~line 44; recompute log before the batched `tmux source -`)

- [ ] **Step 1: Add the source line**

Next to the existing `source @lib_icons@` / `source @lib_claude@` lines near the top of the script, add:

```bash
# shellcheck source=/dev/null
source @lib_log@
```

- [ ] **Step 2: Log the cache hit**

In the fast-path block (lines 43-45), add the log before `exit 0`:

```bash
if ((!FORCE)) && [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	log_enabled && log_event reflow event cache_hit wins "${cache_key%%:*}" width "$WIDTH" sess "$SESSION"
	exit 0
fi
```

- [ ] **Step 3: Log the recompute**

Immediately before the batched-command execution block (the `{ printf '%s\n' "${tmux_cmds[@]}"; echo "refresh-client -S"; } | tmux source -` near line 290), add:

```bash
if log_enabled; then
	lines=2
	((current_line >= 1)) && lines=3
	((current_line >= 2)) && lines=4
	log_event reflow event recompute forced "$FORCE" wins "${cache_key%%:*}" \
		width "$WIDTH" split1 "$split1" split2 "$split2" lines "$lines" \
		labels_mode "$labels_mode" sess "$SESSION"
fi
```

- [ ] **Step 4: Run shellcheck**

Run: `nix develop --command shellcheck scripts/tmux-reflow-windows.sh`
Expected: no warnings.

- [ ] **Step 5: Build**

Run: `nix build .#default -L`
Expected: builds.

- [ ] **Step 6: Commit**

```bash
nix develop --command bash -c 'git add scripts/tmux-reflow-windows.sh && git commit -m "feat(log): log reflow cache hits + recomputes"'
```

---

## Task 6: Instrument `tmux-issue-stamp.sh` + `tmux-pr-enrich.sh`

**Files:**
- Modify: `scripts/tmux-issue-stamp.sh` (source; log selection + clear)
- Modify: `scripts/tmux-pr-enrich.sh` (source; log in `write_pr_options`)

- [ ] **Step 1: Source lib-log in `tmux-issue-stamp.sh`**

Next to the existing `source @lib_enrich@` line near the top, add:

```bash
# shellcheck source=/dev/null
source @lib_log@
```

- [ ] **Step 2: Log the no-match clear**

In the no-provider-matched branch (after the `@reflow@ … --force &` at line 59, before its `exit`/end), add:

```bash
	log_enabled && log_event enrich event stamp_clear win_id "$(tmux display-message -t "$target" -p '#{window_id}' 2>/dev/null || true)" sess "$(tmux display-message -t "$target" -p '#{session_name}' 2>/dev/null || true)"
```

- [ ] **Step 3: Log the chosen provider**

After the block that writes `@issue_*` options (after line 68, `tmux set-option -t "$target" -w @issue_branch "$branch"`), add:

```bash
log_enabled && log_event enrich event stamp provider "$chosen_provider" id "$id" title "$title" url "$url" win_id "$(tmux display-message -t "$target" -p '#{window_id}' 2>/dev/null || true)" sess "$(tmux display-message -t "$target" -p '#{session_name}' 2>/dev/null || true)"
```

- [ ] **Step 4: Source lib-log in `tmux-pr-enrich.sh`**

Next to the existing `source @lib_enrich@` line near the top, add:

```bash
# shellcheck source=/dev/null
source @lib_log@
```

- [ ] **Step 5: Log every PR write**

`write_pr_options TARGET NUMBER TITLE STATE CHECK URL MERGEABLE` is the single funnel for all `@pr_*` writes (forced and cached). At the end of that function (after `tmux set-option -t "$1" -w @pr_mergeable "${7:-}"`, line 81), add:

```bash
	log_enabled && log_event enrich event pr target "$1" number "$2" state "$4" check "$5" mergeable "${7:-}"
```

- [ ] **Step 6: Run shellcheck**

Run: `nix develop --command shellcheck scripts/tmux-issue-stamp.sh scripts/tmux-pr-enrich.sh`
Expected: no warnings.

- [ ] **Step 7: Build + verify enrich tests still pass**

Run: `nix build .#default -L && nix develop --command bats tests/enrich.bats`
Expected: builds; enrich tests PASS (they source `lib-enrich.sh`, unaffected).

- [ ] **Step 8: Commit**

```bash
nix develop --command bash -c 'git add scripts/tmux-issue-stamp.sh scripts/tmux-pr-enrich.sh && git commit -m "feat(log): log issue stamps + PR writes"'
```

---

## Task 7: Instrument the Go picker

**Files:**
- Modify: `picker/main.go` (add `logEvent` helper)
- Modify: `picker/tui.go:241,249,251` (switch / kill-window / kill-session)
- Modify: `picker/zoxide.go:152-156` (create + switch)

- [ ] **Step 1: Add the `logEvent` helper to `picker/main.go`**

Add this function (near other small helpers in `main.go`; ensure `os/exec` is imported — it already is, given existing `exec.Command` use):

```go
// logEvent fires the lazytmux-log-event CLI (best-effort, never blocks the UI).
// Bare-name exec relies on the tmux wrapper's PATH, like our tmux/zoxide calls.
// No-ops when debug is off (the CLI checks the sentinel).
func logEvent(args ...string) {
	exec.Command("lazytmux-log-event", args...).Run() //nolint:errcheck
}
```

- [ ] **Step 2: Log switch + kills in `tui.go`**

At `picker/tui.go:241` (the `else` switch branch), before the `exec.Command("tmux", "switch-client", …)`:

```go
			} else {
				logEvent("picker", "event", "switch", "target", item.target)
				exec.Command("tmux", "switch-client", "-t", item.target).Run() //nolint:errcheck
			}
```

At the `ctrl+x` kill cases (lines 249/251):

```go
			if strings.Contains(item.target, ":") {
				logEvent("picker", "event", "kill_window", "target", item.target)
				exec.Command("tmux", "kill-window", "-t", item.target).Run() //nolint:errcheck
			} else {
				logEvent("picker", "event", "kill_session", "target", item.target)
				exec.Command("tmux", "kill-session", "-t", item.target).Run() //nolint:errcheck
			}
```

- [ ] **Step 3: Log create in `zoxide.go`**

In `createAndSwitch` (`picker/zoxide.go`), after the successful new-session / before the switch:

```go
	logEvent("picker", "event", "create", "target", name, "path", path)
	exec.Command("tmux", "switch-client", "-t", "="+name).Run() //nolint:errcheck
```

- [ ] **Step 4: Build + vet the picker**

Run: `cd /home/noams/Data/git/noamsto/lazytmux/.worktrees/feat-event-logging/picker && nix develop --command bash -c 'go build ./... && go vet ./... && go test ./...'`
Expected: builds, vets clean, existing tests PASS (no behavior change).

- [ ] **Step 5: Commit**

```bash
nix develop --command bash -c 'git add picker/main.go picker/tui.go picker/zoxide.go && git commit -m "feat(log): log picker switch/create/kill via lazytmux-log-event"'
```

---

## Task 8: Bind `prefix + D` to toggle debug

**Files:**
- Modify: `config/tmux.conf.nix` (prefix bindings region, near line 418)

- [ ] **Step 1: Add the keybinding**

In the prefix key bindings (alongside `bind-key "g"` / `bind-key "b"` near line 418), add:

```
    bind-key D run-shell "${script.lazytmux-debug}/bin/lazytmux-debug toggle"
```

- [ ] **Step 2: Build**

Run: `nix build .#default -L`
Expected: builds (`script.lazytmux-debug` resolves — it was added to `scriptNames` in Task 3).

- [ ] **Step 3: Commit**

```bash
nix develop --command bash -c 'git add config/tmux.conf.nix && git commit -m "feat(log): bind prefix+D to toggle debug logging"'
```

---

## Task 9: Full verification + manual smoke

**Files:** none (verification only)

- [ ] **Step 1: Run the full flake check**

Run: `nix flake check -L`
Expected: all checks (including `log-tests`, `enrich-tests`, `icons-tests`, `claude-issues-tests`) pass; `nix build .#default` succeeds.

- [ ] **Step 2: Manual smoke — arm, exercise, inspect**

Run (in a real tmux started from the new build, or after `home-manager switch` + reload):

```bash
result/bin/tmux  # or reload your running tmux
# inside tmux:
lazytmux-debug on
# trigger events: switch windows (reflow), open the picker (prefix+s) and switch,
# kill a window from the picker (ctrl+x), let a claude pane change state.
lazytmux-debug status      # ON + byte count
lazytmux-debug tail        # watch the JSON timeline (Ctrl-C to stop)
```
Expected: `events.log` contains correlated lines with `cat` of `claude`/`reflow`/`enrich`/`picker`, stable `pane=%N`/`win_id=@N`, ms timestamps, all values quoted.

- [ ] **Step 3: Manual smoke — gate is off by default + reload-safe**

```bash
lazytmux-debug off
# trigger more events — confirm events.log stops growing (lazytmux-debug status size stable)
lazytmux-debug on
# prefix + r  (reload config)
lazytmux-debug status      # still ON — reload did not disarm it
lazytmux-debug off
```
Expected: off → no new lines; reload preserves armed state.

- [ ] **Step 4: Confirm prefix+D toggles**

Press `prefix + D` twice; expect a `lazytmux debug: ON …` then `OFF` flash via `display-message`.

- [ ] **Step 5: Final no-op commit if any verification doc/notes added**

(Only if Step 1-4 surfaced fixes; otherwise nothing to commit.)

---

## Notes / deliberate deviations from the spec

- **Reflow `trigger` field dropped.** The hooks call `tmux-reflow-windows` without naming which hook fired; plumbing hook names through every `set-hook` is out of scope. Logged `forced` (0/1) instead, which is the actionable bit.
- **PR log fields trimmed to what `write_pr_options` holds** (`target number state check mergeable`). `branch`/`cache` are not available at that single funnel without threading; `target` identifies the window, which is the debugging key.
- **`lazytmux-debug` keybind uses a plain `bind-key D`** (which-key surfaces bound prefix keys automatically) rather than a custom which-key menu entry.
- **Sentinel in `/tmp`, no server-start clear** — refines spec §1: tmux sources the conf on every `prefix + r`, so a conf-load clear would disarm debug mid-session. `/tmp` gives reboot-transience without the reload footgun. (Spec already updated to match.)
```
