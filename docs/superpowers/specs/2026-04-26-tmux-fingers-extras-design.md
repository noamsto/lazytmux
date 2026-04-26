# tmux-fingers: extra patterns and pane-path keybind

Date: 2026-04-26
Status: Draft

## Motivation

Two papercuts in the current tmux-fingers setup:

1. The pattern set covers Linear/Jira tickets, TypeIDs, and Nix hashes, but
   misses several other "ID-shaped" strings that show up in real terminal
   output: bare ULIDs, IPv6 addresses, email addresses, AWS ARNs, JWTs, MAC
   addresses.
2. Copying the active pane's current working directory requires a round-trip:
   run `pwd`, trigger fingers, pick the printed path, copy. tmux already knows
   `pane_current_path` — there should be a one-keystroke shortcut.

## Current state

`config/tmux.conf.nix` (lines 387–396) registers four custom fingers patterns
on top of the plugin's built-ins:

| # | Pattern | Matches |
|---|---|---|
| 0 | `[A-Z]{2,}-[0-9]+` | Linear / Jira tickets |
| 1 | `[a-z][a-z_]*_[0-9a-hjkmnp-tv-z]{26}` | TypeIDs (`prefix_<crockford26>`) |
| 2 | `sha256-[A-Za-z0-9+/]{43}=` | Nix SRI hashes |
| 3 | `sha256:[0-9a-z]{52}` | Nix sha256 (nix32 base32) |

Built-ins on top: `ip` (v4), `uuid` (v4), `sha`, `digit`, `url`, `path`, `hex`,
`kubernetes`, `git-status`, `git-status-branch`, `diff`.

Pattern 1 is already a correct TypeID matcher (verified against the spec).

## Decisions

### Why no TypeID/ULID conflict

tmux-fingers joins all enabled patterns into one alternation regex
(`hinter.cr:94`: `Regex.new("(#{patterns.join('|')})")`) and then runs `gsub`
left-to-right, advancing past each match.

For `user_01h455vb4pex5vsknk084sn02q`:

- At position `u`: TypeID alternative matches the full 31-char string. `gsub`
  advances past `q`. The 26-char suffix is never re-scanned.
- For a bare uppercase `01ARZ3NDEKTSV4RRFFQ69G5FAV` (no `prefix_`): TypeID
  fails (no underscore separator), ULID matches.
- Inside a TypeID prefix, ULID can't match anyway because `_` is not in the
  Crockford alphabet.

Adding ULID does not double-hint TypeIDs.

### Why uppercase-only ULID

Canonical ULID form is uppercase. Restricting to uppercase avoids the
hypothetical edge case where a bare lowercase 26-char Crockford string
overlaps with a TypeID suffix in some unusual rendering. If lowercase ULIDs
turn up in real workflows, a second pattern can be added.

### Why eza bare filenames are not addressed

Built-in `path` requires a `/` to disambiguate filenames from prose. `eza`,
`ls`, and `lsd` all output bare filenames in their default modes, so neither
the built-in pattern nor any reasonable replacement catches them without
heavy false positives (e.g. matching `Mr. Smith`, `version 1.2.3`).

ANSI colors and Nerd Font icons are not the problem — fingers reads the
rendered screen with ANSI stripped, and the regex starts at the filename
after the icon. The lack of `/` is a fundamental regex limitation, not an
eza-specific one. Accepting this limitation.

### Why a separate keybind instead of fingers main-action override

The user pain is "copy the pane's current path". The cleanest fix is a single
keybind that pulls `pane_current_path` directly — no fingers involvement, no
`pwd` round-trip.

A fingers `main-action` override (smart `~`-expansion using
`pane_current_path` as `$PWD` inside the action) was considered. It is global
across all match types, which means replacing the built-in `:copy:` mechanism
and re-implementing clipboard auto-detection. Too much surface for the
problem at hand. The keybind solves the stated pain in one line.

## Changes

All edits land in `config/tmux.conf.nix`.

### 1. Six additional fingers patterns

Append after the existing pattern 3 lines, in the same `# tmux-fingers ...`
block:

```tmux
set -g @fingers-pattern-4 "[0-9A-HJKMNP-TV-Z]{26}"
set -g @fingers-pattern-5 "([0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{1,4}"
set -g @fingers-pattern-6 "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"
set -g @fingers-pattern-7 "arn:[a-z0-9-]+:[a-z0-9-]+:[a-z0-9-]*:[0-9]*:[a-zA-Z0-9_/.:-]+"
set -g @fingers-pattern-8 "eyJ[A-Za-z0-9_-]{10,}\\.[A-Za-z0-9_-]{10,}\\.[A-Za-z0-9_-]{10,}"
set -g @fingers-pattern-9 "([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}"
```

Pattern intent (kept as code comments next to each line):

| # | Intent |
|---|---|
| 4 | ULID (uppercase canonical, 26-char Crockford base32) |
| 5 | IPv6 (full + most compressed forms; misses pure `::1`, accepted) |
| 6 | Email |
| 7 | AWS ARN (6-field structure) |
| 8 | JWT (`eyJ`-anchored to avoid false positives on dotted base64) |
| 9 | MAC address (`:` and `-` separators) |

### 2. New `prefix + Y` keybind

Add to the keybindings section of `tmux.conf.nix`:

```tmux
bind Y run-shell 'tmux display-message -p "#{pane_current_path}" | wl-copy'
```

Behavior: copies the active pane's current working directory to the Wayland
system clipboard. Follows the tmux-yank convention for capital `Y` ("yank pane
CWD"). No collision with existing lazytmux bindings.

## Risks and mitigations

- **Visual noise from new matches.** Each pattern is targeted enough that most
  screens add zero new hints; auth/cloud/network screens add a handful.
  tmux-fingers handles 100+ matches per screen without issue. If noise becomes
  a problem the more likely culprits are built-ins (`digit`, `path`, `sha`),
  which can be narrowed via `@fingers-enabled-builtin-patterns`. Out of scope
  for this change.
- **`wl-copy` assumption.** The keybind hardcodes Wayland. The repo's existing
  theme/colors script already assumes this environment. If multi-environment
  support is needed later, route through a `lazytmux-copy` wrapper that
  detects `wl-copy` / `xclip` / `pbcopy` like fingers' built-in `:copy:` does.
  Out of scope.
- **Pattern regression on reload.** Fingers re-initialises on
  `client-attached` (existing hook at line 396). New patterns will pick up on
  the next attach without a full tmux restart.

## Out of scope

- Bare filename matching for `eza` / `ls` output.
- Lowercase ULID pattern.
- Smart `~`-path expansion via fingers main-action override.
- Tightening the built-in pattern allowlist.
