# Restore aeye carousel panes with their images (#577)

> Revision 4. Three designs were refuted before this one, each by code rather
> than opinion, and §"Rejected designs" records all three because each is
> something the next person will reach for:
>
> - **rev 1** carried the **old** manifest key forward — `gc_sweep` deletes it on
>   the very restore that depends on it (fact 4).
> - **rev 2** drove `tmux-claude-images --ensure-open` and assumed the placeholder
>   pane would close when its relaunch exited — it does not (fact 7b).
> - **rev 3** kept `--ensure-open` and killed the pane explicitly — but
>   `--ensure-open` starts no viewer at all while the manifest is unwritten
>   (fact 8), and the kill races remux's `select-layout` (fact 9).
>
> Rev 4 `exec`s the viewer directly, so the pane **becomes** the viewer: no
> launcher guard, no split, no kill. The spec revision cap was lifted by the
> dispatcher to reach it, on two conditions recorded in §Conditions.

## Problem

`tmux-remux` restore brings an aeye carousel pane back as a **bare shell**. Three
pane kinds stamp `@remux_relaunch` today (Claude via `tmux-update-icons.sh`,
Codex and Cursor via their own hook scripts); a carousel pane stamps nothing, so
restore has no command and falls back to the pane's default shell.

Stamping a relaunch alone is not enough. aeye keys its image manifest by
`<tmux server pid>-<host pane id>` and a restore changes **both** halves, so a
naive `aeye` relaunch yields a live-but-empty viewer.

## Verified facts

Read out of `/home/noams/Data/git/noamsto/aeye` at `240e707` and
`/home/noams/Data/git/noamsto/tmux-remux`. Every claim is quoted from code.

1. **The manifest key is a positional arg to the viewer, and there is no
   fallback.** `main.go:37` documents
   `Key ... "<tmux server pid>-<pane> inside tmux"`; `runGallery(pane)`
   (`gallery.go:1129,1134`) passes it straight to `loadManifest`, and
   `manifestPath` is `dir + "/images/" + strings.TrimPrefix(pane, "%") + ".jsonl"`.
   A keyless `aeye` therefore resolves to `images/.jsonl` and shows nothing.

2. **The key belongs to the HOST pane, not the viewer pane.** `resolve_target`
   (`scripts/tmux-claude-images.sh:71-77`) builds it from `$TMUX`'s server pid and
   `$TMUX_PANE`, and the `prefix + I` bind supplies the host pane explicitly:
   `TMUX_PANE=#{q:pane_id} .../tmux-claude-images` (`config/tmux.conf.nix:614`).

3. **aeye already rebuilds the manifest on resume — at the NEW key.**
   `adapters/claude-code/plugin/scripts/session-backfill.sh` is a SessionStart
   hook gated on `.source == resume` (line 15) that resolves
   `resolve_pane_key` (line 26) and does an authoritative
   `rm -f "$manifest"` + replay from the transcript (line 57). Its own header
   calls it "the sole writer of a resumed pane's manifest". Since remux relaunches
   the agent pane as `claude --resume <uuid>`, this fires on every restore and
   populates the **new** key from the transcript.

4. **aeye garbage-collects the OLD key on that same restore.** `session-reset.sh:74`
   calls `gc_sweep`, and `gc_sweep` (`adapters/core/manifest-lifecycle.sh:143-150`)
   removes a `<srv>-<pane>` base by either branch: a different server pid fails
   `kill -0` → `_gc_rm`; the same server pid with the old pane absent from `live`
   → `_gc_rm`. Its one exemption, `[[ $base == "$pane_file" ]] && continue`,
   protects only the key just stamped — never the old one.

5. **`--ensure-open` is idempotent, never kills, and computes the key itself.**
   `main()` sets `ENSURE_OPEN=1` on that flag (`tmux-claude-images.sh:524`), and
   every launcher returns early on an existing match rather than toggling off
   (lines 111, 229, 271, 307, 397). aeye's own `diagrams.sh:122` relies on exactly
   this. Its flag surface is only `--reconcile`, `--resolve-axis`,
   `--ensure-open`, `--resolve` — there is **no** flag to pass a key or a host
   pane, so the host pane must arrive via `$TMUX_PANE`.

6. **remux carries `Relaunch` and allow-listed WINDOW options, but no pane
   option.** `internal/restore/plan.go:104-118`: "replay its stored scrollback,
   then relaunch via the pane's `@remux_relaunch` override if set"; the override
   "wins over the allow-list". `internal/snapshot/manifest.go:74` calls
   `Relaunch` "exec'd verbatim on restore", and `:108` notes
   `StructureFingerprint` changes when the stamp changes, so a new stamp is not
   throttled away. remux *does* re-apply a `DecorationOptions` allow-list
   (`internal/config/config.go:94` — `@crew_name`, `@crew_color`) via
   `plan.go:179-187`, but those are **window** options applied to
   `<sess>:<index>`. `@claude_img_src` is pane-scoped and one window can hold two
   carousels, so that carrier cannot express it — and widening the allow-list
   would be a change to the remux repo, which is out of scope here.

7. **The stamp runs through the pane's default shell, and the pane does NOT close
   when it exits.** `internal/restore/startup.go:56-74` builds
   `<override>; exec <shell>` and documents both halves: "tmux spawns the pane by
   running the output through the pane's default-shell — POSIX sh, bash, zsh, or
   fish; `;` and `exec` and literal single quotes behave the same across all of
   them", and "The relaunched command runs as a child, then the pane exec's the
   default shell — so quitting the agent/program drops back to an interactive
   prompt instead of tearing down the pane".

   Two hard constraints follow. **(a)** The stamped string must be portable
   across those four shells, so no `VAR=value cmd` prefix — that is a hard error
   in fish, and `programs.lazytmux.defaultShell` defaults to `null` (tmux uses
   `$SHELL`) with fish as the option's own example
   (`modules/home-manager.nix:300-303`). A bare store path with literal args is
   the only safe shape. **(b)** A relaunch that merely exits leaves the pane
   alive as an interactive shell, so a placeholder pane must be killed
   explicitly, not exited out of.

8. **`tmux-claude-images` refuses to launch anything while the manifest is
   unwritten.** `scripts/tmux-claude-images.sh:541-544`:
   ```sh
   if [[ ! -s $MANIFEST ]]; then
           [[ $MODE == tmux ]] && tmux display-message "no images yet for this pane"
           exit 0
   fi
   ```
   That is the **launcher** exiting, before it resolves `VIEWER_BIN` (`:548`) and
   before it reaches `launch_$MODE`. The message is a tmux status-line
   `display-message`, **not** aeye's in-viewer `emptyState` (`gallery.go:963`).
   So `--ensure-open` on a restored pane starts no viewer process at all, and
   there is nothing for the mtime poll to run inside. Since fact 3's backfill
   writes the manifest asynchronously, any design routed through the launcher is
   a race against it — and `images.sh` never re-fires the toggle (only
   `diagrams.sh:122` does), so losing that race leaves the carousel gone until
   the user presses `prefix + I`, which is the bug being fixed.

9. **remux applies the window layout after every split, while relaunches run
   asynchronously.** `internal/restore/plan.go:190-199` appends every `SplitPane`
   for a window and *then* one `SetLayout`, and a `SplitPane`'s pane "is born
   running" its `StartupCommand` (`:44-51`). A relaunch that splits or kills
   panes therefore mutates the window's pane count while `select-layout` is still
   pending, and tmux rejects a layout whose count does not match
   ("have N panes but need M") — the failure class CLAUDE.md already documents
   for the bridge's float handling.

10. **The viewer starts and self-heals with no manifest at all.** `runGallery`
    (`gallery.go:1129`) builds its model with a possibly-empty `images`,
    `mtime: manifestMtime(pane)`, and `cursor: max(0, len(images)-1)`, then runs
    `tea.NewProgram(...).Run()` — there is no early return on zero images, so the
    TUI comes up on `emptyState`. `manifestMtime` returns **0** for an absent file
    (`:1348-1354`), and the tick compares `mt != m.mtime` and calls `m.reload()`
    (`:766-769`), so absent → written is picked up on the next tick. This is what
    makes bypassing the launcher (fact 8) both possible and safe.

## Rejected designs

### Rejected: carry the old key (rev 1)

Stamping `aeye <old-key>` is the obvious design and it is wrong three times over:

- **Fact 4 deletes the manifest** on the very restore the design depends on. The
  symptom is worse than an empty carousel: the images paint from the old file,
  then vanish when `gallery.go:766`'s mtime poll sees it gone and
  `loadManifest` returns `nil`.
- **Fact 2 breaks the toggle.** `launch_tmux` matches an open viewer with
  `awk -v s="$KEY" '$2 == s'` against the **live** key
  (`tmux-claude-images.sh:108-109`), so a viewer stamped with the old key is
  invisible to it and `prefix + I` opens a second one.
- **Fact 3 makes it unnecessary.** aeye already puts the images at the new key.

The correct direction is the inverse: work at the **new** key, which is where
fact 3 already puts the images.

### Rejected: drive `--ensure-open` (revs 2 and 3)

Having established the new-key direction, the natural next move is to let aeye
compute the key by invoking its own launcher with the host pane:
`TMUX_PANE=<host> tmux-claude-images --ensure-open`. This is wrong for two
independent reasons, and both took reading the code to see:

- **Fact 8**: the launcher exits on an unwritten manifest *before* it starts
  anything, so on a restore it opens no viewer at all. Rev 2/3's claim that "the
  viewer opens on aeye's placeholder and the mtime poll reloads it" confused a
  tmux status-line `display-message` with the in-viewer `emptyState`. It is a
  race against fact 3's backfill, and losing it leaves no carousel.
- **Fact 9**: `--ensure-open` splits, and rev 3 additionally killed the
  placeholder — both mutate the window's pane count while remux's `select-layout`
  is still pending. Rev 3's kill would also have destroyed the window whenever
  the viewer was the last kept pane in it (#547 class).

Rev 2 additionally assumed a relaunch that exits closes its pane; fact 7(b) says
it execs an interactive shell instead, leaving a stray shell beside the viewer.

## Design

One new stamp, on the **viewer** pane, pointing at one new lazytmux script.
Deliberately **no** change to the Claude/Codex/Cursor relaunch strings: those are
what `tests/update-icons-resume-guard.bats` exists to protect, and composing a
carousel re-open into them would put this change on a collision course with the
exact regression the dispatcher called out.

**Stamp** (from `tmux-update-icons.sh`, which already reads both
`@remux_relaunch` and — once this lands — `@claude_img_src` in its single batched
`list-panes -s`, so the detection costs no extra fork):

```
@remux_relaunch = "<carousel-restore>"
```

on any pane whose `@claude_img_src` is non-empty, change-gated like the Claude
stamp.

The stamp is a **bare store path with no arguments and no environment prefix**,
which is the only shape portable across all four shells fact 7(a) allows.

**`scripts/tmux-carousel-restore.sh`** (new, `#!/usr/bin/env bash` — so its own
body is bash regardless of the pane's shell), run in the restored viewer pane,
where `$TMUX_PANE` is the viewer's **new** id:

1. Find the host: among the panes of its own window, excluding itself, the one
   whose `pane_current_command` is an agent. Bounded retry, since the agent
   pane's own relaunch runs asynchronously (fact 9) and may not have exec'd yet.
2. Compute `key="<server pid>-<host pane id sans %>"`, the shape `main.go:37`
   documents (see §Conditions — this formula is pinned and tested).
3. Stamp `@claude_img_src="$key"` on **its own** pane, plus `@claude_img_axis`,
   so `prefix + I` finds and toggles this viewer (fact 2's live-key match).
4. `export AEYE_HOST_PANE="$host"` so the viewer's `s` axis toggle has a target
   (`gallery_split.go:37-44`), then `exec aeye "$key"`.
5. On no host found: exit without stamping. The pane falls back to an interactive
   shell — exactly today's behaviour, no worse.

Why this shape and not rev 3's:

- **It bypasses the launcher, so fact 8 does not apply.** `exec aeye` never
  consults `[[ ! -s $MANIFEST ]]`; per fact 10 the viewer comes up on
  `emptyState` with `mtime = 0` and reloads when fact 3's backfill writes. The
  ordering is genuinely self-correcting here, which is the claim rev 2/3 made
  falsely about `--ensure-open`.
- **The pane BECOMES the viewer**, so there is no split and no kill: remux's pane
  count and its saved layout string are untouched, and fact 9's `select-layout`
  race cannot occur. This also reproduces the viewer's *saved* geometry, which
  `--ensure-open` could not — it splits from the host (`:144`, `-t "$PANE"`).
- **It cannot destroy a window.** Rev 3's `kill-pane` would have taken the window
  with it when the viewer was the last kept pane in it (agent pane dropped by the
  smart filter, or `resumeClaude = false`) — the #547 class. Rev 4 kills nothing.

**The cost, accepted deliberately.** lazytmux now duplicates aeye's key formula
and the pane-option stamping `launch_tmux` owns — the fragile area of
noamsto/aeye#185, #108, #150. Rev 3 avoided this on purpose; facts 8-10 make it
the only route that preserves remux's layout, so the trade has inverted. §Conditions
is how that cost is contained.

## Conditions

Both are requirements from the dispatcher's approval of this revision, not
optional polish.

1. **Make key-formula drift loud.** aeye has already changed the key shape once
   (#185: bare pane id → server pid + pane id), and a future change would break
   this silently — the carousel opens, finds nothing, and reads as an unrelated
   bug. So the computation carries a comment naming `aeye main.go:37` as its
   contract source, and a test asserts the shape, so drift surfaces as a red test
   rather than an empty viewer. Assert against aeye's own source or a recorded
   fixture if that is cheap; otherwise assert the documented shape directly.
2. **Make the ambiguous host deterministic, and document the residual.** Two
   agent panes in one window must resolve by a *stated rule* — lowest pane index
   among the matches — never arbitrarily or by whichever `list-panes` row arrives
   first. The residual goes in the PR body in prose, not only here, so whoever
   hits the two-carousel case finds it documented rather than rediscovering it.

### Host discovery is the one heuristic, and it is bounded

There is no way to carry the host pane's identity across a restore: remux
restores no pane options (fact 6), and the host's new id does not exist at stamp
time. Discovery is therefore unavoidable, and it is scoped to *within the
restored window*, using the same agent-command set `tmux-update-icons` already
derives from the agentdetect manifest. A window holding two agent panes and one
carousel is ambiguous; the script picks the **lowest pane index** among the
matches (condition 2 — a stated rule, so the outcome is reproducible) because
guessing wrong costs a carousel keyed to the wrong sibling — recoverable with one
keypress — not data.

### Key hygiene

`@remux_relaunch` is exec'd verbatim (fact 7). This design stamps a **fixed store
path with no interpolated data**, so unlike the codex/cursor stamps there is no
untrusted id to validate — the injection surface those scripts guard against does
not exist here, and neither does fact 7(a)'s fish hazard. `@claude_img_src` is
read only as a boolean ("is this a viewer pane") and is never interpolated into
the stamped command.

Inside the script, the host pane id comes from `tmux list-panes` in the script's
own window and is used as a `-t` target, not as shell text.

## Scope

### In

1. `scripts/tmux-carousel-restore.sh` — new; host discovery, key computation,
   pane-option stamping, then `exec aeye <key>`.
2. `scripts/tmux-update-icons.sh` — read `@claude_img_src` in the existing batched
   format; stamp the viewer pane, gated on a new `RESUME_CAROUSEL` argv flag
   mirroring `RESUME_CLAUDE`.
3. `config/tmux.conf.nix` — `resumeCarouselEnable` input, a `@resume_carousel`
   global, that global as the new argv, and the script packaged/pathed.
4. `modules/home-manager.nix` — `programs.lazytmux.persist.resumeCarousel` option
   plus a `resumeCarouselEnable` gate
   (`persist.enable && persist.package != null && persist.resumeCarousel`),
   mirroring `resumeCodexEnable`/`resumeCursorEnable`.
5. Tests in `tests/update-icons-resume-guard.bats` style, plus unit coverage of
   host discovery.
6. `CLAUDE.md` — the carousel row and the restore mechanism.

### Out

- **Any edit to the aeye repo.** Established unnecessary: fact 3 supplies the
  images at the new key and fact 10 means the viewer tolerates starting before
  they land. Confirmed with the dispatcher — no aeye worker is being dispatched.
- **Modifying the three existing agent relaunch stamps.** See Design.
- **Restoring images across a reboot.** The manifest lives under
  `/tmp/claude-status/images/`. On resume the transcript is authoritative
  (fact 3), so images the transcript records come back anyway; ones it does not
  are gone. Documented, not fixed.
- **Non-tmux carousel hosts** (kitty/wezterm/ghostty/iterm). Not tmux panes, so
  remux has nothing to restore.
- **Changing the default.** `resumeCarousel` defaults to **false**, like
  `resumeCodex`/`resumeCursor`.

## Acceptance criteria

**Outcome criteria — these map 1:1 onto the issue's own Acceptance bullets, and
none of them can be ticked by a change that delivers only the stamp.** The
existing guard suite already drives the real script against a private,
config-less tmux server (`setup()`'s `TMUX_TMPDIR`), so a round-trip is reachable
in that harness.

- [ ] **Running viewer, not a bare shell.** A window holding an agent pane and a
      carousel pane, snapshotted and restored, ends with a pane running the
      viewer — asserted on the restored pane's `pane_current_command` and on
      `@claude_img_src` being set to the **new** key
      (`<new srv pid>-<new host pane>`), not the old one.
- [ ] **Shows the images it had.** After the restore the manifest at the new key
      is non-empty and its entries match the pre-restore image set. Driven
      through the transcript, since fact 3 makes the transcript authoritative and
      `session-backfill.sh:57` wipes anything else.
- [ ] **No stray pane, and the saved layout still applies.** The restored window
      ends with the same pane count it was snapshotted with — rev 4 splits and
      kills nothing, so fact 9's `select-layout` has the count it saved.
- [ ] **The key formula is pinned (condition 1).** A test asserts the computed
      key's shape against `main.go:37`'s documented contract, so an aeye-side
      change to the key surfaces red rather than as an empty viewer.
- [ ] **Ambiguous host resolves deterministically (condition 2).** With two agent
      panes in one window, the lowest pane index wins — asserted, not incidental.
- [ ] **The stamp survives repeated cycles.** A second save/restore round-trip
      restores the carousel again, rather than degrading to a shell.

**Mechanism criteria.**

- [ ] A pane with non-empty `@claude_img_src` gets `@remux_relaunch` stamped to
      the carousel-restore command.
- [ ] The stamped string contains no `VAR=value` prefix and no shell
      metacharacter beyond what fact 7(a) permits, so it is valid under fish as
      well as POSIX sh — asserted directly on the stamped value.
- [ ] The stamp is written **only on change** — a second tick over an unchanged
      pane issues no `set` (asserted with the file's existing tmux-spy pattern).
- [ ] With `RESUME_CAROUSEL` off (including the arg omitted, as the
      `run-shell` hook invocation at `config/tmux.conf.nix:1240` does), no
      carousel stamp is written.
- [ ] `tmux-carousel-restore` picks the agent sibling in its own window, ignores
      itself and non-agent panes, and exits non-fatally when there is no host.
- [ ] The Claude stamper is unchanged and the existing six
      `update-icons-resume-guard.bats` tests still pass unmodified.
- [ ] With `persist.enable = false` or `persist.package = null`, nothing is
      stamped and nothing is provisioned.
- [ ] `nix build .#default`, `nix flake check`, `nix build .#lint` green;
      `shellcheck` clean on every touched script.

## Residual risks

| Risk | Status |
|---|---|
| Two agent panes in one window make host discovery ambiguous | Accepted, made deterministic by condition 2 (lowest pane index). Costs a keypress, not data |
| The script runs before the agent pane has exec'd (fact 9) | Bounded retry, then exit unstamped; degrades to today's bare shell |
| lazytmux's copy of the key formula drifts from aeye's | Condition 1: comment naming the contract source plus a test, so drift is red, not an empty viewer |
| A user-set `AEYE_DIR`/`CLAUDE_STATUS_DIR` is not seen by the restored viewer | Real. `config/tmux.conf.nix:741-754` adds only `TERM*`, `COLORTERM`, `TERMINFO*`, `KITTY_LISTEN_ON`, `AEYE_HOST` to `update-environment` — a non-default state dir is out of scope and documented |
| The viewer starts before the backfill writes | Not a race: fact 10 — `emptyState` with `mtime = 0`, reloaded on the next tick |
| The restored viewer's `s` axis toggle has no host | Handled — the script exports `AEYE_HOST_PANE` and stamps `@claude_img_axis` itself, which is part of the accepted duplication cost |
