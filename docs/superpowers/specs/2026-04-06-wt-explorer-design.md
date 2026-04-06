# wt-explorer: Interactive Worktree Explorer TUI

## Summary

A new Go binary (`wt-explorer`) providing a bubbletea TUI for browsing, inspecting, and removing git worktrees. Launched via `wt clean -i`. The existing `wt` bash script stays as-is for all non-interactive commands.

## Motivation

`wt clean` removes stale worktrees automatically, but when removal fails (uncommitted changes, unpushed commits), the user must manually run `git worktree remove --force` per worktree. An interactive explorer lets you see *why* removal failed, inspect the worktree state, and decide per-worktree whether to force-remove, skip, or batch-delete.

## Architecture

### Two tools, each playing to its strengths

- **`wt` (bash)** — CLI for worktree CRUD: `smart`, `list`, `remove`, `clean`, `z`, `main`. Shell-outs to git/tmux are natural in bash. Stays as-is.
- **`wt-explorer` (Go)** — Interactive TUI for browsing and removing worktrees. Bubbletea + lipgloss for rendering. Self-contained: does its own git/tmux queries.

`wt clean -i` execs into `wt-explorer`. The Go binary can also be invoked directly.

### Invocation

```bash
# Via wt
wt clean -i

# Direct (repo root as argument)
wt-explorer /path/to/repo
```

## Module Structure

```
wt-explorer/
  main.go          # Entry point, parse repo root arg, launch TUI
  git.go           # Git queries: list worktrees, dirty status, log, stale detection
  tmux.go          # Kill window by @worktree, list windows
  tui.go           # Bubbletea model, update, view
  go.mod           # github.com/noamsto/lazytmux/wt-explorer
  default.nix      # buildGoModule, runtime: git, tmux, gh
```

No CLI framework — single positional arg (repo root).

## TUI Design

### Layout: List + Preview

```
+-- Search --------------------------------------------------------+
| filter...                                                        |
+-- Worktrees ---------------------+-- Details --------------------+
| *  feat-auth    [merged]         | Branch: feat-auth             |
|    feat-upload                   | Path: .worktrees/feat-auth    |
| *  fix-cors     [remote gone]    |                               |
|    refactor-db                   | Dirty files:                  |
|                                  |   M src/handler.go            |
|                                  |   ?? debug.log                |
|                                  |                               |
|                                  | Unpushed commits:             |
|                                  |   a1b2c3 fix auth flow        |
|                                  |                               |
|                                  | Last commit:                  |
|                                  |   d4e5f6 wip (2 days ago)     |
+----------------------------------+-------------------------------+
| up/down navigate  space select  a sel stale  d/D delete  q quit  |
+------------------------------------------------------------------+
```

### List behavior

- **All worktrees** shown (not just stale)
- **Stale worktrees sorted to top** with reason tag: `[merged]`, `[remote gone]`, `[PR #42 merged]`
- Stale marker: filled dot or similar visual indicator
- Search/filter by branch name (typed input filters the list)
- Preview pane updates on cursor move

### Selection and actions

| Key | Action |
|-----|--------|
| `up/down`, `j/k` | Navigate |
| `space` | Toggle selection on current item |
| `a` | Select all stale worktrees |
| `d` | Delete selected (or cursor item if none selected). Tries normal `git worktree remove` first. |
| `D` | Force delete selected (or cursor item). Uses `git worktree remove --force`. |
| `/` or typing | Filter list by branch name |
| `q`, `esc` | Quit |

### Batch operations

- When items are selected (via `space` or `a`), `d`/`D` operates on **all selected**
- Confirmation shown before batch delete: "Remove N worktrees? y/n"
- Results shown inline per worktree (checkmark or error)
- If no items selected, `d`/`D` operates on cursor item

### Preview pane content

For the worktree under cursor:

1. **Branch** name
2. **Path** (relative to repo root)
3. **Stale reason** (if stale)
4. **Dirty files** — `git status --short` output
5. **Unpushed commits** — `git log --oneline @{upstream}..HEAD`
6. **Last commit** — `git log -1 --format="%h %s (%ar)"`

## Stale Detection

Same 3 strategies as current `wt clean` bash implementation, run once at TUI startup:

1. **`git branch --merged <default-branch>`** — fast, catches regular merges and fast-forwards
2. **Remote branch deleted** — `git show-ref --verify refs/remotes/origin/<branch>` fails after `git fetch --prune`
3. **GitHub PR squash-merged** — `gh pr list --head <branch> --state merged` (parallel checks, optional if `gh` is available)

Results stored as `map[branch]reason` for tagging items in the list.

## Nix Integration

### wt-explorer/default.nix

```nix
{pkgs}:
pkgs.buildGoModule {
  pname = "wt-explorer";
  version = "0.1.0";
  src = ./.;
  vendorHash = "sha256-...";
  ldflags = ["-s" "-w"];
  nativeBuildInputs = [pkgs.git];  # for go generate if needed
}
```

### wt/default.nix change

Add `wt-explorer` to `runtimeInputs` so bash can exec into it:

```nix
{pkgs, wt-explorer}:
pkgs.writeShellApplication {
  name = "wt";
  runtimeInputs = with pkgs; [git tmux gum zoxide wt-explorer];
  text = builtins.readFile ./wt.sh;
}
```

### flake.nix

Expose as `packages.wt-explorer` alongside existing packages.

## Bash Changes

### wt.sh — `-i` flag already parsed

The `clean` dispatch becomes:

```bash
clean | prune)
    if [[ $INTERACTIVE == "true" ]]; then
        exec wt-explorer "$(get_repo_root)"
    else
        wt_clean
    fi
    ;;
```

### wt clean failure tip

Already updated to:

```
Tip: use 'wt clean -i' to interactively explore and force-remove
```

## Dependencies

### Go module

- `charm.land/bubbletea/v2` — TUI framework
- `charm.land/bubbles/v2` — viewport for preview pane
- `charm.land/lipgloss/v2` — styling
- No other deps needed (git/tmux/gh via exec)

### Runtime (Nix)

- `git` — worktree operations
- `tmux` — kill windows for removed worktrees
- `gh` — optional, squash-merge detection

## Out of Scope

- Worktree creation (stays in bash `wt smart`)
- Session/window switching (stays in bash)
- Zoxide integration (stays in bash `wt z`)
- Claude status display (not relevant for worktree management)
- Process icons (not relevant here)
