# tmux-state Integration (Phase 2a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Wire `tmux-state` (the Go binary at `~/Data/git/noamsto/tmux-state`) into lazytmux as an opt-in feature, default disabled. When enabled, lazytmux installs hooks, keybindings, and a systemd save timer that drive `tmux-state`. Coexists with the existing `tmux-resurrect`/`tmux-continuum` plugins — does NOT remove them yet (that's Phase 2b after the soak test).

**Architecture:** New flake input `tmux-state` (defaults to `github:noamsto/tmux-state`, can be overridden with `path:...` for local dev). New `programs.lazytmux.persist.*` options block in `modules/home-manager.nix`. When `persist.enable = true`, the home-manager module: (a) places `tmux-state` on PATH, (b) appends a hooks+keybindings snippet to the tmux conf, (c) writes a systemd user timer + service. All gated behind the option — default-off setups are zero-impact.

**Tech Stack:** Nix flake, flake-parts, home-manager module, tmux conf strings, systemd user units.

**Spec:** `docs/superpowers/specs/2026-04-26-tmux-state-store-design.md`

**Out of scope for Phase 2a:**
- Removing `@resurrect-*` / `@continuum-*` lines (Phase 2b — flip after soak)
- Rich `lazytmux/picker --state` mode (Phase 3)
- Auto-restore on tmux start (will be wired but `restoreMode` defaults to `"off"` for the soak — flip to `"auto"` once trusted)

**Testing model:** No unit tests in lazytmux. Each task ends with `nix flake check` + a manual switch and verification of the relevant artifacts (file contents, systemd unit listing, tmux option queries).

---

## Phase 0: Flake input

### Task 1: Add `tmux-state` as a flake input

**Files:**
- Modify: `flake.nix`

- [ ] **Step 1: Add input declaration**

In `inputs = { ... }` block of `flake.nix`, add:

```nix
tmux-state = {
  url = "github:noamsto/tmux-state";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

(For local dev before pushing the remote: users can override with `inputs.lazytmux.inputs.tmux-state.url = "path:/home/noams/Data/git/noamsto/tmux-state"` from their consuming flake.)

- [ ] **Step 2: Pass it through `perSystem`**

The current `flake.nix` doesn't reference inputs from inside `perSystem`. The `tmux-state` package needs to flow into `homeManagerModules.default`. The flake-parts pattern: capture inputs at the `mkFlake` outer level and pass into the module.

Modify the `flake = { ... }` block at the bottom of `flake.nix`:

```nix
flake = {
  homeManagerModules.default = {
    config,
    lib,
    pkgs,
    ...
  } @ args: import ./modules/home-manager.nix (args // {
    tmux-state-pkg = inputs.tmux-state.packages.${pkgs.system}.default;
  });
};
```

This way the module always has `tmux-state-pkg` in scope without needing a special `_module.args` pattern.

- [ ] **Step 3: Update `flake.lock`**

Run: `nix flake update tmux-state` (or `nix flake update` for a fresh lock).

Expected: `flake.lock` contains a new node for `tmux-state`.

- [ ] **Step 4: Verify flake evaluates**

Run: `nix flake check`

Expected: passes (no module changes yet — the input is just declared).

- [ ] **Step 5: Commit**

```bash
git add flake.nix flake.lock
git commit -m "feat(flake): add tmux-state as flake input"
```

---

## Phase 1: home-manager option block

### Task 2: Add `programs.lazytmux.persist` options skeleton (no behavior yet)

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Accept `tmux-state-pkg` argument**

Top of `modules/home-manager.nix`, change:

```nix
{
  config,
  lib,
  pkgs,
  ...
}: let
```

to:

```nix
{
  config,
  lib,
  pkgs,
  tmux-state-pkg ? null,  # injected via flake; null when consumed standalone
  ...
}: let
```

- [ ] **Step 2: Add the options block**

Inside `options.programs.lazytmux = { ... };`, add a new `persist` sub-block alongside `worktrunk` (around the existing `worktrunk = { ... }` block, ~line 76+):

```nix
persist = {
  enable = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether to enable tmux-state persistence (snapshots, undo, auto-restore).
      Default off during Phase 2a soak — flip to true on a single host first,
      observe for a week, then make default after Phase 2b lands the
      resurrect/continuum removal.
    '';
  };

  saveInterval = lib.mkOption {
    type = lib.types.int;
    default = 60;
    description = "Seconds between periodic saves (systemd timer cadence).";
  };

  restoreMode = lib.mkOption {
    type = lib.types.enum [ "auto" "interactive" "off" ];
    default = "off";
    description = ''
      Behavior on tmux server start. "off" disables auto-restore (safe default
      during soak — manual `prefix + R` still works). "auto" applies the smart
      filter and restores. "interactive" prompts via picker (not implemented in
      tmux-state v0.1.0 — falls back to "off").
    '';
  };

  package = lib.mkOption {
    type = lib.types.nullOr lib.types.package;
    default = tmux-state-pkg;
    defaultText = lib.literalExpression "inputs.tmux-state.packages.\${system}.default";
    description = ''
      The tmux-state package to use. Defaults to the flake input. Set to a
      different derivation to override (e.g. a local checkout for dev).
    '';
  };
};
```

(All other knobs — `historyLimit`, `commandAllowList`, etc. — get added in Task 3 only if needed; v0.1.0 reads everything from environment + defaults so these are nice-to-haves, not blockers.)

- [ ] **Step 3: Sanity-check evaluation**

Run: `nix flake check`

Expected: passes. The options are declared but no `config = ...` block uses them yet.

- [ ] **Step 4: Commit**

```bash
git add modules/home-manager.nix
git commit -m "feat(module): add programs.lazytmux.persist options block (off by default)"
```

---

## Phase 2: tmux conf hooks + keybindings

### Task 3: Generate persist tmux conf snippet when enabled

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Define the snippet generator**

In the `let` block of `modules/home-manager.nix`, after the existing `tmuxConfig = import ../config/tmux.conf.nix {...};` (around line 64), add:

```nix
tmuxStateBin =
  if cfg.persist.enable && cfg.persist.package != null
  then "${cfg.persist.package}/bin/tmux-state"
  else null;

# tmux conf snippet that hooks tmux-state into tmux. Empty string when disabled.
tmuxStateConf =
  if tmuxStateBin == null
  then ""
  else ''

    # === tmux-state (Phase 2a, opt-in via programs.lazytmux.persist) ===
    set-hook -g session-created       'run-shell -b "${tmuxStateBin} save --reason=hook:session-created"'
    set-hook -g window-linked         'run-shell -b "${tmuxStateBin} save --reason=hook:window-linked"'
    set-hook -g client-detached       'run-shell -b "${tmuxStateBin} save --reason=hook:client-detached"'

    set-hook -g pane-died             'run-shell -b "${tmuxStateBin} capture-event pane-died          --pane=#{hook_pane}    --window=#{hook_window} --session=#{hook_session}"'
    set-hook -g window-unlinked       'run-shell -b "${tmuxStateBin} capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
    set-hook -g session-closed        'run-shell -b "${tmuxStateBin} capture-event session-closed     --session=#{hook_session}"'

    set-hook -g window-renamed        'run-shell -b "${tmuxStateBin} index-update --session=#{hook_session}"'
    set-hook -g window-layout-changed 'run-shell -b "${tmuxStateBin} index-update --session=#{hook_session}"'

    ${lib.optionalString (cfg.persist.restoreMode == "auto") ''
      run-shell -b '${tmuxStateBin} restore --auto'
    ''}

    bind   u    run-shell '${tmuxStateBin} undo --pop'
    bind   U    run-shell '${tmuxStateBin} pick --kind=close'
    bind   R    run-shell '${tmuxStateBin} pick --kind=snapshot'
    bind C-s    run-shell '${tmuxStateBin} save --reason=keybinding'
  '';
```

- [ ] **Step 2: Append the snippet to the generated tmux conf**

Find where `tmuxConfig.tmux-wrapped` is referenced or where the tmux config text is written. Two integration points are possible:

**Option A (preferred — minimal edit to `tmux.conf.nix`):** pass the snippet as an argument.

In `let`, change:
```nix
tmuxConfig = import ../config/tmux.conf.nix {
  inherit pkgs lib;
  extraProcessIcons = cfg.processIcons;
  terminalTerm = ...;
};
```

to add:
```nix
extraConfText = tmuxStateConf;
```

Then in `config/tmux.conf.nix`, accept the new arg with `extraConfText ? ""` and append it to the generated tmux.conf text:

```nix
# In tmux.conf.nix, near the bottom of where the config text is built:
tmuxConfText = pkgs.writeText "tmux.conf" ''
  ${baseConf}
  ${extraConfText}
'';
```

(Adjust to whatever variable holds the conf text in `tmux.conf.nix`.)

**Option B (if `tmux.conf.nix` is harder to edit):** write the snippet to a separate file via home-manager `home.file` and `source` it from `~/.config/tmux/tmux.conf` via the existing activation script. More moving pieces; prefer Option A unless a real obstacle appears.

- [ ] **Step 3: Verify default-off path doesn't change anything**

Run: `nix build .` and inspect the generated tmux.conf via:
```bash
cat /nix/store/*-tmux.conf | grep -c tmux-state
```

Expected: `0`. With `persist.enable = false` (default), the snippet is empty.

- [ ] **Step 4: Verify enabled path injects hooks**

Make a small test harness in your home-manager config (a separate test machine or branch):
```nix
programs.lazytmux.persist.enable = true;
```

Then `home-manager build .#yourhost` and inspect:
```bash
zgrep tmux-state ~/.config/tmux/tmux.conf || cat result/.../tmux.conf | grep tmux-state | head
```

Expected: 8 hook lines + 4 keybinding lines reference `tmux-state`.

- [ ] **Step 5: Commit**

```bash
git add modules/home-manager.nix config/tmux.conf.nix
git commit -m "feat(persist): inject tmux-state hooks and keybindings when enabled"
```

---

## Phase 3: systemd save timer

### Task 4: Wire systemd user timer + service

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Add systemd config in the module's `config = { ... };` block**

After the existing `systemd.user.services.lazytmux-startup = { ... };` block (look for `programs.lazytmux.startupSession` use), add:

```nix
systemd.user.timers.lazytmux-state-save = lib.mkIf (cfg.persist.enable && cfg.persist.package != null) {
  Unit.Description = "Periodic tmux-state snapshot";
  Timer = {
    OnBootSec = "2min";
    OnUnitActiveSec = "${toString cfg.persist.saveInterval}s";
    Unit = "lazytmux-state-save.service";
  };
  Install.WantedBy = [ "timers.target" ];
};

systemd.user.services.lazytmux-state-save = lib.mkIf (cfg.persist.enable && cfg.persist.package != null) {
  Unit.Description = "Save tmux-state snapshot";
  Service = {
    Type = "oneshot";
    ExecStart = "${cfg.persist.package}/bin/tmux-state save --reason=timer";
  };
};

systemd.user.timers.lazytmux-state-gc = lib.mkIf (cfg.persist.enable && cfg.persist.package != null) {
  Unit.Description = "tmux-state GC (orphan scrollback files)";
  Timer = {
    OnCalendar = "weekly";
    Unit = "lazytmux-state-gc.service";
  };
  Install.WantedBy = [ "timers.target" ];
};

systemd.user.services.lazytmux-state-gc = lib.mkIf (cfg.persist.enable && cfg.persist.package != null) {
  Unit.Description = "tmux-state garbage collection";
  Service = {
    Type = "oneshot";
    ExecStart = "${cfg.persist.package}/bin/tmux-state gc";
  };
};
```

- [ ] **Step 2: Verify default-off doesn't generate units**

```bash
nix build .  # or home-manager build with persist.enable = false
ls $out/etc/systemd/user/ 2>/dev/null | grep -c lazytmux-state || true
```

Expected: 0 — unit files are absent when `persist.enable = false`.

- [ ] **Step 3: Verify enabled path generates units**

With `persist.enable = true`:
```bash
ls $out/etc/systemd/user/ | grep lazytmux-state
```

Expected: `lazytmux-state-save.service`, `lazytmux-state-save.timer`, `lazytmux-state-gc.service`, `lazytmux-state-gc.timer`.

- [ ] **Step 4: Commit**

```bash
git add modules/home-manager.nix
git commit -m "feat(persist): add systemd user timer for periodic save and weekly GC"
```

---

## Phase 4: PATH + activation tweaks

### Task 5: Add tmux-state to user PATH when enabled

**Files:**
- Modify: `modules/home-manager.nix`

- [ ] **Step 1: Conditionally add to home.packages**

In the module's `config = { ... };` block, find the existing `home.packages = [ ... ];` (if any) or add a new line:

```nix
home.packages = lib.mkIf (cfg.persist.enable && cfg.persist.package != null) [ cfg.persist.package ];
```

If `home.packages` already has other entries gated on `cfg.enable`, merge:
```nix
home.packages =
  lib.optional (cfg.persist.enable && cfg.persist.package != null) cfg.persist.package
  ++ lib.optional cfg.worktrunk.enable pkgs.worktrunk
  # ... existing entries
;
```

(Match whatever pattern lazytmux already uses — don't introduce a new convention.)

- [ ] **Step 2: Verify `tmux-state` is reachable when enabled**

After `home-manager switch` with `persist.enable = true`:
```bash
which tmux-state
tmux-state version
```

Expected: prints a `/nix/store/.../bin/tmux-state` path and `0.1.0`.

- [ ] **Step 3: Commit**

```bash
git add modules/home-manager.nix
git commit -m "feat(persist): expose tmux-state on user PATH when enabled"
```

---

## Phase 5: documentation

### Task 6: Update CLAUDE.md and README

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a "Persist (tmux-state)" section to CLAUDE.md**

Add before the "Key Conventions" section:

```markdown
### Persist (tmux-state)

Optional: `programs.lazytmux.persist.enable = true` in home-manager config wires the
[tmux-state](https://github.com/noamsto/tmux-state) Go binary as the persistence
layer (replaces tmux-resurrect/tmux-continuum). Default off during Phase 2a
soak; flip per-host once trusted, then make default after Phase 2b removes the
old plugins.

When enabled:
- tmux hooks fire `tmux-state save` on structural change and
  `tmux-state capture-event` on close.
- systemd user timer runs `tmux-state save --reason=timer` every 60s.
- Keybindings: `prefix + u` (undo pop), `prefix + U` (close-event picker),
  `prefix + R` (snapshot picker), `prefix + Ctrl-s` (immediate save).
- Storage: `$XDG_DATA_HOME/tmux-state/state.db` + scrollbacks dir.

`@resurrect-*` and `@continuum-*` settings + plugin loads remain in
`config/tmux.conf.nix` during Phase 2a — both run in parallel. Phase 2b
removes them.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document persist (tmux-state) integration"
```

---

## Manual verification checklist (run after Task 6)

Use a test home-manager generation; do NOT yet flip on the production host.

1. **Default-off is zero-impact:**
   - `home-manager build` with `persist.enable = false` (or unset).
   - Diff against pre-Task-1 generation: only `flake.lock` changes (the new tmux-state input). No tmux conf changes, no new systemd units, no new packages on PATH.

2. **Enabled path generates expected artifacts:**
   - Switch to a generation with `persist.enable = true` on a non-production user (or branch).
   - `tmux-state version` prints `0.1.0`.
   - `~/.config/tmux/tmux.conf` (or the wrapped equivalent) contains 8 `set-hook -g` lines and 4 `bind` lines referencing tmux-state.
   - `systemctl --user list-timers | grep lazytmux-state` shows `lazytmux-state-save.timer` and `lazytmux-state-gc.timer`.
   - First save fires within ~2 min (`OnBootSec`), then every 60s.
   - `sqlite3 ~/.local/share/tmux-state/state.db 'SELECT id, kind, reason FROM events ORDER BY id DESC LIMIT 5;'` shows `snapshot` rows with `reason='timer'`.

3. **No conflict with continuum:**
   - With both `@continuum-restore 'on'` (existing) AND `programs.lazytmux.persist.enable = true`, the tmux server starts cleanly. `restoreMode = "off"` (default) means tmux-state will NOT auto-restore; only continuum does. If `restoreMode = "auto"` is set, both restore — which is intentional during soak so you can compare what they produce. Expect duplicate sessions; that's OK for the soak.

4. **`prefix + Ctrl-s` triggers an immediate save:**
   - Press the binding; check `events` table — new row with `reason='keybinding'`.

5. **Hook-fired save under load:**
   - Open 5 windows in quick succession.
   - Verify `events` table doesn't show 5+ snapshot rows (throttle works).

6. **Generation rollback:**
   - `home-manager switch --rollback`.
   - tmux-state binary is no longer on PATH; systemd timer is gone; tmux conf no longer has hooks.
   - DB at `$XDG_DATA_HOME/tmux-state/` is untouched (lives outside `/nix/store`).

7. **`nix flake check`** passes.

---

## What Phase 2b looks like (for reference; not in this plan)

Once Phase 2a soaks for ~1 week without surprises:

- Remove `@resurrect-strategy-*`, `@resurrect-capture-pane-contents`, `@continuum-restore`, `@continuum-save-interval` from `config/tmux.conf.nix`.
- Remove the two `run-shell ${tmuxPlugins.resurrect}/...` and `run-shell ${tmuxPlugins.continuum}/...` lines.
- Remove the `#({tmuxPlugins.continuum}/share/.../continuum_save.sh)` invocation in `status-format[0]`.
- Flip `programs.lazytmux.persist.enable.default = true` and `restoreMode.default = "auto"`.
- Update `modules/home-manager.nix` activation script comment (remove the continuum-restore reference).
- Single commit with body explaining the cutover.

Phase 3 (lazytmux/picker --state mode) gets its own plan in this repo when ready.

---

## Constraints

- One commit per task (~6 commits total).
- Don't touch `picker/`, `scripts/`, or anything outside `flake.nix` / `modules/home-manager.nix` / `config/tmux.conf.nix` / `CLAUDE.md`.
- Don't remove resurrect/continuum yet — that's Phase 2b after the soak.
- All persist behavior gated on `cfg.persist.enable` — default-off must be zero-impact.
