# Remote Auth Handshake Implementation Plan (#357)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the session picker reporting auth-needed hosts as "unreachable", and give the user one place to satisfy the ssh prompt — after which every other call site rides the resulting ControlMaster for free.

**Architecture:** The probe keeps `BatchMode=yes` and never blocks, but now captures stderr and classifies exit 255 into `needsAuth` / `hostKeyChanged` / `unreachable`. Enter on a needs-auth row hands the picker's popup pty to `ssh` via `tea.ExecProcess`, running `lztmux-remote-auth`, which creates a self-reaping ControlMaster and then offers `ssh-copy-id`. Nothing in `lztmux-remote-open.sh`, the bridge daemon, or the graphics fetcher changes — verified that `ControlMaster no` still *reuses* an existing master.

**Tech Stack:** Go 1.25 (`charm.land/bubbletea/v2 v2.0.2`), bash, Nix (flake + home-manager module), bats.

## Global Constraints

- Design spec: `docs/superpowers/specs/2026-08-11-remote-auth-handshake-design.md`. Read it before Task 1.
- Shell scripts are **bash**, indented with **tabs** (`shfmt` project default). Run `shellcheck` on every script touched.
- The three-command local gate, all of which must pass before the final commit: `nix build .#default`, `nix flake check`, `nix build .#lint`. None subsumes another.
- Commit from inside `nix develop` (or direnv) so `.pip-precommit-config.yaml` materializes — never `PRE_COMMIT_ALLOW_NO_CONFIG=1`.
- `errRemoteHostKeyChanged` must **never** be reachable from the auth flow. A changed host key is a MITM signature; its row is inert by design, not by omission.
- The key offered to `ssh-copy-id` comes from `ssh -G <host>` only. This machine has `id_ed25519` **and** `noam_factify_ed25519`; a hardcoded default would push a work key to a personal host.
- Never pass `-o ControlPath` to the master command. The user's ssh config owns the path (`~/.ssh/master-%r@%n:%p`); overriding it puts the master where no other call site looks.

---

### Task 1: Classify ssh stderr into two new probe states

**Files:**
- Modify: `picker/remote.go:41-52` (sentinels + `remoteProbeState` consts), `picker/remote.go:113-125` (`classifyProbeErr`)
- Test: `picker/remote_test.go:209-235` (`TestClassifyProbeErr`)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `classifyProbeErr(err error, stderr string, timedOut bool) error`; sentinels `errRemoteNeedsAuth`, `errRemoteHostKeyChanged`; states `remoteProbeNeedsAuth`, `remoteProbeHostKeyChanged`.

- [ ] **Step 1: Write the failing test**

Replace the whole of `TestClassifyProbeErr` in `picker/remote_test.go` with:

```go
// Exit 255 means ssh itself failed; stderr says whether a human could fix it.
func TestClassifyProbeErr(t *testing.T) {
	exitErr := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}

	const hostKeyChangedStderr = "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n" +
		"Host key verification failed."

	cases := []struct {
		name     string
		err      error
		stderr   string
		timedOut bool
		want     error
	}{
		// Baselines: the pre-#357 behaviour, unchanged.
		{"bare 255", exitErr(255), "", false, errRemoteUnreachable},
		{"exit 1 is the remote command's own", exitErr(1), "", false, errRemoteNoServer},
		{"tmux missing on the remote", exitErr(127), "", false, errRemoteNoServer},
		{"timeout beats the exit status", exitErr(1), "", true, errRemoteUnreachable},
		{"ssh binary missing", errors.New(`exec: "ssh": not found`), "", false, errRemoteUnreachable},

		// New: 255 plus a stderr signature a prompt could fix.
		{"unknown host key", exitErr(255), "Host key verification failed.", false, errRemoteNeedsAuth},
		{"password refused", exitErr(255), "noams@mbp: Permission denied (publickey,password).", false, errRemoteNeedsAuth},
		{"agent exhausted", exitErr(255), "Received disconnect: Too many authentication failures", false, errRemoteNeedsAuth},
		{"2fa offered", exitErr(255), "Authentications that can continue: keyboard-interactive", false, errRemoteNeedsAuth},

		// New: a changed key outranks the auth patterns ssh prints alongside it.
		{"host key changed", exitErr(255), hostKeyChangedStderr, false, errRemoteHostKeyChanged},

		// Genuinely down hosts must not be dragged into the auth flow.
		{"refused", exitErr(255), "ssh: connect to host lab port 22: Connection refused", false, errRemoteUnreachable},
		{"no route", exitErr(255), "ssh: connect to host lab port 22: No route to host", false, errRemoteUnreachable},
		{"unknown name", exitErr(255), "ssh: Could not resolve hostname lab: Name or service not known", false, errRemoteUnreachable},

		// Precedence: a non-255 exit is the remote command's, whatever it printed.
		{"remote command printed Permission denied", exitErr(1), "cat: /etc/shadow: Permission denied", false, errRemoteNoServer},
		// Precedence: a killed process has no meaningful stderr verdict.
		{"timeout beats an auth signature", exitErr(255), "Host key verification failed.", true, errRemoteUnreachable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProbeErr(tc.err, tc.stderr, tc.timedOut); !errors.Is(got, tc.want) {
				t.Errorf("classifyProbeErr(%v, %q, %v) = %v, want %v", tc.err, tc.stderr, tc.timedOut, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd picker && go test ./... -run TestClassifyProbeErr
```

Expected: FAIL to **compile** — `too many arguments in call to classifyProbeErr` and `undefined: errRemoteNeedsAuth`, `undefined: errRemoteHostKeyChanged`.

- [ ] **Step 3: Add the sentinels and states**

In `picker/remote.go`, replace the `var (...)` block at line 41 and the `const (...)` block at line 48:

```go
var (
	errRemoteUnreachable    = errors.New("remote unreachable")
	errRemoteNoServer       = errors.New("remote tmux server not running")
	errRemoteNeedsAuth      = errors.New("remote needs interactive authentication")
	errRemoteHostKeyChanged = errors.New("remote host key changed")
)

type remoteProbeState int

const (
	remoteProbeOK remoteProbeState = iota
	remoteProbeNoServer
	remoteProbeUnreachable
	remoteProbeNeedsAuth
	remoteProbeHostKeyChanged
)
```

- [ ] **Step 4: Rewrite `classifyProbeErr`**

Replace `picker/remote.go:113-125` entirely:

```go
// authFailurePatterns are the ssh stderr signatures meaning a human at a real
// terminal could fix this by answering a prompt. Only consulted on exit 255,
// where the failure is ssh's own.
var authFailurePatterns = []string{
	"Host key verification failed",
	"Permission denied",
	"Too many authentication failures",
	"keyboard-interactive",
}

// hostKeyChangedPattern is ssh's warning that the host key no longer matches
// known_hosts. Kept out of authFailurePatterns deliberately: this is the
// signature of a MITM as much as of a reinstalled host, so it must never reach
// a flow that invites the user to connect. ssh prints it alongside "Host key
// verification failed", so it is matched first.
const hostKeyChangedPattern = "REMOTE HOST IDENTIFICATION HAS CHANGED"

// classifyProbeErr decides which failure a non-zero probe was. ssh exits 255
// when it could not reach the host; any other status is the remote command's
// own, so the host answered and only its tmux server is missing (#266). Within
// 255, stderr distinguishes a host that merely wants an interactive answer from
// one that is genuinely down (#357) — an unrecognised 255 stays unreachable, so
// being wrong costs a stale label rather than a pointless password prompt.
func classifyProbeErr(err error, stderr string, timedOut bool) error {
	if timedOut {
		return fmt.Errorf("%w: probe timed out", errRemoteUnreachable)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != sshConnectFailureExit {
		return fmt.Errorf("%w: %w", errRemoteNoServer, err)
	}
	if strings.Contains(stderr, hostKeyChangedPattern) {
		return fmt.Errorf("%w: %w", errRemoteHostKeyChanged, err)
	}
	for _, p := range authFailurePatterns {
		if strings.Contains(stderr, p) {
			return fmt.Errorf("%w: %w", errRemoteNeedsAuth, err)
		}
	}
	return fmt.Errorf("%w: %w", errRemoteUnreachable, err)
}
```

- [ ] **Step 5: Fix the two existing call sites so the package compiles**

`picker/remote.go:146` and `picker/remote.go:296` currently call `classifyProbeErr(err, ctx.Err() != nil)`. Task 2 gives them real stderr; for now pass an empty string so the package builds:

```go
		return nil, classifyProbeErr(err, "", ctx.Err() != nil)
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd picker && go test ./... -run TestClassifyProbeErr -v
```

Expected: PASS, 15 subtests.

- [ ] **Step 7: Commit**

```bash
git add picker/remote.go picker/remote_test.go
git commit -m "feat(remote): classify ssh stderr into needs-auth and host-key-changed (#357)"
```

---

### Task 2: Capture stderr, propagate the states, render the rows

**Files:**
- Modify: `picker/remote.go:131-157` (`sshListRemoteSessions`), `picker/remote.go:282-300` (`sshListRestorableSessions`), `picker/remote.go:89-111` (`remoteSessionsForHost`), `picker/remote.go:459-477` (note switch in `collectRemoteItems`)
- Modify: `picker/tui.go:38-43` (`listItem` fields)
- Test: `picker/remote_test.go`

**Interfaces:**
- Consumes: `classifyProbeErr`, `errRemoteNeedsAuth`, `errRemoteHostKeyChanged`, `remoteProbeNeedsAuth`, `remoteProbeHostKeyChanged` (Task 1).
- Produces: `listItem.remoteNeedsAuth bool`, `listItem.remoteInert bool`; the note strings `"(auth needed — Enter to connect)"` and `"(host key changed — verify manually)"`.

- [ ] **Step 1: Write the failing test**

Append to `picker/remote_test.go`:

```go
// A host that only wants an interactive answer is not "unreachable": it gets
// its own note and an actionable row (#357).
func TestCollectRemoteItemsNeedsAuth(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "mbp"}
	probe := func(string) ([]string, error) {
		return nil, fmt.Errorf("%w: exit status 255", errRemoteNeedsAuth)
	}
	items := collectRemoteItems(opts, nil, probe, nil)

	if len(items) != 2 {
		t.Fatalf("got %d items, want header + one host row", len(items))
	}
	row := items[1]
	if !strings.Contains(row.plain, "(auth needed — Enter to connect)") {
		t.Errorf("row = %q, want the auth-needed note", row.plain)
	}
	if !row.remoteNeedsAuth {
		t.Error("remoteNeedsAuth = false, want true so Enter runs the handshake")
	}
	if row.remoteInert {
		t.Error("remoteInert = true, want false — this row must be actionable")
	}
}

// A changed host key is a MITM signature. The row must say so and Enter must
// have nothing to act on: offering "Enter to connect" here would train the user
// to accept key changes without checking a fingerprint.
func TestCollectRemoteItemsHostKeyChanged(t *testing.T) {
	opts := map[string]string{"@remote_bridge_hosts": "mbp"}
	probe := func(string) ([]string, error) {
		return nil, fmt.Errorf("%w: exit status 255", errRemoteHostKeyChanged)
	}
	items := collectRemoteItems(opts, nil, probe, nil)

	row := items[1]
	if !strings.Contains(row.plain, "(host key changed — verify manually)") {
		t.Errorf("row = %q, want the host-key-changed note", row.plain)
	}
	if !row.remoteInert {
		t.Error("remoteInert = false, want true so Enter refuses to act")
	}
	if row.remoteNeedsAuth {
		t.Error("remoteNeedsAuth = true, want false — a key change is not an auth prompt")
	}
}

// The two new states map through remoteSessionsForHost, not just through
// classifyProbeErr.
func TestRemoteSessionsForHostNewStates(t *testing.T) {
	cases := map[string]struct {
		err  error
		want remoteProbeState
	}{
		"needs auth":       {fmt.Errorf("%w: x", errRemoteNeedsAuth), remoteProbeNeedsAuth},
		"host key changed": {fmt.Errorf("%w: x", errRemoteHostKeyChanged), remoteProbeHostKeyChanged},
		"unreachable":      {fmt.Errorf("%w: x", errRemoteUnreachable), remoteProbeUnreachable},
		"no server":        {fmt.Errorf("%w: x", errRemoteNoServer), remoteProbeNoServer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probe := func(string) ([]string, error) { return nil, tc.err }
			if _, got := remoteSessionsForHost("h", nil, probe); got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd picker && go test ./... -run 'TestCollectRemoteItemsNeedsAuth|TestCollectRemoteItemsHostKeyChanged|TestRemoteSessionsForHostNewStates'
```

Expected: FAIL to compile — `row.remoteNeedsAuth undefined`, `row.remoteInert undefined`.

- [ ] **Step 3: Add the two `listItem` fields**

In `picker/tui.go`, immediately after line 43 (`remoteRestore bool`):

```go
	remoteNeedsAuth bool   // remote host row: the probe hit an interactive ssh prompt; Enter runs lztmux-remote-auth
	remoteInert     bool   // remote host row: host key changed — Enter must refuse to act, never offer to connect
```

- [ ] **Step 4: Capture stderr in both probes**

In `sshListRemoteSessions` (`picker/remote.go`), replace the buffer setup and `Run` block:

```go
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, classifyProbeErr(err, stderr.String(), ctx.Err() != nil)
	}
```

Apply the identical change in `sshListRestorableSessions`.

- [ ] **Step 5: Propagate the states in `remoteSessionsForHost`**

Replace the error branch at `picker/remote.go:90-96`:

```go
	names, err := probe(host)
	if err != nil {
		switch {
		case errors.Is(err, errRemoteNoServer):
			return nil, remoteProbeNoServer
		case errors.Is(err, errRemoteNeedsAuth):
			return nil, remoteProbeNeedsAuth
		case errors.Is(err, errRemoteHostKeyChanged):
			return nil, remoteProbeHostKeyChanged
		}
		return nil, remoteProbeUnreachable
	}
```

- [ ] **Step 6: Render the notes and set the flags**

In `collectRemoteItems`, extend the note switch and replace the bare `items = append(items, remoteHostRowItem(...))`:

```go
		note := ""
		switch r.state {
		case remoteProbeUnreachable:
			// The host may be back up by the time it is picked.
			note = "(unreachable — open default)"
		case remoteProbeNeedsAuth:
			// ssh wants an answer a batch-mode probe can never give (#357).
			note = "(auth needed — Enter to connect)"
		case remoteProbeHostKeyChanged:
			note = "(host key changed — verify manually)"
		case remoteProbeNoServer:
			// The launcher cold-starts the host's own startup session (#287).
			// The host row itself never restores — it carries no
			// remoteSess/remoteRestore either way — so its note doesn't
			// change when restorable child rows exist below it; those rows
			// carry their own "(restore — saved …)" suffix instead.
			note = "(no server — Enter starts one)"
		default:
			if len(r.sess) == 0 {
				note = "(all open)"
			}
		}
		hostRow := remoteHostRowItem(tmuxOpts, r.host, note)
		switch r.state {
		case remoteProbeNeedsAuth:
			hostRow.remoteNeedsAuth = true
		case remoteProbeHostKeyChanged:
			hostRow.remoteInert = true
		}
		items = append(items, hostRow)
```

- [ ] **Step 7: Run the full picker suite**

```bash
cd picker && go test ./...
```

Expected: PASS. The new states produce no session or restorable child rows because `r.sess` is nil and `restoreProbe` still fires only on `remoteProbeNoServer` — no change needed there.

- [ ] **Step 8: Commit**

```bash
git add picker/remote.go picker/remote_test.go picker/tui.go
git commit -m "feat(remote): surface auth-needed and host-key-changed rows in the picker (#357)"
```

---

### Task 3: The `lztmux-remote-auth` script

**Files:**
- Modify: `scripts/lib-remote.sh` (append `remote_auth_identity`)
- Create: `scripts/lztmux-remote-auth.sh`
- Create: `tests/remote-auth.bats`

**Interfaces:**
- Consumes: nothing from earlier tasks (the picker calls this by name in Task 5).
- Produces: an executable `lztmux-remote-auth <host>`; the shell helper `remote_auth_identity <ssh-G-output>` setting `REPLY` to an absolute `*.pub` path or the empty string.

- [ ] **Step 1: Write the failing test**

Create `tests/remote-auth.bats`:

```bash
#!/usr/bin/env bats

setup() {
	# shellcheck source=/dev/null
	source "${BATS_TEST_DIRNAME}/../scripts/lib-remote.sh"
}

@test "remote_auth_identity: picks the host's own key and expands ~" {
	HOME=/home/tester remote_auth_identity "user noams
hostname mbp-m4-pro
identityfile ~/.ssh/id_ed25519
controlpath /home/tester/.ssh/master-noams@mbp:22"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: first identityfile wins when ssh lists several" {
	HOME=/home/tester remote_auth_identity "identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/noam_factify_ed25519"
	[ "$REPLY" = "/home/tester/.ssh/id_ed25519.pub" ]
}

@test "remote_auth_identity: absolute paths pass through untouched" {
	HOME=/home/tester remote_auth_identity "identityfile /etc/keys/shared_ed25519"
	[ "$REPLY" = "/etc/keys/shared_ed25519.pub" ]
}

# No identityfile means ssh would fall back to its built-in defaults. Guessing
# one here could push a work key to a personal host, so the caller must skip the
# offer instead.
@test "remote_auth_identity: empty when the host declares no identity" {
	HOME=/home/tester remote_auth_identity "user noams
hostname lab"
	[ -z "$REPLY" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
bats tests/remote-auth.bats
```

Expected: FAIL — `remote_auth_identity: command not found` on all four.

- [ ] **Step 3: Add the helper to `scripts/lib-remote.sh`**

Append (tabs, matching the file):

```bash
# remote_auth_identity <ssh -G output>: set REPLY to the public half of the
# first identity the host resolves to, absolute. Empty REPLY when the host
# declares none — the caller must then skip the ssh-copy-id offer rather than
# guess, since this machine carries more than one key and a work key must not
# land on a personal host.
remote_auth_identity() {
	local key value
	REPLY=""
	while read -r key value; do
		[[ $key == identityfile ]] || continue
		REPLY="${value/#\~/$HOME}.pub"
		return 0
	done <<<"$1"
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
bats tests/remote-auth.bats
```

Expected: PASS, 4 tests.

- [ ] **Step 5: Write the script**

Create `scripts/lztmux-remote-auth.sh`:

```bash
#!/usr/bin/env bash
# Interactive ssh handshake for one remote-bridge host (#357).
#
# Creates a ControlMaster, which is the whole point: `ControlMaster no` (the
# Host * default) still REUSES an existing master, so once this succeeds the
# picker probe, lztmux-remote-open's 3-8 ssh calls, the bridge daemon's `ssh
# -CC` and the graphics fetcher all ride it with no prompt and no code changes.
#
# Runs with a real tty — the picker hands its popup over via tea.ExecProcess —
# because ssh must own the terminal while a password is typed. Nothing here
# reads, stores or forwards the secret.
set -euo pipefail

# shellcheck source=/dev/null
source "@lib_remote@"

host="${1:?usage: lztmux-remote-auth <host>}"

persist="$(tmux show -gv @remote_auth_persist 2>/dev/null || true)"
[[ $persist =~ ^[0-9]+$ ]] || persist=14400

# Pause before handing the terminal back, so a failure is readable instead of
# being wiped by the picker's next full-screen paint.
pause_then_exit() {
	if [[ -t 0 ]]; then
		printf '\nPress Enter to return… '
		read -r _
	fi
	exit "$1"
}

printf 'Authenticating to %s\n\n' "$host"

# -M overrides the config's `ControlMaster no`. ControlPath is deliberately NOT
# passed: the user's config owns it, and every other call site looks there.
# -f backgrounds only after authentication, so the host-key question and the
# password prompt both happen here and a clean return means auth succeeded.
# `true` rather than -N lets ControlPersist reap the master (verified: process
# and socket both gone once it expires); -N would live until an explicit -O exit
# and leave a stale socket across a suspend.
# ServerAliveInterval is explicit because Host * sets it to 0, which would let a
# half-open master hang every later call with nothing to time it out.
if ! ssh -M -f -o ControlPersist="$persist" -o ServerAliveInterval=15 "$host" true; then
	printf '\nCould not authenticate to %s.\n' "$host" >&2
	pause_then_exit 1
fi

printf '\nConnected. %s stays authenticated for %ss of idle time.\n' "$host" "$persist"

# Would pubkey auth alone have worked? ControlPath=none forces a fresh
# connection rather than riding the master just created, and BatchMode makes it
# fail rather than prompt — so a non-zero exit means no key is installed. This
# tests the condition directly instead of parsing `ssh -v` for its auth method.
if ssh -o BatchMode=yes -o ControlPath=none -o ConnectTimeout=5 "$host" true 2>/dev/null; then
	exit 0
fi

remote_auth_identity "$(ssh -G "$host" 2>/dev/null || true)"
key="$REPLY"
[[ -n $key && -f $key ]] || exit 0
[[ -t 0 ]] || exit 0

printf '\nInstall %s on %s so this is the last time? [y/N] ' "$key" "$host"
read -r reply
[[ $reply == [Yy]* ]] || exit 0

# Rides the master created above, so this needs no second password.
if ! ssh-copy-id -i "$key" "$host"; then
	printf '\nCould not install the key; the connection is still authenticated.\n' >&2
	pause_then_exit 0
fi
printf '\n%s will not ask again.\n' "$host"
pause_then_exit 0
```

- [ ] **Step 6: Lint the shell**

```bash
shellcheck scripts/lztmux-remote-auth.sh scripts/lib-remote.sh
shfmt -d scripts/lztmux-remote-auth.sh scripts/lib-remote.sh
```

Expected: no output from either (shellcheck silent, shfmt reports no diff). `@lib_remote@` is a build-time placeholder, so shellcheck needs the `# shellcheck source=/dev/null` directive already present.

- [ ] **Step 7: Register the bats file in the existing remote check**

In `flake.nix`, inside the `remote-tests` derivation, after `bats tests/remote-cold-start.bats`:

```nix
              bats tests/remote-auth.bats
```

- [ ] **Step 8: Commit**

```bash
git add scripts/lib-remote.sh scripts/lztmux-remote-auth.sh tests/remote-auth.bats flake.nix
git commit -m "feat(remote): add lztmux-remote-auth interactive handshake script (#357)"
```

---

### Task 4: Nix wiring — option, tmux option, script packaging

**Files:**
- Modify: `modules/home-manager.nix:352-363` (the `remote` option block), and the `remoteBridgeHosts` passthrough near line 154
- Modify: `config/tmux.conf.nix:37` (argument), `:344` (`scriptNames`), `:448` (`scriptsWithRemote`), `:862` (the `set -g` block)

**Interfaces:**
- Consumes: `scripts/lztmux-remote-auth.sh` (Task 3).
- Produces: `programs.lazytmux.remote.authPersistSeconds` (int, default 14400); the tmux global `@remote_auth_persist`; `lztmux-remote-auth` on the tmux server's PATH.

- [ ] **Step 1: Add the module option**

In `modules/home-manager.nix`, inside the `remote = { … }` block, after `hosts`:

```nix
      authPersistSeconds = lib.mkOption {
        type = lib.types.ints.between 60 86400;
        default = 14400;
        description = ''
          How long an ssh ControlMaster created by the picker's auth handshake
          survives idle, in seconds (clamped 60–86400, default 4h). Passed
          straight to ssh's `ControlPersist`. This is an *idle* timer: a live
          bridge holds a session on the master, so an open mirror never expires
          regardless of this value.
        '';
      };
```

- [ ] **Step 2: Thread it to tmux.conf.nix**

In `modules/home-manager.nix`, beside `remoteBridgeHosts = …` (line 154):

```nix
    remoteAuthPersistSeconds = cfg.remote.authPersistSeconds;
```

In `config/tmux.conf.nix`, beside `remoteBridgeHosts ? "",` (line 37):

```nix
  remoteAuthPersistSeconds ? 14400,
```

- [ ] **Step 3: Set the tmux global**

In `config/tmux.conf.nix`, immediately after the `set -g @remote_bridge_hosts` line (~862):

```
    set -g @remote_auth_persist "${toString remoteAuthPersistSeconds}"
```

- [ ] **Step 4: Package the script**

In `config/tmux.conf.nix`, add to `scriptNames` right after `"lztmux-remote-open"`:

```nix
    "lztmux-remote-auth"
```

And extend `scriptsWithRemote` (line 448) — the script sources `lib-remote.sh`, so it needs the `@lib_remote@` substitution:

```nix
  scriptsWithRemote = ["lztmux-remote-open" "lztmux-remote-auth"];
```

- [ ] **Step 5: Build and verify the substitution actually landed**

```bash
nix build .#default
grep -c '@lib_remote@' ./result/bin/lztmux-remote-auth
```

Expected: `nix build` succeeds; `grep -c` prints `0` — an unsubstituted placeholder would mean the script was packaged by `mkScriptFull` instead of `mkRemoteScript` and would fail at runtime with a `source: @lib_remote@` error.

- [ ] **Step 6: Verify the tmux option is set**

```bash
grep 'remote_auth_persist' ./result/share/tmux/tmux.conf || grep -r 'remote_auth_persist' ./result
```

Expected: one `set -g @remote_auth_persist "14400"` line.

- [ ] **Step 7: Commit**

```bash
git add modules/home-manager.nix config/tmux.conf.nix
git commit -m "feat(remote): add authPersistSeconds option and package lztmux-remote-auth (#357)"
```

---

### Task 5: Enter on the new rows

**Files:**
- Modify: `picker/tui.go:1076-1099` (`activateCurrent`), `picker/tui.go:1464-1474` (preview hint)
- Test: `picker/tui_test.go`

**Interfaces:**
- Consumes: `listItem.remoteNeedsAuth`, `listItem.remoteInert` (Task 2); `lztmux-remote-auth` on PATH (Tasks 3-4); `m.remoteCmd()` (existing, `picker/tui.go:1422`).
- Produces: no new exported surface.

- [ ] **Step 1: Write the failing test**

Append to `picker/tui_test.go`:

```go
// Enter on a host-key-changed row must not act. It explains itself and stays
// open — quitting or bridging would both be wrong.
func TestActivateHostKeyChangedRowRefuses(t *testing.T) {
	m := tuiModel{
		visible: []listItem{{
			isRemoteRow: true,
			remoteHost:  "mbp",
			remoteInert: true,
			target:      "remote:mbp",
		}},
		cursor: 0,
	}
	next, cmd := m.activateCurrent()
	if cmd != nil {
		t.Error("cmd != nil, want nil — an inert row must neither quit nor bridge")
	}
	got := next.(tuiModel).statusMsg
	if !strings.Contains(got, "host key") {
		t.Errorf("statusMsg = %q, want an explanation mentioning the host key", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd picker && go test ./... -run TestActivateHostKeyChangedRowRefuses
```

Expected: FAIL — `activateCurrent` currently calls `openRemoteBridge` for any row with `remoteHost != ""`, so `cmd` is `tea.Quit` (or `statusMsg` holds an ssh error).

- [ ] **Step 3: Add the completion message**

`tea.ExecProcess`'s callback returns a `tea.Msg`, not a `tea.Cmd`, so the
re-probe has to be dispatched from `Update`. Do **not** wrap `ExecProcess` in
`tea.Sequence` — bubbletea intercepts `ExecProcess` specially in its event loop,
and burying it inside a composite command is not a supported shape.

Add beside the other message types in `picker/tui.go` (near `remoteMsg` at line 145):

```go
// remoteAuthDoneMsg lands when the interactive ssh handshake has exited and the
// popup's pty is back under bubbletea's control. It carries no result: the
// handshake's effect is a ControlMaster on disk, so the only honest way to know
// what changed is to probe again.
type remoteAuthDoneMsg struct{}
```

And handle it in `Update`, beside `case remoteMsg:` (line 321):

```go
	case remoteAuthDoneMsg:
		return m, m.remoteCmd()
```

- [ ] **Step 4: Handle both new rows in `activateCurrent`**

Replace the `if item.remoteHost != ""` block at `picker/tui.go:1082-1088`:

```go
	if item.remoteHost != "" {
		// A changed host key is a MITM signature as much as a reinstall, and
		// only a human comparing fingerprints out of band can clear it. Acting
		// here — even just offering to connect — would train that check away.
		if item.remoteInert {
			m.statusMsg = "host key changed for " + item.remoteHost + " — verify the fingerprint, then update known_hosts by hand"
			return m, nil
		}
		// ssh needs a terminal to ask its question and the picker is holding the
		// only one. ExecProcess releases the popup's pty for the duration, so
		// ssh prompts for itself and the secret never passes through this
		// process.
		if item.remoteNeedsAuth {
			cmd := exec.Command("lztmux-remote-auth", item.remoteHost)
			return m, tea.ExecProcess(cmd, func(error) tea.Msg { return remoteAuthDoneMsg{} })
		}
		if err := openRemoteBridge(item.remoteHost, item.remoteSess, item.remoteRestore); err != nil {
			m.statusMsg = err.Error()
			return m, nil
		}
		return m, tea.Quit
	}
```

The callback ignores its `error` argument deliberately: a user who declines the
`ssh-copy-id` offer, or aborts the password prompt with Ctrl-C, produces a
non-zero exit that is not a failure worth reporting. The re-probe shows the
truth either way.

- [ ] **Step 5: Update the preview hint**


Replace the body of the `item.remoteHost != ""` branch at `picker/tui.go:1464`:

```go
	if item.remoteHost != "" {
		host, sess := item.remoteHost, item.remoteSess
		inert, needsAuth := item.remoteInert, item.remoteNeedsAuth
		return func() tea.Msg {
			var msg string
			switch {
			case inert:
				msg = "remote bridge → " + host +
					"\n\nThe host key changed since it was last accepted. That is what a" +
					"\nreinstalled host looks like — and also what an interception looks" +
					"\nlike. Compare the fingerprint out of band, then fix known_hosts by" +
					"\nhand. Enter does nothing here."
			case needsAuth:
				msg = "remote bridge → " + host +
					"\n\nEnter runs lztmux-remote-auth: ssh takes this popup and asks for" +
					"\nitself. It opens one shared connection, so the bridge and every" +
					"\nlater probe reuse it without asking again."
			default:
				msg = "remote bridge → " + host
				if sess != "" {
					msg += "/" + sess
				}
				msg += "\n\nEnter runs lztmux-remote-open (outbound ssh)."
			}
			return previewMsg{content: msg, target: t, scrollTop: true}
		}
	}
```

- [ ] **Step 6: Run the tests**

```bash
cd picker && go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add picker/tui.go picker/tui_test.go
git commit -m "feat(remote): run the ssh handshake in the picker popup via ExecProcess (#357)"
```

---

### Task 6: Documentation and the full gate

**Files:**
- Modify: `README.md` (the "Remote tmux bridge" section, around line 195)
- Modify: `CLAUDE.md` (the script table, after the `lztmux-remote-open` row)

**Interfaces:**
- Consumes: everything above.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Document the flow in README.md**

After the paragraph describing `(no server — Enter starts one)`, insert:

````markdown
A host that needs an answer ssh can only get from a terminal — an unknown host
key, a password, a 2FA code — shows as
`<host>  (auth needed — Enter to connect)`. Enter hands the picker's popup to
ssh, which prompts for itself; lazytmux never sees the secret. That one
handshake opens a shared connection (`ControlMaster`), so the bridge, the
launcher and every later probe reuse it without asking again — for
`remote.authPersistSeconds` of idle time (default 4h; a live bridge never
expires, since it holds the connection open).

If a key is not installed, the same prompt offers to run `ssh-copy-id`, which
rides the connection just opened and so needs no second password. Accepting it
means the host never asks again.

A host whose key has *changed* since it was accepted shows as
`<host>  (host key changed — verify manually)` and Enter does nothing. That is
what a reinstalled host looks like, and also what an interception looks like;
resolving it means comparing the fingerprint out of band and editing
`known_hosts` yourself.
````

- [ ] **Step 2: Add the script to CLAUDE.md's table**

After the `lztmux-remote-open` row:

```markdown
| `lztmux-remote-auth` | `prefix + s` on an `(auth needed)` row | One interactive ssh handshake for a host, run in the picker's popup via `tea.ExecProcess` so ssh owns a real pty and lazytmux never handles the secret. Creates a self-reaping `ControlMaster` (`-M -f -o ControlPersist=@remote_auth_persist`) — `ControlMaster no` still *reuses* an existing master, so this alone makes the probe, the launcher's 3-8 calls, the daemon's `ssh -CC` and the graphics fetcher free. Then offers `ssh-copy-id` with the key from `ssh -G` (never a hardcoded default — a work key must not land on a personal host). |
```

- [ ] **Step 3: Run the complete local gate**

Three separate commands, none of which subsumes another:

```bash
nix build .#default
nix flake check
nix build .#lint
```

Expected: all three succeed. `nix flake check` runs `tests/remote-auth.bats` inside `remote-tests` plus the Go suites; `nix build .#lint` is the only one that runs `alejandra`/`shfmt`/`shellcheck`.

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs(remote): document the picker auth handshake (#357)"
```

- [ ] **Step 5: Push and open the PR**

Write the body to a scratch file first (avoids `$()` and heredocs in the commit
path), then create the PR:

```bash
git push -u origin feat/357-remote-auth-handshake
```

Body content — write to `/tmp/pr-357.md`:

```markdown
Closes #357.

The picker's remote probe runs `-o BatchMode=yes`, so every ssh interaction that
needs a terminal — an unknown host key, a password, a 2FA code — exited 255 and
was flattened into "unreachable". `mbp` has been a false negative the whole time:
it fails `Host key verification failed`, not a password.

- `classifyProbeErr` now reads stderr and splits 255 into `needsAuth` /
  `hostKeyChanged` / `unreachable`. An unrecognised 255 stays unreachable, so a
  wrong guess costs a stale label rather than a pointless prompt.
- Enter on a needs-auth row hands the picker's popup pty to ssh via
  `tea.ExecProcess` and runs `lztmux-remote-auth`, which creates a self-reaping
  `ControlMaster` and then offers `ssh-copy-id`. lazytmux never handles the
  secret.
- A *changed* host key gets its own row and Enter refuses to act — that is a MITM
  signature as much as a reinstall, and offering "Enter to connect" would train
  the fingerprint check away.
- Nothing in `lztmux-remote-open.sh`, the bridge daemon or the graphics fetcher
  changed: `ControlMaster no` still *reuses* an existing master, so one handshake
  makes all of them free.

Spec: `docs/superpowers/specs/2026-08-11-remote-auth-handshake-design.md`
Plan: `docs/superpowers/plans/2026-08-11-remote-auth-handshake.md`

Hardware verification against `mbp` is in the plan's final section and is **not**
covered by CI — the popup path needs a real tty and a real host.
```

```bash
gh pr create --assignee @me --title "feat(remote): interactive auth handshake for picker hosts (#357)" --body-file /tmp/pr-357.md
```

---

## Hardware verification (after merge + deploy)

Unit tests cannot cover the popup path — it needs a real tty and a real host.
`mbp` is the host that motivated the issue and currently fails
`Host key verification failed`, so it is the test case. **Do not** pre-accept its
host key before testing; that would destroy the fixture.

- [ ] `prefix + s` — the `mbp` row reads `(auth needed — Enter to connect)`, not `(unreachable — open default)`.
- [ ] Enter — the popup is handed to ssh, which asks to accept the host key. Accept, then enter the password.
- [ ] The `ssh-copy-id` offer appears (no key is installed on `mbp` yet). Accept it; it must **not** ask for the password a second time. This is the one design assumption not verified during design.
- [ ] The popup returns to the picker and the `mbp` row re-probes to a live session list or `(no server — Enter starts one)` — confirming `tea.ExecProcess` released and restored the popup pty cleanly. This is the second unverified assumption.
- [ ] `ssh -O check mbp` reports `Master running`.
- [ ] Enter on an `mbp` session row opens the bridge with no further prompt.
- [ ] Close the picker, reopen — `mbp` probes green with no interaction.
- [ ] `ssh -o BatchMode=yes -T mbp true` now exits 0, proving the key install worked.
