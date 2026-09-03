package daemon

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/noamsto/lazytmux/picker/remotebridge/controlmode"
)

// windowLabelPollInterval is the floor between two polls. Main loop only: rt
// reads the stream, which has one consumer.
const windowLabelPollInterval = time.Second

// mainLoopTickInterval is the main loop's coarse wake-up, and so the ceiling on
// both this poll and the agent-status one. A remote window-option change emits
// no control-stream traffic at all — a @crew_name stamped just after the
// %window-add, a @pr_* refresh, a remote reflow re-stamping @window_label_* —
// so on a quiet mirror the stream alone would never bring the loop back around
// and the label would be stale indefinitely. 5s is therefore the worst-case
// first-appearance latency for a codename; a rename emits %window-renamed and
// stays sub-second under the floor above.
const mainLoopTickInterval = 5 * time.Second

// windowLabelFormat reads each remote window's crew badge, the label segments
// its own reflow already built, and its PR state. @window_label_rest_long is
// the one genuinely free-form field — a branch remainder or an issue title may
// hold a '|' — so it goes last, where a '|' lands inside it instead of shifting
// the row; sanitization runs after the split and cannot repair a shift.
const windowLabelFormat = "'#{window_id}|#{@crew_name}|#{@crew_color}|#{@pr_number}|#{@pr_state}|#{@pr_check_state}|#{@pr_mergeable}|#{@window_pr_plain}|#{@window_label_id}|#{@window_label_rest_long}'"

// labelRow is one remote window's carried label state, already sanitized and
// validated. Comparable, so the unchanged-row check is a struct compare.
type labelRow struct {
	id          string // remote window id, @N
	crewName    string
	crewColor   string
	prNumber    string
	prState     string
	prCheck     string
	prMergeable string
	prPlain     string
	labelID     string
	labelRest   string
}

// bridgeLabelOptions maps each carried value to the daemon-owned @bridge_*
// option it is stamped into. The daemon never writes @crew_*, @window_label_*
// or @pr_*: tmux-reflow-windows stamps those on every window of the mirror
// session, mirrors included, so a same-name write is a two-writer race the
// daemon loses on every reflow pass — and a mirror window carries stale local
// @issue_*/@pr_* residue from the after-new-window hook that fired against the
// launcher's cwd. One ordered list, so the stamp loop, the per-field diff and
// clear cannot drift apart.
var bridgeLabelOptions = []struct {
	opt string
	get func(labelRow) string
}{
	{"@bridge_crew_name", func(r labelRow) string { return r.crewName }},
	{"@bridge_crew_color", func(r labelRow) string { return r.crewColor }},
	{"@bridge_pr_number", func(r labelRow) string { return r.prNumber }},
	{"@bridge_pr_state", func(r labelRow) string { return r.prState }},
	{"@bridge_pr_check_state", func(r labelRow) string { return r.prCheck }},
	{"@bridge_pr_mergeable", func(r labelRow) string { return r.prMergeable }},
	{"@bridge_pr_plain", func(r labelRow) string { return r.prPlain }},
	{"@bridge_label_id", func(r labelRow) string { return r.labelID }},
	{"@bridge_label_rest_long", func(r labelRow) string { return r.labelRest }},
}

const (
	crewNameMaxRunes  = 24 // a codename; a cap so one value cannot dominate a column
	labelTextMaxRunes = 120
)

var (
	lowerWordRe = regexp.MustCompile(`^[a-z]+$`)
	digitsRe    = regexp.MustCompile(`^[0-9]+$`)
	// crewColorRe is interpolated into #[fg=…] by two format strings and into
	// ANSI by the picker, so it must never be markup. Hex is matched
	// case-insensitively: ansiFg accepts either case, and a lowercase-only
	// regex would silently drop an uppercase colour to the mauve fallback.
	crewColorRe = regexp.MustCompile(`^(#[0-9A-Fa-f]{6}|colour([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])|[a-z]+)$`)
)

// parseWindowLabels turns a windowLabelFormat reply body into one sanitized row
// per remote window, so nothing unclean reaches a caller.
func parseWindowLabels(body string) []labelRow {
	var out []labelRow
	for _, line := range strings.Split(body, "\n") {
		// Only the CR comes off: @window_pr_plain carries a LEADING space by
		// construction (" <glyph> #<n>") and reflow's pr_colw padding assumes
		// it, so no TrimSpace, per row or per field.
		line = strings.TrimRight(line, "\r")
		// Trailing empty fields may or may not survive the trip, so read them
		// positionally rather than demanding all ten.
		fields := strings.SplitN(line, "|", 10)
		at := func(i int) string {
			if i < len(fields) {
				return fields[i]
			}
			return ""
		}
		if at(0) == "" {
			continue
		}
		out = append(out, labelRow{
			id:          at(0),
			crewName:    cleanLabelValue(at(1), crewNameMaxRunes),
			crewColor:   matching(cleanLabelValue(at(2), labelTextMaxRunes), crewColorRe),
			prNumber:    matching(cleanLabelValue(at(3), labelTextMaxRunes), digitsRe),
			prState:     matching(cleanLabelValue(at(4), labelTextMaxRunes), lowerWordRe),
			prCheck:     matching(cleanLabelValue(at(5), labelTextMaxRunes), lowerWordRe),
			prMergeable: matching(cleanLabelValue(at(6), labelTextMaxRunes), lowerWordRe),
			prPlain:     cleanLabelValue(at(7), labelTextMaxRunes),
			labelID:     cleanLabelValue(at(8), labelTextMaxRunes),
			labelRest:   cleanLabelValue(at(9), labelTextMaxRunes),
		})
	}
	return out
}

// cleanLabelValue drops framing bytes and #[...] markup (stripWindowName, whose
// '#'-doubling twin is deliberately not used — the local label options already
// store raw '#', so a bridge copy must too), then caps the length.
//
// Two values are dropped whole rather than cleaned, because cfg.LocalTmux execs
// without a shell and tmux's own parser sees the argv: one beginning with '-',
// which args_parse reads as a flag, and one that is exactly ";", the separator
// joining apply's per-window command sequence — tmux fails the whole batch on it
// ("empty value", exit 1) and drops every later option in that sequence. A ';'
// inside a value is not a separator and is kept.
func cleanLabelValue(v string, maxRunes int) string {
	v = stripWindowName(v)
	if strings.HasPrefix(v, "-") || v == ";" {
		return ""
	}
	r := []rune(v)
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return v
}

// matching keeps v only if it is shaped as expected; a failure is an empty
// value, which unsets the option rather than stamping something a format string
// would have to survive.
func matching(v string, re *regexp.Regexp) string {
	if !re.MatchString(v) {
		return ""
	}
	return v
}

// labelShipper stamps the remote's window labels onto the local mirror windows
// under @bridge_* names, the way agentShipper does the remote's pane state —
// one level up, window options rather than pane options.
type labelShipper struct {
	written  map[string]writtenLabels // remote window id -> what was last written for it
	lastPoll time.Time
}

// writtenLabels is one remote window's last stamp, and the local window it
// landed on. The local target is part of the key, not just the payload:
// retireMirror rebuilds a dead mirror through closeWindow + reconcileWindows,
// which re-adds the SAME remote id against a fresh local window, so a row
// compare alone would suppress the re-stamp and leave the replacement bare.
// agentShipper needs no equivalent because it keys on the local pane id, which
// a rebuild changes.
type writtenLabels struct {
	localWin string
	row      labelRow
}

func newLabelShipper() *labelShipper {
	return &labelShipper{written: map[string]writtenLabels{}}
}

// poll re-reads the remote's window options and applies them, throttled to
// windowLabelPollInterval. Main loop only: rt is not safe to share.
func (s *labelShipper) poll(cfg Config, reg *registry, rt roundTrip) {
	if time.Since(s.lastPoll) < windowLabelPollInterval {
		return
	}
	s.lastPoll = time.Now()
	l, ok := one(rt, fmt.Sprintf("list-windows -t %s -F %s", tmuxQuote(cfg.RemoteSession), windowLabelFormat))
	if !ok || l.Kind == controlmode.Error {
		return
	}
	if s.apply(cfg, reg, parseWindowLabels(string(l.Data))) {
		// A label change alters no window count, so reflow's count:width:height
		// cache would skip it — the @window_bridge_name precedent. Exactly once
		// per pass, after every option write of that pass.
		cfg.reflow()
	}
}

// apply stamps the rows whose values moved, and reports whether any did.
//
// A bare mirror's FIRST pass counts as changed: seen is false, so the row
// compare cannot fire and the window gets nine `-u` for a row carrying nothing,
// forcing one reflow at daemon start.
func (s *labelShipper) apply(cfg Config, reg *registry, rows []labelRow) (changed bool) {
	for _, r := range rows {
		mw, ok := reg.byRemoteID(r.id)
		if !ok {
			continue
		}
		prev, seen := s.written[r.id]
		seen = seen && prev.localWin == mw.localWin
		if seen && prev.row == r {
			continue
		}
		s.written[r.id] = writtenLabels{localWin: mw.localWin, row: r}

		// One argv command sequence per window — the form
		// tmux-reflow-windows already uses — so a first pass over N windows
		// costs N forks rather than 9N.
		var argv []string
		for _, o := range bridgeLabelOptions {
			v := o.get(r)
			if seen && o.get(prev.row) == v {
				continue
			}
			if len(argv) > 0 {
				argv = append(argv, ";")
			}
			if v == "" {
				// An empty remote value unsets, so a mirror whose remote
				// carries nothing is option-free rather than holding "".
				argv = append(argv, "set-option", "-w", "-t", mw.localWin, "-u", o.opt)
				continue
			}
			argv = append(argv, "set-option", "-w", "-t", mw.localWin, o.opt, v)
		}
		cfg.LocalTmux(argv...)
		changed = true
	}

	// Forget by absence from the REGISTRY, not from the reply as
	// agentShipper.apply does: a remote window that vanishes from list-windows
	// while its mirror is still registered keeps its last stamp until
	// reconcileWindows closes it.
	for id := range s.written {
		if _, ok := reg.byRemoteID(id); !ok {
			delete(s.written, id)
		}
	}
	return changed
}

// clear unsets every option this bridge wrote, while the mirror windows still
// exist. Near-vacuous in production — teardown ends in kill-session and window
// options die with the session — so unlike agentShipper.clear, whose files
// outlive tmux, this is kept for symmetry and for the paths where that kill
// fails.
func (s *labelShipper) clear(cfg Config, reg *registry) {
	for id := range s.written {
		mw, ok := reg.byRemoteID(id)
		if !ok {
			continue
		}
		var argv []string
		for _, o := range bridgeLabelOptions {
			if len(argv) > 0 {
				argv = append(argv, ";")
			}
			argv = append(argv, "set-option", "-w", "-t", mw.localWin, "-u", o.opt)
		}
		cfg.LocalTmux(argv...)
	}
	s.written = map[string]writtenLabels{}
}
