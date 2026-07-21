{
  description = "Opinionated tmux configuration with Claude Code integration";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # The wrapped tmux is pinned to plain upstream at a fixed rev to pick up the
    # next-3.8 work (floating panes, scene renderer, menus-in-scene,
    # display-panes-as-a-mode). Pin the exact rev (not a moving branch) so builds
    # stay reproducible. The prior fork (noamsto/tmux fix/popup-overlay-flicker)
    # is dropped: its overlay-clipping fix is already upstream (e242da16), and its
    # two #5336 popup-flicker fixes are now resolved — the overlay redraw fix
    # landed as tmux/tmux#5398, the other was rejected upstream.
    # Bump: repoint rev, then `nix flake lock --update-input tmux-upstream`.
    tmux-upstream = {
      url = "github:tmux/tmux/afb3e954bfcbe7a2ab9c58584e0e3df9a61e271f";
      flake = false;
    };
    flake-parts.url = "github:hercules-ci/flake-parts";
    git-hooks-nix.url = "github:cachix/git-hooks.nix";
    git-hooks-nix.inputs.nixpkgs.follows = "nixpkgs";
    tmux-remux = {
      url = "github:noamsto/tmux-remux";
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

  outputs = inputs @ {flake-parts, ...}: let
    # tmux pinned to upstream at a fixed rev (see tmux-upstream input).
    # autoreconfHook and bison are already in nixpkgs tmux's nativeBuildInputs, so
    # overriding src to a raw git checkout (no pre-generated configure) just
    # works. The version must be a substring of `tmux -V` output ("tmux
    # next-3.8") for the versionCheckHook to pass.
    mkTmux = pkgs:
      pkgs.tmux.overrideAttrs (_old: {
        version = "next-3.8";
        src = inputs.tmux-upstream;
      });
  in
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
          tmuxPkg = mkTmux pkgs;
          carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
          prdash = inputs.prdash.packages.${pkgs.system}.prdash;
        };

        # buildGoModule's checkPhase only runs `go test ./<pkg>` per subPackage
        # (non-recursive), so the default `picker` derivation never exercises
        # picker/agentdetect's nested debounce/manifest/screen/statefile
        # packages. Override checkPhase to scope it to `./agentdetect/...`.
        # Reused (not just for its checkPhase) by remote-bridge-integration-tests
        # below, which needs the prebuilt lztmux-remote-bridge binary offline.
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

          codex-relaunch-stamp-tests =
            pkgs.runCommand "codex-relaunch-stamp-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/codex-relaunch-stamp.bats
              touch $out
            '';

          prune-stale-state-tests =
            pkgs.runCommand "prune-stale-state-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/prune-stale-state.bats
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

          reflow-fanout-tests =
            pkgs.runCommand "reflow-fanout-tests" {
              # tmux: the test drives a private, config-less tmux server so the
              # scripts' bare `tmux` calls hit it, never the dev's own server.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.tmux];
              # reflow measures display width, which needs a UTF-8 locale.
              LANG = "C.UTF-8";
              LC_ALL = "C.UTF-8";
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/reflow-fanout.bats
              touch $out
            '';

          remote-tests =
            pkgs.runCommand "remote-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/remote.bats
              touch $out
            '';

          remote-integration-tests =
            pkgs.runCommand "remote-integration-tests" {
              # tmux: the integration test drives a private, config-less tmux
              # server (via a PATH shim injecting -L/-f) to pin the promote
              # choreography against a live server.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep pkgs.tmux];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/remote-integration.bats
              touch $out
            '';

          remote-bridge-integration-tests =
            pkgs.runCommand "remote-bridge-integration-tests" {
              # tmux: same private, config-less server pattern as the other
              # integration tests. The bridge binary is prebuilt via the
              # vendored buildGoModule (pickerAgentDetect) so this check never
              # invokes `go build` — a non-FOD sandbox has no network.
              # util-linux provides `script`, which the real-tty case uses to
              # give the bridge a pty (so refresh-client + real cursor fire).
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep pkgs.tmux pkgs.util-linux];
              BRIDGE = "${pickerAgentDetect}/bin/lztmux-remote-bridge";
            } ''
              cp -r ${./tests} tests
              export HOME=$TMPDIR
              bats tests/remote-bridge-integration.bats
              touch $out
            '';

          remote-m2-integration-tests =
            pkgs.runCommand "remote-m2-integration-tests" {
              # Same private, config-less two-server pattern: an isolated
              # "remote" tmux -L server plus an isolated "local" tmux -L
              # server, wired together by the M2.1 daemon's --test-local seam
              # (no ssh). Both binaries are prebuilt via the vendored
              # buildGoModule (pickerAgentDetect) so this check never invokes
              # `go build` — a non-FOD sandbox has no network.
              #
              # Uses the pinned next-3.8 tmux (mkTmux), not pkgs.tmux (3.7b):
              # the M2 bridge is a next-3.8 effort and its control-mode
              # notification behavior (e.g. %window-close on kill-window) differs
              # from 3.7b, so the mirror is exercised against the version it —
              # and production, local + remote — actually runs.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep (mkTmux pkgs)];
              DAEMON = "${pickerAgentDetect}/bin/lztmux-remote-bridge-daemon";
              RENDERER = "${pickerAgentDetect}/bin/lztmux-remote-bridge-renderer";
            } ''
              cp -r ${./tests} tests
              export HOME=$TMPDIR
              bats tests/remote-m2-integration.bats
              touch $out
            '';
        };

        packages = {
          default = tmuxConfig.tmux-wrapped;
          # Stable store path for the Codex managed-hook config (lazytmux#140
          # Task 3) to point its `command` at, independent of the tmux wrapper.
          codex-relaunch-stamp = tmuxConfig.script.codex-relaunch-stamp;
        };
      };

      flake = {
        nixosModules.default = import ./modules/nixos.nix;

        homeManagerModules.default = {pkgs, ...} @ args:
          import ./modules/home-manager.nix (args
            // {
              tmux-pkg = mkTmux pkgs;
              tmux-remux-pkg = inputs.tmux-remux.packages.${pkgs.system}.default;
              carousel-toggle = inputs.aeye.packages.${pkgs.system}.toggle;
              carousel-aeye = inputs.aeye.packages.${pkgs.system}.default;
              carouselPluginSkills = "${inputs.aeye}/adapters/claude-code/plugin/skills";
              prdash = inputs.prdash.packages.${pkgs.system}.prdash;
            });
      };
    };
}
