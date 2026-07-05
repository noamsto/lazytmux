#!/usr/bin/env bash
# prefix+x guard. Exit 0 => safe to kill instantly (no confirm); exit non-zero
# => confirm first. An idle shell and an idle Claude pane (done/idle/interrupted/
# error) kill instantly; a Claude pane mid-work (processing/compacting/waiting/
# denied) or any other running foreground program (vim, a build, a REPL) confirms.
# shellcheck source=/dev/null  # Nix store path substituted at build time
source @lib_claude@

pane_id="${1:-}"
cmd="${2:-}"

# Normalize the nix makeWrapper decoration (.foo-wrapped -> foo) before matching.
if [[ $cmd =~ ^\.(.*)-wrapped$ ]]; then
	cmd="${BASH_REMATCH[1]}"
fi

case "$cmd" in
bash | fish | zsh | sh | dash) exit 0 ;;
esac

# read_pane_state applies the interrupt reclassification, so a stuck `processing`
# pane that was actually Esc-interrupted reads as `interrupted` (idle) here.
if read_pane_state "$CLAUDE_PANES_DIR/${pane_id#%}"; then
	case "$REPLY" in
	processing | compacting | waiting | denied) exit 1 ;;
	*) exit 0 ;;
	esac
fi

# Non-shell, non-Claude foreground (vim, build, REPL) — confirm.
exit 1
