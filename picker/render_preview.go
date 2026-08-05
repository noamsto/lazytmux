package main

import "charm.land/lipgloss/v2"

// renderPreview stacks the list pane, the separator and the viewport — the
// shape View() built inline before the renderers were split (#286).
func (m tuiModel) renderPreview(listPane string) string {
	return lipgloss.JoinVertical(lipgloss.Left, listPane, m.renderSeparator(), m.preview.View())
}
