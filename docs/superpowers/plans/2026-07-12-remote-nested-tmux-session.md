# Remote Nested tmux Session Promotion — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user who ssh'd into a lztmux host run `tmux`, be detected as nested-over-ssh, and promote the remote session into a real local session `HOST-<session>` they are switched into — via a restricted bash+socat control socket.

**Architecture:** Architecture C (reverse-socket push). A local bash+socat listener (systemd user service) owns the only channel that touches local tmux and accepts a two-verb protocol (`hello`, `promote`). ssh_config forwards that listener socket to opted-in remote hosts and round-trips `TMUX_PANE`. A `tmux` shell shim on the remote detects nesting, handshakes the listener, prompts, and sends `promote`. All the trust logic lives in the listener's validation.

**Tech Stack:** bash (scripts, matching repo convention), socat (new closure dep), bats (tests), Nix (home-manager module + new NixOS companion module), tmux.

## Global Constraints

- Scripts are **bash**, not fish (they run in tmux's env). Copied verbatim from the spec/CLAUDE.md. — `config/tmux.conf.nix`
- **shfmt uses tabs** for indentation (project default). All bash uses tab indentation.
- **shellcheck must pass** on every shell script (project rule); run before considering a task done.
- Pure logic lives in a `lib-*.sh` sourced library and is **unit-tested in bats** run via `nix flake check` (mirror `tests/enrich.bats` / `scripts/lib-enrich.sh`).
- Functions in hot paths use the **`REPLY` variable pattern** (set `REPLY`, don't echo) to avoid subshell forks — match `lib-enrich.sh`.
- New scripts are registered in `config/tmux.conf.nix` via `mkScript` (`:187`) and land in the `scripts` attrset (`:369`).
- The feature is **off by default**: `programs.lazytmux.remote.enable = false`.
- Protocol version constant: `LZTMUX_PROTO_VERSION=1`.
- Local listener socket path: `$HOME/.local/state/lztmux/listener.sock`.
- Remote forwarded socket path: `/tmp/lztmux-outer-$USER.sock` (ssh_config writes it as `/tmp/lztmux-outer-%r.sock`).
- Field charset: `host`/`session` match `^[A-Za-z0-9._-]{1,64}$`; pane matches `^%[0-9]+$`.

---

## File Structure

- **Create** `scripts/lib-remote.sh` — pure, sourced logic: env gate, field validation, session-name composition, proto version. Sourced by shim, listener, and bats.
- **Create** `scripts/lztmux-listener.sh` — socat handler: parse a connection, validate, resolve client, run promote choreography. The only code that mutates local tmux.
- **Create** `scripts/lztmux-remote-shim.sh` — remote `tmux` wrapper: env gate, handshake, prompt (+ per-host memory), send `promote`, then `exec` real tmux.
- **Create** `tests/remote.bats` — bats over `lib-remote.sh` and the listener's parse/validate/client-resolution (with a fake `tmux` on PATH).
- **Create** `tests/remote-integration.bats` — real tmux server + fake remote socket exercising the promote choreography and refusals.
- **Create** `modules/nixos.nix` — companion NixOS module: `services.openssh.settings.AcceptEnv`.
- **Modify** `config/tmux.conf.nix` — register the three scripts, add `socat` and `lib-remote` wiring.
- **Modify** `modules/home-manager.nix` — `programs.lazytmux.remote` options, listener systemd service, ssh_config block, remote `tmux` shell function.
- **Modify** `flake.nix` — export `nixosModules.default`.

---

## Task 1: `lib-remote.sh` — pure logic + unit tests

**Files:**
- Create: `scripts/lib-remote.sh`
- Test: `tests/remote.bats`

**Interfaces:**
- Produces:
  - `LZTMUX_PROTO_VERSION` (integer constant, currently `1`)
  - `remote_env_gate` — reads env `SSH_CONNECTION`, `TMUX`, `TMUX_PANE`; returns 0 iff `SSH_CONNECTION` non-empty **and** `TMUX` empty **and** `TMUX_PANE` matches `^%[0-9]+$`.
  - `remote_validate_field VALUE` — returns 0 iff `VALUE` matches `^[A-Za-z0-9._-]{1,64}$`.
  - `remote_validate_pane VALUE` — returns 0 iff `VALUE` matches `^%[0-9]+$`.
  - `remote_session_name HOST SESSION` — sets `REPLY="$HOST-$SESSION"` (inputs pre-validated by caller).

- [ ] **Step 1: Write the failing test**

Create `tests/remote.bats`:

```bash
#!/usr/bin/env bats

setup() {
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
}

@test "env gate passes when ssh + not in remote tmux + valid pane" {
	SSH_CONNECTION="1.2.3.4 5 6.7.8.9 22" TMUX="" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -eq 0 ]
}

@test "env gate fails when not over ssh" {
	SSH_CONNECTION="" TMUX="" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "env gate fails when already inside remote tmux" {
	SSH_CONNECTION="x" TMUX="/tmp/tmux-1000/default,7,0" TMUX_PANE="%5" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "env gate fails when pane absent (ssh'd from outside local tmux)" {
	SSH_CONNECTION="x" TMUX="" TMUX_PANE="" run remote_env_gate
	[ "$status" -ne 0 ]
}

@test "field validation accepts good names, rejects metacharacters" {
	run remote_validate_field "my-host_1.2"
	[ "$status" -eq 0 ]
	run remote_validate_field 'a;rm -rf'
	[ "$status" -ne 0 ]
	run remote_validate_field ""
	[ "$status" -ne 0 ]
}

@test "pane validation accepts %N only" {
	run remote_validate_pane "%12"
	[ "$status" -eq 0 ]
	run remote_validate_pane "12"
	[ "$status" -ne 0 ]
	run remote_validate_pane '%1;x'
	[ "$status" -ne 0 ]
}

@test "session name composes host-session" {
	remote_session_name "web01" "mono"
	[ "$REPLY" = "web01-mono" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd <worktree> && nix develop -c bats tests/remote.bats`
Expected: FAIL — `lib-remote.sh` not found / functions undefined.

- [ ] **Step 3: Write minimal implementation**

Create `scripts/lib-remote.sh`:

```bash
#!/usr/bin/env bash
# Pure logic for the remote nested-session feature. Sourced by the shim, the
# listener, and bats. No side effects; hot-path helpers set REPLY.

LZTMUX_PROTO_VERSION=1

# remote_env_gate: is this a fresh remote shell that was launched from inside a
# local tmux over ssh? SSH_CONNECTION proves ssh; empty TMUX proves we are not
# already inside the remote server; a %N TMUX_PANE is the forwarded local pane
# id (only present when SendEnv had something to send, i.e. we were in local
# tmux). This is the real nested signal — do not gate on a marker constant.
remote_env_gate() {
	[[ -n ${SSH_CONNECTION:-} ]] || return 1
	[[ -z ${TMUX:-} ]] || return 1
	[[ ${TMUX_PANE:-} =~ ^%[0-9]+$ ]] || return 1
	return 0
}

remote_validate_field() {
	[[ $1 =~ ^[A-Za-z0-9._-]{1,64}$ ]]
}

remote_validate_pane() {
	[[ $1 =~ ^%[0-9]+$ ]]
}

remote_session_name() {
	REPLY="$1-$2"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `nix develop -c bats tests/remote.bats`
Expected: PASS (7 tests).

- [ ] **Step 5: shellcheck + commit**

```bash
nix develop -c shellcheck scripts/lib-remote.sh
git add scripts/lib-remote.sh tests/remote.bats
git commit -m "feat(remote): pure lib for env gate + field validation"
```

---

## Task 2: `lztmux-listener.sh` — protocol handler + validation

**Files:**
- Create: `scripts/lztmux-listener.sh`
- Test: `tests/remote.bats` (append)

**Interfaces:**
- Consumes: everything from `lib-remote.sh` (Task 1).
- Produces (all defined in `lztmux-listener.sh`, sourced by bats via a `LZTMUX_LISTENER_LIB=1` guard that skips the socat main loop):
  - `listener_pane_is_ssh PANE` — returns 0 iff `tmux display-message -p -t PANE '#{pane_current_command}'` equals `ssh`.
  - `listener_resolve_client PANE` — sets `REPLY` to the single client attached to `PANE`'s origin session; returns non-zero (and leaves `REPLY` empty) when zero or more than one client is attached.
  - `listener_handle_line LINE` — parses one protocol line, sets `REPLY` to the reply string, returns 0 on a handled verb, non-zero on refusal; does NOT run tmux mutations (those live in `listener_promote`, exercised in Task 6).

- [ ] **Step 1: Write the failing tests (append to `tests/remote.bats`)**

```bash
setup_tmux_fake() {
	FAKE_BIN="$(mktemp -d)"
	cat >"$FAKE_BIN/tmux" <<'EOF'
#!/usr/bin/env bash
# Fake tmux driven by files the test writes:
#   $FAKE_STATE/cmd_<pane>   -> pane_current_command
#   $FAKE_STATE/sess_<pane>  -> session_name
#   $FAKE_STATE/clients_<session> -> newline list of client names
case "$1" in
display-message)
	pane="${*: -1}"
	fmt=""
	for a in "$@"; do case "$a" in "#{pane_current_command}") fmt=cmd ;; "#{session_name}") fmt=sess ;; esac; done
	cat "$FAKE_STATE/${fmt}_${pane}" 2>/dev/null
	;;
list-clients)
	# args: list-clients -t <session> -F <fmt>
	sess=""; for ((i=1;i<=$#;i++)); do [[ ${!i} == -t ]] && { j=$((i+1)); sess="${!j}"; }; done
	cat "$FAKE_STATE/clients_${sess}" 2>/dev/null
	;;
esac
EOF
	chmod +x "$FAKE_BIN/tmux"
	FAKE_STATE="$(mktemp -d)"
	export FAKE_STATE
	PATH="$FAKE_BIN:$PATH"
}

@test "pane_is_ssh true only for ssh command" {
	setup_tmux_fake
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	echo ssh >"$FAKE_STATE/cmd_%5"
	run listener_pane_is_ssh "%5"
	[ "$status" -eq 0 ]
	echo bash >"$FAKE_STATE/cmd_%6"
	run listener_pane_is_ssh "%6"
	[ "$status" -ne 0 ]
}

@test "resolve_client returns the single client, refuses on 0 or 2" {
	setup_tmux_fake
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	echo laptop >"$FAKE_STATE/sess_%5"
	printf 'client0\n' >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -eq 0 ]
	[ "$output" = "client0" ] || { listener_resolve_client "%5"; [ "$REPLY" = "client0" ]; }
	printf 'client0\nclient1\n' >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -ne 0 ]
	: >"$FAKE_STATE/clients_laptop"
	run listener_resolve_client "%5"
	[ "$status" -ne 0 ]
}

@test "handle_line: hello matches version, rejects mismatch" {
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	listener_handle_line "hello 1"
	[ "$REPLY" = "ok 1" ]
	listener_handle_line "hello 999"
	[ "$REPLY" = "incompatible" ]
}

@test "handle_line: unknown verb refused" {
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	run listener_handle_line "danger rm -rf /"
	[ "$status" -ne 0 ]
}

@test "handle_line: promote with bad charset refused before any tmux call" {
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
	run listener_handle_line 'promote host sess;rm %5'
	[ "$status" -ne 0 ]
	run listener_handle_line 'promote host sess notapane'
	[ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run to verify failure**

Run: `nix develop -c bats tests/remote.bats`
Expected: FAIL — `lztmux-listener.sh` not found.

- [ ] **Step 3: Write the listener**

Create `scripts/lztmux-listener.sh`:

```bash
#!/usr/bin/env bash
# Local listener for remote session promotion. Run as `socat
# UNIX-LISTEN:<sock>,fork,mode=0600 EXEC:lztmux-listener`, so each connection
# gets this process with stdin/stdout wired to the socket. The ONLY component
# that mutates local tmux. Every field is validated before a tmux command runs.
set -uo pipefail

# shellcheck source=lib-remote.sh
source "@lib_remote@"

LZTMUX_RATE_FILE="${XDG_RUNTIME_DIR:-/tmp}/lztmux-last-promote"

listener_pane_is_ssh() {
	local cmd
	cmd="$(tmux display-message -p -t "$1" '#{pane_current_command}' 2>/dev/null)"
	[[ $cmd == ssh ]]
}

# Resolve the single client attached to the pane's origin session. Refuse on 0
# or >1 — the listener is not itself a client and cannot know which client
# issued the ssh when several are attached (multi-attach is ambiguous).
listener_resolve_client() {
	REPLY=""
	local sess clients count
	sess="$(tmux display-message -p -t "$1" '#{session_name}' 2>/dev/null)"
	[[ -n $sess ]] || return 1
	clients="$(tmux list-clients -t "$sess" -F '#{client_name}' 2>/dev/null)"
	count="$(grep -c . <<<"$clients")"
	[[ $count -eq 1 ]] || return 1
	REPLY="$clients"
	return 0
}

# Rate limit: refuse a second promote within 2s (blunts flooding).
listener_rate_ok() {
	local now last
	now="$(date +%s)"
	last="$(cat "$LZTMUX_RATE_FILE" 2>/dev/null || echo 0)"
	((now - last >= 2)) || return 1
	echo "$now" >"$LZTMUX_RATE_FILE"
	return 0
}

# Full promote (mutates tmux). Exercised in the integration test (Task 6).
listener_promote() {
	local host="$1" session="$2" pane="$3" target client
	remote_validate_field "$host" || {
		REPLY="refused bad-host"
		return 1
	}
	remote_validate_field "$session" || {
		REPLY="refused bad-session"
		return 1
	}
	remote_validate_pane "$pane" || {
		REPLY="refused bad-pane"
		return 1
	}
	listener_pane_is_ssh "$pane" || {
		REPLY="refused not-ssh-pane"
		return 1
	}
	listener_resolve_client "$pane" || {
		REPLY="refused client-ambiguous"
		return 1
	}
	client="$REPLY"
	listener_rate_ok || {
		REPLY="refused rate-limited"
		return 1
	}
	remote_session_name "$host" "$session"
	target="$REPLY"
	if tmux has-session -t "=$target" 2>/dev/null; then
		tmux switch-client -c "$client" -t "=$target"
		REPLY="ok existing"
		return 0
	fi
	# Isolate the pane into its own window, relocate that window into a fresh
	# session, drop the placeholder, follow the client. Exact incantation is
	# the one item pinned against a live server (Task 6).
	tmux break-pane -d -s "$pane" -n "$session" -t "=$target-tmp" 2>/dev/null || true
	tmux new-session -d -s "=$target" 2>/dev/null || true
	tmux move-window -s "=$target-tmp:" -t "=$target:" 2>/dev/null || true
	tmux switch-client -c "$client" -t "=$target"
	REPLY="ok promoted"
	return 0
}

# Parse one protocol line; set REPLY to the reply. Return non-zero on refusal.
listener_handle_line() {
	local line="$1"
	local -a f
	read -r -a f <<<"$line"
	case "${f[0]:-}" in
	hello)
		if [[ ${f[1]:-} == "$LZTMUX_PROTO_VERSION" ]]; then
			REPLY="ok $LZTMUX_PROTO_VERSION"
			return 0
		fi
		REPLY="incompatible"
		return 0
		;;
	promote)
		listener_promote "${f[1]:-}" "${f[2]:-}" "${f[3]:-}"
		return $?
		;;
	*)
		REPLY="refused unknown-verb"
		return 1
		;;
	esac
}

# socat EXEC main loop: read lines from the connection, reply per line. Skipped
# when sourced for tests.
if [[ -z ${LZTMUX_LISTENER_LIB:-} ]]; then
	while IFS= read -r line; do
		listener_handle_line "$line" || true
		printf '%s\n' "$REPLY"
		[[ $line == promote* ]] && break
	done
fi
```

> Note: `@lib_remote@` is a Nix build-time placeholder (Task 4 wires it). In bats, source order makes `lib-remote.sh` already loaded, and the `source "@lib_remote@"` line is guarded — adjust: in tests, pre-source `lib-remote.sh` and set `LZTMUX_LISTENER_LIB=1`; the `source "@lib_remote@"` must tolerate the placeholder during tests by guarding: replace with `[[ -f @lib_remote@ ]] && source "@lib_remote@"`. Add that guard now.

- [ ] **Step 4: Add the placeholder guard**

In `scripts/lztmux-listener.sh` replace the source line with:

```bash
# @lib_remote@ is substituted at Nix build time; in bats the lib is pre-sourced.
[[ -f "@lib_remote@" ]] && source "@lib_remote@"
```

- [ ] **Step 5: Run tests to verify pass**

Run: `nix develop -c bats tests/remote.bats`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 6: shellcheck + commit**

```bash
nix develop -c shellcheck -e SC1091 scripts/lztmux-listener.sh
git add scripts/lztmux-listener.sh tests/remote.bats
git commit -m "feat(remote): listener protocol handler + validation"
```

---

## Task 3: `lztmux-remote-shim.sh` — remote tmux wrapper

**Files:**
- Create: `scripts/lztmux-remote-shim.sh`
- Test: `tests/remote.bats` (append)

**Interfaces:**
- Consumes: `lib-remote.sh` (`remote_env_gate`, `LZTMUX_PROTO_VERSION`).
- Produces (guarded by `LZTMUX_SHIM_LIB=1` for tests):
  - `shim_decide` — reads env + a memory file; sets `REPLY` to `promote` or `plain`. Consults `$LZTMUX_STATE/remote-hosts` (`host=always|never`) and, when undecided, the answer in `$LZTMUX_SHIM_ANSWER` (test seam standing in for the tty prompt).
  - Main (unguarded): if `shim_decide` says promote and the handshake succeeds, send `promote`, then `exec` the real tmux; otherwise `exec` the real tmux unchanged.

- [ ] **Step 1: Write the failing tests (append to `tests/remote.bats`)**

```bash
@test "shim_decide: never-listed host -> plain, no prompt" {
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"; echo "web01=never" >"$LZTMUX_STATE/remote-hosts"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "plain" ]
}

@test "shim_decide: always-listed host -> promote, no prompt" {
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"; echo "web01=always" >"$LZTMUX_STATE/remote-hosts"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "promote" ]
}

@test "shim_decide: env gate fails -> plain" {
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	SSH_CONNECTION="" TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 shim_decide
	[ "$REPLY" = "plain" ]
}

@test "shim_decide: undecided host uses seeded answer y -> promote" {
	LZTMUX_SHIM_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-remote-shim.sh"
	LZTMUX_STATE="$(mktemp -d)"
	SSH_CONNECTION=x TMUX="" TMUX_PANE="%5" LZTMUX_HOST=web01 LZTMUX_SHIM_ANSWER=y shim_decide
	[ "$REPLY" = "promote" ]
}
```

- [ ] **Step 2: Run to verify failure**

Run: `nix develop -c bats tests/remote.bats`
Expected: FAIL — shim not found.

- [ ] **Step 3: Write the shim**

Create `scripts/lztmux-remote-shim.sh`:

```bash
#!/usr/bin/env bash
# Remote `tmux` wrapper installed by the home-manager module as a shell function
# (`tmux() { lztmux-remote-shim "$@"; }`). Detects nested-over-ssh, optionally
# promotes into a local session on the initiating laptop, then execs the real
# tmux. Inert unless the env gate passes and the listener handshake succeeds.
set -uo pipefail

[[ -f "@lib_remote@" ]] && source "@lib_remote@"

LZTMUX_STATE="${LZTMUX_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/lztmux}"
LZTMUX_SOCK="/tmp/lztmux-outer-${USER}.sock"

shim_memory() { # host -> echoes always|never|"" from the memory file
	local host="$1"
	[[ -f "$LZTMUX_STATE/remote-hosts" ]] || return 0
	sed -n "s/^${host}=//p" "$LZTMUX_STATE/remote-hosts" | head -n1
}

shim_remember() { # host verdict
	mkdir -p "$LZTMUX_STATE"
	sed -i "/^$1=/d" "$LZTMUX_STATE/remote-hosts" 2>/dev/null || true
	printf '%s=%s\n' "$1" "$2" >>"$LZTMUX_STATE/remote-hosts"
}

# Set REPLY to promote|plain. Does NOT do the network handshake — that is the
# main flow's job so shim_decide stays unit-testable.
shim_decide() {
	REPLY="plain"
	remote_env_gate || return 0
	local host="${LZTMUX_HOST:-$(hostname -s)}" mem ans
	mem="$(shim_memory "$host")"
	case "$mem" in
	always)
		REPLY="promote"
		return 0
		;;
	never)
		REPLY="plain"
		return 0
		;;
	esac
	if [[ -n ${LZTMUX_SHIM_ANSWER:-} ]]; then
		ans="$LZTMUX_SHIM_ANSWER"
	else
		printf '[%s] Nested lztmux over ssh. Promote into a local session? [Y/n] (a=always, x=never) ' "$host" >/dev/tty
		read -r ans </dev/tty || ans="n"
	fi
	case "$ans" in
	"" | y | Y) REPLY="promote" ;;
	a | A)
		shim_remember "$host" always
		REPLY="promote"
		;;
	x | X)
		shim_remember "$host" never
		REPLY="plain"
		;;
	*) REPLY="plain" ;;
	esac
	return 0
}

shim_handshake() { # returns 0 iff listener alive and version matches
	command -v socat >/dev/null || return 1
	local resp
	resp="$(printf 'hello %s\n' "$LZTMUX_PROTO_VERSION" | timeout 2 socat - "UNIX-CONNECT:$LZTMUX_SOCK" 2>/dev/null | head -n1)"
	[[ $resp == "ok $LZTMUX_PROTO_VERSION" ]]
}

if [[ -z ${LZTMUX_SHIM_LIB:-} ]]; then
	shim_decide
	if [[ $REPLY == promote ]] && shim_handshake; then
		# Only bare `tmux` / `tmux a[ttach]` promote; anything else runs plain.
		session="default"
		case "${1:-}" in
		"" | a | at | att | atta | attac | attach | attach-session)
			session="${2:-default}"
			printf 'promote %s %s %s\n' "$(hostname -s)" "$session" "$TMUX_PANE" |
				timeout 3 socat - "UNIX-CONNECT:$LZTMUX_SOCK" >/dev/null 2>&1 || true
			;;
		esac
	fi
	exec tmux "$@"
fi
```

- [ ] **Step 4: Run tests to verify pass**

Run: `nix develop -c bats tests/remote.bats`
Expected: PASS (all shim decision tests).

- [ ] **Step 5: shellcheck + commit**

```bash
nix develop -c shellcheck -e SC1091 scripts/lztmux-remote-shim.sh
git add scripts/lztmux-remote-shim.sh tests/remote.bats
git commit -m "feat(remote): tmux shim with detection + prompt + handshake"
```

---

## Task 4: Nix packaging in `config/tmux.conf.nix`

**Files:**
- Modify: `config/tmux.conf.nix` (script registration around `:141`–`:369`, closure around the `wrapProgram --prefix PATH` at the bottom).

**Interfaces:**
- Consumes: the three new scripts.
- Produces: `lztmux-listener`, `lztmux-remote-shim` on PATH; `@lib_remote@` substituted to the store path of the built `lib-remote.sh`; `socat` in the wrapper closure.

- [ ] **Step 1: Build `lib-remote` as a store script and wire the placeholder**

Near the other `lib-*` definitions (e.g. `lib-enrich` at `:184`), add:

```nix
  lib-remote = pkgs.writeShellScript "lib-remote" (builtins.readFile ../scripts/lib-remote.sh);
```

For the two new scripts, add a substitution set mirroring how `lib-enrich` is patched into consumers. Where scripts get `@lib_*@` substitution, add a branch that replaces `@lib_remote@`:

```nix
  # scripts that source lib-remote get its store path substituted
  scriptsWithRemote = ["lztmux-listener" "lztmux-remote-shim"];
  mkRemoteScript = name:
    pkgs.writeShellScriptBin name (
      builtins.replaceStrings ["@lib_remote@"] ["${lib-remote}"]
      (builtins.readFile ../scripts/${name}.sh)
    );
```

- [ ] **Step 2: Register the scripts in the `script` attrset**

In the `script = { ... }` mapping that feeds `scripts = lib.attrValues script;` (`:369`), add entries so both build via `mkRemoteScript`. Follow the existing conditional that dispatches by name (`:353`): add `else if builtins.elem name scriptsWithRemote then mkRemoteScript name`.

- [ ] **Step 3: Add socat to the wrapper closure**

In the `wrapProgram $out/bin/tmux --prefix PATH` list (`config/tmux.conf.nix:819`), add `pkgs.socat`:

```nix
        --prefix PATH : ${lib.makeBinPath ([tmuxPkg] ++ scripts ++ [pkgs.lazygit gh-dash pkgs.yazi pkgs.btop pkgs.zoxide pkgs.jq pkgs.util-linux pkgs.coreutils pkgs.xdg-utils pkgs.chafa pkgs.socat] ++ ...)}
```

- [ ] **Step 4: Build to verify**

Run: `nix build .#default 2>&1 | tail -20`
Expected: builds; `./result/bin/lztmux-listener` and `./result/bin/lztmux-remote-shim` exist.

Run: `test -x ./result/bin/lztmux-listener && test -x ./result/bin/lztmux-remote-shim && echo OK`
Expected: `OK`

- [ ] **Step 5: Verify the placeholder was substituted**

Run: `grep -c '@lib_remote@' ./result/bin/lztmux-listener`
Expected: `0` (placeholder replaced by a store path).

- [ ] **Step 6: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat(remote): package listener + shim scripts, add socat"
```

---

## Task 5: home-manager options, systemd service, ssh_config, shell function

**Files:**
- Modify: `modules/home-manager.nix` (options near `worktrunk` at `:245`; systemd under `systemd.user` at `:806`; a new `programs.ssh` block; shell-function install).

**Interfaces:**
- Consumes: `lztmux-listener`, `lztmux-remote-shim` (Task 4), `socat`.
- Produces: `programs.lazytmux.remote.{enable,trustedHosts}`; a running listener service; ssh_config for `trustedHosts`; a remote `tmux` shell function.

- [ ] **Step 1: Add the options**

Alongside the `worktrunk` option block (`:245`):

```nix
      remote = {
        enable = lib.mkEnableOption "remote nested-session promotion (architecture C; opt-in, security-sensitive)";
        trustedHosts = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [];
          example = ["lab-*" "myserver"];
          description = ''
            ssh Host patterns that receive the RemoteForward + SendEnv block.
            Only enable for hosts where root is you: a peer who can reach the
            forwarded socket can force UI actions on this machine (see spec
            security model).
          '';
        };
      };
```

- [ ] **Step 2: Add the listener systemd user service**

Under `systemd.user.services` (`:806`), gated on `cfg.remote.enable`:

```nix
        lztmux-listener = lib.mkIf cfg.remote.enable {
          Unit.Description = "lztmux remote-session promotion listener";
          Install.WantedBy = ["default.target"];
          Service = {
            ExecStartPre = "${pkgs.coreutils}/bin/mkdir -p %h/.local/state/lztmux";
            ExecStart = "${pkgs.socat}/bin/socat UNIX-LISTEN:%h/.local/state/lztmux/listener.sock,fork,mode=0600,unlink-early EXEC:${lib.getExe' tmux-wrapped \"lztmux-listener\"}";
            Restart = "on-failure";
          };
        };
```

- [ ] **Step 3: Add the ssh_config block**

Add a `programs.ssh` block (merges with the user's existing ssh config), gated on `cfg.remote.enable && cfg.remote.trustedHosts != []`:

```nix
      programs.ssh = lib.mkIf (cfg.remote.enable && cfg.remote.trustedHosts != []) {
        enable = true;
        matchBlocks."lztmux-remote" = {
          host = lib.concatStringsSep " " cfg.remote.trustedHosts;
          extraOptions = {
            RemoteForward = "/tmp/lztmux-outer-%r.sock %d/.local/state/lztmux/listener.sock";
            SendEnv = "TMUX_PANE";
            SetEnv = "LZTMUX_OUTER=1";
            StreamLocalBindUnlink = "yes";
            ExitOnForwardFailure = "no";
          };
        };
      };
```

- [ ] **Step 4: Install the remote `tmux` shell function**

The shim must intercept `tmux` in the interactive shell on remote hosts. Install for fish (primary) and bash. Add, gated on `cfg.remote.enable`:

```nix
      programs.fish.functions = lib.mkIf cfg.remote.enable {
        tmux.body = ''${lib.getExe' tmux-wrapped "lztmux-remote-shim"} $argv'';
      };
      programs.bash.initExtra = lib.mkIf cfg.remote.enable ''
        tmux() { ${lib.getExe' tmux-wrapped "lztmux-remote-shim"} "$@"; }
      '';
```

- [ ] **Step 5: Evaluate the module**

Run: `nix flake check 2>&1 | tail -30`
Expected: passes (module evaluates; bats run clean).

- [ ] **Step 6: Commit**

```bash
git add modules/home-manager.nix
git commit -m "feat(remote): home-manager options, listener service, ssh_config, shim function"
```

---

## Task 6: NixOS companion module, flake export, integration test

**Files:**
- Create: `modules/nixos.nix`
- Modify: `flake.nix` (module exports)
- Create: `tests/remote-integration.bats`

**Interfaces:**
- Consumes: everything prior.
- Produces: `nixosModules.default` setting `AcceptEnv`; a green integration test proving the promote choreography.

- [ ] **Step 1: Write the NixOS companion module**

Create `modules/nixos.nix`:

```nix
{lib, ...}: {
  # Companion module for lztmux remote nested-session promotion. Import on hosts
  # you ssh INTO so the forwarded LZTMUX_OUTER / TMUX_PANE env survives sshd.
  # home-manager cannot set sshd options, so this is a separate NixOS module.
  options.programs.lazytmux.remoteAcceptEnv =
    lib.mkEnableOption "accept lztmux remote-session env vars over sshd";

  config = lib.mkIf true {
    services.openssh.settings.AcceptEnv = "LZTMUX_OUTER TMUX_PANE";
  };
}
```

> Note: `AcceptEnv` is additive across `settings.AcceptEnv` entries; if the host already sets it, use `lib.mkAfter`/list form per that host's config.

- [ ] **Step 2: Export the module from `flake.nix`**

Add to the flake outputs next to the existing `homeManagerModules`:

```nix
      nixosModules.default = import ./modules/nixos.nix;
```

- [ ] **Step 3: Write the integration test**

Create `tests/remote-integration.bats`:

```bash
#!/usr/bin/env bats
# Real tmux server + direct listener_promote call. Proves the choreography:
# pane moved into HOST-<session>, single client followed, idempotent re-run.

setup() {
	command -v tmux >/dev/null || skip "tmux not on PATH"
	export TMUX_TMPDIR="$(mktemp -d)"
	export XDG_RUNTIME_DIR="$(mktemp -d)"
	tmux new-session -d -s laptop -x 80 -y 24
	# Attach a single background client so resolve_client finds exactly one.
	( tmux attach -t laptop >/dev/null 2>&1 & echo $! >"$TMUX_TMPDIR/cli.pid" )
	sleep 0.3
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
	LZTMUX_LISTENER_LIB=1 source "${BATS_TEST_DIRNAME}/../scripts/lztmux-listener.sh"
}

teardown() {
	[[ -f "$TMUX_TMPDIR/cli.pid" ]] && kill "$(cat "$TMUX_TMPDIR/cli.pid")" 2>/dev/null || true
	tmux kill-server 2>/dev/null || true
}

@test "promote moves an ssh pane into HOST-session and switches the client" {
	# Make the origin pane look like an ssh pane by running `ssh` stand-in.
	pane="$(tmux display-message -p -t laptop '#{pane_id}')"
	tmux respawn-pane -k -t "$pane" "sleep 300" # placeholder; command name = sleep
	# Force pane_current_command to ssh via a tiny wrapper on PATH is complex;
	# instead validate the non-tmux branches here and cover pane_is_ssh in unit
	# tests. Drive promote with the ssh check stubbed:
	listener_pane_is_ssh() { return 0; }
	run listener_promote laptophost mono "$pane"
	[ "$status" -eq 0 ]
	tmux has-session -t "=laptophost-mono"
	[ "$?" -eq 0 ]
}

@test "second promote is idempotent (switch, not recreate)" {
	pane="$(tmux display-message -p -t laptop '#{pane_id}')"
	listener_pane_is_ssh() { return 0; }
	listener_promote laptophost mono "$pane" || true
	run listener_promote laptophost mono "$pane"
	[ "$status" -eq 0 ]
	[[ "$REPLY" == ok* ]]
}
```

- [ ] **Step 4: Run the integration test**

Run: `nix develop -c bats tests/remote-integration.bats`
Expected: PASS (or `skip` if tmux absent). Fix the exact `break-pane`/`move-window` incantation in `lztmux-listener.sh` here if the session isn't created as expected — this is the "pin against a live server" step.

- [ ] **Step 5: Full flake check**

Run: `nix flake check 2>&1 | tail -30`
Expected: passes.

- [ ] **Step 6: Commit**

```bash
git add modules/nixos.nix flake.nix tests/remote-integration.bats
git commit -m "feat(remote): NixOS AcceptEnv module, flake export, integration test"
```

---

## Task 7 (optional): session-scoped inner status hide

**Files:**
- Modify: `config/tmux.conf.nix` (a hook or a documented note).

Skip unless you want the cosmetic double-bar fix. If done: set `status off` on the promoted remote session name at attach, and document the shared-session caveat from the spec (a co-located operator on the same session also loses the bar). No per-client variant exists.

- [ ] **Step 1:** Document the caveat in the module option description; optionally add a `client-attached` hook that sets `status off` when the session name matches `*-*` promoted pattern. Commit.

---

## Manual verification (post-merge, real hardware)

1. `nix build .#default`; deploy via nix-config bump; **server restart** (PATH staleness — see project memory).
2. Enable `programs.lazytmux.remote = { enable = true; trustedHosts = ["<your-homelab>"]; }` and import `nixosModules.default` on that host.
3. From inside local tmux: `ssh <homelab>`; run `tmux`. Expect the prompt; accept; expect a local `HOST-<session>` you're switched into, holding the ssh.
4. Open a **second** concurrent ssh to the same host; run `tmux`; expect graceful behavior (most-recent owns the forward; the other degrades to plain, no hang).
5. ssh from **outside** local tmux; run `tmux`; expect no prompt (plain tmux).

## Self-review notes

- Spec coverage: detection predicate (T1/T3), handshake liveness (T3), restricted protocol + validation (T2), client resolution + refusals (T2/T6), pane-is-ssh binding (T2), promote choreography (T2 impl/T6 verify), config surface + ssh_config + service (T5), AcceptEnv NixOS module (T6), concurrent-ssh degradation (manual step 4), security limits — all mapped.
- The one spec item deliberately verified live rather than unit-tested is the exact tmux `break-pane`/`move-window` incantation (T6 step 4), consistent with the spec's flag.
