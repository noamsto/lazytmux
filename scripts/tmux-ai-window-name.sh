#!/usr/bin/env bash
# Generate a short, sensible window name for a fallback window — one with no
# tracked issue and on the default branch, where build_window_label would
# otherwise show the raw captured prompt (@window_task) or the dir basename.
# Kicked in the background by tmux-update-icons when such a window's task
# changes; self-debouncing, self-deduping, and a no-op for proper worktrees.
#
# Usage: tmux-ai-window-name <session>:<window-index>
#
# Writes @window_ai_name (kebab-case title) + @window_ai_name_hash (the task
# hash it was built for) on the window; build_window_label prefers it over the
# raw task. Titles are cached by task hash under $CLAUDE_STATUS_DIR/ainames so
# identical work across windows costs one model call.

# shellcheck source=/dev/null  # Nix store path substituted at build time
source @lib_claude@

target="${1:?usage: tmux-ai-window-name <session>:<window-index>}"
session="${target%%:*}"

MODEL="@ai_model@"
COMMAND="@ai_command@"
DEBOUNCE=@ai_debounce@ # seconds to wait out further prompts before generating
MAX_CHARS=@ai_max_chars@

AINAMES_DIR="$CLAUDE_STATUS_DIR/ainames"
LOCK_DIR="/tmp/lazytmux-ainame"

read_opt() { tmux display-message -t "$target" -p "#{$1}" 2>/dev/null; }

set_name() {
	tmux set -qw -t "$target" @window_ai_name "$1"
	tmux set -qw -t "$target" @window_ai_name_hash "$2"
	tmux-reflow-windows "$session" --force >/dev/null 2>&1 || true
}

clear_name() {
	[[ -z $(read_opt @window_ai_name) ]] && return 0
	tmux set -qw -t "$target" @window_ai_name ''
	tmux set -qw -t "$target" @window_ai_name_hash ''
	tmux-reflow-windows "$session" --force >/dev/null 2>&1 || true
}

# claude lives in the user profile, not on the wrapped tmux server's PATH, so
# resolve it explicitly across the common Nix / Homebrew profile locations.
resolve_command() {
	if [[ $COMMAND == /* ]]; then
		[[ -x $COMMAND ]] && REPLY="$COMMAND" && return 0
		return 1
	fi
	if command -v "$COMMAND" >/dev/null 2>&1; then
		REPLY="$COMMAND"
		return 0
	fi
	local d
	for d in "$HOME/.nix-profile/bin" "/etc/profiles/per-user/$USER/bin" \
		"/run/current-system/sw/bin" "/usr/local/bin" "/opt/homebrew/bin"; do
		[[ -x "$d/$COMMAND" ]] && REPLY="$d/$COMMAND" && return 0
	done
	return 1
}

# Proper worktree = a tracked issue or a non-default branch; it names itself
# from the issue/branch, so the AI title is not wanted there.
is_worktree() {
	local iid br
	iid=$(read_opt @issue_id)
	br=$(read_opt @branch)
	[[ -n $iid ]] || [[ -n $br && $br != main && $br != master ]]
}

if is_worktree; then
	clear_name
	exit 0
fi

task=$(read_opt @window_task)
if [[ -z $task ]]; then
	clear_name
	exit 0
fi

# Already named for this exact task — nothing to do.
hash=$(printf '%s' "$task" | sha1sum)
hash="${hash%% *}"
if [[ $(read_opt @window_ai_name_hash) == "$hash" && -n $(read_opt @window_ai_name) ]]; then
	exit 0
fi

# Cache hit: another window (or an earlier prompt) already titled this task.
mkdir -p "$AINAMES_DIR"
cache="$AINAMES_DIR/$hash"
if [[ -s $cache ]]; then
	IFS= read -r title <"$cache"
	set_name "$title" "$hash"
	exit 0
fi

# One generation per window at a time; a newer kick just exits.
mkdir -p "$LOCK_DIR"
lockfile="$LOCK_DIR/${target//[^A-Za-z0-9]/_}.lock"
exec 9>"$lockfile"
flock -n 9 || exit 0

# Debounce: let a burst of prompts settle, then bail if the task moved on (the
# next kick owns the newer task) or the window became a worktree meanwhile.
sleep "$DEBOUNCE"
[[ $(read_opt @window_task) == "$task" ]] || exit 0
if is_worktree; then
	clear_name
	exit 0
fi

resolve_command || exit 0
prompt="Reply with ONLY a 2-4 word lowercase window title (no punctuation, no quotes, no explanation) summarizing this task: \"$task\""
raw=$(cd /tmp && "$REPLY" -p --model "$MODEL" \
	--strict-mcp-config --disable-slash-commands --setting-sources project \
	"$prompt" 2>/dev/null) || exit 0

# Sanitize: first line, lowercase, kebab, trimmed, capped without a partial word.
IFS= read -r title <<<"$raw"
title="${title,,}"
title="${title//[^a-z0-9]/-}"
while [[ $title == *--* ]]; do title="${title//--/-}"; done
title="${title#-}"
title="${title%-}"
[[ -z $title ]] && exit 0
if ((${#title} > MAX_CHARS)); then
	title="${title:0:MAX_CHARS}"
	[[ $title == *-* ]] && title="${title%-*}"
	title="${title%-}"
fi

printf '%s\n' "$title" >"$cache"
set_name "$title" "$hash"
