# Codex @ts_relaunch Stamping Implementation Plan (revised post-spike)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Codex `SessionStart` hook stamps `@ts_relaunch="codex resume <uuid>"` on its own tmux pane, so `tmux-state` restores a resumed Codex session — mirroring the Claude path's outcome via a simpler, direct mechanism.

**Architecture (revised after the Task 1 spike, `docs/superpowers/notes/codex-hooks-findings.md`):** The Codex `SessionStart` hook receives the resumable `session_id` on stdin AND `$TMUX_PANE` in its environment, so the hook script stamps `@ts_relaunch` **directly** — no per-pane state file, no `tmux-update-icons.sh` changes. Hook trust is made non-interactive via a **managed config layer** at `/etc/codex/managed_config.toml`, provisioned by a NixOS system module in nix-config (the user-scope trust hash is an unreproducible content hash, so declarative pre-seed must use the managed layer).

**Tech Stack:** Bash, bats (`tests/`), Nix (`flake.nix` script packaging, `modules/home-manager.nix`), a NixOS system module in nix-config, tmux, codex-cli 0.142.3.

## Global Constraints

- Hook mechanism: Codex `[[hooks.SessionStart]]`, `matcher = "startup|resume"`. Payload is JSON on stdin with `session_id` (resumable UUID for top-level sessions) and `transcript_path`; `$TMUX_PANE` is in the hook env. (Source: `codex-hooks-findings.md`.)
- Stamp command: `tmux set-option -p -t "$TMUX_PANE" @ts_relaunch "codex resume <session_id>"`.
- No-op safely (exit 0) when `$TMUX_PANE` is unset, when not in tmux, or when `session_id` is empty — codex runs outside tmux too.
- Interactive-TUI caveat: the hook fires only after the first user turn completes, not at pane spawn. Acceptable (nothing to resume before then); the writer must not assume immediacy.
- Trust: managed-hook layer at `/etc/codex/` (system module) — do NOT attempt to compute a user-scope `trusted_hash`; do NOT ship `--dangerously-bypass-hook-trust`.
- Run `shellcheck` on every shell script touched; fix all findings before commit.
- Depends on `tmux-state` `feat/7-relaunch-override` (the `@ts_relaunch` mechanism) landing for the Task 4 end-to-end check only. Tasks 2–3 have no tmux-state dependency.

---

### Task 1: Verify the Codex hook mechanism (spike) — COMPLETE

Findings committed to `docs/superpowers/notes/codex-hooks-findings.md` (commits `581289e`, `f875f34`). Resolved: SessionStart hook, `session_id` + `TMUX_PANE` available, direct-stamp viable; trust via managed `/etc` layer. No further action.

---

### Task 2: Codex SessionStart hook writer script (portable — no trust dependency)

**Files:**
- Create: `scripts/codex-relaunch-stamp.sh`
- Create: `tests/codex-relaunch-stamp.bats`
- Modify: `flake.nix` (package the script to a stable store path, mirroring `tmux-update-icons`)

**Interfaces:**
- Produces: an executable that, given the SessionStart hook's JSON on stdin, stamps `@ts_relaunch` on `$TMUX_PANE`. Its stable installed path is what the managed hook config (Task 3) points its `command` at.

- [ ] **Step 1: Write the failing bats test**

Create `tests/codex-relaunch-stamp.bats` (mirror an existing bats file's `load helper` header; check how other tests stub `tmux`):

```bash
#!/usr/bin/env bats
load helper

setup() {
	export TMUX_PANE="%7"
	STAMP="$BATS_TEST_DIRNAME/../scripts/codex-relaunch-stamp.sh"
	# Capture tmux invocations instead of running real tmux.
	export TMUX_LOG="$BATS_TEST_TMPDIR/tmux.log"
	mkdir -p "$BATS_TEST_TMPDIR/bin"
	cat >"$BATS_TEST_TMPDIR/bin/tmux" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$TMUX_LOG"
EOF
	chmod +x "$BATS_TEST_TMPDIR/bin/tmux"
	export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
}

@test "stamps @ts_relaunch from session_id on stdin" {
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72","hook_event_name":"SessionStart","source":"startup"}
EOF
	[ "$status" -eq 0 ]
	grep -qF 'set-option -p -t %7 @ts_relaunch codex resume 019f3b53-487c-7973-8103-8e2828a5fd72' "$TMUX_LOG"
}

@test "no-op when TMUX_PANE unset" {
	unset TMUX_PANE
	run bash "$STAMP" <<'EOF'
{"session_id":"019f3b53-487c-7973-8103-8e2828a5fd72"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}

@test "no-op when session_id missing" {
	run bash "$STAMP" <<'EOF'
{"hook_event_name":"SessionStart"}
EOF
	[ "$status" -eq 0 ]
	[ ! -f "$TMUX_LOG" ]
}
```

If the repo's existing bats stub tmux differently, follow that pattern instead of this inline stub — read a neighboring `.bats` first.

- [ ] **Step 2: Run it to verify it fails**

Run: `bats tests/codex-relaunch-stamp.bats`
Expected: FAIL — script does not exist.

- [ ] **Step 3: Implement the writer**

Create `scripts/codex-relaunch-stamp.sh`:

```bash
#!/usr/bin/env bash
# Codex SessionStart hook: stamp this pane's @ts_relaunch so tmux-state resumes
# the Codex session (not a bare shell) on restore. Reads the hook's JSON payload
# from stdin (session_id) and the pane from $TMUX_PANE. No-ops outside tmux.
#
# Note: in the interactive TUI this hook fires only after the first user turn
# completes, so the stamp lags pane creation — acceptable (nothing to resume
# before then).
set -euo pipefail

[[ -n ${TMUX_PANE:-} ]] || exit 0
command -v tmux >/dev/null 2>&1 || exit 0

payload="$(cat)"
session_id=""
if command -v jq >/dev/null 2>&1; then
	session_id="$(printf '%s' "$payload" | jq -r '.session_id // empty' 2>/dev/null || true)"
else
	# Fork-free fallback, mirroring the theme/transcript parse in status.sh.
	[[ $payload =~ \"session_id\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] && session_id="${BASH_REMATCH[1]}"
fi

[[ -n $session_id ]] || exit 0
tmux set-option -p -t "$TMUX_PANE" @ts_relaunch "codex resume $session_id"
```

Verify the `jq`/regex convention against `scripts/lib-claude.sh` / `claude-plugin/scripts/status.sh` and match whichever the repo prefers.

- [ ] **Step 4: Run the test + shellcheck**

Run: `bats tests/codex-relaunch-stamp.bats`
Expected: PASS.
Run: `shellcheck scripts/codex-relaunch-stamp.sh`
Expected: no findings.

- [ ] **Step 5: Package in flake.nix**

Add `codex-relaunch-stamp` to the script set built/packaged in `flake.nix` next to `tmux-update-icons` (find the attribute mapping scripts → store paths and mirror it, including any `@substitution@` handling if the script needed it — this one does not). Verify:

Run: `nix build .#<the-scripts-package> 2>&1 | tail` (match the existing package name)
Expected: build OK; the script is present in the output with a stable path.

- [ ] **Step 6: Commit**

```bash
git add scripts/codex-relaunch-stamp.sh tests/codex-relaunch-stamp.bats flake.nix
git commit -m "feat: codex SessionStart hook that stamps @ts_relaunch (#140)"
```

---

### Task 3: Provision the managed hook (NixOS system module + lazytmux enable)

**Files:**
- lazytmux: `modules/home-manager.nix` (a `persist.resumeCodex` enable option + expose the packaged script's store path for the system module to consume; optionally a passthru/output)
- nix-config: NEW NixOS system module (e.g. `nixos/programs/codex-hooks.nix` or similar — follow nix-config conventions) writing `environment.etc."codex/managed_config.toml"`

**Interfaces:**
- Consumes: the `codex-relaunch-stamp` store path from Task 2.
- Produces: `/etc/codex/managed_config.toml` containing a `[[hooks.SessionStart]]` block (matcher `startup|resume`) whose `command` is the absolute store path of the Task 2 script, trusted-by-policy (managed layer).

- [ ] **Step 1: Empirically confirm the managed-trust file (root, one-time)**

The spike could not write `/etc`. Before wiring the module, confirm which file confers managed trust:

```bash
sudo mkdir -p /etc/codex
# Try managed_config.toml first:
sudo tee /etc/codex/managed_config.toml >/dev/null <<'EOF'
[[hooks.SessionStart]]
matcher = "startup|resume"
[[hooks.SessionStart.hooks]]
type = "command"
command = "/bin/sh -c 'echo fired >/tmp/codex-managed-test'"
EOF
codex exec "hi"   # NO --dangerously-bypass-hook-trust
cat /tmp/codex-managed-test   # expect "fired"; check /hooks shows it as a non-toggleable managed source
```

If `managed_config.toml` alone does NOT confer managed trust, add `/etc/codex/requirements.toml` with `allow_managed_hooks_only` per the findings doc — BUT note that flag disables non-managed user hooks; record the observed behavior. Record the confirmed file + shape in the findings doc before proceeding.

- [ ] **Step 2: Write the NixOS system module (nix-config)**

In nix-config, add a module (gated by an option, default off) that writes `environment.etc."codex/managed_config.toml".text` with the block confirmed in Step 1, substituting the `command` with the lazytmux `codex-relaunch-stamp` store path. Follow nix-config module conventions (options under a sensible namespace, `lib.mkIf enable`). Import it on the relevant host(s).

- [ ] **Step 3: lazytmux enable surface**

In lazytmux `modules/home-manager.nix`, add `persist.resumeCodex` (bool, default false) mirroring `resumeClaude`, and expose the packaged script path so the nix-config module can reference it (e.g. via the flake's packages output). No `@resume_codex` tmux option is needed — the hook stamps unconditionally when the managed layer is present; the enable flag governs provisioning.

- [ ] **Step 4: Evaluate**

Run (lazytmux): `nix flake check 2>&1 | tail -20` — evaluation succeeds.
Run (nix-config): evaluate the host that imports the module (per nix-config's workflow, e.g. `nix build .#nixosConfigurations.<host>.config.system.build.toplevel` or the repo's `just`/`nh` check) — succeeds, and the generated `/etc/codex/managed_config.toml` contains the correct script path.

- [ ] **Step 5: Commit (per repo)**

```bash
# lazytmux worktree:
git add modules/home-manager.nix flake.nix
git commit -m "feat: resumeCodex enable option + expose hook script path (#140)"
# nix-config: commit the new module on its own branch per nix-config workflow.
```

---

### Task 4: End-to-end verification

**Files:** none (manual/scripted verification). Requires `tmux-state` `feat/7-relaunch-override` present.

- [ ] **Step 1: Rebuild + enable** the NixOS module and lazytmux `resumeCodex`; restart tmux so config loads.

- [ ] **Step 2: Drive the flow**

```bash
# Fresh tmux pane:
codex            # start a session, send one prompt, wait for the turn to complete
tmux show-options -pv -t <codex-pane> @ts_relaunch   # -> "codex resume <uuid>"
```

- [ ] **Step 3: Confirm restore**

Save a snapshot (`tmux-state save`), kill the server, restore. Confirm the codex pane comes back running `codex resume <uuid>` (a resumed session), not a bare shell. Record the result in the PR.

---

## Self-Review

- **Spec coverage:** spike (T1, done); direct-stamp hook script + tests (T2); managed-trust provisioning across nix-config + lazytmux enable (T3); e2e (T4). The old file-based writer and `tmux-update-icons.sh` generalization are dropped per the spike — no longer in scope.
- **Unknowns handled honestly:** the residual managed-vs-requirements uncertainty is a first-class root-verification step (T3 Step 1) with a documented fallback, not a buried assumption.
- **Trust:** no user-scope hash computation, no bypass flag — managed layer only, per the findings.
- **Placeholder scan:** T3's nix-config module references "nix-config conventions" rather than exact paths because that module lives in a separate repo whose layout the implementer must read; the shape (option-gated `environment.etc`) and content are fully specified.
- **Cross-repo:** T2 + lazytmux enable are in this worktree; the NixOS module is a separate nix-config change (its own branch/PR).
