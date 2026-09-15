# Window wall (prefix + W): live previews of every window.
Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 16
Set Width 1400
Set Height 760
Set Padding 8
Set Framerate 15
Set Theme "Catppuccin Mocha"

Hide
Type "clear; tmux attach -t tmux-og"
Enter
Sleep 3s
Show

Sleep 1s
Type "`W"
Sleep 2.5s
Right
Sleep 1s
Right
Sleep 1s
Down
Sleep 1.5s
Escape
Sleep 1s
