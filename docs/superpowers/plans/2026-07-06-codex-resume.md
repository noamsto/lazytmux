# Codex @ts_relaunch Stamping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stamp `@ts_relaunch="codex resume <uuid>"` on Codex panes so `tmux-state` restores a resumed Codex session, mirroring the existing Claude path.

**Architecture:** A Codex session hook (fallback: the `notify` program) runs a writer that records the pane's Codex session UUID to `$CODEX_PANES_DIR/<pane_id>`, keyed by `$TMUX_PANE` — the Codex analogue of the Claude pane-state files. The per-tick stamping loop in `tmux-update-icons.sh` is generalized to resolve a resume command per agent kind (`claude --resume <uuid>` / `codex resume <uuid>`) and set `@ts_relaunch` on change. All work is in lazytmux; `tmux-state` is unchanged.

**Tech Stack:** Bash, bats (`tests/`, `tests/helper.bash`), Nix (`flake.nix` script substitution, `modules/home-manager.nix`, `config/tmux.conf.nix`), tmux, codex-cli 0.142.3.

## Global Constraints

- `codex resume <SESSION_ID>` takes a UUID; sessions at `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`; first JSONL line is `session_meta` with `session_id` (resumable thread id — distinct from filename `id` for subagents), `cwd`.
- Pane files are keyed by numeric pane id (`%N` → `N`), matching `CLAUDE_PANES_DIR`.
- Stamp `@ts_relaunch` only on change (compare against the value already read in the batched `list-panes`) — a stable pane must fork nothing per tick.
- Gate behind `@resume_codex` (default off), paralleling `@resume_claude`.
- Degrade gracefully: no-op (exit 0) when `tmux-state`/`tmux` absent or run outside a lazytmux pane — mirror `claude-plugin/scripts/status.sh`.
- Run `shellcheck` on every shell script touched; fix all findings before commit.
- Depends on `tmux-state` `feat/7-relaunch-override` (the `@ts_relaunch` mechanism) landing. No `tmux-state` change here.

---

### Task 1: Verify the Codex hook mechanism (spike)

**Files:**
- Create: `docs/superpowers/notes/codex-hooks-findings.md` (scratch findings, committed for the next tasks)

**Goal:** Turn the two spec unknowns into documented facts before writing the writer. No production code in this task.

- [ ] **Step 1: Discover hook configuration surface**

Run each and record output in the findings file:

```bash
codex --help 2>&1 | grep -iE 'hook'
codex doctor 2>&1 | grep -iE 'hook|notify' || true
grep -rniE 'hook|notify' ~/.codex/config.toml || echo "no hook/notify config yet"
find ~/.codex -maxdepth 2 -iname '*hook*' 2>/dev/null
codex plugin --help 2>&1 | head -40
```

- [ ] **Step 2: Identify a session-start event carrying the session id**

Configure a throwaway hook/notify that dumps its stdin + environment, start a codex session in a tmux pane, and inspect:

```bash
# Point notify (or a hook) at a logger, e.g. in ~/.codex/config.toml:
#   notify = ["/bin/sh", "-c", "cat > /tmp/codex-hook-payload.json; env > /tmp/codex-hook-env.txt"]
codex   # start a session in a tmux pane, send one prompt, exit
cat /tmp/codex-hook-payload.json     # does it contain session_id / a rollout path?
grep -i tmux /tmp/codex-hook-env.txt # is TMUX_PANE present?
```

Record in the findings file:
1. The exact event/mechanism that fires with the session id (or the earliest one that does).
2. Whether the payload gives `session_id` directly, or only a rollout path (basename → uuid) — and whether it is the resumable `session_id` vs the filename `id`.
3. Whether `$TMUX_PANE` is in the hook/notify environment.

- [ ] **Step 3: Decide the pane-resolution path**

Based on Step 2, record the chosen resolution in the findings file:
- If `$TMUX_PANE` present → key the file on it directly.
- If absent but the hook runs as a child of the codex pane process → resolve via `tmux list-panes -a -F '#{pane_pid} #{pane_id}'` matched against the hook's ancestor pid.
- If neither → fall back to the `notify` program (child of the pane process, inherits `TMUX_PANE`).

- [ ] **Step 4: Commit findings**

```bash
git add docs/superpowers/notes/codex-hooks-findings.md
git commit -m "docs: codex hook mechanism findings for resume stamping (#140)"
```

---

### Task 2: Codex session-id writer script

**Files:**
- Create: `scripts/codex-session-write.sh`
- Create: `tests/codex-session-write.bats`
- Modify: `scripts/lib-claude.sh` OR a shared lib — add `CODEX_PANES_DIR` constant (see Step 3)
- Modify: `flake.nix` (register the new script for substitution/packaging, mirroring `tmux-update-icons`)

**Interfaces:**
- Consumes: Task 1 findings (event name, payload shape, pane-resolution path).
- Produces: `$CODEX_PANES_DIR/<pane_id>` containing the resumable session UUID (single line). `CODEX_PANES_DIR` default `${CLAUDE_STATUS_DIR:-/tmp/claude-status}/codex-panes`.

- [ ] **Step 1: Write the failing bats test**

Create `tests/codex-session-write.bats` (mirror an existing bats file's `load helper` header):

```bash
#!/usr/bin/env bats
load helper

setup() {
	export CLAUDE_STATUS_DIR="$BATS_TEST_TMPDIR/status"
	export CODEX_PANES_DIR="$CLAUDE_STATUS_DIR/codex-panes"
	export TMUX_PANE="%7"
}

@test "writes session uuid to pane file keyed by pane id" {
	run bash "$BATS_TEST_DIRNAME/../scripts/codex-session-write.sh" \
		--session-id "019f37d5-1c99-7c91-a31f-09c98f1b4173"
	[ "$status" -eq 0 ]
	[ "$(cat "$CODEX_PANES_DIR/7")" = "019f37d5-1c99-7c91-a31f-09c98f1b4173" ]
}

@test "no-op when TMUX_PANE unset" {
	unset TMUX_PANE
	run bash "$BATS_TEST_DIRNAME/../scripts/codex-session-write.sh" \
		--session-id "019f37d5-1c99-7c91-a31f-09c98f1b4173"
	[ "$status" -eq 0 ]
	[ ! -d "$CODEX_PANES_DIR" ]
}

@test "idempotent on repeat" {
	bash "$BATS_TEST_DIRNAME/../scripts/codex-session-write.sh" --session-id "abc"
	run bash "$BATS_TEST_DIRNAME/../scripts/codex-session-write.sh" --session-id "abc"
	[ "$status" -eq 0 ]
	[ "$(cat "$CODEX_PANES_DIR/7")" = "abc" ]
}
```

Note: the writer's *input contract* (`--session-id` vs reading a rollout path / JSON from stdin) is fixed by Task 1 findings. If the hook delivers a rollout path, add a `--rollout <path>` mode that derives the uuid from the basename and adjust the test accordingly — but keep `--session-id` as the canonical internal form.

- [ ] **Step 2: Run it to verify it fails**

Run: `bats tests/codex-session-write.bats`
Expected: FAIL — script does not exist.

- [ ] **Step 3: Implement the writer**

Create `scripts/codex-session-write.sh`:

```bash
#!/usr/bin/env bash
# Records a Codex pane's resumable session UUID to $CODEX_PANES_DIR/<pane_id>
# so tmux-update-icons can stamp @ts_relaunch="codex resume <uuid>". Invoked by
# a Codex session hook / notify program. No-ops outside a lazytmux tmux pane.
set -euo pipefail

CODEX_PANES_DIR="${CODEX_PANES_DIR:-${CLAUDE_STATUS_DIR:-/tmp/claude-status}/codex-panes}"

uuid=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--session-id) uuid="$2"; shift 2 ;;
	--rollout) uuid="$(basename "$2" .jsonl)"; uuid="${uuid##*-}"; shift 2 ;;
	*) shift ;;
	esac
done

[[ -n ${TMUX_PANE:-} ]] || exit 0
[[ -n $uuid ]] || exit 0

mkdir -p "$CODEX_PANES_DIR"
printf '%s\n' "$uuid" >"$CODEX_PANES_DIR/${TMUX_PANE#%}"
```

If Task 1 shows the hook delivers JSON on stdin instead of args, add a stdin-parse branch (fork-free regex like `status.sh`, or `jq` with graceful no-op if absent) that sets `uuid`; keep the arg interface for the test.

Add the `CODEX_PANES_DIR` constant next to `CLAUDE_PANES_DIR` in `scripts/lib-claude.sh` (with the same `# shellcheck disable=SC2034` note) so `tmux-update-icons.sh` (Task 3) shares it.

- [ ] **Step 4: Run the test + shellcheck**

Run: `bats tests/codex-session-write.bats`
Expected: PASS.
Run: `shellcheck scripts/codex-session-write.sh scripts/lib-claude.sh`
Expected: no findings.

- [ ] **Step 5: Register in flake.nix**

Add `codex-session-write` to the script set built/substituted in `flake.nix` next to the other `scripts/*.sh` entries (find the attribute that maps `tmux-update-icons` and mirror it). Verify:

Run: `nix build .#... 2>&1 | tail` (the package that bundles scripts — match the existing name)
Expected: build OK; the script lands in the wrapper output.

- [ ] **Step 6: Commit**

```bash
git add scripts/codex-session-write.sh scripts/lib-claude.sh tests/codex-session-write.bats flake.nix
git commit -m "feat: codex session-id pane writer (#140)"
```

---

### Task 3: Generalize the resume-stamping loop

**Files:**
- Modify: `scripts/tmux-update-icons.sh` (the `RESUME_CLAUDE` block, lines ~34–109)
- Modify: `tests/*.bats` covering `tmux-update-icons` relaunch stamping (extend the existing `@ts_relaunch` fixture/test)

**Interfaces:**
- Consumes: `CODEX_PANES_DIR` (Task 2), `@resume_codex` (Task 4), the batched `list-panes` fields already read (`proc`, `pane_id`, `pane_cur_relaunch`).
- Produces: `@ts_relaunch` set to `claude --resume <uuid>` for claude panes and `codex resume <uuid>` for codex panes.

- [ ] **Step 1: Write/extend the failing test**

Locate the existing test that asserts `@ts_relaunch` for a Claude pane (search: `grep -rn 'ts_relaunch' tests/`). Extend its fixture with a codex pane whose `$CODEX_PANES_DIR/<id>` file holds a uuid, and add assertions:
- codex pane → `@ts_relaunch` == `codex resume <uuid>`
- claude pane → `@ts_relaunch` == `claude --resume <uuid>` (unchanged)
- a codex pane with no session file → `@ts_relaunch` cleared/empty
- unchanged value → no `tmux set` invocation (assert via the test's tmux-call spy, matching how the Claude no-op case is asserted)

If no such test exists yet (feature-7 not merged into this branch's test suite), create `tests/tmux-update-icons-resume.bats` following the harness in the nearest existing `tmux-update-icons` bats file.

- [ ] **Step 2: Run it to verify it fails**

Run: `bats tests/<the-resume-test>.bats`
Expected: FAIL — codex pane not stamped.

- [ ] **Step 3: Implement the generalization**

In `scripts/tmux-update-icons.sh`:

1. Add a `RESUME_CODEX` positional (parallel to `RESUME_CLAUDE` at line ~39):

```bash
	# $3 is #{@resume_codex}, expanded by the status format (avoids a fork).
	RESUME_CODEX=${3:-}
```

2. Add a resume-command resolver near the top of `main`:

```bash
	# resume_cmd AGENT UUID -> echoes the verbatim @ts_relaunch command, or "".
	resume_cmd() {
		[[ -n $2 ]] || { printf ''; return; }
		case "$1" in
		claude) printf 'claude --resume %s' "$2" ;;
		codex) printf 'codex resume %s' "$2" ;;
		*) printf '' ;;
		esac
	}
```

3. Keep the existing Claude block (it derives the uuid from the transcript basename and sets `@ts_relaunch` on change), but route its command through `resume_cmd claude "$uuid"` instead of the inline `claude --resume` string, so both agents share the change-gated `tmux set` path.

4. Add a codex stamping pass that iterates codex panes from the batched data. Codex panes are those whose `proc` (`pane_current_command`) is `codex`; the pane id and current `@ts_relaunch` are already in `pane_to_win` / `pane_cur_relaunch`. For each, when `RESUME_CODEX == on`:

```bash
	if [[ $RESUME_CODEX == on ]]; then
		for pane_file in "${!pane_to_win[@]}"; do
			[[ ${pane_proc[$pane_file]:-} == codex ]] || continue
			uuid=""
			[[ -f "$CODEX_PANES_DIR/$pane_file" ]] && IFS= read -r uuid <"$CODEX_PANES_DIR/$pane_file"
			desired="$(resume_cmd codex "$uuid")"
			if [[ $desired != "${pane_cur_relaunch[$pane_file]:-}" ]]; then
				tmux set -pq -t "%$pane_file" @ts_relaunch "$desired"
			fi
		done
	fi
```

This requires a `pane_proc` map keyed by pane id. The batched `list-panes` loop (line ~55) already has `$proc` and `$pane_id`; add `pane_proc["${pane_id#%}"]="$proc"` alongside the existing `pane_to_win`/`pane_cur_relaunch` assignments.

5. Source `CODEX_PANES_DIR` (now defined in `lib-claude.sh`, already sourced at line 11).

- [ ] **Step 4: Run tests + shellcheck**

Run: `bats tests/<resume-test>.bats`
Expected: PASS.
Run: `shellcheck scripts/tmux-update-icons.sh`
Expected: no findings.

- [ ] **Step 5: Commit**

```bash
git add scripts/tmux-update-icons.sh tests/
git commit -m "feat: stamp @ts_relaunch for codex panes (#140)"
```

---

### Task 4: Config + module wiring

**Files:**
- Modify: `config/tmux.conf.nix` (define `@resume_codex`; thread into the `tmux-update-icons` invocation in `status-format[0]`)
- Modify: `modules/home-manager.nix` (`resumeCodec` option; provision the Codex hook/notify config; set `@resume_codex on`)

**Interfaces:**
- Consumes: `codex-session-write` script (Task 2), `@resume_codex` read by Task 3.

- [ ] **Step 1: Add the `@resume_codex` tmux option**

In `config/tmux.conf.nix`, near the `@resume_claude` definition (search: `grep -n resume_claude config/tmux.conf.nix`), add a `@resume_codex` set-option defaulting off, and add `'#{@resume_codex}'` as the third positional to the `tmux-update-icons` call in `status-format[0]` (currently passes `'#{session_name}' '#{@resume_claude}'`).

- [ ] **Step 2: Add the Home Manager option + provisioning**

In `modules/home-manager.nix`:
- Add `resumeCodex = lib.mkOption { type = lib.types.bool; default = false; description = "…codex resume <uuid> on restore"; }` mirroring `resumeClaude` (line ~264).
- Add `resumeCodexEnable = cfg.persist.enable && cfg.persist.package != null && cfg.persist.resumeCodex;` mirroring line 132.
- When enabled: write the Codex hook/notify config into `~/.codex/config.toml` (using the mechanism confirmed in Task 1) pointing at the packaged `codex-session-write` script, and set `@resume_codex on` in the generated tmux config (mirror how `@resume_claude on` is emitted).

- [ ] **Step 3: Evaluate the module**

Run: `nix flake check 2>&1 | tail -20` (or the repo's module eval test if present)
Expected: evaluation succeeds; no undefined-option errors.

- [ ] **Step 4: shellcheck any generated/edited scripts + commit**

```bash
shellcheck scripts/codex-session-write.sh scripts/tmux-update-icons.sh
git add config/tmux.conf.nix modules/home-manager.nix
git commit -m "feat: wire codex resume option + hook provisioning (#140)"
```

---

### Task 5: End-to-end verification

**Files:** none (manual/scripted verification)

- [ ] **Step 1: Rebuild and enable**

Enable `programs.lazytmux.persist.resumeCodex = true`, rebuild Home Manager, restart the tmux server so the new config + hook config load.

- [ ] **Step 2: Drive the real flow**

```bash
# In a fresh tmux pane:
codex            # start a session, send one prompt
# In another pane:
cat /tmp/claude-status/codex-panes/*    # a uuid appears, keyed by the codex pane id
tmux show-options -pv -t <codex-pane> @ts_relaunch   # -> "codex resume <uuid>"
```

- [ ] **Step 3: Confirm restore**

Save a snapshot (`tmux-state save` per its CLI), kill the server, restore. Confirm the codex pane comes back running `codex resume <uuid>` (a resumed session), not a bare shell.

- [ ] **Step 4: Record the result**

Note the verified behavior in the PR description. No commit unless verification surfaced a fix.

---

## Self-Review

- **Spec coverage:** writer (T2) + hook wiring (T4) = spec §1; generalized stamping loop (T3) = spec §2; config/module surface (T4) = spec §3; testing (T2/T3) + e2e (T5) = spec Testing; dependency on feature-7 stated in Global Constraints.
- **Unknowns handled honestly:** the two spec verification items (hook event, `TMUX_PANE`) are a first-class spike (T1) whose findings parameterize T2/T4 — not buried as "handle appropriately".
- **Type/name consistency:** `CODEX_PANES_DIR` defined in `lib-claude.sh` (T2) and consumed in `tmux-update-icons.sh` (T3); `@resume_codex` set in `tmux.conf.nix` (T4) and read as `RESUME_CODEX` positional (T3); `resume_cmd`/`codex resume <uuid>` consistent across T3 and T5.
- **Placeholder scan:** the only deferred specifics (exact hook config syntax, stdin-vs-arg input) are explicitly gated on T1 findings with the fallback behavior spelled out — no bare TODOs.
