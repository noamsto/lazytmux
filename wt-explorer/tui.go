package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// Catppuccin Mocha palette.
var (
	colorRed    = lipgloss.Color("#f38ba8")
	colorGreen  = lipgloss.Color("#a6e3a1")
	colorBlue   = lipgloss.Color("#89b4fa")
	colorDim    = lipgloss.Color("#6c7086")
	colorText   = lipgloss.Color("#cdd6f4")
	colorPeach  = lipgloss.Color("#fab387")
)

// Styles.
var (
	staleStyle     = lipgloss.NewStyle().Foreground(colorRed)
	selectedStyle  = lipgloss.NewStyle().Foreground(colorGreen)
	cursorStyle    = lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(colorDim)
	headerStyle    = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	warnStyle      = lipgloss.NewStyle().Foreground(colorPeach)
	borderStyle    = lipgloss.NewStyle().Foreground(colorDim)
	statusBarStyle = lipgloss.NewStyle().Foreground(colorDim)
)

type model struct {
	repoRoot      string
	worktrees     []Worktree
	visible       []int // indices into worktrees after filtering
	cursor        int
	selected      map[int]bool
	detailsLoaded map[int]bool
	query         string
	preview       viewport.Model
	width         int
	height        int
	ready         bool
	confirmMsg    string
	confirmForce  bool
	statusMsg     string
}

func runTUI(repoRoot string, worktrees []Worktree) error {
	m := model{
		repoRoot:      repoRoot,
		worktrees:     worktrees,
		selected:      make(map[int]bool),
		detailsLoaded: make(map[int]bool),
		preview:       viewport.New(),
	}

	// The first item has details pre-loaded by main.go.
	if len(worktrees) > 0 {
		m.detailsLoaded[0] = true
	}

	m.filterVisible()

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.updatePreviewSize()
		m.loadCurrentDetails()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Confirmation mode: only y/n.
	if m.confirmMsg != "" {
		switch msg.String() {
		case "y", "Y":
			m.executeDelete()
		default:
			m.statusMsg = "Cancelled."
		}
		m.confirmMsg = ""
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "q", "esc":
		if m.query != "" {
			m.query = ""
			m.filterVisible()
			m.loadCurrentDetails()
			return m, nil
		}
		return m, tea.Quit

	case "up", "k":
		m.moveCursor(-1)
		return m, nil

	case "down", "j":
		m.moveCursor(1)
		return m, nil

	case " ":
		if len(m.visible) > 0 {
			idx := m.visible[m.cursor]
			if m.selected[idx] {
				delete(m.selected, idx)
			} else {
				m.selected[idx] = true
			}
		}
		return m, nil

	case "a":
		for _, idx := range m.visible {
			if m.worktrees[idx].IsStale() {
				m.selected[idx] = true
			}
		}
		return m, nil

	case "d":
		m.startDelete(false)
		return m, nil

	case "D":
		m.startDelete(true)
		return m, nil

	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.filterVisible()
			m.loadCurrentDetails()
		}
		return m, nil

	default:
		// Printable character: append to search.
		key := msg.Key()
		if key.Text != "" && key.Mod == 0 {
			m.query += key.Text
			m.filterVisible()
			m.loadCurrentDetails()
		}
		return m, nil
	}
}

func (m *model) moveCursor(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	m.loadCurrentDetails()
}

func (m *model) loadCurrentDetails() {
	if len(m.visible) == 0 {
		return
	}
	idx := m.visible[m.cursor]
	if !m.detailsLoaded[idx] {
		loadWorktreeDetails(&m.worktrees[idx])
		m.detailsLoaded[idx] = true
	}
}

func (m *model) filterVisible() {
	m.visible = m.visible[:0]
	q := strings.ToLower(m.query)
	for i := range m.worktrees {
		if q == "" || strings.Contains(strings.ToLower(m.worktrees[i].Branch), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

func (m *model) startDelete(force bool) {
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return
	}
	var names []string
	for _, idx := range targets {
		names = append(names, m.worktrees[idx].Branch)
	}
	verb := "Remove"
	if force {
		verb = "Force remove"
	}
	m.confirmMsg = fmt.Sprintf("%s %d worktree(s) [%s]? y/n", verb, len(targets), strings.Join(names, ", "))
	m.confirmForce = force
}

func (m *model) deleteTargets() []int {
	var targets []int
	for _, idx := range m.visible {
		if m.selected[idx] {
			targets = append(targets, idx)
		}
	}
	if len(targets) == 0 && len(m.visible) > 0 {
		targets = []int{m.visible[m.cursor]}
	}
	return targets
}

func (m *model) executeDelete() {
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return
	}

	removedSet := make(map[int]bool, len(targets))
	var removed, failed int
	var lastErr string
	for _, idx := range targets {
		wt := m.worktrees[idx]
		err := removeWorktree(m.repoRoot, wt.Path, m.confirmForce)
		if err != nil {
			failed++
			lastErr = fmt.Sprintf("Error removing %s: %v", wt.Branch, err)
		} else {
			killTmuxWindow(m.repoRoot, wt.Path)
			removedSet[idx] = true
			removed++
		}
	}

	if removed > 0 {
		// Rebuild worktrees slice.
		var newWorktrees []Worktree
		indexMap := make(map[int]int) // old index -> new index
		for i, wt := range m.worktrees {
			if !removedSet[i] {
				indexMap[i] = len(newWorktrees)
				newWorktrees = append(newWorktrees, wt)
			}
		}

		// Rebuild maps with new indices.
		newSelected := make(map[int]bool)
		for oldIdx := range m.selected {
			if newIdx, ok := indexMap[oldIdx]; ok {
				newSelected[newIdx] = true
			}
		}
		newDetailsLoaded := make(map[int]bool)
		for oldIdx := range m.detailsLoaded {
			if newIdx, ok := indexMap[oldIdx]; ok {
				newDetailsLoaded[newIdx] = true
			}
		}

		m.worktrees = newWorktrees
		m.selected = newSelected
		m.detailsLoaded = newDetailsLoaded
		m.filterVisible()
	}

	switch {
	case failed == 0:
		m.statusMsg = fmt.Sprintf("Removed %d worktree(s).", removed)
	case removed == 0:
		m.statusMsg = lastErr
	default:
		m.statusMsg = fmt.Sprintf("Removed %d, failed %d. %s", removed, failed, lastErr)
	}
}

func (m *model) updatePreviewSize() {
	previewW, previewH := m.previewDimensions()
	m.preview = viewport.New(
		viewport.WithWidth(previewW),
		viewport.WithHeight(previewH),
	)
}

// listWidth returns the width of the left list pane.
func (m *model) listWidth() int {
	w := m.width * 3 / 5
	if w < 30 {
		w = 30
	}
	if w > m.width-20 {
		w = m.width - 20
	}
	return w
}

func (m *model) previewDimensions() (int, int) {
	lw := m.listWidth()
	pw := m.width - lw - 3 // 3 for " | " separator
	if pw < 10 {
		pw = 10
	}
	// Height: total - search header (2) - column headers (1) - separator (1) - help (1) - status (1) = 6
	ph := m.height - 6
	if ph < 3 {
		ph = 3
	}
	return pw, ph
}

func (m model) View() tea.View {
	var content string
	if !m.ready {
		content = "Loading..."
	} else {
		content = m.renderFull()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m *model) renderFull() string {
	var b strings.Builder

	// Search box.
	searchLine := dimStyle.Render("filter: ") + m.query
	if m.query == "" {
		searchLine = dimStyle.Render("filter: (type to search)")
	}
	searchHeader := borderStyle.Render("── Search " + strings.Repeat("─", max(0, m.width-10)))
	b.WriteString(searchHeader + "\n")
	b.WriteString(searchLine + "\n")

	// Column headers.
	lw := m.listWidth()
	_, ph := m.previewDimensions()

	listHeader := padRight(headerStyle.Render(" Worktrees"), lw)
	previewHeader := headerStyle.Render(" Details")
	b.WriteString(listHeader + borderStyle.Render(" │ ") + previewHeader + "\n")

	// Build list lines.
	listLines := m.renderListLines(lw, ph)

	// Build preview content.
	previewContent := m.renderPreview()
	m.preview.SetContent(previewContent)
	previewRendered := m.preview.View()
	previewLines := strings.Split(previewRendered, "\n")

	// Side by side.
	for i := range ph {
		var left, right string
		if i < len(listLines) {
			left = listLines[i]
		}
		left = padRight(left, lw)

		if i < len(previewLines) {
			right = previewLines[i]
		}

		b.WriteString(left + borderStyle.Render(" │ ") + right + "\n")
	}

	// Help line.
	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width)) + "\n")
	b.WriteString(dimStyle.Render("up/down navigate  space select  a sel stale  d/D delete  q quit") + "\n")

	// Status or confirmation.
	if m.confirmMsg != "" {
		b.WriteString(warnStyle.Render(m.confirmMsg))
	} else {
		b.WriteString(m.renderStatusLine())
	}

	return b.String()
}

func (m *model) renderListLines(width, height int) []string {
	lines := make([]string, 0, height)

	// Scroll window.
	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}

	for i, idx := range m.visible {
		if i < start {
			continue
		}
		if len(lines) >= height {
			break
		}

		wt := m.worktrees[idx]
		var line strings.Builder

		// Cursor.
		if i == m.cursor {
			line.WriteString(cursorStyle.Render("> "))
		} else {
			line.WriteString("  ")
		}

		// Selection checkmark.
		if m.selected[idx] {
			line.WriteString(selectedStyle.Render("✓"))
		} else {
			line.WriteString(" ")
		}

		// Stale marker.
		if wt.IsStale() {
			line.WriteString(staleStyle.Render("●"))
		} else {
			line.WriteString(" ")
		}

		// Branch name.
		line.WriteString(" ")
		if i == m.cursor {
			line.WriteString(cursorStyle.Render(wt.Branch))
		} else {
			line.WriteString(wt.Branch)
		}

		// Stale reason tag.
		if wt.IsStale() {
			line.WriteString(" ")
			line.WriteString(staleStyle.Render("[" + wt.StaleReason + "]"))
		}

		lines = append(lines, truncateToWidth(line.String(), width))
	}

	// Pad with empty lines.
	for len(lines) < height {
		lines = append(lines, "")
	}

	return lines
}

func (m *model) renderPreview() string {
	if len(m.visible) == 0 {
		return dimStyle.Render("No worktrees to display.")
	}

	idx := m.visible[m.cursor]
	wt := m.worktrees[idx]

	var b strings.Builder

	b.WriteString(headerStyle.Render("Branch: ") + wt.Branch + "\n")
	b.WriteString(headerStyle.Render("Path:   ") + wt.Path + "\n")

	if wt.IsStale() {
		b.WriteString(headerStyle.Render("Stale:  ") + staleStyle.Render(wt.StaleReason) + "\n")
	}

	if !m.detailsLoaded[idx] {
		b.WriteString("\n" + dimStyle.Render("Loading details..."))
		return b.String()
	}

	b.WriteString("\n")

	if wt.DirtyFiles > 0 {
		b.WriteString(warnStyle.Render(fmt.Sprintf("Dirty files: %d", wt.DirtyFiles)) + "\n")
	} else {
		b.WriteString(dimStyle.Render("Dirty files: 0") + "\n")
	}

	b.WriteString("\n")

	if len(wt.UnpushedLog) > 0 {
		b.WriteString(warnStyle.Render("Unpushed commits:") + "\n")
		for _, line := range wt.UnpushedLog {
			b.WriteString("  " + warnStyle.Render(line) + "\n")
		}
	} else {
		b.WriteString(dimStyle.Render("Unpushed commits: none") + "\n")
	}

	b.WriteString("\n")

	if wt.LastCommit != "" {
		b.WriteString(headerStyle.Render("Last commit:") + "\n")
		b.WriteString("  " + wt.LastCommit + "\n")
	} else {
		b.WriteString(dimStyle.Render("Last commit: none") + "\n")
	}

	return b.String()
}

func (m *model) renderStatusLine() string {
	if m.statusMsg != "" {
		return statusBarStyle.Render(m.statusMsg)
	}

	total := len(m.worktrees)
	stale := 0
	for i := range m.worktrees {
		if m.worktrees[i].IsStale() {
			stale++
		}
	}
	sel := len(m.selected)
	return statusBarStyle.Render(fmt.Sprintf("%d worktrees, %d stale, %d selected", total, stale, sel))
}

// padRight pads a string with spaces to the given visible width.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncateToWidth truncates a string to fit within the given visible width.
func truncateToWidth(s string, width int) string {
	w := lipgloss.Width(s)
	if w <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}
