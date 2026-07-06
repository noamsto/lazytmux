{
  description = "Opinionated tmux configuration with Claude Code integration";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Pinned solely for tmux 3.6a. In 3.7 an open display-popup is repainted on
    # every overlapping background-pane redraw (side effect of the issue-4920
    # "popup overwritten by background updates" fix), so a fullscreen TUI behind
    # it — Claude, vim, btop — flickers the popup on every frame. 3.6a did not
    # repaint popups from background output, and 3.7 has no option to restore
    # that. Only the wrapped tmux comes from here.
    # Cause:    https://github.com/tmux/tmux/issues/4920
    # Tracking: https://github.com/tmux/tmux/issues/5336  (unpin when resolved)
    nixpkgs-tmux36.url = "github:NixOS/nixpkgs/567a49d1913ce81ac6e9582e3553dd90a955875f";
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
    prdash = {
      url = "github:noamsto/prdash";
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
          tmuxPkg = inputs.nixpkgs-tmux36.legacyPackages.${pkgs.system}.tmux;
          carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
          prdash = inputs.prdash.packages.${pkgs.system}.prdash;
        };

        # buildGoModule's checkPhase only runs `go test ./<pkg>` per subPackage
        # (non-recursive), so the default `picker` derivation never exercises
        # picker/agentdetect's nested debounce/manifest/screen/statefile
        # packages. Override checkPhase to scope it to `./agentdetect/...`.
        pickerAgentDetect =
          (import ./picker {
            inherit pkgs lib;
            processIcons = import ./config/process-icons.nix;
            fallbackIcon = "";
            maxIconsPicker = "5";
          }).overrideAttrs (_old: {
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              export GOFLAGS=''${GOFLAGS//-trimpath/}
              go test ./agentdetect/...
              runHook postCheck
            '';
          });
      in {
        pre-commit.settings.hooks = {
          # Nix
          statix.enable = true;
          deadnix.enable = true;
          alejandra.enable = true;

          # Shell
          shellcheck.enable = true;
          shfmt.enable = true;
          macos-portability = {
            enable = true;
            name = "macos-portability";
            description = "Reject Linux-only binaries that break on nix-darwin";
            entry = "bash ${./tests/check-portability.sh}";
            files = "^scripts/.*\\.sh$";
          };

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

          reconcile-tests =
            pkgs.runCommand "reconcile-tests" {
              # git: the test derives tags from a real repo it builds in $HOME.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.git];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/reconcile.bats
              touch $out
            '';

          agent-detect-arm-tests =
            pkgs.runCommand "agent-detect-arm-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/agent-detect-arm.bats
              touch $out
            '';

          agent-detect-merge-tests =
            pkgs.runCommand "agent-detect-merge-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/agent-detect-merge.bats
              touch $out
            '';

          agent-detect-enum-tests =
            pkgs.runCommand "agent-detect-enum-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/agent-detect-enum.bats
              touch $out
            '';

          agent-detect-go-tests = pickerAgentDetect;
        };

        packages = {
          default = tmuxConfig.tmux-wrapped;
        };
      };

      flake = {
        homeManagerModules.default = {pkgs, ...} @ args:
          import ./modules/home-manager.nix (args
            // {
              tmux-pkg = inputs.nixpkgs-tmux36.legacyPackages.${pkgs.system}.tmux;
              tmux-state-pkg = inputs.tmux-state.packages.${pkgs.system}.default;
              carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
              carousel-aeye = inputs.aeye.packages.${pkgs.system}.default;
              carouselPluginSkills = "${inputs.aeye}/adapters/claude-code/plugin/skills";
              prdash = inputs.prdash.packages.${pkgs.system}.prdash;
            });
      };
    };
}
