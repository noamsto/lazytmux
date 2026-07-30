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
		# @reflow_key "N:" means the width probe was empty — fail loud (#235).
		if [[ $option == @reflow_key && $got =~ ^[0-9]+:$ ]]; then
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

@test "status-format 0-3 parse and the W loop expands after reflow" {
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
	wait_for_option @reflow_key "10:36"
	printf 'detach-client\n' >&"${REFLOW_CTL[1]}" || true
	kill "$REFLOW_CTL_PID" 2>/dev/null || true

	[ "$(t show-options -t s -qv status)" -ge 3 ]
	[ "$(t show-options -t s -qv @window_split)" != "999" ]
	[ "$(t show-options -t s -qv @window_per)" -lt 10 ]

	for i in 0 1 2 3; do
		fmt="$(t show -gv "status-format[$i]")"
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
