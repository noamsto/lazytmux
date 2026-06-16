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

  enrichProvidersStr = lib.concatStringsSep " " enrichProviders;
  # Nerd Font (Material Design) glyph defaults. Override per-icon with Nerd Font
  # glyphs via programs.lazytmux.enrich.icons (see CLAUDE.md). Keys: linear,
  # github, pending, success, failure, merged.
  enrichIconDefaults = {
    linear = "󰰍"; # nerd: nf-md-alpha-l-circle (U+F0C0D)
    github = "󰊤"; # nerd: nf-md-github (U+F02A4)
    pending = "󰦖"; # nerd: nf-md-progress-clock (U+F0996)
    success = "󰗠"; # nerd: nf-md-check-circle (U+F05E0)
    failure = "󰀨"; # nerd: nf-md-alert-circle (U+F0028)
    merged = "󰘭"; # nerd: nf-md-source-merge (U+F062D)
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

  scriptNames = [
    "claude-status"
    "claude-status-update"
    "tmux-reflow-windows"
    "tmux-session-picker"
    "tmux-window-picker"
    "tmux-update-icons"
    "tmux-branch-display"
    "tmux-dir-display"
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

  mkScriptFull = name: let
    raw = builtins.readFile ../scripts/${name}.sh;
    patched =
      builtins.replaceStrings
      ["@lib_icons@" "@lib_claude@" "@lib_enrich@" "claude-status " "@claude_status_bin@" "@ICON_MAP@" "@FALLBACK_ICON@" "@MAX_ICONS@" "@MAX_ICONS_PICKER@" "@picker_generate@" "@lib_log@"]
      ["${lib-icons}" "${lib-claude}" "${lib-enrich}" "${claude-status-bin} " claude-status-bin iconMapBash fallbackIcon maxIcons maxIconsPicker picker-generate-bin "${lib-log}"]
      raw;
  in
    pkgs.writeShellScriptBin name patched;

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
    else if builtins.elem name scriptsWithIcons
    then mkScriptFull name
    else if name == "claude-status"
    then claude-status-pkg
    else if name == "tmux-splash-maybe"
    then mkScriptSplash name
    else if name == "tmux-gh-dash"
    then mkScriptGhDash name
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
  # toggle package is wired in (carousel-toggle != null).
  carouselBind =
    lib.optionalString (carousel-toggle != null)
    "bind I run-shell '${carousel-toggle}/bin/tmux-claude-images'";

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

    # Alt-shift window navigation
    bind -n M-H previous-window
    bind -n M-L next-window

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
      # prefix + i enters the enrich table: i open issue, p open PR, r refresh.
      bind-key i switch-client -T enrich
      bind-key -T enrich i run-shell 'url="#{@issue_url}"; [ -n "$url" ] && xdg-open "$url" >/dev/null 2>&1'
      bind-key -T enrich p run-shell 'url="#{@pr_url}"; [ -n "$url" ] && xdg-open "$url" >/dev/null 2>&1'
      # run-shell inherits the tmux server's cwd (not a repo) — pass a repo dir
      # so gh has context. Same @worktree→@git_root chain as the full pass (so
      # both resolve the same cache key), with the pane path as a last resort.
      bind-key -T enrich r run-shell '${script.tmux-pr-enrich}/bin/tmux-pr-enrich --target "#{session_id}:#{window_id}" --branch "#{@branch}" --dir "#{?#{@worktree},#{@worktree},#{?#{@git_root},#{@git_root},#{pane_current_path}}}" --force'
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

    bind-key x kill-pane
    bind-key & confirm-before -p "kill-window #W? (y/n)" kill-window
    set -g detach-on-destroy off

    # Vim-tmux navigation (respects zoom)
    is_vim="ps -o state= -o comm= -t '#{pane_tty}' | grep -iqE '^[^TXZ ]+ +(\\S+\\/)?g?(view|l?n?vim?x?|fzf)(diff)?$'"
    bind-key -n C-h if-shell "$is_vim" "send-keys C-h" "if-shell '[ #{window_zoomed_flag} -eq 0 ]' 'select-pane -L'"
    bind-key -n C-j if-shell "$is_vim" "send-keys C-j" "if-shell '[ #{window_zoomed_flag} -eq 0 ]' 'select-pane -D'"
    bind-key -n C-k if-shell "$is_vim" "send-keys C-k" "if-shell '[ #{window_zoomed_flag} -eq 0 ]' 'select-pane -U'"
    bind-key -n C-l if-shell "$is_vim" "send-keys C-l" "if-shell '[ #{window_zoomed_flag} -eq 0 ]' 'select-pane -R'"

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

    # Icon variables
    set -g @icon_session "${icons.session}"
    set -g @icon_branch "${icons.branch}"
    set -g @icon_dir "${icons.dir}"
    set -g @picker_zoxide_exclude "${zoxideExclude}"

    # Line 0: Session / Branch / Dir / Claude status (left) | App info (right)
    set -g status-format[0] "#(${script.tmux-update-icons}/bin/tmux-update-icons '#{session_name}')${lib.optionalString enrichEnable "#(${script.tmux-pr-enrich}/bin/tmux-pr-enrich --tick)"}#[align=left,bg=#{@thm_bg}]#{?client_prefix,#[fg=#{@thm_red}#,bold],#{?#{@claude_session_fg},#[fg=#{@claude_session_fg}],#[fg=#{@thm_mauve}]}} #{@icon_session} #S  #{?#{&&:#{@issue_id},#{==:#{@issue_branch},#{@branch}}},#[fg=#{@thm_blue}#,bold]#{?#{==:#{@issue_provider},linear},${enrichIconSet.linear},${enrichIconSet.github}} #{@issue_id} #[fg=#{@thm_text}#,nobold]#{@issue_title},#[fg=#{@thm_blue}#,bold]#{@icon_branch} #(${script.tmux-branch-display}/bin/tmux-branch-display '#{@branch}' '#{pane_current_path}')}  #[fg=#{@thm_subtext_0},nobold]#{@icon_dir} #(${script.tmux-dir-display}/bin/tmux-dir-display '#{@branch}' '#{pane_current_path}' '#{@git_root}')  #[fg=#{@thm_overlay_1}]#(${script.claude-status}/bin/claude-status --session '#{session_name}' --format icon-color) #[align=right]#{?#{&&:#{@pr_number},#{&&:#{!=:#{@pr_number},none},#{==:#{@pr_branch},#{@branch}}}},#{?#{||:#{==:#{@pr_check_state},failure},#{==:#{@pr_mergeable},conflicting}},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#{?#{==:#{@pr_state},closed},#[fg=#{@thm_overlay_0}],#[fg=#{@thm_green}]}}}}#{?#{==:#{@pr_mergeable},conflicting},${enrichIconSet.conflict},#{?#{==:#{@pr_check_state},failure},${enrichIconSet.failure},#{?#{==:#{@pr_check_state},pending},${enrichIconSet.pending},#{?#{==:#{@pr_state},merged},${enrichIconSet.merged},${enrichIconSet.success}}}}} ###{@pr_number} #{@pr_title}  ,}#[fg=#{@thm_subtext_0}]#{@active_pane_icon} #{s|^\\.(.*)-wrapped$|\\1|:pane_current_command} "

    # Lines 1-3: Window list (dynamically generated by tmux-reflow-windows hook)
    # Tab anatomy: idx + bold identity prefix (@window_label_id) + remainder +
    # icons, then the PR segment last, colored by check state on every tab
    # (failing=red, pending=peach, merged=mauve, success/open=green). Rendered
    # last so its state color only runs into the separator, which sets its own
    # color. tmux-reflow-windows mirrors this layout for the multi-line
    # variants with column-padded segments.
    set -g status-format[1] "#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#[range=window|#{window_index}]#[nobold]#{?window_active,#[fg=#{@thm_lavender}#,bg=#{@thm_surface_0}],#[fg=#{@thm_subtext_0}#,bg=#{@thm_bg}]}#{window_index}: #[bold]#{@window_label_id}#[nobold]#{?#{==:#{@labels_mode},long},#{@window_label_rest_long},#{@window_label_rest_short}} #{@window_icon_display}#{?window_zoomed_flag, 󰁌,}#{?#{&&:#{@pr_number},#{!=:#{@pr_number},none}},#{?#{||:#{==:#{@pr_check_state},failure},#{==:#{@pr_mergeable},conflicting}},#[fg=#{@thm_red}],#{?#{==:#{@pr_check_state},pending},#[fg=#{@thm_peach}],#{?#{==:#{@pr_state},merged},#[fg=#{@thm_mauve}],#[fg=#{@thm_green}]}}},}#{@window_pr_plain}#[bg=#{@thm_bg}]#[norange]#{?window_end_flag,, #[fg=#{@thm_subtext_0}#,nobold]│ }}"
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
    set -g window-style "fg=#{@thm_text},bg=#{@thm_mantle}"
    set -g window-active-style "fg=#{@thm_text},bg=#{@thm_bg}"

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
