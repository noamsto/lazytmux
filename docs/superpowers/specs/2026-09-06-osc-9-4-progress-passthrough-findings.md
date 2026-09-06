# Spike findings: OSC 9;4 progress passthrough (#521)

## Verdict

**Technically viable, but not implemented in this PR.** The forwarding mechanism
is real and proven empirically; kitty's support is real and confirmed from its
own source. What's missing is not "does it work" but "does it work *correctly*
in every case this codebase cares about" — specifically, whether tmux re-emits
a pane's progress state when the client switches focus onto it after the fact.
That's a real design question, not a research gap I ran out of time on, and it
determines the emission strategy (one-shot at state-change vs. periodic
re-emit for the active pane). Getting "clearing is not optional" right without
answering it first risks shipping a progress indicator that's sometimes wrong
in ways worse than having none — so this spike stops here and hands the
question to a follow-up ticket with the mechanism already de-risked.

## 1. Does tmux pass OSC 9;4 through?

**Not blind passthrough — tmux natively parses and re-emits it**, gated by the
same `terminal-features` mechanism as clipboard (OSC 52), hyperlinks, and
title. This is a different (and better) answer than the task's three
candidate outcomes assumed.

Confirmed from the installed `tmux 3.7c` binary (`strings` on
`/nix/store/.../bin/tmux`):

- `tty_feature_progressbar` / `tty_feature_progressbar_capabilities` — a
  first-class tty feature, same family as `osc7`, `clipboard`, `hyperlinks`.
- A terminfo extension capability `Spb` (Set Progress Bar) with value
  `\E]9;4;%p1%d;%p2%d\E\\` — literally the OSC 9;4 template tmux re-emits when
  the feature is enabled for the attached client's terminal.
- tmux's **default** terminal-features table (built into the binary) enables
  `progressbar` for `foot`, `WezTerm`, and `XTerm`. **kitty is not in that
  table.** (Confirmed: `strings tmux | grep -i kitty` — zero hits anywhere in
  the binary. The repo's existing `terminal-overrides`/`terminal-features` for
  kitty, `config/tmux.conf.nix:587-591` and `:758`, only set `RGB:extkeys` and
  `hyperlinks` — nothing enables `progressbar`.)

### Empirical proof (scratch server, `tmux -L osc-probe*`, positive + negative control)

Captured the literal bytes tmux sends to an attached client via
`script -qc "tmux -L <sock> attach -t probe" capture.raw` (a plain attach,
**not** control mode — `script -c "tmux -C attach"` EOFs instantly, a known
trap from #464-#471). From inside the pane: `printf '\033]9;4;1;50\033\\'`.

| terminal-features | bytes reaching the client |
|---|---|
| `xterm-256color:progressbar` (added) | `ESC ] 9 ; 4 ; 1 ; 50 ESC \` and `ESC ] 9 ; 4 ; 0 ; 0 ESC \` reach the client **byte-for-byte**, at the exact offsets matching what was sent inside the pane |
| default (no `progressbar` feature for that TERM) | **zero** OSC 9;4 bytes reach the client — confirmed absent via `grep -aboP '\x1b\]9;4'` on the capture |

This is the safety property that matters most: **a terminal only ever receives
raw OSC 9;4 bytes if its TERM pattern is explicitly granted the `progressbar`
feature.** Scope that grant to kitty's TERM specifically (mirroring the
existing `terminalTerm` RGB/extkeys pattern) and no other terminal — including
one with zero OSC 9;4 support — is ever exposed to the sequence. The "stray
escape painted as literal text" regression risk the task worried about **does
not exist** for any terminal we don't explicitly opt in.

`allow-passthrough` (the per-client DCS-wrapper gate from #464-#471) is
**irrelevant here** — this is tmux's own native interpret-and-re-emit path, not
a program asking to bypass tmux via `\ePtmux;...\e\\`. The wrapper's
cross-session gap doesn't apply because there's no wrapper.

### Emission mechanism (resolves "claude-status-update has no tty of its own")

`claude-status-update` runs as a Claude Code hook subprocess — its own stdout
is CC's pipe, not the target pane's screen. Confirmed empirically that this is
not a blocker: **writing OSC bytes directly to a pane's `#{pane_tty}` device
path from a wholly unrelated process reaches the client exactly like the
pane's own output would.**

```
PANE_TTY=$(tmux -L <sock> display-message -p -t <pane> '#{pane_tty}')
printf '\033]9;4;1;77\033\\' > "$PANE_TTY"
```

— captured in the attached client's byte stream, verified via the same
offset-search method. This means any script holding a `pane_id` (which
`claude-status-update`, `read_pane_state`, and the 1s pollers all already do)
can emit or clear progress for that pane without needing to be its own
foreground process, and without touching the pane's actual input/output
streams that the shell or Claude sees.

## 2. Does kitty honor it?

**Yes — confirmed from source, not just docs.** The installed `kitty 0.48.2`
(`/nix/store/.../kitty-0.48.2`) has a dedicated module,
`lib/kitty/kitty/progress.py`, implementing the full ConEmu state machine:

- States: `unset(0)` / `set(1, +percent)` / `error(2)` / `indeterminate(3)` /
  `paused(4, +percent)`.
- Parsed in `kitty/window.py:1356-1381` (`desktop_notify`, gated on
  `osc_code == 9 and raw_data.startswith('4;')` — kitty's own comment calls
  OSC 9;4 "the ConEmu progress reporting conflicting implementation which
  sadly some thoughtless people have implemented," i.e. they support it
  defensively, distinct from OSC 9 plain notifications).
- Rendered into the **tab-bar title** as a `[50%] ` / `[…] ` prefix
  (`window.py:1157-1162`) and aggregated across all tabs into the **OS
  dock/taskbar** via `TabManager.update_progress` →
  `get_boss().update_progress_in_dock()` (`tabs.py:2103-2113`).
- Kitty has its **own auto-clear safety net independent of us**:
  `Progress.clear_progress()` times out a stuck bar after 60s (5s if the last
  update was 100%). A bug in our clear logic self-heals within a minute rather
  than leaking a stuck progress indicator forever — this meaningfully lowers
  the stakes of "clearing is not optional," though it doesn't remove the need
  to clear explicitly.

I have no way to visually observe kitty's rendered tab-bar or OS dock from
this session (no screenshot/remote-control path to kitty's chrome). The
source-level confirmation is against the exact installed version, which is as
strong a substitute as is available here — recommend a 10-second manual check
before any code lands:
```
printf '\033]9;4;1;50\033\\'   # inside a kitty tab, no tmux
```
and look at the tab title / dock.

## 3. Open question that stopped this from shipping

**Does tmux re-emit a pane's already-set progress state when the client's
focus switches onto that pane, or is emission one-shot at the moment the
sequence is received?** This is exactly the question that decides the
architecture:

- If tmux resends on focus (like it does for pane/window title on redraw):
  emitting from `claude-status-update` (write-time) and the two derived-state
  sites in `read_pane_state` (interrupted, dead-agent withdrawal) is
  sufficient — each writes once, tmux handles the rest.
- If it doesn't: a pane that starts `processing` while in the background,
  then gets focused later, would show stale/no progress until its *next*
  state-change event — which for a quiet `processing` pane could be a long
  time. That would need periodic re-emission for whichever pane is currently
  active, driven from the existing 1s poller (`tmux-update-icons`) instead of
  purely from state-change events.

I attempted to test this on the scratch server (set progress on an inactive
pane, switch focus onto it, check for a resend) and hit an unrelated harness
issue — the inactive pane's shell exited mid-test for a reason I did not chase
down, which broke the write path (`Permission denided` on `pane_tty` — most
likely the pty was torn down when the pane died, not a real permissions
problem). I did not want to burn further spike budget debugging a secondary
harness flake once the primary go/no-go questions (1) and (2) were already
answered cleanly. This is the one question a follow-up should resolve first —
it's a single well-scoped experiment, not open-ended.

## Other things a follow-up needs to decide (not investigated further here)

- **Per-pane vs. per-client granularity.** tmux tracks progress per-pane
  internally (`pane_pb_progress`), but only forwards OSC to the client for
  whatever's driving its current screen — in practice this reads like "the
  currently attached/active pane," the same way window title does. Two
  concurrently-processing panes will not both show progress in kitty's chrome
  at once; only whichever one is in view. That's probably the right UX
  (matches title behavior) but should be stated as a deliberate scoping
  decision, not discovered later.
- **Clearing paths.** Enumerated for completeness, beyond what the task
  named: `done`/`idle`/`waiting`/`error`/`denied` (direct state writes),
  `interrupted` (derived, `read_pane_state`), the dead-agent withdrawal
  (`claudeStatus.assumeDeadAfter`, also `read_pane_state`), the CC `clear`
  verb, `cleanup_stale_panes` (`claude-status-update.sh:66-99`), and
  `claude_reap_dead_panes`'s every-5th-tick sweep. All are additive call
  sites (write bytes to `pane_tty`), none require touching the state machine
  or status-file format.
- **Remote bridge.** Deliberately not investigated. The mirror pane's
  renderer is local, so a locally-emitted OSC for a mirror pane would likely
  work without crossing the control stream at all (unlike the graphics
  proxy's kitty-APC case) — but this needs its own pass once the local case's
  open question above is resolved.

## Recommendation

Don't implement in this PR. File a follow-up ticket scoped to: (1) resolve the
resend-on-focus question empirically, (2) pick the emission strategy it
implies, (3) wire the `terminal-features 'xterm-kitty*:progressbar'` line
into `config/tmux.conf.nix` alongside the existing `terminalTerm` pattern, (4)
implement the enumerated clear paths. The scratch-probe commands above are
reusable verbatim for that work.

## 4. Resend-on-focus (resolved)

tmux 3.7c **does resend** a pane's stored OSC 9;4 when the client focuses that
pane later. Emission is therefore one-shot at write time — no 1s re-emit loop
in `tmux-update-icons`.

Harness (two panes held with `sleep 3600`, `terminal-features
xterm-256color:progressbar`, `script` capturing a `TERM=xterm-256color`
attach):

- Wrote `\033]9;4;1;77\033\\` to the **inactive** pane's `#{pane_tty}`. Pane
  stayed alive (no Permission denied).
- Stored: inactive pane `pane_pb_state=normal pane_pb_progress=77`; active pane
  stayed `hidden|0`.
- Client byte stream had **zero** OSC 9;4 while that pane was inactive.
- After `select-pane` onto it: client received `\x1b]9;4;1;77\x1b\\` at the
  previous capture offset.

Killing an active pane that had progress caused tmux to emit `\x1b]9;4;0;0`
because the remaining pane was `hidden|0`. Dead-pane clear is therefore mostly
tmux's own resend; the enumerated call sites (`claude-status-update` state
writes / `clear` / `cleanup_stale_panes`, `read_pane_state` interrupt and
dead-agent withdrawal, `claude_reap_dead_panes`) still emit once and no-op if
the tty is gone.

Chosen mapping: `processing`/`compacting` → OSC 9;4;3;0 (indeterminate, no
fake percent); everything else including explicit `clear` → OSC 9;4;0;0.
Remote bridge remains out of scope.
