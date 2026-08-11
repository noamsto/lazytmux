# Cursor session resume via @remux_relaunch (#249)

## Problem

Cursor panes do not resume across tmux-remux restore. Claude (polled) and Codex
(hook-stamped) both stamp `@remux_relaunch` today; Cursor has no equivalent.

## Verified facts (live on this machine, cursor-agent 2026.08.04-aaa8809)

- `cursor-agent` supports `--resume [chatId]`.
- A Cursor `sessionStart` hook fires once per **new** conversation only (never
  on `--resume`, confirmed by capturing the hook payload across both an
  initial run and a `--resume` run of the same chat — only one payload was
  ever written).
- The `sessionStart` hook's JSON stdin payload carries `conversation_id` (and
  an identical `session_id` in the same payload) — captured via a
  project-local `.cursor/hooks.json` debug hook in an isolated scratch
  directory (`/tmp/.../cursorprobe3`), never touching the real
  `~/.cursor/hooks.json`. Sample payload:
  ```json
  {
    "conversation_id": "dab2cc8a-7cd5-44c7-be8d-35c6928d3267",
    "generation_id": "dab2cc8a-7cd5-44c7-be8d-35c6928d3267",
    "model": "cursor-grok-4.5-low-fast",
    "is_background_agent": false,
    "session_id": "dab2cc8a-7cd5-44c7-be8d-35c6928d3267",
    "hook_event_name": "sessionStart",
    "cursor_version": "2026.08.04-aaa8809",
    "workspace_roots": ["/tmp/.../cursorprobe3"],
    "user_email": "noam@factify.com",
    "transcript_path": null
  }
  ```
- The round trip is proven: `cursor-agent -p --resume dab2cc8a-... "what word
  did I ask you to reply with earlier in this exact chat?"` answered correctly
  ("pong"), in the same scratch project. `transcript_path` is null at
  sessionStart (matches the aeye adapter's own comment), so that field is not
  usable — `conversation_id`/`session_id` are.
- The chat id also corresponds to a real directory:
  `~/.config/cursor/chats/<md5(workspace_root)>/<conversation_id>/meta.json`
  — confirmed independently, though not needed by the implementation (the
  hook payload alone is sufficient).
- This mirrors the existing `codex-relaunch-stamp.sh` shape almost exactly:
  a hook, id in the JSON payload, no jq dependency.

### The one-shot problem (found by plan-critic revision 1, then verified)

Because `sessionStart` never fires on `--resume`, a naive sessionStart-only
implementation resumes correctly exactly **once**: tmux-remux's own restore
path (`internal/restore/apply.go`, verified in a scratch clone of
`noamsto/tmux-remux`) execs the stored `@remux_relaunch` command verbatim as
the new pane's startup command, but does **not** re-set the `@remux_relaunch`
pane option on that new pane. So a pane restored via `cursor-agent --resume
<id>` has no `@remux_relaunch` of its own; the *next* save/restore cycle sees
an unset option and falls back to a bare shell. (This is exactly why Codex's
hook uses `matcher = "startup|resume"` — it deliberately re-fires on resume,
which Cursor's `sessionStart` does not.)

Also confirmed, from `noamsto/tmux-remux`'s own
`cmd/tmux-remux/relaunch_stamp.go`: that repo already has generic
`relaunch-stamp`/`install-hook` machinery with `claude`/`codex` presets, and a
`cursor` preset explicitly deferred with the comment "cursor is added later,
gated on empirical verification" — the exact verification this plan just
performed. Migrating lazytmux's Cursor (and eventually Claude/Codex) stamps
onto tmux-remux's native mechanism is a real, valuable follow-up, but it is a
**separate repo** or a nix-config wiring change either way, not authorized by
this ticket (scoped to lazytmux only) — worth one sentence in the PR body as a
pointer, not implemented here.

**Fix chosen: also wire the stamp on `beforeSubmitPrompt`.** Verified live via
an interactive `cursor-agent --trust` session driven in an isolated tmux
socket (two real turns): `beforeSubmitPrompt` fires on **every** user turn —
including turns sent after `--resume` — and its JSON payload carries the same
`conversation_id`/`session_id` as `sessionStart`. Sample payload from turn 2 of
a resumed-in-place session:
```json
{"conversation_id":"8dcd92d8-...","generation_id":"49e50437-...","model":"cursor-grok-4.5-low-fast","prompt":"...","attachments":[],"session_id":"8dcd92d8-...","hook_event_name":"beforeSubmitPrompt","cursor_version":"2026.08.04-aaa8809","workspace_roots":["..."],"user_email":"...","transcript_path":"..."}
```
So: `cursor-relaunch-stamp.sh` is agent-event-agnostic (it reads whatever JSON
it's handed) and gets wired on **both** `sessionStart` and
`beforeSubmitPrompt`. Every subsequent user turn in a resumed pane re-stamps
`@remux_relaunch` with the (unchanged) chat id, so the option survives to the
next save as long as the user sends at least one message per restore cycle —
matching how Claude's continuous polling and Codex's `startup|resume` matcher
both achieve the same property by different means. **Accepted residual gap**
(document in the option doc + PR body, do not silently paper over): a pane
that is restored and receives **zero** further turns before the *next* save
reverts to a bare shell on the restore after that. No `-p`/print-mode
invocation exhibits ANY of this — hooks fire once at most in `-p` mode even
across `--resume`, confirmed by testing; the interactive TUI is what actually
matters here and is what was driven for this verification.

## Escalated (2-revision cap reached — surfaced per WORKER_PROTOCOL rule 3)

The plan-critic loop's final allowed revision (revision 2) surfaced one more
blocking, repo-verified finding, independently re-verified below by reading
the actual scripts (not taken on faith). Per protocol this is being
incorporated directly rather than spent on a 3rd critic round, and is
surfaced here + in the PR body under `## Escalated` for a human reviewer to
re-check.

**Finding: `scripts/tmux-update-icons.sh`'s Claude-resume stamper clears ANY
pane's `@remux_relaunch` that lacks a Claude transcript — including a Cursor
or Codex pane's own stamp — within one second of it being set.**

Verified chain (all read directly, not inferred):
- `claude_pane_ids()` (`scripts/lib-claude.sh:333-340`) unions `panes/*` and
  `screen/*` state files by design ("screen-only panes (non-Claude agents
  with no hook, e.g. Codex) surface too") — a Cursor pane running
  `cursor-agent` always has a `screen/<id>` file via `agent-detect`
  (`picker/agentdetect/manifest/manifests/cursor.toml:2`,
  `match_commands = ["cursor-agent"]`), piped unconditionally by
  `arm_agent_detect` regardless of `cursorStatus`/`codexStatus`.
- `read_pane_state` (`scripts/lib-claude.sh`, the `elif [[ -n $screen_state
  ]]` branch) never sets `transcript` for a screen-only pane, so
  `REPLY_TRANSCRIPT` is always empty for Cursor (and Codex, when it's
  screen-only too).
- `scripts/tmux-update-icons.sh:186-194`'s `RESUME_CLAUDE == on` block runs
  for **every** id `claude_pane_ids()` yields, computes `desired=""` (empty
  uuid), and calls `tmux set -pq … @remux_relaunch ""` whenever that differs
  from the pane's current value — with no check that the pane is actually a
  Claude pane. `RESUME_CLAUDE` is `#{@resume_claude}`, on by default
  (`resumeClaude` defaults `true`), so this tick runs on the exact
  configuration where `resumeCursor` (or today, `resumeCodex`) would also be
  on.
- Net effect: `cursor-relaunch-stamp.sh` (or `codex-relaunch-stamp.sh` today)
  stamps `@remux_relaunch`, and the very next 1-second tick wipes it back to
  `""`. The acceptance criterion ("relaunch with `cursor-agent --resume
  <chatId>` instead of a bare shell") fails outright without this fix — it is
  not optional polish. This also means `resumeCodex` is very likely
  non-functional today; worth one sentence in the PR body, but fixing Codex's
  own coverage gap is not otherwise in scope here (the guard fixes both for
  free).

**Fix: narrow the clearing/stamping guard to values this poller owns.**
Change `scripts/tmux-update-icons.sh`'s block (currently around lines
186-194) from:
```bash
if [[ $RESUME_CLAUDE == on ]]; then
	uuid="${REPLY_TRANSCRIPT##*/}"
	uuid="${uuid%.jsonl}"
	desired=""
	[[ -n $uuid ]] && desired="claude --resume $uuid"
	if [[ $desired != "${pane_cur_relaunch[$pane_file]:-}" ]]; then
		tmux set -pq -t "%$pane_file" @remux_relaunch "$desired"
	fi
fi
```
to:
```bash
if [[ $RESUME_CLAUDE == on ]]; then
	uuid="${REPLY_TRANSCRIPT##*/}"
	uuid="${uuid%.jsonl}"
	desired=""
	[[ -n $uuid ]] && desired="claude --resume $uuid"
	cur="${pane_cur_relaunch[$pane_file]:-}"
	# Only touch a value this poller owns (empty, or previously stamped by
	# this same "claude --resume *" branch) — never clobber a Codex/Cursor
	# hook's own stamp on a pane that also has a screen-only state file
	# (agent-detect fires for every known agent, unconditionally).
	if [[ -z $cur || $cur == "claude --resume "* ]] && [[ $desired != "$cur" ]]; then
		tmux set -pq -t "%$pane_file" @remux_relaunch "$desired"
	fi
fi
```
This is a pure narrowing: every existing Claude-pane behavior is unchanged
(a real Claude pane's current value is always either empty or `claude
--resume <uuid>`, so the new condition is always true for it); the only new
effect is refusing to touch a value that doesn't match that shape.

**Test:** add `tests/update-icons-resume-guard.bats`, styled like
`tests/update-icons-enrich-trigger.bats` (real private tmux server via
`tmux -f /dev/null new-session`, not a fake `tmux` binary, since this reads
back `pane_cur_relaunch` through a real batched `list-panes`). Cover:
- a pane with a `screen/<id>` state file (no `panes/<id>` hook file) and a
  pre-stamped `@remux_relaunch "cursor-agent --resume abc123"` — after
  `run_update_icons` with `RESUME_CLAUDE=on` (positional `$2`), the option is
  **unchanged**;
- (regression guard) a pane with a real Claude `panes/<id>` file carrying a
  `transcript=` path — the option is still stamped/updated exactly as before.

This step lands independently of Steps 1-7 below (it touches
`tmux-update-icons.sh`, not any new Cursor file) but is required before this
feature can work at all — do it first or alongside Step 4.

## Plan

- [ ] **Step 1: `scripts/cursor-relaunch-stamp.sh`** — new sessionStart hook
  binary, styled like `scripts/codex-relaunch-stamp.sh` (fork-free slurp +
  regex via `IFS= read -r -d ''`, no jq — this already handles a
  multiline/pretty-printed stdin payload since it's a NUL-delimited slurp, not
  a line read). No-ops outside tmux / without the tmux binary / without a
  pane. Extract the chat id as two **independent** regex attempts with an
  explicit emptiness guard between them (precedence must be pinned here, not
  left to the implementer): match
  `"conversation_id"[[:space:]]*:[[:space:]]*"([^"]*)"` first — `[[:space:]]`,
  **not** `\s` (a glibc `regcomp` extension; `codex-relaunch-stamp.sh:19` uses
  `[[:space:]]` deliberately, and the CI matrix includes `aarch64-darwin`) —
  only use that capture if **non-empty**; otherwise match
  `"session_id"[[:space:]]*:[[:space:]]*"([^"]*)"` and use that if non-empty;
  otherwise stamp nothing. `conversation_id` is always the first key in every
  real captured payload and bash `=~` takes the leftmost match, so a
  `beforeSubmitPrompt` payload's `prompt` field can't shadow it — and a
  prompt string containing literal-looking `"session_id":"..."` text is
  JSON-escaped (`\"session_id\":\"...\"`) in the real payload, which this
  regex cannot match; still, add one adversarial fixture in Step 5 exercising
  exactly that (a `beforeSubmitPrompt` payload whose `prompt` value embeds
  `\"conversation_id\":\"evil\"`), since nothing else in the suite exercises
  the event Step 2 newly wires. If a non-empty id was found:
  `tmux set-option -p -t "$TMUX_PANE" @remux_relaunch "cursor-agent --resume
  $chat_id"`. If neither field yields a non-empty id, stamp nothing (never a
  half-built command). Unlike `codex-relaunch-stamp.sh`, Cursor hooks carry a
  stdout contract (`scripts/cursor-status-hook.sh:3`: "Stdout must stay empty:
  Cursor injects hook stdout `additional_context` into the model") and a pane
  can legitimately vanish between hook-fire and the `tmux set-option` call —
  redirect the `tmux set-option` call's output and swallow its exit status
  (`>/dev/null 2>&1 || true`, mirroring `cursor-status-hook.sh:12`) so a dying
  pane never surfaces a spurious hook failure or leaks output into the model's
  context.

- [ ] **Step 2: `scripts/cursor-relaunch-hooks-install.sh`** — new idempotent
  JSON upsert script, styled like `scripts/cursor-hooks-install.sh` (same
  `CURSOR_HOOKS_FILE`/`CURSOR_HOME` env override, same jq-empty malformed-JSON
  guard, same mktemp/mv pattern). Appends the **same** `{command: <stamp-path>,
  timeout: 15}` entry to **both** `sessionStart` AND `beforeSubmitPrompt`
  (see "The one-shot problem" above — `sessionStart` alone only resumes once),
  stripping prior entries whose command contains the marker
  `/bin/cursor-relaunch-stamp` from **each** of those two arrays independently;
  leaves every other entry in both arrays (aeye's
  `session-reset.sh`/`session-backfill.sh`/`diagram-guidance.sh`,
  `cursor-status-hook cleanup`/`idle`/`processing --force`, user entries)
  untouched. Refuses to clobber malformed JSON. A new script — do **not**
  touch `cursor-hooks-install.sh` itself, which is a separate, already-tested
  feature (`cursorStatus`) out of scope here. Needs the same
  `# shellcheck disable=SC2016` directive immediately above the single-quoted
  jq program that `cursor-hooks-install.sh:29` carries, or `nix build .#lint`
  fails on the new file. Two independent installers read-modify-write the
  same `~/.cursor/hooks.json` (this one and `cursor-hooks-install.sh`) — this
  is safe because home-manager activation runs them sequentially
  (`entryAfter ["writeBoundary"]`) and each script's `strip` only filters
  entries matching its *own* marker, so neither can eat the other's entries.
  Don't "helpfully" unify them into one script.

- [ ] **Step 3: register both scripts** — add `"cursor-relaunch-stamp"` and
  `"cursor-relaunch-hooks-install"` to `config/tmux.conf.nix`'s `scriptNames`
  list (next to the existing `"codex-relaunch-stamp"` / `"cursor-status-hook"`
  / `"cursor-hooks-install"` entries), so `tmuxConfig.script.*` exposes them
  via `writeShellScriptBin`.

- [ ] **Step 4: wire `modules/home-manager.nix`**
  - Add `persist.resumeCursor` option (bool, default **false**) right after
    `resumeCodex` (lands between `resumeCodex`, which ends at line 423, and
    `package` at line 425), matching `resumeCodex`'s doc tone/length: explain
    the mechanism (a `sessionStart` + `beforeSubmitPrompt` hook upsert into
    `~/.cursor/hooks.json`, marker-guarded like `cursorStatus`'s hooks; stamps
    `@remux_relaunch` to `cursor-agent --resume <chatId>` from the payload's
    `conversation_id`/`session_id`); note that `sessionStart` alone only fires
    on a brand-new conversation, so the hook is **also** wired on
    `beforeSubmitPrompt` to re-stamp on every turn — including turns sent after
    a restore — so resume survives repeated restore cycles as long as at least
    one message is sent per cycle; a restored pane that receives **zero**
    further turns before the next save reverts to a bare shell on the restore
    after that (an accepted, documented gap — unlike `resumeClaude`'s
    continuous poll or `resumeCodex`'s `startup|resume` matcher, Cursor's CLI
    gives no re-fire-on-resume hook to hang this off). Default is off — like
    `resumeCodex` — because this is a brand-new capture path across an
    externally-versioned CLI, and an unrecognized payload shape stamps nothing
    rather than a broken command.
  - Add `resumeCursorEnable = cfg.persist.enable && cfg.persist.package !=
    null && cfg.persist.resumeCursor;` next to `resumeCodexEnable`, same
    one-line comment style.
  - Add `++ lib.optionals resumeCursorEnable [tmuxConfig.script.cursor-relaunch-stamp
    tmuxConfig.script.cursor-relaunch-hooks-install]` to `home.packages`.
    Unlike `resumeCodex` (raw nix store path — codex invokes it by absolute
    path from `config.toml`), the `cursorStatus`/`cursor-hooks-install`
    precedent embeds `${config.home.profileDirectory}/bin/...` paths into
    `hooks.json` for rebuild stability, so `cursor-relaunch-stamp` needs to
    actually be on the profile PATH; `cursor-hooks-install` already ships the
    installer binary itself in `home.packages` too.
  - Add activation block `provisionCursorResumeHook = lib.mkIf
    resumeCursorEnable (...)`, mirroring `provisionCursorStatusHooks` exactly
    in style (`run env PATH="${lib.makeBinPath [pkgs.jq pkgs.coreutils]}:$PATH"
    ${tmuxConfig.script.cursor-relaunch-hooks-install}/bin/cursor-relaunch-hooks-install
    ${config.home.profileDirectory}/bin/cursor-relaunch-stamp`). No
    `agentIntegration` assertion needed (unlike `cursorStatus`/`codexStatus`)
    — `cursor-relaunch-stamp` calls `tmux` directly, no
    `claude-status-update` dependency.

- [ ] **Step 5: tests**
  - `tests/cursor-relaunch-stamp.bats` — mirror
    `tests/codex-relaunch-stamp.bats`'s structure/fixture-tmux-log style, with
    Cursor's real captured payload shape (including a pretty-printed
    multiline fixture, matching the real hook stdin shape shown above — the
    NUL-delimited slurp must handle it). Cover:
    - stamps from `conversation_id` when it's non-empty — use a fixture with
      **distinct** `conversation_id`/`session_id` values (e.g.
      `"conversation_id":"aaaaaaaa-..."`, `"session_id":"bbbbbbbb-..."`) and
      assert the stamped command contains `--resume aaaaaaaa-...`, NOT
      `bbbbbbbb-...` (a fixture reusing the same id for both, like the sample
      payload above, cannot distinguish "reads conversation_id" from "reads
      session_id" and must not be the only case);
    - falls back to `session_id` when `conversation_id` is **absent**;
    - falls back to `session_id` when `conversation_id` is present but the
      **empty string** (`"conversation_id":"","session_id":"bbb..."`) —
      asserts the stamp uses `bbb...`;
    - no-op when `TMUX_PANE` unset;
    - no-op when tmux absent;
    - no-op when both id fields are missing or both empty.
  - `tests/cursor-relaunch-hooks-install.bats` — mirror
    `tests/cursor-status-hooks.bats`'s structure: creates `hooks.json` from
    empty and stamps the entry under **both** `sessionStart` AND
    `beforeSubmitPrompt`; preserves aeye-shaped AND
    `cursor-status-hook`-shaped entries already present in **both** arrays
    (including `beforeSubmitPrompt`'s existing `cursor-status-hook processing
    --force` entry), and re-running is idempotent (still exactly one marked
    entry per array); refuses to clobber malformed JSON; a final case mirroring
    `tests/cursor-status-hooks.bats`'s last case (`sed -n` /grep over
    `modules/home-manager.nix`) asserting: `persist.resumeCursor` defaults to
    `false`, `resumeCursorEnable` exists, `provisionCursorResumeHook` exists,
    `cursor-relaunch-hooks-install` is referenced, and
    `profileDirectory}/bin/cursor-relaunch-stamp` is referenced — this is the
    only guard on the `home.packages` ↔ profile-path pairing that makes the
    feature actually work, since nothing else in `nix flake check` evaluates
    the module. `persist.resumeCursor` is written as `resumeCursor =
    lib.mkOption {` nested inside the `persist = { ... }` block — the literal
    string `persist.resumeCursor` won't appear in the file (and
    `modules/home-manager.nix:701` already contains the literal
    `persist.resumeCodex` in a comment, so a sloppy substring grep for that
    style of string can silently false-pass). Pin a `sed -n` range the way
    `tests/cursor-status-hooks.bats:89` does for `cursorStatus` — e.g.
    `sed -n '/resumeCursor = lib.mkOption/,/^      };/p'` — then assert
    `default = false;` within that slice.

- [ ] **Step 6: register both new suites in `flake.nix`'s `checks`
  attrset** — `nix flake check` only runs suites hand-enumerated there (no
  directory-walking discovery exists anywhere in this repo); skipping this
  step means Step 5's tests sit inert and the gate can't fail on this
  feature at all. Mirror the existing pairs verbatim:
  - `cursor-relaunch-stamp-tests`, styled exactly like
    `codex-relaunch-stamp-tests` (flake.nix:218-226):
    ```nix
    cursor-relaunch-stamp-tests =
      pkgs.runCommand "cursor-relaunch-stamp-tests" {
        nativeBuildInputs = [pkgs.bats pkgs.coreutils];
      } ''
        cp -r ${./scripts} scripts
        cp -r ${./tests} tests
        bats tests/cursor-relaunch-stamp.bats
        touch $out
      '';
    ```
  - `cursor-relaunch-hooks-install-tests`, styled exactly like
    `cursor-status-hooks-tests` (flake.nix:260-270) — needs `pkgs.jq` (the
    installer hard-exits 1 without it) and the `modules/home-manager.nix`
    copy (for the Step 5 module-wiring case):
    ```nix
    cursor-relaunch-hooks-install-tests =
      pkgs.runCommand "cursor-relaunch-hooks-install-tests" {
        nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.jq];
      } ''
        cp -r ${./tests} tests
        cp -r ${./scripts} scripts
        mkdir modules
        cp ${./modules/home-manager.nix} modules/home-manager.nix
        bats tests/cursor-relaunch-hooks-install.bats
        touch $out
      '';
    ```

- [ ] **Step 7: gate** — `nix build .#default`, `nix flake check` (confirm
  the two new check derivation names above actually build and run), `nix
  build .#lint` (all three; none subsumes another). Commit from inside `nix
  develop` so pre-commit hooks run. Stage the plan doc
  (`docs/superpowers/plans/2026-08-11-cursor-relaunch-resume.md`) in the same
  commit/PR per `CLAUDE.md`'s "Plans and Specs" rule — but do **not** sweep
  `WORKER_TASK.md` (untracked worker scratch) into a broad `git add -A`.

## Out of scope

- `picker/agentdetect/` — separate detector work on `fix/250-cursor-processing-liveness`.
- nix-config changes — companion Codex change lives in a different repo/ticket.

## Verification honesty (for the PR body)

- `cursor-agent` version driven: `2026.08.04-aaa8809`.
- The `--resume` round trip **was** proven on a real chat: first via an
  isolated scratch project + `-p` print-mode invocation (context-recall test),
  then via a real interactive `cursor-agent --trust` session driven in an
  isolated tmux socket (two live turns), which is what established that
  `beforeSubmitPrompt` fires per-turn (including post-resume) with the same
  `conversation_id`. Neither test touched the user's real
  `~/.cursor/hooks.json`.
- **Not** verified: an actual tmux-remux pane restore end-to-end (save a real
  Cursor pane, kill the server, restore, confirm the new pane really is
  `cursor-agent --resume <id>` and the chat continues). That part is only
  unit-tested (the stamp script + the hooks.json installer, each in
  isolation) — say so plainly, don't imply it from the hook-level proof above.
- State the accepted one-shot-per-restore-cycle limitation (see "The one-shot
  problem") explicitly — it is a real, documented gap, not a bug to hide.
- Mention, as a pointer only (not implemented): `noamsto/tmux-remux` already
  has generic `relaunch-stamp`/`install-hook` machinery with a `cursor` preset
  explicitly deferred pending this exact verification — a future follow-up
  could migrate onto it instead of lazytmux's bespoke scripts.
- Note the same limitation Codex's precedent already accepts: the stamped
  command (`cursor-agent --resume <id>`) drops whatever launch flags the
  original pane used (`--trust`/`--force`/`--approve-mcps`), so a restored
  pane may re-prompt for workspace trust before continuing — same shape as
  `codex resume <id>`, not a regression.
- New shell scripts must be tab-indented (project `shfmt` default) —
  `nix build .#lint` enforces it.
