#!/usr/bin/env bash
# Window picker: tree view with session headers and window rows.
# Go binary generates ANSI output, fzf-tmux provides the popup.
# Background refresh every 1s. Preview shows pane content.
#
# Flags:
#   --generate [--claude]  Output list for fzf (called by self/fzf reload)
#   --claude               Start in claude-only mode (only sessions with active claude)

# shellcheck disable=SC2016,SC2001  # SC2016: fzf uses single-quoted commands; SC2001: ANSI regex needs sed
set -euo pipefail

FZF=@fzf@

if [[ ${1:-} == "--generate" ]]; then
	if [[ ${2:-} == "--claude" ]]; then
		exec @picker_generate@ --windows --claude
	else
		exec @picker_generate@ --windows
	fi
fi

CLAUDE_MODE=0
[[ ${1:-} == "--claude" ]] && CLAUDE_MODE=1

SELF="$0"
FZF_TMUX="${FZF%fzf}fzf-tmux"
PORT=$((RANDOM % 10000 + 40000))

# Mode file: background refresh reads this to know current mode
MODE_FILE=$(mktemp)
trap 'rm -f "$MODE_FILE"' EXIT
if ((CLAUDE_MODE)); then
	echo "claude" >"$MODE_FILE"
	LABEL=" 󰒲 Claude Windows "
else
	echo "all" >"$MODE_FILE"
	LABEL=" Windows "
fi

# Background refresh: only reload when output changes (preserves preview scroll)
HASH_FILE=$(mktemp)
trap 'rm -f "$MODE_FILE" "$HASH_FILE"' EXIT
(
	sleep 1
	while sleep 2; do
		mode=$(cat "$MODE_FILE" 2>/dev/null || echo "all")
		if [[ $mode == "claude" ]]; then
			new=$("$SELF" --generate --claude 2>/dev/null | md5sum)
		else
			new=$("$SELF" --generate 2>/dev/null | md5sum)
		fi
		old=$(cat "$HASH_FILE" 2>/dev/null || echo "")
		[[ $new == "$old" ]] && continue
		echo "$new" >"$HASH_FILE"
		if [[ $mode == "claude" ]]; then
			body="reload($SELF --generate --claude)"
		else
			body="reload($SELF --generate)"
		fi
		exec 3<>/dev/tcp/127.0.0.1/"$PORT" 2>/dev/null || exit 0
		printf 'POST / HTTP/1.0\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s' \
			"${#body}" "$body" >&3
		cat <&3 >/dev/null 2>&1
		exec 3>&-
	done
) &

# Target is field 1 (before TAB). {1} in fzf extracts it.
PREVIEW_CMD='bash -c '"'"'tmux capture-pane -t "$0" -p -e 2>/dev/null'"'"' {1}'
KILL_CMD='bash -c '"'"'
	target="$0"
	if [[ "$target" == *:* ]]; then
		tmux kill-window -t "$target" 2>/dev/null
	else
		tmux kill-session -t "$target" 2>/dev/null
	fi
'"'"' {1}'

# Mode switch commands write to mode file then reload
SWITCH_CLAUDE="execute-silent(echo claude > $MODE_FILE)+reload($SELF --generate --claude)+change-border-label( 󰒲 Claude Windows )"
SWITCH_ALL="execute-silent(echo all > $MODE_FILE)+reload($SELF --generate)+change-border-label( Windows )"

GEN_INIT="$SELF --generate"
((CLAUDE_MODE)) && GEN_INIT="$SELF --generate --claude"

selected=$(
	$GEN_INIT | "$FZF_TMUX" -p 90%,85% -- \
		--listen "$PORT" \
		--ansi \
		--delimiter '\t' \
		--with-nth 2 \
		--header-lines 1 \
		--layout reverse \
		--border rounded \
		--border-label "$LABEL" \
		--list-border bottom \
		--list-label $' \e[2m^x\e[0m kill \e[2m·\e[0m \e[2m^/\e[0m preview \e[2m·\e[0m \e[2mM-jk\e[0m scroll \e[2m·\e[0m \e[2m^a\e[0m claude \e[2m·\e[0m \e[2m^r\e[0m refresh ' \
		--list-label-pos 2:bottom \
		--pointer '▸' \
		--prompt '  ' \
		--no-info \
		--margin 0 \
		--padding 0,1 \
		--preview "$PREVIEW_CMD" \
		--preview-window 'right:50%:wrap' \
		--bind "ctrl-r:$SWITCH_ALL" \
		--bind "ctrl-a:$SWITCH_CLAUDE" \
		--bind 'ctrl-/:toggle-preview' \
		--bind "ctrl-x:execute-silent($KILL_CMD)+reload($SELF --generate)" \
		--bind 'alt-j:preview-down' \
		--bind 'alt-k:preview-up' \
		--bind 'enter:accept' \
		--bind 'esc:abort'
) || true

[[ -z $selected ]] && exit 0
target=$(cut -f1 <<<"$selected")
[[ -n $target ]] && tmux switch-client -t "$target"
exit 0
