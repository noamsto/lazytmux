# Session picker: don't let a pulled-in host row steal the cursor; sink the current session below a same-name mirror (#551)

Two independent `tmux-session-picker` (`picker/`, `scripts/tmux-session-picker.sh`,
`config/tmux.conf.nix`) usability fixes, shipped in one PR.

## A. A pulled-in Remote host row must not steal the cursor while filtering

### Mechanism (verified against `picker/tui.go`)

`withFilter` (~1301-1432) scores every non-header item, remote **host rows**
(`remoteHost != "" && remoteSess == ""`) included. In the non-window-mode
branch (~1392-1430), a host row can enter the visible list two different ways:

1. **Its own `searchText` matched the query** — it's a normal member of
   `matches`, appended via the final `out = append(out, match.item)` in the
   loop, at its natural scored/collection-order position.
2. **Only a child session matched** — the `hostRows`/`seenHost` block
   (~1408-1422) pulls a copy of the host row out of the `hostRows[h]` map
   (built once from `m.allItems`) and inserts it just before the first
   matching child, purely as tree context so the `├─`/`╰─` prefix never
   dangles. Guarded by `match.item.remoteSess != ""` — this branch only fires
   when the *triggering* match is a session row, never the host row's own
   match — and `seenHost[h]`, set the first time either path touches `h`, so
   a host row that matched on its own (case 1, processed first because
   `collectRemoteItems` (`picker/remote.go:976-989`) places a host row before
   its children and `rankRemote` ties are stable/collection-order under
   `sort.SliceStable`) can never also go through case 2 for the same query.

`isSelectable` (~1129) currently can't tell these apart — a host row is
selectable whenever `item.target != "" || item.remoteHost != ""`, true for
every host row (`target = "remote:" + host`, `picker/remote.go:830`, is always
non-empty) regardless of which path put it in `visible`.

### Fix

- Add `remoteContextOnly bool` to `listItem` — true only on the copy inserted
  via the case-2 path above; never set on `m.allItems`' own host-row entries,
  so the empty-query path (which copies `m.allItems` verbatim, ~1305-1319) and
  case-1 matches are untouched.
- `isSelectable` treats a host row (`remoteHost != "" && remoteSess == ""`)
  with `remoteContextOnly` set the same as a header: not selectable.
  `firstSelectable`, `moveCursor`, `listIndexAt` all route through it — no
  other change needed there. **Verified exception**: `restoreCursor`
  (~1165-1172) matches on `item.target`, not `isSelectable`, and a host row's
  `target` is non-empty. This is unreachable in practice: parking on a
  context-only host row would need `keep == "remote:<host>"` while that exact
  row is a non-match, but every query-change path resets to
  `firstSelectable(0)` (the handlers around ~504/637/645/664/674), and
  `restoreCursor` itself is only ever called from the `remoteMsg` handler
  (~387-395, the async remote-items load), which runs against a *fixed* query
  — a host row's own match status can't change there. No code change to
  `restoreCursor`.
- `markRemoteTreeEnds` / `pruneOrphanHeaders` operate purely on
  `display`/`plain`/tree-glyph fields and header-adjacency, never
  selectability — read both fully, no change needed.
- Empty query (~1305-1319): copies `m.allItems` unfiltered by mode only, never
  touches the `hostRows` mechanism — host rows keep whatever selectability
  they had before filtering (always selectable, matching today).
- **Extra robustness** (closes a class the task calls out — "no cursor
  parking on a skipped row"): both `refreshMsg` (~355-362) and `zoxideMsg`
  (~379-381) only reset the cursor when it falls out of bounds
  (`m.cursor >= len(m.visible)`), not when a rebuild's reordering lands an
  *in-bounds* but now-unselectable row (a context-only host row, or
  pre-existing headers) under a fixed index. `zoxideMsg` is benign today
  (zoxide rows always sort last, so the prefix under the cursor can't shift),
  but harden both identically rather than leave an inconsistency: change the
  guard to `if m.cursor >= len(m.visible) || !m.isSelectable(m.visible[m.cursor])`
  (then `m.cursor = m.firstSelectable(0)`, matching the convention every other
  cursor-reset site already uses). Pre-existing gap for headers; Part A widens
  the set of unselectable rows enough to make it worth closing in this PR.
- **`^o` (browse the remote's own picker) is not lost on a context-only host
  row**: `remotePickHost` (`picker/remotepick.go:92`) resolves off a session
  row's own `remoteHost` field, which `collectRemoteItems` sets on every
  child row too (`picker/remote.go:982,996`) — independent of the parent host
  row's selectability. Part A only changes whether the *host* row itself can
  hold the cursor, not what a selected *child* row can do. Worth a line in the
  PR body so it doesn't read as a browse regression.
- `remoteHostRowItem`'s doc comment (`picker/remote.go:811-815`, "The host row
  is always selectable...") becomes false after this change — update it to
  describe the two paths and which one keeps that property.

## B. The current session sorts below a same-name session on a different host

### Mechanism (verified)

- `buildSessionItems`'s `sort.Slice` (~1712-1717) orders `[]sessionData` by
  activity desc, then name asc.
- `sessionDisplayName(name, bridgeHost)` (`picker/remote.go:795`) strips a
  mirror's `<host>-` prefix for display; a local `lazytmux` and a `g6` mirror
  of remote `lazytmux` (raw name `g6-lazytmux`) both display as `lazytmux`.
- The currently attached session is usually the most recently active, so it
  wins the activity sort and sits on top — the one row nobody wants to jump to.
- `sessionData` (`picker/main.go:52`) carries no "is this the session the
  invoking client is attached to" marker today.

### Verified: the filtered (non-empty query) path needs the same fix, independently

`withFilter`'s scored sort (~1342-1368) ranks `rankSession` items by
`fuzzyScore(item.searchText, query)` (searchText = raw session name, e.g.
`lazytmux` vs `g6-lazytmux`). Probed directly with a throwaway test
(`fuzzyScore("lazytmux","lazytmux")` vs `fuzzyScore("g6-lazytmux","lazytmux")`,
not part of the diff): **`local=218 mirror=200`**. Scores do not tie — the
local name's match starts at a string boundary (`prev=charWhite` →
`fzfBonusBoundaryWhite=10`), the mirror's starts just past the `-`
(`classOf('-')` is `charNonWord`, **not** `charDelimiter` — confirmed by
reading `classOf`, ~2516-2530 — so `charBonus` returns `fzfBonusBoundary=8`,
not `fzfBonusBoundaryDelimiter=9`). Verified per-character by probing
`fuzzyScore` against successive prefixes of the pattern: the first matched
char alone contributes a 4-point gap (boundary bonus `10` vs `8`,
`×fzfBonusFirstCharMultiplier=2`), and — because `cb := max(fzfBonusConsecutive,
prevBonus)` (~2606) carries the *first* char's boundary bonus forward through
the whole consecutive run — each of the remaining 7 characters *also* differs
by 2 (`10` vs `8` again, unmultiplied). Total gap: `4 + 7×2 = 18`, matching the
measured `218` vs `200`. So the bare name **always** outscores the host-prefixed one for
a query matching both, regardless of which one is current — exactly backwards
when the *bare/local* session happens to be the currently attached one. The
filtered path needs its own fix; score cannot provide it.

### Design pass 1 was wrong — a pairwise override in the comparator is not transitive

The first draft of this plan proposed folding a pairwise rule directly into
`sort.Slice`'s `less` ("if `si.current != sj.current && collide`, hard-override
the pair's order"). **plan-critic pass 1 caught a real bug**: this makes
`less` intransitive. Concrete cycle, verified by hand-tracing: current local
`A` (activity 100), its mirror `B` (activity 50), an unrelated session `C`
(activity 75, no name collision with either) — `less(A,C)` is true (activity:
100>75), `less(C,B)` is true (activity: 75>50), but `less(B,A)` is *also* true
(the pairwise override: `B`/`A` collide, `A` is current, so `B` must sort
before `A`). `A<C<B<A` is a 3-cycle — Go's `sort.Slice`/`sort.SliceStable` are
documented to require a strict weak ordering and produce an **unspecified**
result otherwise (confirmed: hand-tracing insertion sort over the 6
permutations of `[A,B,C]` gives the wanted `B` before `A` in only some of
them — e.g. `[A,C,B]` converges to `[A,C,B]`, the override never fires because
`A` and `B` are never compared as adjacent neighbours). Since
`panesSnapshot.sessions()` builds its slice from **map** iteration
(`picker/main.go:198-209`), the input permutation is random per run, so this
bug would present as the fix "sometimes working" — exactly the kind of defect
a 2-element hand-written unit test cannot catch (verified: the critic's
own trace shows a naive 2-session test always passes).

**Fix: a stable post-pass, not a comparator override**, for both sort sites.
At most one session is ever `current` (a client attaches to exactly one
session), so each post-pass moves at most one row — the exact one
`WORKER_TASK.md` names.

- `sessionData` sort (`picker/tui.go` ~1712-1717): extract the *existing,
  unchanged* activity/name sort into `sortSessionsForDisplay(sessions
  []sessionData)`, then apply `sinkCurrentBelowPeers`:
  ```go
  // sameDisplayDifferentHost reports whether two sessions display under the
  // same name but live on different hosts (bridgeHost=="" counts as local) —
  // the collision that makes "current" ambiguous with a session the user is
  // actually trying to reach.
  func sameDisplayDifferentHost(nameA, hostA, nameB, hostB string) bool {
  	return hostA != hostB && sessionDisplayName(nameA, hostA) == sessionDisplayName(nameB, hostB)
  }

  // sinkCurrentBelowPeers runs after the ordinary activity/name sort. It moves
  // the currently attached session (at most one ever exists) to immediately
  // after the LAST session it display-collides with on another host. Every
  // OTHER session keeps its relative order to every other one — but a session
  // that sat between the current one and its peer is, unavoidably, no longer
  // between them (e.g. [cur(100), other(90), mirror(50)] -> [other, mirror,
  // cur]): the requirement is "current sorts below its peer", which forces
  // that collateral shift. This must be a stable
  // post-pass rather than a rule folded into sort.Slice's comparator — a
  // pairwise "current loses" rule inside the comparator is not transitive
  // (three sessions whose activity interleaves across the collision produce a
  // comparator cycle; verified by hand-trace, see plan history) — and
  // sort.Slice's behavior on a non-transitive comparator is unspecified, which
  // would make this fix pass or silently no-op depending on map-iteration
  // order.
  func sinkCurrentBelowPeers(sessions []sessionData) {
  	i := -1
  	for idx, s := range sessions {
  		if s.current {
  			i = idx
  			break
  		}
  	}
  	if i < 0 {
  		return
  	}
  	cur := sessions[i]
  	lastPeer := -1
  	for j, p := range sessions {
  		if j != i && sameDisplayDifferentHost(cur.name, cur.bridgeHost, p.name, p.bridgeHost) {
  			lastPeer = j
  		}
  	}
  	if lastPeer <= i {
  		return // already below every peer, or no peer present
  	}
  	copy(sessions[i:lastPeer], sessions[i+1:lastPeer+1])
  	sessions[lastPeer] = cur
  }
  ```
  `buildSessionItems` calls `sortSessionsForDisplay` (which internally calls
  the plain sort then `sinkCurrentBelowPeers`) instead of its inline
  `sort.Slice`.
- `withFilter`'s scored sort (~1342-1368): leave the existing
  `sort.SliceStable` comparator **untouched** (no pairwise rule added — that
  was pass 1's second mistake, caught by the same critic finding:
  `sort.SliceStable`'s insertion sort only compares neighbours, so a
  pairwise rule silently no-ops whenever another match's score falls between
  the colliding pair — verified by hand-trace: matches in score order
  `[local(218), other(210), mirror(200)]` never places `local`/`mirror`
  adjacent, so a pairwise check between them never runs). Instead, after the
  sort, find the contiguous `rankSession` prefix (ranks are ordered
  session/remote/zoxide, so it's a prefix — hoist the `rank` closure at
  ~1348 out to function scope so both the comparator and the prefix scan can
  call it, rather than duplicating the switch) and apply the identical
  post-pass logic to it, as a local closure (matches's `scored` type is
  file-local to `withFilter`, so this stays inline rather than hoisted to a
  package-level function — the same algorithm, operating on
  `matches[i].item.current`/`.session`/`.bridgeHost` instead of
  `sessionData` fields). Guard it with `!m.windowMode` explicitly even though
  window-mode rows never set `current` today (so it would already be a
  no-op) — states the "window mode is out of scope" decision in code, not
  just relies on an unset field.
- **Decision, stated explicitly per the task's ask**: this DOES need the
  identical fix, independently — score alone cannot express "current sinks
  below its peer" because score has no notion of which session is current
  (see the measured 218/200 above). If the query matches the current session
  but not its peer (the peer never entered `matches`), the post-pass is a
  no-op (`lastPeer` stays `-1`) — there's nothing to sink below in the
  visible list, so leaving it at its natural score position is correct.

### Sourcing `current`: bind-time format, not a live fork inside the collector

**Design pass 1 was also wrong here**: it proposed `currentSessionName()`
calling `tmux display-message -p '#{client_session}'` from *inside*
`panesSnapshot.sessions()`, reasoning by analogy to `newSessionSizeArgs`
(`picker/zoxide.go:276`, which reads `#{client_width}|#{client_height}` the
same way). plan-critic pass 1 flagged two real problems: (1) `sessions()` runs
on every `refreshDataCmd` tick (~1534-1552) and again from
`collectZoxideItems`→`collectSessions()` (~1881) — an extra `tmux` fork per
call on a file whose own comments stress round-trip latency; (2) it makes
`TestSessionHeaderLabelsAndAlignment`/`TestSessionsBridgeProcOverride`
(`picker/main_test.go:69-141`, whose fixture session is literally named
`lazytmux`) resolve a **real** live client whenever `go test` happens to run
inside a tmux session named `lazytmux` — this repo's own dev session is
routinely named exactly that, so this isn't a hypothetical.

**Fix: thread it through the keybind that already exists, at zero extra cost.**
`config/tmux.conf.nix:865` already resolves `#{client_name}` at bind time (the
moment the key is pressed, in the correct pane/session context) and passes it
as `--client` to `scripts/tmux-session-picker.sh`. Add a second bind-time
format the same way: `#{session_name}` — the *actual* local session name of
the pane the key was pressed in, resolved by the tmux server with no
subprocess, and inherently correct for a bridged/mirror session too (the raw
name, exactly what `sameDisplayDifferentHost`/`current` comparisons need).

- `config/tmux.conf.nix`: bind lines `s` (865) and `MouseDown1StatusLeft`
  (871, the other invocation of `tmux-session-picker`) gain
  `--current #{qs:session_name}` — **bare `#{qs:...}`, never wrapped in
  `#{?session_name,--current #{q:session_name},}`** as the plan's first draft
  had it. **plan-critic pass 2 caught this**: `tests/conf-shell-quoting.bats`
  (the `conf-shell-quoting-tests` flake check) declares `session_name` a
  `WRAP_REQUIRED_FORMATS` entry and fails the build on any `#{q:session_name}`
  occurrence, recursing into `#{?...}` bodies too — every existing use in this
  file already uses bare `#{qs:session_name}` (e.g. line 817's `bind S`, lines
  860-861, 1040, 1095-1228). `#{qs:}` self-quotes and always emits a token
  (`''` for the pathological empty case, which a session name never is), so
  the `#{?...}` conditional isn't needed here at all — `--current` is simply
  always appended. `w`/`a`/`W` (`tmux-window-picker`/`tmux-window-wall`) are
  untouched — Part B is session-list-only.
- `scripts/tmux-session-picker.sh`: replace the single `--client` parse with a
  small loop accepting both `--client` and `--current`. Pass the current
  session through to the popup via `display-popup -e
  'LZTMUX_PICKER_CURRENT_SESSION=<value>'` — tmux's own env-for-popup flag
  (confirmed present: `tmux(1)`, `display-popup`'s `-e environment`, "sets an
  environment variable for the popup ... may be specified multiple times").
  This sidesteps quoting a dynamic value into the popup's shell-command
  string entirely (no risk from a session name with special characters), and
  matches the existing `LZTMUX_PICKER_EMIT`/`LZTMUX_PICKER_HOST` env-var
  precedent (`picker/tui.go:252,272`, set via `env` in
  `scripts/lztmux-remote-picker.sh:189-190` for the remote-pick path) rather
  than introducing a new argv-based mechanism.
- `tests/picker-launcher.bats`: three new cases alongside the existing
  `--client foo`/`no --client` pair (~85-95) — `--current bar` logs
  `-e LZTMUX_PICKER_CURRENT_SESSION=bar`; `--client foo --current bar` logs
  both; no `--current` logs no `-e`. This is the one link nothing else in the
  gate (`shellcheck`, `go test`) can see.
- `picker/tui.go`'s `runTUI` reads `os.Getenv("LZTMUX_PICKER_CURRENT_SESSION")`
  once (alongside `opts := readTmuxOpts()`), passes it into the first
  `buildSessionItems` call, and stores it on the model
  (`m.currentSession = currentSession`, new `tuiModel` field) so
  `refreshDataCmd`'s closure can capture `m.currentSession` for every
  periodic rebuild without re-reading the environment or forking anything.
- Add `current bool` to `sessionData` (`picker/main.go:52`) and `listItem`
  (`picker/tui.go`). `buildSessionItems` gains a `currentSession string`
  parameter. **Marking site, stated unambiguously** (plan-critic pass 2 found
  the first draft self-contradicting — it said "in its existing per-session
  loop... before calling `sortSessionsForDisplay`", but the *only* existing
  per-session loop, at ~1727, runs building the display `rows` *after* the
  sort at ~1712; marking there would be silent no-op, since
  `sinkCurrentBelowPeers` would see every `current` still `false`): add a new,
  dedicated loop immediately after `mergeAgent(sessions, agentMap)` (~1672)
  and before `sortSessionsForDisplay(sessions)`:
  ```go
  for i := range sessions {
  	sessions[i].current = sessions[i].name == currentSession
  }
  ```
  (raw-name comparison — never display-name-transformed). The later ~1727
  loop is untouched except to carry `current: r.sess.current` into each
  constructed `listItem` alongside the existing `bridgeHost:` field.
  `panesSnapshot.sessions()` itself is untouched — no live-tmux dependency
  added to that collector or its existing tests.
- **Test that would have caught the marking-site bug** (plan-critic pass 2:
  every other planned test calls `sortSessionsForDisplay`/`withFilter`
  directly with pre-marked fixtures, so none of them exercise the
  marking→sort wiring *inside* `buildSessionItems` — this is the only one
  that can fail if `current` lands on the wrong side of the sort): a
  `buildSessionItems`-level case in `picker/main_test.go` — a snapshot with
  local `lazytmux` (higher activity) and mirror `g6-lazytmux`
  (`@bridge_host=g6`), `currentSession: "lazytmux"`, asserting the returned
  items order the mirror row before the local row.
- Existing call-site updates: `buildSessionItems(nil, snap, nil, "dark",
  false)` in `picker/main_test.go:74` gains a trailing `""`; the two call
  sites in `picker/tui.go` (~267, ~1547) gain `currentSession`/`m.currentSession`
  respectively.

## Tests (table-driven, style of `tui_test.go` / `main_test.go`)

**Part A** (`picker/tui_test.go`):
- Host row whose own `searchText` matches the query stays selectable and can
  hold the cursor (extend/add alongside `TestWithFilterRemoteTree`).
- Host row pulled in only as tree context (own text doesn't match, a child
  does) is not selectable — `isSelectable` false, and cursor placement after
  `withFilter` skips it, landing on the first matching session row instead.
- Empty query: host row selectability unchanged (still always selectable).
- Query that matches the host name AND every child (host row's own text
  matches): host row stays selectable and precedes its children — confirms
  today's behavior survives.
- `refreshMsg`-style scenario: cursor sitting at a fixed index gets nudged off
  a row that becomes unselectable after a rebuild at that same index.

**Part B**:
- `picker/main_test.go` (alongside `TestSessionsBridgeProcOverride`'s
  `sessionData`-literal style): `sortSessionsForDisplay` table — unique
  display names unaffected; same-name same-host pair unaffected; same-name
  different-host pair with local `current` sinks below the mirror; same-name
  different-host pair with the *mirror* `current` sinks below local; **the
  A/B/C three-session interleaved-activity case that pins the transitivity
  fix** (this is the case a naive 2-session test cannot catch — assert the
  full resulting order, not just a pairwise relation).
- `picker/tui_test.go`: a regression test pinning `fuzzyScore("lazytmux",
  "lazytmux") > fuzzyScore("g6-lazytmux", "lazytmux")` (documents why the
  filter path needs its own fix; would pass before this change, explicitly
  labelled as a mechanism-documentation test, not acceptance evidence).
- `picker/tui_test.go`: `withFilter` table exercising the same
  current/collision cases as the `sortSessionsForDisplay` table via a
  non-empty query on `rankSession` listItems, including a case where the
  query matches only the current session (its peer never enters `matches`) —
  post-pass must no-op, order unaffected.

## Verify

- `go test ./...` (picker package + subpackages).
- `shellcheck scripts/tmux-session-picker.sh`.
- `nix build .#default`, `nix flake check`, `nix build .#lint` — paste output
  into the PR body.

## Change list addendum

- CLAUDE.md's `tmux-session-picker` Script Roles row: add one short clause
  noting the current-session sink (the existing "sorted by activity" sentence
  becomes incomplete after Part B) — in scope per the task's conditional
  ("update only if the described behavior is now wrong"), since it now is,
  narrowly. (Not "out of scope" — listed separately here only because it's a
  doc-only line, not Go/shell/nix.)
- PR body must state, explicitly (not just in this plan doc): the filter-path
  (`withFilter`) decision — that it needs the identical current-sinks-below-peer
  fix, independently of the unfiltered sort, because fuzzy score has no notion
  of "current" and (measured) does not tie between a bare name and its
  host-prefixed mirror.

## Out of scope

- Window mode (`--windows`) grouping/sort — Part B's collision only applies to
  the session list; window mode groups by session/state, a different sort
  entirely, and its binds (`w`/`a`/`W`) don't gain `--current`.

## Plan-critic history

- **Pass 1**: `revise`, 2 blocking findings (pairwise comparator override is
  not transitive, both at the `sessionData` sort and the `withFilter` scored
  sort — see "Design pass 1 was wrong" above) + non-blocking notes (wrong
  boundary-bonus constant named in the doc comment; `currentSessionName()`
  as a live per-tick fork is a latency/test-hermeticity concern with a
  zero-cost bind-time alternative already wired in this repo; the popup's
  format-resolution rationale was imprecise; `restoreCursor`'s routing
  exception should be stated, not glossed; `refreshMsg` can strand the cursor
  on a newly-unselectable row; CLAUDE.md's row should gain one clause). All
  incorporated above.
- **Pass 2**: `revise`, 2 blocking findings — (1) `#{q:session_name}` in the
  bind fails `tests/conf-shell-quoting.bats`'s `WRAP_REQUIRED_FORMATS` check
  (fix: bare `#{qs:session_name}`, no `#{?...}` wrapper, matching every other
  use of `session_name` in this file); (2) the "mark `current` in the existing
  per-session loop, before the sort" instruction was self-contradicting (that
  loop runs *after* the sort) — fix: a new, explicit, earlier loop, plus a
  `buildSessionItems`-level test that is the only one that can catch marking
  landing on the wrong side of the sort. Non-blocking notes (no bats coverage
  for the `--current`→popup-env link; a stale "always selectable" doc comment
  on `remoteHostRowItem`; the fuzzyScore explanation undercounted the
  consecutive-bonus propagation — corrected to `4 + 7×2 = 18`; `rank` needs
  hoisting out of the sort comparator literal for the prefix scan to reuse it;
  gate the `withFilter` post-pass on `!m.windowMode` explicitly;
  `zoxideMsg` has the identical bounds-only cursor guard as `refreshMsg`;
  `^o` still works on a context-only host row's children, worth a PR-body
  line; CLAUDE.md's clause was misplaced under "Out of scope"; the
  filter-path decision must land in the PR body, not just this doc). All
  incorporated above.
