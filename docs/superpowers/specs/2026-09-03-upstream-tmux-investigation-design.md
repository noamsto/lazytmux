# Design spec — which lazytmux issues are upstream tmux bugs (#488)

Revision 3 (revision cap 2 of 2). Revisions 1–2 resolved: claim 2 is stale and
already filed upstream; the lethal popup shape is two popups with `-B`; the
wrapper force-loads the lazytmux config; in-repo prior art must be reconciled;
the claim-2 time box; issue-comment ordering/idempotency; the flake/lock
two-node question. Revision 3 resolves the seven findings of critic pass 2.

## Problem

Four lazytmux issues (#341, #346, #474/#476, #478/#481) were each worked around
locally, and each was suspected of having a root cause in tmux itself. Nobody
has established which suspicions survive contact with the tmux revision this
repo actually pins. Until that is done the human cannot decide what is worth
reporting upstream.

## Goal

Per claim, an evidence-backed verdict — reproduced against the pinned tmux,
with the mechanism named from tmux source where it can be found.

## Verdict vocabulary

Every claim carries exactly one of:

- **report** — genuine upstream defect, not already filed.
- **already reported upstream** — filed; record the upstream number, its state,
  and whether the fix is present in the rev we pin.
- **did not reproduce at the pin** — the behaviour observed on the older tmux
  no longer occurs on `40381bdc`. Record what was observed instead and, where
  findable, the upstream commit that changed it.
- **do not report** — ours, or documented tmux behaviour. This is distinct
  from the above: it asserts the behaviour is *not* a tmux defect, not merely
  that it changed.
- **needs more work** — the evidence does not settle it. A legitimate outcome;
  never round it to one of the others.

## Non-goals (hard boundaries)

- **Nothing is sent to the tmux project.** No issues, patches, PRs, or
  mailing-list posts. Checkable proxy: no `gh` call that writes to `tmux/tmux`,
  no git remote pointing at it, and every draft lives only inside
  `docs/upstream-tmux.md`.
- **No behaviour change to lazytmux.** Docs-only PR. Where the investigation
  shows an in-repo document or comment is now wrong, record it as a follow-up;
  do not edit it here.
- **No duplicate upstream drafts.** A claim already filed upstream gets no
  draft unless the upstream issue was closed *without* a fix, and the doc must
  name that evidence.
- No patching of tmux to manufacture a reproduction.
- Do not re-litigate the ruled-out issues, whose reasons are recorded here so
  the "concrete contrary evidence" test can actually be applied:
  - **#276** control-mode reply desync — tmux sets the `%begin`/`%end` flags
    field precisely so a client can distinguish its own replies from hook
    blocks; we were not reading it. Ours.
  - **#306** toast freezes the client — `-N`/`-C` are documented flags with
    documented semantics. Ours.
  - **#371** floating-pane geometry on resize — already filed as tmux#5135,
    undecided upstream. Nothing to add.
  - **#378** tab-delimited `-F` output mangled per the querying client's
    locale — surprising, but tmux's non-printable rewriting is deliberate.

## Ground truth to establish first

1. **Which binary is evidence.** `./result/bin/tmux` is a *wrapper*: it execs
   `.tmux-wrapped -f <lazytmux tmux.conf> "$@"`, and `tmux.c`'s `-f` handling
   makes the first `-f` clear the defaults while later ones **append** — so a
   repro's own `-f /dev/null` does *not* displace the baked config. Verified:
   the wrapper with `-f /dev/null` still reports `aggressive-resize on`; the
   unwrapped binary reports `off`.
   **Therefore every reproduction runs against the unwrapped tmux built from
   the pinned rev, on a scratch `-L <socket>` with `-f /dev/null`**, and the
   doc records the exact binary path and argv used.
   A repro **may** set stock, documented tmux options on the scratch server
   (e.g. `set -g aggressive-resize on`) provided every such `set-option`
   appears verbatim in the recorded repro. What disqualifies a **report**
   verdict is dependence on *lazytmux's own* scripts, hooks, or wrapper — not
   the mere use of a non-default tmux option.
2. **Version, rev and platform.** Record the binary's `-V`, the git rev it was
   built from, and the **OS/arch** the repro ran on. Each verdict must name the
   platform(s) its evidence covers, and single-platform evidence must be
   flagged as such.
3. **Which source tree the mechanism is read from.** Record the store path and
   rev of the tmux checkout read. Every `file:line` citation must resolve at
   that rev; any line number carried over from an in-tree document must be
   re-resolved at `40381bdc` or explicitly labelled as a citation against the
   older rev.
4. **The flake/lock question.** The contract describes `flake.nix` @
   `40381bdc` vs `flake.lock` holding both `d5afb67a` and `40381bdc` as
   possible drift. Establish and record the actual relationship between the two
   nodes, how many tmux derivations are in the built closure, and — per the
   contract — whether anything here is real and **worth its own issue**. Do not
   fix it.
5. **Pinned vs system tmux.** The contract states the system tmux is 3.7c.
   Verify; if stale, say so. Any genuine divergence is itself a finding.

## Prior art that must be reconciled, not re-derived

The doc must state, for each artifact **by path**, whether this investigation
**confirms** or **revises** it at the current pin:

1. `docs/superpowers/plans/2026-08-10-reap-pane-state.md` — the artifact that
   established #341's root cause ("tmux accepts the `set-hook` call … but never
   actually stores or fires it"). This is the primary 1a prior art.
2. `tests/tmux-next38-readiness.bats` — the "every registered hook is stored"
   assertion. **Its actual boundary:** it scans only hooks the *generated
   config* registers, and `pane-exited` was removed from that config by (1) —
   so a green check says nothing about `pane-exited`. Do not claim it as
   coverage it does not provide.
3. `modules/home-manager.nix:107` — the `pane-exited[99]` hook (1) flagged as
   an unfixed "phantom". State whether it is still a phantom at the root pin.
4. `docs/superpowers/specs/2026-08-10-popup-control-mode-guard-design.md` — the
   #346 root cause and minimal repro. Its `file:line` citations were taken
   against `d5afb67`, not the root pin; re-resolve per ground-truth rule 3.
5. `docs/superpowers/plans/2026-09-02-bridge-aggressive-resize-478.md` and
   `docs/superpowers/specs/2026-09-02-bridge-window-size-latest-478-design.md`
   — already name 1c's mechanism (`resize.c`,
   `clients_calculate_size_skip_client`'s `current` branch).

**Upstream prior art must be enumerated before any draft is written.** Grep the
tree for every `tmux/tmux#N` reference (at least #5551, #5135, #5398, #5336,
#4920), record each one's current upstream state, and match claims 1a/1b/1c/2
against that list. **tmux/tmux#5551 is the existing report for claim 2** and is
referenced from five in-tree scripts.

## Claims under test

Each claim's write-up must include a row-by-row diff against the contract's
older-tmux observations where the contract supplies them (it supplies a full
table for 1a), so pinned-vs-3.7c divergence is visible per behaviour, not just
per version string.

### Claim 1 — the silent-no-op family

- **1a — `set-hook` accepts a non-hook and stores nothing (#341).** The repro
  table must include the contract's **positive controls** (`after-kill-pane`,
  `client-detached`: accepted *and stored*) alongside the silent group
  (`pane-exited`, `pane-died`, `pane-focus-in`) and the correctly rejected
  `bogus-hook-name`. Establish from source whether these are real options in
  another table, reserved names, or a gap — and test not only whether they are
  *listed* but whether they are *stored anywhere* and whether they **fire**.
- **1b — `show-options -t '=name'` (#474/#476).** `has-session`,
  `switch-client`, `kill-session` honour the `=` exact-match prefix;
  `show-options` rejects it, and `-q` renders the error indistinguishable from
  an unset option. Establish from source *why* the target parsers differ.
- **1c — `refresh-client -C @N:WxH` under `aggressive-resize on` (#478/#481).**
  The cap is accepted then discarded for any window no client has selected.
  The doc must state **in words** the documented `aggressive-resize` behaviour
  it is *not* disputing, so "the sizing is defensible, only the silence is
  reportable" is checkable rather than a slogan.

Then an **argued judgement**: one family worth one report, or three unrelated
things that merely rhyme? The case *against* must be stated, grounded in
whether the source mechanisms are in fact related.

### Claim 2 — the display-popup SEGV (#346) — verify and consolidate

The contract's framing ("never reproduced"; "`tmux-splash-maybe` does not
exclude control-mode clients") is stale: both a shipped gate and an upstream
report (**tmux/tmux#5551**) already exist in-tree. The work is therefore not
rediscovery:

- Confirm #5551's current upstream status, and whether its fix is present in
  the rev we pin.
- Confirm the reproduction at the **root** pin using the shape the design doc
  establishes as lethal — **two** popups at the same control-mode client, the
  second carrying `-B`/`-b`. One popup alone is harmless, which is why an
  existing bats case has always passed. **"Not reproduced" may not be recorded
  without having tried that shape.**
- Confirm whether the shipped mitigations (the splash control-mode gate and the
  popup `-c` client pinning) close the path the crash needs — this is in scope
  only insofar as it determines whether anything remains to report.
- **Time box: at most four distinct hypotheses beyond the design doc's known
  shape, and at most three repro attempts per hypothesis.** The doc records the
  budget and what was spent.
- Assess — do not implement — whether the splash should gate on control-mode
  regardless of the crash. If the gate already exists, say so and cite it.

## Deliverables

1. **`docs/upstream-tmux.md`** — per claim: exact reproduction commands, the
   binary and argv used, OS/arch, observed output, tmux version/rev, the source
   mechanism where found (cited at the recorded rev), and a verdict with
   reasoning. Failed reproductions included. Uncertainty written as uncertainty.
2. **A comment on each of #341, #346, #474, #478.** Ordering and idempotency
   are part of the deliverable:
   - The three gates (`nix build .#default`, `nix flake check`,
     `nix build .#lint`) pass **before** the branch is pushed and the PR opened.
   - Comments are posted **after** the PR exists and link it (a `docs/` path on
     an unmerged branch is a dead link).
   - Every comment body carries the literal marker `<!-- lazytmux/488
     upstream-tmux -->`. Before posting, search that issue for the marker; on a
     hit, edit that comment in place instead of posting a second one.
3. **A draft upstream report** inside the doc for any claim whose verdict is
   **report** — the text a maintainer would read, with a repro runnable on a
   stock tmux build (no lazytmux, no nix). Draft only. No draft for a claim
   verdicted **already reported upstream**.

## Acceptance criteria

- [ ] The evidence binary is the unwrapped pinned tmux; the doc records its
      path, `-V`, source rev, OS/arch, and the wrapper-contamination reason for
      not using `./result/bin/tmux`.
- [ ] The tmux source tree read for mechanisms is recorded by store path and
      rev, and every `file:line` citation resolves at that rev.
- [ ] The flake/lock question is answered with evidence, including how many
      tmux derivations are in the built closure and the worth-its-own-issue
      call.
- [ ] The contract's pinned-vs-system claim is verified or corrected.
- [ ] Every claim (1a, 1b, 1c, 2) is reproduced against the pinned build with
      commands and output recorded, or recorded as not-reproduced with the
      attempts written down. No claim rests on a 3.7c observation, and none on
      a wrapper-contaminated run.
- [ ] Each claim carries a row-by-row diff against the contract's older-tmux
      observations where the contract supplies them.
- [ ] Claim 1a's table carries the positive controls as well as the silent
      group, and reports storage **and** firing, not just listing.
- [ ] Each of 1a, 1b, 1c carries a mechanism-from-source finding **or** an
      explicit statement that the mechanism was not established. (Either
      satisfies this; "establish the mechanism" is the goal, not a gate that
      licenses guessing.)
- [ ] 1c states the documented behaviour it is not disputing.
- [ ] The one-family-or-three judgement is present and argues both sides.
- [ ] Claim 2 records the two-popup `-B` attempt, #5551's status and whether
      its fix is in our pin, the mitigation-coverage check, the time-box spend,
      and the splash-gate assessment — without implementing it.
- [ ] All six prior-art artifacts (the five paths listed above plus
      `modules/home-manager.nix:107`) are each marked confirmed or revised.
- [ ] Every in-tree `tmux/tmux#N` reference is listed with its current upstream
      state, and each claim is matched against that list, before any draft.
- [ ] Every claim carries exactly one of the five verdicts, naming the
      platform(s) its evidence covers.
- [ ] The three gates pass, then the PR is opened, then comments are posted on
      #341, #346, #474, #478 — each carrying the marker, none duplicated.
- [ ] `git diff` against the merge-base touches only `docs/upstream-tmux.md`
      plus this spec and its plan under `docs/superpowers/`. `WORKER_TASK.md`
      and any root-level scratch stay **untracked and uncommitted**. Any other
      change is justified in the PR body.
- [ ] Nothing was sent to the tmux project (per the checkable proxy above).
