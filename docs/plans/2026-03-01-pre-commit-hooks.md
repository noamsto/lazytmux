# Pre-commit Hooks Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add pre-commit hooks (statix, deadnix, alejandra, shellcheck, shfmt, typos, merge-conflict, trailing-whitespace) via git-hooks.nix.

**Architecture:** Use the git-hooks.nix flakeModule with existing flake-parts setup. Hooks auto-install on `nix develop`.

**Tech Stack:** Nix, git-hooks.nix, flake-parts

---

### Task 1: Add git-hooks-nix input and configure hooks

**Files:**
- Modify: `flake.nix`

**Step 1: Add the git-hooks-nix input**

In `flake.nix`, add to `inputs`:

```nix
git-hooks-nix.url = "github:cachix/git-hooks.nix";
git-hooks-nix.inputs.nixpkgs.follows = "nixpkgs";
```

**Step 2: Import the flakeModule**

Change the `mkFlake` body to import the module:

```nix
flake-parts.lib.mkFlake {inherit inputs;} {
  imports = [inputs.git-hooks-nix.flakeModule];
  # ... rest unchanged
};
```

**Step 3: Add hook configuration in perSystem**

Inside `perSystem`, add:

```nix
pre-commit.settings.hooks = {
  # Nix
  statix.enable = true;
  deadnix.enable = true;
  alejandra.enable = true;

  # Shell
  shellcheck.enable = true;
  shfmt.enable = true;

  # General
  typos.enable = true;
  check-merge-conflict.enable = true;
  trim-trailing-whitespace.enable = true;
};
```

**Step 4: Add devShell with shellHook**

Inside `perSystem`, add:

```nix
devShells.default = pkgs.mkShell {
  shellHook = config.pre-commit.shellHook;
  packages = config.pre-commit.settings.enabledPackages;
};
```

Note: `config` must be added to the `perSystem` function arguments.

**Step 5: Lock the new input**

Run: `nix flake lock --update-input git-hooks-nix`
Expected: `flake.lock` updated with git-hooks-nix entry.

**Step 6: Verify hooks install**

Run: `nix develop -c pre-commit run --all-files`
Expected: All hooks run. Some may report findings (statix warnings, trailing whitespace, etc.) — that's fine for this step.

**Step 7: Commit**

```bash
git add flake.nix flake.lock
git commit -m "feat: add pre-commit hooks via git-hooks.nix"
```

---

### Task 2: Fix any findings from initial hook run

**Files:**
- Modify: whichever files the hooks flag

**Step 1: Run hooks and capture output**

Run: `nix develop -c pre-commit run --all-files`

**Step 2: Fix all auto-fixable issues**

alejandra, shfmt, and trim-trailing-whitespace auto-fix. Re-run hooks until clean.

**Step 3: Fix remaining manual issues**

Address any statix, deadnix, shellcheck, or typos findings manually.

**Step 4: Verify clean run**

Run: `nix develop -c pre-commit run --all-files`
Expected: All hooks pass.

**Step 5: Commit**

```bash
git add -A
git commit -m "style: fix pre-commit hook findings"
```
