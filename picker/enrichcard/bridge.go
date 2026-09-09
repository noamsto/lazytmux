package main

// resolve picks the effective winState for a window: the local values
// unchanged, or — in a mirror — the bridge values with NO fallback to the
// local names.
//
// The missing fallback is the point, not an omission. A mirror window's
// @issue_*/@pr_*/@branch/@worktree/@git_root were stamped by the local
// after-new-window hook against whatever cwd launched the bridge, so they can
// describe an entirely different repo on a different host. A remote that
// stamps nothing therefore resolves to an empty winState, which renders as
// "no issue / no PR". Same semantics as bridgeOpt in config/tmux.conf.nix.
func resolve(o winOpts) winState {
	if !o.mirror {
		return o.local
	}
	w := o.bridge
	// Always local, in both modes: @window_task and @window_claude_ago come from
	// the claude-status files agentShipper already writes under the LOCAL pane
	// id. paneIcon rides along for symmetry, but @active_pane_icon is stamped
	// session-scoped by tmux-update-icons while this read is `-w` only, so it is
	// empty on every window, mirror or not — pre-existing, not addressed here.
	w.task, w.claudeAgo, w.paneIcon = o.local.task, o.local.claudeAgo, o.local.paneIcon
	return w
}
