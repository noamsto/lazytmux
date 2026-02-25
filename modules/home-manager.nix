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

    skills = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to install Claude Code skills into ~/.claude/skills";
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

    home.file = lib.mkIf cfg.skills.enable (
      lib.mapAttrs' (name: _: {
        name = ".claude/skills/${name}";
        value.source = ../skills/${name};
      }) (builtins.readDir ../skills)
    );

    # Plugin hardcodes ~/.config/tmux/tmux-nerd-font-window-name.yml
    xdg.configFile."tmux/tmux-nerd-font-window-name.yml".source = tmuxConfig.nerdFontConfig;

    # Fish completions for wt
    xdg.configFile."fish/completions/wt.fish" = lib.mkIf cfg.wt.enable {
      text = ''
        # Completions for wt (worktree manager)

        # Flags
        complete -c wt -f -s y -l yes -d 'Skip confirmation prompts'
        complete -c wt -f -s q -l quiet -d 'Quiet mode (only output path)'
        complete -c wt -f -s n -l no-switch -d 'Skip tmux window operations'

        # Subcommands
        complete -c wt -f -n '__fish_use_subcommand' -a 'list' -d 'List worktrees'
        complete -c wt -f -n '__fish_use_subcommand' -a 'ls' -d 'List worktrees (alias)'
        complete -c wt -f -n '__fish_use_subcommand' -a 'remove' -d 'Remove worktree + window'
        complete -c wt -f -n '__fish_use_subcommand' -a 'rm' -d 'Remove worktree + window (alias)'
        complete -c wt -f -n '__fish_use_subcommand' -a 'clean' -d 'Remove merged worktrees'
        complete -c wt -f -n '__fish_use_subcommand' -a 'prune' -d 'Remove merged worktrees (alias)'
        complete -c wt -f -n '__fish_use_subcommand' -a 'z' -d 'Fuzzy find worktree'
        complete -c wt -f -n '__fish_use_subcommand' -a 'main' -d 'Switch to main worktree'
        complete -c wt -f -n '__fish_use_subcommand' -a 'help' -d 'Show help'

        # Complete existing worktree branches
        function __wt_list_worktree_branches
            if git rev-parse --git-dir >/dev/null 2>&1
                set -l repo_root (git rev-parse --show-toplevel)
                git -C "$repo_root" worktree list 2>/dev/null | while read -l line
                    if string match -rq '\[(.+)\]$' -- $line
                        set -l branch (string match -r '\[(.+)\]$' -- $line)[2]
                        echo $branch
                    end
                end
            end
        end

        # Complete all branches (for smart mode: existing worktrees + available branches)
        function __wt_list_all_branches
            if git rev-parse --git-dir >/dev/null 2>&1
                set -l repo_root (git rev-parse --show-toplevel)

                # Existing worktrees first (likely what user wants to switch to)
                __wt_list_worktree_branches

                # Then available branches (not in worktrees)
                set -l used_branches (__wt_list_worktree_branches)

                for branch in (git -C "$repo_root" branch --format='%(refname:short)' 2>/dev/null)
                    if not contains $branch $used_branches
                        echo $branch
                    end
                end

                for branch in (git -C "$repo_root" branch -r --format='%(refname:short)' 2>/dev/null)
                    set -l short_name (string replace -r '^origin/' "" -- $branch)
                    if test "$short_name" != "HEAD"; and not contains $short_name $used_branches
                        echo $short_name
                    end
                end | sort -u
            end
        end

        # Branch completions for remove and z (only existing worktrees)
        complete -c wt -f -n '__fish_seen_subcommand_from remove rm z' -a '(__wt_list_worktree_branches)'

        # Branch completions for smart mode (all branches, shown at top level)
        complete -c wt -f -n '__fish_use_subcommand' -a '(__wt_list_all_branches)'
      '';
    };

    # Stable config path so theme-toggle and prefix+r can source it.
    # The actual config is in /nix/store; this symlink always points to the latest.
    xdg.configFile."tmux/tmux.conf".source = tmuxConfig.tmuxConf;

    # Reload tmux config + reflow all sessions after profile switch.
    # The config embeds full /nix/store paths, so reloading makes the running
    # server use new script versions without restart. Reflow regenerates
    # status-format lines for all sessions with the new reflow script.
    # Run after restoreTheme (which sources the config and sets theme vars).
    # We only need to: ensure config is loaded, then reflow all sessions.
    home.activation.reloadTmux = lib.hm.dag.entryAfter ["writeBoundary" "restoreTheme"] ''
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
