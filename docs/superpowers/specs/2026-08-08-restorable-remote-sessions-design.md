# Restorable Remote Sessions — Design Notes (#268)

## Host verification

`tmux-remux`'s `Manifest.Host` (and the mirrored `store.Event.Host` column) is
whatever `os.Hostname()` returned on the machine that wrote the snapshot — not
necessarily the ssh `Host` alias used to reach it (`@remote_bridge_hosts` can
alias, tunnel, or shadow a real hostname). `sshListRestorableSessions` fetches
the remote's own `hostname` output in the same ssh round trip as the manifest
and rejects a mismatch outright, rather than trusting that whichever `state.db`
answered belongs to the host the picker thinks it's talking to.

## Throwaway-session filter

Chosen signal: `session_last_attached == 0` (tmux's own "no client ever
attached" value). This is principled rather than a name denylist
(`probe-verify` was the specific symptom, not the rule) — any session created
and abandoned without a human ever attaching reads the same way, regardless of
what it happens to be called.

## Why `tmux-remux restore` bypasses `restoreMode=off`

`RestoreCmd.Run` only respects the `off` gate when invoked with `--auto`
(`if cfg.RestoreMode == config.RestoreOff && c.Auto { return nil }`). That gate
exists to keep *automatic* restore-on-server-start opt-in. Activating a
restorable row from the picker is an explicit, one-off user action, not the
server's own boot sequence — so `lztmux-remote-open` calls the bare `restore`
(no `--auto`), which always attempts the restore regardless of the remote's
configured mode.

## Why the restore branch is env-var-gated, not always-on

`tests/remote-cold-start.bats`'s `"cold start: an explicit session argument
skips both the probe and the unit"` test locks in that a live-session attach
(the common case: the picker already knows the session is live from its own
prior probe) takes zero extra ssh round trips. Since the script alone cannot
tell "this name came from a live probe" from "this name came from a snapshot"
without probing — and probing unconditionally would violate that test's
invariant — the picker signals it explicitly via `LZTMUX_REMOTE_RESTORE=1`,
set only on rows built from `sshListRestorableSessions` (`remoteRestore: true`
in `picker/tui.go`'s `listItem`).

## Restore filter mismatch (known limitation)

The picker lists every non-throwaway session in the newest snapshot
(`last_attached != 0`). It does **not** replicate `tmux-remux restore`'s own
smart filter (`internal/filter.Filter`, wired up in `RestoreCmd.Run`): by
default that filter also drops sessions whose windows are all idle plain
shells (`RestoreSkipIdleShells`/`RestoreSkipIdleWindows`), sessions older than
`RestoreMaxSessionAge` (14d default), and entire snapshots older than
`RestoreMaxSnapshotAge` (30d default).

This isn't an oversight: `internal/filter` lives under `tmux-remux`'s own
`internal/` tree, so Go's internal-import rule makes it unimportable from this
repo's separate `github.com/noamsto/lazytmux/picker` module. Reimplementing
the filter here would duplicate upstream logic that can silently drift
(defaults, new flags), and even a faithful copy couldn't see the remote's
*actual* configured overrides without another ssh round trip (`tmux-remux`
has no `config show`/`restore --dry-run` to query them). Given that, this plan
does not attempt to pre-filter rows to match what `restore` would keep.

The consequence: pressing Enter on a listed session can restore nothing if
`tmux-remux restore`'s own filter would have skipped it. `lztmux-remote-open`
makes this honest rather than silent — when the session is still missing
after a successful `restore` call, it fails with a message naming the filter
as the likely cause (`tmux-remux's restore filter may have skipped it (idle
shells / stale age)`) instead of a bare "not found".

## #267 and scope

#267 (`feat(module): startupSession.headless for GUI-less hosts`) shipped
before this issue was picked up. It reduces how often a remote goes fully
serverless (a systemd/launchd unit can now stay up without a GUI session) but
doesn't eliminate the case — a fresh boot before the unit starts, a disabled
persist module, or manual maintenance all still produce a serverless remote
with snapshots on disk. Scope was kept at the full listing + restore path
(not trimmed to listing-only); see the crew-bus message sent alongside this
work for the explicit call-out.
