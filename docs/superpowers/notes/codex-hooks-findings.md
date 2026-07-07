# Codex hook mechanism findings (spike for #140)

Environment: codex-cli 0.142.3 (nix store build), tested interactively in a
real tmux pane and via `codex exec`.

## Step 1: hook configuration surface

```
$ codex --help 2>&1 | grep -iE 'hook'
      --dangerously-bypass-hook-trust
          Run enabled hooks without requiring persisted hook trust for this invocation. DANGEROUS.
          Intended only for automation that already vets hook sources

$ codex doctor 2>&1 | grep -iE 'hook|notify'
(no matches)

$ grep -rniE 'hook|notify' ~/.codex/config.toml
(no hook/notify config yet — key not present in this machine's real config)

$ find ~/.codex -maxdepth 2 -iname '*hook*'
(nothing)

$ codex plugin --help
Manage Codex plugins (add/list/marketplace/remove) — plugins are a separate
surface from hooks, not required for this.
```

`codex` ships a full hooks system (not documented in `--help`, confirmed via
`strings` on the binary and the official docs at
https://developers.openai.com/codex/hooks). Config lives under a top-level
`[hooks]` table in `config.toml` (or any layered profile
`$CODEX_HOME/<name>.config.toml`, or a `-c hooks.X=...` CLI override — all
three were tested and all work identically for `codex exec`).

Supported hook events (from binary strings): `PreToolUse`, `PermissionRequest`,
`PostToolUse`, `PreCompact`, `PostCompact`, `SessionStart`, `SubagentStart`,
`SubagentStop`.

Config shape (array-of-tables, mirrors Claude Code's own hooks schema):

```toml
[[hooks.SessionStart]]
matcher = "startup|resume"

[[hooks.SessionStart.hooks]]
type = "command"
command = "/path/to/handler"
timeout = 30
```

Non-managed command hooks require interactive trust approval (hash-pinned)
before they run; `--dangerously-bypass-hook-trust` skips that for a single
invocation. This is required for any headless/automated caller (i.e. required
for whatever spawns the pane in Task 2, unless we pre-seed hook trust another
way).

The classic `notify = [...]` config key still exists separately from `hooks`
(confirmed present in the `ConfigToml` struct's field list) but per the
official docs it **only fires on `agent-turn-complete`** — no session-start
signal, and it predates the hooks system. Not useful for this feature;
`SessionStart` hook is the right mechanism.

## Step 2: SessionStart payload and TMUX_PANE

Verified empirically with a throwaway logger hook
(`cat > payload.json; env > env.txt`) configured via `-c` override and via a
layered profile config (`$CODEX_HOME/hooktest.config.toml` + `-p hooktest`),
run with `--dangerously-bypass-hook-trust`.

### `codex exec` (non-interactive) — fires synchronously at session start

```json
{
  "session_id": "019f3b53-487c-7973-8103-8e2828a5fd72",
  "transcript_path": "/home/noams/.codex/sessions/2026/07/07/rollout-2026-07-07T09-45-41-019f3b53-487c-7973-8103-8e2828a5fd72.jsonl",
  "cwd": "/home/noams/Data/git/noamsto/lazytmux-worktrees/feat-140-codex-resume",
  "hook_event_name": "SessionStart",
  "model": "gpt-5.5",
  "permission_mode": "default",
  "source": "startup"
}
```

- `session_id` is the resumable UUID — verified by cross-checking the
  rollout file's first line (`type: "session_meta"`), where
  `payload.session_id == payload.id == "019f3b53-487c-7973-8103-8e2828a5fd72"`
  for a top-level (non-subagent) session, and it matches the UUID embedded in
  the rollout filename (`rollout-<ts>-<uuid>.jsonl`). The task brief's warning
  that `session_id` and filename `id` can differ applies to **subagent**
  sessions only; for the top-level pane session that tmux-state cares about,
  they are identical.
- Re-ran with `codex exec resume <that-uuid>` — hook fired again with
  `"source":"resume"`, same `session_id`, confirming `codex resume <uuid>`
  round-trips correctly with the id captured from the hook.
- `TMUX_PANE=%9` was present in the hook's `env` dump, matching the pane the
  `codex exec` command was actually run in. The hook process is a plain
  child process and inherits the full environment, including `TMUX_PANE`,
  `TMUX`, and `TERM_PROGRAM=tmux`.

### Interactive TUI (`codex`, no subcommand) — fires late, not at launch

This is the mode that actually matters for #140 (a user runs bare `codex` in
a tmux pane). Behavior differs from `codex exec`:

- The hook does **not** fire immediately when the TUI renders/session is
  created (waited 10+s after launch with `-c`-configured hook and again with
  a layered profile config — no payload file appeared either time).
- Sending a first prompt and waiting for the turn to complete **does** cause
  the hook to fire — payload appeared with `"source":"startup"` and the
  correct resumable `session_id`, after the model's reply had already
  rendered in the TUI.
- `TMUX_PANE` was present in this delayed firing too (`%20`, matching the
  actual pane), so the env-inheritance behavior is the same as `codex exec`
  — the firing is just deferred, not env-restricted.

Practical implication for Task 2: **the `SessionStart` hook in interactive
Codex sessions only fires after the first user turn completes, not at pane
creation.** A pane running `codex` that is killed before the user ever sends
a prompt will not have stamped `@ts_relaunch` — this is an acceptable gap
(nothing to resume anyway, since no rollout content exists yet either), but
the writer must not assume the stamp is available immediately on pane
spawn; it should treat the hook firing as this event driven callback whenever
it happens, not on the pane's spawn callback.

This "late" behavior is consistent with a real upstream bug report,
https://github.com/openai/codex/issues/17532, which observed
`SessionStart`/`Stop` hooks configured via repo-local `.codex/config.toml`
failing to fire in interactive sessions at all; our config was global
(`-c` and a layered profile, not repo-local) and did eventually fire, but
only post-turn — so the "hooks are unreliable/delayed in the interactive
TUI" theme holds in both reports even though the exact trigger differs.

## Step 3: pane-resolution path

`TMUX_PANE` is present directly in both the `codex exec` and interactive-TUI
hook environments, and it correctly identifies the pane the `codex` process
is actually running in (verified: `%9` and `%20` respectively, matching the
tmux windows used for each test). **No pid-ancestry matching against
`tmux list-panes` is needed** — the hook command should just read
`$TMUX_PANE` directly from its own environment and use it to key the
`@ts_relaunch` attribute (`tmux set-option -p -t "$TMUX_PANE" @ts_relaunch
"codex resume $session_id"`).

The `notify` program fallback is not needed as a result, but if it were, the
same reasoning applies: it too runs as a child of the codex process and would
inherit `TMUX_PANE`.

## Recommendation for Task 2 (writer)

- Configure a global `[[hooks.SessionStart]]` entry (matcher
  `"startup|resume"`) pointing at the writer script, in the user's
  `~/.codex/config.toml` (or equivalent layered config the install process
  manages) — not a repo-local `.codex/config.toml`, since that path is the
  one with an open reliability bug (#17532) upstream.
  - **`hooks.SubagentStart` also exists as a separate event** — do not use it
    for pane-level resume stamping; it is scoped to per-thread subagent
    sessions and only `SessionStart`'s top-level `session_id` is guaranteed
    to equal the resumable session id we tested here.
- Read `session_id` directly from the hook's JSON stdin payload — no need to
  parse the rollout filename or its `session_meta` line.
- Read `$TMUX_PANE` directly from the hook process's environment — no
  pid-ancestry lookup required.
- Write the attribute with
  `tmux set-option -p -t "$TMUX_PANE" @ts_relaunch "codex resume $session_id"`.
- Hook installation requires the pane owner to have already trusted the hook
  once (or the installer must ship it in a way that's pre-trusted / use
  `--dangerously-bypass-hook-trust` in the wrapper that launches `codex`,
  if lazytmux controls that launch path).
- Because the interactive-TUI hook fires only after the first turn
  completes, the `@ts_relaunch` stamp will lag pane creation by however long
  the user's first turn takes — acceptable for this feature, but worth a
  one-line comment in the writer so a future reader doesn't assume
  immediacy.
