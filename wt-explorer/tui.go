package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

var (
	colorRed   = lipgloss.Color("#f38ba8")
	colorGreen = lipgloss.Color("#a6e3a1")
	colorBlue  = lipgloss.Color("#89b4fa")
	colorDim   = lipgloss.Color("#6c7086")
	colorText  = lipgloss.Color("#cdd6f4")
	colorPeach = lipgloss.Color("#fab387")
)

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
	repoRoot     string
	worktrees    []Worktree
	visible      []int
	cursor       int
	selected     map[int]bool
	query        string
	searching    bool // true = typing into search bar
	preview      viewport.Model
	width        int
	height       int
	ready        bool
	confirmMsg   string
	confirmForce bool
	statusMsg    string
	staleCount   int
}

// detailsLoadedMsg is sent when async detail loading completes.
type detailsLoadedMsg struct {
	index int
}

func runTUI(repoRoot string, worktrees []Worktree) error {
	m := model{
		repoRoot: repoRoot,
		worktrees: worktrees,
		selected:  make(map[int]bool),
		preview:   viewport.New(),
	}
	m.filterVisible()
	m.recomputeStaleCount()

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func (m model) Init() tea.Cmd {
	// Kick off async detail load for the first item
	if len(m.visible) > 0 {
		idx := m.visible[0]
		if !m.worktrees[idx].DetailsLoaded {
			return m.loadDetailsCmd(idx)
		}
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.updatePreviewSize()
		return m, nil

	case detailsLoadedMsg:
		m.worktrees[msg.index].DetailsLoaded = true
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Confirmation mode: only y/anything-else
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

	// Search mode: typing goes into the filter
	if m.searching {
		return m.handleSearchKey(msg)
	}

	// Normal mode: navigation and actions
	return m.handleNormalKey(msg)
}

func (m model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.searching = false
		return m, nil
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, nil
	default:
		key := msg.Key()
		if key.Text != "" && key.Mod == 0 {
			m.query += key.Text
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, nil
	}
}

func (m model) handleNormalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "q", "esc":
		if m.query != "" {
			m.query = ""
			m.filterVisible()
			return m, m.ensureDetailsLoaded()
		}
		return m, tea.Quit

	case "/":
		m.searching = true
		return m, nil

	case "up", "k":
		return m, m.moveCursor(-1)

	case "down", "j":
		return m, m.moveCursor(1)

	case "space", " ":
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
	}

	return m, nil
}

func (m *model) moveCursor(delta int) tea.Cmd {
	if len(m.visible) == 0 {
		return nil
	}
	m.cursor += delta
	m.cursor = max(0, min(m.cursor, len(m.visible)-1))
	return m.ensureDetailsLoaded()
}

// ensureDetailsLoaded returns a Cmd to load details for the cursor item if needed.
func (m *model) ensureDetailsLoaded() tea.Cmd {
	if len(m.visible) == 0 {
		return nil
	}
	idx := m.visible[m.cursor]
	if !m.worktrees[idx].DetailsLoaded {
		return m.loadDetailsCmd(idx)
	}
	return nil
}

// loadDetailsCmd returns a Cmd that loads worktree details in a goroutine.
func (m *model) loadDetailsCmd(idx int) tea.Cmd {
	wt := &m.worktrees[idx]
	return func() tea.Msg {
		loadWorktreeDetails(wt)
		return detailsLoadedMsg{index: idx}
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
		var newWorktrees []Worktree
		indexMap := make(map[int]int)
		for i, wt := range m.worktrees {
			if !removedSet[i] {
				indexMap[i] = len(newWorktrees)
				newWorktrees = append(newWorktrees, wt)
			}
		}

		newSelected := make(map[int]bool)
		for oldIdx := range m.selected {
			if newIdx, ok := indexMap[oldIdx]; ok {
				newSelected[newIdx] = true
			}
		}

		m.worktrees = newWorktrees
		m.selected = newSelected
		m.filterVisible()
		m.recomputeStaleCount()
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

func (m *model) recomputeStaleCount() {
	m.staleCount = 0
	for i := range m.worktrees {
		if m.worktrees[i].IsStale() {
			m.staleCount++
		}
	}
}

func (m *model) updatePreviewSize() {
	previewW, previewH := m.previewDimensions()
	m.preview = viewport.New(
		viewport.WithWidth(previewW),
		viewport.WithHeight(previewH),
	)
}

func (m *model) listWidth() int {
	w := m.width * 3 / 5
	return max(30, min(w, m.width-20))
}

func (m *model) previewDimensions() (int, int) {
	lw := m.listWidth()
	pw := max(10, m.width-lw-3)
	ph := max(3, m.height-6)
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

	var searchLine string
	if m.searching {
		searchLine = cursorStyle.Render("/") + m.query + cursorStyle.Render("│")
	} else if m.query != "" {
		searchLine = dimStyle.Render("/") + m.query
	} else {
		searchLine = dimStyle.Render("/ to search")
	}
	b.WriteString(borderStyle.Render("── Search " + strings.Repeat("─", max(0, m.width-10))) + "\n")
	b.WriteString(searchLine + "\n")

	lw := m.listWidth()
	_, ph := m.previewDimensions()

	b.WriteString(padRight(headerStyle.Render(" Worktrees"), lw) + borderStyle.Render(" │ ") + headerStyle.Render(" Details") + "\n")

	listLines := m.renderListLines(lw, ph)

	previewContent := m.renderPreview()
	m.preview.SetContent(previewContent)
	previewRendered := m.preview.View()
	previewLines := strings.Split(previewRendered, "\n")

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

	b.WriteString(borderStyle.Render(strings.Repeat("─", m.width)) + "\n")
	if m.searching {
		b.WriteString(dimStyle.Render("type to filter  enter/esc accept  q clear+quit") + "\n")
	} else {
		b.WriteString(dimStyle.Render("j/k navigate  space select  a sel stale  d/D delete  / search  q quit") + "\n")
	}

	if m.confirmMsg != "" {
		b.WriteString(warnStyle.Render(m.confirmMsg))
	} else if m.statusMsg != "" {
		b.WriteString(statusBarStyle.Render(m.statusMsg))
	} else {
		b.WriteString(statusBarStyle.Render(fmt.Sprintf("%d worktrees, %d stale, %d selected",
			len(m.worktrees), m.staleCount, len(m.selected))))
	}

	return b.String()
}

func (m *model) renderListLines(width, height int) []string {
	lines := make([]string, 0, height)

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

		if i == m.cursor {
			line.WriteString(cursorStyle.Render("> "))
		} else {
			line.WriteString("  ")
		}

		if m.selected[idx] {
			line.WriteString(selectedStyle.Render("✓"))
		} else {
			line.WriteString(" ")
		}

		if wt.IsStale() {
			line.WriteString(staleStyle.Render("●"))
		} else {
			line.WriteString(" ")
		}

		line.WriteString(" ")
		if i == m.cursor {
			line.WriteString(cursorStyle.Render(wt.Branch))
		} else {
			line.WriteString(wt.Branch)
		}

		if wt.IsStale() {
			line.WriteString(" ")
			line.WriteString(staleStyle.Render("[" + wt.StaleReason + "]"))
		}

		lines = append(lines, truncateToWidth(line.String(), width))
	}

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
	pw, _ := m.previewDimensions()
	sep := dimStyle.Render(strings.Repeat("─", max(0, pw-1)))

	var b strings.Builder

	// Header: branch + path
	b.WriteString(headerStyle.Render("  " + wt.Branch) + "\n")
	b.WriteString(dimStyle.Render("  " + wt.Path) + "\n")

	if wt.IsStale() {
		b.WriteString(staleStyle.Render("  ● " + wt.StaleReason) + "\n")
	}

	if !wt.DetailsLoaded {
		b.WriteString("\n" + dimStyle.Render("  Loading..."))
		return b.String()
	}

	b.WriteString(sep + "\n")

	// Status indicators — compact one-line summary
	dirtyLabel := dimStyle.Render("  clean")
	if wt.DirtyFiles > 0 {
		dirtyLabel = warnStyle.Render(fmt.Sprintf("  %d dirty file(s)", wt.DirtyFiles))
	}
	b.WriteString(dirtyLabel + "\n")

	unpushedLabel := dimStyle.Render("  pushed")
	if len(wt.UnpushedLog) > 0 {
		unpushedLabel = warnStyle.Render(fmt.Sprintf("  %d unpushed commit(s)", len(wt.UnpushedLog)))
	}
	b.WriteString(unpushedLabel + "\n")

	// Unpushed commit details
	if len(wt.UnpushedLog) > 0 {
		b.WriteString(sep + "\n")
		b.WriteString(headerStyle.Render("  Unpushed") + "\n")
		for _, line := range wt.UnpushedLog {
			b.WriteString(dimStyle.Render("  ") + line + "\n")
		}
	}

	// Last commit
	if wt.LastCommit != "" {
		b.WriteString(sep + "\n")
		b.WriteString(headerStyle.Render("  Last commit") + "\n")
		b.WriteString(dimStyle.Render("  ") + wt.LastCommit + "\n")
	}

	return b.String()
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncateToWidth trims a string (possibly containing ANSI escapes) to fit
// within the given visible width, using a linear left-to-right scan.
func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	// Build from left, tracking visible width
	var result []rune
	visW := 0
	inEscape := false
	for _, r := range runes {
		if r == '\x1b' {
			inEscape = true
			result = append(result, r)
			continue
		}
		if inEscape {
			result = append(result, r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		rw := lipgloss.Width(string(r))
		if visW+rw > width {
			break
		}
		visW += rw
		result = append(result, r)
	}
	return string(result)
}
