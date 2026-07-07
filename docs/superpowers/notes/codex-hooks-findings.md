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

## Hook trust pre-seeding

Follow-up spike: the chosen design has the `SessionStart` hook stamp
`@ts_relaunch` directly, so it must run with **no interactive approval and no
`--dangerously-bypass-hook-trust`**. Question: can a Nix/home-manager config
pre-seed trust declaratively? All findings below verified against
codex-cli 0.142.3 with a throwaway `CODEX_HOME` (confirmed respected via the
`CODEX_HOME` env var) plus binary `strings` and the official docs.

### Where/how trust is stored

Trust for a non-managed hook is persisted back into the **user
`config.toml`** (`$CODEX_HOME/config.toml`) — verified by trusting a hook once
in the TUI ("Hooks need review" → "Trust all and continue") and diffing the
file. It appends a `[hooks.state]` table:

```toml
[hooks.state]

[hooks.state."/home/noams/.cache/codex-hook-spike/config.toml:session_start:0:0"]
trusted_hash = "sha256:3ec01a97faa48ed093329f104df43609013904e851c9820db17eb30a600d2956"
```

- The state **key** is `"<absolute config.toml path>:<event_snake>:<matcher_group_idx>:<hook_idx>"`
  (here `session_start:0:0`). Path is absolute, event is snake_case.
- `trusted_hash` is `sha256:` + hex of a **content hash over the normalized
  hook definition**, versioned (the TUI labels a stale entry "rust-v1 hook is
  new or changed"). It covers the hook body: **verified** by editing
  `timeout = 30` → `60` while leaving the `trusted_hash` untouched — the hook
  was then treated as untrusted and **skipped** in headless `codex exec`.

### Can trust be pre-seeded declaratively?

**Yes, two independent routes — with very different robustness.**

Route A — hand-write `trusted_hash` into `~/.codex/config.toml`
(**works, but NOT recommended**). A config.toml carrying both the
`[[hooks.SessionStart]]` block and a matching `[hooks.state."…:session_start:0:0"]`
`trusted_hash` runs the hook headlessly with no flag — **verified**: after the
one-time TUI trust wrote the entry, `codex exec "…"` (no
`--dangerously-bypass-hook-trust`) fired the hook and produced the payload.
The blocker is computing the hash: it is an **undocumented, content-based,
versioned** digest. I could not reproduce `sha256:3ec01a97…` from any
plausible serialization (command string, script file bytes, TOML/JSON of the
handler with/without event+matcher, `rust-v1` prefix variants — all tried,
none matched). DeepWiki and the official docs both explicitly leave the
algorithm undisclosed. So a Nix generator cannot reliably compute it, and any
codex upgrade that bumps the "rust-v1" scheme (or any edit to the hook block)
silently re-triggers review. **Do not pursue precomputing the hash.**

Route B — **managed hooks (recommended).** A hook delivered through a
**managed config layer** is marked managed, "trusted by policy, always on,"
and **cannot be disabled or prompted** — no `trusted_hash`, no review, no
bypass flag. This is the clean declarative answer. Managed layers (from
binary strings + docs) are **fixed system paths, NOT relocatable via
`CODEX_HOME`**:

- `/etc/codex/config.toml` (admin/system)
- `/etc/codex/managed_config.toml`
- `/etc/codex/requirements.toml` (the docs' canonical managed-hooks file;
  supports `allow_managed_hooks_only = true` + `[features] hooks = true`)
- macOS MDM plist; cloud-managed config.

Relevant keys: `hooks.managed_dir` / `hooks.windows_managed_dir` mark a
**directory of trusted scripts** (codex does not install them — your tooling
does); inline `[[hooks.<Event>]]` blocks placed in a managed layer are
themselves managed. `allow_managed_hooks_only = true` additionally suppresses
all user/project/session/plugin hooks while keeping managed ones.

Caveat: the managed-layer behavior is confirmed from the official docs +
binary strings (`allow_managed_hooks_only`, `ManagedHooksRequirementsToml`,
the "Managed hooks are always on" TUI string, "trusted by policy" doc text),
**not** locally executed, because it requires writing under `/etc` and this
spike must not touch the real system. Route A was fully executed end-to-end.

There is **no headless trust CLI** — no `codex trust` / `codex hooks`
subcommand exists (checked `codex --help` and subcommands); trust is only via
the interactive TUI review or a managed layer. `features.hooks` is stable and
on by default, so no feature flag is needed.

### Recommendation for the Nix module

Primary (robust, survives codex upgrades): a **NixOS system module** writes
the hook definition into a managed layer, e.g.

```nix
environment.etc."codex/managed_config.toml".text = ''
  [[hooks.SessionStart]]
  matcher = "startup|resume"

  [[hooks.SessionStart.hooks]]
  type = "command"
  command = "${pkgs.lazytmux}/bin/lazytmux-codex-relaunch-stamp"
  timeout = 30
'';
```

Home-manager still owns the writer script itself (ship it to a stable store
path and point the managed hook's `command` at it). Because the hook lives in
a managed layer it runs with no approval and no bypass flag. This is
system-scoped (needs `/etc`), which is the price of "no interactive approval."

Pure home-manager fallback (user-scoped, no root): HM writes the
`[[hooks.SessionStart]]` block into `~/.codex/config.toml` and the user does a
**one-time** `/hooks` → "Trust all" (or accepts the first-run review) per
machine. Fully declarative pre-seeding is **not** achievable this way because
`trusted_hash` is an unreproducible versioned content hash — do not attempt to
generate it.

Rejected per requirements: launching `codex` via a wrapper with
`--dangerously-bypass-hook-trust` (the only other zero-approval path) —
excluded by the follow-up brief.

Manual step to confirm Route B on this machine before committing the module
to the managed path (spike could not write `/etc`): as root, drop the
`[[hooks.SessionStart]]` block into `/etc/codex/managed_config.toml`, run
`codex exec "hi"` as the user with **no** `--dangerously-bypass-hook-trust`,
and confirm the hook fires (and that `/hooks` shows it as a non-toggleable
"Admin/Managed" source). If `managed_config.toml` doesn't mark it managed, try
`/etc/codex/requirements.toml` with `allow_managed_hooks_only = true` +
`[features] hooks = true`.
