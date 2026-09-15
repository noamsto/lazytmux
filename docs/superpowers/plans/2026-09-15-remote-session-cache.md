# #631 remote session cache — plan

## Goal

The session picker's Remote section is searchable from the first frame. Each
host's last good session list is cached on disk, painted immediately on
launch, and replaced in place when the background ssh probe returns.

## Starting point

- `newPickerModel` paints `pendingRemoteItems`: a header and one `…` host row
  per host, with no sessions. `remoteSessionsForHost`'s children arrive only
  when `collectRemoteItems` returns — after the slowest host's probe, which is
  bounded by `remoteProbeTimeout` (3s), plus another 3s restore probe for a
  serverless host. Until then a query cannot match any remote session.
- Nothing survives between popup launches except the self-alias markers in
  `/tmp/og-remote-self`.
- An unreachable host resolves to a bare `(unreachable — open default)` row,
  and every session it had disappears.

## Steps

1. **Cache per host.** `$XDG_CACHE_HOME/tmux-og/remote/<host>.json` (falling
   back to `~/.cache`) holds `{host, saved_at, sessions}`. The sessions are the
   raw probe list, before bridge suppression, so a mirror detached since then
   reappears. Written only when a probe answered: `remoteProbeOK`, or
   `remoteProbeNoServer` as an empty list. The write is atomic (temp file plus
   rename, 0600) in a 0700 dir.
   - **Trust rule.** Read and write only when the leaf dir is a real directory,
     owned by the user and with no group/other bits. The file must be a regular
     file owned by the user, and its `host` must match the host asked for. The
     host field guards against two hosts sanitising to the same filename.
2. **Paint from cache.** `pendingRemoteItems` puts each host's cached sessions
   under its `…` row. It suppresses sessions already bridged, using the mirror
   rows `newPickerModel` already has (`bridgeHost` plus the
   `<host>-<sess>` name), so first paint forks nothing. A cache older than
   `remoteCacheStaleAfter` (5 minutes) renders dimmed, with a
   `(cached 12m ago)` suffix.
3. **Refresh in the background.** `remoteMsg` still replaces the section
   wholesale. Cached rows share their `remote:<host>:<sess>` targets with live
   ones, so `restoreCursor` and the kept `query` hold across the swap. The one
   change is in `collectRemoteItems`: an **unreachable** probe keeps the
   host's cached children, always marked stale, under the unchanged
   unreachable host row.
   - **Special states are unchanged.** Needs-auth, host-key-changed and
     tailscale check get no cached children and leave the cache file alone.
     Once the probe says a host is inert, no cached row for it survives, so
     Enter cannot act on it.
   - **Before the probe returns.** A cached row there is as actionable as the
     `…` host row already is.
4. **Mirrored sessions (issue step 5).** Remote rows already search on
   `host/sess host sess`, and cached rows reuse that. A local mirror row whose
   name lacks the `<host>-` prefix gains its `@bridge_host` in `searchText`,
   so typing the host narrows across all three row kinds.

## Tests (`picker/`)

- Cache write and read round trip.
- An untrusted cache dir is ignored, for both read and write.
- A host mismatch in the file is ignored.
- A failed probe keeps cached rows and marks them stale, and leaves the cache
  file untouched.
- A special-state probe with a cache present shows no cached children.
- Stale threshold boundary: exactly 5m is fresh, just over is stale.
- First paint shows cached sessions, and a query on the host name matches them.
- A `remoteMsg` merge over cached rows keeps the cursor target and the query.
- `TestMain` points `XDG_CACHE_HOME` at a temp dir, so no test touches the real
  cache.

## Out of scope

- **Issue step 4, monitor-hook cache warming.** Deferred; it needs a `-B` hook
  that follows the #603 rules.
- **A configurable stale threshold.** It is a Go constant until someone asks.
