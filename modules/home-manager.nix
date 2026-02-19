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
