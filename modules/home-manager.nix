{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.programs.lazytmux;
  tmuxConfig = import ../config/tmux.conf.nix {inherit pkgs lib;};
  wtPkg = import ../wt {inherit pkgs;};
in {
  options.programs.lazytmux = {
    enable = lib.mkEnableOption "lazytmux - opinionated tmux configuration";

    wt = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to install the wt (git worktree manager) tool";
      };
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
        term = lib.mkOption {
          type = lib.types.str;
          default = "xterm-256color";
          description = "TERM value for the tmux session";
        };

        colorterm = lib.mkOption {
          type = lib.types.str;
          default = "truecolor";
          description = "COLORTERM value";
        };

        termProgram = lib.mkOption {
          type = lib.types.str;
          default = "";
          description = "TERM_PROGRAM value (e.g. 'kitty')";
        };

        terminfoPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Path to terminfo directory (e.g. pkgs.kitty + '/share/terminfo')";
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages =
      [tmuxConfig.tmux-wrapped]
      ++ lib.optionals cfg.wt.enable [wtPkg];

    # Plugin hardcodes ~/.config/tmux/tmux-nerd-font-window-name.yml
    xdg.configFile."tmux/tmux-nerd-font-window-name.yml".source = tmuxConfig.nerdFontConfig;

    # Stable config path so theme-toggle and prefix+r can source it.
    # The actual config is in /nix/store; this symlink always points to the latest.
    xdg.configFile."tmux/tmux.conf".source = tmuxConfig.tmuxConf;

    # Reload tmux config + reflow all sessions after profile switch.
    # The config embeds full /nix/store paths, so reloading makes the running
    # server use new script versions without restart. Reflow regenerates
    # status-format lines for all sessions with the new reflow script.
    home.activation.reloadTmux = lib.hm.dag.entryAfter ["writeBoundary"] ''
      TMUX=${pkgs.tmux}/bin/tmux
      REFLOW=${tmuxConfig.script.tmux-reflow-windows}/bin/tmux-reflow-windows
      if $TMUX info &>/dev/null 2>&1; then
        $TMUX source-file ${tmuxConfig.tmuxConf} || true
        # Brief pause to let run-shell commands within the config finish
        sleep 0.5
        # Reflow ALL sessions (use client width from any attached client, or default 200)
        WIDTH=$($TMUX list-clients -F '#{client_width}' 2>/dev/null | head -1)
        WIDTH=''${WIDTH:-200}
        while read -r sess; do
          [ -n "$sess" ] && "$REFLOW" "$sess" "$WIDTH" || true
        done < <($TMUX list-sessions -F '#{session_name}' 2>/dev/null)
      fi
    '';

    # Never restart on switch — killing the tmux server destroys all sessions and history.
    # The startup script is a stable wrapper that resolves tmux via ~/.nix-profile/bin,
    # so the unit file doesn't change when lazytmux is updated (preventing sd-switch restart).
    systemd.user.services.tmux-startup = lib.mkIf cfg.startupSession.enable (let
      # Stable script that doesn't embed nix store paths — prevents unit file churn
      tmux-startup-script = pkgs.writeShellScript "tmux-startup" ''
        # Resolve tmux from user profile (avoids hardcoded store paths in the unit)
        TMUX_BIN="$HOME/.nix-profile/bin/tmux"
        if [ ! -x "$TMUX_BIN" ]; then
          echo "tmux not found at $TMUX_BIN" >&2
          exit 1
        fi
        # Only start if not already running
        if "$TMUX_BIN" has-session 2>/dev/null; then
          echo "tmux server already running, skipping"
          exit 0
        fi
        exec "$TMUX_BIN" new -s ${lib.escapeShellArg cfg.startupSession.name} -c ${lib.escapeShellArg cfg.startupSession.directory} -d
      '';
    in {
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
            "TERM=${cfg.startupSession.terminal.term}"
            "TMUX_TMPDIR=%t"
          ]
          ++ lib.optionals (cfg.startupSession.terminal.termProgram != "") [
            "TERM_PROGRAM=${cfg.startupSession.terminal.termProgram}"
          ]
          ++ lib.optionals (cfg.startupSession.terminal.terminfoPath != null) [
            "TERMINFO=${cfg.startupSession.terminal.terminfoPath}"
          ];
      };

      Install = {
        WantedBy = ["graphical-session.target"];
      };
    });
  };
}
