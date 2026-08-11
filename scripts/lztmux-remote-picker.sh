#!/usr/bin/env bash
# Opens a remote host's OWN session picker in a local floating pane (#356), and
# hands the pick to lztmux-remote-open — the only supported bridging path.
#
# Dual-role, one file, so the capability probe target and the thing it probes
# for can never be different builds:
#
#   lztmux-remote-picker <host>        local  — drives three ssh legs, then the
#                                              handoff
#   lztmux-remote-picker --probe       remote — reports resolved paths, mutates
#                                              nothing
#   lztmux-remote-picker --serve <tok> remote — prepares the emit file, execs
#                                              the picker in emit mode
set -euo pipefail

# Store paths, substituted at Nix build time. This script is spawned by the tmux
# server, whose PATH is frozen until a server restart, so a bare name can miss a
# sibling that a config reload already repointed at (#336). Unsubstituted
# placeholders keep their leading '@' and fall back to PATH (bats).
remote_open="@remote_open@"
picker_generate="@picker_generate@"
zoxide_bin="@zoxide@/bin"

# BatchMode on the interactive leg too: the bridge already requires
# non-interactive auth, and without it a key-less host parks a password prompt
# in a floating pane — a hang, not a message.
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=2)

# shell_quote single-quotes $1 for a POSIX shell (escaping embedded quotes),
# mirroring lztmux-remote-open — remote-derived paths must not break out.
shell_quote() { printf "'%s'" "${1//\'/\'\\\'\'}"; }

# Parses `key=value` out of $1; the value is everything after the first `=`, so
# spaces and tabs are safe. Lines with no `=` are SKIPPED: `ssh host bash -s` is
# still `fish -c 'bash -s'`, which sources config.fish, so a login greeting
# rides on the same stdout. Last match wins — a greeting precedes the command it
# introduces, so the later value is the real one.
KV_VALUE=""
kv_get() {
	local text="$1" want="$2" line found=1
	KV_VALUE=""
	while IFS= read -r line || [[ -n $line ]]; do
		[[ $line == *=* ]] || continue
		[[ ${line%%=*} == "$want" ]] || continue
		KV_VALUE="${line#*=}"
		found=0
	done <<<"$text"
	return "$found"
}

last_non_empty_line() {
	local line last=""
	while IFS= read -r line || [[ -n $line ]]; do
		[[ -n ${line//[[:space:]]/} ]] && last="$line"
	done <"$1"
	printf '%s\n' "$last"
}

# Mirrors lztmux-remote-open's client-side resolver, run here on the host that
# owns the answer: the OS decides the tmux socket dir.
resolve_tmpdir() {
	local uid
	uid="$(id -u)"
	if [[ "$(uname -s)" == Darwin ]]; then
		printf '/tmp/tmux-%s\n' "$uid"
	else
		printf '/run/user/%s\n' "$uid"
	fi
}

resolve_tmux() {
	command -v tmux 2>/dev/null || printf '/etc/profiles/per-user/%s/bin/tmux\n' "$(id -un)"
}

emit_dir_path() { printf '%s\n' "${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/lztmux-pick"; }

# A floating pane is destroyed when its command exits and nothing sets
# remain-on-exit, so pane output at exit is unobservable: the message has to
# reach the status line, and the pane has to be held so it can be read.
fatal() {
	printf 'lztmux-remote-picker: %s\n' "$1" >&2
	tmux display-message "lztmux-remote-picker: $1" 2>/dev/null || true
	printf 'Press any key to close.\n'
	[[ -t 0 ]] && read -r -n 1 -s
	exit 1
}

# Legs 1 and 3 are non-interactive, so their stderr is capturable and the
# taxonomy can be specific. Leg 2 has its own, shorter one: `-t` merges its
# stderr into the pty, leaving only an exit status.
leg_fatal() {
	local host="$1" rc="$2" errfile="$3" msg
	case "$rc" in
	124) fatal "$host: timed out" ;; # timeout(1) fired
	255) fatal "$host: unreachable" ;;
	3) fatal "remote lazytmux too old — rebuild $host" ;;
	4) fatal "$host: emit dir unusable" ;;
	esac
	msg="$(last_non_empty_line "$errfile")"
	fatal "${msg:-$host: remote command failed (status $rc)}"
}

# Absolute, and free of whitespace and shell metacharacters: `script` crosses to
# the remote's login shell (fish) as bare argv, and lztmux-remote-open
# interpolates LZTMUX_REMOTE_TMPDIR *unquoted* into its remote command strings.
valid_remote_path() { [[ $1 =~ ^/[A-Za-z0-9._/@+:-]*$ ]]; }

remote_probe() {
	local self="$0"
	[[ $self != /* ]] && self="$(command -v -- "$self")"
	printf 'script=%s\n' "$self"
	printf 'emit_dir=%s\n' "$(emit_dir_path)"
	printf 'tmpdir=%s\n' "$(resolve_tmpdir)"
}

remote_serve() {
	local token="$1" emit_dir emit mode found
	# Validated before it is joined onto a path, so it can never escape it.
	if [[ ! $token =~ ^[A-Za-z0-9]+$ ]]; then
		echo "lztmux-remote-picker: invalid emit token" >&2
		exit 1
	fi

	emit_dir="$(emit_dir_path)"
	# -m applies only when mkdir creates; -p calls an existing directory success
	# and checks neither its mode nor its owner. XDG_RUNTIME_DIR is absent on
	# macOS, so this can land in a shared /tmp, where another uid's
	# lztmux-pick would fail EACCES inside the picker — long past the point
	# where it could be reported. Assert instead of trusting.
	mkdir -p "$emit_dir"
	# chmod separately rather than via `mkdir -m`: -m applies only on create, so a
	# fresh dir under the default umask would come out 755 and fail our own assert
	# below, and an existing one of ours would keep whatever mode it had.
	[[ -O $emit_dir ]] && chmod 700 "$emit_dir"
	mode="$(stat -c '%a' "$emit_dir" 2>/dev/null || stat -f '%Lp' "$emit_dir")"
	if [[ ! -O $emit_dir || $mode != 700 ]]; then
		echo "lztmux-remote-picker: $emit_dir is not a private directory of ours" >&2
		exit 4
	fi
	# The only owner of token-file cleanup: a local trap cannot reach the remote,
	# and the collect leg removes only the file it read.
	find "$emit_dir" -maxdepth 1 -type f -mmin +60 -delete 2>/dev/null || true

	emit="$emit_dir/$token"
	# Pre-created and probed for writability here so an unwritable emit target is
	# a non-zero exit rather than an empty file the local side reads as a cancel.
	: >"$emit"
	chmod 600 "$emit"
	if [[ ! -w $emit ]]; then
		echo "lztmux-remote-picker: cannot write $emit" >&2
		exit 1
	fi

	[[ $picker_generate == @* ]] && picker_generate="$(command -v tmux-picker-generate)"
	if [[ $zoxide_bin == @* ]]; then
		found="$(command -v zoxide 2>/dev/null || true)"
		zoxide_bin="$(dirname "${found:-/nonexistent/zoxide}")"
	fi
	# The picker calls `tmux` and `zoxide` by bare name and an ssh session has no
	# lazytmux PATH. @zoxide@ ships from the same store path as this script, so
	# the two cannot drift.
	exec env TMUX_TMPDIR="$(resolve_tmpdir)" LZTMUX_PICKER_EMIT="$emit" \
		PATH="$zoxide_bin:$(dirname "$(resolve_tmux)"):$PATH" \
		"$picker_generate" --tui --remote-pick
}

# Deliberately not `local` to local_pick: the EXIT trap below is evaluated after
# that function has returned, where a local is already out of scope — under
# `set -u` that turns every *successful* pick into an exit 1 with a spurious
# "unbound variable", long after the bridge was opened.
work=""

local_pick() {
	local host="$1"
	local probe_out payload rc key script emit_dir tmpdir token kind name msg
	local open_env=()

	work="$(mktemp -d "${TMPDIR:-/tmp}/lztmux-remote-picker.XXXXXX")"
	trap 'rm -rf "$work"' EXIT

	# 16 hex chars: matches --serve's ^[A-Za-z0-9]+$, and od reads a fixed count
	# so there is no SIGPIPE-through-pipefail hazard.
	token="$(od -An -tx1 -N8 </dev/urandom | tr -d ' \n')"

	probe_out="$(
		timeout 8 ssh "${SSH_OPTS[@]}" -T "$host" bash -s 2>"$work/leg1.err" <<-'PROBE'
			# Resolve, never execute, until [ -x ] proves the capability: an older
			# picker binary ignores unknown flags and would start its TUI instead
			# of answering, which over ssh is a hang.
			script="$(command -v lztmux-remote-picker 2>/dev/null || echo /etc/profiles/per-user/$(id -un)/bin/lztmux-remote-picker)"
			[ -x "$script" ] || exit 3
			exec "$script" --probe
		PROBE
	)" && rc=0 || rc=$?
	((rc == 0)) || leg_fatal "$host" "$rc" "$work/leg1.err"

	for key in script emit_dir tmpdir; do
		kv_get "$probe_out" "$key" || fatal "$host: probe reported no $key"
		printf -v "$key" '%s' "$KV_VALUE"
		valid_remote_path "${!key}" || fatal "$host: probe reported an unusable $key: ${!key}"
	done

	# Bare argv, no `var=value` prefix: the remote login shell is fish, which
	# does not parse a leading assignment. Unbounded — the human is browsing.
	ssh "${SSH_OPTS[@]}" -t "$host" -- "$script" --serve "$token" && rc=0 || rc=$?
	case "$rc" in
	0) ;;
	4) fatal "$host: emit dir unusable" ;;
	255) fatal "$host: connection lost" ;;
	*) fatal "remote picker failed on $host (status $rc)" ;;
	esac

	payload="$(
		timeout 8 ssh "${SSH_OPTS[@]}" -T "$host" "bash -s -- $(shell_quote "$emit_dir/$token")" \
			2>"$work/leg3.err" <<-'COLLECT'
				[ -f "$1" ] || exit 0
				cat -- "$1"
				rm -f -- "$1"
			COLLECT
	)" && rc=0 || rc=$?
	((rc == 0)) || leg_fatal "$host" "$rc" "$work/leg3.err"

	# The emit file is pre-created, so its existence proves nothing: only
	# non-empty content distinguishes a choice from esc/q/^c.
	[[ -n ${payload//[[:space:]]/} ]] || exit 0

	kv_get "$payload" kind || fatal "$host: remote picker returned an unreadable pick"
	kind="$KV_VALUE"
	kv_get "$payload" name || fatal "$host: pick carried no session name"
	name="$KV_VALUE"
	[[ -n $name ]] || fatal "$host: pick carried an empty session name"

	open_env=("LZTMUX_REMOTE_TMPDIR=$tmpdir")
	case "$kind" in
	session) ;;
	dir)
		kv_get "$payload" path || fatal "$host: dir pick carried no path"
		[[ -n $KV_VALUE ]] || fatal "$host: dir pick carried an empty path"
		open_env+=("LZTMUX_REMOTE_NEW_DIR=$KV_VALUE")
		;;
	*) fatal "$host: unrecognised pick kind '$kind'" ;;
	esac

	[[ $remote_open == @* ]] && remote_open="$(command -v lztmux-remote-open)"
	# The launcher makes three more round trips before switch-client; without a
	# note the floating pane just sits blank.
	printf 'Opening %s on %s…\n' "$name" "$host"
	# A child, not exec: the launcher's fatals would otherwise print into a pane
	# destroyed microseconds later. No backgrounding either — killing the pane
	# must SIGHUP the whole chain so no ssh is orphaned.
	env "${open_env[@]}" "$remote_open" "$host" "$name" 2>"$work/open.err" && rc=0 || rc=$?
	if ((rc != 0)); then
		msg="$(last_non_empty_line "$work/open.err")"
		fatal "${msg:-lztmux-remote-open failed on $host (status $rc)}"
	fi
}

case "${1:-}" in
--probe) remote_probe ;;
--serve) remote_serve "${2:-}" ;;
"" | -*)
	echo "usage: lztmux-remote-picker <host> | --probe | --serve <token>" >&2
	exit 1
	;;
*) local_pick "$1" ;;
esac
