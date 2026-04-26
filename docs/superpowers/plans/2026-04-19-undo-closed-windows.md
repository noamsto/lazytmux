# Undo Closed Windows Implementation Plan

> **Status:** Superseded by the unified plan derived from `docs/superpowers/specs/2026-04-26-tmux-state-store-design.md`. Do not implement this plan as-is — undo functionality is folded into the state-store design (SQLite-backed, Go implementation, shared with periodic snapshots).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `prefix+u` (quick pop) and `prefix+U` (picker) to re-open recently closed tmux sessions, windows, and panes.

**Architecture:** A single bash script `scripts/tmux-undo.sh` wired to tmux hooks (`pane-died`, `window-unlinked`, `session-closed`, plus structural hooks to maintain a shadow index). Captures are stored as flat `key=value` files under `/tmp/lazytmux-undo/` with a stack capped at 10. Restoration recreates sessions/windows/panes using `new-session`, `new-window`, `split-window`, and `select-layout`.

**Tech Stack:** bash (inside tmux), fzf (for picker), Nix (build), tmux hooks and format strings. No new runtime dependencies (fzf is already in the build).

**Spec:** `docs/superpowers/specs/2026-04-19-undo-closed-windows-design.md`

**Testing:** Lazytmux has no unit tests. Per-task validation = `shellcheck` + `nix build .` + a manual tmux sequence documented inline. Each task ends with a commit.

---

## File Structure

| Path | Change | Responsibility |
|---|---|---|
| `scripts/tmux-undo.sh` | **Create** | Subcommand dispatcher: `capture {pane,window,session}`, `refresh-shadow`, `pop`, `picker`, `preview`. All logic in one file; internal functions partition by concern. |
| `config/tmux.conf.nix` | Modify | Add `tmux-undo` to `scriptNames`, wire hooks and keybindings. |
| `CLAUDE.md` | Modify | Append a row to the "Script Roles" table. |

No new lib file — existing scripts use inline functions when the logic is single-consumer. If `tmux-undo.sh` grows past ~400 LOC or a second consumer appears, extract `lib-undo.sh` later.

---

## Task 1: Scaffolding — empty script + Nix wiring

**Files:**
- Create: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix` (add to `scriptNames`)

- [ ] **Step 1: Create the script with subcommand dispatch**

Write `scripts/tmux-undo.sh`:

```bash
#!/usr/bin/env bash
# Undo for closed tmux sessions, windows, and panes.
# Hooks capture closures; `pop` and `picker` restore them.
# State lives in /tmp/lazytmux-undo/.
# See docs/superpowers/specs/2026-04-19-undo-closed-windows-design.md
set -u

UNDO_ROOT="/tmp/lazytmux-undo"
SCRATCH_DIR="$UNDO_ROOT/_scratch"
LIVE_DIR="$UNDO_ROOT/_live"
CORRUPT_DIR="$UNDO_ROOT/_corrupt"
LOG_FILE="$UNDO_ROOT/_log"
STACK_CAP=10
DEDUP_TTL=2 # seconds

log() {
	local ts
	ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	printf '%s %s\n' "$ts" "$*" >>"$LOG_FILE" 2>/dev/null || true
}

ensure_dirs() {
	mkdir -p "$UNDO_ROOT" "$SCRATCH_DIR" "$LIVE_DIR" "$CORRUPT_DIR" 2>/dev/null || true
}

main() {
	ensure_dirs
	local cmd="${1:-}"
	shift || true
	case "$cmd" in
	capture)
		local level="${1:-}"
		shift || true
		case "$level" in
		pane) capture_pane "$@" ;;
		window) capture_window "$@" ;;
		session) capture_session "$@" ;;
		*)
			log "unknown capture level: $level"
			exit 2
			;;
		esac
		;;
	refresh-shadow) refresh_shadow "$@" ;;
	pop) pop "$@" ;;
	picker) picker "$@" ;;
	preview) preview "$@" ;;
	*)
		echo "usage: tmux-undo {capture <level>|refresh-shadow|pop|picker|preview}" >&2
		exit 2
		;;
	esac
}

# Stubs — filled in by later tasks.
capture_pane() { log "capture_pane: not implemented"; }
capture_window() { log "capture_window: not implemented"; }
capture_session() { log "capture_session: not implemented"; }
refresh_shadow() { log "refresh_shadow: not implemented"; }
pop() { log "pop: not implemented"; }
picker() { log "picker: not implemented"; }
preview() { log "preview: not implemented"; }

main "$@"
```

- [ ] **Step 2: Register in Nix build**

Edit `config/tmux.conf.nix`, add `"tmux-undo"` to the `scriptNames` list (after `"tmux-scratchpad"`):

```nix
  scriptNames = [
    "claude-status"
    "claude-status-update"
    "tmux-reflow-windows"
    "tmux-session-picker"
    "tmux-window-picker"
    "tmux-update-icons"
    "tmux-branch-display"
    "tmux-dir-display"
    "tmux-apply-theme-colors"
    "claude-copy-mode"
    "tmux-scratchpad"
    "tmux-undo"
  ];
```

Do **not** add to `scriptsWithIcons` — this script doesn't need icon map / claude-status placeholders.

- [ ] **Step 3: Verify build + shellcheck**

Run:

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

Expected: both commands exit 0. `result/bin/tmux-undo` should exist (or at least the build succeeds — the wrapped tmux puts scripts on PATH).

- [ ] **Step 4: Smoke-test the dispatch**

Run:

```fish
./result/bin/tmux-undo 2>&1; echo "exit=$status"
./result/bin/tmux-undo capture pane 2>/dev/null
cat /tmp/lazytmux-undo/_log
```

Expected: first prints usage and exits 2; second creates `/tmp/lazytmux-undo/_log` with a "not implemented" line.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): scaffold tmux-undo script with subcommand dispatch"
```

---

## Task 2: Storage helpers — entry IO, cap enforcement, dedup markers

**Files:**
- Modify: `scripts/tmux-undo.sh`

- [ ] **Step 1: Add timestamp, entry-path, and cap helpers**

Insert these helpers above the `main()` function in `scripts/tmux-undo.sh`:

```bash
# Monotonic-ish millisecond timestamp (padded for lexical sort).
now_ms() {
	local s ns
	read -r s ns < <(date +'%s %N')
	printf '%013d' "$((s * 1000 + 10#${ns:0:3}))"
}

# All live entry files, newest last (lexical == chronological since fixed-width).
list_entries_sorted() {
	shopt -s nullglob
	local f
	for f in "$UNDO_ROOT"/*.pane "$UNDO_ROOT"/*.window "$UNDO_ROOT"/*.session; do
		printf '%s\n' "$f"
	done | sort
	shopt -u nullglob
}

# Enforce STACK_CAP: delete oldest entries past the limit.
enforce_cap() {
	local -a entries
	mapfile -t entries < <(list_entries_sorted)
	local count=${#entries[@]}
	if ((count > STACK_CAP)); then
		local drop=$((count - STACK_CAP))
		local i
		for ((i = 0; i < drop; i++)); do
			rm -f -- "${entries[$i]}" 2>/dev/null || true
		done
	fi
}

# Write a new entry atomically. Args: level, key=value pairs on stdin.
# Prints the created file path.
write_entry() {
	local level="$1"
	local ts
	ts=$(now_ms)
	local final="$UNDO_ROOT/$ts.$level"
	local tmp="$final.tmp.$$"
	cat >"$tmp"
	mv -- "$tmp" "$final"
	enforce_cap
	printf '%s\n' "$final"
}
```

- [ ] **Step 2: Add dedup marker helpers**

Append below the cap helpers:

```bash
# Dedup markers are files whose mtime is their creation time.
# A marker is "fresh" if mtime is within DEDUP_TTL seconds of now.
dedup_path() {
	printf '%s/_dedup_%s' "$UNDO_ROOT" "$1"
}

set_dedup() {
	local id="$1"
	: >"$(dedup_path "$id")" 2>/dev/null || true
}

dedup_fresh() {
	local id="$1"
	local f
	f=$(dedup_path "$id")
	[[ -f $f ]] || return 1
	local mtime now age
	mtime=$(stat -c %Y "$f" 2>/dev/null || echo 0)
	now=$(date +%s)
	age=$((now - mtime))
	((age <= DEDUP_TTL))
}

# Best-effort cleanup of stale markers.
prune_dedup() {
	shopt -s nullglob
	local f mtime now age
	now=$(date +%s)
	for f in "$UNDO_ROOT"/_dedup_*; do
		mtime=$(stat -c %Y "$f" 2>/dev/null || echo 0)
		age=$((now - mtime))
		((age > DEDUP_TTL)) && rm -f -- "$f" 2>/dev/null
	done
	shopt -u nullglob
}
```

- [ ] **Step 3: Verify shellcheck**

Run:

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

Expected: clean.

- [ ] **Step 4: Defer smoke-testing until Task 4**

These helpers have no user-facing subcommand yet — they're invoked internally by later capture/restore code. Confirmation that shellcheck + nix build pass in Step 3 is sufficient. Real-world exercise happens once Task 4 wires up `capture_pane`.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh
git commit -m "feat(undo): add storage helpers (entry IO, cap, dedup markers)"
```

---

## Task 3: Shadow index — refresh subcommand + hook wiring

**Files:**
- Modify: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Implement refresh_shadow**

Replace the `refresh_shadow()` stub in `scripts/tmux-undo.sh`:

```bash
# Called by structural tmux hooks. Rewrites /tmp/lazytmux-undo/_live/<session_id>
# with the current session's window + pane layout (minus cwds — those come from
# pane-died scratch entries at capture time).
#
# Args: $1 = session_id ($N) — optional; if empty, refreshes all live sessions.
refresh_shadow() {
	local target="${1:-}"
	local sessions
	if [[ -n $target ]]; then
		sessions="$target"
	else
		sessions=$(tmux list-sessions -F '#{session_id}' 2>/dev/null || true)
	fi
	local sid
	while IFS= read -r sid; do
		[[ -n $sid ]] || continue
		write_shadow_for_session "$sid"
	done <<<"$sessions"
}

write_shadow_for_session() {
	local sid="$1"
	local shadow="$LIVE_DIR/${sid#\$}"
	local tmp="$shadow.tmp.$$"
	local session_name
	session_name=$(tmux display-message -p -t "$sid" '#{session_name}' 2>/dev/null) || {
		rm -f -- "$shadow"
		return 0
	}
	{
		printf 'session_id=%s\n' "$sid"
		printf 'session_name=%s\n' "$session_name"
		local win_count=0
		while IFS=$'\t' read -r win_id win_index win_name win_layout; do
			printf 'win_%d_id=%s\n' "$win_count" "$win_id"
			printf 'win_%d_index=%s\n' "$win_count" "$win_index"
			printf 'win_%d_name=%s\n' "$win_count" "$win_name"
			printf 'win_%d_layout=%s\n' "$win_count" "$win_layout"
			win_count=$((win_count + 1))
		done < <(tmux list-windows -t "$sid" -F '#{window_id}	#{window_index}	#{window_name}	#{window_layout}' 2>/dev/null || true)
		printf 'window_count=%d\n' "$win_count"
	} >"$tmp"
	mv -- "$tmp" "$shadow"
}

# Called on session-closed with the dying session_id — just remove its shadow.
drop_shadow() {
	local sid="$1"
	rm -f -- "$LIVE_DIR/${sid#\$}"
}
```

- [ ] **Step 2: Wire refresh-shadow hooks in Nix config**

Edit `config/tmux.conf.nix`. Find the existing hooks block (around line 356-361, after the `set-hook -gu ...` resets). Some of the hook names we need — `after-new-window`, `after-new-session`, `window-linked` — already have unindexed hooks calling `tmux-reflow-windows`. Adding another unindexed hook would clobber the existing one. Use high indices like `[50]` so both run:

Add below the existing `client-session-changed` line (~361):

```tmux
    # === Undo: shadow-index refresh on structural changes ===
    set-hook -g after-new-window[50]      'run-shell "${script.tmux-undo}/bin/tmux-undo refresh-shadow #{session_id}"'
    set-hook -g window-linked[50]         'run-shell "${script.tmux-undo}/bin/tmux-undo refresh-shadow #{session_id}"'
    set-hook -g window-renamed[50]        'run-shell "${script.tmux-undo}/bin/tmux-undo refresh-shadow #{session_id}"'
    set-hook -g window-layout-changed[50] 'run-shell "${script.tmux-undo}/bin/tmux-undo refresh-shadow #{session_id}"'
    set-hook -g after-new-session[50]     'run-shell "${script.tmux-undo}/bin/tmux-undo refresh-shadow #{session_id}"'
```

- [ ] **Step 3: Verify shellcheck + build**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

- [ ] **Step 4: Manual test the shadow index**

Reload tmux config in an active tmux session:

```fish
tmux source-file ~/.config/tmux/tmux.conf  # or prefix+r
tmux new-window -t '#{session_id}'
ls /tmp/lazytmux-undo/_live/
cat /tmp/lazytmux-undo/_live/*
```

Expected: a shadow file per live session exists, containing `session_id`, `session_name`, `window_count`, and per-window layout entries.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): add shadow index of live session structure"
```

---

## Task 4: Pane scratch — capture pane cwd on pane-died

**Files:**
- Modify: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Implement scratch writer**

Add to `scripts/tmux-undo.sh` (near the capture stubs):

```bash
# Write a scratch file for a dying pane. Called by pane-died hook with
# pre-death pane info. Scratch files are later promoted to pane entries or
# aggregated into window/session entries.
#
# Args: $1 = pane_id ($%N), $2 = window_id ($@N), $3 = session_id ($N),
#       $4 = pane_current_path, $5 = window_name, $6 = session_name,
#       $7 = window_index
write_pane_scratch() {
	local pane_id="$1" window_id="$2" session_id="$3"
	local cwd="$4" win_name="$5" sess_name="$6" win_idx="$7"
	local key="${pane_id#%}"
	local f="$SCRATCH_DIR/$key"
	{
		printf 'pane_id=%s\n' "$pane_id"
		printf 'window_id=%s\n' "$window_id"
		printf 'session_id=%s\n' "$session_id"
		printf 'cwd=%s\n' "$cwd"
		printf 'window_name=%s\n' "$win_name"
		printf 'session_name=%s\n' "$sess_name"
		printf 'window_index=%s\n' "$win_idx"
		printf 'ts=%s\n' "$(now_ms)"
	} >"$f.tmp.$$" && mv -- "$f.tmp.$$" "$f"
}
```

- [ ] **Step 2: Replace capture_pane stub with scratch-write entrypoint**

Replace the `capture_pane()` stub with:

```bash
# Called on pane-died. Always writes a scratch file; whether it is promoted
# to a pane entry is decided by capture_pane_promote() later (triggered from
# the same hook after scratch write).
#
# Args from hook: $1..$7 = pane_id window_id session_id cwd win_name sess_name win_idx
capture_pane() {
	write_pane_scratch "$@"
	# Defer the promotion decision — window-unlinked / session-closed hooks
	# may fire immediately after and aggregate this scratch. We handle
	# promotion in a second pass below.
	capture_pane_promote "$1" "$2" "$3"
}

# Promote the scratch for $1 to a pane entry IF no cascading close is in flight.
# Args: pane_id window_id session_id
capture_pane_promote() {
	local pane_id="$1" window_id="$2" session_id="$3"
	prune_dedup
	# If parent window or session is already marked as "closing," skip.
	# (Markers are set by window/session capture before they iterate scratches.)
	if dedup_fresh "$window_id" || dedup_fresh "$session_id"; then
		return 0
	fi
	# Check liveness: if window still exists, write pane entry; else let
	# window capture handle it.
	if tmux display-message -p -t "$window_id" '#{window_id}' >/dev/null 2>&1; then
		emit_pane_entry "$pane_id"
	fi
	# If window doesn't exist, scratch stays until window/session capture consumes it.
}

# Read scratch, write a pane entry, delete scratch.
emit_pane_entry() {
	local pane_id="$1"
	local key="${pane_id#%}"
	local f="$SCRATCH_DIR/$key"
	[[ -f $f ]] || return 0
	# shellcheck source=/dev/null
	declare -A sc=()
	local k v
	while IFS='=' read -r k v; do
		sc["$k"]="$v"
	done <"$f"
	local split_dir="none"
	# If the surviving window now has > 0 panes, split direction is unknown
	# from the scratch alone — we fall back to "none" which triggers a
	# window-level fallback in the restore path if needed.
	{
		printf 'level=pane\n'
		printf 'timestamp=%s\n' "${sc[ts]}"
		printf 'display_name=%s:%s (%s)\n' "${sc[session_name]}" "${sc[window_name]}" "${sc[cwd]}"
		printf 'session_name=%s\n' "${sc[session_name]}"
		printf 'session_id=%s\n' "${sc[session_id]}"
		printf 'window_id=%s\n' "${sc[window_id]}"
		printf 'window_name=%s\n' "${sc[window_name]}"
		printf 'window_index=%s\n' "${sc[window_index]}"
		printf 'pane_cwd=%s\n' "${sc[cwd]}"
		printf 'split_direction=%s\n' "$split_dir"
	} | write_entry pane >/dev/null
	rm -f -- "$f"
}
```

- [ ] **Step 3: Wire the pane-died hook**

Edit `config/tmux.conf.nix`. Add after the existing hook block (before pane-mode-changed at ~364):

```tmux
    # === Undo: capture closed panes/windows/sessions ===
    set-hook -g pane-died[50] 'run-shell "${script.tmux-undo}/bin/tmux-undo capture pane #{pane_id} #{window_id} #{session_id} \"#{pane_current_path}\" \"#{window_name}\" \"#{session_name}\" #{window_index}"'
```

Note: `pane-died` provides `#{pane_current_path}` referring to the dying pane. Quoting with `\"...\"` handles paths/names containing spaces.

- [ ] **Step 4: Verify shellcheck + build**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

- [ ] **Step 5: Manual test scratch writing**

Reload config (`prefix+r`), then in a throwaway window with two panes (split with `prefix+"` or `prefix+%`):

```fish
# Kill one of the two panes (kill the non-focused one)
tmux kill-pane -t :.!
ls /tmp/lazytmux-undo/_scratch/    # scratch may be gone already (promoted)
ls /tmp/lazytmux-undo/*.pane       # pane entry should exist
cat /tmp/lazytmux-undo/*.pane | head -20
```

Expected: one `*.pane` entry with `level=pane`, the cwd of the killed pane, and the surviving window's name/id.

- [ ] **Step 6: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): capture dying panes via pane-died hook"
```

---

## Task 5: Window capture — aggregate scratches on window-unlinked

**Files:**
- Modify: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Implement capture_window**

Replace the `capture_window()` stub:

```bash
# Called on window-unlinked (after the window is gone from the tmux object tree,
# but scratches for its panes should exist from pane-died firings earlier).
#
# Args from hook: $1 = window_id (dead), $2 = session_id (parent, still alive)
capture_window() {
	local window_id="$1" session_id="$2"
	prune_dedup

	# If a session capture is in flight for the parent, skip — session owns it.
	if dedup_fresh "$session_id"; then
		return 0
	fi
	# If parent session is gone too, skip — session capture will handle.
	if ! tmux display-message -p -t "$session_id" '#{session_id}' >/dev/null 2>&1; then
		return 0
	fi

	# Claim this window so any sibling pane-died promotions are suppressed.
	set_dedup "$window_id"

	# Collect all scratch files tagged with this window_id.
	local -a scratches=()
	local f
	shopt -s nullglob
	for f in "$SCRATCH_DIR"/*; do
		[[ -f $f ]] || continue
		local sc_window_id
		sc_window_id=$(grep -m1 '^window_id=' "$f" | cut -d= -f2-)
		[[ $sc_window_id == "$window_id" ]] && scratches+=("$f")
	done
	shopt -u nullglob

	if ((${#scratches[@]} == 0)); then
		log "capture_window: no scratches for $window_id — aborting"
		return 0
	fi

	# Pull shared fields from the first scratch.
	local first="${scratches[0]}"
	declare -A sc=()
	local k v
	while IFS='=' read -r k v; do sc["$k"]="$v"; done <"$first"

	# Look up the captured window's layout from the shadow index.
	local layout=""
	layout=$(shadow_lookup_win_layout "$session_id" "$window_id")

	# Emit the window entry.
	{
		printf 'level=window\n'
		printf 'timestamp=%s\n' "$(now_ms)"
		printf 'display_name=%s:%s\n' "${sc[session_name]}" "${sc[window_name]}"
		printf 'session_name=%s\n' "${sc[session_name]}"
		printf 'session_id=%s\n' "${sc[session_id]}"
		printf 'window_name=%s\n' "${sc[window_name]}"
		printf 'window_index=%s\n' "${sc[window_index]}"
		printf 'window_layout=%s\n' "$layout"
		printf 'pane_count=%d\n' "${#scratches[@]}"
		local i=0
		for f in "${scratches[@]}"; do
			local cwd
			cwd=$(grep -m1 '^cwd=' "$f" | cut -d= -f2-)
			printf 'pane_%d_cwd=%s\n' "$i" "$cwd"
			i=$((i + 1))
		done
	} | write_entry window >/dev/null

	# Consume the scratches.
	rm -f -- "${scratches[@]}"
}

# Read the shadow index for a session, extract the layout string for a given
# window_id. Empty string if not found.
shadow_lookup_win_layout() {
	local session_id="$1" window_id="$2"
	local shadow="$LIVE_DIR/${session_id#\$}"
	[[ -f $shadow ]] || return 0
	# Entries are numbered win_0_id, win_0_layout, win_1_id, ...
	awk -v target="$window_id" '
		BEGIN { FS="=" }
		$1 ~ /^win_[0-9]+_id$/ {
			match($1, /[0-9]+/); idx = substr($1, RSTART, RLENGTH)
			if ($2 == target) hit = idx
		}
		$1 ~ /^win_[0-9]+_layout$/ && hit != "" {
			match($1, /[0-9]+/); idx = substr($1, RSTART, RLENGTH)
			if (idx == hit) { print $2; exit }
		}
	' "$shadow"
}
```

- [ ] **Step 2: Wire the window-unlinked hook**

In `config/tmux.conf.nix`, add below the `pane-died[50]` hook from Task 4:

```tmux
    set-hook -g window-unlinked[51] 'run-shell "${script.tmux-undo}/bin/tmux-undo capture window #{hook_window} #{hook_session}"'
```

Note: `window-unlinked` provides `#{hook_window}` and `#{hook_session}` referring to the removed window and its (still-alive) session. Index `[51]` is chosen to run after the existing `window-unlinked` reflow hook; order doesn't functionally matter here but keeping reflow first is consistent.

- [ ] **Step 3: Verify**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

- [ ] **Step 4: Manual test**

Reload config. In an active session, create a window with two panes, then close it (`prefix+&` + confirm):

```fish
tmux new-window -n test-undo
# inside the new window: prefix+"  to split
# then Ctrl+D each pane until the window dies
ls /tmp/lazytmux-undo/*.window
cat /tmp/lazytmux-undo/*.window
# Check scratches were consumed
ls /tmp/lazytmux-undo/_scratch/
```

Expected: one `*.window` entry with `pane_count=2`, both pane cwds, and a `window_layout` string. Scratches dir empty.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): aggregate pane scratches into window entries"
```

---

## Task 6: Session capture — aggregate on session-closed

**Files:**
- Modify: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Implement capture_session**

Replace the `capture_session()` stub:

```bash
# Called on session-closed (session is already gone). Assembles a session entry
# from the shadow index (windows + layouts) + all scratch files tagged with
# this session_id (panes + cwds).
#
# Args from hook: $1 = session_id (dead)
capture_session() {
	local session_id="$1"
	prune_dedup

	# Claim this session to suppress any in-flight pane/window captures.
	set_dedup "$session_id"

	local shadow="$LIVE_DIR/${session_id#\$}"
	if [[ ! -f $shadow ]]; then
		log "capture_session: no shadow for $session_id — aborting"
		return 0
	fi

	# Load shadow.
	declare -A sh=()
	local k v
	while IFS='=' read -r k v; do sh["$k"]="$v"; done <"$shadow"

	# Gather scratches for this session, bucketed by window_id.
	declare -A win_panes_csv=()
	shopt -s nullglob
	local f
	for f in "$SCRATCH_DIR"/*; do
		[[ -f $f ]] || continue
		local sc_sid sc_wid sc_cwd
		sc_sid=$(grep -m1 '^session_id=' "$f" | cut -d= -f2-)
		[[ $sc_sid == "$session_id" ]] || continue
		sc_wid=$(grep -m1 '^window_id=' "$f" | cut -d= -f2-)
		sc_cwd=$(grep -m1 '^cwd=' "$f" | cut -d= -f2-)
		# Accumulate cwds for this window_id, TAB-separated.
		win_panes_csv["$sc_wid"]="${win_panes_csv[$sc_wid]:-}${win_panes_csv[$sc_wid]:+	}$sc_cwd"
	done
	shopt -u nullglob

	local win_count="${sh[window_count]:-0}"
	{
		printf 'level=session\n'
		printf 'timestamp=%s\n' "$(now_ms)"
		printf 'display_name=%s (%s windows)\n' "${sh[session_name]}" "$win_count"
		printf 'session_name=%s\n' "${sh[session_name]}"
		printf 'window_count=%s\n' "$win_count"
		local i
		for ((i = 0; i < win_count; i++)); do
			local w_id="${sh[win_${i}_id]:-}"
			local w_name="${sh[win_${i}_name]:-}"
			local w_index="${sh[win_${i}_index]:-}"
			local w_layout="${sh[win_${i}_layout]:-}"
			printf 'win_%d_name=%s\n' "$i" "$w_name"
			printf 'win_%d_index=%s\n' "$i" "$w_index"
			printf 'win_%d_layout=%s\n' "$i" "$w_layout"
			# Panes for this window, from the bucket.
			local cwds="${win_panes_csv[$w_id]:-}"
			local -a parts=()
			if [[ -n $cwds ]]; then
				IFS=$'\t' read -ra parts <<<"$cwds"
			fi
			printf 'win_%d_pane_count=%d\n' "$i" "${#parts[@]}"
			local j
			for ((j = 0; j < ${#parts[@]}; j++)); do
				printf 'win_%d_pane_%d_cwd=%s\n' "$i" "$j" "${parts[$j]}"
			done
		done
	} | write_entry session >/dev/null

	# Consume scratches and shadow.
	for f in "$SCRATCH_DIR"/*; do
		[[ -f $f ]] || continue
		local sc_sid
		sc_sid=$(grep -m1 '^session_id=' "$f" | cut -d= -f2-)
		[[ $sc_sid == "$session_id" ]] && rm -f -- "$f"
	done
	rm -f -- "$shadow"
}
```

- [ ] **Step 2: Wire session-closed hook**

Add to `config/tmux.conf.nix` below the window-unlinked undo hook:

```tmux
    set-hook -g session-closed[50] 'run-shell "${script.tmux-undo}/bin/tmux-undo capture session #{hook_session}"'
```

- [ ] **Step 3: Verify**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
```

- [ ] **Step 4: Manual test**

Reload config. Create a throwaway session with 2 windows, each with 2 panes, then kill the session:

```fish
tmux new-session -d -s undotest -c ~
tmux send-keys -t undotest 'cd /tmp' Enter
tmux new-window -t undotest -n second -c /var
tmux split-window -t undotest:second -c /etc
tmux kill-session -t undotest
ls /tmp/lazytmux-undo/*.session
cat /tmp/lazytmux-undo/*.session
```

Expected: one `*.session` entry with `window_count=2`, per-window layouts, and pane cwds per window.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): aggregate session close into single entry"
```

---

## Task 7: Dedup verification — cascades produce one entry

**Files:**
- No code changes; this task is a dedicated manual-test + bug-fix task.

- [ ] **Step 1: Manual test cascade dedup**

Reload config. Create a single-pane session, then kill it the way a `^D` cascade would fire all three hooks:

```fish
# From outside any tmux session, or from a different session:
tmux new-session -d -s casc -c ~
tmux kill-session -t casc    # triggers pane-died -> window-unlinked -> session-closed

ls -1 /tmp/lazytmux-undo/*.{pane,window,session} 2>/dev/null
```

Expected: exactly **one** `*.session` entry, no `*.window` or `*.pane` entries from this cascade.

- [ ] **Step 2: If duplicate entries appeared, diagnose**

If you see a `*.pane` or `*.window` entry alongside the session entry, check:

1. Hook order: session-closed must claim the `_dedup_<session_id>` marker before the surviving pane-died promotion runs. tmux does not guarantee hook ordering across events in a cascade — they all fire sequentially in the same event loop, but the order is emission order from tmux, not our hook index.
2. Fix: in `capture_pane_promote` and `capture_window`, add a short `sleep 0.05` before the `dedup_fresh` check to give session-closed time to write its marker.

If this sleep is required, add it as:

```bash
capture_pane_promote() {
	local pane_id="$1" window_id="$2" session_id="$3"
	# Brief yield so cascading window-unlinked / session-closed hooks can
	# mark dedup before we decide to emit.
	sleep 0.05
	prune_dedup
	if dedup_fresh "$window_id" || dedup_fresh "$session_id"; then
		return 0
	fi
	...
}
```

And similarly in `capture_window` before its `dedup_fresh` check.

- [ ] **Step 3: Re-test and confirm clean dedup**

Re-run the cascade test from Step 1. Expected: exactly one entry.

- [ ] **Step 4: Test non-cascade cases still capture**

```fish
# Window close (session survives): should produce exactly one *.window
tmux new-session -d -s keepme -c ~
tmux new-window -t keepme -n closeme
tmux kill-window -t keepme:closeme
ls /tmp/lazytmux-undo/*.window | tail -1
tmux kill-session -t keepme

# Pane close (window survives): should produce exactly one *.pane
tmux new-session -d -s panetest -c ~
tmux split-window -t panetest
tmux kill-pane -t panetest:.+
ls /tmp/lazytmux-undo/*.pane | tail -1
tmux kill-session -t panetest
```

Expected: each scenario produces its expected single entry.

- [ ] **Step 5: Commit (only if the sleep fix was needed)**

```fish
git add scripts/tmux-undo.sh
git commit -m "fix(undo): yield briefly so cascade dedup markers land first"
```

If no fix was needed, skip the commit.

---

## Task 8: Pop + restore pane

**Files:**
- Modify: `scripts/tmux-undo.sh`

- [ ] **Step 1: Implement pop + restore_pane**

Replace the `pop()` stub and add `restore_pane`:

```bash
# Pop the newest entry and restore it.
pop() {
	local -a entries
	mapfile -t entries < <(list_entries_sorted)
	local n=${#entries[@]}
	if ((n == 0)); then
		tmux display-message 'undo: stack is empty' 2>/dev/null
		return 0
	fi
	local newest="${entries[$((n - 1))]}"
	restore_entry "$newest"
	rm -f -- "$newest"
}

# Dispatch by entry level.
restore_entry() {
	local f="$1"
	declare -A e=()
	local k v
	while IFS='=' read -r k v; do e["$k"]="$v"; done <"$f"
	case "${e[level]:-}" in
	pane) restore_pane "$f" ;;
	window) restore_window "$f" ;;
	session) restore_session "$f" ;;
	*)
		log "restore_entry: unknown level in $f"
		mkdir -p "$CORRUPT_DIR"
		mv -- "$f" "$CORRUPT_DIR/"
		return 1
		;;
	esac
}

# Restore a pane entry. If parent window is alive, split it; else promote
# to a new window in the parent session.
restore_pane() {
	local f="$1"
	declare -A e=()
	local k v
	while IFS='=' read -r k v; do e["$k"]="$v"; done <"$f"

	local cwd="${e[pane_cwd]:-$HOME}"
	[[ -d $cwd ]] || cwd="$HOME"

	local target_window="${e[window_id]}"
	if tmux display-message -p -t "$target_window" '#{window_id}' >/dev/null 2>&1; then
		# Window lives → split it.
		local dir_flag="-v"
		[[ ${e[split_direction]:-} == h ]] && dir_flag="-h"
		tmux split-window "$dir_flag" -t "$target_window" -c "$cwd"
		tmux select-window -t "$target_window"
		tmux display-message -t "$target_window" "undo: pane restored ($cwd)"
		return 0
	fi

	# Window is gone → make a new window in the session if it still lives.
	if tmux display-message -p -t "${e[session_name]}" '#{session_name}' >/dev/null 2>&1; then
		tmux new-window -t "${e[session_name]}" -n "${e[window_name]}" -c "$cwd"
		tmux display-message "undo: pane restored as new window ($cwd)"
		return 0
	fi

	# Session also gone → make a new session.
	tmux new-session -d -s "${e[session_name]}" -n "${e[window_name]}" -c "$cwd"
	tmux switch-client -t "${e[session_name]}" 2>/dev/null || true
	tmux display-message "undo: pane restored as new session ($cwd)"
}
```

- [ ] **Step 2: Add stub restore_window / restore_session**

Add placeholders so `restore_entry` dispatch works for pop testing (Tasks 9-10 will fill):

```bash
restore_window() {
	log "restore_window: not implemented"
	return 1
}

restore_session() {
	log "restore_session: not implemented"
	return 1
}
```

- [ ] **Step 3: Wire prefix+u (temporary binding for testing)**

Add to `config/tmux.conf.nix` in the keybindings section (near `bind-key x kill-pane` at ~300):

```tmux
    bind-key u run-shell "${script.tmux-undo}/bin/tmux-undo pop"
```

- [ ] **Step 4: Verify + manual test**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
tmux source-file ~/.config/tmux/tmux.conf
```

In tmux:
1. Create a window with 2 panes.
2. Kill one pane (`prefix+x`, confirm).
3. Press `prefix+u`.

Expected: the killed pane is restored as a split in the same window, cwd preserved.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): add pop command and pane restore"
```

---

## Task 9: Restore window

**Files:**
- Modify: `scripts/tmux-undo.sh`

- [ ] **Step 1: Implement restore_window**

Replace the `restore_window()` stub:

```bash
# Restore a window entry: new-window in the session, split panes, apply layout.
# Falls back to new-session if the session is gone.
restore_window() {
	local f="$1"
	declare -A e=()
	local k v
	while IFS='=' read -r k v; do e["$k"]="$v"; done <"$f"

	local sess="${e[session_name]}"
	local wname="${e[window_name]}"
	local layout="${e[window_layout]:-}"
	local pcount="${e[pane_count]:-1}"
	local first_cwd="${e[pane_0_cwd]:-$HOME}"
	[[ -d $first_cwd ]] || first_cwd="$HOME"

	# Create-or-create-session.
	local target_session="$sess"
	if ! tmux display-message -p -t "$sess" '#{session_name}' >/dev/null 2>&1; then
		tmux new-session -d -s "$sess" -n "$wname" -c "$first_cwd"
		target_session="$sess"
		# First window already created with right name/cwd — skip new-window below.
	else
		tmux new-window -t "$target_session" -n "$wname" -c "$first_cwd"
	fi

	# Split additional panes.
	local i
	for ((i = 1; i < pcount; i++)); do
		local cwd="${e[pane_${i}_cwd]:-$HOME}"
		[[ -d $cwd ]] || cwd="$HOME"
		tmux split-window -t "$target_session:$wname" -c "$cwd"
	done

	# Apply captured layout if we have one and panes > 1.
	if [[ -n $layout && $pcount -gt 1 ]]; then
		tmux select-layout -t "$target_session:$wname" "$layout" 2>/dev/null ||
			log "restore_window: select-layout failed for $layout"
	fi

	tmux select-window -t "$target_session:$wname"
	tmux display-message "undo: window '$wname' restored"
}
```

- [ ] **Step 2: Verify + manual test**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
tmux source-file ~/.config/tmux/tmux.conf
```

In tmux:
1. Create a window with 2 panes split, cd each to different dirs.
2. Close the window entirely (kill all panes or `prefix+&`).
3. Press `prefix+u`.

Expected: window reappears with the same name, 2 panes, layout, and each pane's cwd.

- [ ] **Step 3: Commit**

```fish
git add scripts/tmux-undo.sh
git commit -m "feat(undo): restore windows with layout and pane cwds"
```

---

## Task 10: Restore session

**Files:**
- Modify: `scripts/tmux-undo.sh`

- [ ] **Step 1: Implement restore_session**

Replace the `restore_session()` stub:

```bash
# Restore a session entry. Recreates the session, windows, panes, layouts.
# Switches the current client to the new session.
restore_session() {
	local f="$1"
	declare -A e=()
	local k v
	while IFS='=' read -r k v; do e["$k"]="$v"; done <"$f"

	local sess="${e[session_name]}"
	local win_count="${e[window_count]:-0}"

	# Handle name conflict — suffix -restored, -restored-2, ...
	if tmux display-message -p -t "$sess" '#{session_name}' >/dev/null 2>&1; then
		local suffix=1
		while tmux display-message -p -t "${sess}-restored${suffix#1}" '#{session_name}' >/dev/null 2>&1; do
			suffix=$((suffix + 1))
		done
		if ((suffix == 1)); then
			sess="${sess}-restored"
		else
			sess="${sess}-restored-${suffix}"
		fi
	fi

	# Create session with first window's first pane.
	local w0_name="${e[win_0_name]:-main}"
	local w0_cwd="${e[win_0_pane_0_cwd]:-$HOME}"
	[[ -d $w0_cwd ]] || w0_cwd="$HOME"
	tmux new-session -d -s "$sess" -n "$w0_name" -c "$w0_cwd"

	# Add first window's remaining panes + layout.
	local w0_pcount="${e[win_0_pane_count]:-1}"
	local j
	for ((j = 1; j < w0_pcount; j++)); do
		local cwd="${e[win_0_pane[$j]_cwd]:-$HOME}"
		[[ -d $cwd ]] || cwd="$HOME"
		tmux split-window -t "$sess:$w0_name" -c "$cwd"
	done
	local w0_layout="${e[win_0_layout]:-}"
	if [[ -n $w0_layout && $w0_pcount -gt 1 ]]; then
		tmux select-layout -t "$sess:$w0_name" "$w0_layout" 2>/dev/null ||
			log "restore_session: layout failed for $w0_name"
	fi

	# Each subsequent window.
	local i
	for ((i = 1; i < win_count; i++)); do
		local wname="${e[win_${i}_name]:-}"
		local wlayout="${e[win_${i}_layout]:-}"
		local wpcount="${e[win_${i}_pane_count]:-1}"
		local w_first_cwd="${e[win_${i}_pane_0_cwd]:-$HOME}"
		[[ -d $w_first_cwd ]] || w_first_cwd="$HOME"
		tmux new-window -t "$sess" -n "$wname" -c "$w_first_cwd"
		for ((j = 1; j < wpcount; j++)); do
			local cwd="${e[win_${i}_pane[$j]_cwd]:-$HOME}"
			[[ -d $cwd ]] || cwd="$HOME"
			tmux split-window -t "$sess:$wname" -c "$cwd"
		done
		if [[ -n $wlayout && $wpcount -gt 1 ]]; then
			tmux select-layout -t "$sess:$wname" "$wlayout" 2>/dev/null ||
				log "restore_session: layout failed for $wname"
		fi
	done

	tmux switch-client -t "$sess" 2>/dev/null || true
	tmux display-message "undo: session '$sess' restored ($win_count windows)"
}
```

- [ ] **Step 2: Verify + manual test**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
tmux source-file ~/.config/tmux/tmux.conf
```

In tmux:
1. Create a throwaway session with 2 windows, each with 2 panes, different cwds.
2. `tmux kill-session -t <name>` from another client.
3. `prefix+u` in your surviving session.

Expected: the session reappears with both windows, layouts, and cwds. Client switches to it.

Test the name-conflict path: create session `foo`, close it, re-create `foo` manually, then `prefix+u`. Expected: entry restored as `foo-restored`.

- [ ] **Step 3: Commit**

```fish
git add scripts/tmux-undo.sh
git commit -m "feat(undo): restore full sessions with all windows and panes"
```

---

## Task 11: Picker + preview + final keybindings

**Files:**
- Modify: `scripts/tmux-undo.sh`
- Modify: `config/tmux.conf.nix`

- [ ] **Step 1: Implement picker + preview + formatting helpers**

Replace the `picker()` and `preview()` stubs and add formatters:

```bash
# Human relative time: "2m ago", "45s ago", "1h ago".
fmt_relative_ms() {
	local entry_ms="$1"
	local now_s elapsed
	now_s=$(date +%s)
	elapsed=$((now_s - entry_ms / 1000))
	if ((elapsed < 60)); then
		printf '%ds ago' "$elapsed"
	elif ((elapsed < 3600)); then
		printf '%dm ago' $((elapsed / 60))
	elif ((elapsed < 86400)); then
		printf '%dh ago' $((elapsed / 3600))
	else
		printf '%dd ago' $((elapsed / 86400))
	fi
}

level_icon() {
	case "$1" in
	session) printf '' ;;    # nerd: nf-md-view_dashboard
	window) printf '' ;;     # nerd: nf-cod-window
	pane) printf '' ;;       # nerd: nf-cod-terminal
	*) printf '?' ;;
	esac
}

# One fzf line per entry: "<file>\t<icon> <level>  <display_name>  <rel_time>"
# The TAB-prefixed file path is hidden from display via fzf --with-nth.
picker_list() {
	local -a entries
	mapfile -t entries < <(list_entries_sorted)
	# Reverse: newest first.
	local i
	for ((i = ${#entries[@]} - 1; i >= 0; i--)); do
		local f="${entries[$i]}"
		local level display ts
		level=$(grep -m1 '^level=' "$f" | cut -d= -f2-)
		display=$(grep -m1 '^display_name=' "$f" | cut -d= -f2-)
		ts=$(grep -m1 '^timestamp=' "$f" | cut -d= -f2-)
		printf '%s	%s  %-7s  %-50s  %s\n' "$f" "$(level_icon "$level")" "$level" "$display" "$(fmt_relative_ms "$ts")"
	done
}

picker() {
	local list
	list=$(picker_list)
	if [[ -z $list ]]; then
		tmux display-message 'undo: stack is empty'
		return 0
	fi

	# Run fzf inside a tmux popup. On selection, restore and clean up.
	local self
	self=$(readlink -f -- "$0" 2>/dev/null || printf '%s' "$0")
	local selected
	selected=$(printf '%s' "$list" | fzf \
		--ansi \
		--delimiter='	' \
		--with-nth=2.. \
		--preview="$self preview {1}" \
		--preview-window=right:50% \
		--prompt='undo > ' \
		--header='enter=restore  esc=cancel' \
		|| true)

	[[ -n $selected ]] || return 0
	local f
	f=$(printf '%s' "$selected" | cut -f1)
	[[ -f $f ]] || return 0
	restore_entry "$f"
	rm -f -- "$f"
}

# Called as `tmux-undo preview <entry_file>`. Prints a human-readable tree.
preview() {
	local f="${1:-}"
	[[ -f $f ]] || {
		printf 'entry not found: %s\n' "$f"
		return 0
	}
	declare -A e=()
	local k v
	while IFS='=' read -r k v; do e["$k"]="$v"; done <"$f"
	local level="${e[level]:-?}"
	printf 'Level:      %s\n' "$level"
	printf 'When:       %s\n' "$(fmt_relative_ms "${e[timestamp]:-0}")"
	printf 'Display:    %s\n' "${e[display_name]:-}"
	printf '\n'
	case "$level" in
	pane)
		printf 'Session:    %s\n' "${e[session_name]}"
		printf 'Window:     %s (idx %s)\n' "${e[window_name]}" "${e[window_index]}"
		printf 'CWD:        %s\n' "${e[pane_cwd]}"
		printf 'Split dir:  %s\n' "${e[split_direction]}"
		;;
	window)
		printf 'Session:    %s\n' "${e[session_name]}"
		printf 'Window:     %s (idx %s)\n' "${e[window_name]}" "${e[window_index]}"
		printf 'Panes:      %s\n' "${e[pane_count]}"
		local i
		for ((i = 0; i < ${e[pane_count]:-0}; i++)); do
			printf '  [%d]  %s\n' "$i" "${e[pane_${i}_cwd]}"
		done
		printf 'Layout:     %s\n' "${e[window_layout]}"
		;;
	session)
		printf 'Session:    %s\n' "${e[session_name]}"
		printf 'Windows:    %s\n' "${e[window_count]}"
		local i j
		for ((i = 0; i < ${e[window_count]:-0}; i++)); do
			printf '  %s (idx %s, %s panes)\n' "${e[win_${i}_name]}" "${e[win_${i}_index]}" "${e[win_${i}_pane_count]}"
			for ((j = 0; j < ${e[win_${i}_pane_count]:-0}; j++)); do
				printf '     [%d]  %s\n' "$j" "${e[win_${i}_pane[$j]_cwd]}"
			done
		done
		;;
	esac
}
```

- [ ] **Step 2: Make `fzf` available as a placeholder**

The picker uses `fzf` directly, not the existing `@fzf@` placeholder pattern. Since `tmux-undo.sh` is **not** in `scriptsWithIcons`, placeholders aren't substituted in it. To access fzf reliably, we have two options:

1. Add the script to `scriptsWithIcons` (would pull in irrelevant substitutions).
2. Add a dedicated placeholder set for this script.

Use option 2. Edit `config/tmux.conf.nix`:

Below the existing `mkScriptFull` (around line 130), add a new builder:

```nix
  mkScriptWithFzf = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      ["@fzf@"]
      ["${pkgs.fzf}/bin/fzf"]
      raw;
  in
    pkgs.writeShellScriptBin name patched;
```

Then extend the `script` attrset dispatch:

```nix
  script = lib.genAttrs scriptNames (name:
    if builtins.elem name scriptsWithIcons
    then mkScriptFull name
    else if name == "claude-status"
    then claude-status-pkg
    else if name == "tmux-undo"
    then mkScriptWithFzf name
    else mkScript name);
```

And in `scripts/tmux-undo.sh`, replace the bare `fzf` invocation with `@fzf@`:

```bash
selected=$(printf '%s' "$list" | @fzf@ \
  ...
```

- [ ] **Step 3: Wire prefix+U in popup**

In `config/tmux.conf.nix`, below the existing `bind-key u` from Task 8, add:

```tmux
    bind-key U display-popup -E -w 80% -h 60% "${script.tmux-undo}/bin/tmux-undo picker"
```

- [ ] **Step 4: Verify + manual test**

```fish
shellcheck scripts/tmux-undo.sh
nix build .
tmux source-file ~/.config/tmux/tmux.conf
```

1. Close a few different things (a pane, a window, a session).
2. `prefix+U` — expect a popup with 3 entries, newest first, icons + relative times.
3. Arrow down to preview each — the right pane shows tree detail.
4. Enter on one — it restores and popup closes.

- [ ] **Step 5: Commit**

```fish
git add scripts/tmux-undo.sh config/tmux.conf.nix
git commit -m "feat(undo): add picker and preview via fzf popup"
```

---

## Task 12: Docs — update CLAUDE.md Script Roles table

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add row to the Script Roles table**

Edit `CLAUDE.md`. Find the Script Roles table (under "### Script Roles"). Add a new row at the bottom:

```markdown
| `tmux-undo` | `prefix+u` / `prefix+U`, and `pane-died` / `window-unlinked` / `session-closed` hooks | Captures closed panes/windows/sessions into a small stack at `/tmp/lazytmux-undo/` (cap 10). `prefix+u` pops newest; `prefix+U` opens an fzf picker. Running processes are not restored — only structure + cwds. |
```

- [ ] **Step 2: Verify spec + plan references still resolve**

Grep to confirm:

```fish
grep -c tmux-undo CLAUDE.md
grep -c tmux-undo config/tmux.conf.nix
```

Expected: `CLAUDE.md` contains 1 match; `tmux.conf.nix` contains multiple (script registration, hooks, keybindings).

- [ ] **Step 3: Final full-build smoke test**

```fish
nix flake check
nix build .
shellcheck scripts/tmux-undo.sh
tmux source-file ~/.config/tmux/tmux.conf
```

- [ ] **Step 4: Commit**

```fish
git add CLAUDE.md
git commit -m "docs: add tmux-undo to script roles table"
```

---

## Post-Implementation Verification Checklist

Run all these in a live tmux with the reloaded config. Each should pass.

- [ ] `^D` in a single-pane window → `prefix+u` → window reappears with same name/cwd.
- [ ] `^D` in the last pane of the last window of a session → `prefix+u` → full session reappears.
- [ ] `prefix+x` (kill-pane) on one of two panes → `prefix+u` → pane restored as split.
- [ ] `prefix+U` shows all recent closures, newest first, preview works.
- [ ] Stack cap: close 11 things, verify oldest dropped (`ls /tmp/lazytmux-undo/*.{pane,window,session} | wc -l` ≤ 10).
- [ ] Cascade dedup: killing a session produces exactly one `*.session` entry (no stray `*.pane`/`*.window`).
- [ ] Name conflict: close `foo`, recreate `foo`, `prefix+u` → restored as `foo-restored`.
- [ ] Dead cwd: edit an entry file to point `pane_cwd=` to a non-existent path, restore → falls back to `$HOME`, tmux message shown.
- [ ] `shellcheck scripts/tmux-undo.sh` — clean.
- [ ] `nix flake check` — clean.
- [ ] Stale `/tmp/lazytmux-undo/_log` is empty or only has expected info messages.
