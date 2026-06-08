#!/usr/bin/env bats

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/state"
	export TMUX_PANE="%7"
	MANIFEST="$CLAUDE_STATUS_DIR/images/7.jsonl"
	IMG="$BATS_TEST_TMPDIR/pic.png"
	printf 'x' >"$IMG"
	APP="scripts/claude-images-update.sh"
}

run_app() { # $1 = fixture name
	sed "s#IMGPATH#$IMG#g" "tests/fixtures/$1" | bash "$APP"
}

@test "Read of an image appends one manifest line" {
	run_app hook-read-image.json
	[ -f "$MANIFEST" ]
	run wc -l <"$MANIFEST"
	[ "$output" -eq 1 ]
	run jq -r '.path' "$MANIFEST"
	[ "$output" = "$IMG" ]
	run jq -r '.source' "$MANIFEST"
	[ "$output" = "Read" ]
}

@test "Write of an image is recorded" {
	run_app hook-write-image.json
	run jq -r '.source' "$MANIFEST"
	[ "$output" = "Write" ]
}

@test "screenshot path is extracted from tool_response" {
	run_app hook-screenshot.json
	[ -f "$MANIFEST" ]
	run jq -r '.path' "$MANIFEST"
	[ "$output" = "$IMG" ]
}

@test "non-image is ignored (no manifest)" {
	run_app hook-non-image.json
	[ ! -f "$MANIFEST" ]
}

@test "missing file is ignored" {
	rm -f "$IMG"
	run_app hook-read-image.json
	[ ! -f "$MANIFEST" ]
}

@test "dedup by (path,mtime): same image twice -> one line" {
	run_app hook-read-image.json
	run_app hook-read-image.json
	run wc -l <"$MANIFEST"
	[ "$output" -eq 1 ]
}

@test "no TMUX_PANE -> no-op, exit 0" {
	unset TMUX_PANE
	run run_app hook-read-image.json
	[ "$status" -eq 0 ]
	[ ! -f "$MANIFEST" ]
}

choose() { bash scripts/claude-image-render.sh --choose "$1" "$2"; }

@test "kitty terminal with kitten → kitten" {
	run choose xterm-kitty 1
	[ "$output" = "kitten" ]
}

@test "kitty terminal without kitten → chafa-kitty" {
	run choose xterm-kitty 0
	[ "$output" = "chafa-kitty" ]
}

@test "ghostty with kitten → kitten" {
	run choose xterm-ghostty 1
	[ "$output" = "kitten" ]
}

@test "foot → chafa-sixel" {
	run choose foot 0
	[ "$output" = "chafa-sixel" ]
}

@test "unknown terminal → chafa-symbols (universal floor)" {
	run choose dumb 0
	[ "$output" = "chafa-symbols" ]
}

@test "wezterm → chafa-sixel" {
	run choose wezterm 0
	[ "$output" = "chafa-sixel" ]
}
