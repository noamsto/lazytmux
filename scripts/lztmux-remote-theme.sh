#!/usr/bin/env bash
# Fan a light/dark toggle out to every mirrored remote.
#
# A mirror pane is a renderer replaying the remote pane's bytes, so its colours
# were chosen on the remote from the remote's own theme state. A local toggle
# re-themes the frame (status line, borders, pickers) and leaves the content in
# the other flavour until the remote applies the same theme.
#
# The toggle already re-sources the config, so the config's tail calls this and
# the change detection lives here: the flavour is compared against the stamp
# from the last fan-out, making `prefix + r` and an activation reload no-ops.
set -uo pipefail

# Pinned at build time for the reason lztmux-remote-open spells out: the tmux
# server's PATH is frozen until a restart, so a bare name can reach a stale ctl.
ctl="@bridge_ctl@"
[[ $ctl == @* ]] && ctl="$(command -v lztmux-remote-bridge-ctl)"

flavor="$(tmux show-options -gv @catppuccin_flavor 2>/dev/null)"
if [[ $flavor == "latte" ]]; then
	theme="light"
else
	theme="dark"
fi

[[ $(tmux show-options -gv @lztmux_theme_applied 2>/dev/null) == "$theme" ]] && exit 0
tmux set-option -g @lztmux_theme_applied "$theme"

# The socket leads so a session name holding the delimiter cannot shift it: read
# gives everything after the first | to the last variable.
while IFS='|' read -r sock sess; do
	[[ -n $sock ]] || continue
	# Any mirrored pane in the session reaches the daemon; the verb is
	# session-wide, and the pane only rides along because every verb takes one.
	pane="$(tmux list-panes -s -t "=$sess" -F '#{@bridge_pane}' 2>/dev/null | grep -m1 .)" || continue
	"$ctl" --sock "$sock" theme "$pane" "$theme" >/dev/null 2>&1
done < <(tmux list-sessions -F '#{@bridge_sock}|#{session_name}' 2>/dev/null)

# An unreachable daemon leaves one mirror in the old flavour; it does not make
# the reload that called this a failure.
exit 0
