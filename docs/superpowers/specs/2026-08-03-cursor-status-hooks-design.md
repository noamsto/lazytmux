# Cursor status hooks (design)

Drive the lazytmux status line from Cursor Agent lifecycle hooks, the same way
`codexStatus` drives Codex — via `claude-status-update` keyed off `$TMUX_PANE`.

## Why

Cursor today is **screen-scrape only** (`agent-detect` + `cursor.toml`). That
works for braille `Working`/`Running`/`Waiting`, but has no event for turn
start/stop or subagent lifetime. Claude and Codex already write
`/tmp/claude-status/panes/<id>` from hooks; Cursor should share that path.

aeye is unrelated to status. It happens to merge into the same
`~/.cursor/hooks.json` for the image carousel. This feature only needs to
**not clobber** aeye entries when upserting its own.

## Non-goals

- Replacing `agent-detect` (kept as backfill, especially for `waiting` /
  permission UI that Cursor does not expose as a clean hook).
- IDE plugin-bundled hooks (CLI only reads user/project `hooks.json` —
  proven by aeye's 2026-07-28 spike).
- Issue/task/name self-report (Claude-only skills; out of scope).

## Option

```nix
programs.lazytmux.cursorStatus.enable = true;  # default false
# asserts agentIntegration.enable
```

Same opt-in posture as `codexStatus`: mutates an external tool's config.

## Distribution

| Piece | Location |
|-------|----------|
| Option + activation | `modules/home-manager.nix` |
| Thin wrapper binary | `scripts/cursor-status-hook.sh` → `writeShellScriptBin` (on PATH via `agentIntegration` or the `cursorStatus` package list) |
| Hook template | embedded in the activation (or a small JSON beside the module) |

Wrapper does one thing: `claude-status-update "$@" >/dev/null`. Stdout must
stay empty — Cursor injects hook stdout `additional_context` into the model
(aeye spike). Redirect keeps a future stray echo from poisoning context.

Command paths use `${config.home.profileDirectory}/bin/cursor-status-hook`
(rebuild-stable), never a raw `/nix/store/...` path.

## Hook → state map

| Cursor event | `claude-status-update` | Notes |
|--------------|------------------------|-------|
| `sessionStart` | `cleanup` then `idle` | Two command entries |
| `beforeSubmitPrompt` | `processing --force` | Analog of UserPromptSubmit |
| `preToolUse` | `processing` | |
| `postToolUse` | `processing` | |
| `postToolUseFailure` | `error` | Best-effort; may be sparse |
| `preCompact` | `compacting` | |
| `stop` | `done` | |
| `subagentStart` | `processing` | Keeps parent busy while Task/bg runs |
| `subagentStop` | _(no-op)_ | Parent may still be working; `stop` clears |

No dedicated Cursor "permission" event with Claude's `permission_prompt`
fidelity — leave `waiting` to `agent-detect` (existing `Proceed (y)` /
reject-feedback rules).

## Merge into `~/.cursor/hooks.json`

Activation runs **every** home-manager switch (like aeye's install.sh, unlike
Codex's append-once):

1. Ensure file exists (`{"version":1,"hooks":{}}` if missing).
2. Strip any prior lazytmux status entries: command contains
   `/bin/cursor-status-hook` (or a dedicated marker path).
3. Append the template entries under each event key.
4. Leave all other entries (aeye, user) untouched.
5. Malformed JSON → fail the activation loudly (do not clobber).

Requires `jq` on the activation PATH (pin like the aeye cursor activation in
nix-config, or use `pkgs.jq` in the HM script).

## Precedence with agent-detect

Unchanged merge in `read_pane_state`:

- Fresh hook (`panes/<id>`) wins.
- Stale/absent hook → screen (`screen/<id>`) backfill.

So during a long Shell wait, hooks leave `processing` from `preToolUse`; even
if the screen label is parsed wrong, the spinner stays correct. When hooks are
off, behaviour is today's agent-detect-only path.

## nix-config

```nix
programs.lazytmux = {
  agentIntegration.enable = true;
  cursorStatus.enable = true;
};
```

Alongside the already-planned `codexStatus.enable` + `persist.resumeCodex`.

## Testing

- Unit: jq merge pure function (strip + append) over fixture hooks.json
  containing aeye-shaped entries — assert aeye kept, status upserted, second
  run idempotent.
- Smoke: enable locally, `cursor-agent` in tmux, confirm
  `/tmp/claude-status/panes/<id>` flips processing→done across a turn, and
  window icon follows without relying on the braille label.
- Regression: aeye carousel hooks still present after switch.

## Open follow-ups (out of v1)

- Richer `waiting` if Cursor adds a permission hook event.
- `sessionEnd` → `clear` once payload/reliability is confirmed.
