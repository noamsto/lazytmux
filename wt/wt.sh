#!/usr/bin/env bash
# Git worktree + tmux session manager
# Bash + gum version (for Claude/Amp compatibility)

# Flags
SKIP_PROMPT=false
QUIET=false
NO_SWITCH=false

# Colors for non-gum output
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

log() {
  [[ $QUIET == "true" ]] && return
  echo "$@"
}

log_success() {
  [[ $QUIET == "true" ]] && return
  echo -e "${GREEN}✓${NC} $*"
}

log_error() {
  echo -e "${RED}Error:${NC} $*" >&2
}

log_header() {
  [[ $QUIET == "true" ]] && return
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "$1"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
}

confirm() {
  local prompt="$1"
  if [[ $SKIP_PROMPT == "true" ]]; then
    return 0
  fi
  gum confirm "$prompt"
}

wt_help() {
  cat <<'EOF'
Git Worktree + Tmux Window Manager

Usage:
  wt <branch>           Smart switch/create (prompts before creating)
  wt -y <branch>        Skip prompts
  wt -q <branch>        Quiet mode (only output path)
  wt -n <branch>        No tmux (skip window creation/switching)
  wt -yqn <branch>      Combine flags (for Claude/scripts)
  wt z [query]          Fuzzy find worktree, output path (use: cd "$(wt z)")
  wt main               Switch to root repository window
  wt list               List all worktrees
  wt remove <branch>    Remove worktree + kill window
  wt clean              Remove stale worktrees (merged, squash-merged, deleted)
  wt help               Show this help

Model: Session = Project, Window = Worktree
  • One tmux session per repository (e.g., 'nix-config')
  • One window per worktree/branch (e.g., 'feat-x', 'fix-y')
  • Fast switching between branches: `n / `p

Smart mode:
  • Worktree exists     → switch to window (unless -n)
  • Branch exists       → prompt to create worktree
  • Branch not found    → prompt to create new branch

Claude usage:  cd "$(wt -yqn feature-x)"

Worktree location: .worktrees/<branch-name>
EOF
}

# Find window index by @worktree option
wt_find_window_by_worktree() {
  local session="$1"
  local worktree="$2"
  tmux list-windows -t "$session" -F '#{window_index}:#{@worktree}' 2>/dev/null | while IFS=: read -r idx wt; do
    if [[ $wt == "$worktree" ]]; then
      echo "$idx"
      return 0
    fi
  done
}

# Switch to or create tmux session/window
wt_switch_to_session() {
  local session_name="$1" # Format: repo-name/branch
  local worktree_path="$2"

  # If NO_SWITCH, skip all tmux operations - just output the path
  if [[ $NO_SWITCH == "true" ]]; then
    if [[ $QUIET == "true" ]]; then
      echo "$worktree_path"
    else
      echo ""
      echo "Worktree: $worktree_path"
    fi
    return 0
  fi

  # Parse repo and branch from session_name
  local repo_name="${session_name%%/*}"
  local branch_name="${session_name#*/}"

  if [[ -n ${TMUX:-} ]]; then
    # Inside tmux - use window-per-worktree model
    local current_session
    current_session=$(tmux display-message -p '#{session_name}')

    # Check if we're already in a session for this repo
    local in_repo_session=false
    if [[ $current_session == "$repo_name" ]] || [[ $current_session == "$repo_name/"* ]]; then
      in_repo_session=true
    fi

    if [[ $in_repo_session == "true" ]]; then
      # Already in repo session - create/switch to window
      local target_session="$current_session"

      # Check if window already exists (by @worktree option)
      local window_idx
      window_idx=$(wt_find_window_by_worktree "$target_session" "$worktree_path")

      if [[ -n $window_idx ]]; then
        # Window exists, switch to it
        tmux select-window -t "$target_session:$window_idx"
        log_success "Switched to window: $target_session:$window_idx"
      else
        # Create new window
        tmux new-window -a -t "$target_session" -c "$worktree_path"
        tmux set-option -t "$target_session" -w @worktree "$worktree_path"
        tmux set-option -t "$target_session" -w @branch "$branch_name"
        log_success "Created window in: $target_session"
      fi
    else
      # Different session - switch to repo session
      local target_idx=""

      if tmux has-session -t "$repo_name" 2>/dev/null; then
        # Session exists, check for window
        local window_idx
        window_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
        if [[ -z $window_idx ]]; then
          # Create window
          tmux new-window -a -t "$repo_name" -c "$worktree_path"
          tmux set-option -t "$repo_name" -w @worktree "$worktree_path"
          tmux set-option -t "$repo_name" -w @branch "$branch_name"
        fi
        target_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
      else
        # Create session
        tmux new-session -d -s "$repo_name" -c "$worktree_path"
        tmux set-option -t "$repo_name" -w @worktree "$worktree_path"
        tmux set-option -t "$repo_name" -w @branch "$branch_name"
      fi

      if [[ -n $target_idx ]]; then
        tmux switch-client -t "$repo_name:$target_idx"
      else
        tmux switch-client -t "$repo_name"
      fi
    fi
  else
    # Outside tmux - create session with window
    if tmux has-session -t "$repo_name" 2>/dev/null; then
      # Check if window exists
      local window_idx
      window_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
      if [[ -z $window_idx ]]; then
        tmux new-window -a -t "$repo_name" -c "$worktree_path"
        tmux set-option -t "$repo_name" -w @worktree "$worktree_path"
        tmux set-option -t "$repo_name" -w @branch "$branch_name"
      fi
      local target_idx
      target_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
      if [[ -n $target_idx ]]; then
        tmux attach-session -t "$repo_name:$target_idx"
      else
        tmux attach-session -t "$repo_name"
      fi
    else
      # Create new session
      tmux new-session -d -s "$repo_name" -c "$worktree_path"
      tmux set-option -t "$repo_name" -w @worktree "$worktree_path"
      tmux set-option -t "$repo_name" -w @branch "$branch_name"
      tmux attach-session -t "$repo_name"
    fi
  fi

  # Output path (always for quiet mode, as final line for normal mode)
  if [[ $QUIET == "true" ]]; then
    echo "$worktree_path"
  else
    echo ""
    echo "Worktree: $worktree_path"
  fi
}

# Create worktree and switch to session
wt_create_worktree() {
  local repo_root="$1"
  local branch="$2"
  local worktree_path="$3"
  local session_name="$4"
  local create_branch="$5"
  local is_remote="$6"

  log_header "🌿 Creating git worktree + tmux session"
  log "Creating worktree..."

  # Create worktree
  local git_output
  if [[ $create_branch == "true" ]]; then
    # New branch
    git_output=$(git -C "$repo_root" worktree add -b "$branch" "$worktree_path" 2>&1) || {
      log_error "Failed to create worktree"
      [[ $QUIET == "false" ]] && echo "$git_output" >&2
      return 1
    }
  elif [[ $is_remote == "true" ]]; then
    # Track remote branch
    git_output=$(git -C "$repo_root" worktree add --track -b "$branch" "$worktree_path" "origin/$branch" 2>&1) || {
      log_error "Failed to create worktree"
      [[ $QUIET == "false" ]] && echo "$git_output" >&2
      return 1
    }
  else
    # Existing local branch
    git_output=$(git -C "$repo_root" worktree add "$worktree_path" "$branch" 2>&1) || {
      log_error "Failed to create worktree"
      [[ $QUIET == "false" ]] && echo "$git_output" >&2
      return 1
    }
  fi

  log_success "Worktree created: $worktree_path"

  # Add to zoxide for fuzzy finding
  zoxide add "$worktree_path" 2>/dev/null || true

  log ""

  wt_switch_to_session "$session_name" "$worktree_path"
}

# Get the true repository root (works from worktrees too)
get_repo_root() {
  local git_common_dir
  git_common_dir=$(git rev-parse --git-common-dir)

  # If git-common-dir is ".git", we're in the main repo
  if [[ $git_common_dir == ".git" ]]; then
    git rev-parse --show-toplevel
  else
    # We're in a worktree - git-common-dir points to main repo's .git
    # e.g., /path/to/repo/.git or /path/to/repo/.git/worktrees/branch
    # Resolve to absolute path and get parent
    local abs_git_dir
    abs_git_dir=$(cd "$(dirname "$git_common_dir")" && pwd)/$(basename "$git_common_dir")
    # Remove /.git suffix to get repo root
    echo "${abs_git_dir%/.git}"
  fi
}

# Smart worktree handler with prompts
wt_smart() {
  local branch="$1"

  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  local repo_name
  repo_name=$(basename "$repo_root")
  local worktree_path="$repo_root/.worktrees/$branch"
  local session_name="$repo_name/$branch"

  # Case 1: Worktree already exists → switch to it
  local existing_worktree=""
  while IFS= read -r line; do
    if [[ $line =~ \[$branch\]$ ]]; then
      existing_worktree="${line%% *}"
      break
    fi
  done < <(git -C "$repo_root" worktree list 2>/dev/null)

  if [[ -n $existing_worktree ]]; then
    # Check if directory actually exists (worktree/branch mismatch)
    if [[ ! -d $existing_worktree ]]; then
      log_error "Worktree directory missing: $existing_worktree"
      log "Git thinks branch '$branch' has a worktree, but directory doesn't exist."
      echo ""
      if confirm "Run 'git worktree prune' to fix stale references?"; then
        git -C "$repo_root" worktree prune
        log_success "Pruned stale worktree references"
        echo ""
        # Now continue to create new worktree
        existing_worktree=""
      else
        log "You can manually fix with: git worktree prune"
        return 1
      fi
    else
      wt_switch_to_session "$session_name" "$existing_worktree"
      return $?
    fi
  fi

  # Case 2: Check if branch exists (local or remote)
  local branch_exists=false
  local is_remote=false

  if git -C "$repo_root" show-ref --verify --quiet "refs/heads/$branch" 2>/dev/null; then
    branch_exists=true
  elif git -C "$repo_root" show-ref --verify --quiet "refs/remotes/origin/$branch" 2>/dev/null; then
    branch_exists=true
    is_remote=true
  fi

  if [[ $branch_exists == "true" ]]; then
    # Branch exists but no worktree
    local source_desc="local branch"
    if [[ $is_remote == "true" ]]; then
      source_desc="remote branch origin/$branch"
    fi
    log "Branch '$branch' exists ($source_desc) but has no worktree."
    if confirm "Create worktree at .worktrees/$branch?"; then
      wt_create_worktree "$repo_root" "$branch" "$worktree_path" "$session_name" false "$is_remote"
    else
      log "Cancelled."
      return 1
    fi
  else
    # Case 3: Branch doesn't exist → create new
    log "Branch '$branch' does not exist."
    if confirm "Create new branch + worktree?"; then
      wt_create_worktree "$repo_root" "$branch" "$worktree_path" "$session_name" true false
    else
      log "Cancelled."
      return 1
    fi
  fi
}

wt_main() {
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  local repo_name
  repo_name=$(basename "$repo_root")
  # Use repo/main format so wt_switch_to_session can parse it
  local session_name="$repo_name/main"

  wt_switch_to_session "$session_name" "$repo_root"
}

wt_list() {
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  git -C "$repo_root" worktree list
}

wt_remove() {
  local branch="$1"

  if [[ -z $branch ]]; then
    log_error "Branch name required"
    echo "Usage: wt remove <branch-name>"
    return 1
  fi

  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  local repo_name
  repo_name=$(basename "$repo_root")

  # Find worktree path from git worktree list
  local worktree_path=""
  while IFS= read -r line; do
    if [[ $line =~ \[$branch\]$ ]]; then
      worktree_path="${line%% *}"
      break
    fi
  done < <(git -C "$repo_root" worktree list 2>/dev/null)

  if [[ -z $worktree_path ]]; then
    log_error "No worktree found for branch '$branch'"
    echo ""
    echo "Available worktrees:"
    wt_list
    return 1
  fi

  log_header "🗑️  Removing worktree + tmux window"

  # Kill tmux window if it exists (find by @worktree option)
  if tmux has-session -t "$repo_name" 2>/dev/null; then
    local window_idx
    window_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
    if [[ -n $window_idx ]]; then
      log "Killing tmux window: $repo_name:$window_idx"
      tmux kill-window -t "$repo_name:$window_idx"
      log_success "Window killed"
    else
      log "No tmux window found for worktree: $worktree_path"
    fi
  else
    log "No tmux session found: $repo_name"
  fi

  log ""
  log "Removing worktree: $worktree_path"
  if git -C "$repo_root" worktree remove "$worktree_path"; then
    log_success "Worktree removed"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "✓ Complete"
  else
    log_error "Failed to remove worktree"
    return 1
  fi
}

# Clean up worktrees with merged branches
# Uses three strategies to detect stale worktrees:
#   1. git branch --merged (regular merges / fast-forwards)
#   2. Remote branch deleted after merge (repos with auto-delete)
#   3. GitHub PR squash-merged (requires gh CLI, checks in parallel)
wt_clean() {
  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  local repo_name
  repo_name=$(basename "$repo_root")

  # Determine default branch (main or master)
  local default_branch
  if git -C "$repo_root" show-ref --verify --quiet refs/heads/main; then
    default_branch=main
  elif git -C "$repo_root" show-ref --verify --quiet refs/heads/master; then
    default_branch=master
  else
    log_error "Could not find main or master branch"
    return 1
  fi

  # Fetch and prune to get accurate remote state
  log "Fetching latest remote state..."
  git -C "$repo_root" fetch --prune 2>/dev/null || true

  # Build list of worktree branches and paths (excluding main worktree and default branch)
  local wt_branches=()
  local wt_paths=()
  local current_path=""
  while IFS= read -r line; do
    if [[ $line == worktree\ * ]]; then
      current_path="${line#worktree }"
    elif [[ $line == branch\ refs/heads/* ]]; then
      local current_branch="${line#branch refs/heads/}"
      if [[ $current_path != "$repo_root" && $current_branch != "$default_branch" ]]; then
        wt_branches+=("$current_branch")
        wt_paths+=("$current_path")
      fi
      current_path=""
    fi
  done < <(git -C "$repo_root" worktree list --porcelain 2>/dev/null)

  if [[ ${#wt_branches[@]} -eq 0 ]]; then
    log "No worktrees to clean (besides main)."
    return 0
  fi

  # Detect stale worktrees using multiple strategies
  local stale_indices=()
  local stale_reasons=()
  local -A checked_map # track indices already identified as stale

  # Strategy 1: git branch --merged (fast, works for regular merges)
  local git_merged
  git_merged=$(git -C "$repo_root" branch --merged "$default_branch" 2>/dev/null | sed 's/^[*+ ]*//')

  for i in "${!wt_branches[@]}"; do
    if echo "$git_merged" | grep -qxF "${wt_branches[$i]}"; then
      stale_indices+=("$i")
      stale_reasons+=("merged into $default_branch")
      checked_map[$i]=1
    fi
  done

  # Strategy 2: remote branch deleted after merge (works with auto-delete on GitHub)
  for i in "${!wt_branches[@]}"; do
    [[ -n ${checked_map[$i]:-} ]] && continue
    if ! git -C "$repo_root" show-ref --verify --quiet "refs/remotes/origin/${wt_branches[$i]}" 2>/dev/null; then
      stale_indices+=("$i")
      stale_reasons+=("remote branch deleted")
      checked_map[$i]=1
    fi
  done

  # Strategy 3: GitHub PR squash-merged (parallel checks via gh CLI)
  local unchecked=()
  for i in "${!wt_branches[@]}"; do
    [[ -n ${checked_map[$i]:-} ]] && continue
    unchecked+=("$i")
  done

  if [[ ${#unchecked[@]} -gt 0 ]] && command -v gh &>/dev/null; then
    log "Checking ${#unchecked[@]} branches against GitHub PRs..."

    local tmpdir
    tmpdir=$(mktemp -d)

    for idx in "${unchecked[@]}"; do
      (
        local branch="${wt_branches[$idx]}"
        local pr_num
        pr_num=$(cd "$repo_root" && gh pr list --head "$branch" --state merged --json number --jq '.[0].number' 2>/dev/null) || true
        if [[ -n $pr_num ]]; then
          echo "$pr_num" >"$tmpdir/$idx"
        fi
      ) &
    done
    wait || true

    for idx in "${unchecked[@]}"; do
      if [[ -f "$tmpdir/$idx" ]]; then
        local pr_num
        pr_num=$(<"$tmpdir/$idx")
        stale_indices+=("$idx")
        stale_reasons+=("PR #$pr_num squash-merged")
      fi
    done

    rm -rf "$tmpdir"
  elif [[ ${#unchecked[@]} -gt 0 ]]; then
    log "Tip: install gh CLI to also detect squash-merged branches"
  fi

  if [[ ${#stale_indices[@]} -eq 0 ]]; then
    log "No stale worktrees found."
    return 0
  fi

  log_header "🧹 Found ${#stale_indices[@]} stale worktree(s)"

  for j in "${!stale_indices[@]}"; do
    local i="${stale_indices[$j]}"
    echo "  • ${wt_branches[$i]} (${stale_reasons[$j]})"
    echo "    ${wt_paths[$i]}"
  done
  echo ""

  if ! confirm "Remove all stale worktrees?"; then
    log "Cancelled."
    return 1
  fi

  echo ""
  local failed=0
  local failed_lines=()

  for j in "${!stale_indices[@]}"; do
    local i="${stale_indices[$j]}"
    local branch="${wt_branches[$i]}"
    local worktree_path="${wt_paths[$i]}"

    log "Removing: $branch"

    # Kill tmux window if it exists
    if tmux has-session -t "$repo_name" 2>/dev/null; then
      local window_idx
      window_idx=$(wt_find_window_by_worktree "$repo_name" "$worktree_path")
      if [[ -n $window_idx ]]; then
        tmux kill-window -t "$repo_name:$window_idx" 2>/dev/null || true
      fi
    fi

    # Remove worktree
    local git_error
    if git_error=$(git -C "$repo_root" worktree remove "$worktree_path" 2>&1); then
      echo "  ✓ Removed worktree"
    else
      echo "  ❌ Failed: $git_error"
      failed=$((failed + 1))
      failed_lines+=("  • $branch: $git_error")
    fi
  done

  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  local cleaned=$((${#stale_indices[@]} - failed))
  if [[ $failed -eq 0 ]]; then
    echo "✓ Cleaned ${#stale_indices[@]} worktree(s)"
  else
    echo "⚠ Cleaned $cleaned worktree(s), $failed failed:"
    for line in "${failed_lines[@]}"; do
      echo "$line"
    done
    echo ""
    echo "Tip: use 'git worktree remove --force <path>' for worktrees with uncommitted changes"
  fi

  if [[ $cleaned -gt 0 ]]; then
    echo ""
    echo "Run 'nix store gc' to reclaim nix store space from removed worktrees"
  fi

  return 0
}

# Fuzzy find worktree using zoxide - outputs path for cd
wt_z() {
  local query="${1:-}"

  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    log_error "Not in a git repository"
    return 1
  fi

  local repo_root
  repo_root=$(get_repo_root)
  local repo_name
  repo_name=$(basename "$repo_root")

  # Get worktree paths (excluding main)
  local worktree_paths=()
  while IFS= read -r line; do
    local wt_path="${line%% *}"
    if [[ $wt_path != "$repo_root" ]]; then
      worktree_paths+=("$wt_path")
    fi
  done < <(git -C "$repo_root" worktree list 2>/dev/null)

  if [[ ${#worktree_paths[@]} -eq 0 ]]; then
    log_error "No worktrees found (besides main)"
    return 1
  fi

  local result
  if [[ -n $query ]]; then
    # Filter paths matching query, use zoxide frecency to sort
    local matches=()
    for path in "${worktree_paths[@]}"; do
      if [[ $path == *"$query"* ]]; then
        matches+=("$path")
      fi
    done

    if [[ ${#matches[@]} -eq 0 ]]; then
      log_error "No worktree matching '$query'"
      return 1
    elif [[ ${#matches[@]} -eq 1 ]]; then
      result="${matches[0]}"
    else
      # Multiple matches - pick interactively
      result=$(printf '%s\n' "${matches[@]}" | gum filter --placeholder "Select worktree...")
      [[ -z $result ]] && return 1
    fi
  else
    # No query - interactive pick from all worktrees
    result=$(printf '%s\n' "${worktree_paths[@]}" | gum filter --placeholder "Select worktree...")
    [[ -z $result ]] && return 1
  fi

  echo "$result"
}

# Main entry point
main() {
  local args=()

  # Parse flags
  while [[ $# -gt 0 ]]; do
    case "$1" in
    -y | --yes)
      SKIP_PROMPT=true
      shift
      ;;
    -q | --quiet)
      QUIET=true
      shift
      ;;
    -n | --no-switch)
      NO_SWITCH=true
      shift
      ;;
    -*)
      # Handle combined short flags like -yqn
      local flags="${1#-}"
      if [[ $flags =~ ^[yqn]+$ ]]; then
        [[ $flags == *y* ]] && SKIP_PROMPT=true
        [[ $flags == *q* ]] && QUIET=true
        [[ $flags == *n* ]] && NO_SWITCH=true
        shift
      else
        args+=("$1")
        shift
      fi
      ;;
    *)
      args+=("$1")
      shift
      ;;
    esac
  done

  local subcommand="${args[0]:-}"

  case "$subcommand" in
  list | ls)
    wt_list
    ;;
  remove | rm)
    wt_remove "${args[1]:-}"
    ;;
  clean | prune)
    wt_clean
    ;;
  z)
    wt_z "${args[1]:-}"
    ;;
  main)
    wt_main
    ;;
  help | -h | --help | "")
    wt_help
    ;;
  *)
    # Smart mode: branch name given directly
    if [[ -n $subcommand ]]; then
      wt_smart "$subcommand"
    else
      wt_help
    fi
    ;;
  esac
}

main "$@"
