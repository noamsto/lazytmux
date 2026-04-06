# wt — Standalone Git Worktree Manager (Go Rewrite)

**Date:** 2026-04-06
**Status:** Approved
**Repo:** `~/Data/git/noamsto/wt`

## Overview

Rewrite `wt` as a standalone Go binary. Absorbs current `wt.sh` (796 LOC bash) and `wt-explorer/` (Go Bubble Tea TUI) into a single self-contained tool. Zero required runtime deps beyond `git`. Optional auto-detected integrations: `tmux`, `zoxide`, `gh`.

## Motivation

- `wt` is useful independent of lazytmux/tmux
- Bash at ~800 LOC is fragile (associative arrays, parallel subshells, string manipulation)
- Single Go binary eliminates `gum` dependency, simplifies packaging
- Absorbing `wt-explorer` removes a separate binary/module

## Architecture

Single Go module with clear internal packages:

```
cmd/wt/main.go              — CLI entry point, arg parsing, command dispatch
internal/
  git/                       — worktree CRUD, branch detection, stale detection
  git/git_test.go            — unit tests with temp repos
  tmux/                      — window/session management (no-ops when unavailable)
  zoxide/                    — add/remove paths (no-ops when unavailable)
  gh/                        — squash-merge PR detection (no-ops when unavailable)
  tui/
    explorer/                — Bubble Tea TUI for `wt clean -i`
    prompt/                  — confirm/filter components via Charm's huh (replaces gum)
```

### Design Principles

- **Deep modules:** each `internal/` package has a simple interface hiding implementation complexity
- **Optional integrations are interfaces:** tmux/zoxide/gh expose a common pattern — check availability once, provide no-op fallback
- **Shell out to external tools:** no go-git. `git`, `tmux`, `zoxide`, `gh` are all called via `exec.Command`
- **Single binary:** all TUI (explorer, prompts, filters) built with Bubble Tea / huh

### Runtime Detection

At startup, build a runtime context:

```go
type Runtime struct {
    HasTmux   bool   // command -v tmux
    HasZoxide bool   // command -v zoxide
    HasGh     bool   // command -v gh
    InTmux    bool   // $TMUX is set
    NoSwitch  bool   // -n flag
}
```

Each integration package accepts this and silently no-ops when unavailable.

## Commands (Full Parity with wt.sh)

| Command | Behavior |
|---------|----------|
| `wt <branch>` | Smart: switch if worktree exists, prompt to create otherwise |
| `wt main` | Switch to repo root worktree |
| `wt list` | List all worktrees |
| `wt remove <branch>` | Remove worktree + tmux window + zoxide entry |
| `wt clean` | Detect stale worktrees (3 strategies), prompt to remove |
| `wt clean -i` | Interactive Bubble Tea explorer (absorbs wt-explorer) |
| `wt z [query]` | Fuzzy find worktree (Bubble Tea filter when interactive, best-match when piped) |
| `wt help` | Help text |

### Flags

| Flag | Short | Behavior |
|------|-------|----------|
| `--yes` | `-y` | Skip confirmation prompts |
| `--quiet` | `-q` | Only output worktree path |
| `--no-switch` | `-n` | Skip tmux operations |
| `--interactive` | `-i` | TUI mode (for clean) |

Combinable: `-yqn` works as today.

## Stale Worktree Detection (wt clean)

Three strategies, same as current:

1. **git branch --merged** — regular merges / fast-forwards into default branch
2. **Remote branch deleted** — `git show-ref` fails for `refs/remotes/origin/<branch>` after `git fetch --prune`
3. **GitHub PR squash-merged** — `gh pr list --head <branch> --state merged` (only when `gh` is available)

## Tmux Integration

When tmux is available AND `-n` is not set AND (`$TMUX` is set OR tmux server is running):

- **`wt <branch>`**: create/switch tmux window, set `@worktree` and `@branch` window options
- **`wt remove`**: kill associated tmux window before removing worktree
- **`wt clean`**: kill windows for each removed worktree
- **`wt z`**: update current window's `@worktree`/`@branch` options
- **`wt main`**: switch to repo root window

Session targeting uses `#{session_id}` (`$N` format) to avoid numeric session name ambiguity.

Model: one tmux session per repo, one window per worktree.

## Zoxide Integration

When zoxide is available:

- **Create worktree**: `zoxide add <path>`
- **Remove worktree**: `zoxide remove <path>`

## TUI Components

### Explorer (`wt clean -i`)

Port of current `wt-explorer` Bubble Tea TUI:
- Split-pane layout: worktree list + detail preview
- Search/filter mode
- Multi-select for batch removal
- Async detail loading (dirty files, unpushed commits, last commit)
- Confirm-then-delete with force flag

### Prompts (replaces gum)

Using Charm's `huh` library:
- `confirm(prompt string) bool` — yes/no confirmation
- `filter(items []string, placeholder string) string` — fuzzy filter selection

When `--yes` is set, confirms auto-accept. When stdout is not a TTY, filter returns best match.

## Dependencies

### Go Dependencies
- `charm.land/bubbletea/v2` — TUI framework
- `charm.land/bubbles/v2` — viewport component
- `charm.land/lipgloss/v2` — styling
- `charm.land/huh` — form/confirm/filter prompts
- Standard library for everything else

### Runtime Dependencies
- **Required:** `git`
- **Optional:** `tmux`, `zoxide`, `gh`

## Nix Packaging

### New repo flake (`~/Data/git/noamsto/wt/flake.nix`)

```nix
{
  outputs = { self, nixpkgs, flake-parts, ... }:
    # buildGoModule, expose packages.default and homeManagerModules.default
}
```

Home-manager module: `programs.wt` with options for `enable` and fish completions.

### Lazytmux integration

Lazytmux adds the new wt flake as an input. `programs.lazytmux.wt.enable` uses the wt package from the input. Fish completions come from the wt repo.

## Testing

- **`internal/git/`**: unit tests with temp git repos (create repo, add worktrees, verify detection)
- **Integration**: test full command flows against temp repos
- **TUI**: manual testing (same as current wt-explorer approach)
- **CI**: `nix build`, `nix flake check`, `go test ./...`

## Migration Path

1. Build new `wt` Go binary in new repo
2. Update lazytmux to use new repo as flake input
3. Remove `wt/wt.sh` and `wt-explorer/` from lazytmux
4. Update lazytmux home-manager module to delegate to wt's module
