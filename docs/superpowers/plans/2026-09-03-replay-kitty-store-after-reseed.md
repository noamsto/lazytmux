# Plan: replay retained kitty stores after every mirror re-seed

Issue: #465.

Root cause in one line: a mirror re-seed restores only `capture-pane -e -p`
grid state, but kitty image stores are out-of-band APC payloads, so the
placeholders come back without the localised store they reference and the image
goes blank.

Fix: retain the last localised store per `(pane, image id)` inside that pane's
`graphics.Proxy`, evict it on `a=d`, bound the retained set with a small FIFO/LRU
cap, and replay the retained stores immediately after every `FrameSeed` path.

## Step 1: teach `graphics.Proxy` to retain replayable stores

- [ ] Extend `picker/remotebridge/graphics/proxy.go` so `Filter` keeps the
      encoded wrapped bytes of each surviving localised store, keyed by kitty
      image id, after `Rewrite` has already pointed it at the local cache copy.
- [ ] Add delete handling there too: an `a=d` for id `N` must evict the retained
      store for `N` before the delete is forwarded, so later replays cannot
      resurrect a killed image.
- [ ] Bound retention per pane: keep only the newest retained store for each id,
      and cap the total ids held by one proxy. When the cap is exceeded, evict
      the oldest retained id wholesale.
- [ ] Add a `Replay() []byte` method returning the retained wrapped stores in
      stable oldest-to-newest order, ready to append after a seed with no new
      fetch and no remote round-trip.

## Step 2: prove the retention behavior in graphics unit tests

- [ ] Add tests in `picker/remotebridge/graphics/proxy_test.go` covering:
      retained stores replay after `Filter`,
      `a=d` evicts replay state,
      retention is per proxy instance,
      and the cap keeps only the newest `N` ids.
- [ ] Make the tests non-vacuous by asserting the old behavior first where
      useful: before the implementation, replay should be empty even after a
      store passed through, while the new assertions require the retained
      wrapped payloads to exist.

## Step 3: replay after every `FrameSeed` path, never before

- [ ] Add one daemon helper that takes a sink plus seed bytes and enqueues the
      `FrameSeed` first, then any replay bytes from that sink's proxy state.
- [ ] Use that helper in all three re-seed paths:
      `picker/remotebridge/daemon/reconcile.go` layout-change reseeds,
      `picker/remotebridge/daemon/daemon.go` `%continue`,
      and `picker/remotebridge/daemon/daemon.go` `reseedDropped`.
- [ ] Keep the replay on the sink path only. Do not issue any nested round-trip
      from a `PaneSeeds` callback; the replay must be derived entirely from the
      proxy's retained local bytes.

## Step 4: let pane teardown drop retained state naturally

- [ ] Store the proxy on the owning `outputSink`, so replay reads the same
      per-pane state the output pump writes.
- [ ] Rely on existing pane teardown (`Router.Unregister` -> `outputSink.Close`)
      to drop the sink and therefore its proxy state; no global registry or
      daemon-wide cache.
- [ ] Add a regression test showing unregister/close discards replay state by
      constructing a fresh sink/proxy after teardown and asserting nothing
      carries over.

## Step 5: cover the three reseed paths end-to-end

- [ ] Add or extend daemon tests so each re-seed path proves replay happens
      after the seed:
      `reconcilereseed_test.go` for layout reshape,
      `dropreseed_test.go` for dropped-frame recovery,
      and a `%continue`-path test in `daemon_test.go` or adjacent coverage.
- [ ] Assert ordering explicitly: first frame is `FrameSeed`, later payload on
      the same sink includes the retained kitty store.
- [ ] Keep the existing sink/pipe seams; no live remote required.

## Step 6: validate locally

- [ ] `shellcheck` any edited shell script if this diff touches one.
- [ ] `cd picker && go test -race -count=1 ./remotebridge/...`
- [ ] `nix build .#default`
- [ ] `nix flake check`
- [ ] `nix build .#lint`

All three Nix gates are required and none subsumes another.
