# Session picker (prefix + s), then window picker (prefix + w).
Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 16
Set Width 1400
Set Height 760
Set Padding 8
Set Framerate 15
Set TypingSpeed 120ms
Set Theme "Catppuccin Mocha"

Hide
Type "clear; tmux attach -t tmux-og"
Enter
Sleep 3s
Show

Sleep 1s
Type "`s"
Sleep 2s
Down
Sleep 0.8s
Down
Sleep 1s
Type "dot"
Sleep 1.5s
# The first Escape clears the filter, the second closes the picker.
Escape
Sleep 0.5s
Escape
Sleep 1s
Type "`w"
Sleep 2s
Down
Sleep 0.8s
Down
Sleep 0.8s
Down
Sleep 1.5s
Escape
Sleep 1s
