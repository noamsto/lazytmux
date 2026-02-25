# lazygit popup + tmux-which-key

## lazygit

- Bind `prefix + g` to `display-popup -E -w 90% -h 90% lazygit`
- Add `pkgs.lazygit` to the wrapped tmux PATH in `makeBinPath`

## tmux-which-key

- Fetch from GitHub via `mkTmuxPlugin` (same pattern as catppuccin)
- Trigger: `prefix + Space` (replaces `next-layout`)
- Colors: bg `#1e1e2e` (catppuccin mocha base), fg `#cba6f7` (mauve)
- Config: `config/which-key.json`, referenced via nix store path in `@which-key-config`

### Initial config (`config/which-key.json`)

```json
{
  "items": [
    { "key": "g", "type": "popup", "command": "lazygit", "description": "lazygit" }
  ]
}
```
