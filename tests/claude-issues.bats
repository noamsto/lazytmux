#!/usr/bin/env bats

load helper

CSU="scripts/claude-status-update.sh"

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/claude-status"
	# Hermetic: ignore the tmux session the developer runs bats from
	unset TMUX TMUX_PANE
}

@test "issue add: creates issues file with id" {
	bash "$CSU" issue add ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "issue add: appends second id comma-separated" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add GH-42 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123,GH-42" ]
}

@test "issue add: dedupes existing id" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "issue done: removes id, keeps others" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue add GH-42 --pane %7
	bash "$CSU" issue "done" ENG-123 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "GH-42" ]
}

@test "issue done: removing last id removes the file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue "done" ENG-123 --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}

@test "issue done: missing file is a no-op" {
	run bash "$CSU" issue "done" ENG-123 --pane %7
	[ "$status" -eq 0 ]
}

@test "issue clear: removes the file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" issue clear --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}

@test "issue add: rejects id with invalid characters" {
	run bash "$CSU" issue add 'ENG#123' --pane %7
	[ "$status" -eq 1 ]
}

@test "issue add: rejects empty id" {
	run bash "$CSU" issue add --pane %7
	[ "$status" -eq 1 ]
}

@test "issue: rejects unknown action" {
	run bash "$CSU" issue frobnicate --pane %7
	[ "$status" -eq 1 ]
}

@test "issue add: no pane id exits 0 silently" {
	run bash "$CSU" issue add ENG-123
	[ "$status" -eq 0 ]
	[ ! -e "$CLAUDE_STATUS_DIR/issues" ] || [ -z "$(ls -A "$CLAUDE_STATUS_DIR/issues")" ]
}

@test "issue done: exact match only — prefix id does not remove longer id" {
	bash "$CSU" issue add ENG-1 --pane %7
	bash "$CSU" issue add ENG-12 --pane %7
	bash "$CSU" issue "done" ENG-1 --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-12" ]
}

@test "state write does not touch the issues file" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" processing --pane %7
	[ "$(cat "$CLAUDE_STATUS_DIR/issues/7")" = "ENG-123" ]
}

@test "clear state removes pane and issues files" {
	bash "$CSU" issue add ENG-123 --pane %7
	bash "$CSU" processing --pane %7
	bash "$CSU" clear --pane %7
	[ ! -e "$CLAUDE_STATUS_DIR/panes/7" ]
	[ ! -e "$CLAUDE_STATUS_DIR/issues/7" ]
}

@test "format_issue_list: no ids yields empty" {
	setup_lib_claude
	format_issue_list 3
	[ -z "$REPLY" ]
}

@test "format_issue_list: under cap joins with spaces" {
	setup_lib_claude
	format_issue_list 3 ENG-1 GH-2
	[ "$REPLY" = "ENG-1 GH-2" ]
}

@test "format_issue_list: exactly at cap has no suffix" {
	setup_lib_claude
	format_issue_list 3 ENG-1 ENG-2 ENG-3
	[ "$REPLY" = "ENG-1 ENG-2 ENG-3" ]
}

@test "format_issue_list: over cap truncates with +N" {
	setup_lib_claude
	format_issue_list 3 ENG-1 ENG-2 ENG-3 ENG-4 ENG-5
	[ "$REPLY" = "ENG-1 ENG-2 ENG-3 +2" ]
}

write_pane_fixture() {
	# write_pane_fixture PANE SESSION [ISSUES]
	mkdir -p "$CLAUDE_STATUS_DIR/panes" "$CLAUDE_STATUS_DIR/issues"
	printf 'state=processing\ntimestamp=%s\nsession=%s\n' "$(date +%s)" "$2" \
		>"$CLAUDE_STATUS_DIR/panes/$1"
	if [ -n "${3:-}" ]; then
		printf '%s\n' "$3" >"$CLAUDE_STATUS_DIR/issues/$1"
	fi
}

@test "claude-status session: appends issue ids after icon" {
	write_pane_fixture 7 work "ENG-123,GH-42"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[ "$status" -eq 0 ]
	[[ $output == *"ENG-123 GH-42"* ]]
}

@test "claude-status session: dedupes ids across panes and caps at 3" {
	write_pane_fixture 7 work "ENG-1,ENG-2"
	write_pane_fixture 8 work "ENG-2,ENG-3,ENG-4,ENG-5"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[[ $output == *"ENG-1 ENG-2 ENG-3 +2"* ]]
}

@test "claude-status session: no issues leaves output unchanged" {
	write_pane_fixture 7 work
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[ "$status" -eq 0 ]
	[[ $output != *"ENG"* ]]
}

@test "claude-status session: other session's issues not shown" {
	write_pane_fixture 7 work "ENG-1"
	write_pane_fixture 8 other "GH-9"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --session work --format icon-color
	[[ $output == *"ENG-1"* ]]
	[[ $output != *"GH-9"* ]]
}

@test "claude-status pane: icon-color shows no issue ids" {
	write_pane_fixture 7 work "ENG-123"
	make_claude_status
	run bash "$CLAUDE_STATUS_SCRIPT" --pane %7 --format icon-color
	[ "$status" -eq 0 ]
	[[ $output != *"ENG-123"* ]]
}

# Stubs tmux so cleanup sees PANE_LIST (TSV of "pane_id<TAB>current_command"
# lines, '%' optional) as the live panes; all other tmux subcommands no-op.
stub_tmux() {
	mkdir -p "$BATS_TEST_TMPDIR/bin"
	# /bin/sh, not /usr/bin/env: the Nix flake-check sandbox has no /usr/bin.
	cat >"$BATS_TEST_TMPDIR/bin/tmux" <<'EOF'
#!/bin/sh
if [ "$1" = list-panes ]; then printf '%s' "$PANE_LIST"; fi
exit 0
EOF
	chmod +x "$BATS_TEST_TMPDIR/bin/tmux"
	PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "cleanup: keeps stamp for a live pane not running claude" {
	write_pane_fixture 7 work "ENG-123"
	stub_tmux
	PANE_LIST=$'%7\tfish\n' bash "$CSU" cleanup
	[ -f "$CLAUDE_STATUS_DIR/issues/7" ]
	[ -f "$CLAUDE_STATUS_DIR/panes/7" ]
}

@test "cleanup: removes stamp for a pane that no longer exists" {
	write_pane_fixture 9 work "GH-42"
	stub_tmux
	PANE_LIST=$'%7\tclaude\n' bash "$CSU" cleanup
	[ ! -f "$CLAUDE_STATUS_DIR/issues/9" ]
	[ ! -f "$CLAUDE_STATUS_DIR/panes/9" ]
}
