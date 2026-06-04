# Adaptive Window Labels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Window tabs show the full ticket/branch title when there's room and shrink to the bare id when windows pile up, with one consistent label source across single- and multi-line modes.

**Architecture:** A pure shell helper (`build_window_label` in `lib-enrich.sh`) composes a text-only short/long label per window. `tmux-reflow-windows` (the event-driven layout script) measures both variants with a new `measure_display_width` helper, decides a session-wide `@labels_mode` (`long` if all-long fits the available lines, else `active`), and sets `@window_label_short`/`@window_label_long` per window. The status templates select live via `@labels_mode` + `window_active` and append the existing 1s-fresh `@window_icon_*` separately, so process/claude icons never freeze. See spec: `docs/superpowers/specs/2026-06-04-adaptive-window-labels-design.md`.

**Tech Stack:** Bash (REPLY-convention libs), tmux format strings, Nix (`writeShellScript`/`writeShellScriptBin` placeholder substitution), bats tests.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `scripts/lib-icons.sh` | width measurement helpers | **Add** `measure_display_width` |
| `scripts/lib-enrich.sh` | enrich pure logic + glyph constants | **Add** `ENRICH_ICON_*` constants + `build_window_label` |
| `config/tmux.conf.nix` | Nix build + tmux config | **Modify** `lib-enrich` substitution (glyphs), `mkScriptFull` (`@lib_enrich@`), `mkScriptEnrich` (`@reflow@`), single-line status-format[1], `automatic-rename-format` |
| `scripts/tmux-reflow-windows.sh` | layout + label allocation | **Modify** read enrich vars, build/measure labels, set vars + mode, natural-width packing, `--force`, cache key |
| `scripts/tmux-issue-stamp.sh` | issue stamping | **Modify** call reflow `--force` after writing vars |
| `scripts/tmux-pr-enrich.sh` | PR poller | **Modify** call reflow `--force` after writing vars |
| `tests/helper.bash` | bats setup | **Add** `setup_lib_icons` |
| `tests/icons.bats` | unit tests | **Create** `measure_display_width` tests |
| `tests/enrich.bats` | unit tests | **Add** `build_window_label` tests |

---

## Task 1: `measure_display_width` helper

**Files:**
- Modify: `scripts/lib-icons.sh` (append after `pad_to_width`, ~line 93)
- Modify: `tests/helper.bash`
- Test: `tests/icons.bats` (create)

- [ ] **Step 1: Add a `setup_lib_icons` bats helper**

In `tests/helper.bash`, after the existing `setup_lib_enrich` function, add:

```bash
setup_lib_icons() {
	local tmp
	tmp="$(mktemp)"
	# Stub the @ICON_MAP@ / @FALLBACK_ICON@ Nix placeholders so the file sources.
	sed -e 's/@ICON_MAP@//' -e 's/@FALLBACK_ICON@//' scripts/lib-icons.sh >"$tmp"
	# shellcheck source=/dev/null
	source "$tmp"
	rm -f "$tmp"
}
```

- [ ] **Step 2: Write the failing test**

Create `tests/icons.bats`:

```bash
#!/usr/bin/env bats

load helper

setup() {
	setup_lib_icons
}

@test "measure_display_width: pure ASCII counts one cell each" {
	measure_display_width "ENG-1957"
	[ "$REPLY_DW" = "8" ]
}

@test "measure_display_width: nerd font PUA glyph is one cell" {
	# U+F0C0D md-alpha_l_circle
	measure_display_width $'\Uf0c0d'
	[ "$REPLY_DW" = "1" ]
}

@test "measure_display_width: supplementary-plane emoji is two cells" {
	measure_display_width $'\U0001f9e0' # 🧠
	[ "$REPLY_DW" = "2" ]
}

@test "measure_display_width: glyph + space + ascii sums correctly" {
	# U+F0C0D (1) + space (1) + "ENG-1957" (8) = 10
	measure_display_width $'\Uf0c0d ENG-1957'
	[ "$REPLY_DW" = "10" ]
}

@test "measure_display_width: empty string is zero" {
	measure_display_width ""
	[ "$REPLY_DW" = "0" ]
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `bats tests/icons.bats`
Expected: FAIL — `measure_display_width: command not found`.

- [ ] **Step 4: Implement `measure_display_width`**

Append to `scripts/lib-icons.sh`:

```bash
# measure_display_width STRING
# Computes the terminal display width of STRING: ASCII = 1 cell, non-ASCII via
# _icon_cell_width (matches tmux's measurement of nerd/emoji glyphs).
# Sets REPLY_DW to the integer width.
measure_display_width() {
	local str="$1" ch
	local -i i cp w=0
	for ((i = 0; i < ${#str}; i++)); do
		ch="${str:i:1}"
		printf -v cp '%d' "'$ch"
		if ((cp < 128)); then
			((w += 1))
		else
			_icon_cell_width "$ch"
			((w += _ICW))
		fi
	done
	REPLY_DW=$w
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `bats tests/icons.bats`
Expected: PASS (5 tests).

- [ ] **Step 6: Commit**

```bash
git add scripts/lib-icons.sh tests/helper.bash tests/icons.bats
git commit -m "feat(icons): add measure_display_width helper"
```

---

## Task 2: `build_window_label` + glyph constants

**Files:**
- Modify: `scripts/lib-enrich.sh` (add constants near top, function at end)
- Modify: `config/tmux.conf.nix:109-113` (lib-enrich substitution)
- Test: `tests/enrich.bats`, `tests/helper.bash`

- [ ] **Step 1: Add glyph constants to `lib-enrich.sh`**

In `scripts/lib-enrich.sh`, after the `ENRICH_CACHE_DIR` line (~line 9), add:

```bash
# Enrich glyphs (substituted at Nix build time from enrichIconSetRaw). Text-only;
# the status template applies color and appends process/claude icons separately.
ENRICH_ICON_LINEAR="@enrich_icon_linear@"
ENRICH_ICON_GITHUB="@enrich_icon_github@"
ENRICH_ICON_PENDING="@enrich_icon_pending@"
ENRICH_ICON_SUCCESS="@enrich_icon_success@"
ENRICH_ICON_FAILURE="@enrich_icon_failure@"
ENRICH_ICON_MERGED="@enrich_icon_merged@"
```

- [ ] **Step 2: Extend `setup_lib_enrich` to stub the new placeholders**

In `tests/helper.bash`, change the `setup_lib_enrich` `sed` (currently
`sed 's/@providers@/linear github/g' scripts/lib-enrich.sh >"$tmp"`) to also stub
the glyphs with single-letter sentinels so assertions are readable:

```bash
setup_lib_enrich() {
	local tmp
	tmp="$(mktemp)"
	sed \
		-e 's/@providers@/linear github/g' \
		-e 's/@enrich_icon_linear@/L/g' \
		-e 's/@enrich_icon_github@/G/g' \
		-e 's/@enrich_icon_pending@/P/g' \
		-e 's/@enrich_icon_success@/S/g' \
		-e 's/@enrich_icon_failure@/F/g' \
		-e 's/@enrich_icon_merged@/M/g' \
		scripts/lib-enrich.sh >"$tmp"
	# shellcheck source=/dev/null
	source "$tmp"
	rm -f "$tmp"
}
```

(If `setup_lib_enrich` currently spans multiple lines, replace its whole body with the above. Keep any existing trailing logic.)

- [ ] **Step 3: Write the failing tests**

Append to `tests/enrich.bats`:

```bash
@test "build_window_label: enriched short = provider id" {
	build_window_label short linear ENG-1957 "refactor services" "" "" "" feat/eng-1957 /x
	[ "$REPLY" = "L ENG-1957" ]
}

@test "build_window_label: enriched long = provider id title" {
	build_window_label long linear ENG-1957 "refactor services" "" "" "" feat/eng-1957 /x
	[ "$REPLY" = "L ENG-1957 refactor services" ]
}

@test "build_window_label: enriched with failing PR adds glyph in both modes" {
	build_window_label short github 247 "fix bug" 247 OPEN failure gh-247 /x
	[ "$REPLY" = "G 247 F" ]
	build_window_label long github 247 "fix bug" 247 OPEN failure gh-247 /x
	[ "$REPLY" = "G 247 F fix bug" ]
}

@test "build_window_label: merged PR uses merged glyph" {
	build_window_label short linear ENG-1 "t" 9 merged success br /x
	[ "$REPLY" = "L ENG-1 M" ]
}

@test "build_window_label: pr_number=none is treated as no PR" {
	build_window_label short linear ENG-1 "t" none "" "" br /x
	[ "$REPLY" = "L ENG-1" ]
}

@test "build_window_label: long with empty title falls back to short form" {
	build_window_label long linear ENG-1 "" "" "" "" br /x
	[ "$REPLY" = "L ENG-1" ]
}

@test "build_window_label: plain short = branch basename" {
	build_window_label short "" "" "" "" "" "" feature/fix-login /x
	[ "$REPLY" = "fix-login" ]
}

@test "build_window_label: plain long = full branch" {
	build_window_label long "" "" "" "" "" "" feature/fix-login /x
	[ "$REPLY" = "feature/fix-login" ]
}

@test "build_window_label: no branch falls back to dir basename" {
	build_window_label long "" "" "" "" "" "" "" /home/noams/proj
	[ "$REPLY" = "proj" ]
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `bats tests/enrich.bats`
Expected: the new `build_window_label` tests FAIL — `command not found`.

- [ ] **Step 5: Implement `build_window_label`**

Append to `scripts/lib-enrich.sh`:

```bash
# build_window_label MODE PROVIDER ISSUE_ID ISSUE_TITLE PR_NUMBER PR_STATE \
#                    PR_CHECK_STATE BRANCH PANE_PATH
# MODE is "short" or "long". Composes the text-only window label (no color, no
# process/claude icons — the status template adds those). Enriched windows show
# "<provider> <id>[ <pr-glyph>][ <title>]"; plain windows show branch (long=full,
# short=basename) or the directory basename. Sets REPLY.
build_window_label() {
	local mode="$1" provider="$2" issue_id="$3" issue_title="$4"
	local pr_number="$5" pr_state="$6" pr_check="$7" branch="$8" pane_path="$9"
	local provider_icon pr_icon=""

	if [[ -n $issue_id ]]; then
		if [[ $provider == "linear" ]]; then
			provider_icon="$ENRICH_ICON_LINEAR"
		else
			provider_icon="$ENRICH_ICON_GITHUB"
		fi
		if [[ -n $pr_number && $pr_number != "none" ]]; then
			case "$pr_check" in
			failure) pr_icon=" $ENRICH_ICON_FAILURE" ;;
			pending) pr_icon=" $ENRICH_ICON_PENDING" ;;
			*)
				if [[ $pr_state == "merged" ]]; then
					pr_icon=" $ENRICH_ICON_MERGED"
				else
					pr_icon=" $ENRICH_ICON_SUCCESS"
				fi
				;;
			esac
		fi
		if [[ $mode == "long" && -n $issue_title ]]; then
			REPLY="${provider_icon} ${issue_id}${pr_icon} ${issue_title}"
		else
			REPLY="${provider_icon} ${issue_id}${pr_icon}"
		fi
	elif [[ -n $branch ]]; then
		if [[ $mode == "long" ]]; then
			REPLY="$branch"
		else
			REPLY="${branch##*/}"
		fi
	else
		REPLY="${pane_path##*/}"
	fi
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `bats tests/enrich.bats`
Expected: PASS (existing + 9 new).

- [ ] **Step 7: Wire glyph substitution into the `lib-enrich` derivation**

In `config/tmux.conf.nix`, immediately before the `lib-enrich = let` block (line 109), add the raw (un-escaped) icon set:

```nix
  # Shell label builder needs raw glyphs (single '#'); the tmux-format path uses
  # the '##'-escaped enrichIconSet. Reverse the module's escaping for shell use.
  enrichIconSetRaw = builtins.mapAttrs (_: v: builtins.replaceStrings ["##"] ["#"] v) enrichIconSet;
```

Then replace the `lib-enrich` block (lines 109-113) with:

```nix
  lib-enrich = let
    raw = builtins.readFile ../scripts/lib-enrich.sh;
    patched =
      builtins.replaceStrings
      [
        "@providers@"
        "@enrich_icon_linear@"
        "@enrich_icon_github@"
        "@enrich_icon_pending@"
        "@enrich_icon_success@"
        "@enrich_icon_failure@"
        "@enrich_icon_merged@"
      ]
      [
        enrichProvidersStr
        enrichIconSetRaw.linear
        enrichIconSetRaw.github
        enrichIconSetRaw.pending
        enrichIconSetRaw.success
        enrichIconSetRaw.failure
        enrichIconSetRaw.merged
      ]
      raw;
  in
    pkgs.writeShellScript "lib-enrich" patched;
```

Note: `enrichIconSet`/`enrichIconSetRaw` are defined later in the file (line ~69). Nix `let` bindings are order-independent, so this forward reference is fine.

- [ ] **Step 8: Verify the libraries still build**

Run: `nix build .#default 2>&1 | tail -3`
Expected: builds with `rc=0` (substitution well-formed).

- [ ] **Step 9: Commit**

```bash
git add scripts/lib-enrich.sh config/tmux.conf.nix tests/enrich.bats tests/helper.bash
git commit -m "feat(enrich): add build_window_label + glyph constants"
```

---

## Task 3: Source `lib-enrich` from `tmux-reflow-windows`

**Files:**
- Modify: `config/tmux.conf.nix:160-166` (`mkScriptFull` substitution list)
- Modify: `scripts/tmux-reflow-windows.sh:11-12` (source line)

- [ ] **Step 1: Add `@lib_enrich@` to `mkScriptFull`**

In `config/tmux.conf.nix`, in `mkScriptFull` (line 160), add `@lib_enrich@` →
`${lib-enrich}` to the two `replaceStrings` lists. The search list becomes:

```nix
      ["@lib_icons@" "@lib_claude@" "@lib_enrich@" "claude-status " "@claude_status_bin@" "@ICON_MAP@" "@FALLBACK_ICON@" "@MAX_ICONS@" "@MAX_ICONS_PICKER@" "@fzf@" "@picker_generate@"]
```

and the replacement list:

```nix
      ["${lib-icons}" "${lib-claude}" "${lib-enrich}" claude-status-bin "${claude-status-bin} " iconMapBash fallbackIcon maxIcons maxIconsPicker "${pkgs.fzf}/bin/fzf" picker-generate-bin]
```

> CAUTION: the replacement order must match the search order exactly. The existing
> pairs are `claude-status ` → `"${claude-status-bin} "` and `@claude_status_bin@`
> → `claude-status-bin`. Insert `"${lib-enrich}"` in the **third** position
> (after `${lib-claude}`) in BOTH lists and change nothing else. Re-verify by
> eye that every other element stays paired.

- [ ] **Step 2: Source it in the reflow script**

In `scripts/tmux-reflow-windows.sh`, after the `source @lib_icons@` block
(line 11-12), add:

```bash
# shellcheck source=/dev/null
source @lib_enrich@
```

- [ ] **Step 3: Verify build + correct substitution**

Run:
```bash
nix build .#default 2>&1 | tail -2
reflow=$(nix eval --raw .#packages.x86_64-linux.default 2>/dev/null) || true
```
Then confirm the built reflow script has no leftover placeholder:
```bash
grep -l 'source /nix/store/.*-lib-enrich' $(find /nix/store -maxdepth 4 -name tmux-reflow-windows -type f 2>/dev/null | head -1) && echo OK
```
Expected: `OK` (the `@lib_enrich@` was replaced with a store path); build `rc=0`.

- [ ] **Step 4: Commit**

```bash
git add config/tmux.conf.nix scripts/tmux-reflow-windows.sh
git commit -m "build(reflow): source lib-enrich for label building"
```

---

## Task 4: Reflow — read enrich vars, build labels, set vars + mode

This task rewrites the data-collection and var-setting of `tmux-reflow-windows.sh`. The packing loop is rewritten in Task 5; here we only (a) parse `--force`, (b) extend the cache key with the active window index, (c) read enrich vars per window, (d) build short/long labels + measure widths, (e) set `@window_label_short`/`@window_label_long` + session `@labels_mode`.

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh`

- [ ] **Step 1: Parse `--force` and extend the cache key**

Replace lines 14-28 (the arg parsing + cache fast-path) with:

```bash
# Accept --force (cache bypass, used by enrich scripts after writing vars).
FORCE=0
pos=()
for a in "$@"; do
	if [[ $a == --force ]]; then
		FORCE=1
	else
		pos+=("$a")
	fi
done
set -- "${pos[@]}"

# Accept session/width as args (from hooks) or fall back to display-message
SESSION=${1:-$(tmux display-message -p '#{session_name}')}
WIDTH=${2:-$(tmux display-message -p '#{client_width}')}
MAX_ICONS=@MAX_ICONS@

# Scratch sessions manage their own status bar (hints bar); skip reflow.
case "$SESSION" in
scratch-*) exit 0 ;;
esac

# Fast-path: skip if window count + width + active window unchanged since last
# reflow. Active window is in the key so focus changes re-pack multi-line active
# mode (where the active tab's long width shifts split points).
active_win=$(tmux display-message -t "$SESSION" -p '#{window_index}')
cache_key="$(tmux display-message -t "$SESSION" -p '#{session_windows}'):${WIDTH}:${active_win}"
if ((!FORCE)) && [[ $cache_key == "$(tmux display-message -t "$SESSION" -p '#{@reflow_key}' 2>/dev/null)" ]]; then
	exit 0
fi
```

- [ ] **Step 2: Read enrich vars in the window-data pass**

Replace the `FMT='…'` line (currently line 41) and its `while` loop header
(line 42) with a version that also pulls enrich + active vars:

```bash
FMT='#{window_index}|#{window_active}|#{@branch}|#{pane_current_path}|#{window_zoomed_flag}|#{@issue_provider}|#{@issue_id}|#{@issue_title}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}'
declare -A win_short win_long win_short_dw win_long_dw
active_idx=""
while IFS='|' read -r idx wactive branch pane_path zoomed iprov iid ititle prnum prstate prcheck; do
	indices+=("$idx")
	((zoomed)) && has_zoom=1
	((wactive)) && active_idx="$idx"

	build_window_label short "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path"
	win_short[$idx]="$REPLY"
	measure_display_width "$REPLY"
	win_short_dw[$idx]=$REPLY_DW

	build_window_label long "$iprov" "$iid" "$ititle" "$prnum" "$prstate" "$prcheck" "$branch" "$pane_path"
	win_long[$idx]="$REPLY"
	measure_display_width "$REPLY"
	win_long_dw[$idx]=$REPLY_DW

	((total++))
done < <(tmux list-windows -t "$SESSION" -F "$FMT")
```

Then DELETE the now-unused text-length bookkeeping that the old loop did
(the `win_text_len`, `max_text_len`, `capped`, `has_truncated` logic — old lines
35-39 declarations and 46-58 inside the loop). Label widths replace them. Keep
`declare -a indices`, `total=0`, `has_zoom=0`.

- [ ] **Step 3: Set label + mode vars in the batched command block**

In the batched `tmux_cmds` section (old lines 148-151 set `@window_icon_padded`),
add label vars in the same loop. Replace the icon-padding loop with:

```bash
# Per-window: padded icon (unchanged) + short/long labels (new).
for idx in "${indices[@]}"; do
	tmux_cmds+=("set -w -t '${SESSION}:${idx}' @window_icon_padded '${win_icon_str[$idx]}'")
	tmux_cmds+=("set -w -t '${SESSION}:${idx}' @window_label_short '${win_short[$idx]}'")
	tmux_cmds+=("set -w -t '${SESSION}:${idx}' @window_label_long '${win_long[$idx]}'")
done
```

The `@labels_mode` session var is set in Task 5 (it depends on the packing
decision). For now add a placeholder right after the split-point sets
(old line 156, after `@reflow_key`):

```bash
tmux_cmds+=("set -t '$SESSION' @labels_mode '${labels_mode:-active}'")
```

- [ ] **Step 4: Build to confirm the script is still valid bash**

Run: `nix build .#default 2>&1 | tail -2`
Expected: `rc=0`. (Behavioral correctness is verified in Task 8; this is a
syntax/wiring gate.)

- [ ] **Step 5: Commit**

```bash
git add scripts/tmux-reflow-windows.sh
git commit -m "feat(reflow): read enrich vars, set window_label_short/long"
```

---

## Task 5: Reflow — mode decision + natural-width packing

Rewrite the width/packing math to use per-window chosen widths and set
`@labels_mode`. The old code (lines 75-143 region) used a uniform padded
`slot_base`; replace with natural widths.

**Files:**
- Modify: `scripts/tmux-reflow-windows.sh`

- [ ] **Step 1: Replace the width-summing + split-point computation**

Replace the block from the icon-string build through split-point computation
(old lines 75-143) with the following. It computes each window's short/long
**slot width** (label + icon column + per-window overhead), decides
`labels_mode`, then greedily packs the **chosen** widths into up to 3 lines.

```bash
# --- Build icon strings with fixed-width padding (stable across icon changes) ---
max_icon_width=$((MAX_ICONS * 3 + 2))
declare -A win_icon_str
for idx in "${indices[@]}"; do
	build_proc_icons "${win_procs[$idx]:-}" "$MAX_ICONS"
	pad_to_width "$REPLY" "$REPLY_DW" "$max_icon_width"
	win_icon_str[$idx]="$REPLY"
done

# --- Per-window slot widths ---
# Slot = idx_width + ": "(2) + label + " "(1) + icon column.
last_idx=${indices[$((total - 1))]}
idx_width=${#last_idx}
overhead=$((idx_width + 3 + max_icon_width)) # ": " + trailing space + icons
declare -A short_slot long_slot
for idx in "${indices[@]}"; do
	short_slot[$idx]=$((win_short_dw[$idx] + overhead))
	long_slot[$idx]=$((win_long_dw[$idx] + overhead))
done

available=$((WIDTH - PREFIX_WIDTH))
zoom_extra=0
((has_zoom)) && zoom_extra=2
SEP_WIDTH=3 # " │ "
MAX_WIN_LINES=3

# pack_lines NAME-of-slot-array  → echoes the line count needed (1..N)
# Greedy first-fit; SEP between items on a line.
pack_count() {
	local -n _slot=$1
	local lines=1 cur=0 first=1
	for idx in "${indices[@]}"; do
		local w=${_slot[$idx]}
		if ((first)); then
			cur=$w
			first=0
		elif ((cur + SEP_WIDTH + w > available)); then
			((lines++))
			cur=$w
		else
			cur=$((cur + SEP_WIDTH + w))
		fi
	done
	echo "$lines"
}

# Mode decision: all-long if it fits the allowed lines, else active.
long_lines=$(pack_count long_slot)
if ((long_lines <= MAX_WIN_LINES)); then
	labels_mode=long
else
	labels_mode=active
fi

# Chosen slot per window for the actual packing.
declare -A chosen_slot
for idx in "${indices[@]}"; do
	if [[ $labels_mode == long ]] || [[ $idx == "$active_idx" ]]; then
		chosen_slot[$idx]=${long_slot[$idx]}
	else
		chosen_slot[$idx]=${short_slot[$idx]}
	fi
done

# Single-line check using chosen widths.
total_single=0
for idx in "${indices[@]}"; do
	((total_single += chosen_slot[$idx]))
done
total_single=$((total_single + (total - 1) * SEP_WIDTH))

needs_multiline=0
((total_single + zoom_extra > available)) && needs_multiline=1

# Greedy split points using chosen widths.
current_line=0
split1=999
split2=999
if ((needs_multiline)); then
	cumulative=0
	prev_idx=
	for ((j = 0; j < total; j++)); do
		idx=${indices[$j]}
		w=${chosen_slot[$idx]}
		if ((cumulative == 0)); then
			cumulative=$w
		elif ((cumulative + SEP_WIDTH + w > available)); then
			((current_line++))
			if ((current_line == 1)); then
				split1=$prev_idx
			elif ((current_line == 2)); then
				split2=$prev_idx
				break
			fi
			cumulative=$w
		else
			cumulative=$((cumulative + SEP_WIDTH + w))
		fi
		prev_idx=$idx
	done
fi
```

- [ ] **Step 2: Confirm `@labels_mode` is set from the computed value**

The Task 4 line `tmux_cmds+=("set -t '$SESSION' @labels_mode '${labels_mode:-active}'")`
now picks up the real `labels_mode`. No change needed if it sits **after** this
block in execution order — verify the `@labels_mode` set line comes after the
mode decision (move it down with the other `set -t '$SESSION' …` lines if not).

- [ ] **Step 3: Replace the multi-line format fragments (drop padding)**

Replace the format-fragment block (old lines 184-193) with natural-width
versions that select the label live and append the icon:

```bash
# Common format fragments
SEP=" #[fg=#{@thm_subtext_0}#,nobold]│ "
ICON='#{@window_icon_padded}'
# Live label selection: long mode → long; active mode → long iff active else short.
LABEL='#{?#{==:#{@labels_mode},long},#{@window_label_long},#{?window_active,#{@window_label_long},#{@window_label_short}}}'
LABEL_Z="${LABEL}#{?window_zoomed_flag, 󰁌,}"
IDX="#{p${idx_width}:window_index}"
ENTRY="#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]${IDX}: ${LABEL_Z} ${ICON},#[fg=#{@thm_subtext_0}#,nobold]${IDX}: #[fg=#{@thm_fg}]${LABEL_Z} ${ICON}}#[norange]"
```

The downstream `status-format[1..3]` assignments (old lines 198-220) reference
`${ENTRY}` and `${SEP}` and need **no further change** — they now render natural
widths automatically.

- [ ] **Step 4: shellcheck + build**

Run:
```bash
shellcheck scripts/tmux-reflow-windows.sh
nix build .#default 2>&1 | tail -2
```
Expected: shellcheck clean (note `_slot` nameref needs `# shellcheck disable=SC2178` if it warns; add it above `local -n _slot=$1` if so); build `rc=0`.

- [ ] **Step 5: Commit**

```bash
git add scripts/tmux-reflow-windows.sh
git commit -m "feat(reflow): natural-width packing + labels_mode decision"
```

---

## Task 6: Templates — single-line + automatic-rename

**Files:**
- Modify: `config/tmux.conf.nix` single-line status-format[1] (line ~433)
- Modify: `config/tmux.conf.nix` `automatic-rename-format` (line ~488)

- [ ] **Step 1: Update the single-line global window list**

Replace the `status-format[1]` line (the `#{W:…#{window_name}…}` one, ~line 433)
with one that selects the label live and appends the unpadded icon:

```nix
    set -g status-format[1] "#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: #{?#{==:#{@labels_mode},long},#{@window_label_long},#{?window_active,#{@window_label_long},#{@window_label_short}}} #{@window_icon_display},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]#{?#{==:#{@labels_mode},long},#{@window_label_long},#{@window_label_short}} #{@window_icon_display}}#{?window_zoomed_flag, 󰁌,}#[norange]#{?window_end_flag,, #[fg=#{@thm_subtext_0}#,nobold]│ }}"
```

(The inactive branch can skip the `window_active` sub-check since it only renders
for inactive windows — it picks long in `long` mode, else short.)

- [ ] **Step 2: Point `automatic-rename-format` at the short label**

Replace the `automatic-rename-format` line (~488) with one that reuses the
reflow-computed short label, falling back to the path basename before the first
reflow:

```nix
    set -g automatic-rename-format "#{?#{@window_label_short},#{@window_label_short},#{b:pane_current_path}} #{@window_icon_display}"
```

- [ ] **Step 3: Build**

Run: `nix build .#default 2>&1 | tail -2`
Expected: `rc=0`.

- [ ] **Step 4: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(status): adaptive window labels in single-line + window_name"
```

---

## Task 7: Enrich scripts trigger reflow

**Files:**
- Modify: `config/tmux.conf.nix:173-191` (`mkScriptEnrich` — add `@reflow@`)
- Modify: `scripts/tmux-issue-stamp.sh` (after line 50)
- Modify: `scripts/tmux-pr-enrich.sh` (in `set_pr`, after line 68)

- [ ] **Step 1: Add `@reflow@` to `mkScriptEnrich`**

In `config/tmux.conf.nix` `mkScriptEnrich` (lines 176-190), add `"@reflow@"` to
the search list and `"${script.tmux-reflow-windows}/bin/tmux-reflow-windows"` to
the replacement list (append as the last pair in each):

```nix
      [
        "@lib_enrich@"
        "@pr_refresh_seconds@"
        "@issue_stamp_linear@"
        "@issue_stamp_github@"
        "@pr_enrich@"
        "@reflow@"
      ]
      [
        "${lib-enrich}"
        (toString enrichPrRefreshSeconds)
        "${enrich-linear-bin}/bin/tmux-issue-stamp-linear"
        "${enrich-github-bin}/bin/tmux-issue-stamp-github"
        "${enrich-pr-bin}/bin/tmux-pr-enrich"
        "${script.tmux-reflow-windows}/bin/tmux-reflow-windows"
      ]
```

(`script.tmux-reflow-windows` is `mkScriptFull`, which does not depend on the
enrich bins, so there is no evaluation cycle.)

- [ ] **Step 2: Call reflow from `tmux-issue-stamp.sh`**

After the `@issue_url` set (line 50) and before the background PR-enrich spawn
(line 53), add:

```bash
# Recompute window labels now that the issue id/title exist (cache bypass).
@reflow@ "$(tmux display-message -t "$target" -p '#{session_name}')" --force >/dev/null 2>&1 &
```

- [ ] **Step 3: Call reflow from `tmux-pr-enrich.sh`**

In the `set_pr` helper, after the `@pr_url` set (line 68), add:

```bash
	@reflow@ "$(tmux display-message -t "$1" -p '#{session_name}')" --force >/dev/null 2>&1 &
```

(`$1` is the `--target` `session:window` passed to `set_pr`.)

- [ ] **Step 4: shellcheck + build**

Run:
```bash
shellcheck scripts/tmux-issue-stamp.sh scripts/tmux-pr-enrich.sh
nix build .#default 2>&1 | tail -2
```
Expected: shellcheck clean; build `rc=0`.

- [ ] **Step 5: Commit**

```bash
git add config/tmux.conf.nix scripts/tmux-issue-stamp.sh scripts/tmux-pr-enrich.sh
git commit -m "feat(enrich): recompute labels via reflow --force after writes"
```

---

## Task 8: Integration verification

**Files:** none (verification only)

- [ ] **Step 1: Full flake check**

Run: `nix flake check 2>&1 | tail -20`
Expected: passes (bats `enrich.bats` + `icons.bats`, shellcheck, shfmt, etc.).

> If `icons.bats` is not picked up by the flake's bats runner, find where
> `enrich.bats` is registered (grep `enrich.bats` in `flake.nix`/`config/`) and
> add `icons.bats` alongside it.

- [ ] **Step 2: Build and load in a throwaway server**

```bash
nix build .#default
tmuxbin=$(pwd)/result/bin/tmux
```

- [ ] **Step 3: Verify single-line — active shows long, others short**

```bash
$tmuxbin -L lbltest new-session -d -s mono -x 200 -y 50
$tmuxbin -L lbltest set -w -t mono:1 @issue_provider linear
$tmuxbin -L lbltest set -w -t mono:1 @issue_id ENG-2001
$tmuxbin -L lbltest set -w -t mono:1 @issue_title "fix login flow"
$tmuxbin -L lbltest new-window -t mono
$tmuxbin -L lbltest set -w -t mono:2 @issue_provider linear
$tmuxbin -L lbltest set -w -t mono:2 @issue_id ENG-1957
$tmuxbin -L lbltest set -w -t mono:2 @issue_title "refactor services"
$tmuxbin -L lbltest run-shell "$(pwd)/result/bin/tmux-reflow-windows mono 200 --force" 2>/dev/null || \
  $tmuxbin -L lbltest run-shell "tmux-reflow-windows mono 200 --force"
sleep 0.5
$tmuxbin -L lbltest capture-pane -p -t mono | tail -3
$tmuxbin -L lbltest kill-server
```
Expected (active window 1): window 1 shows `ENG-2001 fix login flow`, window 2
shows short `ENG-1957` (it's inactive) — OR, if both-long fits at width 200, both
show full titles (that is correct `long` mode). Switching to window 2 and
re-running shows window 2 long, window 1 short.

- [ ] **Step 4: Verify multi-line shrink — many windows go short, active stays long**

Repeat with ~10 windows at `-x 120`; confirm the status wraps to multiple lines,
inactive tabs show bare ids, and the active tab keeps its title. Use
`tests/test-display.sh` for a richer visual pass:

```bash
./tests/test-display.sh
```

- [ ] **Step 5: Verify the claude spinner still animates**

In a real session (after `home-manager switch` with the branch, or by loading
`result/bin/tmux`), start a claude pane and confirm the per-window claude icon
still updates every second — proving labels (event-driven) and icons (1s) stay
decoupled (decision A1).

- [ ] **Step 6: Final commit (if any verification fixups were needed)**

```bash
git add -A
git commit -m "test(labels): integration verification fixups"
```

---

## Self-review notes

- **Spec coverage:** label model (T2), one-source consistency (T4/T6), two-tier mode (T5), text-only/icons-separate A1 (T4/T5/T6), template-live-select A2 (T5/T6), global flag A3 (T5), testable lib A4 (T1/T2), automatic-rename reuse A5 (T6), focus re-pack via cache key (T4), enrich-triggered refresh (T7), natural widths (T5), scratch skip (preserved T4). Row-0 full title + glyphs already shipped (baseline commit).
- **Edge cases:** long==short window never grows (build_window_label returns equal strings; packing treats it uniformly); no-CLI/no-title falls back to short (T2 test); plain window basename/full (T2 tests).
- **Naming consistency:** vars `@window_label_short`, `@window_label_long`, `@labels_mode`; functions `measure_display_width` (REPLY_DW), `build_window_label` (REPLY) used identically across T1–T7.
