{
  config,
  lib,
  pkgs,
  tmux-state-pkg ? null,
  carousel-toggle ? null,
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

      ${lib.optionalString (cfg.persist.restoreMode == "auto") ''
        run-shell -b '${tmuxStateBin} restore --auto'
      ''}

      bind   u    run-shell '${tmuxStateBin} undo --pop'
      # The picker is a bubbletea TUI (tmux-state >= 0.2.0); launching it through
      # the `env` binary breaks its TTY init and renders a blank popup, so invoke
      # it directly. (The old `env -u FZF_DEFAULT_OPTS` wrapper was only needed for
      # the fzf-based picker, which no longer exists.)
      bind   U    display-popup -E -w 90% -h 85% -b rounded -T " Close events " '${tmuxStateBin} pick --kind=close'
      bind   R    display-popup -E -w 90% -h 85% -b rounded -T " Snapshots "     '${tmuxStateBin} pick --kind=snapshot'
      bind C-s    run-shell '${tmuxStateBin} save --reason=keybinding'
    '';

  tmuxConfig = import ../config/tmux.conf.nix {
    inherit pkgs lib;
    inherit carousel-toggle;
    extraProcessIcons = cfg.processIcons;
    zoxideExclude = lib.concatStringsSep "," cfg.sessionPicker.zoxideExclude;
    inherit (cfg) prefix defaultShell;
    # Pass the resolved TERM string so tmux.conf can derive terminal-features
    # without needing to re-encode emulator names. Null when no preset is active.
    terminalTerm =
      if emulatorCfg != null
      then emulatorCfg.term
      else null;
    extraConfText = tmuxStateConf;
    enrichEnable = cfg.enrich.enable;
    enrichProviders = cfg.enrich.providers;
    enrichPrRefreshSeconds = cfg.enrich.prRefreshSeconds;
    enrichIcons = builtins.mapAttrs (_: v: builtins.replaceStrings ["#"] ["##"] v) cfg.enrich.icons;
  };

  inherit (pkgs.stdenv.hostPlatform) isLinux isDarwin;

  persistEnabled = cfg.persist.enable && cfg.persist.package != null;

  # Stable startup script shared by the Linux systemd service and the darwin
  # launchd agent. Resolves tmux from the user profile so the unit/plist never
  # embeds a nix store path (no churn on update).
  tmux-startup-script = pkgs.writeShellScript "tmux-startup" ''
    # Resolve tmux from user profile (avoids hardcoded store paths in the unit)
    # Try per-user profile (NixOS/home-manager/nix-darwin) then nix-profile (nix-env)
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

    # Expand %h and leading ~ to $HOME — tmux does NOT expand format strings
    # (or shell ~) in the -c argument; it passes the path directly to chdir.
    DIRECTORY=${lib.escapeShellArg cfg.startupSession.directory}
    DIRECTORY="''${DIRECTORY//%h/$HOME}"
    DIRECTORY="''${DIRECTORY/#~/$HOME}"

    # Exact-match check (`=name`) — default is prefix match, which would
    # incorrectly skip creation if e.g. `foo-bar` existed when SESSION=foo.
    if "$TMUX_BIN" has-session -t "=$SESSION" 2>/dev/null; then
      echo "tmux session $SESSION already running, skipping"
      exit 0
    fi

    # Try to create the session. If creation fails but the session now
    # exists anyway, something else won the race to create it (e.g.
    # tmux-state auto-restore on server start). Treat that as success.
    if "$TMUX_BIN" new -s "$SESSION" -c "$DIRECTORY" -d; then
      exit 0
    fi

    if "$TMUX_BIN" has-session -t "=$SESSION" 2>/dev/null; then
      echo "tmux session $SESSION exists (created by another source), continuing"
      exit 0
    fi

    exit 1
  '';
in {
  options.programs.lazytmux = {
    enable = lib.mkEnableOption "lazytmux - opinionated tmux configuration";

    processIcons = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {};
      example = lib.literalExpression ''{ "my-app" = "⚡"; }'';
      description = "Extra process name → icon mappings. Overrides built-in defaults on collision.";
    };

    prefix = lib.mkOption {
      type = lib.types.str;
      default = "`";
      example = "§";
      description = ''
        tmux prefix key (literal character). Defaults to backtick. On macOS ISO
        keyboards the otherwise-unused § key (left of 1) is a convenient prefix.
      '';
    };

    defaultShell = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/run/current-system/sw/bin/fish";
      description = ''
        Absolute path to the shell tmux spawns in new panes (tmux
        default-shell). When null, tmux uses $SHELL / the account shell. Set
        this when the login shell isn't reliably propagated to the tmux server
        — e.g. launchd-started servers on macOS capture a stale $SHELL, so
        panes open in /bin/zsh even after the account shell is changed.
      '';
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
          Behavior on tmux server start. "off" disables auto-restore (manual
          `prefix + R` still works). "auto" applies the smart filter and restores.
          "interactive" prompts via picker (not yet implemented — falls back to "off").
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

    enrich = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          PR + issue-tracker window enrichment: stamps tmux windows with the
          Linear/GitHub issue id and PR check-state for their worktree's branch,
          and adds `prefix + i` keybinds to open the issue/PR or force refresh.
        '';
      };

      providers = lib.mkOption {
        type = lib.types.listOf (lib.types.enum ["linear" "github"]);
        default = ["linear" "github"];
        description = "Issue-tracker providers, tried in priority order. First match wins.";
      };

      prRefreshSeconds = lib.mkOption {
        type = lib.types.ints.between 10 300;
        default = 30;
        description = "Background PR enrichment cadence in seconds (clamped 10–300).";
      };

      icons = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = {};
        example = lib.literalExpression ''{ linear = "<glyph>"; github = "<glyph>"; }'';
        description = ''
          Override enrichment icon glyphs (keys: linear, github, pending,
          success, failure, merged, conflict). Unset keys fall back to
          nerd-font defaults. Values must not contain '#' (tmux format escape).
        '';
      };
    };

    skills = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to install Claude Code skills into ~/.claude/skills. Disable when the lazytmux Claude Code plugin is installed (marketplace or --plugin-dir) — the plugin ships the same skills.";
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
        (prefix+g → lazygit, prefix+b → btop, prefix+y → yazi) resolve in
        shells that don't inherit the tmux wrapper's PATH prepends — e.g.
        fish login shells opened by display-popup, or direnv-loaded
        devshells. sesh has no binding anymore but stays for external
        `sesh connect` CLI workflows.

        Set to [] to opt out entirely, or drop individual entries if you
        install those tools elsewhere (home-manager errors if two
        different derivations install the same file).
      '';
    };

    sessionPicker = {
      zoxideExclude = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [".ssh" "/tmp/*"];
        example = [".ssh" "/tmp/*" "*/node_modules"];
        description = ''
          Patterns the session picker drops from its zoxide directory
          suggestions. A pattern matches when it equals the path, is an
          ancestor dir of it (subtree), or globs the full path or its
          basename — so ".ssh" hides any dir named .ssh, "/tmp/*" hides
          /tmp children, and "/home/you/Downloads" hides that subtree.
          Set to [] to suggest every zoxide dir.
        '';
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
        description = ''
          Starting directory for the session. Leading `~` and any `%h` are
          expanded to `$HOME` by the startup script before being passed to
          `tmux new -c` (tmux itself does not expand these in `-c`).
        '';
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

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
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
          ++ lib.optionals cfg.enrich.enable [
            tmuxConfig.script.tmux-issue-stamp
            tmuxConfig.script.tmux-issue-stamp-linear
            tmuxConfig.script.tmux-issue-stamp-github
            tmuxConfig.script.tmux-pr-enrich
          ]
          ++ cfg.popupTools;

        file =
          lib.optionalAttrs cfg.skills.enable (
            lib.mapAttrs' (name: _: {
              name = ".claude/skills/${name}";
              value.source = ../claude-plugin/skills/${name};
            }) (builtins.readDir ../claude-plugin/skills)
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
            # Sibling dir, NOT nested under the repo: a worktree inside the repo
            # working tree sits under watchman's already-watched root, so a fresh
            # npm install floods fsevents and watchman drops events — breaking
            # Metro module resolution in RN/Expo worktrees (see issue #41). A
            # sibling becomes its own watch root with a clean crawl. Only affects
            # newly-created worktrees; existing ones keep their path.
            worktree-path = "{{ repo_path }}-worktrees/{{ branch | sanitize }}"

            # The tmux post-switch hook owns navigation (select-window or switch-client),
            # so skip cd'ing the parent shell — otherwise it ends up pwd'd at the
            # worktree behind a different session's window. Hooks still fire normally.
            [switch]
            cd = false

            [post-switch]
            tmux = """
            [ -z "$TMUX" ] && exit 0
            [ -n "$CLAUDECODE" ] && exit 0
            # display-message resolves against the attached client's ACTIVE
            # window unless pinned to the invoking pane — and wt's pane often
            # isn't the active one at hook time (long checkout, multi-client,
            # focus moved). $TMUX_PANE is set for every pane once $TMUX is.
            CUR_SESSION=$(tmux display-message -t "$TMUX_PANE" -p '#{session_name}')
            CUR_WIN=$(tmux display-message -t "$TMUX_PANE" -p '#{window_index}')
            # Primary: match by @worktree tag across ALL sessions (set when we create
            # the window). Output is "<session>\t<window>".
            MATCH=$(tmux list-windows -a -F '#{session_name}\t#{window_index}\t#{@worktree}' \
              | awk -F'\t' -v wt="{{ worktree_path }}" '$3 == wt { print $1 "\t" $2; exit }')
            # Fallback: match by pane_current_path — prefix match, so a pane
            # cd'd into a subdirectory still counts (but not into a nested
            # .worktrees/ checkout, which belongs to a different branch).
            # @worktree tags are lost on tmux-state restore, so this path
            # also re-tags them (self-heal). Prefer the current window: when
            # we're already sitting in the worktree, re-tag in place instead
            # of navigating away.
            if [ -z "$MATCH" ]; then
              MATCH=$(tmux list-windows -a -F '#{session_name}\t#{window_index}\t#{pane_current_path}' \
                | awk -F'\t' -v wt="{{ worktree_path }}" -v cs="$CUR_SESSION" -v cw="$CUR_WIN" '
                    $3 == wt || (index($3, wt "/") == 1 && index($3, wt "/.worktrees/") != 1) {
                      if ($1 == cs && $2 == cw) { m = $1 "\t" $2; exit }
                      if (!m) m = $1 "\t" $2
                    }
                    END { if (m) print m }')
            fi
            if [ -n "$MATCH" ]; then
              SESS=$(printf '%s' "$MATCH" | cut -f1)
              WIN=$(printf '%s' "$MATCH" | cut -f2)
              if [ "$SESS" = "$CUR_SESSION" ]; then
                tmux select-window -t "$SESS:$WIN"
              else
                tmux switch-client -t "$SESS:$WIN"
              fi
              # Auto-tag matched-by-path windows so the next call hits the primary signal.
              tmux set-option -t "$SESS:$WIN" -w @worktree "{{ worktree_path }}"
              tmux set-option -t "$SESS:$WIN" -w @branch "{{ branch | sanitize }}"
              STAMP_TARGET="$SESS:$WIN"
            else
              # Take over the current window when it's a single pane whose
              # worktree is already shown by another window in THIS session —
              # repurpose the redundant window instead of stacking up a new
              # one. Same-session only: another session showing the same path
              # doesn't make this session's window redundant.
              CUR_PANES=$(tmux display-message -t "$TMUX_PANE" -p '#{window_panes}')
              CUR_WT=$(tmux display-message -t "$TMUX_PANE" -p '#{@worktree}')
              CUR_PATH=$(tmux display-message -t "$TMUX_PANE" -p '#{pane_current_path}')
              CUR_CMD=$(tmux display-message -t "$TMUX_PANE" -p '#{pane_current_command}')
              DUP=""
              # send-keys below types into the active pane, so only take over
              # when wt itself is what's running there (or a bare shell) —
              # never when wt was invoked via a popup/binding over e.g. nvim.
              case "$CUR_CMD" in
                wt | fish | bash | zsh | sh)
                  if [ "$CUR_PANES" = "1" ]; then
                    DUP=$(tmux list-windows -t "$CUR_SESSION" -F '#{window_index}\t#{@worktree}\t#{pane_current_path}' \
                      | awk -F'\t' -v cw="$CUR_WIN" -v cwt="$CUR_WT" -v cp="$CUR_PATH" '
                          $1 == cw { next }
                          (cwt != "" && $2 == cwt) || $3 == cp { print "dup"; exit }')
                  fi
                  ;;
              esac
              if [ -n "$DUP" ]; then
                CUR_TARGET=$(tmux display-message -t "$TMUX_PANE" -p '#{session_id}:#{window_index}')
                tmux set-option -t "$CUR_TARGET" -w @worktree "{{ worktree_path }}"
                tmux set-option -t "$CUR_TARGET" -w @branch "{{ branch | sanitize }}"
                # Queued in the pty; the shell reads it once wt exits.
                tmux send-keys -t "$CUR_TARGET" "cd '{{ worktree_path }}'" Enter
                STAMP_TARGET="$CUR_TARGET"
              else
                # Capture the new window as a precise target ($N:idx session-id form,
                # immune to numeric session names). A bare session target resolves to
                # the *currently active* window, and the backgrounded issue-stamp
                # finishes seconds later — by then the user may have switched windows
                # and the stamp would land on the wrong one.
                NEW_WIN=$(tmux new-window -a -t "$CUR_SESSION" -c "{{ worktree_path }}" -P -F '#{session_id}:#{window_index}')
                tmux set-option -t "$NEW_WIN" -w @worktree "{{ worktree_path }}"
                tmux set-option -t "$NEW_WIN" -w @branch "{{ branch | sanitize }}"
                STAMP_TARGET="$NEW_WIN"
              fi
            fi${lib.optionalString cfg.enrich.enable ''

              if [ -n "''${STAMP_TARGET:-}" ]; then
                ${tmuxConfig.script.tmux-issue-stamp}/bin/tmux-issue-stamp "$STAMP_TARGET" "{{ worktree_path }}" "{{ branch | sanitize }}" >/dev/null 2>&1 &
              fi''}
            """
            zoxide = """
            command -v zoxide >/dev/null 2>&1 && zoxide add "{{ worktree_path }}"
            """

            [post-remove]
            tmux = """
            [ -z "$TMUX" ] && exit 0
            [ -n "$CLAUDECODE" ] && exit 0
            SESSION=$(tmux display-message -t "$TMUX_PANE" -p '#{session_name}')
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
    }
    # Never restart on switch — killing the tmux server destroys all sessions and
    # history. The startup script resolves tmux via the user profile, so the
    # unit/plist doesn't change when lazytmux updates (preventing sd-switch restart).
    (lib.mkIf isLinux {
      systemd.user = {
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
          #
          # TMUX_TMPDIR points tmux-state at the user's actual socket
          # ($XDG_RUNTIME_DIR/tmux-$UID/default) instead of tmux's compiled-in
          # /tmp default; without it the timer queried a stale socket and
          # bailed. (tmux-state >= 7f8c820 also synthesizes the TMUX env var
          # internally so format-string control bytes survive — no need to set
          # TMUX here.)
          lazytmux-state-save = lib.mkIf persistEnabled {
            Unit.Description = "Save tmux-state snapshot";
            Service = {
              Type = "oneshot";
              Environment = ["TMUX_TMPDIR=%t"];
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
    })
    # Darwin: launchd agents mirroring the systemd units. tmux uses its default
    # /tmp/tmux-$UID socket here (no $XDG_RUNTIME_DIR), so we omit the
    # TMUX_TMPDIR / %t handling the Linux units need.
    (lib.mkIf isDarwin {
      launchd.agents =
        lib.optionalAttrs cfg.startupSession.enable {
          tmux-startup = {
            enable = true;
            config = {
              ProgramArguments = ["${tmux-startup-script}"];
              RunAtLoad = true;
              EnvironmentVariables =
                {
                  COLORTERM = cfg.startupSession.terminal.colorterm;
                  TERM = effectiveTerm;
                }
                // lib.optionalAttrs (effectiveTermProgram != "") {
                  TERM_PROGRAM = effectiveTermProgram;
                }
                // lib.optionalAttrs (effectiveTerminfoPath != null) {
                  TERMINFO = effectiveTerminfoPath;
                };
              StandardOutPath = "/tmp/lazytmux-startup.log";
              StandardErrorPath = "/tmp/lazytmux-startup.log";
            };
          };
        }
        // lib.optionalAttrs persistEnabled {
          # Periodic snapshot. No TMUX_TMPDIR: darwin tmux uses its default
          # /tmp/tmux-$UID socket, which tmux-state also resolves to by default.
          lazytmux-state-save = {
            enable = true;
            config = {
              ProgramArguments = ["${cfg.persist.package}/bin/tmux-state" "save" "--reason=timer"];
              StartInterval = cfg.persist.saveInterval;
            };
          };
          # Weekly GC of orphaned scrollback files.
          lazytmux-state-gc = {
            enable = true;
            config = {
              ProgramArguments = ["${cfg.persist.package}/bin/tmux-state" "gc"];
              StartCalendarInterval = [{Weekday = 0;}];
            };
          };
        };
    })
  ]);
}
