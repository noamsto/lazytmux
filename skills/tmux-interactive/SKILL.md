---
name: tmux-interactive
description: Use when running interactive CLI programs (Python REPL, gdb, psql, node, lldb) that require keystroke-level control, output scraping, or waiting for prompts inside a tmux pane.
---

# tmux Interactive Sessions

Remote-control interactive programs via tmux: send keystrokes, scrape output, wait for prompts.

## Quickstart

```bash
# Isolated socket — never touches your personal tmux sessions
SOCKET_DIR="${TMPDIR:-/tmp}/claude-tmux-sockets"
mkdir -p "$SOCKET_DIR"
SOCKET="$SOCKET_DIR/claude.sock"
SESSION="claude-work"   # slug-like, no spaces

tmux -S "$SOCKET" new-session -d -s "$SESSION" -x 220 -y 50
tmux -S "$SOCKET" send-keys -t "$SESSION" -- 'python3 -q' Enter
tmux -S "$SOCKET" capture-pane -p -J -t "$SESSION" -S -200
```

**After starting any session, immediately print this for the user:**
```
Monitor this session:
  tmux -S /tmp/claude-tmux-sockets/claude.sock attach -t claude-work

Or capture output once:
  tmux -S /tmp/claude-tmux-sockets/claude.sock capture-pane -p -J -t claude-work -S -200
```

Print this again at the end of the tool loop.

## Sending Input

```bash
# Literal send — safe, no shell expansion
tmux -S "$SOCKET" send-keys -t "$SESSION" -l -- 'print("hello")'
tmux -S "$SOCKET" send-keys -t "$SESSION" -- Enter

# Control keys
tmux -S "$SOCKET" send-keys -t "$SESSION" -- C-c   # interrupt
tmux -S "$SOCKET" send-keys -t "$SESSION" -- C-d   # EOF / exit
```

## Capturing Output

```bash
# Capture last 200 lines, join wrapped lines
tmux -S "$SOCKET" capture-pane -p -J -t "$SESSION" -S -200
```

## Waiting for Prompts (Polling)

Do NOT proceed after sending input without waiting for the prompt. Poll:

```bash
# Poll until pattern appears (max 15s, check every 0.5s)
end=$((SECONDS + 15))
while [ $SECONDS -lt $end ]; do
  out=$(tmux -S "$SOCKET" capture-pane -p -J -t "$SESSION" -S -200)
  echo "$out" | grep -qE '^>>>' && break
  sleep 0.5
done
```

Common prompt patterns:

| Program | Pattern |
|---------|---------|
| Python REPL | `^>>>` |
| psql | `=#` or `=#\s` |
| node | `^>` |
| gdb/lldb | `\(gdb\)` / `\(lldb\)` |
| bash/fish | `\$\s*$` |

## Program-Specific Notes

**Python REPL** — always set `PYTHON_BASIC_REPL=1` or the readline UI breaks `send-keys`:
```bash
tmux -S "$SOCKET" send-keys -t "$SESSION" -- 'PYTHON_BASIC_REPL=1 python3 -q' Enter
# wait for ^>>>
tmux -S "$SOCKET" send-keys -t "$SESSION" -l -- 'import os; print(os.getcwd())'
tmux -S "$SOCKET" send-keys -t "$SESSION" -- Enter
```

**gdb/lldb** — disable pager immediately to avoid `--More--` pauses:
```bash
# gdb
tmux ... send-keys -l -- 'set pagination off' && tmux ... send-keys -- Enter
# lldb (preferred debugger per mitsuhiko skill)
tmux ... send-keys -l -- 'settings set term-width 200' && tmux ... send-keys -- Enter
```

**psql / mysql** — connect, wait for prompt, then send queries normally.

## Cleanup

```bash
tmux -S "$SOCKET" kill-session -t "$SESSION"    # one session
tmux -S "$SOCKET" kill-server                   # all claude sessions
```

## Your Session Model

Your tmux uses `<repo>/<branch>` session naming (e.g. `nix-config/main`). Interactive tool sessions are intentionally **separate** — use the isolated socket pattern above so they never collide with your worktree sessions. The `worktree-tmux-integration` skill handles the `<repo>/<branch>` sessions; this skill handles throwaway interactive ones.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Not waiting for prompt after send | Always poll before sending next command |
| Using `-L` (socket name) instead of `-S` (socket path) | Always use `-S "$SOCKET"` |
| Forgetting `PYTHON_BASIC_REPL=1` | Python's readline UI garbles send-keys |
| Starting without explicit width/height | Add `-x 220 -y 50` to `new-session` to avoid narrow pane wrapping |
| Not telling user how to monitor | Print monitor command immediately after session start |
