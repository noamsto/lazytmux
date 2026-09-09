#!/usr/bin/env bash
# Stamped into a carousel viewer pane's @remux_relaunch by tmux-update-icons.sh
# (RESUME_CAROUSEL on), so tmux-remux exec's this on restore, in the pane's
# default shell. $TMUX_PANE is then the VIEWER's NEW pane id, not the host's.
#
# aeye keys its image manifest by "<tmux server pid>-<host pane id>" (aeye
# main.go:37) and a restore changes both halves, so a bare `aeye` relaunch
# would resolve to no key and show nothing. This script rediscovers the host
# agent pane in its own window, recomputes the key, re-stamps the pane options
# a fresh `prefix + I` launch would have set, then exec's the real viewer so
# the pane BECOMES it — no split, no kill, so tmux-remux's pending
# select-layout never sees a pane-count change (docs/superpowers/specs/
# 2026-09-08-carousel-remux-resume-design.md, facts 8-9).
set -euo pipefail

[[ -n ${TMUX_PANE:-} ]] || exit 0
command -v tmux >/dev/null 2>&1 || exit 0

# Resolve the viewer BEFORE stamping anything below: under set -e an abort here
# after the @claude_img_src stamp would leave a bare shell MARKED as a viewer,
# which tmux-update-icons then re-stamps every tick and every later restore
# faithfully reproduces. @carousel_aeye@ is an absolute store path substituted
# at Nix build time, so -x covers both an unsubstituted placeholder and a
# missing binary. ${AEYE_BIN:-...} is a test seam, not a user override —
# update-environment never carries it (cf. AGENT_DETECT_BIN in lib-claude.sh).
viewer="${AEYE_BIN:-@carousel_aeye@}"
[[ -x $viewer ]] || exit 0

# Space-separated pane-command basenames from the agentdetect manifests, same
# source (and same nix-wrapper normalization) as tmux-update-icons.sh's
# presence sweep and tmux-agent-usage.sh's gate — not a hand-maintained list.
AGENT_COMMANDS="${AGENT_COMMANDS:-@AGENT_COMMANDS@}"

# is_agent_cmd CMD -> 0 if CMD, after stripping nix makeWrapper's
# `.foo-wrapped` decoration, is one of AGENT_COMMANDS.
is_agent_cmd() {
	local base=${1##*/}
	base=${base#.}
	base=${base%-wrapped}
	case " $AGENT_COMMANDS " in
	*" $base "*) return 0 ;;
	*) return 1 ;;
	esac
}

# $1 is the pane index the host had when the stamp was written, carried across
# the restore in the relaunch string itself. On a restore it is the ONLY usable
# evidence of which sibling is the host: tmux-remux restores no pane options,
# every pane id changes, and — measured — a restored pane reports its SHELL as
# pane_current_command, never the relaunched program, because the relaunch runs
# as a child sharing the shell's process group and tmux names the group leader
# (`<cmd>; exec <shell>`, internal/restore/startup.go:42-47). So the agent-name
# match below cannot fire on the very path this script exists for.
HOST_HINT="${1:-}"
[[ $HOST_HINT =~ ^[0-9]+$ ]] || HOST_HINT=""

# scan_host_panes: one list-panes read of this pane's window (tmux scopes
# list-panes to the target's own window with no -a/-s). Reports three
# candidates, in the order the resolver below trusts them:
#   AGENT_HOST — a sibling whose command is an agent: the hinted index if one
#                matches, else the lowest index. Only available in a live
#                session, never on a restore (see above).
#   HINT_HOST  — the sibling sitting at HOST_HINT, whatever it is running.
#   OTHER/OTHER_COUNT — the window's non-self panes, for the last resort.
AGENT_HOST=""
HINT_HOST=""
OTHER=""
OTHER_COUNT=0
scan_host_panes() {
	AGENT_HOST=""
	HINT_HOST=""
	OTHER=""
	OTHER_COUNT=0
	local best_idx="" hinted_agent="" idx pane_id cmd
	while IFS='|' read -r idx pane_id cmd; do
		[[ -n $pane_id && $pane_id != "$TMUX_PANE" ]] || continue
		# `|| true`: a post-increment from 0 evaluates to 0, which is a
		# non-zero exit status, which set -e turns into an abort on the very
		# first sibling found (claude-status.sh:47-53 does the same).
		((OTHER_COUNT++)) || true
		OTHER="$pane_id"
		[[ -n $HOST_HINT && $idx == "$HOST_HINT" ]] && HINT_HOST="$pane_id"
		is_agent_cmd "$cmd" || continue
		[[ -n $HOST_HINT && $idx == "$HOST_HINT" ]] && hinted_agent="$pane_id"
		if [[ -z $best_idx ]] || ((idx < best_idx)); then
			best_idx="$idx"
			AGENT_HOST="$pane_id"
		fi
	done < <(tmux list-panes -t "$TMUX_PANE" -F '#{pane_index}|#{pane_id}|#{pane_current_command}' 2>/dev/null)
	[[ -n $hinted_agent ]] && AGENT_HOST="$hinted_agent"
	return 0
}

# Bounded retry, not one sample: neither candidate is necessarily there yet.
#   - A pane-0 carousel is briefly the window's only pane — tmux-remux creates
#     the window from its first pane and adds the rest as later splits — so the
#     hinted index names nothing until they land.
#   - In a live session an agent pane can be mid-exec, or replaying scrollback
#     (`cat-scrollback` runs first, so the pane reads "tmux-remux").
# 250ms * 60 = ~15s ceiling, but the loop normally settles in two passes.
# ${CAROUSEL_RESTORE_TRIES:-} is a test seam (bats shortens the bound so the
# expiry path below is reachable in under a second), not a user knob — same
# shape as AEYE_BIN above; tmux's update-environment never carries it, so a
# restored pane cannot pick one up.
tries="${CAROUSEL_RESTORE_TRIES:-60}"
[[ $tries =~ ^[0-9]+$ ]] || tries=60
HOST=""
prev_count=-1
while :; do
	scan_host_panes
	# An agent command is positive evidence and is trusted at once. It also
	# outranks the hint: in a live session the index may have moved since the
	# stamp, while the command cannot lie about what a pane is running.
	if [[ -n $AGENT_HOST ]]; then
		HOST="$AGENT_HOST"
		break
	fi
	# No agent in sight — the restore case. Trust the hint, but only once the
	# window has stopped changing shape: remux adds panes one at a time and
	# every insertion renumbers the panes above it, so an index read mid-restore
	# can name a pane that is about to become someone else.
	if [[ -n $HINT_HOST && $OTHER_COUNT == "$prev_count" ]]; then
		HOST="$HINT_HOST"
		break
	fi
	prev_count="$OTHER_COUNT"
	((tries-- > 0)) || break
	sleep 0.25
done

# Last resort: exactly one non-self pane in the window is the host by
# elimination — matching "not me" is immune to whatever command it shows, and
# to a hint whose index no longer exists (remux's filter can drop a pane and
# shift every index above it).
if [[ -z $HOST ]] && ((OTHER_COUNT == 1)); then
	HOST="$OTHER"
fi

# Never a partial stamp: no host found means today's bare shell, unchanged.
[[ -n $HOST ]] || exit 0

# Key formula pinned to aeye main.go:37's documented contract
# ("<tmux server pid>-<pane>") — a drift here is silent, since the carousel
# would open, find nothing, and read as an unrelated bug. $TMUX is
# "<socket>,<server pid>,<session>", read the same way aeye's own
# resolve_target does (scripts/tmux-claude-images.sh) — no tmux fork needed.
srv=""
IFS=, read -r _ srv _ <<<"${TMUX:-}"
[[ $srv =~ ^[0-9]+$ ]] || exit 0
key="$srv-${HOST#%}"

# Stamp the pane options a fresh `prefix + I` launch would have set, so the
# live-key toggle (scripts/tmux-claude-images.sh) finds this viewer.
# "side" is a literal fallback, not aeye's resolve_axis measurement — matching
# that would also mean honouring AEYE_SPLIT/CELL_ASPECT, a duplication this
# design doesn't budget for. It's aeye's own documented default for absent
# dims, so a wrong guess costs one `prefix + I` toggle, not correctness.
# One invocation, not two: as separate commands a failure of the second (the
# pane can close in the gap — `set-option -p` on a missing pane exits 1) would
# abort under set -e with @claude_img_src already written, leaving exactly the
# half-stamped pane the resolution ordering above exists to prevent. Batched,
# tmux parses both before running either, so they stand or fall together.
tmux set-option -p -t "$TMUX_PANE" @claude_img_src "$key" \; \
	set-option -p -t "$TMUX_PANE" @claude_img_axis side

# AEYE_HOST_PANE gives the viewer's own `s` axis toggle a target
# (gallery_split.go's hostPane), same as the launcher's env_args.
export AEYE_HOST_PANE="$HOST"
exec "$viewer" "$key"
