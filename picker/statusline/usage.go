package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// usageCache mirrors the normalized JSON the tmux-agent-usage-* provider
// scripts write to usageCacheDir/<agent>.json.
type usageCache struct {
	Windows []usageWindow `json:"windows"`
	Monthly *usageWindow  `json:"monthly"`
}

type usageWindow struct {
	Label   string  `json:"label"`
	Pct     float64 `json:"pct"`
	ResetAt int64   `json:"reset_at,omitempty"`
}

const usageCacheDir = "/tmp/lazytmux-agent-usage"

// usageAgentOrder fixes the left-to-right agent order in the segment.
var usageAgentOrder = []string{"claude", "codex", "cursor"}

// loadUsageCaches reads whatever provider caches exist. Missing or malformed
// files are skipped — the poller rewrites them atomically on the next pass.
func loadUsageCaches(dir string) map[string]usageCache {
	out := map[string]usageCache{}
	for _, agent := range usageAgentOrder {
		data, err := os.ReadFile(filepath.Join(dir, agent+".json"))
		if err != nil {
			continue
		}
		var c usageCache
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		out[agent] = c
	}
	return out
}

// agentsRunning is the display gate: the segment only matters while a coding
// agent is running somewhere. A failed list-panes returns true — a transient
// tmux error shouldn't blink the segment off. A mirror pane's own
// pane_current_command is the bridge renderer, not the remote's real command,
// so @bridge_proc (the remote's, stamped by the daemon) is checked too (#513).
func agentsRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_current_command}|#{@bridge_proc}").Output()
	if err != nil {
		return true
	}
	known := map[string]bool{"claude": true, "codex": true, "cursor-agent": true}
	isKnown := func(cmd string) bool {
		base := path.Base(strings.TrimSpace(cmd))
		if m := wrappedRe.FindStringSubmatch(base); m != nil {
			base = m[1]
		}
		return known[base]
	}
	for line := range strings.Lines(string(out)) {
		parts := strings.SplitN(strings.TrimRight(line, "\n"), "|", 2)
		if isKnown(parts[0]) {
			return true
		}
		if len(parts) == 2 && parts[1] != "" && isKnown(parts[1]) {
			return true
		}
	}
	return false
}

func usageColor(pct float64, a args) string {
	switch {
	case pct >= 90:
		return a.thmRed
	case pct >= 70:
		return a.thmPeach
	default:
		return a.thmGreen
	}
}

func (a args) usageIcon(agent string) string {
	switch agent {
	case "claude":
		return a.iconUsageClaude
	case "codex":
		return a.iconUsageCodex
	default:
		return a.iconUsageCursor
	}
}

// usageResetGlyph is a 1-cell nerd refresh mark. The previous ↻ (U+21BB) reads
// dense next to the %·label run and often measures 2 cells in terminal fonts.
const usageResetGlyph = "󰑐" // nerd: nf-md-refresh

// usageResetSuffix appends a " <glyph> <dur>" countdown to a nearly-exhausted
// window, the moment the reset time starts to matter. Duration granularity
// matches the window's own: minutes under an hour, hours under two days, then days.
func usageResetSuffix(w usageWindow, now int64) string {
	if w.Pct < 90 || w.ResetAt <= now {
		return ""
	}
	d := w.ResetAt - now
	switch {
	case d < 3600:
		return fmt.Sprintf(" %s %dm", usageResetGlyph, max(d/60, 1))
	case d < 172800:
		return fmt.Sprintf(" %s %dh", usageResetGlyph, d/3600)
	default:
		return fmt.Sprintf(" %s %dd", usageResetGlyph, d/86400)
	}
}

// usageSegment renders "<icon> <pct>·<label> …" per agent with data, joined
// and trailing-padded for the right-aligned group. The monthly window only
// appears at/above the configured threshold; an agent with nothing to show
// (e.g. an uncapped enterprise tier) drops out entirely.
func usageSegment(a args, caches map[string]usageCache, now int64) string {
	if a.usageMonthlyThreshold <= 0 {
		return ""
	}
	render := func(w usageWindow) string {
		s := "#[fg=" + usageColor(w.Pct, a) + "]" + fmt.Sprintf("%.0f%%·%s", w.Pct, w.Label)
		if suf := usageResetSuffix(w, now); suf != "" {
			s += "#[fg=" + a.thmSubtext0 + "]" + suf
		}
		return s
	}
	var blocks []string
	for _, agent := range usageAgentOrder {
		c, ok := caches[agent]
		if !ok {
			continue
		}
		var parts []string
		for _, w := range c.Windows {
			parts = append(parts, render(w))
		}
		if c.Monthly != nil && c.Monthly.Pct >= float64(a.usageMonthlyThreshold) {
			parts = append(parts, render(*c.Monthly))
		}
		if len(parts) == 0 {
			continue
		}
		blocks = append(blocks, "#[fg="+a.thmSubtext0+"]"+a.usageIcon(agent)+" "+strings.Join(parts, " "))
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "  ") + "  "
}
