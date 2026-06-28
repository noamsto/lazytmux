# tmux-fingers from develop, ahead of the v2.6.2 nixpkgs release.
# Picks up PR #161 ("remove tput calls", merged 2026-03-16) which kills the
# per-style `tput` fork on every load-config — the failure path that adds ~1s
# when TERM is stripped in tmux subshells. Delete this directory once
# tmuxPlugins.fingers in nixpkgs ships a post-#161 version.
{
  mkTmuxPlugin,
  replaceVars,
  fetchFromGitHub,
  crystal,
}: let
  # Bump shards.nix in lockstep if shard.lock changes upstream.
  rev = "c77287b56ddf490a98925f181ea1c5d4c031576d";
  fingers = crystal.buildCrystalPackage {
    format = "shards";
    version = "2.6.2-develop-${builtins.substring 0 7 rev}";
    pname = "fingers";
    src = fetchFromGitHub {
      owner = "Morantron";
      repo = "tmux-fingers";
      inherit rev;
      hash = "sha256-9t0MTj00PF3KlT5ihBz/7WXuA5fNu7y/KNUkVT2E8bY=";
    };

    shardsFile = ./shards.nix;
    crystalBinaries.tmux-fingers.src = "src/fingers.cr";

    # @fingers-enabled-builtin-patterns is consumed as a comma-list
    # (add_builtin_patterns splits on ",") but declared as a single-value enum,
    # so any subset fails load-config. Make it a free-form string instead.
    # TODO(upstream): drop once Morantron/tmux-fingers#177 lands a release.
    patches = [./enabled-builtin-patterns-list.patch];

    postInstall = ''
      shopt -s dotglob extglob
      rm -rv !("tmux-fingers.tmux"|"bin")
      shopt -u dotglob extglob
    '';

    doCheck = false;
    doInstallCheck = false;
  };
  plugin = mkTmuxPlugin {
    inherit (fingers) version src;
    pluginName = "tmux-fingers";
    rtpFilePath = "tmux-fingers.tmux";

    patches = [
      (replaceVars ./fix.patch {
        tmuxFingersDir = "${fingers}/bin";
      })
    ];
  };
in
  # Expose the binary so keybindings can invoke it with a custom env (TERM).
  # Attach it after mkTmuxPlugin rather than via a `passthru` arg: newer
  # nixpkgs sets `passthru.updateScript` inside mkTmuxPlugin with a shallow
  # `//`, and its `overrideAttrs` re-invokes mkTmuxPlugin — both paths clobber
  # a caller-supplied `passthru`. A plain `//` on the result survives.
  plugin // {passthru = (plugin.passthru or {}) // {inherit fingers;};}
