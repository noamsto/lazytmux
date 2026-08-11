package main

// This file holds the list shape's renderer and its chrome (search bar,
// separator, hint line), split out of tui.go (#286).

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func (m tuiModel) renderList() string {
	h := m.listHeight()
	w := m.listWidth()

	selBgHex := m.thmColorHex("@thm_surface_2", "#45475a", "#acb0be")
	selBg := lipgloss.Color(selBgHex)
	selStyle := lipgloss.NewStyle().
		Background(selBg)
	// ANSI reset (\033[0m) inside display strings kills the background.
	// Replace resets with "reset fg + re-apply bg" so background persists.
	selResetKeepBg := "\033[39m" + ansiBg(selBgHex) // reset fg only, re-set bg
	start := m.scrollStart(h)

	lines := make([]string, 0, h)
	for i := start; i < start+h && i < len(m.visible); i++ {
		item := m.visible[i]
		if i == m.cursor {
			patched := strings.ReplaceAll(item.display, "\033[0m", selResetKeepBg)
			line := fitVisibleWidth("▶ "+patched, w)
			lines = append(lines, selStyle.Render(line))
		} else {
			lines = append(lines, fitVisibleWidth("  "+item.display, w))
		}
	}
	empty := strings.Repeat(" ", w)
	for len(lines) < h {
		lines = append(lines, empty)
	}

	return strings.Join(lines, "\n")
}

func (m tuiModel) renderSeparator() string {
	sepColor := lipgloss.NewStyle().
		Foreground(m.thmColor("@thm_surface_1", "#45475a", "#9ca0b0"))
	return sepColor.Render(strings.Repeat("─", m.innerWidth()))
}

func (m tuiModel) renderSearch() string {
	blue := lipgloss.NewStyle().Foreground(m.thmColor("@thm_blue", "#89b4fa", "#1e66f5"))
	dim := lipgloss.NewStyle().Foreground(m.thmColor("@thm_surface_2", "#585b70", "#9ca0b0"))

	icon := blue.Render("  ")
	var queryStr string
	if m.query == "" {
		queryStr = dim.Render("type to filter...") + " "
	} else {
		queryStr = m.query + "█"
	}

	return lipgloss.NewStyle().
		Width(m.width).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(m.thmColor("@thm_surface_1", "#45475a", "#9ca0b0")).
		Render(icon + queryStr)
}

func (m tuiModel) renderHints() string {
	dim := lipgloss.NewStyle().Foreground(m.thmColor("@thm_surface_2", "#585b70", "#9ca0b0"))
	key := lipgloss.NewStyle().Foreground(m.thmColor("@thm_lavender", "#b4befe", "#7287fd"))

	if m.statusMsg != "" {
		red := lipgloss.NewStyle().Foreground(m.thmColor("@thm_red", "#f38ba8", "#d20f39"))
		return fitVisibleWidth(red.Render("  "+m.statusMsg), m.width)
	}

	hint := func(k, desc string) string {
		return key.Render(k) + dim.Render(":"+desc)
	}

	highlight := lipgloss.NewStyle().Foreground(m.thmColor("@thm_peach", "#fab387", "#fe640b"))

	agentLabel := "agents"
	if m.agentOnly {
		agentLabel = highlight.Render(agentLabel)
	}
	scratchLabel := "scratch"
	if m.scratchOnly {
		scratchLabel = highlight.Render(scratchLabel)
	}

	item, hasItem := m.currentItem()

	killLabel := "kill"
	if hasItem && item.createPath != "" {
		killLabel = "forget"
	}

	// ^/ goes back to the wall in a wall-launched popup, and toggles the preview
	// in every other one — the label has to say which.
	toggleLabel := "preview"
	if m.wallLaunched {
		toggleLabel = "wall"
	}

	// Emit mode never switches or creates — enter only writes the pick back to
	// the wrapper (spec D8) — and inherits no session/window to kill or forget.
	enterLabel := "open"
	if m.emitPath != "" {
		enterLabel = "pick"
	}

	parts := []string{
		hint("^jk/↑↓", "nav"),
		hint("enter", enterLabel),
	}
	if m.emitPath == "" {
		parts = append(parts, hint("^x", killLabel))
	}
	parts = append(parts,
		hint("^a", agentLabel),
		hint("^s", scratchLabel),
	)
	if m.windowMode {
		groupLabel := "group"
		if m.stateGrouped {
			groupLabel = highlight.Render(groupLabel)
		}
		parts = append(parts, hint("^g", groupLabel))
	}
	if hasItem && item.remoteHost != "" {
		parts = append(parts, hint("^o", "remote"))
	}
	parts = append(parts,
		hint("^/", toggleLabel),
		hint("M-hjkl", "scroll"),
		hint("q", "quit"),
	)

	// Clipped, not width-styled: a lipgloss Width() wraps this keymap onto a
	// second line at a narrow width, and bodyHeight has reserved exactly one
	// (renderWallHints clips for the same reason).
	return fitVisibleWidth("  "+strings.Join(parts, "  "), m.width)
}
