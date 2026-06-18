package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// statePalette holds the per-state catppuccin hues for one theme, mirroring
// the H_* values in lib-claude.sh setup_claude_colors.
type statePalette struct {
	waiting, compacting, processing, done, idle, errorC, denied string
}

func claudePalette(theme string) statePalette {
	if theme == "light" { // Latte
		return statePalette{
			waiting: "#fe640b", compacting: "#04a5e5", processing: "#179299",
			done: "#40a02b", idle: "#6c6f85", errorC: "#d20f39", denied: "#df8e1d",
		}
	}
	// Mocha (default)
	return statePalette{
		waiting: "#fab387", compacting: "#89dceb", processing: "#94e2d5",
		done: "#a6e3a1", idle: "#6c7086", errorC: "#f38ba8", denied: "#f9e2af",
	}
}

func (p statePalette) hue(state string) string {
	switch state {
	case "waiting":
		return p.waiting
	case "compacting":
		return p.compacting
	case "processing":
		return p.processing
	case "done":
		return p.done
	case "idle":
		return p.idle
	case "error":
		return p.errorC
	case "denied":
		return p.denied
	}
	return ""
}

// fadedHue eases a state's hue toward the dim idle hue by pct (0..100).
// unseen pins to full color. Empty string for an unknown state.
func (p statePalette) fadedHue(state string, pct int, unseen bool) string {
	base := p.hue(state)
	if base == "" {
		return ""
	}
	if unseen {
		pct = 0
	}
	if pct <= 0 {
		return base
	}
	return fadeHex(base, p.idle, pct)
}

// fadeHex linearly interpolates between two #rrggbb colors; pct 0 = from, 100 = to.
func fadeHex(from, to string, pct int) string {
	fr, fg, fb := hexBytes(from)
	tr, tg, tb := hexBytes(to)
	return fmt.Sprintf("#%02x%02x%02x",
		fr+(tr-fr)*pct/100,
		fg+(tg-fg)*pct/100,
		fb+(tb-fb)*pct/100)
}

func hexBytes(h string) (int, int, int) {
	r, _ := strconv.ParseInt(h[1:3], 16, 0)
	g, _ := strconv.ParseInt(h[3:5], 16, 0)
	b, _ := strconv.ParseInt(h[5:7], 16, 0)
	return int(r), int(g), int(b)
}

// detectTheme reads $XDG_STATE_HOME/theme-state.json (default ~/.local/state),
// returning "light" or "dark" (the default). Matches lib-claude.sh.
func detectTheme() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = os.Getenv("HOME") + "/.local/state"
	}
	data, err := os.ReadFile(stateHome + "/theme-state.json")
	if err != nil {
		return "dark"
	}
	s := string(data)
	i := strings.Index(s, "\"theme\"")
	if i < 0 {
		return "dark"
	}
	rest := s[i+7:]
	c := strings.Index(rest, ":")
	if c < 0 {
		return "dark"
	}
	rest = rest[c+1:]
	q1 := strings.Index(rest, "\"")
	if q1 < 0 {
		return "dark"
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, "\"")
	if q2 < 0 {
		return "dark"
	}
	if rest[:q2] == "light" {
		return "light"
	}
	return "dark"
}

type counts struct {
	processing, waiting, compacting, done, idle, errorN, denied, total int
}

func (c counts) priorityState() string {
	switch {
	case c.errorN > 0:
		return "error"
	case c.waiting > 0:
		return "waiting"
	case c.denied > 0:
		return "denied"
	case c.compacting > 0:
		return "compacting"
	case c.processing > 0:
		return "processing"
	case c.done > 0:
		return "done"
	case c.idle > 0:
		return "idle"
	}
	return ""
}

func (c *counts) tally(state string) {
	c.total++
	switch state {
	case "processing":
		c.processing++
	case "waiting":
		c.waiting++
	case "compacting":
		c.compacting++
	case "done":
		c.done++
	case "idle":
		c.idle++
	case "error":
		c.errorN++
	case "denied":
		c.denied++
	}
}

// fadePct mirrors read_pane_state: bright until the state's staleness threshold,
// then linear ramp to 100 over 45s.
func fadePct(state string, now, ts int64) int {
	if ts == 0 {
		return 0
	}
	const fadeDuration = 45
	start := map[string]int64{
		"waiting": 30, "compacting": 60, "processing": 300,
		"done": 60, "error": 120, "denied": 60,
	}[state]
	if start == 0 {
		return 0
	}
	age := now - ts
	if age <= start {
		return 0
	}
	if age >= start+fadeDuration {
		return 100
	}
	return int((age - start) * 100 / fadeDuration)
}

type sessionAgg struct {
	counts  counts
	minFade int
	unseen  bool
	issues  []string
}

// aggregateSession scans <dir>/panes/*, filters by the session= field, and
// tallies state + freshest fade + issue ids. No tmux call — session mode keys
// entirely off the file's session field.
func aggregateSession(dir, session string, now int64) sessionAgg {
	agg := sessionAgg{minFade: 100}
	entries, err := os.ReadDir(filepath.Join(dir, "panes"))
	if err != nil {
		return agg
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "panes", e.Name()))
		if err != nil {
			continue
		}
		var state, sess string
		var ts int64
		var unseen bool
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "state":
				state = v
			case "session":
				sess = v
			case "timestamp":
				ts, _ = strconv.ParseInt(v, 10, 64)
			case "unseen":
				unseen = v == "1"
			}
		}
		if state == "" || sess != session {
			continue
		}
		agg.counts.tally(state)
		if f := fadePct(state, now, ts); f < agg.minFade {
			agg.minFade = f
		}
		if unseen {
			agg.unseen = true
		}
		for _, id := range readIssueFile(filepath.Join(dir, "issues", e.Name())) {
			if id != "" && !seen[id] {
				seen[id] = true
				agg.issues = append(agg.issues, id)
			}
		}
	}
	return agg
}

func readIssueFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	line, _, _ := strings.Cut(string(data), "\n") // bash collect_pane_issues reads only the first line
	return strings.Split(strings.TrimSpace(line), ",")
}

func formatIssueList(max int, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= max {
		return strings.Join(ids, " ")
	}
	return strings.Join(ids[:max], " ") + " +" + strconv.Itoa(len(ids)-max)
}

var spinnerFrames = []string{"󰪞", "󰪟", "󰪠", "󰪡", "󰪢", "󰪣", "󰪤", "󰪥"}

func stateIcon(state string, now int64) string {
	switch state {
	case "processing":
		return spinnerFrames[now%int64(len(spinnerFrames))]
	case "waiting":
		return "󰔟"
	case "compacting":
		return "󰡍"
	case "done":
		return "󰸞"
	case "idle":
		return "󰒲"
	case "error":
		return "󰅚"
	case "denied":
		return "󰔟"
	}
	return ""
}

// claudeSegment mirrors `claude-status --session <s> --format icon-color`.
func claudeSegment(dir, session, theme string, now int64) string {
	agg := aggregateSession(dir, session, now)
	if agg.counts.total == 0 {
		return ""
	}
	state := agg.counts.priorityState()
	icon := stateIcon(state, now)
	if icon == "" {
		return ""
	}
	pal := claudePalette(theme)
	hue := pal.fadedHue(state, agg.minFade, agg.unseen)
	out := "#[fg=" + hue + "]" + icon + "#[fg=default] "
	if list := formatIssueList(3, agg.issues); list != "" {
		out += "#[fg=" + pal.idle + "]" + list + "#[fg=default] "
	}
	return out
}
