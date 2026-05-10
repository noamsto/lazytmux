{
  config,
  lib,
  pkgs,
  tmux-state-pkg ? null,
  ...
}: let
  cfg = config.programs.lazytmux;

  # Per-emulator defaults. terminfoPath uses an if-expression (not lib.optionalString)
  # because Nix evaluates function arguments strictly — the string interpolation would
  # force pkgs.<name> even when the condition is false.
  emulatorDefaults = {
    ghostty = {
      available = pkgs ? ghostty;
      term = "xterm-ghostty";
      termProgram = "ghostty";
      terminfoPath =
        if pkgs ? ghostty
        then "${pkgs.ghostty}/share/terminfo"
        else null;
    };
    kitty = {
      available = pkgs ? kitty;
      term = "xterm-kitty";
      termProgram = "kitty";
      terminfoPath =
        if pkgs ? kitty
        then "${pkgs.kitty}/share/terminfo"
        else null;
    };
  };

  # Resolved emulator config (null when emulator = null)
  emulatorCfg =
    if cfg.startupSession.terminal.emulator != null
    then emulatorDefaults.${cfg.startupSession.terminal.emulator} or null
    else null;

  # Effective env values: emulator preset wins over manual options
  effectiveTerm =
    if emulatorCfg != null
    then emulatorCfg.term
    else cfg.startupSession.terminal.term;

  effectiveTermProgram =
    if emulatorCfg != null
    then emulatorCfg.termProgram
    else cfg.startupSession.terminal.termProgram;

  effectiveTerminfoPath =
    if emulatorCfg != null
    then emulatorCfg.terminfoPath
    else cfg.startupSession.terminal.terminfoPath;

  # tmux-state binary path (null when persist is disabled or package missing).
  # Resolving here keeps the conditional logic out of the conf string itself.
  tmuxStateBin =
    if cfg.persist.enable && cfg.persist.package != null
    then "${cfg.persist.package}/bin/tmux-state"
    else null;

  # Persist (tmux-state) tmux.conf snippet. Empty string when disabled — appended
  # verbatim to the generated tmux.conf via extraConfText. The hooks fire
  # `tmux-state save` on structural change and `capture-event` on close so the
  # daemon can correlate (window closed at T, last save at T-2s ⇒ replay row).
  tmuxStateConf =
    if tmuxStateBin == null
    then ""
    else ''

      # === tmux-state (Phase 2a, opt-in via programs.lazytmux.persist) ===
      # Use index [99] so persist hooks coexist with lazytmux's index-0 hooks
      # (e.g. tmux-reflow-windows on window-unlinked). Same pattern as
      # claude-status-update + tmux-fingers in config/tmux.conf.nix.
      set-hook -g session-created[99]       'run-shell -b "${tmuxStateBin} save --reason=hook:session-created"'
      set-hook -g window-linked[99]         'run-shell -b "${tmuxStateBin} save --reason=hook:window-linked"'
      set-hook -g client-detached[99]       'run-shell -b "${tmuxStateBin} save --reason=hook:client-detached"'

      set-hook -g pane-died[99]             'run-shell -b "${tmuxStateBin} capture-event pane-died          --pane=#{hook_pane}    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g window-unlinked[99]       'run-shell -b "${tmuxStateBin} capture-event window-unlinked    --window=#{hook_window} --session=#{hook_session}"'
      set-hook -g session-closed[99]        'run-shell -b "${tmuxStateBin} capture-event session-closed     --session=#{hook_session}"'

      set-hook -g window-renamed[99]        'run-shell -b "${tmuxStateBin} index-update --session=#{hook_session}"'
      set-hook -g window-layout-changed[99] 'run-shell -b "${tmuxStateBin} index-update --session=#{hook_session}"'

      ${lib.optionalString (cfg.persist.restoreMode == "auto") ''
        run-shell -b '${tmuxStateBin} restore --auto'
      ''}

      bind   u    run-shell '${tmuxStateBin} undo --pop'
      # FZF_DEFAULT_OPTS may contain --tmux=... (fzf's own popup), which crashes
      # when tmux-state's fzf invocation is already inside display-popup -E.
      # Strip it for these two bindings.
      bind   U    display-popup -E -w 90% -h 85% -b rounded -T " Close events " 'env -u FZF_DEFAULT_OPTS ${tmuxStateBin} pick --kind=close'
      bind   R    display-popup -E -w 90% -h 85% -b rounded -T " Snapshots "     'env -u FZF_DEFAULT_OPTS ${tmuxStateBin} pick --kind=snapshot'
      bind C-s    run-shell '${tmuxStateBin} save --reason=keybinding'
    '';

  tmuxConfig = import ../config/tmux.conf.nix {
    inherit pkgs lib;
    extraProcessIcons = cfg.processIcons;
    # Pass the resolved TERM string so tmux.conf can derive terminal-features
    # without needing to re-encode emulator names. Null when no preset is active.
    terminalTerm =
      if emulatorCfg != null
      then emulatorCfg.term
      else null;
    extraConfText = tmuxStateConf;
  };
in {
  options.programs.lazytmux = {
    enable = lib.mkEnableOption "lazytmux - opinionated tmux configuration";

    processIcons = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {};
      example = lib.literalExpression ''{ "my-app" = "⚡"; }'';
      description = "Extra process name → icon mappings. Overrides built-in defaults on collision.";
    };

    worktrunk = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Whether to install worktrunk and configure tmux integration hooks";
      };
    };

    persist = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Whether to enable tmux-state persistence (snapshots, undo, auto-restore).
          Replaces tmux-resurrect/tmux-continuum.
        '';
      };

      saveInterval = lib.mkOption {
        type = lib.types.int;
        default = 60;
        description = "Seconds between periodic saves (systemd timer cadence).";
      };

      restoreMode = lib.mkOption {
        type = lib.types.enum ["auto" "interactive" "off"];
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

    skills = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to install Claude Code skills into ~/.claude/skills";
      };
    };

    opencode = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to install OpenCode status plugin into ~/.config/opencode/plugin";
      };
    };

    claudeIntegration = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Expose claude-status-update and claude-status on PATH via
          home.packages. Needed when Claude Code hooks (or any tool that
          calls them by bare name) run in a shell that doesn't inherit the
          tmux wrapper's PATH — e.g. a fish login shell, which resets PATH,
          or a direnv-loaded devshell.
        '';
      };
    };

    popupTools = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [pkgs.sesh pkgs.lazygit pkgs.yazi pkgs.btop];
      defaultText = lib.literalExpression "[pkgs.sesh pkgs.lazygit pkgs.yazi pkgs.btop]";
      description = ''
        Tools installed via home.packages so popup keybindings
        (prefix+K/S → sesh, prefix+g → lazygit, prefix+b → btop,
        prefix+y → yazi) resolve in shells that don't inherit the tmux
        wrapper's PATH prepends — e.g. fish login shells opened by
        display-popup, or direnv-loaded devshells.

        Set to [] to opt out entirely, or drop individual entries if you
        install those tools elsewhere (home-manager errors if two
        different derivations install the same file).
      '';
    };

    startupSession = {
      enable = lib.mkEnableOption "systemd service to start a tmux session on login";

      name = lib.mkOption {
        type = lib.types.str;
        default = "main";
        description = "Name of the tmux session to create on login";
      };

      directory = lib.mkOption {
        type = lib.types.str;
        default = "~";
        description = "Starting directory for the session";
      };

      terminal = {
        emulator = lib.mkOption {
          type = lib.types.nullOr (lib.types.enum ["ghostty" "kitty"]);
          default = null;
          description = ''
            Terminal emulator preset. When set, auto-configures TERM,
            TERM_PROGRAM, TERMINFO, and tmux terminal-features/overrides
            for the chosen emulator. The emulator package must be available
            in pkgs. Set to null to configure terminal options manually.
          '';
          example = "ghostty";
        };

        term = lib.mkOption {
          type = lib.types.str;
          default = "xterm-256color";
          description = "TERM value for the tmux session (ignored when emulator is set)";
        };

        colorterm = lib.mkOption {
          type = lib.types.str;
          default = "truecolor";
          description = "COLORTERM value";
        };

        termProgram = lib.mkOption {
          type = lib.types.str;
          default = "";
          description = "TERM_PROGRAM value (ignored when emulator is set)";
        };

        terminfoPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Path to terminfo directory (ignored when emulator is set)";
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    assertions =
      lib.optional (
        cfg.startupSession.terminal.emulator
        != null
        && !emulatorCfg.available
      ) {
        assertion = false;
        message = ''
          programs.lazytmux.startupSession.terminal.emulator = "${cfg.startupSession.terminal.emulator}"
          but pkgs.${cfg.startupSession.terminal.emulator} is not available.
          Add it to your packages or set terminal.emulator = null and configure manually.
        '';
      };

    home = {
      packages =
        [tmuxConfig.tmux-wrapped]
        ++ lib.optionals cfg.worktrunk.enable [pkgs.worktrunk]
        ++ lib.optionals (cfg.persist.enable && cfg.persist.package != null) [cfg.persist.package]
        ++ lib.optionals cfg.claudeIntegration.enable [
          tmuxConfig.script.claude-status-update
          tmuxConfig.script.claude-status
        ]
        ++ cfg.popupTools;

      file =
        lib.optionalAttrs cfg.skills.enable (
          lib.mapAttrs' (name: _: {
            name = ".claude/skills/${name}";
            value.source = ../skills/${name};
          }) (builtins.readDir ../skills)
        )
        // lib.optionalAttrs cfg.opencode.enable {
          ".config/opencode/plugin/opencode-status.ts".source = ../plugins/opencode-status.ts;
        };

      # Reload tmux config + reflow all sessions after profile switch.
      # The config embeds full /nix/store paths, so reloading makes the running
      # server use new script versions without restart. Reflow regenerates
      # status-format lines for all sessions with the new reflow script.
      # Run after restoreTheme (which sources the config and sets theme vars).
      # We only need to: ensure config is loaded, then reflow all sessions.
      activation.reloadTmux = lib.hm.dag.entryAfter ["writeBoundary" "restoreTheme"] ''
        TMUX=${pkgs.tmux}/bin/tmux
        REFLOW=${tmuxConfig.script.tmux-reflow-windows}/bin/tmux-reflow-windows
        if $TMUX info &>/dev/null 2>&1; then
          # Source config (restoreTheme may have already done this, but it's
          # idempotent and handles the case where restoreTheme doesn't exist)
          $TMUX source-file ${tmuxConfig.tmuxConf} || true
          # Wait for async run-shell plugin commands to finish
          sleep 1
          # Reflow ALL sessions
          WIDTH=$($TMUX list-clients -F '#{client_width}' 2>/dev/null | head -1)
          WIDTH=''${WIDTH:-200}
          while read -r sess; do
            [ -n "$sess" ] && "$REFLOW" "$sess" "$WIDTH" || true
          done < <($TMUX list-sessions -F '#{session_name}' 2>/dev/null)
        fi
      '';
    };

    xdg.configFile = {
      "worktrunk/config.toml" = lib.mkIf cfg.worktrunk.enable {
        text = ''
          worktree-path = "{{ repo_path }}/.worktrees/{{ branch | sanitize }}"

          [post-switch]
          tmux = """
          [ -z "$TMUX" ] && exit 0
          SESSION=$(tmux display-message -p '#{session_name}')
          WIN=$(tmux list-windows -t "$SESSION" -F '#{window_index}\t#{@worktree}\t#{pane_current_path}' \
            | awk -F'\t' '$2 == "{{ worktree_path }}" || $3 == "{{ worktree_path }}" { print $1; exit }')
          if [ -n "$WIN" ]; then
            tmux select-window -t "$SESSION:$WIN"
          else
            tmux new-window -a -t "$SESSION" -c "{{ worktree_path }}"
            tmux set-option -t "$SESSION" -w @worktree "{{ worktree_path }}"
            tmux set-option -t "$SESSION" -w @branch "{{ branch | sanitize }}"
          fi
          """
          zoxide = """
          command -v zoxide >/dev/null 2>&1 && zoxide add "{{ worktree_path }}"
          """

          [post-remove]
          tmux = """
          [ -z "$TMUX" ] && exit 0
          SESSION=$(tmux display-message -p '#{session_name}')
          WIN=$(tmux list-windows -t "$SESSION" -F '#{window_index}\t#{@worktree}\t#{pane_current_path}' \
            | awk -F'\t' '$2 == "{{ worktree_path }}" || $3 == "{{ worktree_path }}" { print $1; exit }')
          [ -n "$WIN" ] && tmux kill-window -t "$SESSION:$WIN" 2>/dev/null || true
          """
        '';
      };

      # Stable config path so theme-toggle and prefix+r can source it.
      # The actual config is in /nix/store; this symlink always points to the latest.
      "tmux/tmux.conf".source = tmuxConfig.tmuxConf;
    };

    # Never restart on switch — killing the tmux server destroys all sessions and history.
    # The startup script is a stable wrapper that resolves tmux via ~/.nix-profile/bin,
    # so the unit file doesn't change when lazytmux is updated (preventing sd-switch restart).
    systemd.user = let
      persistEnabled = cfg.persist.enable && cfg.persist.package != null;

      # Stable script that doesn't embed nix store paths — prevents unit file churn
      tmux-startup-script = pkgs.writeShellScript "tmux-startup" ''
        # Resolve tmux from user profile (avoids hardcoded store paths in the unit)
        # Try per-user profile (NixOS/home-manager) then nix-profile (nix-env)
        for candidate in "/etc/profiles/per-user/$USER/bin/tmux" "$HOME/.nix-profile/bin/tmux"; do
          if [ -x "$candidate" ]; then
            TMUX_BIN="$candidate"
            break
          fi
        done
        if [ -z "''${TMUX_BIN:-}" ]; then
          echo "tmux not found in /etc/profiles/per-user/$USER/bin or ~/.nix-profile/bin" >&2
          exit 1
        fi

        SESSION=${lib.escapeShellArg cfg.startupSession.name}

        # Exact-match check (`=name`) — default is prefix match, which would
        # incorrectly skip creation if e.g. `foo-bar` existed when SESSION=foo.
        if "$TMUX_BIN" has-session -t "=$SESSION" 2>/dev/null; then
          echo "tmux session $SESSION already running, skipping"
          exit 0
        fi

        # Try to create the session. If creation fails but the session now
        # exists anyway, something else won the race to create it (e.g.
        # tmux-state auto-restore on server start). Treat that as success.
        if "$TMUX_BIN" new -s "$SESSION" -c ${lib.escapeShellArg cfg.startupSession.directory} -d; then
          exit 0
        fi

        if "$TMUX_BIN" has-session -t "=$SESSION" 2>/dev/null; then
          echo "tmux session $SESSION exists (created by another source), continuing"
          exit 0
        fi

        exit 1
      '';
    in {
      services = {
        tmux-startup = lib.mkIf cfg.startupSession.enable {
          Unit = {
            Description = "Start tmux server on login";
            After = ["graphical-session.target"];
            Wants = ["graphical-session.target"];
          };

          Service = {
            Type = "forking";
            ExecStartPre = "${lib.getExe' pkgs.systemd "systemctl"} --user import-environment DISPLAY WAYLAND_DISPLAY XDG_SESSION_TYPE XDG_SESSION_DESKTOP XDG_CURRENT_DESKTOP COLORTERM TERM TERMINFO";
            ExecStart = "${tmux-startup-script}";
            RemainAfterExit = true;
            TimeoutStopSec = "5s";
            Environment =
              [
                "COLORTERM=${cfg.startupSession.terminal.colorterm}"
                "TERM=${effectiveTerm}"
                "TMUX_TMPDIR=%t"
              ]
              ++ lib.optionals (effectiveTermProgram != "") [
                "TERM_PROGRAM=${effectiveTermProgram}"
              ]
              ++ lib.optionals (effectiveTerminfoPath != null) [
                "TERMINFO=${effectiveTerminfoPath}"
              ];
          };

          Install = {
            WantedBy = ["graphical-session.target"];
          };
        };

        # Periodic snapshot — fires `tmux-state save --reason=timer` so the
        # daemon has a recent baseline even between structural-change hooks.
        lazytmux-state-save = lib.mkIf persistEnabled {
          Unit.Description = "Save tmux-state snapshot";
          Service = {
            Type = "oneshot";
            ExecStart = "${cfg.persist.package}/bin/tmux-state save --reason=timer";
          };
        };

        # Weekly GC sweeps orphaned scrollback files (panes whose snapshot row
        # was already pruned). Cheap to run; safe to skip on missed firings.
        lazytmux-state-gc = lib.mkIf persistEnabled {
          Unit.Description = "tmux-state garbage collection";
          Service = {
            Type = "oneshot";
            ExecStart = "${cfg.persist.package}/bin/tmux-state gc";
          };
        };
      };

      timers = {
        lazytmux-state-save = lib.mkIf persistEnabled {
          Unit.Description = "Periodic tmux-state snapshot";
          Timer = {
            OnBootSec = "2min";
            OnUnitActiveSec = "${toString cfg.persist.saveInterval}s";
            Unit = "lazytmux-state-save.service";
          };
          Install.WantedBy = ["timers.target"];
        };

        lazytmux-state-gc = lib.mkIf persistEnabled {
          Unit.Description = "tmux-state GC (orphan scrollback files)";
          Timer = {
            OnCalendar = "weekly";
            Unit = "lazytmux-state-gc.service";
          };
          Install.WantedBy = ["timers.target"];
        };
      };
    };
  };
}
