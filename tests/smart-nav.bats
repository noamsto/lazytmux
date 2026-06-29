#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031  # bats @test blocks run in subshells; export is intentional
setup() {
	SCRIPT="$(dirname "$BATS_TEST_DIRNAME")/scripts/tmux-smart-nav.sh"
	STUB="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$STUB"
	export KITTY_LOG="$BATS_TEST_TMPDIR/kitty.log"
	: >"$KITTY_LOG"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	: >"$TMUX_LOG"
	printf '#!/usr/bin/env bash\necho "$*" >>"%s"\n' "$KITTY_LOG" >"$STUB/kitty"
	printf '#!/usr/bin/env bash\necho "$*" >>"%s"\n' "$TMUX_LOG" >"$STUB/tmux"
	chmod +x "$STUB/kitty" "$STUB/tmux"
	export PATH="$STUB:$PATH"
}

@test "zoomed: no movement at all" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 1 1
	[ "$status" -eq 0 ]
	[ ! -s "$KITTY_LOG" ]
	[ ! -s "$TMUX_LOG" ]
}

@test "non-edge: select-pane within tmux" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 0 0
	grep -q 'select-pane -R' "$TMUX_LOG"
	[ ! -s "$KITTY_LOG" ]
}

@test "edge without KITTY_LISTEN_ON: falls back to select-pane" {
	unset KITTY_LISTEN_ON
	run bash "$SCRIPT" R right 0 1
	grep -q 'select-pane -R' "$TMUX_LOG"
	[ ! -s "$KITTY_LOG" ]
}

@test "edge with kitty: hand off to neighboring_window" {
	export KITTY_LISTEN_ON=unix:/tmp/k
	run bash "$SCRIPT" R right 0 1
	grep -q '@ action neighboring_window right' "$KITTY_LOG"
	[ ! -s "$TMUX_LOG" ]
}
