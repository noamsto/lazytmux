#!/usr/bin/env bash
# Create a local <host>-<sess> session and launch the M2 multi-window bridge
# daemon detached: it enumerates every remote window and mirrors each into its
# own local window (live add/close/rename/active-changed). Resolves remote
# tmux path + TMUX_TMPDIR for the ssh control connection.
set -euo pipefail

# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
# shellcheck source=/dev/null
[[ -f "@lib_remote@" ]] && source "@lib_remote@"

# shell_quote single-quotes $1, escaping embedded single quotes — correct
# under any POSIX shell or fish for every character except a literal
# backslash (see shell_quotable() in lib-remote.sh for why). Callers must
# clear a value through shell_quotable() before it reaches here; $sess and
# LZTMUX_REMOTE_NEW_DIR are the two that do.
shell_quote() {
	local s="$1"
	s="${s//\'/\'\\\'\'}"
	printf "'%s'" "$s"
}

# reap_daemon SIGTERMs pid, waits up to 2s, then SIGKILLs if it's still
# alive. Used whenever a live daemon has been proven stale so its socket +
# pidfile can be safely removed and a fresh one started.
reap_daemon() {
	local pid="$1"
	kill -TERM -- "$pid" 2>/dev/null || true
	for _ in {1..20}; do
		kill -0 "$pid" 2>/dev/null || return 0
		sleep 0.1
	done
	kill -KILL -- "$pid" 2>/dev/null || true
}

host="$1"
sess="${2:-}"
win="${3:-}"

if [[ -n $win && ! $win =~ ^[0-9]+$ ]]; then
	echo "lztmux-remote-open: window index must be numeric, got: $win" >&2
	exit 1
fi

# Both pre-create a session the caller named, by different means, so honouring
# them together would restore and then create over the result. Checked here
# rather than left to callers: this is a public entry point (the README hands it
# out), so the picker never setting both is not an invariant to rely on.
if [[ -n ${LZTMUX_REMOTE_NEW_DIR:-} && -n ${LZTMUX_REMOTE_RESTORE:-} ]]; then
	echo "lztmux-remote-open: LZTMUX_REMOTE_NEW_DIR and LZTMUX_REMOTE_RESTORE are mutually exclusive" >&2
	exit 1
fi

if [[ -n ${LZTMUX_REMOTE_NEW_DIR:-} ]] && ! shell_quotable "$LZTMUX_REMOTE_NEW_DIR"; then
	echo "lztmux-remote-open: LZTMUX_REMOTE_NEW_DIR contains a backslash, which no remote shell dialect can quote safely: $LZTMUX_REMOTE_NEW_DIR" >&2
	exit 1
fi

# The session-name quoting discipline (shell_quote is unsafe for a value
# containing a backslash) applies here too: check before a caller-given $sess
# rides into the probe below, not only after a remote-derived one comes back.
if [[ -n $sess ]] && ! shell_quotable "$sess"; then
	echo "lztmux-remote-open: session name contains a backslash, which no remote shell dialect can quote safely: $sess — pass an explicit session name instead" >&2
	exit 1
fi

# Prints the host's most-recent session name, or nothing when the remote has no
# tmux server: list-sessions fails into `head`, so the remote pipeline still
# exits 0 with empty output. Used both inside the combined probe below and to
# re-check after a cold start (#429: that round-trip stays separate — the
# server didn't exist yet when the probe ran).
first_remote_session() {
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-sessions -F '#{session_name}' | head -1"
}

# One round-trip for everything the launcher can know before it has to act:
# remote OS (tmpdir default + cold-start service manager), the tmux binary
# path, and — for whichever of session/window the caller didn't already name —
# the live session and its active window. Four sequential probes each cost a
# full SSH handshake; collapsing them into one compound remote command removes
# three round-trips from every bridge open (#429). Marker line first so a test
# double can recognize this call without parsing full shell semantics. Every
# single-quoted probe_script segment below is intentional: it's the
# remote-evaluated half of the command and must not expand locally.
# shellcheck disable=SC2016
probe_script=': lztmux-probe;
os=$(uname -s)
uid=$(id -u)
tmux_bin=$(command -v tmux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux)'

if [[ -n ${LZTMUX_REMOTE_TMPDIR:-} ]]; then
	probe_script+="
tmpdir_lit=$(shell_quote "$LZTMUX_REMOTE_TMPDIR")
tmpdir=\"\$tmpdir_lit\""
else
	# shellcheck disable=SC2016
	probe_script+='
case "$os" in
	Darwin) tmpdir="/tmp/tmux-$uid" ;;
	*) tmpdir="/run/user/$uid" ;;
esac'
fi

if [[ -n $sess ]]; then
	probe_script+="
sess_lit=$(shell_quote "$sess")
sess=\"\$sess_lit\""
else
	# shellcheck disable=SC2016
	probe_script+='
sess=$(env TMUX_TMPDIR="$tmpdir" "$tmux_bin" list-sessions -F '"'"'#{session_name}'"'"' | head -1)'
fi

if [[ -n $win ]]; then
	probe_script+="
win_lit=$(shell_quote "$win")
win=\"\$win_lit\""
else
	# shellcheck disable=SC2016
	probe_script+='
win=""
if [ -n "$sess" ]; then
	win=$(env TMUX_TMPDIR="$tmpdir" "$tmux_bin" list-windows -t "$sess" -F '"'"'#{window_index} #{window_active}'"'"' | awk '"'"'$2==1{print $1; exit}'"'"')
fi'
fi

# shellcheck disable=SC2016
probe_script+='
printf '"'"'os=%s\nuid=%s\ntmux=%s\ntmpdir=%s\nsess=%s\nwin=%s\n'"'"' "$os" "$uid" "$tmux_bin" "$tmpdir" "$sess" "$win"'

# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
probe_out="$(ssh "$host" "$probe_script")"

remote_os="" remote_uid="" remote_tmux="" remote_tmpdir="" probe_sess="" probe_win=""
while IFS= read -r probe_line; do
	case "$probe_line" in
	os=*) remote_os="${probe_line#os=}" ;;
	uid=*) remote_uid="${probe_line#uid=}" ;;
	tmux=*) remote_tmux="${probe_line#tmux=}" ;;
	tmpdir=*) remote_tmpdir="${probe_line#tmpdir=}" ;;
	sess=*) probe_sess="${probe_line#sess=}" ;;
	win=*) probe_win="${probe_line#win=}" ;;
	esac
done <<<"$probe_out"
[[ -z $sess ]] && sess="$probe_sess"
[[ -z $win ]] && win="$probe_win"

# A session already live on the remote (the common case) is named here, by
# the probe above, not by the caller — so it hasn't run the backslash check
# above yet.
if [[ -n $sess ]] && ! shell_quotable "$sess"; then
	echo "lztmux-remote-open: session name contains a backslash, which no remote shell dialect can quote safely: $sess — pass an explicit session name instead" >&2
	exit 1
fi

if ! valid_remote_path "$remote_tmpdir"; then
	echo "lztmux-remote-open: unusable remote tmpdir: $remote_tmpdir" >&2
	exit 1
fi

# Starts the host's OWN startup session — the remote's tmux-startup unit
# carries its configured session name and directory, so nothing is invented
# here. Starting it blind is safe: the systemd unit is Type=forking with
# RemainAfterExit, the launchd agent is RunAtLoad, and both scripts
# exact-match `has-session` before creating anything. Callers must always
# re-probe with first_remote_session afterwards rather than trust this
# returning cleanly — unit state is not server state: a live server can sit
# behind an `inactive` unit (#287), and a dead one behind an `active` unit
# (#345). Exits the whole script on failure: a cold start is a fatal
# precondition for every caller.
start_remote_server() {
	if [[ $remote_os == Darwin ]]; then
		# The launchd agent mirrors tmux-startup.service on macOS; kickstart
		# runs a RunAtLoad agent on demand.
		start_cmd=(launchctl kickstart "gui/$remote_uid/org.nix-community.home.tmux-startup")
		start_desc="tmux-startup launchd agent"
	else
		# `restart`, not `start`: RemainAfterExit keeps the unit `active` after
		# the tmux server it forked has exited, so `start` no-ops on exactly the
		# host this function exists for (#345).
		start_cmd=(systemctl --user restart tmux-startup.service)
		start_desc="tmux-startup.service"
	fi
	if ! ssh "$host" -- "${start_cmd[@]}"; then
		echo "lztmux-remote-open: $host has no tmux server, and no $start_desc to start one" >&2
		exit 1
	fi
}

if [[ -z $sess ]]; then
	start_remote_server
	sess="$(first_remote_session)"
	if [[ -z $sess ]]; then
		echo "lztmux-remote-open: started $start_desc on $host but no session appeared" >&2
		exit 1
	fi
	# A remote-derived name gets the same backslash check the caller-given path
	# already ran above — this is the only route it could have skipped it.
	if ! shell_quotable "$sess"; then
		echo "lztmux-remote-open: session name contains a backslash, which no remote shell dialect can quote safely: $sess — pass an explicit session name instead" >&2
		exit 1
	fi
fi

# The picker's row came from a tmux-remux snapshot, not a live probe (#268):
# the named session may not exist on the remote yet. Only entered when the
# caller explicitly asked for a restore — a plain live-session attach (the
# common case) takes none of these extra round trips.
if [[ -n ${LZTMUX_REMOTE_RESTORE:-} && -n $sess ]]; then
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
		if [[ -z "$(first_remote_session)" ]]; then
			start_remote_server
		fi
		remote_remux="$(ssh "$host" 'command -v tmux-remux 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/tmux-remux')"
		# Bypasses the remote's own restoreMode=off gate (config/tmux.conf.nix's
		# `restore --auto`) on purpose: the user directly asked for this
		# session, not merely for the server to start.
		# tmux-remux shells out to the bare `tmux` binary name (it doesn't know
		# the store path we just resolved), so it needs that directory on its
		# PATH — the same non-interactive-ssh-PATH problem $remote_tmux above
		# already had to work around.
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir PATH=$(dirname "$remote_tmux"):\$PATH $remote_remux restore"; then
			echo "lztmux-remote-open: tmux-remux restore failed on $host" >&2
			exit 1
		fi
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
			# tmux-remux restore can exit 0 having restored nothing: its own
			# smart filter (idle-shells-only sessions, or sessions/snapshots
			# past its age ceiling) runs regardless of what the picker listed
			# (see the design doc's "Restore filter mismatch" section) — name
			# that as the likely cause instead of a bare "not found".
			echo "lztmux-remote-open: session '$sess' was not restored on $host — tmux-remux's restore filter may have skipped it (idle shells / stale age)" >&2
			exit 1
		fi
	fi
fi

# The picker's row was a remote zoxide directory, not a session (#356): the name
# is derived, so nothing by it exists yet. Creation lives here rather than in the
# remote-side picker so there is one creator resolving one socket dir, and so the
# session is made moments before the daemon attaches instead of having to survive
# the whole interactive pick.
if [[ -n ${LZTMUX_REMOTE_NEW_DIR:-} && -n $sess ]]; then
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
		# Both cold-start gates above are `[[ -z $sess ]]`, and we hold a name —
		# so without this the server would be whatever a transient ssh session
		# spawned, outside the startup unit that owns it everywhere else (#345).
		if [[ -z "$(first_remote_session)" ]]; then
			start_remote_server
		fi
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux new-session -d -s $(shell_quote "$sess") -c $(shell_quote "$LZTMUX_REMOTE_NEW_DIR")"; then
			echo "lztmux-remote-open: could not create session '$sess' in '$LZTMUX_REMOTE_NEW_DIR' on $host" >&2
			exit 1
		fi
		# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
		if ! ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux has-session -t $(shell_quote "=$sess")" 2>/dev/null; then
			echo "lztmux-remote-open: session '$sess' was not created on $host" >&2
			exit 1
		fi
	fi
fi

if [[ -z $win ]]; then
	# base-index is non-zero under lazytmux (windows start at 1), so target the
	# session's active window rather than assuming index 0.
	# shellcheck disable=SC2029 # intentional: expand client-side, resolved values ride in the remote command
	win="$(ssh "$host" "env TMUX_TMPDIR=$remote_tmpdir $remote_tmux list-windows -t $(shell_quote "$sess") -F '#{window_index} #{window_active}' | awk '\$2==1{print \$1; exit}'")"
	# A live session always has an active window, and this pipeline can't fail:
	# a failed list-windows still exits 0 into awk with the remote shell carrying
	# none of our pipefail. So empty means the session isn't there, and bridging
	# on would launch the daemon at a blank window index.
	if [[ -z $win ]]; then
		echo "lztmux-remote-open: session '$sess' has no window on $host — it is gone or was never there" >&2
		exit 1
	fi
fi

local_sess="${host}-${sess}"
sock_dir="${TMUX_TMPDIR:-${XDG_RUNTIME_DIR:-/tmp}}"
sock_name="${local_sess//[^A-Za-z0-9._-]/_}"
sock="${sock_dir}/lztmux-daemon-${sock_name}.sock"
# Store paths, substituted at build time. This script runs from the tmux server,
# whose PATH is frozen until a server restart, while the keybinds that reach the
# daemon repoint on a config reload alone — a bare name straddles the two, so
# the daemon can end up older than the ctl talking to it (#336).
# Unsubstituted placeholders keep their leading '@' and fall back to PATH (bats).
ctl="@bridge_ctl@"
daemon="@bridge_daemon@"
renderer="@bridge_renderer@"
reflow="@reflow@"
[[ $ctl == @* ]] && ctl="$(command -v lztmux-remote-bridge-ctl)"
[[ $daemon == @* ]] && daemon="$(command -v lztmux-remote-bridge-daemon)"
[[ $renderer == @* ]] && renderer="$(command -v lztmux-remote-bridge-renderer)"
[[ $reflow == @* ]] && reflow="$(command -v tmux-reflow-windows)"

# Dedup: a live pid alone is not enough. A config reload can leave a daemon
# that speaks an older ctl protocol behind, so prove its compatibility before
# reusing the mirror. ctl bounds the probe at two seconds.
if remote_daemon_alive "${sock}.pid"; then
	if probe_error="$("$ctl" --sock "$sock" ping _ 2>&1)"; then
		if tmux has-session -t "=$local_sess" 2>/dev/null; then
			tmux switch-client -t "=$local_sess"
			exit 0
		fi
		# The daemon is alive and speaks the protocol, but the mirror session it
		# was serving is gone — killed from the picker, by hand, or a crash.
		# switch-client above would otherwise fail against a session
		# that no longer exists, so reap the orphan daemon and fall through to
		# recreate.
		daemon_pid="$(<"${sock}.pid")"
		[[ $daemon_pid =~ ^[0-9]+$ ]] && reap_daemon "$daemon_pid"
	# A stale pidfile can be recycled by an unrelated process. Only the daemon's
	# deterministic old-protocol replies establish that the PID owns this socket;
	# an unreachable socket goes straight to cleanup/recreate without signalling.
	# Matched on the suffix both replies share: pinning the version digits stops
	# reaping the daemon a later bump obsoletes.
	elif [[ $probe_error == *'— reopen the bridge'* ]]; then
		daemon_pid="$(<"${sock}.pid")"
		[[ $daemon_pid =~ ^[0-9]+$ ]] && reap_daemon "$daemon_pid"
	fi
fi
# Stale cleanup: a prior daemon was killed (SIGTERM/SIGKILL) without running
# teardown, leaving socket + pidfile behind. Remove both so the new daemon can
# bind cleanly; the session below is also replaced.
rm -f "$sock" "${sock}.pid"

# The <host>-<sess> session is an ephemeral mirror (the remote is the source of
# truth). Discard any pre-existing one — a stale bridge from a prior run, or a
# ghost resurrected by tmux-remux on restore — so it can't collide with
# new-session ("duplicate session"); =-prefix is exact-match (numeric names).
tmux kill-session -t "=$local_sess" 2>/dev/null || true

# Create the local session with a single initial window; the daemon reuses it
# for the first remote window and creates the rest.
tmux new-session -d -s "$local_sess" -n "$sess"

# Read by tmux-statusline to name the machine on line 0. Session-scoped, so it
# survives the daemon replacing every window under it.
tmux set-option -t "$local_sess" @bridge_host "$host"

# Pass the (remote-derived, untrusted) params through the environment instead
# of interpolating them into a shell/command string tmux/ssh would re-parse,
# so a crafted remote session name can't break out into local shell execution.
export LZTMUX_BRIDGE_HOST="$host"
export LZTMUX_BRIDGE_SESSION="$sess"
export LZTMUX_BRIDGE_WINDOW="$win"
export LZTMUX_BRIDGE_TMUX="$remote_tmux"
export LZTMUX_BRIDGE_TMPDIR="$remote_tmpdir"
export LZTMUX_DAEMON_LOCAL_SESS="$local_sess"
export LZTMUX_DAEMON_SOCK="$sock"
export LZTMUX_DAEMON_RENDERER="$renderer"
export LZTMUX_DAEMON_REFLOW="$reflow"
# This very script, so a hand-off (a remote switch-client the daemon pinned back)
# re-enters the launcher at the revision the daemon itself came from, never
# whatever a later home-manager switch left on PATH (#336).
export LZTMUX_DAEMON_REMOTE_OPEN="${BASH_SOURCE[0]}"

# The remote viewer picks its graphics backend from #{client_termname}, which is
# whatever the daemon's ssh advertises — so hand it the termname of the terminal
# that will actually paint the pixels. Empty (no client) is fine: the remote then
# falls back to block art, which renders anywhere.
term="$(tmux display-message -p '#{client_termname}')"
export LZTMUX_BRIDGE_TERM="$term"

# Launch the daemon DETACHED, outside the panes it manages (I4): it is not the
# window's command — it respawns the local panes into renderers. setsid is
# Linux-only (not on macOS base), so fall back to plain backgrounding + disown
# where it's unavailable; either way the daemon is fully detached from this shell.
if command -v setsid >/dev/null 2>&1; then     # portable-ok: guard, verified fallback below
	setsid "$daemon" >/dev/null 2>"${sock}.log" & # portable-ok: guarded above; else branch is the verified macOS fallback
else
	nohup "$daemon" >/dev/null 2>"${sock}.log" &
	disown
fi

tmux switch-client -t "=$local_sess"
