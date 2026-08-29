#!/usr/bin/env bats
# shellcheck disable=SC2030,SC2031 # bats @test blocks run in subshells; export is intentional
# shellcheck disable=SC2016 # picker_eval snippets expand in the subshell, not here
# The remote-side session picker wrapper (#356). Three roles in one file:
# `--probe` and `--serve` run on the remote, and the bare `<host>` form drives
# the three ssh legs locally and hands the pick to lztmux-remote-open.
#
# ssh, tmux, the picker binary and the launcher are all fakes on PATH; nothing
# here touches a real host, a real tmux server, or a path outside the test tmpdir.

setup() {
	FAKEBIN="$BATS_TEST_TMPDIR/bin"
	mkdir -p "$FAKEBIN"

	export SCRIPT="$PWD/scripts/lztmux-remote-picker.sh"
	export SSH_LOG="$BATS_TEST_TMPDIR/ssh.log"
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	export OPEN_LOG="$BATS_TEST_TMPDIR/open.log"
	export PICKER_LOG="$BATS_TEST_TMPDIR/picker.log"
	: >"$SSH_LOG"
	: >"$TMUX_LOG"
	: >"$OPEN_LOG"
	: >"$PICKER_LOG"

	# Keeps the local role's mktemp work dir and the remote role's emit dir
	# inside the test tmpdir, whatever the caller's environment says.
	export TMPDIR="$BATS_TEST_TMPDIR"
	export XDG_RUNTIME_DIR="$BATS_TEST_TMPDIR/xdg"

	# What leg 1's `--probe` reports by default: a well-formed triple. Cases that
	# exercise the validation override it.
	export FAKE_PROBE="script=/nix/store/fake/bin/lztmux-remote-picker
emit_dir=/run/user/1000/lztmux-pick
tmpdir=/run/user/1000"
	# Empty payload = the human pressed esc. Choice cases override it.
	export FAKE_PAYLOAD=""

	cat >"$FAKEBIN/ssh" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$SSH_LOG"
		case "$*" in
		# Leg 2, the interactive one: bare argv, so it carries --serve.
		*--serve*)
			exit "${FAKE_SERVE_RC:-0}"
			;;
		# Leg 3 collects the emit file: `bash -s --` plus the quoted path.
		*"bash -s --"*)
			[ -n "${FAKE_COLLECT_ERR:-}" ] && printf '%s\n' "$FAKE_COLLECT_ERR" >&2
			[ "${FAKE_COLLECT_RC:-0}" -eq 0 ] && printf '%s' "$FAKE_PAYLOAD"
			exit "${FAKE_COLLECT_RC:-0}"
			;;
		# Leg 1 probes: `bash -s` with the capability heredoc on stdin.
		*)
			[ -n "${FAKE_PROBE_ERR:-}" ] && printf '%s\n' "$FAKE_PROBE_ERR" >&2
			[ "${FAKE_PROBE_RC:-0}" -eq 0 ] && printf '%s\n' "$FAKE_PROBE"
			exit "${FAKE_PROBE_RC:-0}"
			;;
		esac
	EOF

	# fatal() routes its message to the status line; without this stub that would
	# reach the developer's own live server.
	cat >"$FAKEBIN/tmux" <<-'EOF'
		#!/bin/sh
		echo "$*" >>"$TMUX_LOG"
		exit 0
	EOF

	# The handoff target. Logs argv one bracketed field per arg, so a name with a
	# space is distinguishable from two names.
	cat >"$FAKEBIN/lztmux-remote-open" <<-'EOF'
		#!/bin/sh
		printf 'argv:' >>"$OPEN_LOG"
		for a in "$@"; do printf ' [%s]' "$a" >>"$OPEN_LOG"; done
		printf '\n' >>"$OPEN_LOG"
		printf 'tmpdir: [%s]\n' "${LZTMUX_REMOTE_TMPDIR-}" >>"$OPEN_LOG"
		if [ -n "${LZTMUX_REMOTE_NEW_DIR+set}" ]; then
			printf 'newdir: [%s]\n' "$LZTMUX_REMOTE_NEW_DIR" >>"$OPEN_LOG"
		else
			printf 'newdir: unset\n' >>"$OPEN_LOG"
		fi
		[ -n "${FAKE_OPEN_ERR:-}" ] && printf '%s\n' "$FAKE_OPEN_ERR" >&2
		exit "${FAKE_OPEN_RC:-0}"
	EOF

	# What --serve execs. Records the env that decides whether the pick can ever
	# cross back.
	cat >"$FAKEBIN/tmux-picker-generate" <<-'EOF'
		#!/bin/sh
		printf 'argv:' >>"$PICKER_LOG"
		for a in "$@"; do printf ' [%s]' "$a" >>"$PICKER_LOG"; done
		printf '\n' >>"$PICKER_LOG"
		printf 'emit: [%s]\n' "${LZTMUX_PICKER_EMIT-}" >>"$PICKER_LOG"
		printf 'tmux_tmpdir: [%s]\n' "${TMUX_TMPDIR-}" >>"$PICKER_LOG"
		exit 0
	EOF

	chmod +x "$FAKEBIN"/*
	export PATH="$FAKEBIN:$PATH"
}

# Every case runs `bash "$SCRIPT"` rather than executing it: the nix check
# sandbox has no /usr/bin/env, so the `#!/usr/bin/env bash` shebang cannot
# resolve there. writeShellScriptBin rewrites that shebang to a store path
# anyway, so an explicit interpreter is what the shipped script really gets.
#
# stdin comes from /dev/null throughout: fatal() holds the pane with `read` when
# stdin is a tty, and no case may ever be able to block on that.

# Runs a snippet with the wrapper's helpers in scope. Sourcing it with --probe
# reaches the role dispatch, and --probe only prints — no role runs and nothing
# is mutated. $0 stays $SCRIPT, which remote_probe needs to be absolute.
picker_eval() {
	local snippet="$1"
	shift
	run bash -c "source \"\$0\" --probe >/dev/null; $snippet" "$SCRIPT" "$@"
}

# Drives the local role end to end with $1 as the payload leg 3 collects.
pick() {
	export FAKE_PAYLOAD="$1"
	run bash "$SCRIPT" tp-g6 </dev/null
}

file_mode() { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }

# --- Step 11a: the key=value parser -----------------------------------------

@test "kv_get: key order does not matter and unknown keys are ignored" {
	local text="tmpdir=/run/user/1000
noise=whatever
emit_dir=/run/user/1000/lztmux-pick
script=/usr/bin/lztmux-remote-picker"

	picker_eval 'kv_get "$1" script && printf "[%s]" "$KV_VALUE"' "$text"
	[ "$status" -eq 0 ]
	[ "$output" = "[/usr/bin/lztmux-remote-picker]" ]

	picker_eval 'kv_get "$1" tmpdir && printf "[%s]" "$KV_VALUE"' "$text"
	[ "$status" -eq 0 ]
	[ "$output" = "[/run/user/1000]" ]
}

@test "kv_get: a line with no '=' is skipped (a fish greeting rides leg 1's stdout)" {
	# `ssh host bash -s` against a fish login shell is really
	# `fish -c 'bash -s'`, which sources config.fish first.
	local text="Welcome to fish, the friendly interactive shell
script=/usr/bin/lztmux-remote-picker
--- some banner ---
tmpdir=/run/user/1000"

	picker_eval 'kv_get "$1" script && printf "[%s]" "$KV_VALUE"' "$text"
	[ "$status" -eq 0 ]
	[ "$output" = "[/usr/bin/lztmux-remote-picker]" ]
}

@test "kv_get: a value may contain '=', tabs and spaces" {
	local text="name=My Session	v=2
path=/home/x/My Docs=old"

	picker_eval 'kv_get "$1" name && printf "[%s]" "$KV_VALUE"' "$text"
	[ "$status" -eq 0 ]
	[ "$output" = "[My Session	v=2]" ]

	picker_eval 'kv_get "$1" path && printf "[%s]" "$KV_VALUE"' "$text"
	[ "$status" -eq 0 ]
	[ "$output" = "[/home/x/My Docs=old]" ]
}

@test "kv_get: a missing key is rejected, and the last match wins" {
	picker_eval 'kv_get "$1" script' "name=work"
	[ "$status" -ne 0 ]

	# A greeting can precede the line it introduces, so the later value is real.
	picker_eval 'kv_get "$1" name && printf "[%s]" "$KV_VALUE"' "name=first
name=second"
	[ "$status" -eq 0 ]
	[ "$output" = "[second]" ]
}

# --- Step 11a: leg-1 value validation ---------------------------------------

@test "leg 1: a relative script path is refused by name" {
	export FAKE_PROBE="script=bin/lztmux-remote-picker
emit_dir=/run/user/1000/lztmux-pick
tmpdir=/run/user/1000"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"unusable script"* ]]
	[[ $output == *"bin/lztmux-remote-picker"* ]]

	# Refused before leg 2 ever runs.
	run grep -c -- --serve "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "leg 1: an emit_dir carrying a shell metacharacter is refused by name" {
	export FAKE_PROBE="script=/usr/bin/lztmux-remote-picker
emit_dir=/run/user/1000/lztmux-pick;id
tmpdir=/run/user/1000"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"unusable emit_dir"* ]]
	run grep -c -- --serve "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "leg 1: a tmpdir with whitespace or metacharacters is refused — it crosses unquoted" {
	# LZTMUX_REMOTE_TMPDIR is interpolated *unquoted* into lztmux-remote-open's
	# remote command strings, so this value is the injection seam.
	export FAKE_PROBE="script=/usr/bin/lztmux-remote-picker
emit_dir=/run/user/1000/lztmux-pick
tmpdir=/run/user/1000 x"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"unusable tmpdir"* ]]

	export FAKE_PROBE='script=/usr/bin/lztmux-remote-picker
emit_dir=/run/user/1000/lztmux-pick
tmpdir=/run/user/$(id -u)'

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"unusable tmpdir"* ]]

	[ ! -s "$OPEN_LOG" ]
}

@test "leg 1: a probe missing a required key is refused by name" {
	export FAKE_PROBE="script=/usr/bin/lztmux-remote-picker
tmpdir=/run/user/1000"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"reported no emit_dir"* ]]
	run grep -c -- --serve "$SSH_LOG"
	[ "$status" -ne 0 ]
}

# --- Step 11b: the remote roles ---------------------------------------------

@test "--probe reports absolute paths and mutates nothing" {
	local expect_tmpdir
	if [[ "$(uname -s)" == Darwin ]]; then
		expect_tmpdir="/tmp/tmux-$(id -u)"
	else
		expect_tmpdir="/run/user/$(id -u)"
	fi

	run bash "$SCRIPT" --probe </dev/null
	[ "$status" -eq 0 ]

	[[ $output == *"script=$SCRIPT"* ]]
	[[ $output == *"emit_dir=$XDG_RUNTIME_DIR/lztmux-pick"* ]]
	[[ $output == *"tmpdir=$expect_tmpdir"* ]]

	# Resolution only: the emit dir is --serve's job, and a probe that created it
	# would leave a directory behind on every host the picker ever asks.
	[ ! -e "$XDG_RUNTIME_DIR" ]
}

@test "--serve rejects a token that could escape the path join" {
	local token
	for token in "../../etc" "a b" "tok;id" "" "tok/sub"; do
		run bash "$SCRIPT" --serve "$token" </dev/null
		[ "$status" -eq 1 ]
		[[ $output == *"invalid emit token"* ]]
	done

	[ ! -e "$XDG_RUNTIME_DIR/lztmux-pick" ]
	[ ! -s "$PICKER_LOG" ]
}

@test "--serve exits 4 on an emit dir it cannot make private" {
	# `mkdir -m 700 -p` neither applies the mode to an existing directory nor
	# checks its owner, so --serve asserts instead. A no-op chmod stands in for
	# the mode correction failing (an existing dir of another uid, unreachable
	# in a test that runs as one user).
	mkdir -p "$XDG_RUNTIME_DIR/lztmux-pick"
	chmod 755 "$XDG_RUNTIME_DIR/lztmux-pick"
	local nochmod="$BATS_TEST_TMPDIR/nochmod"
	mkdir -p "$nochmod"
	printf '#!/bin/sh\nexit 0\n' >"$nochmod/chmod"
	chmod +x "$nochmod/chmod"
	export PATH="$nochmod:$PATH"

	run bash "$SCRIPT" --serve deadbeef </dev/null
	[ "$status" -eq 4 ]
	[[ $output == *"is not a private directory of ours"* ]]

	# Refused before the picker could open on an emit target it cannot trust.
	[ ! -s "$PICKER_LOG" ]
}

@test "--serve execs the picker with LZTMUX_PICKER_EMIT and a pre-created emit file" {
	run bash "$SCRIPT" --serve deadbeef01 </dev/null
	[ "$status" -eq 0 ]

	local emit="$XDG_RUNTIME_DIR/lztmux-pick/deadbeef01"
	# LZTMUX_PICKER_EMIT is the only thing that gives the picker an emit target;
	# without it every selection is silently dropped.
	grep -qF "emit: [$emit]" "$PICKER_LOG"
	grep -qF 'argv: [--tui] [--remote-pick]' "$PICKER_LOG"
	grep -q 'tmux_tmpdir: \[/' "$PICKER_LOG"

	[ "$(file_mode "$XDG_RUNTIME_DIR/lztmux-pick")" = 700 ]
	# Pre-created, so an unwritable target fails here rather than reading back as
	# a cancel, and empty, so the local side's discriminator still works.
	[ -f "$emit" ]
	[ ! -s "$emit" ]
	[ "$(file_mode "$emit")" = 600 ]
}

# --- Step 12: the local role's three legs -----------------------------------

@test "the three legs: probe, interactive serve, collect — flags and token all round-trip" {
	pick "kind=session
name=work"
	[ "$status" -eq 0 ]

	# Leg 1: non-interactive, bounded, and the capability heredoc on stdin.
	grep -q 'BatchMode=yes' "$SSH_LOG"
	grep -q 'ConnectTimeout=2' "$SSH_LOG"
	grep -q -- '-T tp-g6 bash -s$' "$SSH_LOG"

	# Leg 2: -t, bare argv (no `var=value` prefix fish would not parse), and the
	# script path the probe reported — never a locally guessed one.
	local token
	token="$(sed -n 's/.*--serve //p' "$SSH_LOG")"
	[[ $token =~ ^[A-Za-z0-9]+$ ]]
	grep -qF -- "-t tp-g6 -- /nix/store/fake/bin/lztmux-remote-picker --serve $token" "$SSH_LOG"

	# Leg 3 reads the probe's emit_dir joined with that same token.
	grep -qF -- "-T tp-g6 bash -s -- '/run/user/1000/lztmux-pick/$token'" "$SSH_LOG"
}

@test "leg 1: a timeout is reported as a timeout, not as a failure" {
	export FAKE_PROBE_RC=124 # what timeout(1) exits when it fires

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: timed out"* ]]
}

@test "leg 1: an unreachable host is named" {
	export FAKE_PROBE_RC=255

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: unreachable"* ]]
}

@test "leg 1: a remote too old to answer --probe says so instead of hanging" {
	# The heredoc resolves but never executes an unproven script: an older picker
	# would ignore --probe and start its TUI, which over ssh is a hang.
	export FAKE_PROBE_RC=3

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"remote lazytmux too old — rebuild tp-g6"* ]]

	run grep -c -- --serve "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "leg 1: an unusable remote emit dir is named" {
	export FAKE_PROBE_RC=4

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: emit dir unusable"* ]]
}

@test "leg 1: any other failure surfaces the remote's last non-empty stderr line" {
	export FAKE_PROBE_RC=7
	export FAKE_PROBE_ERR="bash: line 1: warning

bash: lztmux-remote-picker: Permission denied"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"lztmux-remote-picker: Permission denied"* ]]

	# With no stderr at all the status still has to reach the human.
	unset FAKE_PROBE_ERR
	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"remote command failed (status 7)"* ]]
}

@test "leg 2: exit 4 is an emit dir the remote could not make private" {
	export FAKE_SERVE_RC=4

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: emit dir unusable"* ]]

	# Never collected, so a stale file can't be read as this run's pick.
	run grep -c -- 'bash -s --' "$SSH_LOG"
	[ "$status" -ne 0 ]
}

@test "leg 2: 255 is a lost connection, not a fresh unreachable" {
	export FAKE_SERVE_RC=255

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: connection lost"* ]]
}

@test "leg 2: any other status is reported with the status — its stderr went to the pty" {
	export FAKE_SERVE_RC=9

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"remote picker failed on tp-g6 (status 9)"* ]]
}

@test "leg 3: a timeout collecting the pick is reported as one" {
	export FAKE_COLLECT_RC=124

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: timed out"* ]]
	[ ! -s "$OPEN_LOG" ]
}

@test "leg 3: any other failure surfaces the last non-empty stderr line" {
	export FAKE_COLLECT_RC=1
	export FAKE_COLLECT_ERR="cat: /run/user/1000/lztmux-pick/x: Permission denied
"

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"Permission denied"* ]]
	[ ! -s "$OPEN_LOG" ]
}

# --- Step 13: cancel vs. choice vs. failure ---------------------------------

@test "cancel: an empty emit file exits 0 silently and hands off nothing" {
	# The file is pre-created by --serve, so existence proves nothing: only
	# content distinguishes a pick from esc/q/^c.
	pick ""
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	[ ! -s "$OPEN_LOG" ]
	[ ! -s "$TMUX_LOG" ] # no error message either
}

@test "cancel: a whitespace-only payload is still a cancel" {
	pick "

	"
	[ "$status" -eq 0 ]
	[ -z "$output" ]
	[ ! -s "$OPEN_LOG" ]
}

@test "choice: a non-empty payload is a pick and reaches the launcher" {
	pick "kind=session
name=work"
	[ "$status" -eq 0 ]
	[[ $output == *"Opening work on tp-g6"* ]]
	grep -qF 'argv: [tp-g6] [work]' "$OPEN_LOG"
}

@test "failure: a failed collect is an error, never a cancel" {
	export FAKE_COLLECT_RC=255

	run bash "$SCRIPT" tp-g6 </dev/null
	[ "$status" -eq 1 ]
	[[ $output == *"tp-g6: unreachable"* ]]
	[ ! -s "$OPEN_LOG" ]
}

# --- Step 14: the handoff ---------------------------------------------------

@test "handoff: kind=session runs the launcher as <host> <name>, no LZTMUX_REMOTE_NEW_DIR" {
	pick "kind=session
name=My Session"
	[ "$status" -eq 0 ]

	grep -qF 'argv: [tp-g6] [My Session]' "$OPEN_LOG"
	grep -qF 'newdir: unset' "$OPEN_LOG"
	# The tmpdir the *remote* resolved, not a local guess — the launcher would
	# otherwise pay for its own round trip, or resolve the wrong OS's socket dir.
	grep -qF 'tmpdir: [/run/user/1000]' "$OPEN_LOG"
}

@test "handoff: kind=dir adds LZTMUX_REMOTE_NEW_DIR and keeps the session name" {
	pick "kind=dir
path=/srv/my proj
name=proj"
	[ "$status" -eq 0 ]

	grep -qF 'argv: [tp-g6] [proj]' "$OPEN_LOG"
	grep -qF 'newdir: [/srv/my proj]' "$OPEN_LOG"
	grep -qF 'tmpdir: [/run/user/1000]' "$OPEN_LOG"
}

@test "handoff: an unrecognised kind refuses instead of bridging a bare <host> <name>" {
	pick "kind=window
name=work"
	[ "$status" -eq 1 ]
	[[ $output == *"unrecognised pick kind 'window'"* ]]
	[ ! -s "$OPEN_LOG" ]
}

@test "handoff: a pick with no kind, no name, an empty name or a dir with no path is refused" {
	pick "name=work"
	[ "$status" -eq 1 ]
	[[ $output == *"unreadable pick"* ]]

	pick "kind=session"
	[ "$status" -eq 1 ]
	[[ $output == *"carried no session name"* ]]

	pick "kind=session
name="
	[ "$status" -eq 1 ]
	[[ $output == *"empty session name"* ]]

	pick "kind=dir
name=proj"
	[ "$status" -eq 1 ]
	[[ $output == *"dir pick carried no path"* ]]

	pick "kind=dir
name=proj
path="
	[ "$status" -eq 1 ]
	[[ $output == *"empty path"* ]]

	[ ! -s "$OPEN_LOG" ]
}

@test "handoff: the launcher's own failure surfaces its last non-empty stderr line" {
	export FAKE_OPEN_RC=1
	export FAKE_OPEN_ERR="lztmux-remote-open: session 'work' was not created on tp-g6"

	pick "kind=session
name=work"
	[ "$status" -eq 1 ]
	[[ $output == *"was not created on tp-g6"* ]]
	# A destroyed floating pane makes pane output unobservable, so the message
	# also has to reach the status line.
	grep -q "display-message" "$TMUX_LOG"

	export FAKE_OPEN_RC=3
	unset FAKE_OPEN_ERR
	pick "kind=session
name=work"
	[ "$status" -eq 1 ]
	[[ $output == *"lztmux-remote-open failed on tp-g6 (status 3)"* ]]
}

@test "AC3: an already-bridged session is the launcher's call — the wrapper hands off unchanged" {
	# The wrapper owns no bridge state: it asks nothing about existing mirrors and
	# passes the pick through verbatim, so focusing an existing mirror is decided
	# once, in lztmux-remote-open (covered by tests/remote-cold-start.bats's
	# "a host that already has a session is never cold-started").
	pick "kind=session
name=workstation"
	[ "$status" -eq 0 ]

	grep -qF 'argv: [tp-g6] [workstation]' "$OPEN_LOG"
	run grep -cE 'has-session|list-sessions|new-session' "$SSH_LOG"
	[ "$status" -ne 0 ]
	[ ! -s "$TMUX_LOG" ]
}
