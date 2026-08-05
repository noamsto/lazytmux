{
  description = "Opinionated tmux configuration with Claude Code integration";

  # Public binary cache so installs pull the from-source tmux (mkTmux) and the Go
  # binaries instead of compiling. Populated by CI (cachix-action) for
  # x86_64-linux + aarch64-darwin.
  nixConfig = {
    extra-substituters = ["https://lazytmux.cachix.org"];
    extra-trusted-public-keys = ["lazytmux.cachix.org-1:8P28D3LZAKqPlkEGKzRRU9gon3rgBv4u8/4VWRn6TCg="];
  };

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
      url = "github:tmux/tmux/d5afb67a81d8a30379e0d4186ec4b968244393bf";
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
    # --disable-asan: ASan's runtime deadlocks during init on macOS 26
    # (llvm/llvm-project#200447), hanging every tmux call before main(). Upstream
    # now defaults ASan off on Darwin, so this is belt-and-suspenders — it keeps
    # the flag correct regardless of upstream's default. No-op on Linux.
    # --enable-jemalloc (darwin only): with ASan off, configure now *requires* an
    # explicit --enable/--disable-jemalloc on macOS, because its calloc(3) can
    # fail to zero allocations for complex codepoints (emoji/nerd-font glyphs we
    # render). jemalloc avoids that, so opt in and add the lib to buildInputs.
    mkTmux = pkgs:
      pkgs.tmux.overrideAttrs (old: {
        version = "next-3.8";
        src = inputs.tmux-upstream;
        configureFlags =
          old.configureFlags
          ++ ["--disable-asan"]
          ++ pkgs.lib.optionals pkgs.stdenv.isDarwin ["--enable-jemalloc"];
        buildInputs = old.buildInputs ++ pkgs.lib.optionals pkgs.stdenv.isDarwin [pkgs.jemalloc];
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
        # (non-recursive), so the default `picker` derivation never exercises the
        # nested packages under agentdetect/ (debounce/manifest/screen/statefile)
        # or remotebridge/ (daemon/wire/controlmode/render) — where the mirror
        # engine, the pane diff, the ctl verb table and the focus state machine
        # live. Both run here in one derivation: as two, all nine subPackages got
        # compiled twice over the same source, the most expensive thing in a CI
        # run. -race on remotebridge because the mirror engine touches mirror
        # state from a second goroutine (M2.3).
        # Reused (not just for its checkPhase) by the remote bridge integration
        # checks below, which need its binaries prebuilt and offline.
        pickerChecked =
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
              go test -race ./remotebridge/...
              runHook postCheck
            '';
          });
      in {
        # Not a check: the hook closure (python + every nix/shell linter, ~1200
        # store paths) is the largest fetch in the repo, and as a check it was
        # paid on both CI matrix legs. It moves to `packages.lint`, built once.
        # The devShell shellHook below still installs the hooks locally.
        pre-commit.check.enable = false;

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

          enrich-budget-tests =
            pkgs.runCommand "enrich-budget-tests" {
              # git: the pass groups windows by `git rev-parse --git-common-dir`,
              # so the fixture needs a real repo.
              nativeBuildInputs = [pkgs.bats pkgs.jq pkgs.coreutils pkgs.git];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/enrich-budget.bats
              touch $out
            '';

          reflow-tests =
            pkgs.runCommand "reflow-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/reflow.bats
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

          codex-status-hooks-tests =
            pkgs.runCommand "codex-status-hooks-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gawk pkgs.gnused];
            } ''
              cp -r ${./tests} tests
              mkdir modules
              cp ${./modules/home-manager.nix} modules/home-manager.nix
              bats tests/codex-status-hooks.bats
              touch $out
            '';

          startup-session-tests =
            pkgs.runCommand "startup-session-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused];
            } ''
              cp -r ${./tests} tests
              mkdir modules
              cp ${./modules/home-manager.nix} modules/home-manager.nix
              bats tests/startup-session.bats
              touch $out
            '';

          cursor-status-hooks-tests =
            pkgs.runCommand "cursor-status-hooks-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.jq];
            } ''
              cp -r ${./tests} tests
              cp -r ${./scripts} scripts
              mkdir modules
              cp ${./modules/home-manager.nix} modules/home-manager.nix
              bats tests/cursor-status-hooks.bats
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

          # Deliberately no LANG/LC_ALL. Nothing in these suites asserts a
          # character count or a display width — the multibyte values are only
          # ever compared as bytes — and C.UTF-8 does not exist on darwin, where
          # bash's setlocale warning lands on stderr. bats folds stderr into
          # $output, so pinning a locale here buys nothing and breaks the
          # center's line-count assertions on macOS.
          notify-router-tests =
            pkgs.runCommand "notify-router-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/notify-router.bats
              touch $out
            '';

          notify-producers-tests =
            pkgs.runCommand "notify-producers-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/notify-producers.bats
              touch $out
            '';

          notify-center-tests =
            pkgs.runCommand "notify-center-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/notify-center.bats
              touch $out
            '';

          # The bell/activity WIRING, asserted on the generated conf rather than
          # by inspection: hooks present at the free [20] index, both commands
          # pinned to a store path (a bare name resolves against the tmux
          # server's frozen PATH and would make prefix+r an incomplete deploy),
          # both passing #{window_id} (never #{session_id} — run-shell's sh -c
          # re-expands a leading $), matching -gu clears for reload idempotence,
          # and the n bind running a store-path center.
          #
          # Two things here are the ONLY automated coverage of their failure mode
          # and must not be softened:
          #   * hook ORDER — a bare `set-hook -gu alert-bell` clears every index,
          #     so a setter above it is erased on every load. Presence greps pass
          #     either way; only the line-number comparison catches it.
          #   * @notify@ SUBSTITUTION — both bats suites override the seam via
          #     LZTMUX_NOTIFY_BIN, so a placeholder-name drift would build clean,
          #     test green, and never notify in production. The store paths are
          #     bound straight out of tmuxConfig, so nothing is globbed or guessed.
          notify-conf-assertions =
            pkgs.runCommand "notify-conf-assertions" {
              nativeBuildInputs = [pkgs.gnugrep pkgs.coreutils];
              CONF = tmuxConfig.tmuxConf;
              CSU = "${tmuxConfig.script.claude-status-update}/bin/claude-status-update";
              PRE = "${tmuxConfig.script.tmux-pr-enrich}/bin/tmux-pr-enrich";
            } ''
              grep -q 'set-hook -g alert-bell\[20\]' "$CONF"
              grep -q 'set-hook -g alert-activity\[20\]' "$CONF"
              grep -q 'set-hook -gu alert-bell' "$CONF"
              grep -q 'set-hook -gu alert-activity' "$CONF"
              grep -E 'alert-bell\[20\].*/nix/store/[^ ]*/bin/lztmux-notify .*--window #\{window_id\}' "$CONF"
              grep -E 'alert-activity\[20\].*/nix/store/[^ ]*/bin/lztmux-notify .*--window #\{window_id\}' "$CONF"
              grep -E 'bind-key n display-popup -E .*/nix/store/[^ ]*/bin/lztmux-notify-center' "$CONF"

              # ORDER, not just presence: the clear must precede the setter, or
              # every config load (fresh server AND prefix+r) erases the hook.
              bell_clear=$(grep -n 'set-hook -gu alert-bell' "$CONF" | head -1 | cut -d: -f1)
              bell_set=$(grep -n 'alert-bell\[20\]' "$CONF" | head -1 | cut -d: -f1)
              [ "$bell_clear" -lt "$bell_set" ]
              act_clear=$(grep -n 'set-hook -gu alert-activity' "$CONF" | head -1 | cut -d: -f1)
              act_set=$(grep -n 'alert-activity\[20\]' "$CONF" | head -1 | cut -d: -f1)
              [ "$act_clear" -lt "$act_set" ]

              # The producer seam actually resolved to the router's store path.
              grep -qE '/nix/store/[^ ]*/bin/lztmux-notify' "$CSU"
              grep -qE '/nix/store/[^ ]*/bin/lztmux-notify' "$PRE"
              ! grep -q '@notify@' "$CSU"
              ! grep -q '@notify@' "$PRE"

              # monitor-activity stays off by design (a working Claude pane would
              # be a continuous activity event), and monitor-bell is already on.
              ! grep -q 'set -g monitor-activity on' "$CONF"
              ! grep -q 'set -g monitor-bell' "$CONF"
              touch $out
            '';

          notify-bell-integration-tests =
            pkgs.runCommand "notify-bell-integration-tests" {
              # mkTmux, not pkgs.tmux: the hook behavior this pins must be the
              # tmux that actually ships.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils (mkTmux pkgs)];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              export HOME=$TMPDIR
              bats tests/notify-bell-integration.bats
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

          issue-stamp-tests =
            pkgs.runCommand "issue-stamp-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/issue-stamp.bats
              touch $out
            '';

          enrich-command-tests =
            pkgs.runCommand "enrich-command-tests" {
              # git: the test derives worktree/branch from a real repo it builds
              # in $HOME (like reconcile-tests).
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.git];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/enrich-command.bats
              touch $out
            '';

          update-icons-enrich-trigger-tests =
            pkgs.runCommand "update-icons-enrich-trigger-tests" {
              # tmux: drives a private, config-less server (like reflow-fanout-tests);
              # git: builds a real repo in $HOME to exercise a real branch transition.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.git pkgs.tmux];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/update-icons-enrich-trigger.bats
              touch $out
            '';

          worktree-match-tests =
            pkgs.runCommand "worktree-match-tests" {
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gawk pkgs.gnugrep];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/worktree-match.bats
              touch $out
            '';

          worktree-match-integration-tests =
            pkgs.runCommand "worktree-match-integration-tests" {
              # mkTmux, not pkgs.tmux: assert against the tmux that actually ships.
              # No version divergence is known here (both report window options on
              # pane rows); the derivation is already built for the m2 check anyway.
              # Deliberately no LANG/LC_ALL: the sandbox's stripped locale is the
              # hostile case. tmux rewrites non-printable bytes to "_" without
              # UTF-8, which is why the -F format is "|"-delimited rather than
              # tab-delimited — pinning a UTF-8 locale here would hide a
              # regression back to a tab everywhere except the one test that
              # overrides the locale itself.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gawk (mkTmux pkgs)];
            } ''
              cp -r ${./tests} tests
              export HOME=$TMPDIR
              bats tests/worktree-match-integration.bats
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

          picker-go-tests = pickerChecked;

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

          tmux-next38-readiness-tests =
            pkgs.runCommand "tmux-next38-readiness-tests" {
              # mkTmux via the wrapped default package: exercise the same
              # generated config, plugin store paths, and PATH wrapper users run.
              nativeBuildInputs = [pkgs.bash pkgs.bats pkgs.coreutils pkgs.gawk pkgs.gnugrep pkgs.gnused];
              TMUX_BIN = "${tmuxConfig.tmux-wrapped}/bin/tmux";
              LANG = "C.UTF-8";
              LC_ALL = "C.UTF-8";
            } ''
              cp -r ${./tests} tests
              export HOME=$TMPDIR/home
              mkdir -p "$HOME"
              bats tests/tmux-next38-readiness.bats
              touch $out
            '';

          remote-tests =
            pkgs.runCommand "remote-tests" {
              # bash: the cold-start cases run the launcher through an explicit
              # interpreter (no /usr/bin/env in the sandbox).
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep pkgs.bash];
            } ''
              cp -r ${./scripts} scripts
              cp -r ${./tests} tests
              bats tests/remote.bats
              bats tests/remote-cold-start.bats
              touch $out
            '';

          remote-bridge-integration-tests =
            pkgs.runCommand "remote-bridge-integration-tests" {
              # tmux: same private, config-less server pattern as the other
              # integration tests. The bridge binary is prebuilt via the
              # vendored buildGoModule (pickerChecked) so this check never
              # invokes `go build` — a non-FOD sandbox has no network.
              # util-linux provides `script`, which the real-tty case uses to
              # give the bridge a pty (so refresh-client + real cursor fire).
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep pkgs.tmux pkgs.util-linux];
              BRIDGE = "${pickerChecked}/bin/lztmux-remote-bridge";
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
              # buildGoModule (pickerChecked) so this check never invokes
              # `go build` — a non-FOD sandbox has no network.
              #
              # Uses the pinned next-3.8 tmux (mkTmux), not pkgs.tmux (3.7b):
              # the M2 bridge is a next-3.8 effort and its control-mode
              # notification behavior (e.g. %window-close on kill-window) differs
              # from 3.7b, so the mirror is exercised against the version it —
              # and production, local + remote — actually runs.
              nativeBuildInputs = [pkgs.bats pkgs.coreutils pkgs.gnused pkgs.gnugrep (mkTmux pkgs)];
              DAEMON = "${pickerChecked}/bin/lztmux-remote-bridge-daemon";
              RENDERER = "${pickerChecked}/bin/lztmux-remote-bridge-renderer";
              # M2.3 structural input: the tests drive ctl straight at the
              # daemon's socket, since these vanilla -L servers carry no
              # lazytmux keybindings for a gate to intercept.
              CTL = "${pickerChecked}/bin/lztmux-remote-bridge-ctl";
            } ''
              cp -r ${./tests} tests
              export HOME=$TMPDIR
              bats tests/remote-m2-integration.bats
              touch $out
            '';

          # The gate itself, not a keypress: remote-m2-integration-tests above
          # drives vanilla -L servers with no lazytmux keybindings for a gate to
          # intercept (comment on that check), so it can only exercise the
          # `carousel` ctl verb directly. Proving both bind I branches exist in
          # the REAL generated config is what actually covers carouselBind.
          bridge-carousel-bind-assertions =
            pkgs.runCommand "bridge-carousel-bind-assertions" {
              nativeBuildInputs = [pkgs.gnugrep];
              CONF = tmuxConfig.tmuxConf;
            } ''
              grep -qE 'bind I if-shell -F .*@bridge_win' "$CONF"
              touch $out
            '';
        };

        packages = {
          default = tmuxConfig.tmux-wrapped;
          # Runs every pre-commit hook over the tree (see pre-commit.check above).
          lint = config.pre-commit.settings.run;
          # Stable store path for the Codex managed-hook config (lazytmux#140
          # Task 3) to point its `command` at, independent of the tmux wrapper.
          codex-relaunch-stamp = tmuxConfig.script.codex-relaunch-stamp;
        };
      };

      flake = {
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
