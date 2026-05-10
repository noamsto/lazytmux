# Fast, Non-Corrupting Snapshot Restore

**Date:** 2026-05-10
**Status:** Approved
**Repo (spec):** `~/Data/git/lazytmux`
**Repo (implementation):** `~/Data/git/noamsto/tmux-state`

## Problem

`prefix + R` (snapshot picker) calls `tmux-state pick --kind=snapshot`, which runs `restore.Apply` on the chosen snapshot. The current apply path:

1. Recreates sessions/windows/panes/layouts via direct `tmux` commands. (Fast — fine.)
2. For each allow-listed pane, runs `tmux send-keys -t <pane> "<cmd>" Enter` — types the command into a freshly-spawned shell.
3. For each pane with stored scrollback (commonly ~500 KB compressed → multi-MB raw, ×N panes), writes the raw bytes to a temp file, `load-buffer`, then `paste-buffer -t <pane>` — dumping the entire historical terminal output **into the live shell as input**.

Step 3 is the dealbreaker. Shells (and bracketed-paste handlers) were never designed to absorb hundreds of kilobytes of escape sequences and command output as keystrokes. With many panes, the system stalls; pasted scrollback also corrupts pane state (escape sequences misrender, embedded newlines may be interpreted, prompt redraws stack up). Step 2 races with shell init in the same way.

`tmux-resurrect` (which `tmux-continuum` drives) avoids this entirely: it bakes a `cat <scrollback>; exec <shell>` string into the pane's *creation* command. The pane is born running it, the terminal renders the scrollback once as static stdout, then `exec` replaces `cat` with the interactive shell. The shell never sees scrollback as input.

This spec ports that pattern to `tmux-state`.

## Goals

- Eliminate `tmux paste-buffer` and `tmux send-keys` from the restore path entirely.
- Render saved scrollback as static terminal history, not as shell input.
- Preserve current behavior for everything else: layout, cwds, allow-listed command relaunch, the smart filter, the `restoreMode = "auto"` opt-in.
- No change required in lazytmux. After tmux-state ships, lazytmux just bumps `flake.lock`.

## Non-goals

- Fixing the leftover empty window-0 created by `tmux new-session` before `CreateWindow` runs. Real pre-existing bug, separate fix.
- Restoring scrollback into shell *history* (`Up arrow` won't recall it). Resurrect doesn't either; it's correct that pasted output isn't shell history.
- Cross-host snapshot portability (already out of scope for tmux-state).

## Approach

Each pane's startup command becomes a single shell string of the form:

```
<self> cat-scrollback <sha>; exec <relaunch-or-shell>
```

passed as the trailing `shell-command` argument to `tmux new-session` / `tmux new-window` / `tmux split-window`. tmux runs it via `/bin/sh -c`, so multi-statement strings work. When the `cat-scrollback` step exits, `exec` replaces it with the user's allow-listed command or default shell — that single process becomes the pane's interactive program.

`<self>` is the absolute path of the running `tmux-state` binary, resolved once per restore via `os.Executable()`. This keeps the restore plan self-contained — no dependency on `zstd` being in PATH, no temp files to clean up, no environment assumptions.

`<relaunch-or-shell>` is either the allow-listed command (with arguments) from the snapshot, or the user's tmux `default-shell` if no relaunch applies.

## Action model (`internal/restore/plan.go`)

Add one optional field to the three creation actions; delete the two post-creation actions.

```go
type CreateSession struct { Name, Cwd, StartupCommand string }
type CreateWindow  struct { Session string; Index int; Name, Cwd, StartupCommand string }
type SplitPane     struct { Target, Cwd, StartupCommand string }
type SetLayout     struct { Window, Layout string } // unchanged

// Deleted:
//   RelaunchCommand
//   RestoreScrollback
```

`StartupCommand == ""` means "use tmux default-command" — same behavior as today's no-relaunch / no-scrollback case.

## Plan composition (`BuildPlan`)

Per kept pane, compute `StartupCommand` from the snapshot data:

| scrollback present | command allow-listed | `StartupCommand`                                                   |
|--------------------|----------------------|--------------------------------------------------------------------|
| no                 | no                   | `""`                                                               |
| no                 | yes                  | `<cmd> <quoted-args...>`                                           |
| yes                | no                   | `<self> cat-scrollback <sha>; exec <default-shell>`                |
| yes                | yes                  | `<self> cat-scrollback <sha>; exec <cmd> <quoted-args...>`         |

- `<self>`, `<default-shell>`, and the allow-list are passed into `BuildPlan` (same shape as today's `allowList` parameter).
- Argument quoting uses `strconv.Quote`, matching today's `RelaunchCommand` formatting.
- The first kept pane of each window owns its `CreateWindow.StartupCommand` (or `CreateSession.StartupCommand` for the implicit session-default-window). Subsequent kept panes own `SplitPane.StartupCommand`.

## Apply (`internal/restore/apply.go`)

```
CreateSession  → tmux new-session  -d -s <name> -c <cwd> [<startup>]
CreateWindow   → tmux new-window   -d -t <s>:<i> -n <name> -c <cwd> [<startup>]
SplitPane      → tmux split-window -t <target> -c <cwd> [<startup>]
SetLayout      → tmux select-layout -t <window> <layout>
```

The trailing `<startup>` argument is omitted when `StartupCommand == ""`; otherwise it's passed as a single arg. Errors stay best-effort (continue on failure), matching today's behavior.

Delete: `pasteScrollback`, `ApplyWithScrollback`, the `ScrollbackReader` interface, `randHex`, and the temp-file/load-buffer/paste-buffer/delete-buffer dance. The restore caller no longer injects a scrollback reader.

## New subcommand: `tmux-state cat-scrollback <sha>`

Tiny `cobra` command that:
- Resolves the on-disk store path for `<sha>` via the existing `scrollback.Store`.
- Streams decompressed bytes (zstd) to stdout.
- Exits 0 on success **and** on missing-file (silent degrade — pane gets a fresh shell with no preceding history; restore must never fail because of a stale snapshot reference).
- On mid-stream I/O or decompression error: writes whatever was already produced, exits 0. The user sees a partial scrollback above their prompt; the pane still functions. Restore correctness must not depend on scrollback integrity.
- Exits non-zero only on malformed sha (which would be a programming error in `BuildPlan`, not user-visible).

This subcommand is an internal implementation detail of restore. It's not advertised in `--help` for end users (or, equivalently, marked hidden via cobra's `Hidden: true`).

## Default-shell discovery

Resolved once per restore (call site: wherever `BuildPlan` is invoked):

1. `tmux show-option -gqv default-shell` → use this if non-empty.
2. Fall back to `$SHELL` env.
3. Fall back to `/bin/sh`.

If the resolved shell's basename is `bash`, prepend `-l` to the relaunch arg list (matches resurrect's login-shell behavior). Other shells get no extra flags.

## Edge cases

- **Missing scrollback file**: `cat-scrollback` exits 0 silently; `exec` proceeds; pane gets the relaunch or shell with no history above it.
- **Empty allow-list, no scrollback**: `StartupCommand == ""` → tmux uses default-command, identical to today.
- **Special characters in cwd / args**: cwd is passed via tmux's `-c` flag (tmux handles quoting). Args are quoted with `strconv.Quote`. The `<sha>` is hex — no quoting needed. `<self>` is shell-quoted with single quotes.
- **`os.Executable()` returns a symlink**: pass through unchanged (no `EvalSymlinks`) so the path matches what the user installed. If the symlink target moves between save and restore, the user has bigger problems.
- **`default-shell` is empty (no tmux server reachable from the binary's view)**: covered by the `$SHELL` fallback.

## Tests

- `internal/restore/plan_test.go`: replace `RelaunchCommand` / `RestoreScrollback` expectations with `StartupCommand` strings on the relevant `CreateWindow` / `SplitPane`. Add a case per row of the table in §"Plan composition".
- `internal/restore/apply_test.go`: assert the right `tmux` argv per action; drop paste-buffer assertions; add cases verifying the trailing `shell-command` arg is included only when non-empty.
- `integration_test.go` (top-level): same restore flow, just verify the spawned `tmux` invocations include the new startup string and no `send-keys` / `paste-buffer` calls happen.
- New `cmd/tmux-state/cat_scrollback_test.go`: existing sha → bytes on stdout; missing sha → empty stdout, exit 0.

## Risks

- **Default-shell discovery** can return empty in odd setups (tmux server with `default-shell ""`). The `$SHELL` → `/bin/sh` fallback chain handles this; tested explicitly.
- **Behavioral change** for existing users: scrollback now appears as visible terminal output and is *not* in shell history. This matches resurrect and is expected. Worth a one-line note in tmux-state's CHANGELOG.
- **`os.Executable()` portability**: works on Linux and macOS (lazytmux's supported targets). Not tested on Windows; tmux-state doesn't claim Windows support.

## Out of scope

- Leftover empty window-0 from `tmux new-session` (pre-existing).
- Lazytmux changes — none needed; flake.lock bump only after tmux-state ships.
- `restoreMode = "auto"` semantics on server start — unchanged shape, inherits the speed-up automatically.
