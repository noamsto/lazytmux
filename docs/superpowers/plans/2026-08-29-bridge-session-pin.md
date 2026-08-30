# Bridge session pinning (#396)

`sesh connect <dir>` inside a mirrored window froze the mirror.

## Diagnosis

Reproduced on a scratch tmux socket with the real `sesh` binary, a control-mode
client attached to session A:

```
=== pane content (out-of-band capture-pane) ===
 sesh connect /tmp/seshtarget; echo SESH_RETURNED
SESH_RETURNED                     <- command completed
                                  <- prompt returned
=== foreground cmd ===
cmd=fish                          <- nothing blocked
=== control stream ===
%session-changed $0 A             <- initial, at attach
%session-changed $4 seshtarget    <- the switch
=== SESH_RETURNED in the stream? ===
typed echo only, all BEFORE the switch
```

A control-mode client receives `%output` only for the session it is currently
attached to. A mirror pane's keystrokes reach the *remote* shell, where `$TMUX`
is set, so `sesh` runs `switch-client` with no `-c` and tmux resolves "current
client" to the bridge daemon — the only client the bridged session has.

Nothing hangs. The remote command ran to completion and the prompt came back;
only the output stream was redirected to another session's panes. What reads as
a hang is a live shell whose output pipe stopped.

This is not a `sesh` bug: `prefix + s` on the remote, tmux-sessionizer and
`tmux new -s foo` from a bridged shell all do the same thing. Replacing `sesh`
would only move the trigger.

## Two behaviors the fix rests on, both verified

1. `display-message -p -F '#{client_name} #{session_id} #{session_name}'` sent
   over the control stream resolves to *our own* control client
   (`client-70541 $0 A`), so no client name has to be plumbed through.
2. `switch-client -t '$0'` sent over our own stream, **with no `-c`**, switches
   us back — `%session-changed $0 A`, and `%output` for our panes resumes.

The same run confirmed output produced during the excursion never arrives: it is
dropped by the server, not buffered. Hence the reseed.

## Design

Pin, reseed, hand off (`picker/remotebridge/daemon/sessionpin.go`):

1. Resolve the mirrored session's id once at startup, from the remote — not from
   the first `%session-changed`, whose arrival order is not evidence of which
   session the bridge belongs to. A non-`$N` reply disables pinning rather than
   reaching a command.
2. On a `%session-changed` naming another id, `switch-client -t '$N'`.
3. Reseed every mirrored pane. Without this the panes are live but stale — the
   excursion swallows the command that caused it and the prompt after it.
4. Hand the session we were switched to off to `lztmux-remote-open <host>
   <sess>`, which reuses or starts its bridge and switches the local client to
   it, so the gesture lands where the user meant.

### Rejected: follow the switch

Rebuilding the mirror windows for the new session in place matches `sesh`
semantics more literally, but it breaks the one-local-session ↔
one-remote-session invariant that `@bridge_host`, `@bridge_win` and
`lztmux-remote-detach` all assume, and mixes two remote sessions' history into
one local session.

## Known limit

`capture-pane` restores the visible screen, not scrollback produced during the
excursion. A `switch-client` excursion lasts milliseconds, so nothing meaningful
is lost; a remote command that switched away, printed hundreds of lines and
switched back would lose them.

## Verification

- `daemon/sessionpin_test.go` — parse, the two no-op cases (our own id; pinning
  disabled), id validation, and one pass asserting switch-back + FrameSeed +
  hand-off argv.
- `tests/remote-m2-integration.bats` — end-to-end over the offline `--test-local`
  seam: a real `switch-client` excursion, then the control client is back on the
  bridged session, the mirror paints output produced after the pin, and the
  hand-off stub receives `<host> <session>`. Confirmed red with the dispatch case
  stubbed out (`[ "$back" = yes ]' failed`), green with it.
