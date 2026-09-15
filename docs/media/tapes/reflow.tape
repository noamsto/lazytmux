# The window list reflows onto more status lines as the client narrows.
# og-demo-reflow (from the runner) attaches through an outer tmux and shrinks it.
Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 16
Set Width 1400
Set Height 560
Set Padding 8
Set Framerate 15
Set Theme "Catppuccin Mocha"

Hide
Type "clear; og-demo-reflow"
Enter
Sleep 2.5s
Show

Sleep 10s
