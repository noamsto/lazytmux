# Cursor Status Hooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `programs.lazytmux.cursorStatus.enable` so Cursor Agent hooks write `claude-status-update` state like Codex/Claude.

**Architecture:** Thin `cursor-status-hook` wrapper + home-manager activation that upserts entries into `~/.cursor/hooks.json` every switch (strip by `/bin/cursor-status-hook`, leave aeye/user entries). agent-detect remains backfill.

**Tech Stack:** bash, jq, Nix home-manager, Cursor CLI `hooks.json`.

## Global Constraints

- Profile-stable paths only (`config.home.profileDirectory`), never raw store paths in hooks.json.
- Wrapper stdout must be empty (Cursor injects `additional_context`).
- Requires `agentIntegration.enable`.
- Default `cursorStatus.enable = false`.

---

## File map

| File | Role |
|------|------|
| `scripts/cursor-status-hook.sh` | Wrapper → `claude-status-update "$@" >/dev/null` |
| `config/tmux.conf.nix` | Package the script via existing mkScript |
| `modules/home-manager.nix` | Option, assert, packages, activation jq upsert |
| `tests/cursor-status-hooks.bats` | Pure jq merge fixtures |
| `README.md` | Document enable |
| nix-config `home/terminal/default.nix` | Enable cursorStatus (+ already-planned codexStatus/resumeCodex) |

---

### Task 1: Wrapper + package

- [ ] Create `scripts/cursor-status-hook.sh` (bash, `set -euo pipefail`, forward args, redirect stdout/stderr of csu to `/dev/null`, exit 0 even if csu missing so hooks fail open).
- [ ] Register in `config/tmux.conf.nix` script list / expose on `tmuxConfig.script.cursor-status-hook`.
- [ ] `shellcheck` the script.
- [ ] Commit.

### Task 2: HM option + activation

- [ ] Add `cursorStatus.enable` option (mirror `codexStatus` docs tone).
- [ ] Assert requires `agentIntegration.enable`.
- [ ] Add wrapper (+ ensure `claude-status-update`) to `home.packages` when enabled (or rely on agentIntegration for csu and only add wrapper here).
- [ ] Activation after `writeBoundary`: pin `jq`+`coreutils` on PATH; upsert hooks.json per spec event map; strip prior `/bin/cursor-status-hook` commands; fail loud on malformed JSON.
- [ ] Commit.

### Task 3: Merge test

- [ ] `tests/cursor-status-hooks.bats` — fixture with aeye-shaped entries; run the same jq used in activation (extract to a small `scripts/lib-cursor-hooks.sh` or inline the jq file under `tests/fixtures/`); assert aeye kept, status present, second upsert idempotent.
- [ ] Wire check in `flake.nix` if sibling bats checks exist.
- [ ] Commit.

### Task 4: Docs + nix-config enable

- [ ] README: Cursor status section next to Codex.
- [ ] nix-config worktree: `cursorStatus.enable = true` beside `codexStatus` / `resumeCodex`.
- [ ] Commit both repos.

### Task 5: Smoke

- [ ] After HM switch (or manual jq merge + PATH), run a short `cursor-agent` turn in tmux; confirm `/tmp/claude-status/panes/<id>` and window icon.
