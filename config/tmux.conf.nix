{
  pkgs,
  lib,
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

  nerd-font-wn = pkgs.tmuxPlugins.mkTmuxPlugin {
    pluginName = "tmux-nerd-font-window-name";
    version = "2025-03-21";
    src = pkgs.fetchFromGitHub {
      owner = "joshmedeski";
      repo = "tmux-nerd-font-window-name";
      rev = "9a66e18972de25c0bb3a58b7422d6e6555f166ba";
      sha256 = "sha256-X4Li6xkxKjqac7xedCNzzSoW7wT6N2oqVKIx7TFay64=";
    };
  };

  # --- Nerd font icon mapping ---
  yamlFormat = pkgs.formats.yaml {};
  nerdFontConfig = yamlFormat.generate "tmux-nerd-font-window-name.yml" {
    config = {
      fallback-icon = "";
      multi-pane-icon = "";
      show-name = false;
      icon-position = "left";
    };
    icons = {
      claude = "🧠";
      nh = "❄️";
      nix = "❄️";
      fish = "🐟";
      process-compose = "⚙️";
      amp = "⚡";
    };
  };

  # --- Helper scripts ---
  mkScript = name: pkgs.writeShellScriptBin name (builtins.readFile ../scripts/${name}.sh);
  scriptNames = [
    "claude-status"
    "claude-status-update"
    "tmux-reflow-windows"
    "tmux-session-picker"
    "tmux-window-picker"
    "tmux-branch-display"
    "tmux-dir-display"
    "tmux-set-pane-border"
  ];
  scripts = map mkScript scriptNames;

  inherit (pkgs) tmuxPlugins;

  # --- Plugin config options (set before run-shell) ---
  pluginConfigs = ''
    # catppuccin theme
    if-shell '[ -z "#{@catppuccin_flavor}" ]' 'set -g @catppuccin_flavor mocha'
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

    # resurrect
    set -g @resurrect-strategy-vim 'session'
    set -g @resurrect-strategy-nvim 'session'
    set -g @resurrect-capture-pane-contents 'on'

    # continuum
    set -g @continuum-restore 'on'
    set -g @continuum-save-interval '10'
  '';

  # --- Plugin run-shell loading ---
  # Order matters: theme first, then others
  pluginRunShells = ''
    run-shell ${catppuccin}/share/tmux-plugins/catppuccin/catppuccin.tmux
    run-shell ${tmuxPlugins.better-mouse-mode}/share/tmux-plugins/better-mouse-mode/scroll_copy_mode.tmux
    run-shell ${tmuxPlugins.resurrect}/share/tmux-plugins/resurrect/resurrect.tmux
    run-shell ${tmuxPlugins.vim-tmux-navigator}/share/tmux-plugins/vim-tmux-navigator/vim-tmux-navigator.tmux
    run-shell ${tmuxPlugins.tmux-fzf}/share/tmux-plugins/tmux-fzf/main.tmux
    run-shell ${tmuxPlugins.continuum}/share/tmux-plugins/continuum/continuum.tmux
  '';

  # --- Generated tmux.conf ---
  # Use a placeholder for the self-reference, then substitute it in a runCommand.
  tmuxConfText = pkgs.writeText "tmux.conf.in" ''
    # === Base Settings ===
    set -g default-terminal "tmux-256color"
    set -g history-limit 1500000
    set -g base-index 1
    setw -g pane-base-index 1

    # === Plugin Configs (must be set before run-shell) ===
    ${pluginConfigs}

    # === Plugin Loading ===
    ${pluginRunShells}

    # === Main Config ===
    set -g mouse on
    set -g window-size smallest
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
    set -as terminal-features 'xterm-kitty*:extkeys'
    set -s set-clipboard on
    set -s copy-command 'wl-copy'

    # Prefix: backtick
    unbind C-b
    set-option -g prefix `
    bind ` send-prefix

    # Config reload
    bind r source-file @tmuxConfPath@ \; display "Config reloaded!"

    # Vi copy mode
    setw -g mode-keys vi
    unbind-key -T copy-mode-vi v
    bind-key -T copy-mode-vi v send -X begin-selection
    bind-key -T copy-mode-vi C-v send -X rectangle-toggle
    bind-key -T copy-mode-vi 'y' send -X copy-selection

    # Copy mode styling (tmux 3.6+)
    set -g copy-mode-position-style "bg=#{@thm_surface_0},fg=#{@thm_mauve}"
    set -g copy-mode-selection-style "bg=#{@thm_mauve},fg=#{@thm_bg}"

    # Clear screen
    bind -n M-l send-keys 'C-l'

    # Shift+Enter: process-aware newline for Claude Code / Amp
    bind -n S-Enter if-shell "ps -o comm= -t '#{pane_tty}' | grep -qE '^(amp|bun)$'" "send-keys \\\\ Enter" "send-keys M-Enter"

    # Pane splitting (| and _)
    unbind %
    bind | split-window -h -c "#{pane_current_path}"
    unbind '"'
    bind _ split-window -v -c "#{pane_current_path}"
    bind c new-window -c "#{pane_current_path}"

    # Resize panes
    bind -r -T prefix M-Up    resize-pane -U 5
    bind -r -T prefix M-Down  resize-pane -D 5
    bind -r -T prefix M-Left  resize-pane -L 5
    bind -r -T prefix M-Right resize-pane -R 5

    # Alt-shift window navigation
    bind -n M-H previous-window
    bind -n M-L next-window

    # Session/window pickers (wrappers pre-compute claude status)
    bind s run-shell 'tmux-session-picker'
    bind w run-shell 'tmux-window-picker'

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
    set-option -g set-titles-string "#S / #{pane_current_command}"

    # === Status Bar (Catppuccin + multi-line) ===
    set -g allow-rename off
    set -g status-position top
    set -g status-interval 1
    set -g status-style "bg=#{@thm_bg}"

    # Multi-line status bar (tmux 3.4+)
    set -g status 2
    set -g @window_split 999
    set -g @window_split2 999

    # Icon variables
    set -g @icon_session "${icons.session}"
    set -g @icon_branch "${icons.branch}"
    set -g @icon_dir "${icons.dir}"

    # Line 0: Session / Branch / Dir / Claude status (left) | App info (right)
    set -g status-format[0] "#(${tmuxPlugins.continuum}/share/tmux-plugins/continuum/scripts/continuum_save.sh)#[align=left,bg=#{@thm_bg}]#{?client_prefix,#[fg=#{@thm_red}#,bold],#[fg=#{@thm_mauve}]} #{@icon_session} #S  #[fg=#{@thm_blue},bold]#{@icon_branch} #(tmux-branch-display '#{@branch}' '#{pane_current_path}')  #[fg=#{@thm_subtext_0},nobold]#{@icon_dir} #(tmux-dir-display '#{@branch}' '#{pane_current_path}')  #[fg=#{@thm_overlay_1}]#(claude-status --session '#{session_name}' --format icon-color) #[align=right,fg=#{@thm_subtext_0}]#{window_name} #{pane_current_command} "

    # Lines 1-3: Window list (dynamically generated by tmux-reflow-windows hook)
    set -g status-format[1] "#[align=left,bg=#{@thm_bg}]#[fg=#{@thm_overlay_1}] ╰─ #{W:#[range=window|#{window_index}]#{?window_active,#[fg=#{@thm_green}#,bold]#{window_index}: #{window_name},#[fg=#{@thm_subtext_0}#,nobold]#{window_index}: #[fg=#{@thm_fg}]#{window_name}}#{?window_zoomed_flag, 󰁌,}#(claude-status --window '#{session_name}:#{window_index}')#[norange]#{?window_end_flag,, #[fg=#{@thm_subtext_0}#,nobold]│ }}"
    set -g status-format[2] ""
    set -g status-format[3] ""

    # Hooks to reflow windows across status lines
    set-hook -g window-linked      'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g window-unlinked    'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g window-renamed     'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g client-resized     'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g after-new-session   'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'
    set-hook -g client-session-changed 'run-shell "tmux-reflow-windows #{session_name} #{client_width}"'

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

    # Window naming: nerd font icon + branch or dir name
    set -wg automatic-rename on
    set -g automatic-rename-format "#{window_icon} #{?#{@branch},#{=20:@branch}#{?#{==:#{=20:@branch},#{@branch}},,…},#{b:pane_current_path}}"

    # Nerd font window name plugin
    run-shell ${nerd-font-wn}/share/tmux-plugins/tmux-nerd-font-window-name/tmux-nerd-font-window-name.tmux

    # tmux-fingers (smart copy)
    set -g @fingers-hint-style "fg=colour234,bg=colour183,bold"
    set -g @fingers-highlight-style "fg=colour216,bg=colour236"
    set -g @fingers-selected-hint-style "fg=colour234,bg=colour151,bold"
    set -g @fingers-selected-highlight-style "fg=colour116,bg=colour236"
    set -g @fingers-pattern-0 "[A-Z]{2,}-[0-9]+"
    set -g @fingers-pattern-1 "[a-z][a-z_]*_[0-9a-hjkmnp-tv-z]{26}"
    set -g @fingers-pattern-2 "sha256-[A-Za-z0-9+/]{43}="
    set -g @fingers-pattern-3 "sha256:[0-9a-z]{52}"
    run-shell ${tmuxPlugins.fingers}/share/tmux-plugins/tmux-fingers/tmux-fingers.tmux

    # Reload config on first attach (workaround for tmux-fingers colors)
    set-hook -g client-attached[99] 'source-file @tmuxConfPath@'

    # Set pane-border-format with expanded colors (must run after catppuccin loads)
    run-shell "tmux-set-pane-border"
  '';

  # Substitute the self-reference placeholder with the actual output path
  tmuxConf = pkgs.runCommand "tmux.conf" {} ''
    substitute ${tmuxConfText} $out --replace-fail '@tmuxConfPath@' "$out"
  '';

  # --- XDG config for nerd font YAML ---
  nerdFontConfigDir = pkgs.runCommand "tmux-nerd-font-config" {} ''
    mkdir -p $out/tmux
    cp ${nerdFontConfig} $out/tmux/tmux-nerd-font-window-name.yml
  '';

  # --- Wrapped tmux binary ---
  tmux-wrapped = pkgs.symlinkJoin {
    name = "tmux-wrapped";
    paths = [pkgs.tmux];
    nativeBuildInputs = [pkgs.makeWrapper];
    postBuild = ''
      wrapProgram $out/bin/tmux \
        --add-flags "-f ${tmuxConf}" \
        --prefix PATH : ${lib.makeBinPath scripts} \
        --prefix XDG_CONFIG_DIRS : ${nerdFontConfigDir}
    '';
    meta.mainProgram = "tmux";
  };
in {
  inherit tmux-wrapped tmuxConf;
}
