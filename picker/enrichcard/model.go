package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/noamsto/lazytmux/picker/enrichstate"
)

// cfg holds the launch-time flags: target, the poller binary to spawn for
// refresh, the theme palette, and the enrich glyphs (raw — NOT ##-escaped).
type cfg struct {
	target, prEnrichBin string
	bg, fg, mauve, red, green, peach, blue, overlay0, subtext0, lavender string
	icLinear, icGitHub, icPending, icSuccess, icFailure, icMerged, icClosed, icConflict string
}

type model struct {
	cfg           cfg
	win           winState
	width, height int
	refreshing    bool
	flash         string // transient footer note ("opened ↗"), cleared on next tick
}

const (
	widthFloor  = 34 // below this: drop the worktree path line, truncate harder
	heightFloor = 12 // below this: drop the Claude block, keep identity + footer
)

func (m model) sty(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

func (m model) colorFor(r enrichstate.ColorRole) string {
	switch r {
	case enrichstate.ColorMerged:
		return m.cfg.mauve
	case enrichstate.ColorClosed:
		return m.cfg.overlay0
	case enrichstate.ColorFailure:
		return m.cfg.red
	case enrichstate.ColorPending:
		return m.cfg.peach
	default:
		return m.cfg.green
	}
}

func (m model) glyphFor(r enrichstate.GlyphRole) string {
	switch r {
	case enrichstate.GlyphMerged:
		return m.cfg.icMerged
	case enrichstate.GlyphClosed:
		return m.cfg.icClosed
	case enrichstate.GlyphConflict:
		return m.cfg.icConflict
	case enrichstate.GlyphFailure:
		return m.cfg.icFailure
	case enrichstate.GlyphPending:
		return m.cfg.icPending
	default:
		return m.cfg.icSuccess
	}
}

func (m model) titleWidth() int {
	w := m.width - 6 // border + padding
	if w < 10 {
		return 10
	}
	if w > 80 {
		return 80
	}
	return w
}

func truncate(s string, max int) string {
	if max <= 1 || lipgloss.Width(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) > max-1 {
		r = r[:max-1]
	}
	return string(r) + "…"
}

func (m model) issueBlock() string {
	c, w := m.cfg, m.win
	if w.issueID == "" {
		return m.sty(c.overlay0).Render("no issue")
	}
	glyph := c.icGitHub
	if w.issueProvider == "linear" {
		glyph = c.icLinear
	}
	head := m.sty(c.blue).Bold(true).Render(glyph + "  " + w.issueID)
	title := m.sty(c.fg).Render(truncate(w.issueTitle, m.titleWidth()))
	return lipgloss.JoinVertical(lipgloss.Left, head, title)
}

func (m model) prBlock() string {
	c, w := m.cfg, m.win
	if w.prNumber == "" || w.prNumber == "none" {
		return m.sty(c.overlay0).Render("no PR")
	}
	if m.refreshing {
		return m.sty(c.peach).Render("⧗ #" + w.prNumber + " refreshing…")
	}
	cr, gr := enrichstate.Classify(w.prState, w.prCheck, w.prMergeable)
	badge := m.sty(m.colorFor(cr)).Render(m.glyphFor(gr) + " #" + w.prNumber)
	title := m.sty(c.fg).Render(truncate(w.prTitle, m.titleWidth()))
	return lipgloss.JoinVertical(lipgloss.Left, badge, title)
}

func (m model) branchBlock() string {
	c, w := m.cfg, m.win
	target := "main"
	line := m.sty(c.subtext0).Render(w.branch + "  →  " + target)
	dir := w.worktree
	if dir == "" {
		dir = w.gitRoot
	}
	if dir == "" || m.width < widthFloor {
		return line
	}
	return lipgloss.JoinVertical(lipgloss.Left, line, m.sty(c.overlay0).Render(truncate(dir, m.titleWidth())))
}

func (m model) claudeBlock() string {
	c, w := m.cfg, m.win
	parts := []string{}
	if w.paneIcon != "" {
		parts = append(parts, w.paneIcon)
	}
	if w.task != "" {
		parts = append(parts, w.task)
	}
	if w.claudeAgo != "" {
		parts = append(parts, w.claudeAgo)
	}
	if len(parts) == 0 {
		return ""
	}
	return m.sty(c.subtext0).Render(strings.Join(parts, "  ·  "))
}

func (m model) footer() string {
	c := m.cfg
	items := []string{"[o] issue", "[p] PR"}
	if m.win.branch == "" {
		items = append(items, m.sty(c.overlay0).Render("[r] no branch"))
	} else {
		items = append(items, "[r] refresh")
	}
	items = append(items, "[q] close")
	if m.flash != "" {
		items = append(items, m.sty(c.green).Render(m.flash))
	}
	return m.sty(c.subtext0).Render(strings.Join(items, "   "))
}

// card renders the full bordered popup. Pure over model state (no tmux calls).
func (m model) card() string {
	rows := []string{m.issueBlock(), "", m.prBlock()}
	if m.height >= heightFloor {
		rows = append(rows, "", m.branchBlock())
		if cb := m.claudeBlock(); cb != "" {
			rows = append(rows, "", cb)
		}
	}
	rows = append(rows, "", m.footer())
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.cfg.overlay0)).
		Padding(0, 1).
		Render(inner)
}
