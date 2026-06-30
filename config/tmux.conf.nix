{
  pkgs,
  lib,
  extraProcessIcons ? {},
  # TERM string of the outer terminal emulator (e.g. "xterm-ghostty", "xterm-kitty").
  # When set, adds a terminal-features line for RGB true-color + extended keys.
  # Null when no emulator preset is active (manual terminal config).
  terminalTerm ? null,
  # Additional tmux config text appended verbatim at the end of the generated
  # tmux.conf. Used by the home-manager module to inject opt-in features
  # (e.g. tmux-state hooks/keybindings) without polluting the base config.
  extraConfText ? "",
  # Issue/PR enrichment config (threaded from the home-manager module).
  enrichEnable ? true,
  enrichProviders ? ["linear" "github"],
  enrichPrRefreshSeconds ? 30,
  enrichIcons ? {},
  # Comma-separated glob/basename patterns the session picker drops from its
  # zoxide suggestions (e.g. "*/.ssh,/tmp/*"). Empty => suggest everything.
  zoxideExclude ? "",
  # tmux prefix key (literal character). Default backtick.
  prefix ? "`",
  # Absolute path to the shell tmux spawns in new panes (default-shell).
  # Null => tmux uses $SHELL / the account shell.
  defaultShell ? null,
  # agent-carousel toggle package (threaded from the flake input; used by the prefix+I keybind).
  carousel-toggle ? null,
  # Welcome-buffer splash (threaded from the home-manager module).
  splashEnable ? true,
  splashTips ? [],
  splashTimeout ? 10,
  # AI window naming (threaded from the home-manager module). When enabled, a
  # UserPromptSubmit hook nudges the pane's Claude to name fallback windows (no
  # tracked issue, on the default branch) for itself via `claude-status-update
  # name set`. Exposed to the plugin hook as the @ai_naming global.
  aiNamingEnable ? false,
  # Resume Claude sessions on tmux-state restore (threaded from the module).
  # When on, tmux-update-icons stamps each Claude pane's @ts_relaunch override
  # so restore relaunches `claude --resume <uuid>` instead of a bare shell.
  # Exposed as the @resume_claude global, read by update-icons each tick.
  resumeClaudeEnable ? true,
}: let
  # --- Nerd font icons (edit these if they don't render in your terminal) ---
  icons = {
    session = "";
    branch = "";
    dir = "";
    window-last = "󰖰";
    window-current = "󰖯";
    window-zoom = "󰁌";
    window-mark = "󰃀";
    window-silent = "󰂛";
    window-activity = "󱅫";
    window-bell = "󰂞";
  };

  # Process name → icon mapping (separate file for easy editing)
  # extraProcessIcons overrides defaults when keys collide
  processIcons = let
    raw = (import ./process-icons.nix) // extraProcessIcons;
    # VS16 (U+FE0F) emoji cause width miscalculation in lipgloss/go-runewidth
    # (charmbracelet/lipgloss#55, #562). Use text presentation (no VS16) instead.
    vs16Icons = lib.filterAttrs (_: v: lib.hasInfix "️" v) raw; # "️" = U+FE0F (invisible)
    vs16Names = builtins.attrNames vs16Icons;
  in
    if vs16Names != []
    then
      builtins.throw ''
        process-icons: VS16 emoji (U+FE0F) cause alignment bugs in the picker.
        Strip the trailing ️ from: ${builtins.concatStringsSep ", " vs16Names}
        See: charmbracelet/lipgloss#55
      ''
    else raw;
  fallbackIcon = "";
  maxIcons = "2";
  maxIconsPicker = "5";

  aiNamingFlag =
    if aiNamingEnable
    then "1"
    else "0";

  resumeClaudeFlag =
    if resumeClaudeEnable
    then "on"
    else "off";

  enrichProvidersStr = lib.concatStringsSep " " enrichProviders;
  # Nerd Font (Material Design) glyph defaults. Override per-icon with Nerd Font
  # glyphs via programs.lazytmux.enrich.icons (see CLAUDE.md). Keys: linear,
  # github, pending, success, failure, merged, closed, conflict.
  enrichIconDefaults = {
    linear = "󰰍"; # nerd: nf-md-alpha-l-circle (U+F0C0D)
    github = "󰊤"; # nerd: nf-md-github (U+F02A4)
    pending = "󰦖"; # nerd: nf-md-progress-clock (U+F0996)
    success = "󰗠"; # nerd: nf-md-check-circle (U+F05E0)
    failure = "󰀨"; # nerd: nf-md-alert-circle (U+F0028)
    merged = "󰘭"; # nerd: nf-md-source-merge (U+F062D)
    closed = "󰅖"; # nerd: nf-md-close-circle-outline (U+F0156) — closed/superseded PR
    conflict = "󰀦"; # nerd: nf-md-alert (U+F0026) — swap for preferred conflict glyph
  };
  enrichIconSet = enrichIconDefaults // enrichIcons;

  # Generate bash associative array entries from Nix attrset
  iconMapBash = lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "  [${k}]=\"${v}\"") processIcons);

  # --- Custom plugins (pinned versions) ---
  catppuccin = pkgs.tmuxPlugins.mkTmuxPlugin rec {
    pluginName = "catppuccin";
    version = "2.1.3";
    src = pkgs.fetchFromGitHub {
      owner = "catppuccin";
      repo = "tmux";
      rev = "v${version}";
      sha256 = "0v3vji240mykfxf573kpwjmnswil0f9j7srqlhq74ca9ar1h5k92";
    };
    meta = with lib; {
      description = "Catppuccin theme for Tmux";
      homepage = "https://github.com/catppuccin/tmux";
      license = licenses.mit;
      platforms = platforms.all;
    };
  };

  # --- Shared libraries (sourced, not executed) ---
  mkLib = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      ["@ICON_MAP@" "@FALLBACK_ICON@"]
      [iconMapBash fallbackIcon]
      raw;
  in
    pkgs.writeShellScript name patched;

  lib-icons = mkLib "lib-icons";
  lib-claude = mkLib "lib-claude";

  # lib-log has no build-time placeholders of its own; a plain writeShellScript.
  lib-log = pkgs.writeShellScript "lib-log" (builtins.readFile ../scripts/lib-log.sh);

  # Shell label builder needs raw glyphs (single '#'). The tmux-format path uses
  # the '##'-escaped enrichIconSet (which MUST keep '##' — do not change it). Only
  # user-override icons are '##'-escaped by the module, so un-escape just those and
  # merge over the raw defaults; default glyphs are never touched.
  enrichIconSetRaw = enrichIconDefaults // (builtins.mapAttrs (_: v: builtins.replaceStrings ["##"] ["#"] v) enrichIcons);

  # lib-enrich needs the provider-priority substitution rather than the icon map.
  lib-enrich = let
    raw = builtins.readFile ../scripts/lib-enrich.sh;
    patched =
      builtins.replaceStrings
      [
        "@providers@"
        "@enrich_icon_linear@"
        "@enrich_icon_github@"
        "@enrich_icon_pending@"
        "@enrich_icon_success@"
        "@enrich_icon_failure@"
        "@enrich_icon_merged@"
        "@enrich_icon_closed@"
        "@enrich_icon_conflict@"
      ]
      [
        enrichProvidersStr
        enrichIconSetRaw.linear
        enrichIconSetRaw.github
        enrichIconSetRaw.pending
        enrichIconSetRaw.success
        enrichIconSetRaw.failure
        enrichIconSetRaw.merged
        enrichIconSetRaw.closed
        enrichIconSetRaw.conflict
      ]
      raw;
  in
    pkgs.writeShellScript "lib-enrich" patched;

  # --- Helper scripts ---
  mkScript = name: pkgs.writeShellScriptBin name (builtins.readFile ../scripts/${name}.sh);

  mkScriptSplash = name:
    pkgs.writeShellScriptBin name (
      builtins.replaceStrings ["@tmux_splash@"] [picker-splash-bin]
      (builtins.readFile ../scripts/${name}.sh)
    );

  # gh-dash launcher: needs the pinned gh-dash + yq store paths.
  mkScriptGhDash = name:
    pkgs.writeShellScriptBin name (
      builtins.replaceStrings ["@gh_dash@" "@yq@"] ["${gh-dash}/bin/gh-dash" "${pkgs.yq-go}/bin/yq"]
      (builtins.readFile ../scripts/${name}.sh)
    );

  # claude-status needs lib substitution but not self-reference
  mkScriptWithLibs = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      ["@lib_icons@" "@lib_claude@"]
      ["${lib-icons}" "${lib-claude}"]
      raw;
  in
    pkgs.writeShellScriptBin name patched;

  # Scripts that source only lib-log (gated event logging). Includes
  # claude-status-update, which is run RAW by tests/claude-issues.bats — its
  # source is guarded so the raw script defines no-op stubs.
  scriptsWithLog = ["claude-status-update" "lazytmux-log-event" "lazytmux-debug"];

  mkScriptWithLog = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched = builtins.replaceStrings ["@lib_log@"] ["${lib-log}"] raw;
  in
    pkgs.writeShellScriptBin name patched;

  # Build claude-status first — other scripts reference it by full path
  claude-status-pkg = mkScriptWithLibs "claude-status";
  claude-status-bin = "${claude-status-pkg}/bin/claude-status";

  # Go binary for fast session picker generation (~4ms vs ~85ms in bash)
  picker-generate = import ../picker {
    inherit pkgs lib processIcons fallbackIcon;
    inherit maxIconsPicker;
    inherit splashTips splashTimeout prefix;
  };
  picker-generate-bin = "${picker-generate}/bin/tmux-picker-generate";
  picker-splash-bin = "${picker-generate}/bin/tmux-splash";
  picker-statusline-bin = "${picker-generate}/bin/tmux-statusline";
  picker-card-bin = "${picker-generate}/bin/tmux-enrich-card";

  scriptNames = [
    "claude-status"
    "claude-status-update"
    "tmux-reflow-windows"
    "tmux-session-picker"
    "tmux-window-picker"
    "tmux-update-icons"
    "tmux-branch-display"
    "tmux-dir-display"
    "tmux-window-nav"
    "tmux-smart-nav"
    "tmux-reconcile-window"
    "tmux-apply-theme-colors"
    "tmux-scratchpad"
    "tmux-issue-stamp"
    "tmux-issue-stamp-linear"
    "tmux-issue-stamp-github"
    "tmux-pr-enrich"
    "tmux-splash-maybe"
    "tmux-gh-dash"
    "lazytmux-log-event"
    "lazytmux-debug"
  ];

  # Scripts that need icon map + library + claude-status path substitution
  scriptsWithIcons = ["tmux-reflow-windows" "tmux-session-picker" "tmux-window-picker" "tmux-update-icons"];

  iconSubstFrom = ["@lib_icons@" "@lib_claude@" "@lib_enrich@" "claude-status " "@claude_status_bin@" "@ICON_MAP@" "@FALLBACK_ICON@" "@MAX_ICONS@" "@MAX_ICONS_PICKER@" "@picker_generate@" "@lib_log@"];
  iconSubstTo = ["${lib-icons}" "${lib-claude}" "${lib-enrich}" "${claude-status-bin} " claude-status-bin iconMapBash fallbackIcon maxIcons maxIconsPicker picker-generate-bin "${lib-log}"];

  mkScriptFull = name:
    pkgs.writeShellScriptBin name
    (builtins.replaceStrings iconSubstFrom iconSubstTo (builtins.readFile ../scripts/${name}.sh));

  # tmux-update-icons kicks a forced reflow on branch/task change. Pin that call
  # to the reflow store path via @reflow@ so a config reload alone repoints it; a
  # bare name resolves against the tmux server's frozen PATH and stays stale
  # until a full server restart. Built apart from mkScriptFull so reflow itself
  # (also icon-substituted) never references its own store path.
  mkScriptIcons = name:
    pkgs.writeShellScriptBin name
    (builtins.replaceStrings
      (iconSubstFrom ++ ["@reflow@"])
      (iconSubstTo ++ ["${script.tmux-reflow-windows}/bin/tmux-reflow-windows"])
      (builtins.readFile ../scripts/${name}.sh));

  # Scripts that need enrich library + provider/icon/config substitution
  scriptsWithEnrich = ["tmux-issue-stamp" "tmux-issue-stamp-linear" "tmux-issue-stamp-github" "tmux-pr-enrich"];

  mkScriptEnrich = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      [
        "@lib_enrich@"
        "@pr_refresh_seconds@"
        "@issue_stamp_linear@"
        "@issue_stamp_github@"
        "@pr_enrich@"
        "@reflow@"
        "@lib_log@"
      ]
      [
        "${lib-enrich}"
        (toString enrichPrRefreshSeconds)
        "${enrich-linear-bin}/bin/tmux-issue-stamp-linear"
        "${enrich-github-bin}/bin/tmux-issue-stamp-github"
        "${enrich-pr-bin}/bin/tmux-pr-enrich"
        "${script.tmux-reflow-windows}/bin/tmux-reflow-windows"
        "${lib-log}"
      ]
      raw;
  in
    pkgs.writeShellScriptBin name patched;

  enrich-linear-bin = mkScriptEnrich "tmux-issue-stamp-linear";
  enrich-github-bin = mkScriptEnrich "tmux-issue-stamp-github";
  enrich-pr-bin = mkScriptEnrich "tmux-pr-enrich";

  # The cwd-derived window reconciler. Always built (tagging drives navigation
  # even with enrich off); the @issue_stamp@ kick is empty when enrich is off.
  mkScriptReconcile = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      ["@issue_stamp@"]
      [
        (
          if enrichEnable
          then "${script.tmux-issue-stamp}/bin/tmux-issue-stamp"
          else ""
        )
      ]
      raw;
  in
    pkgs.writeShellScriptBin name patched;

  # Individual script references for full store paths in config.
  # Reuse the pre-built enrich provider/poller derivations (also referenced by
  # the dispatcher's substitution) instead of rebuilding them via mkScriptEnrich.
  script = lib.genAttrs scriptNames (name:
    if name == "tmux-issue-stamp-linear"
    then enrich-linear-bin
    else if name == "tmux-issue-stamp-github"
    then enrich-github-bin
    else if name == "tmux-pr-enrich"
    then enrich-pr-bin
    else if builtins.elem name scriptsWithEnrich
    then mkScriptEnrich name
    else if name == "tmux-update-icons"
    then mkScriptIcons name
    else if builtins.elem name scriptsWithIcons
    then mkScriptFull name
    else if name == "claude-status"
    then claude-status-pkg
    else if name == "tmux-splash-maybe"
    then mkScriptSplash name
    else if name == "tmux-gh-dash"
    then mkScriptGhDash name
    else if name == "tmux-reconcile-window"
    then mkScriptReconcile name
    else if builtins.elem name scriptsWithLog
    then mkScriptWithLog name
    else mkScript name);

  scripts = lib.attrValues script;

  inherit (pkgs) tmuxPlugins;

  # tmux-fingers from develop — picks up PR #161 (removes per-style tput
  # shell-outs) which eliminates the ~1s broken-TERM penalty in tmux subshells.
  # Drop in favor of `tmuxPlugins.fingers` once nixpkgs ships a post-#161 release.
  fingers = pkgs.callPackage ../packages/tmux-fingers-dev {
    inherit (pkgs.tmuxPlugins) mkTmuxPlugin;
  };

  # terminal-features line for the outer terminal, derived from its TERM string.
  # Pattern uses a wildcard suffix to match version variants (e.g. "xterm-ghostty*").
  terminalConfig =
    lib.optionalString (terminalTerm != null)
    "set -as terminal-features '${terminalTerm}*:RGB:extkeys'\n    ";

  # default-shell line, emitted only when a shell path is configured.
  defaultShellConfig =
    lib.optionalString (defaultShell != null)
    "set -g default-shell ${defaultShell}\n    ";

  # prefix+I bind for the agent-carousel image gallery, emitted only when the
  # toggle package is wired in (carousel-toggle != null). tmux's run-shell does not
  # export TMUX_PANE, so the toggle can't key the manifest to the pressing pane and
  # reports "no images yet for this pane". Inject it via #{pane_id}, which tmux
  # format-expands in the command string before exec.
  carouselBind =
    lib.optionalString (carousel-toggle != null)
    "bind I run-shell 'TMUX_PANE=#{pane_id} ${carousel-toggle}/bin/tmux-claude-images'";

  # In kitty-pane mode (AEYE_HOST=kitty) the carousel is a kitty split that doesn't
  # know about tmux focus, so reconcile it whenever the on-screen window changes —
  # stashing carousels for off-screen panes and restoring the visible one. Indexed
  # ([60]) to coexist with the reflow (index 0) and splash ([50]) hooks on the same
  # events. -b so focus changes never block; --reconcile self-gates to kitty mode,
  # so it's a fast no-op for tmux-split users.
  carouselHooks = lib.optionalString (carousel-toggle != null) ''
    set-hook -g client-session-changed[60] 'run-shell -b "${carousel-toggle}/bin/tmux-claude-images --reconcile"'
    set-hook -g session-window-changed[60] 'run-shell -b "${carousel-toggle}/bin/tmux-claude-images --reconcile"'
    set-hook -g client-attached[60]        'run-shell -b "${carousel-toggle}/bin/tmux-claude-images --reconcile"'
  '';

  # --- Plugin config options (set before run-shell) ---
  pluginConfigs = ''
    # catppuccin theme
    # Detect theme from state file on first load (theme-toggle sets flavor before re-source)
    if-shell '[ -z "#{@catppuccin_flavor}" ]' \
      'if-shell "grep -q light \"$HOME/.local/state/theme-state.json\" 2>/dev/null" \
        "set -g @catppuccin_flavor latte" \
        "set -g @catppuccin_flavor mocha"'
    set -g @catppuccin_status_background 'none'
    set -g @catppuccin_window_status_style 'none'
    set -g @catppuccin_window_flags 'icon'
    set -g @catppuccin_window_flags_icon_last " ${icons.window-last}"
    set -g @catppuccin_window_flags_icon_current " ${icons.window-current}"
    set -g @catppuccin_window_flags_icon_zoom " ${icons.window-zoom}"
    set -g @catppuccin_window_flags_icon_mark " ${icons.window-mark}"
    set -g @catppuccin_window_flags_icon_silent " ${icons.window-silent}"
    set -g @catppuccin_window_flags_icon_activity " ${icons.window-activity}"
    set -g @catppuccin_window_flags_icon_bell " ${icons.window-bell}"
    set -g @catppuccin_pane_status_enabled 'off'
    set -g @catppuccin_pane_border_status 'off'

  '';

  # --- Plugin run-shell loading ---
  # Order matters: theme first, then others
  pluginRunShells = ''
    run-shell ${catppuccin}/share/tmux-plugins/catppuccin/catppuccin.tmux
    run-shell ${tmuxPlugins.better-mouse-mode}/share/tmux-plugins/better-mouse-mode/scroll_copy_mode.tmux
    run-shell ${tmuxPlugins.vim-tmux-navigator}/share/tmux-plugins/vim-tmux-navigator/vim-tmux-navigator.tmux
    run-shell ${tmuxPlugins.tmux-fzf}/share/tmux-plugins/tmux-fzf/main.tmux
  '';

  # --- Generated tmux.conf ---
  tmuxConfText = pkgs.writeText "tmux.conf" ''
    # === Base Settings ===
    set -g default-terminal "tmux-256color"
    ${defaultShellConfig}set -g history-limit 1500000
    set -g base-index 1
    setw -g pane-base-index 1
    set -g popup-border-lines rounded

    # === Plugin Configs (must be set before run-shell) ===
    ${pluginConfigs}

    # === Plugin Loading ===
    ${pluginRunShells}

    # === Main Config ===
    set -g mouse on
    set -g window-size latest
    set -g aggressive-resize on
    set-option -g renumber-window on
    set -g focus-events on
    set -g allow-passthrough on
    set -g visual-activity off

    # Preserve terminal environment variables
    set -ga update-environment TERM
    set -ga update-environment TERM_PROGRAM
    set -ga update-environment COLORTERM
    set -ga update-environment TERMINFO
    set -ga update-environment TERMINFO_DIRS
    # kitty remote-control socket, so `kitty @` (carousel reconcile) reaches the
    # enclosing kitty from panes attached after the server started outside it.
    set -ga update-environment KITTY_LISTEN_ON
    # kitty-pane carousel opt-in. The reconcile hook runs in the session env, not
    # the user's interactive shell, so thread AEYE_HOST in on attach — otherwise
    # the carousel never follows tmux focus and the launcher degrades to a split.
    set -ga update-environment AEYE_HOST

    # Timing
    set -s escape-time 0
    set -g repeat-time 300
    set -g initial-repeat-time 600

    # Extended keyboard + clipboard
    set -g extended-keys on
    set -g extended-keys-format csi-u
    ${terminalConfig}set -as terminal-features '*:hyperlinks'
    set -s set-clipboard on
    set -s copy-command '${
      if pkgs.stdenv.hostPlatform.isDarwin
      then "pbcopy"
      else "wl-copy"
    }'

    # Prefix (configurable; default backtick)
    unbind C-b
    set-option -g prefix ${prefix}
    bind ${prefix} send-prefix

    # Config reload
    bind r source-file ~/.config/tmux/tmux.conf \; display "Config reloaded!"

    # Vi copy mode
    setw -g mode-keys vi
    set -g status-keys vi
    unbind-key -T copy-mode-vi v
    bind-key -T copy-mode-vi v send -X begin-selection
    bind-key -T copy-mode-vi C-v send -X rectangle-toggle
    bind-key -T copy-mode-vi 'y' send -X copy-pipe

    # Copy mode styling (tmux 3.6+)
    set -g copy-mode-position-style "bg=#{@thm_surface_0},fg=#{@thm_mauve}"
    set -g copy-mode-selection-style "bg=#{@thm_mauve},fg=#{@thm_bg}"

    # Clear screen
    bind -n M-l send-keys 'C-l'

    # Shift+Enter: process-aware newline for Claude Code / Amp / OpenCode
    bind -n S-Enter if-shell "ps -o comm= -t '#{pane_tty}' | grep -qE '^(amp|bun|opencode)$'" "send-keys \\\\ Enter" "send-keys M-Enter"

    # Pane splitting (| and _)
    unbind %
    bind | split-window -h -c "#{pane_current_path}"
    unbind '"'
    bind _ split-window -v -c "#{pane_current_path}"
    bind c if-shell -F '#{m:scratch-*,#{session_name}}' \
      'display-message "scratchpad: new windows disabled"' \
      'new-window -c "#{pane_current_path}"'
    bind p run-shell '${script.tmux-scratchpad}/bin/tmux-scratchpad "#{session_name}"'
    ${carouselBind}

    # Yank pane's current working directory to system clipboard
    bind Y run-shell 'tmux display-message -p "#{pane_current_path}" | wl-copy'

    # Resize panes
    bind -r -T prefix M-Up    resize-pane -U 5
    bind -r -T prefix M-Down  resize-pane -D 5
    bind -r -T prefix M-Left  resize-pane -L 5
    bind -r -T prefix M-Right resize-pane -R 5

    # Alt-shift window navigation: H/L step within a row, J/K move row-to-row in
    # the reflowed multi-line window grid (no-op when there is no row that way).
    bind -n M-H previous-window
    bind -n M-L next-window
    bind -n M-J run-shell '${script.tmux-window-nav}/bin/tmux-window-nav down #{session_name} #{window_index} #{@window_per}'
    bind -n M-K run-shell '${script.tmux-window-nav}/bin/tmux-window-nav up #{session_name} #{window_index} #{@window_per}'

    # Session/window pickers (wrappers pre-compute claude status)
    bind s run-shell '${script.tmux-session-picker}/bin/tmux-session-picker'
    bind w run-shell '${script.tmux-window-picker}/bin/tmux-window-picker'
    bind a run-shell '${script.tmux-window-picker}/bin/tmux-window-picker --claude'
    # Click session name in status bar to open session picker
    bind -T root MouseDown1StatusLeft choose-tree -Zs

    ${lib.optionalString splashEnable ''
      # Summon the welcome splash on demand (bypasses the once-per-session gate;
      # no auto-timeout — dismiss with any key).
      bind C-Space display-popup -E -B -w 100% -h 100% '${picker-splash-bin} --no-timeout'
    ''}

    ${lib.optionalString enrichEnable ''
      # === Issue/PR enrichment ===
      # prefix + i opens the enrich card popup (issue/PR/branch/Claude identity
      # + o/p/r/q actions). Icons use the RAW set: the popup's stdout is not
      # re-parsed as a tmux format, so ##-escaped glyphs must not be passed.
      bind-key i display-popup -E -w 64 -h 18 "${picker-card-bin} \
        --target '#{session_id}:#{window_id}' \
        --pr-enrich-bin '${script.tmux-pr-enrich}/bin/tmux-pr-enrich' \
        --thm-bg '#{@thm_bg}' --thm-fg '#{@thm_fg}' --thm-mauve '#{@thm_mauve}' \
        --thm-red '#{@thm_red}' --thm-green '#{@thm_green}' --thm-peach '#{@thm_peach}' \
        --thm-blue '#{@thm_blue}' --thm-overlay0 '#{@thm_overlay_0}' \
        --thm-subtext0 '#{@thm_subtext_0}' --thm-lavender '#{@thm_lavender}' \
        --icon-linear '${enrichIconSetRaw.linear}' --icon-github '${enrichIconSetRaw.github}' \
        --icon-pending '${enrichIconSetRaw.pending}' --icon-success '${enrichIconSetRaw.success}' \
        --icon-failure '${enrichIconSetRaw.failure}' --icon-merged '${enrichIconSetRaw.merged}' \
        --icon-closed '${enrichIconSetRaw.closed}' --icon-conflict '${enrichIconSetRaw.conflict}'"
    ''}

    # Floating popups
    bind-key "g" display-popup -E -w 90% -h 90% -d '#{pane_current_path}' lazygit
    bind-key "b" display-popup -E -w 90% -h 90% btop
    bind-key "G" display-popup -E -w 90% -h 90% -d '#{pane_current_path}' ${script.tmux-gh-dash}/bin/tmux-gh-dash
    bind-key D run-shell '${script.lazytmux-debug}/bin/lazytmux-debug toggle'
    # yazi crashes in display-popup (tmux popups don't support passthrough, yazi needs it for terminal detection)
    bind-key "y" if-shell -F '#{m:scratch-*,#{session_name}}' \
      'display-message "scratchpad: new windows disabled"' \
      "new-window -S -n yazi -c '#{pane_current_path}' yazi"

    # New session prompt
    bind N command-prompt -p "New session name:" "new-session -s '%%'"

    # An idle shell kills instantly; anything else running (Claude, vim, a
    # build, a REPL) prompts first, so a reflexive prefix+x can't silently take
    # down a working pane. Normalize the nix makeWrapper decoration
    # (.foo-wrapped -> foo, see set-titles-string) before matching the shell.
    bind-key x if-shell -F '#{m/r:^(bash|fish|zsh|sh|dash)$,#{s|^\.(.*)-wrapped$|\1|:pane_current_command}}' \
      kill-pane \
      'confirm-before -p "kill-pane #P (#{pane_current_command})? (y/n)" kill-pane'
    bind-key & confirm-before -p "kill-window #W? (y/n)" kill-window
    set -g detach-on-destroy off

    # Vim-tmux navigation (respects zoom)
    is_vim="ps -o state= -o comm= -t '#{pane_tty}' | grep -iqE '^[^TXZ ]+ +(\\S+\\/)?g?(view|l?n?vim?x?|fzf)(diff)?$'"
    bind-key -n C-h if-shell "$is_vim" "send-keys C-h" "run-shell 'tmux-smart-nav L left #{window_zoomed_flag} #{pane_at_left}'"
    bind-key -n C-j if-shell "$is_vim" "send-keys C-j" "run-shell 'tmux-smart-nav D down #{window_zoomed_flag} #{pane_at_bottom}'"
    bind-key -n C-k if-shell "$is_vim" "send-keys C-k" "run-shell 'tmux-smart-nav U up #{window_zoomed_flag} #{pane_at_top}'"
    bind-key -n C-l if-shell "$is_vim" "send-keys C-l" "run-shell 'tmux-smart-nav R right #{window_zoomed_flag} #{pane_at_right}'"

    # Window titles
    set-option -g set-titles on
    # Strip the nix makeWrapper decoration (.foo-wrapped → foo). On macOS,
    # pane_current_command resolves to the real on-disk binary, which
    # makeWrapper names `.foo-wrapped`; argv[0] can't change that (the kernel
    # reports the resolved file via proc_pidpath). Rewrite at the display layer.
    # Backslashes are doubled: tmux's config-file lexer unescapes "..." once
    # (\\1 -> \1) before the format engine sees the regex backreference.
    set-option -g set-titles-string "#S / #{s|^\\.(.*)-wrapped$|\\1|:pane_current_command}"

    # === Status Bar (Catppuccin + multi-line) ===
    set -g allow-rename off
    set -g status-position top
    set -g status-interval 1
    set -g status-style "bg=#{@thm_bg}"

    # Multi-line status bar (tmux 3.4+)
    set -g status 2
    set -g @window_split 999
    set -g @window_split2 999

    # Read by the CC plugin's UserPromptSubmit hook to gate the window-naming
    # nudge (programs.lazytmux.aiNaming.enable).
    set -g @ai_naming "${aiNamingFlag}"
    set -g @resume_claude "${resumeClaudeFlag}"

    # Icon variables
    set -g @icon_session "${icons.session}"
    set -g @icon_branch "${icons.branch}"
    set -g @icon_dir "${icons.dir}"
    set -g @picker_zoxide_exclude "${zoxideExclude}"

    # Line 0: Session / Branch / Dir / Claude status (left) | App info (right)
    set -g status-format[0] "#(${script.tmux-update-icons}/bin/tmux-update-icons '#{session_name}' '#{@resume_claude}')${lib.optionalString enrichEnable "#(${script.tmux-pr-enrich}/bin/tmux-pr-enrich --tick)"}#(${picker-statusline-bin} --session '#{session_name}' --prefix '#{client_prefix}' --claude-fg '#{@claude_session_fg}' --issue-id '#{@issue_id}' --issue-branch '#{@issue_branch}' --issue-provider '#{@issue_provider}' --issue-title '#{@issue_title}' --branch '#{@branch}' --path '#{pane_current_path}' --git-root '#{@git_root}' --pr-number '#{@pr_number}' --pr-branch '#{@pr_branch}' --pr-state '#{@pr_state}' --pr-check '#{@pr_check_state}' --pr-mergeable '#{@pr_mergeable}' --pr-title '#{@pr_title}' --pane-icon '#{@active_pane_icon}' --pane-cmd '#{pane_current_command}' --thm-bg '#{@thm_bg}' --thm-red '#{@thm_red}' --thm-mauve '#{@thm_mauve}' --thm-blue '#{@thm_blue}' --thm-text '#{@thm_fg}' --thm-subtext0 '#{@thm_subtext_0}' --thm-overlay0 '#{@thm_overlay_0}' --thm-overlay1 '#{@thm_overlay_1}' --thm-peach '#{@thm_peach}' --thm-green '#{@thm_green}' --icon-session '#{@icon_session}' --icon-branch '#{@icon_branch}' --icon-dir '#{@icon_dir}' --icon-linear '${enrichIconSet.linear}' --icon-github '${enrichIconSet.github}' --icon-pending '${enrichIconSet.pending}' --icon-success '${enrichIconSet.success}' --icon-failure '${enrichIconSet.failure}' --icon-merged '${enrichIconSet.merged}' --icon-closed '${enrichIconSet.closed}' --icon-conflict '${enrichIconSet.conflict}')"

    # Lines 1-3: Window list (dynamically generated by tmux-reflow-windows hook)
    # A window with @crew_color set (e.g. a fan-out worker tagged by an external
    # orchestrator) tints its index+label with that color instead of the default
    # mauve/subtext0, so each agent's tab is identifiable at a glance; the PR
    # segment keeps its own check-state color either way.
    # Tab anatomy: idx + bold identity prefix (@window_label_id) + remainder +
    # icons, then the PR segment last, colored by check state on every tab
    # (closed=overlay0, failing=red, pending=peach, merged=mauve,
    # success/open=green; closed wins over a stale check). Rendered
    # last so its state color only runs into the separator, which sets its own
    # color. tmux-reflow-windows mirrors this layout for the multi-line
    # variants with column-padded segments.
    set -g status-format[1] "#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#[range=window|#{window_index}]#[nobold]#{?#{@crew_color},#{?window_active,#[fg=#{@crew_color}#,bg=#{@thm_bg}#,bold],#[fg=#{@crew_color}#,bg=#{@thm_bg}]},#{?window_active,#[fg=#{@thm_mauve}#,bg=#{@thm_bg}#,bold],#[fg=#{@thm_subtext_0}#,bg=#{@thm_bg}]}}#{window_index}: #[bold]#{@window_label_id}#{?window_active,,#[nobold]}#{?#{==:#{@labels_mode},long},#{@window_label_rest_long},#{@window_label_rest_short}}#{?window_active,#[fg=#{@thm_fg}#,bg=#{@thm_bg}#,nobold],} #{@window_icon_display}#{?window_zoomed_flag, 󰁌,}#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}},#{?#{==:#{@pr_state},closed},#[fg=#{@thm_overlay_0}],#{?#{||:#{==:#{@pr_check_state},failure},#{==:#{@pr_mergeable},conflicting}},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#[fg=#{@thm_green}]}}}},}#{@window_pr_plain}#{?#{@window_claude_ago}, #[fg=#{@thm_overlay_1}]#{@window_claude_ago},}#[bg=#{@thm_bg}]#[norange]#{?window_end_flag,, #[fg=#{@thm_subtext_0}#,nobold]│ }}"
    set -g status-format[2] ""
    set -g status-format[3] ""

    # Reflow hooks: clear stale hooks first so source-file is idempotent.
    # Without this, hooks from previous configs or manual testing persist across reloads.
    set-hook -gu after-new-window
    set-hook -gu session-window-changed
    set-hook -gu client-resized
    set-hook -gu after-new-session
    set-hook -gu client-session-changed
    set-hook -gu pane-exited

    # Also clear hooks from older config versions that may linger
    set-hook -gu window-linked
    set-hook -gu window-unlinked
    set-hook -gu after-resize-pane
    set-hook -gu after-kill-pane
    set-hook -gu pane-focus-in

    # Hooks to reflow windows across status lines
    set-hook -g after-new-window        'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g window-unlinked         'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g session-window-changed  'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g client-resized          'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g after-new-session       'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g client-session-changed  'run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"'

    # Tag every newly-created window as a worktree window from its cwd — at
    # creation, regardless of creator or CLAUDECODE (issue #95). new-session
    # doesn't fire after-new-window for its first window, so both are needed; -b
    # keeps the git probe off the creation path and avoids the foreground
    # run-shell re-entrancy that cascades the hook. Indexed so they coexist with
    # the index-0 reflow hooks; the bare `set-hook -gu` above clears them on reload.
    # Target by #{window_id} alone (globally unique): #{session_id} is "$N", and
    # run-shell's sh -c would re-expand the leading $ (e.g. $0 -> "sh").
    set-hook -g after-new-window[10]  'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'
    set-hook -g after-new-session[10] 'run-shell -b "${script.tmux-reconcile-window}/bin/tmux-reconcile-window #{window_id}"'

    # Clean up claude status file when a pane closes (pane_id is %N, files are just N)
    set-hook -g pane-exited 'run-shell "rm -f /tmp/claude-status/panes/#{s/%%//:pane_id}"'

    # A scratchpad dies with its parent session ([99] is tmux-state's capture-event)
    set-hook -g session-closed[98] 'run-shell -b "tmux kill-session -t \"=scratch-#{hook_session_name}\" 2>/dev/null || true"'

    # Clear unseen claude status flags when user focuses a window
    set-hook -g session-window-changed[99] 'run-shell "${script.claude-status-update}/bin/claude-status-update mark-seen --session #{session_name} --window #{window_index}"'
    set-hook -g client-session-changed[99] 'run-shell "${script.claude-status-update}/bin/claude-status-update mark-seen --session #{session_name} --window #{window_index}"'

    # Pane borders
    setw -g pane-border-status top
    setw -g pane-border-format "━━━━━"
    setw -g pane-active-border-style "bg=#{@thm_bg},fg=#{@thm_mauve}"
    setw -g pane-border-style "bg=#{@thm_bg},fg=#{@thm_overlay_1}"
    setw -g pane-border-lines heavy

    # Pane background: dim inactive
    set -g window-style "fg=#{@thm_fg},bg=#{@thm_mantle}"
    set -g window-active-style "fg=#{@thm_fg},bg=#{@thm_bg}"

    # Pane scrollbars (tmux 3.6+) - disabled
    set -g pane-scrollbars off

    # Prompt cursor styling (tmux 3.6+)
    set -g prompt-cursor-style "blinking-bar"
    set -g prompt-cursor-colour "colour183"

    # Window naming: multi-pane process icons + branch or dir name
    set -wg automatic-rename on
    # window_name keeps the plain PR number (no color codes) so choose-tree
    # pickers still show which window owns which PR. Same segment order as the
    # status tabs: name, icons, PR last.
    set -g automatic-rename-format "#{?#{@window_label_short},#{@window_label_short},#{b:pane_current_path}} #{@window_icon_display}#{@window_pr_plain}"

    # tmux-fingers (smart copy) — hint colors set dynamically by tmux-apply-theme-colors
    set -g @fingers-pattern-0 "[A-Z]{2,}-[0-9]+"
    set -g @fingers-pattern-1 "[a-z][a-z_]*_[0-9a-hjkmnp-tv-z]{26}"
    set -g @fingers-pattern-2 "sha256-[A-Za-z0-9+/]{43}="
    set -g @fingers-pattern-3 "sha256:[0-9a-z]{52}"
    set -g @fingers-pattern-4 "[0-9A-HJKMNP-TV-Z]{26}"
    set -g @fingers-pattern-5 "([0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{1,4}"
    set -g @fingers-pattern-6 "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"
    set -g @fingers-pattern-7 "arn:[a-z0-9-]+:[a-z0-9-]+:[a-z0-9-]*:[0-9]*:[a-zA-Z0-9_/.:-]+"
    set -g @fingers-pattern-8 "eyJ[A-Za-z0-9_-]{10,}\\.[A-Za-z0-9_-]{10,}\\.[A-Za-z0-9_-]{10,}"
    set -g @fingers-pattern-9 "([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}"
    # Only scan built-ins we actually use. Dropped: digit (too noisy — matches
    # any 4+ digit run), git-status, git-status-branch, diff (niche).
    set -g @fingers-enabled-builtin-patterns "url,path,ip,uuid,sha,hex,kubernetes"
    run-shell ${fingers}/share/tmux-plugins/tmux-fingers/tmux-fingers.tmux

    # Force TERM around prefix+F / prefix+J so spawned shells don't hit the
    # no-TERM error path. Belt-and-suspenders alongside the develop-branch fix.
    bind -T prefix F run-shell -b "TERM=tmux-256color ${fingers.passthru.fingers}/bin/tmux-fingers start #{pane_id} >>$HOME/.local/share/tmux-fingers/fingers.log 2>&1"
    bind -T prefix J run-shell -b "TERM=tmux-256color ${fingers.passthru.fingers}/bin/tmux-fingers start --mode jump #{pane_id} >>$HOME/.local/share/tmux-fingers/fingers.log 2>&1"

    # Apply theme-dependent colors (must run after catppuccin loads)
    run-shell "${script.tmux-apply-theme-colors}/bin/tmux-apply-theme-colors"

    # Synchronous init on config load so icons + window bar are ready before the user sees it
    run-shell "${script.tmux-update-icons}/bin/tmux-update-icons #{session_name}"
    run-shell "${script.tmux-reflow-windows}/bin/tmux-reflow-windows #{session_name} #{client_width}"

    ${lib.optionalString splashEnable ''
      # Welcome buffer: indexed ([50]) so it coexists with the reflow hooks'
      # index-0 bindings on the same events (a bare set-hook would clobber them).
      set-hook -g client-attached[50]        'run-shell -b "${script.tmux-splash-maybe}/bin/tmux-splash-maybe #{hook_session}"'
      set-hook -g client-session-changed[50] 'run-shell -b "${script.tmux-splash-maybe}/bin/tmux-splash-maybe #{hook_session}"'
    ''}

    ${carouselHooks}

    ${extraConfText}
  '';

  # Config references ~/.config/tmux/tmux.conf (stable symlink managed by HM module)
  # so no self-referential substitution needed
  tmuxConf = tmuxConfText;

  # Pinned to 4.23.2: the bubbletea-v2 rewrite in 4.24.0 panics in
  # issueview.renderBody when the issues view syncs its preview sidebar, which
  # closes the `prefix + G` popup (display-popup -E). Unpin once upstream ships
  # a fixed 4.24.x. Shared with the module's popupTools so a single version runs.
  gh-dash = pkgs.gh-dash.overrideAttrs (_: rec {
    version = "4.23.2";
    src = pkgs.fetchFromGitHub {
      owner = "dlvhdr";
      repo = "gh-dash";
      tag = "v${version}";
      hash = "sha256-C06LPVoE23ITJpMG0x75Djgeup+eb5uYwA8wL7xxvWU=";
    };
    vendorHash = "sha256-4AbeoH0l7eIS7d0yyJxM7+woC7Q/FCh0BOJj3d1zyX4=";
    ldflags = ["-s" "-w" "-X github.com/dlvhdr/gh-dash/v4/cmd.Version=${version}" "-buildid="];
  });

  # --- Wrapped tmux binary ---
  tmux-wrapped = pkgs.symlinkJoin {
    name = "tmux-wrapped";
    paths = [pkgs.tmux];
    nativeBuildInputs = [pkgs.makeWrapper];
    postBuild = ''
      wrapProgram $out/bin/tmux \
        --add-flags "-f ${tmuxConf}" \
        --prefix PATH : ${lib.makeBinPath ([pkgs.tmux] ++ scripts ++ [pkgs.lazygit gh-dash pkgs.yazi pkgs.btop pkgs.zoxide pkgs.jq pkgs.util-linux pkgs.coreutils pkgs.xdg-utils pkgs.chafa] ++ lib.optional (carousel-toggle != null) carousel-toggle)}
    '';
    meta.mainProgram = "tmux";
  };
in {
  inherit tmux-wrapped tmuxConf script gh-dash;
}
