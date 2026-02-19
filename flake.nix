{
  description = "Opinionated tmux configuration with Claude Code integration";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = ["x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin"];

      perSystem = {pkgs, lib, ...}: let
        tmuxConfig = import ./config/tmux.conf.nix {inherit pkgs lib;};
      in {
        packages = {
          default = tmuxConfig.tmux-wrapped;
          wt = import ./wt {inherit pkgs;};
        };
      };

      flake = {
        homeManagerModules.default = import ./modules/home-manager.nix;
      };
    };
}
