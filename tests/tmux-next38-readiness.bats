#!/usr/bin/env bats
# Headless readiness smoke for the shipped next-3.8 tmux wrapper.

setup() {
	TMUX_BIN="${TMUX_BIN:?set TMUX_BIN to the built wrapper}"
	SOCKET="lztmux-next38-${BATS_TEST_NUMBER}-$$"
	TEST_HOME="$BATS_TEST_TMPDIR/home"
	mkdir -p "$TEST_HOME"
	export HOME="$TEST_HOME"
	export XDG_CACHE_HOME="$TEST_HOME/.cache"
	export XDG_CONFIG_HOME="$TEST_HOME/.config"
	export XDG_STATE_HOME="$TEST_HOME/.local/state"
	export TERM=xterm-256color

	t new-session -d -s s -c "$PWD"
	wait_for_nonempty_option @thm_bg
}

teardown() {
	t kill-server 2>/dev/null || true
}

t() {
	"$TMUX_BIN" -L "$SOCKET" "$@"
}

wait_for_nonempty_option() {
	local option=$1
	local got
	local i
	for i in {1..50}; do
		got="$(t show-options -gqv "$option" 2>/dev/null || true)"
		if [[ -n $got ]]; then
			return 0
		fi
		sleep 0.1
	done
	printf 'timed out waiting for non-empty %s\n' "$option" >&2
	return 1
}

wait_for_option() {
	local option=$1
	local want=$2
	local got
	local i
	for i in {1..50}; do
		got="$(t show-options -t s -qv "$option" 2>/dev/null || true)"
		[[ $got == "$want" ]] && return 0
		# @reflow_key "N::H" means the width probe was empty — fail loud (#235).
		if [[ $option == @reflow_key && $got =~ ^[0-9]+:: ]]; then
			printf 'empty width in %s=%s (want %s); width probe was empty\n' \
				"$option" "$got" "$want" >&2
			return 1
		fi
		sleep 0.1
	done
	printf 'timed out waiting for %s=%s, last=%s\n' "$option" "$want" "$got" >&2
	return 1
}

store_conf() {
	grep -o -- '-f /nix/store/[a-z0-9]*-tmux[.]conf' "$TMUX_BIN" | head -1 | cut -d' ' -f2
}

store_path() {
	local pattern=$1
	grep -o "$pattern" "$(store_conf)" | head -1
}

embedded_path() {
	local file=$1
	local pattern=$2
	grep -o "$pattern" "$file" | head -1
}

wrapper_path() {
	local pattern=$1
	grep -o "$pattern" "$TMUX_BIN" | head -1
}

make_tmux_shim() {
	SHIM_DIR="$BATS_TEST_TMPDIR/shim"
	mkdir -p "$SHIM_DIR"
	cat >"$SHIM_DIR/tmux" <<EOF
#!$(command -v bash)
exec "$TMUX_BIN" -L "$SOCKET" "\$@"
EOF
	chmod +x "$SHIM_DIR/tmux"
}

strip_styles() {
	sed 's/#[[][^]]*[]]//g'
}

wait_for_client() {
	local i
	for i in {1..30}; do
		[[ "$(t list-clients -t s 2>/dev/null | wc -l)" -gt 0 ]] && return 0
		sleep 0.1
	done
	return 1
}

@test "wrapper runs pinned next-3.8 tmux and catppuccin renders theme variables" {
	run t -V
	[ "$status" -eq 0 ]
	[[ $output == *"next-3.8"* ]]

	thm_bg="$(t show -gv @thm_bg)"
	thm_mauve="$(t show -gv @thm_mauve)"
	[[ $thm_bg =~ ^#[0-9a-fA-F]{6}$ ]]
	[[ $thm_mauve =~ ^#[0-9a-fA-F]{6}$ ]]
}

@test "status-format 0-4 parse and the W loop expands after reflow" {
	t set-option -t s:1 -w @branch "feat/next38-one"
	for idx in 2 3 4 5 6 7 8 9 10; do
		t new-window -t s -c "$PWD"
		t set-option -t "s:$idx" -w @branch "feat/next38-window-$idx-with-extra-label-width"
	done

	make_tmux_shim
	reflow="$(store_path '/nix/store/[[:alnum:]]*-tmux-reflow-windows/bin/tmux-reflow-windows')"
	coproc REFLOW_CTL { "$TMUX_BIN" -L "$SOCKET" -C attach-session -t s; }
	wait_for_client
	PATH="$SHIM_DIR:$PATH" "$reflow" s 36 --force
	# Keep the control client attached until the key is observed — detach first
	# used to race backgrounded empty-width reflows over the good stamp (#235).
	# Height trails the width in the key; a control client contributes no size, so
	# it resolves to 0 and the row cap stays at its 3-row baseline.
	wait_for_option @reflow_key "10:36:0"
	printf 'detach-client\n' >&"${REFLOW_CTL[1]}" || true
	kill "$REFLOW_CTL_PID" 2>/dev/null || true

	[ "$(t show-options -t s -qv status)" -ge 3 ]
	[ "$(t show-options -t s -qv @window_split)" != "999" ]
	[ "$(t show-options -t s -qv @window_per)" -lt 10 ]
	# Row 4 exists but stays unreached at the 3-row cap.
	[ "$(t show-options -t s -qv @window_split3)" = "999" ]

	for i in 0 1 2 3; do
		fmt="$(t show -gv "status-format[$i]")"
		t display-message -p -F "$fmt" >/dev/null
	done
	# Reflow's own session-level rows, including the row-4 format it now writes.
	for i in 1 2 3 4; do
		fmt="$(t show-options -t s -qv "status-format[$i]")"
		t display-message -p -F "$fmt" >/dev/null
	done

	line1="$(t display-message -p -F "$(t show -gv status-format[1])" | strip_styles)"
	line2="$(t display-message -p -F "$(t show -gv status-format[2])" | strip_styles)"
	[[ $line1 == *"1:"* ]]
	[[ $line1 == *"next38"* || $line2 == *"next38"* ]]
}

@test "popup bindings and picker tmux data path are present" {
	keys="$(t list-keys -T prefix)"
	[[ $keys == *"tmux-session-picker"* ]]
	[[ $keys == *"tmux-window-picker"* ]]
	[[ $keys == *"display-popup"* && $keys == *"tmux-enrich-card"* ]]
	[[ $keys == *"tmux-gh-dash"* ]]

	session_picker="$(store_path '/nix/store/[[:alnum:]]*-tmux-session-picker/bin/tmux-session-picker')"
	picker="$(embedded_path "$session_picker" '/nix/store/[[:alnum:]]*-lazytmux-go-tools-[^[:space:]]*/bin/tmux-picker-generate')"
	[[ -x $picker ]]

	make_tmux_shim

	run env PATH="$SHIM_DIR:$PATH" tmux list-sessions -F '#{session_name}'
	[ "$status" -eq 0 ]
	[[ $output == *"s"* ]]

	run env PATH="$SHIM_DIR:$PATH" tmux list-windows -a -F '#{session_name}:#{window_index}'
	[ "$status" -eq 0 ]
	[[ $output == *"s"* ]]
}

@test "display-popup opens for an attached control client" {
	marker="$BATS_TEST_TMPDIR/popup-ran"
	coproc CTL { "$TMUX_BIN" -L "$SOCKET" -C attach-session -t s; }

	wait_for_client

	printf 'display-popup -E "printf popup-ok > %q"\n' "$marker" >&"${CTL[1]}"
	for _ in {1..30}; do
		[[ -f $marker ]] && break
		sleep 0.1
	done
	printf 'detach-client\n' >&"${CTL[1]}" || true
	kill "$CTL_PID" 2>/dev/null || true

	[ "$(cat "$marker")" = "popup-ok" ]
}

@test "gh-dash stays pinned to 4.23.2 and launcher composes a theme config" {
	gh_dash="$(wrapper_path '/nix/store/[a-z0-9]*-gh-dash-4[.]23[.]2/bin')/gh-dash"
	launcher="$(store_path '/nix/store/[a-z0-9]*-tmux-gh-dash/bin/tmux-gh-dash')"
	[[ -x $gh_dash ]]
	[[ -x $launcher ]]

	run "$gh_dash" --version
	[ "$status" -eq 0 ]
	[[ $output == *"4.23.2"* ]]

	run timeout 2 "$launcher" --help
	[[ -f $XDG_CACHE_HOME/lazytmux/gh-dash-config.yml ]]
	grep -q 'theme:' "$XDG_CACHE_HOME/lazytmux/gh-dash-config.yml"
}

# === Remote bridge structural-input gate (M2.3) ===
#
# The bridge daemon's own integration tests run on vanilla `tmux -L` servers with
# no lazytmux keybindings, so they cannot see the gate at all. These cases run
# against the WRAPPED tmux — the real generated config — which is the only place
# in CI where the gate and the rebound defaults exist.

# The gate is a pure format, so its truth table is checkable without pressing a
# key: it must be true only where the daemon stamped BOTH carriers. #{&&:...}
# yields "1"/"0", and if-shell -F treats "0" (like "") as false.
@test "bridge gate is true only for a tagged window with a stamped pane" {
	gate='#{&&:#{@bridge_win},#{@bridge_pane}}'

	# A normal window: neither carrier set.
	[ "$(t display-message -p -t s:1 -F "$gate")" = 0 ]

	t new-window -d -t s: -n bridgey
	t set-option -w -t s:bridgey @bridge_win 1
	# Window tagged but the pane not stamped — a pane the daemon does not own must
	# still fall through to the local action.
	[ "$(t display-message -p -t s:bridgey -F "$gate")" = 0 ]

	pane="$(t list-panes -t s:bridgey -F '#{pane_id}' | head -1)"
	t set-option -p -t "$pane" @bridge_pane '%42'
	[ "$(t display-message -p -t s:bridgey -F "$gate")" = 1 ]

	# Still false in the untagged window, i.e. the pane option did not leak.
	[ "$(t display-message -p -t s:1 -F "$gate")" = 0 ]
}

# M2.3 made the config own three keys tmux used to own (`,`, `{`, `}`). Their
# non-bridge behavior must stay byte-identical to next-3.8's default, so assert
# the else-branch command and the -N note against the pinned upstream text. This
# turns a hand transcription into a claim that fails loudly on the next tmux bump.
@test "keys the bridge gate newly owns keep their upstream default and note" {
	run t list-keys -T prefix ,
	[ "$status" -eq 0 ]
	# else-branch is tmux's default rename prompt, seeded from #W. Compared in the
	# form list-keys itself prints (tmux normalises the `--` away), which is also
	# the form a future tmux bump would change.
	[[ $output == *'{ command-prompt -I "#W" { rename-window "%%" } }'* ]]

	run t list-keys -T prefix '{'
	[ "$status" -eq 0 ]
	[[ $output == *'swap-pane -U'* ]]

	run t list-keys -T prefix '}'
	[ "$status" -eq 0 ]
	[[ $output == *'swap-pane -D'* ]]

	# The -N notes feed which-key, and rebinding a default drops the note unless
	# it is re-supplied.
	run t list-keys -N -T prefix
	[ "$status" -eq 0 ]
	[[ $output == *'Rename current window'* ]]
	[[ $output == *'Swap the active pane with the pane above'* ]]
	[[ $output == *'Swap the active pane with the pane below'* ]]
}

# Every gated key must still carry its original local behavior in the else
# branch: a regression here is a regression for every non-bridge window.
@test "gated keys keep their local behavior in the else branch" {
	run t list-keys -T prefix '|'
	[[ $output == *'split-window -h'* ]]
	[[ $output == *'pane_current_path'* ]]

	run t list-keys -T prefix _
	[[ $output == *'split-window -v'* ]]

	# c keeps the scratchpad guard.
	run t list-keys -T prefix c
	[[ $output == *'scratch-'* ]]
	[[ $output == *'new-window -c'* ]]

	# x keeps the claude-status kill guard.
	run t list-keys -T prefix x
	[[ $output == *'tmux-kill-pane-guard'* ]]
	[[ $output == *'confirm-before'* ]]

	# & keeps its confirmation.
	run t list-keys -T prefix '&'
	[[ $output == *'kill-window'* ]]
	[[ $output == *'confirm-before'* ]]

	# The four resize binds keep -r (repeatable) and their local resize-pane.
	local key
	for key in M-Up M-Down M-Left M-Right; do
		run t list-keys -T prefix "$key"
		[ "$status" -eq 0 ]
		[[ $output == *' -r '* ]]
		[[ $output == *'resize-pane -'* ]]
	done
}

# The focus hook must be registered, and `prefix + r` must stay idempotent: the
# bare `set-hook -gu after-select-pane` clear has to exist, or a reload would
# stack hooks pointing at dead store paths.
@test "after-select-pane focus hook is set and reload-idempotent" {
	run t show-hooks -g
	[ "$status" -eq 0 ]
	[[ $output == *'after-select-pane[20]'* ]]
	[[ $output == *'lztmux-remote-bridge-ctl'* ]]

	# prefix + r re-sources the generated config, so re-sourcing must not stack a
	# second hook. Source the wrapper's own config rather than a user symlink,
	# which does not exist in the sandbox.
	before="$(t show-hooks -g | grep -c 'after-select-pane')"
	t source-file "$(store_conf)"
	after="$(t show-hooks -g | grep -c 'after-select-pane')"
	[ "$before" -eq "$after" ]

	# The clear that makes that true has to be in the config, not incidental.
	grep -q 'set-hook -gu after-select-pane' "$(store_conf)"
}
