#!/usr/bin/env bash
# Notification history — the `prefix + n` view (#164). Runs inside
# display-popup -E.
#
# READ-ONLY consumer of the on-disk format: it never writes an event, never
# rewrites the .server_start marker, and never prunes. Pruning is the router's
# job exclusively, so this popup can never race the prune lock.
#
# No pager: the popup's own pane scrolls to the BOTTOM of overlong output, which
# would push the newest events — the ones that matter — off the top, and no
# pager is pinned to a store path in this repo. Instead: newest first, capped to
# the popup's rows, with one "… +N older" line.
set -uo pipefail

# shellcheck source=/dev/null
source @lib_notify@

# Growth within a server generation is bounded HERE, not on the emit path: at
# most this many filenames are read however many files the directory holds.
READ_MAX=100

# The popup gives us a pty, so stty answers. Fall back to 20 rows when it does
# not (e.g. stdout is a pipe under test).
rows=20
r=""
read -r r _ < <(stty size 2>/dev/null) || true
[[ $r =~ ^[0-9]+$ ]] && rows="$r"
cap=$((rows - 2))
((cap < 1)) && cap=1

# Colour only for a terminal: piped output stays plain so it is greppable.
c_reset="" c_dim="" c_error="" c_warn="" c_info=""
if [[ -t 1 ]]; then
	c_reset=$'\033[0m'
	c_dim=$'\033[2m'
	c_error=$'\033[31m'
	c_warn=$'\033[33m'
	c_info=$'\033[32m'
fi

# Newest first. The epoch-prefixed filename (10-digit epoch until 2286) makes a
# plain glob's ascending order chronological, so walking it backwards needs no
# stat per file and no sort.
files=()
if [[ -d $NOTIFY_EVENTS_DIR ]]; then
	shopt -s nullglob
	all=("$NOTIFY_EVENTS_DIR"/*)
	shopt -u nullglob
	for ((i = ${#all[@]} - 1; i >= 0; i--)); do
		[[ -f ${all[i]} ]] || continue
		files+=("${all[i]}")
		((${#files[@]} >= READ_MAX)) && break
	done
fi

# One keypress dismisses, on BOTH paths. A popup that flashed open and shut on
# an empty history would read as a broken keybind, not as "nothing to show".
dismiss() {
	read -rsn1 _ 2>/dev/null || true
}

if ((${#files[@]} == 0)); then
	printf 'no notifications\n'
	dismiss
	exit 0
fi

printf -v now '%(%s)T' -1
shown=0
for f in "${files[@]}"; do
	((shown < cap)) || break
	ts="" src="" level="" window="" session="" title="" body=""
	while IFS='=' read -r k v; do
		case "$k" in
		ts) ts="$v" ;;
		source) src="$v" ;;
		level) level="$v" ;;
		window) window="$v" ;;
		session) session="$v" ;;
		title) title="$v" ;;
		body) body="$v" ;;
		esac
	done <"$f"
	[[ -n $title ]] || continue

	age="?"
	if [[ $ts =~ ^[0-9]+$ ]]; then
		notify_ago "$((now - ts))"
		age="$REPLY"
	fi
	# Same glyph constants and the same locator helper the router's message line
	# uses, fed the stored session/window — so the two cannot disagree.
	notify_icon "$src"
	glyph="$REPLY"
	notify_locator "$session" "$window"
	loc="$REPLY"
	case "$level" in
	error) lc="$c_error" ;;
	warn) lc="$c_warn" ;;
	info) lc="$c_info" ;;
	*) lc="" ;;
	esac

	line=$(printf '%-4s %s %s%s%s  %s%s%s' \
		"$age" "$glyph" "$c_dim" "$loc" "$c_reset" "$lc" "$title" "$c_reset")
	[[ -n $body ]] && line+="  ${c_dim}${body}${c_reset}"
	printf '%s\n' "$line"
	((++shown))
done

# N counts the older events among the newest READ_MAX this run read.
if ((${#files[@]} > shown)); then
	printf '… +%d older\n' "$((${#files[@]} - shown))"
fi

dismiss
exit 0
