# Overview: agent status in the status bar, window hopping, session picker.
Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 16
Set Width 1400
Set Height 760
Set Padding 8
Set Framerate 15
Set TypingSpeed 80ms
Set Theme "Catppuccin Mocha"

Hide
Type "clear; tmux attach -t tmux-og"
Enter
Sleep 3s
Show

Sleep 2.5s
# prefix + <n>, not M-L: vhs's Alt+L reaches the pane as ^L.
Type "`2"
Sleep 2s
Type "`3"
Sleep 2s
Type "`s"
Sleep 2.5s
Down
Sleep 1s
Down
Sleep 1.5s
Escape
Sleep 1.5s
