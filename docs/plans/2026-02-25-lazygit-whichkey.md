# lazygit popup + tmux-which-key Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a lazygit floating popup on `prefix + g` and a tmux-which-key menu on `prefix + Space` with catppuccin mocha colors.

**Architecture:** Both changes live entirely in `config/tmux.conf.nix`. lazygit is added to the wrapped tmux PATH and bound to a `display-popup`. tmux-which-key is fetched from GitHub via `mkTmuxPlugin`, configured via a `config/which-key.json` file referenced by its nix store path, and loaded in `pluginRunShells`.

**Tech Stack:** Nix (`pkgs.tmuxPlugins.mkTmuxPlugin`, `pkgs.fetchFromGitHub`, `pkgs.writeText`), tmux `display-popup`, tmux-which-key plugin.

---

### Task 1: Add lazygit to tmux PATH and bind `prefix + g`

**Files:**
- Modify: `config/tmux.conf.nix:361` (makeBinPath line)
- Modify: `config/tmux.conf.nix:232-234` (keybindings section)

**Step 1: Add `pkgs.lazygit` to the wrapped tmux PATH**

In `config/tmux.conf.nix`, find the `makeBinPath` line (currently line 361):

```nix
--prefix PATH : ${lib.makeBinPath (scripts ++ [pkgs.sesh])} \
```

Change to:

```nix
--prefix PATH : ${lib.makeBinPath (scripts ++ [pkgs.sesh pkgs.lazygit])} \
```

**Step 2: Add the `prefix + g` binding**

After the sesh bindings block (after line 234), add:

```nix
    # Lazygit floating popup
    bind-key "g" display-popup -E -w 90% -h 90% lazygit
```

**Step 3: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat: add lazygit floating popup on prefix+g"
```

---

### Task 2: Fetch tmux-which-key plugin

**Files:**
- Modify: `config/tmux.conf.nix` — add plugin derivation in the `let` block (after the `nerd-font-wn` block, around line 45)

**Step 1: Add the plugin derivation**

After the `nerd-font-wn` block (line 45), add:

```nix
  which-key = pkgs.tmuxPlugins.mkTmuxPlugin {
    pluginName = "tmux-which-key";
    version = "2026-02-25";
    src = pkgs.fetchFromGitHub {
      owner = "Nucc";
      repo = "tmux-which-key";
      rev = "151227fe1ec40cd5e8a17b34a5d08dda9e1ef3fd";
      sha256 = "1rrmp64j3sg0ygwazmakdvmr48nslk150g4jykv3lzs01r1bjvqa";
    };
  };
```

**Step 2: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat: fetch tmux-which-key plugin"
```

---

### Task 3: Create the which-key JSON config

**Files:**
- Create: `config/which-key.json`

**Step 1: Create the file**

```json
{
  "items": [
    { "key": "g", "type": "popup", "command": "lazygit", "description": "lazygit" }
  ]
}
```

**Step 2: Commit**

```bash
git add config/which-key.json
git commit -m "feat: add minimal which-key config"
```

---

### Task 4: Wire which-key into the tmux config

**Files:**
- Modify: `config/tmux.conf.nix` — `pluginConfigs`, `pluginRunShells`, and the `Space` binding

**Step 1: Add a nix reference to the JSON config file**

In the `let` block (after `nerdFontConfig`, around line 64), add:

```nix
  whichKeyConfig = pkgs.writeText "which-key.json" (builtins.readFile ../config/which-key.json);
```

**Step 2: Add plugin options to `pluginConfigs`**

In the `pluginConfigs` string (after the `# continuum` block, before the closing `''`), add:

```nix
    # which-key
    set -g @which-key-trigger "Space"
    set -g @which-key-config "${whichKeyConfig}"
    set -g @which-key-popup-bg "#1e1e2e"
    set -g @which-key-popup-fg "#cba6f7"
```

**Step 3: Add `run-shell` to `pluginRunShells`**

Append to the `pluginRunShells` string:

```nix
    run-shell ${which-key}/share/tmux-plugins/tmux-which-key/which-key.tmux
```

**Step 4: Rebind `Space` (remove old next-layout binding)**

tmux's default `prefix + Space` is `next-layout`. The plugin sets its own binding via `@which-key-trigger`, so no explicit `bind` is needed — but if there's an explicit `bind Space` anywhere in the config, remove it. Check with:

```bash
grep -n "bind.*Space" config/tmux.conf.nix
```

If nothing found, no action needed.

**Step 5: Commit**

```bash
git add config/tmux.conf.nix
git commit -m "feat: wire tmux-which-key with catppuccin mocha colors"
```

---

### Task 5: Build and verify

**Step 1: Build the nix derivation**

From the repo root, run whatever command rebuilds the tmux config (e.g. `nh home switch` or `nixos-rebuild switch`). Confirm it builds without errors.

**Step 2: Reload tmux**

```
prefix + r
```

**Step 3: Verify lazygit**

Press `prefix + g` — a full-screen popup should open lazygit. Press `q` to close.

**Step 4: Verify which-key**

Press `prefix + Space` — a popup menu should appear with a dark (`#1e1e2e`) background and mauve (`#cba6f7`) text, showing `g → lazygit`. Press `g` to confirm it launches lazygit. Press `Escape` to dismiss.

**Step 5: Push**

```bash
git push
```
