# Bridge state on format subscriptions, not polling

Issue #566. Replaces the remote-bridge daemon's two option pollers with tmux
control-mode format subscriptions. This document holds the measured tmux
behaviour the design rests on — none of it is documented in a form you can act
on, and two of the facts are the reason the design has the shape it does.

## Why polling was there

A remote window- or pane-option change emits **no control-stream traffic of its
own**. Nothing in `%output`, `%layout-change` or any other notification reports
it. So `labelShipper` and `agentShipper` each ran a round-trip (`list-windows` /
`list-panes -s`) on a 1s floor, and `mainLoopTickInterval` — a coarse 5s ticker
— existed solely to bring the main loop back around on a quiet mirror so those
reads could happen at all. 5s was therefore the worst-case latency for a
codename or a PR badge appearing on a mirror window.

## The mechanism

    refresh-client -B "<name>:<what>:<format>"
      -> %subscription-changed <name> $sess @win idx <pane|-> : <value>

`what` is empty (attached session), a pane id, `%*` (every pane in the attached
session), a window id, or `@*` (every window). Added in tmux **3.2** — see
`CHANGES`, "CHANGES FROM 3.1c TO 3.2": *"Add a way for control mode clients to
subscribe to a format and be notified of changes rather than having to poll."*
Not a new feature; simply never used here. (The tmux **3.8** addition is
`set-hook -B`, a *monitor hook* reusing the same syntax to fire a hook rather
than notify a client — not what this needs, since the daemon is the consumer.)

## Measured, against the pinned `next-3.8` on a scratch server

1. **One notification per changed object**, carrying session id, window id,
   window index and — for `%*` — the pane id. Not one line for the whole scope.
2. **Objects created after subscribing are covered.** A window and a pane
   created while the subscription was live both reported.
3. **Values holding `:` survive.** The separator is the first `" : "`, which
   `controlmode.cutExtSep` already implements for `%extended-output`. An issue
   title in `@window_label_rest_long` routinely holds one.
4. **An emptied value still reports**, because `control.c` writes the separator
   with an unconditional `printf` — the line arrives with a trailing space.
   That line is how "the option was unset" is expressed, and dropping it would
   leave the last non-empty value stamped forever.
5. **Re-subscribing under the same name re-reports every object.** This is the
   replacement for "the poll re-reads everything", available for one command.
6. **Edge-detected at 1 Hz.** `A -> B -> C` inside one second fires once, with
   `C`. No worse than the 1s pollers, but it is not an event log.
7. The argument **must be quoted**: an unquoted `#{...}` is a control-mode
   `parse error: syntax error`, not a passthrough.

## The two facts that shaped the design

**A subscription cannot report a change in the mirror SET.** (2) covers a remote
object appearing, but not the *local* side appearing: a mirror window registered
after its remote value was last reported, and a `retireMirror` rebuild that
re-adds the same remote id against a fresh local window, both leave a mirror
whose stamp is missing while the remote value is unchanged — so nothing fires,
forever. Hence `registry.generation`, bumped by `add` (rebuilds included) and
`remove`: a shipper whose recorded generation is stale re-reads immediately
rather than waiting out its backstop. Dropping the poll without this is how you
get a permanently bare mirror.

**A snapshot is a burst, and the main loop runs a pass per line.** From (5),
every reattach re-reports every window and pane at once. Each applied pass costs
a local fork per shipper (`LocalPanes`, the option writes) and, for labels, a
forced `tmux-reflow-windows`. So `dispatch` only *queues* the row; the loop
applies, and holds while more lines are already buffered on the pump
(`queuedApplyDue`, `len(pump.lines) == 0`), bounded by `queuedApplyMaxHold` so a
stream that never goes quiet cannot hold rows indefinitely.

## Shape

- `controlmode`: new `SubscriptionChanged` kind. `handleAsideLine`'s `default`
  already queues an unknown notification met inside a reply reader, so no change
  there.
- Subscriptions installed per attach — in `Run` after the shippers are built,
  and at the end of `repair`, because they are per *client*. A round-trip rather
  than a bare send: an `%error` (a remote older than 3.2) leaves that shipper
  polling on its original 1s floor for the life of the connection.
- The subscribed format **is** the shipper's own `-F` format, so the push path
  and the backstop cannot describe different rows. Both formats begin with their
  object's id, which is why nothing parses the ids out of the notification.
- `agentShipper.apply` splits: `stamp` writes rows, `apply` = `stamp` + reap.
  Reaping treats a pane absent from the rows as gone, so only the backstop —
  the one caller holding the whole set — may do it. Reaping against a
  one-pane notification would delete every other pane's state.
- `mainLoopTickInterval` stays 5s: it also clocks the maintenance sweep,
  `reseedDropped`/`reseedReshaped` and `retryFailedShapes`.

## Verification

`tests/remote-m2-integration.bats` 32 and 33 change a value on the fake remote
*after* the mirror has settled, with no window or pane created — so the registry
generation is unchanged and the next backstop read is 30s away, outside the
budget. Only a `%subscription-changed` can carry it.

Negative control (run, not assumed): subscribing under a name `dispatch` does
not match makes exactly those two fail, and nothing else — which also
demonstrates the poll fallback still carries the rest of the suite on its own.
