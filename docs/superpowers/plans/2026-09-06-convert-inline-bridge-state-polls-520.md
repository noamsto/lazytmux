# Convert three inline `@bridge_state` polls to `wait_bridge_disconnected` (#520)

## Problem

`tests/remote-m2-integration.bats` already has `wait_bridge_disconnected`, a
tight-poll helper (200 iterations, 10ms then 20ms) written specifically
because `@bridge_state=disconnected` is a transient stamp — cleared within a
few ms by a warm `--test-local` reconnect. Three reconnect-family tests never
adopted it and instead open-code the poll with `sleep 0.1` at only 40
iterations — the exact interval the helper's own comment says misses the
stamp. This caused intermittent failures in
`checks.aarch64-darwin.remote-m2-integration-tests` (darwin only, never seen
on linux), costing a re-run on PRs #499, #502, #517, #518.

## Verification before editing

- `grep -c wait_bridge_disconnected tests/remote-m2-integration.bats` → 3
  (definition, its internal log-tail line, and the one existing call site).
- `grep -n '"$state" = disconnected'` → 7 hits: 1 inside the helper
  (`return 0`), 3 inline poll/assert pairs at the reported locations.

## Fix

Replace each inline block:

```bash
state=""
for _ in $(seq 1 40); do
	state="$($DST show-options -v -t host-sess -q @bridge_state 2>/dev/null || true)"
	[ "$state" = disconnected ] && break
	sleep 0.1
done
[ "$state" = disconnected ]
```

with a call to the existing helper, using the same `<tag>`/log-file
convention as the one existing call site (`bridge_up <n> <tag>` earlier in
each test):

- `wait_bridge_disconnected drc "$BATS_TEST_TMPDIR/drc.log"` (control-drop
  reattach test)
- `wait_bridge_disconnected drz "$BATS_TEST_TMPDIR/drz.log"` (resize-during-
  outage test)
- `wait_bridge_disconnected lbr "$BATS_TEST_TMPDIR/lbr.log"` (window-labels
  test)

## Out of scope

- No daemon changes — `@bridge_state`'s transience is deliberate.
- The three `[ -z "$state" ]` polls (waiting for the stamp to clear again,
  a steady-state wait, not a transient one) are untouched.
- The helper's own internal `[ "$state" = disconnected ] && return 0` is
  untouched.

## Verification performed

- `nix build .#default`, `nix flake check`, `nix build .#lint` — all exit 0.
- Ran the reconnect-family tests (`bats -f 'reconnect|reattach|resized
  during the outage|killed during the outage|window labels keep tracking'
  tests/remote-m2-integration.bats`) 10x under concurrent `nix build
  --rebuild` load (loadavg ~3–6.7 on a 16-core box) against **both** the
  pre-fix and post-fix test file: 10/10 pass in both cases.
- Could not reproduce the darwin-only flake on linux, as expected — this
  suite has never failed on linux per the repo's own record, and the defect
  is specifically about darwin's slower/more variable CI runners meeting a
  race with a narrower catch window. The fix addresses the verified
  asymmetry (helper exists, 3 sites bypassed it with the exact interval its
  own comment names as insufficient); it does not carry independent local
  reproduction evidence of the flake itself.
