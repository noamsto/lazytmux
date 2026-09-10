# tmux-og: rename and packaging beyond Nix

Design doc. Renames the project from lazytmux to tmux-og, introduces a single
`og` CLI, and extracts config generation out of Nix so the project can ship
through Homebrew and a shell installer.

## Why

Three separate pressures land on the same decision.

**The name collides and undersells.** `lazy*` is a crowded prefix (lazygit,
lazydocker, lazyvim, lazysql) and reads as membership in a family this project
is not part of. It also describes a light TUI wrapper, while the thing is a
tmux distribution with a control-mode remote bridge, agent status, PR/issue
enrichment, and a persistence layer.

**`tmux-og` is a positioning statement, not a claim.** It winks at the wave of
Rust multiplexers: tmux is still the OG, and here is the batteries-included
distribution that proves it. That pitch only lands if a stranger can install it
without Nix, which makes the rename and the packaging one move rather than two.

**Houston is coming.** A separate repo, already owned, for viewing and
controlling agents from mobile. It is a client of this project, not a
replacement, so this repo stays tmux-anchored and Houston keeps its own name.

## Decisions

| Decision | Choice | Rejected |
|---|---|---|
| Package name | `tmux-og` | `tmuxog` (dies on lowercasing), `og-tmux` (sorts away from the `tmux-*` ecosystem), `tmux-tog` |
| Public CLI | one `og` dispatcher | 48 scripts and 9 binaries on PATH |
| Config generation | extracted to `og generate` | keeping it in Nix, or maintaining two generators |
| Nix module | keeps typed options, serializes to `config.toml` | freeform `settings` passthrough; a second generator |
| Rename migration | hard flip, no compat window | one-release aliases |
| Plugin identity | rename plugin and marketplace | keeping the old marketplace name |

`tmuxOG` with the internal capital was the starting proposal. It is rejected
only because Homebrew formula names, apt package names and Nix attribute names
are all lowercase, so the capitalization that carries the joke does not survive
any packaging channel. `tmux-og` keeps the wink readable in every one of them.

## Architecture

The seam is a single generator that every builder calls.

```
home-manager options ─┐
                      ├─> config.toml ─> og generate ─> tmux.conf
og init (first run) ──┘                       ^          substituted scripts
                                              │                  │
                       Nix flake ─────────────┤                  v
                       Homebrew formula ──────┤            wrapped tmux
                       install.sh ────────────┘
```

Two front doors produce the same config file. One implementation consumes it.
Nix stops generating tmux.conf and becomes a caller like the others.

### What `og generate` may and may not do

It emits files into its own output directory and nothing else. It never
installs a binary, writes a systemd unit or launchd plist, or creates a symlink
outside that output. This restriction is what lets Nix, Homebrew and a shell
installer all call it without fighting over the same files.

### Proposed CLI surface

```
og init          write a commented config.toml with detected defaults
og generate      config.toml -> tmux.conf + substituted scripts
og remote open   what lztmux-remote-open is today
og doctor        what is missing on PATH, here and on each configured remote
```

The existing 48 scripts become subcommands or internal helpers rather than
public PATH surface.

### Install prefix

Today's scripts have Nix store paths substituted at build time (`@lib_icons@`,
`@lib_claude@`, `@claude_status_bin@`). A Homebrew install has no store, so the
prefix becomes an input to `og generate` rather than a build-time constant.
This is the one place where the non-Nix path is not merely a different caller
but a different assumption.

## What moves where

The 18 top-level option groups split along one line.

**Behavior, moves to `config.toml`:** `ghostty`, `kitty`, `worktrunk`,
`remote`, `persist`, `enrich`, `notifications`, `agentUsage`, `claudeStatus`,
`splash`, `aiNaming`, `opencode`, `codexStatus`, `cursorStatus`,
`agentIntegration`, `picker`.

**System wiring, stays Nix:** `startupSession` (systemd unit, launchd agent,
lingering) and `skills` (symlinks into `~/.claude/skills`). These are things a
package manager does, not things a config file describes.

### Guarantees downgrade to checks

Some options today are guarantees rather than settings. When
`remote.exposePickOnPath` is true, Nix does not ask whether the picker is on
PATH, it puts it there. Same for `agentIntegration.tools` (prdash, lazygit,
yazi) and for resvg behind the carousel.

Homebrew can express part of that as formula dependencies. A curl installer
cannot express it at all. So off the Nix path a guarantee becomes a check, and
`og doctor` is what reports it — locally, and over the bridge's existing ssh
transport for each configured remote. This is also the most useful thing to
hand a stranger who cannot work out why their status bar has no PR badge.

## Cross-machine contract

Measured, not estimated. The name appears in 270 files, but almost none of that
crosses a host boundary.

**Unaffected by the rename**, because the daemon reads them off the remote and
none carries the project name: `@claude_status`, `@claude_task`,
`@claude_issues`, `@window_label_*`, `@crew_*`, `@issue_*`, `@pr_*`,
`@bridge_proc`. That is the bulk of the bridge's cross-host surface.

**Also local-only:** every `/tmp/lztmux-*` and `/tmp/lazytmux-*` path,
`@lztmux_theme_applied`, and 30 of the 31 `LZTMUX_*` variables, which are
daemon-to-child and same-revision by construction.

**Genuinely crosses a machine boundary:** any name this repo or one of its
flake inputs owns that is sent to a remote host by bare name. The ownership
qualifier is load-bearing — the literal class "any binary name sent to a
remote" also captures `sh`, `id`, `uname`, `hostname`, `cat`, `stat`,
`getconf`, `ps`, `mktemp`, `find`, `rm`, `printf`, `systemctl`, `launchctl`
and more. All of those are genuinely sent over ssh; none of them is
renameable by anyone here. POSIX, coreutils and init-system names are
**explicitly out of class**.

**Crosses, and this repo owns the name:**

1. `lztmux-remote-picker` — its presence on the remote *is* the capability
   probe. Renamed, an un-rebuilt remote reports "remote lazytmux too old"
   (`scripts/lztmux-remote-picker.sh:227`).
2. `LZTMUX_RELAY_GRAPHICS` — written by the local daemon into the remote
   session's environment and read there
   (`picker/remotebridge/daemon/relayenv.go:15`).
3. The plugin marketplace name — `lazytmux@lazytmux` is what installed users
   have pinned.
4. `tmux-startup.service` and its launchd label
   `org.nix-community.home.tmux-startup` — restarted by name over ssh on the
   cold-start path (`scripts/lztmux-remote-open.sh:224-231`). It survives the
   rename only because the name carries no project prefix; step 3 must keep
   it that way deliberately, not by luck.
5. `tmux` itself — the single most-sent name. `config/tmux.conf.nix`'s
   `tmux-wrapped = pkgs.symlinkJoin` builds `tmux-wrapped`, whose only binary
   is `$out/bin/tmux` with
   `meta.mainProgram = "tmux"`, installed by `modules/home-manager.nix:1022`.
   It is sent to the remote by bare name with a hard-coded per-user-profile
   fallback from `picker/remote.go:39` (the session probe and
   `picker/remote_resources.go:30`'s resource fetch), from
   `scripts/lztmux-remote-open.sh:129`, and from
   `picker/remotebridge/cmd/daemon/main.go:169` (`LZTMUX_BRIDGE_TMUX`
   defaults to the bare name). Like the startup unit, it survives the rename
   only because the name carries no project prefix — step 3 must keep it
   that way deliberately.

**Crosses, owned elsewhere:** `tmux-claude-images`
(`picker/remotebridge/daemon/ctl.go:405,416`, the `aeye` input),
`theme-toggle` (`ctl.go:323,339`, ships from the desktop profile), `prdash`,
`lazygit`, `yazi` (`ctl.go:153-164`, third-party tool binds), and
`tmux-remux` (`scripts/lztmux-remote-open.sh:264`,
`picker/remote.go:70`, a separate repo). None of these is this repo's to
rename.

**The audit surface regenerates from a two-leg grep, not a file list that
rots:**

1. **Executed binaries** — grep `picker/**` and `scripts/lztmux-remote-*.sh`
   for `command -v`, `sh -c` and `ssh`, then filter to names owned by this
   repo or its flake inputs. That leg returns seven files today:
   `picker/remotebridge/daemon/ctl.go`, `scripts/lztmux-remote-open.sh`,
   `picker/remote.go`, `picker/remote_resources.go`,
   `picker/remotebridge/graphics/fetch.go`, `picker/remotebridge/cmd/daemon/main.go`,
   and `scripts/lztmux-remote-picker.sh`. It is "files that execute code on a
   remote host, to be audited then filtered by ownership" — not "files that
   send owned names", which is why `graphics/fetch.go` is on the list despite
   sending only `stat` and `cat`, both out of class.
2. **Published environment** — grep for control-mode `set-environment`. This
   leg exists because `LZTMUX_RELAY_GRAPHICS` crosses by being *written into
   the remote session's environment* rather than executed; leg 1 can never
   return it.

Because the flip is hard, halo and both laptops must be rebuilt in the same
sitting. Either order leaves the probe failing until the last host lands.

## Migration plan

Four steps, each independently revertable.

1. **Add the `og` dispatcher over today's scripts.** No rename, no behavior
   change, both names on PATH. Pure addition.
2. **Extract generation into `og generate`.** The Nix module keeps its typed
   options and serializes them to `config.toml`.
   *Gate: the generated tmux.conf must be byte-identical to today's.* A diff
   means the extraction is wrong.
3. **Flip the name to `tmux-og`.** No aliases, no compat window. Rebuild all
   hosts together. Rename the plugin and the marketplace; existing plugin users
   re-add.
4. **Add the Homebrew tap, install script, and `og init`.** First-run config
   branches off the existing splash gate, which already fires once per tmux
   server and already gates on a global option; a missing config file is the
   same shape of gate.

## Non-goals

- Houston itself. It stays a separate repo and a separate design.
- Splitting the project into multiple repos. The scope growth is real, but the
  components share the tmux substrate and the bridge.
- Distro packages (AUR, deb, copr). Possible later; Homebrew and a shell
  installer cover the reach this is aimed at.
- A GUI or web config editor. `og init` plus a commented TOML is the whole
  configuration story off the Nix path.

## Open questions

- Whether the `og` binary absorbs the existing Go mains (picker, splash,
  statusline, agentdetect, remotebridge) as subcommands or dispatches to them
  as separate binaries. Absorbing gives one artifact to ship; dispatching keeps
  the remote bridge's renderer small, which matters because it is respawned per
  pane.
- Whether `config.toml` is hand-authored on the Nix path too, or remains a
  generated intermediate that a Nix user never sees.
