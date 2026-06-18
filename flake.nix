{
  description = "Opinionated tmux configuration with Claude Code integration";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    git-hooks-nix.url = "github:cachix/git-hooks.nix";
    git-hooks-nix.inputs.nixpkgs.follows = "nixpkgs";
    tmux-state = {
      url = "github:noamsto/tmux-state";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    aeye = {
      url = "github:noamsto/aeye";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      imports = [inputs.git-hooks-nix.flakeModule];

      systems = ["x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin"];

      perSystem = {
        config,
        pkgs,
        lib,
        ...
      }: let
        tmuxConfig = import ./config/tmux.conf.nix {
          inherit pkgs lib;
          carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
        };
      in {
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
          check-merge-conflicts.enable = true;
          trim-trailing-whitespace.enable = true;
        };

        devShells.default = pkgs.mkShell {
          inherit (config.pre-commit) shellHook;
          packages =
            config.pre-commit.settings.enabledPackages
            ++ [
              pkgs.go
              pkgs.gopls
              pkgs.gotools
              pkgs.bats
              pkgs.jq
            ];
        };

        checks = {
          enrich-tests =
            pkgs.runCommand "enrich-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.jq pkgs.coreutils];
              # truncate_ellipsis appends a multibyte "…"; bash's ${#REPLY}
              # only counts it as one char under a UTF-8 locale.
              LANG = "C.UTF-8";
              LC_ALL = "C.UTF-8";
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/enrich.bats
              touch $out
            '';

          icons-tests =
            pkgs.runCommand "icons-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.jq pkgs.coreutils];
              # measure_display_width classifies multibyte codepoints; bash's
              # per-char indexing only works under a UTF-8 locale.
              LANG = "C.UTF-8";
              LC_ALL = "C.UTF-8";
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/icons.bats
              touch $out
            '';

          claude-issues-tests =
            pkgs.runCommand "claude-issues-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/claude-issues.bats
              touch $out
            '';

          log-tests =
            pkgs.runCommand "log-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.util-linux];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/log.bats
              touch $out
            '';

          splash-tests =
            pkgs.runCommand "splash-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/splash.bats
              touch $out
            '';

          interrupt-tests =
            pkgs.runCommand "interrupt-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
              LANG = "C.UTF-8";
              LC_ALL = "C.UTF-8";
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/interrupt.bats
              touch $out
            '';

          naming-seed-tests =
            pkgs.runCommand "naming-seed-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.jq pkgs.coreutils];
            } ''
              cp -r ${./claude-plugin} claude-plugin
              cp -r ${./tests} tests
              bats tests/naming-seed.bats
              touch $out
            '';
        };

        packages = {
          default = tmuxConfig.tmux-wrapped;
        };
      };

      flake = {
        homeManagerModules.default = {pkgs, ...} @ args:
          import ./modules/home-manager.nix (args
            // {
              tmux-state-pkg = inputs.tmux-state.packages.${pkgs.system}.default;
              carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
              carousel-aeye = inputs.aeye.packages.${pkgs.system}.default;
              carouselPluginSkills = "${inputs.aeye}/adapters/claude-code/plugin/skills";
            });
      };
    };
}
