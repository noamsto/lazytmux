package main

import (
	"crypto/md5"
	"fmt"
	imgcolor "image/color"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// listItem is one row in the picker list.
type listItem struct {
	target          string // tmux target; "" = unselectable
	display         string // ANSI-rendered display line
	plain           string // display stripped of ANSI (cached for width)
	searchText      string // filterable text (name, branch — no paths/icons)
	isHeader        bool   // session header row
	session         string // owning session name (for kill)
	hasActiveClaude bool   // used for --claude filter
	isScratch       bool   // scratch-* session
}

// tuiModel is the bubbletea model for the picker.
type tuiModel struct {
	// Data
	allItems []listItem // unfiltered
	visible  []listItem // after query + mode filter
	cursor   int

	// Modes
	windowMode  bool
	claudeOnly  bool
	scratchOnly bool

	// Search
	query string

	// Preview
	preview        viewport.Model
	showPreview    bool
	previewFor     string // target currently loaded in preview
	previewRaw     string // unshifted content for horizontal scroll
	previewXOffset int    // horizontal scroll offset (in cells)

	// Refresh
	lastStructHash string // ASCII-only hash; spinner changes don't affect it

	// Layout
	width, height int
	ready         bool

	// Config
	theme    string
	tmuxOpts map[string]string
}

// --- Catppuccin palette (dark/light) ---

// thmColor reads a Catppuccin color from tmux options, falling back to defaults.
func (m tuiModel) thmColor(tmuxOpt, darkFallback, lightFallback string) imgcolor.Color {
	return lipgloss.Color(m.thmColorHex(tmuxOpt, darkFallback, lightFallback))
}

func (m tuiModel) thmColorHex(tmuxOpt, darkFallback, lightFallback string) string {
	if v, ok := m.tmuxOpts[tmuxOpt]; ok && v != "" {
		return v
	}
	if m.theme == "light" {
		return lightFallback
	}
	return darkFallback
}

// --- Messages ---

type tickMsg struct{}

type refreshMsg struct {
	items      []listItem
	structHash string
}

type previewMsg struct {
	content string
	target  string
}

// --- Entry point ---

func runTUI(windowMode, claudeOnly bool) error {
	theme := detectTheme()
	opts := readTmuxOpts()
	panes := collectClaudePanes()

	var items []listItem
	if windowMode {
		items = buildWindowItems(opts, panes, theme)
	} else {
		items = buildSessionItems(opts, panes, theme)
	}

	m := tuiModel{
		windowMode:     windowMode,
		claudeOnly:     claudeOnly,
		showPreview:    true,
		theme:          theme,
		tmuxOpts:       opts,
		allItems:       items,
		lastStructHash: structHash(items),
	}
	m = m.withFilter()
	m.cursor = m.firstSelectable(0)

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// --- Bubbletea interface ---

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), m.loadPreviewCmd())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.preview = viewport.New(
				viewport.WithWidth(m.previewWidth()),
				viewport.WithHeight(m.previewHeight()),
			)
			m.preview.MouseWheelEnabled = true
			m.ready = true
		} else {
			m.preview.SetWidth(m.previewWidth())
			m.preview.SetHeight(m.previewHeight())
		}
		return m, m.loadPreviewCmd()

	case tickMsg:
		return m, tea.Batch(tickCmd(), m.refreshDataCmd())

	case refreshMsg:
		m.allItems = msg.items
		m.lastStructHash = msg.structHash
		m = m.withFilter()
		if m.cursor >= len(m.visible) {
			m.cursor = m.firstSelectable(0)
		}
		return m, m.loadPreviewCmd()

	case previewMsg:
		if msg.target == m.currentTarget() {
			sameTarget := msg.target == m.previewFor
			m.previewRaw = msg.content
			m.previewFor = msg.target
			if sameTarget && m.previewXOffset > 0 {
				m.applyPreviewXOffset()
			} else {
				m.previewXOffset = 0
				m.preview.SetContent(msg.content)
			}
			if !sameTarget {
				m.preview.SetYOffset(m.preview.TotalLineCount())
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.preview, cmd = m.preview.Update(msg)
	return m, cmd
}

func (m tuiModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "q", "esc":
		if m.query != "" {
			m.query = ""
			m = m.withFilter()
			m.cursor = m.firstSelectable(0)
			return m, m.loadPreviewCmd()
		}
		return m, tea.Quit

	case "ctrl+j", "down":
		m = m.moveCursor(1)
		return m, m.loadPreviewCmd()

	case "ctrl+k", "up":
		m = m.moveCursor(-1)
		return m, m.loadPreviewCmd()

	case "enter":
		if t := m.currentTarget(); t != "" {
			exec.Command("tmux", "switch-client", "-t", t).Run() //nolint:errcheck
			return m, tea.Quit
		}

	case "ctrl+x":
		if t := m.currentTarget(); t != "" {
			if strings.Contains(t, ":") {
				exec.Command("tmux", "kill-window", "-t", t).Run() //nolint:errcheck
			} else {
				exec.Command("tmux", "kill-session", "-t", t).Run() //nolint:errcheck
			}
			return m, m.refreshDataCmd()
		}

	case "ctrl+a":
		m.claudeOnly = !m.claudeOnly
		if m.claudeOnly {
			m.scratchOnly = false
		}
		m = m.withFilter()
		m.cursor = m.firstSelectable(0)
		return m, m.loadPreviewCmd()

	case "ctrl+s":
		m.scratchOnly = !m.scratchOnly
		if m.scratchOnly {
			m.claudeOnly = false
		}
		m = m.withFilter()
		m.cursor = m.firstSelectable(0)
		return m, m.loadPreviewCmd()

	case "ctrl+/", "ctrl+_":
		m.showPreview = !m.showPreview
		if m.ready {
			m.preview.SetWidth(m.previewWidth())
		}
		return m, nil

	case "alt+j":
		m.preview.SetYOffset(m.preview.YOffset() + 3)

	case "alt+k":
		m.preview.SetYOffset(m.preview.YOffset() - 3)

	case "alt+l":
		m.previewXOffset += 8
		m.applyPreviewXOffset()

	case "alt+h":
		m.previewXOffset -= 8
		if m.previewXOffset < 0 {
			m.previewXOffset = 0
		}
		m.applyPreviewXOffset()

	case "backspace":
		if len(m.query) > 0 {
			runes := []rune(m.query)
			m.query = string(runes[:len(runes)-1])
			m = m.withFilter()
			m.cursor = m.firstSelectable(0)
			return m, m.loadPreviewCmd()
		}

	default:
		s := msg.String()
		if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
			m.query += s
			m = m.withFilter()
			m.cursor = m.firstSelectable(0)
			return m, m.loadPreviewCmd()
		}
	}
	return m, nil
}

// --- View ---

func (m tuiModel) View() tea.View {
	var content string
	if !m.ready {
		content = "Loading..."
	} else {
		listPane := m.renderList()
		var body string
		if m.showPreview {
			sep := m.renderSeparator()
			if m.portrait() {
				body = lipgloss.JoinVertical(lipgloss.Left, listPane, sep, m.preview.View())
			} else {
				body = lipgloss.JoinHorizontal(lipgloss.Top, listPane, sep, m.preview.View())
			}
		} else {
			body = listPane
		}
		borderColor := m.thmColor("@thm_surface_1", "#45475a", "#9ca0b0")
		bordered := lipgloss.NewStyle().
			Width(m.width).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(borderColor).
			Render(body)
		content = lipgloss.JoinVertical(lipgloss.Left, m.renderSearch(), bordered, m.renderHints())
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

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
	if m.portrait() {
		return sepColor.Render(strings.Repeat("─", m.innerWidth()))
	}
	h := m.listHeight()
	lines := make([]string, h)
	for i := range lines {
		lines[i] = "│"
	}
	return sepColor.Render(strings.Join(lines, "\n"))
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

	hint := func(k, desc string) string {
		return key.Render(k) + dim.Render(":"+desc)
	}

	highlight := lipgloss.NewStyle().Foreground(m.thmColor("@thm_peach", "#fab387", "#fe640b"))

	claudeLabel := "claude"
	if m.claudeOnly {
		claudeLabel = highlight.Render(claudeLabel)
	}
	scratchLabel := "scratch"
	if m.scratchOnly {
		scratchLabel = highlight.Render(scratchLabel)
	}

	parts := []string{
		hint("^jk/↑↓", "nav"),
		hint("enter", "open"),
		hint("^x", "kill"),
		hint("^a", claudeLabel),
		hint("^s", scratchLabel),
		hint("^/", "preview"),
		hint("M-hjkl", "scroll"),
		hint("q", "quit"),
	}

	return dim.Width(m.width).Render("  " + strings.Join(parts, "  "))
}

// --- Layout ---

// portrait returns true when preview should be below the list (narrow terminal).
func (m tuiModel) portrait() bool {
	return m.width < 2*m.height
}

// bodyHeight is the total height available for list + preview (excludes search/hints/borders).
func (m tuiModel) bodyHeight() int {
	h := m.height - 5 // search (3 with border) + bottom border (1) + hints (1)
	if h < 5 {
		return 5
	}
	return h
}

func (m tuiModel) listHeight() int {
	bh := m.bodyHeight()
	if !m.showPreview || !m.portrait() {
		return bh
	}
	// Portrait: list gets top 50%, preview gets bottom 50% (minus 1 for separator)
	return bh * 50 / 100
}

func (m tuiModel) innerWidth() int {
	return m.width // tmux popup provides the border
}

func (m tuiModel) listWidth() int {
	iw := m.innerWidth()
	if !m.showPreview || m.portrait() {
		return iw
	}
	pct := 60
	if m.windowMode {
		pct = 45
	}
	w := iw * pct / 100
	if w < 30 {
		return 30
	}
	return w
}

func (m tuiModel) previewWidth() int {
	if m.portrait() {
		return m.innerWidth()
	}
	return m.innerWidth() - m.listWidth() - 1 // -1 for separator
}

func (m tuiModel) previewHeight() int {
	if !m.portrait() {
		return m.bodyHeight()
	}
	return m.bodyHeight() - m.listHeight() - 1 // -1 for separator
}

func (m tuiModel) scrollStart(h int) int {
	start := m.cursor - h/2
	if start < 0 {
		start = 0
	}
	if start+h > len(m.visible) {
		start = len(m.visible) - h
		if start < 0 {
			start = 0
		}
	}
	return start
}

// --- Navigation ---

func (m tuiModel) moveCursor(delta int) tuiModel {
	n := len(m.visible)
	if n == 0 {
		return m
	}
	c := m.cursor
	for {
		c += delta
		if c < 0 || c >= n {
			// Ran past the edge — keep the current cursor rather than
			// landing on a non-selectable title/header row.
			return m
		}
		if m.isSelectable(m.visible[c]) {
			break
		}
	}
	m.cursor = c
	return m
}

func (m tuiModel) isSelectable(item listItem) bool {
	if item.target == "" {
		return false
	}
	// In window mode, session headers are not selectable
	return !item.isHeader || !m.windowMode
}

func (m tuiModel) firstSelectable(from int) int {
	for i := from; i < len(m.visible); i++ {
		if m.isSelectable(m.visible[i]) {
			return i
		}
	}
	return from
}

func (m tuiModel) currentTarget() string {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return ""
	}
	return m.visible[m.cursor].target
}

// --- Filter ---

// itemVisible reports whether an item passes the current mode filters
// (scratch/claude). Headers are always visible (pruned separately).
func (m tuiModel) itemVisible(item listItem) bool {
	if m.scratchOnly && !item.isScratch {
		return false
	}
	if !m.scratchOnly && item.isScratch {
		return false
	}
	if m.claudeOnly && !item.hasActiveClaude {
		return false
	}
	return true
}

func (m tuiModel) withFilter() tuiModel {
	q := strings.ToLower(m.query)

	// No search query — filter by mode only
	if q == "" {
		var out []listItem
		for _, item := range m.allItems {
			if item.isHeader {
				out = append(out, item)
				continue
			}
			if !m.itemVisible(item) {
				continue
			}
			out = append(out, item)
		}
		m.visible = pruneOrphanHeaders(out)
		return m
	}

	// Score and filter matchable items
	type scored struct {
		item  listItem
		score int
	}
	var matches []scored
	for _, item := range m.allItems {
		if item.isHeader {
			continue
		}
		if !m.itemVisible(item) {
			continue
		}
		s := fuzzyScore(strings.ToLower(item.searchText), q)
		if s >= 0 {
			matches = append(matches, scored{item: item, score: s})
		}
	}

	// Sort by score descending; stable preserves original order for ties
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if m.windowMode {
		// Re-group under session headers, ordered by best child score
		headerMap := make(map[string]listItem)
		for _, item := range m.allItems {
			if item.isHeader {
				headerMap[item.session] = item
			}
		}
		seen := make(map[string]bool)
		var out []listItem
		for _, match := range matches {
			if !seen[match.item.session] {
				seen[match.item.session] = true
				if h, ok := headerMap[match.item.session]; ok {
					out = append(out, h)
				}
			}
			out = append(out, match.item)
		}
		m.visible = out
	} else {
		out := make([]listItem, len(matches))
		for i, match := range matches {
			out[i] = match.item
		}
		m.visible = out
	}
	return m
}

func pruneOrphanHeaders(items []listItem) []listItem {
	out := make([]listItem, 0, len(items))
	for i, item := range items {
		if !item.isHeader {
			out = append(out, item)
			continue
		}
		hasChild := i+1 < len(items) && !items[i+1].isHeader
		if hasChild {
			out = append(out, item)
		}
	}
	return out
}

// --- Commands ---

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m tuiModel) refreshDataCmd() tea.Cmd {
	wm := m.windowMode
	opts := m.tmuxOpts
	theme := m.theme
	return func() tea.Msg {
		panes := collectClaudePanes()
		var items []listItem
		if wm {
			items = buildWindowItems(opts, panes, theme)
		} else {
			items = buildSessionItems(opts, panes, theme)
		}
		// Always send — spinners need to animate even without structural changes.
		return refreshMsg{items: items, structHash: structHash(items)}
	}
}

func (m tuiModel) loadPreviewCmd() tea.Cmd {
	t := m.currentTarget()
	if t == "" || !m.showPreview {
		return nil
	}
	return func() tea.Msg {
		out, err := exec.Command("tmux", "capture-pane", "-t", t, "-p", "-e").Output()
		if err != nil {
			return previewMsg{content: "(no preview available)", target: t}
		}
		content := strings.TrimRight(string(out), "\n ")
		// Reset background at end of each line to prevent ANSI color bleeding
		// into empty viewport padding cells (e.g. opencode's black input area).
		content = strings.ReplaceAll(content, "\n", "\033[49m\n") + "\033[49m"
		return previewMsg{content: content, target: t}
	}
}

// --- Item builders ---

func buildSessionItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string) []listItem {
	sessions := collectSessions()
	claudeMap := aggregateClaudeBySession(claudePanes)
	mergeClaude(sessions, claudeMap)

	// Resource collection runs in parallel with rendering prep (uses cached ps data)
	resCh := make(chan map[string]sessionResources, 1)
	go func() { resCh <- collectSessionResources(sessions) }()

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmBlue := envOrMap("THM_BLUE", tmuxOpts, "@thm_blue", "#89b4fa")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	iDir := envOrMap("PICKER_ICON_DIR", tmuxOpts, "@icon_dir", iconDir)
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)

	cMauve := ansiFg(thmMauve)
	cBlue := ansiFg(thmBlue)
	cDim := ansiFg(thmSubtext0)
	rc := newResourceColors(tmuxOpts)
	reset := "\033[0m"
	dim := "\033[2m"

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].activity != sessions[j].activity {
			return sessions[i].activity > sessions[j].activity
		}
		return sessions[i].name < sessions[j].name
	})

	type row struct {
		sess   *sessionData
		icons  string
		iconDW int
	}
	rows := make([]row, len(sessions))
	maxName, maxIconDW := 0, 0
	for i := range sessions {
		s := &sessions[i]
		if len(s.name) > maxName {
			maxName = len(s.name)
		}
		icons, dw := buildProcIcons(s.procs, maxIconsPicker)
		icons, dw = appendClaudeIcon(icons, dw, s.claude, theme, dim, reset)
		rows[i] = row{sess: s, icons: icons, iconDW: dw}
		if dw > maxIconDW {
			maxIconDW = dw
		}
	}
	iconCol := max(maxIconDW+1, 5)
	for i := range rows {
		rows[i].icons = padToWidth(rows[i].icons, rows[i].iconDW, iconCol)
	}
	emptyIcons := strings.Repeat(" ", iconCol)

	mergeResources(sessions, <-resCh)

	// Pre-compute CPU and MEM strings separately so the "/" aligns
	cpuStrs := make([]string, len(rows))
	memStrs := make([]string, len(rows))
	maxCPU, maxMem := cpuColWidth(), 0
	for i, r := range rows {
		cpuStrs[i] = formatCPU(r.sess.cpuPct)
		memStrs[i] = formatMem(r.sess.memMB)
		if len(cpuStrs[i]) > maxCPU {
			maxCPU = len(cpuStrs[i])
		}
		if len(memStrs[i]) > maxMem {
			maxMem = len(memStrs[i])
		}
	}

	// Build header row
	hdrCPU := "CPU"
	hdrMem := "Mem"
	hdrCPUPad := strings.Repeat(" ", max(0, maxCPU-len(hdrCPU)))
	hdrMemPad := strings.Repeat(" ", max(0, maxMem-len(hdrMem)))
	hdrRes := hdrCPUPad + hdrCPU + " / " + hdrMem + hdrMemPad
	hdrDisplay := fmt.Sprintf("%s %s%s  %s  %s  %s %s",
		cDim+iSess+reset,
		cDim+"Session"+reset,
		strings.Repeat(" ", max(0, maxName-7)),
		cDim+padToWidth("Procs", 5, iconCol)+reset,
		cDim+hdrRes+reset,
		cDim+iDir+reset,
		cDim+"Path"+reset,
	)
	hdrPlain := fmt.Sprintf("%s %s%s  %s  %s  %s %s",
		iSess, "Session", strings.Repeat(" ", max(0, maxName-7)),
		padToWidth("Procs", 5, iconCol), hdrRes, iDir, "Path",
	)

	home := os.Getenv("HOME")
	items := make([]listItem, 0, len(rows)+1)
	items = append(items, listItem{
		display: hdrDisplay,
		plain:   hdrPlain,
	})
	for i, r := range rows {
		pad := strings.Repeat(" ", max(0, maxName-len(r.sess.name)))
		icons := r.icons
		if icons == "" {
			icons = emptyIcons
		}
		shortPath := r.sess.path
		if home != "" && strings.HasPrefix(shortPath, home) {
			shortPath = "~" + shortPath[len(home):]
		}
		cpuPad := strings.Repeat(" ", max(0, maxCPU-len(cpuStrs[i])))
		memPad := strings.Repeat(" ", max(0, maxMem-len(memStrs[i])))
		display := fmt.Sprintf("%s %s%s  %s  %s%s %s %s%s  %s %s",
			cMauve+iSess+reset,
			cMauve+r.sess.name+reset,
			pad,
			icons,
			cpuPad,
			rc.cpuColor(r.sess.cpuPct)+cpuStrs[i]+reset,
			cDim+"/"+reset,
			rc.memColor(r.sess.memMB)+memStrs[i]+reset,
			memPad,
			cBlue+iDir+reset,
			cDim+shortPath+reset,
		)
		resPlain := cpuPad + cpuStrs[i] + " / " + memStrs[i] + memPad
		plain := fmt.Sprintf("%s %s%s  %s  %s  %s %s",
			iSess, r.sess.name, pad, stripANSI(icons), resPlain, iDir, shortPath,
		)
		items = append(items, listItem{
			target:          r.sess.name,
			display:         display,
			plain:           plain,
			searchText:      r.sess.name,
			session:         r.sess.name,
			hasActiveClaude: isActiveState(claudePriority(r.sess.claude)),
			isScratch:       strings.HasPrefix(r.sess.name, "scratch-"),
		})
	}
	return items
}

func buildWindowItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string) []listItem {
	windows := collectWindows()
	claudeByWin := aggregateClaudeByWindow(claudePanes)
	mergeClaudeWindows(windows, claudeByWin)

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmGreen := envOrMap("THM_GREEN", tmuxOpts, "@thm_green", "#a6e3a1")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	thmOverlay1 := envOrMap("THM_OVERLAY_1", tmuxOpts, "@thm_overlay_1", "#7f849c")
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)

	cMauve := ansiFg(thmMauve)
	cGreen := ansiFg(thmGreen)
	cDim := ansiFg(thmSubtext0)
	cFaint := ansiFg(thmOverlay1)
	reset := "\033[0m"
	dim := "\033[2m"

	type sessGroup struct {
		name     string
		activity int64
		windows  []*windowData
	}
	groupMap := make(map[string]*sessGroup)
	for i := range windows {
		w := &windows[i]
		g, ok := groupMap[w.session]
		if !ok {
			g = &sessGroup{name: w.session}
			groupMap[w.session] = g
		}
		g.windows = append(g.windows, w)
	}

	sessActivity := collectSessionActivity()
	groups := make([]*sessGroup, 0, len(groupMap))
	for _, g := range groupMap {
		g.activity = sessActivity[g.name]
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].activity != groups[j].activity {
			return groups[i].activity > groups[j].activity
		}
		return groups[i].name < groups[j].name
	})
	for _, g := range groups {
		sort.Slice(g.windows, func(i, j int) bool {
			return g.windows[i].index < g.windows[j].index
		})
	}

	type renderedWin struct {
		win    *windowData
		icons  string
		iconDW int
	}
	winRows := make(map[string][]renderedWin)
	maxIconDW := 0
	for _, g := range groups {
		for _, w := range g.windows {
			icons, dw := buildProcIcons(w.procs, maxIconsPicker)
			icons, dw = appendClaudeIcon(icons, dw, w.claude, theme, dim, reset)
			winRows[g.name] = append(winRows[g.name], renderedWin{win: w, icons: icons, iconDW: dw})
			if dw > maxIconDW {
				maxIconDW = dw
			}
		}
	}
	iconCol := max(maxIconDW+1, 3)
	for _, rows := range winRows {
		for i := range rows {
			rows[i].icons = padToWidth(rows[i].icons, rows[i].iconDW, iconCol)
		}
	}
	emptyIcons := strings.Repeat(" ", iconCol)

	var items []listItem
	for _, g := range groups {
		rows := winRows[g.name]

		sessHasClaude := false
		for _, r := range rows {
			key := fmt.Sprintf("%s:%d", r.win.session, r.win.index)
			if cc, ok := claudeByWin[key]; ok && isActiveState(claudePriority(*cc)) {
				sessHasClaude = true
				break
			}
		}

		headerDisplay := fmt.Sprintf("%s %s",
			cMauve+iSess+reset,
			cMauve+g.name+reset,
		)
		items = append(items, listItem{
			target:          g.name,
			display:         headerDisplay,
			plain:           fmt.Sprintf("%s %s", iSess, g.name),
			searchText:      g.name,
			isHeader:        true,
			session:         g.name,
			hasActiveClaude: sessHasClaude,
		})

		multiWin := len(rows) > 1
		for wi, r := range rows {
			w := r.win
			name := w.name
			if len(name) > 40 {
				name = name[:38] + "…"
			}
			winLabel := fmt.Sprintf("%d: %s", w.index, name)
			if w.zoomed {
				winLabel += " 󰁌"
			}

			activeMarker := " "
			if w.active && multiWin {
				activeMarker = cGreen + "▸" + reset
			}

			icons := r.icons
			if icons == "" {
				icons = emptyIcons
			}

			tree := "├─"
			if wi == len(rows)-1 {
				tree = "╰─"
			}

			var branchStr string
			if w.branch != "" && w.branch != w.name && w.branch != "main" && w.branch != "master" {
				br := w.branch
				if len(br) > 35 {
					br = br[:33] + "…"
				}
				branchStr = iconBranch + " " + br
			}

			display := fmt.Sprintf("%s %s %s %s",
				cDim+tree+reset,
				activeMarker,
				winLabel,
				icons,
			)
			if branchStr != "" {
				display += "  " + cFaint + branchStr + reset
			}

			plain := fmt.Sprintf("%s %s %s %s",
				tree,
				strings.TrimSpace(stripANSI(activeMarker)),
				winLabel,
				stripANSI(icons),
			)
			if branchStr != "" {
				plain += "  " + branchStr
			}

			search := g.name + " " + name
			if branchStr != "" {
				search += " " + branchStr
			}
			items = append(items, listItem{
				target:          fmt.Sprintf("%s:%d", g.name, w.index),
				display:         display,
				plain:           plain,
				searchText:      search,
				session:         g.name,
				hasActiveClaude: isActiveState(claudePriority(w.claude)),
			})
		}
	}
	return items
}

// --- Utilities ---

// isActiveState reports whether a claude priority state is worth highlighting.
func isActiveState(state string) bool {
	return state != "" && state != "idle"
}

// structHash hashes only ASCII characters so spinner/icon changes don't trigger structural reloads.
func structHash(items []listItem) string {
	var sb strings.Builder
	for _, item := range items {
		sb.WriteString(item.plain)
		sb.WriteByte('\n')
	}
	return fmt.Sprintf("%x", md5.Sum([]byte(sb.String())))
}

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip 'm'
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// applyPreviewXOffset shifts preview content horizontally by previewXOffset visible cells,
// then truncates each line to the preview width to prevent wrapping changes.
func (m *tuiModel) applyPreviewXOffset() {
	if m.previewRaw == "" {
		return
	}
	if m.previewXOffset == 0 {
		m.preview.SetContent(m.previewRaw)
		return
	}
	pw := m.previewWidth()
	lines := strings.Split(m.previewRaw, "\n")
	shifted := make([]string, len(lines))
	for i, line := range lines {
		shifted[i] = truncateVisibleWidth(shiftLineLeft(line, m.previewXOffset), pw)
	}
	m.preview.SetContent(strings.Join(shifted, "\n"))
}

// shiftLineLeft drops the first n visible cells from a line, preserving ANSI escapes.
func shiftLineLeft(line string, n int) string {
	runes := []rune(line)
	var out strings.Builder
	skipped := 0
	i := 0
	// First pass: skip n visible cells, keeping ANSI escapes (preserves color state)
	for i < len(runes) && skipped < n {
		if runes[i] == '\033' && i+1 < len(runes) && runes[i+1] == '[' {
			// ANSI escape — emit it but don't count as visible
			j := i + 2
			for j < len(runes) && runes[j] != 'm' {
				j++
			}
			if j < len(runes) {
				j++ // skip 'm'
			}
			for _, r := range runes[i:j] {
				out.WriteRune(r)
			}
			i = j
		} else {
			skipped += runeCellWidth(runes[i])
			i++
		}
	}
	// Remainder
	for _, r := range runes[i:] {
		out.WriteRune(r)
	}
	return out.String()
}

// truncateVisibleWidth truncates a line to maxCells visible cells, preserving ANSI escapes.
func truncateVisibleWidth(line string, maxCells int) string {
	runes := []rune(line)
	var out strings.Builder
	cells := 0
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\033' && i+1 < len(runes) && runes[i+1] == '[' {
			// ANSI escape — always emit, zero visible width
			j := i + 2
			for j < len(runes) && runes[j] != 'm' {
				j++
			}
			if j < len(runes) {
				j++
			}
			for _, r := range runes[i:j] {
				out.WriteRune(r)
			}
			i = j - 1
			continue
		}
		w := runeCellWidth(runes[i])
		if cells+w > maxCells {
			break
		}
		out.WriteRune(runes[i])
		cells += w
	}
	out.WriteString("\033[0m")
	return out.String()
}

// fitVisibleWidth truncates or pads a line to exactly targetCells visible cells,
// using runeCellWidth which handles nerd font PUA correctly (go-runewidth reports 0).
func fitVisibleWidth(line string, targetCells int) string {
	truncated := truncateVisibleWidth(line, targetCells)
	cells := visibleWidth(truncated)
	if cells < targetCells {
		return truncated + strings.Repeat(" ", targetCells-cells)
	}
	return truncated
}

// visibleWidth returns the display width of a string, skipping ANSI escapes.
func visibleWidth(s string) int {
	runes := []rune(s)
	cells := 0
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\033' && i+1 < len(runes) && runes[i+1] == '[' {
			j := i + 2
			for j < len(runes) && runes[j] != 'm' {
				j++
			}
			if j < len(runes) {
				j++
			}
			i = j - 1
			continue
		}
		cells += runeCellWidth(runes[i])
	}
	return cells
}

// ---------------------------------------------------------------------------
// Fuzzy scoring (fzf-style)
//
// Two-pass alignment: forward scan finds the end of the first valid match,
// backward scan from that end finds a tighter start. Scoring uses fzf's
// constants: boundary/consecutive bonuses, gap penalties, first-char multiplier.
// ---------------------------------------------------------------------------

// Scoring constants (matching fzf).
const (
	fzfScoreMatch        = 16
	fzfScoreGapStart     = -3
	fzfScoreGapExtension = -1

	fzfBonusConsecutive        = 4  // -(scoreGapStart + scoreGapExtension)
	fzfBonusBoundary           = 8  // scoreMatch / 2
	fzfBonusBoundaryWhite      = 10 // boundary + 2
	fzfBonusBoundaryDelimiter  = 9  // boundary + 1
	fzfBonusNonWord            = 8
	fzfBonusCamelCase          = 7  // boundary + scoreGapExtension
	fzfBonusFirstCharMultiplier = 2
)

type charClass int8

const (
	charWhite charClass = iota
	charDelimiter
	charNonWord
	charLower
	charUpper
	charNumber
)

func classOf(c byte) charClass {
	switch {
	case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		return charWhite
	case c == '/' || c == ',' || c == ';' || c == ':':
		return charDelimiter
	case c >= 'a' && c <= 'z':
		return charLower
	case c >= 'A' && c <= 'Z':
		return charUpper
	case c >= '0' && c <= '9':
		return charNumber
	default:
		return charNonWord // covers '-', '_', '.', etc.
	}
}

func charBonus(prev, curr charClass) int {
	if curr >= charLower { // letter or digit
		switch prev {
		case charWhite:
			return fzfBonusBoundaryWhite
		case charDelimiter:
			return fzfBonusBoundaryDelimiter
		case charNonWord:
			return fzfBonusBoundary
		}
	}
	if prev == charLower && curr == charUpper {
		return fzfBonusCamelCase
	}
	if prev != charNumber && curr == charNumber {
		return fzfBonusCamelCase
	}
	if curr <= charNonWord {
		return fzfBonusNonWord
	}
	return 0
}

// fuzzyScore returns a relevance score (>=0) if all characters in pattern
// appear in text in order, or -1 if there is no match.
func fuzzyScore(text, pattern string) int {
	if len(pattern) == 0 {
		return 0
	}

	// Forward pass: verify match exists, find end position.
	pi := 0
	endIdx := 0
	for i := 0; i < len(text) && pi < len(pattern); i++ {
		if text[i] == pattern[pi] {
			endIdx = i
			pi++
		}
	}
	if pi < len(pattern) {
		return -1
	}

	// Backward pass: from endIdx, find a tighter alignment.
	pos := make([]int, len(pattern))
	pi = len(pattern) - 1
	for i := endIdx; i >= 0 && pi >= 0; i-- {
		if text[i] == pattern[pi] {
			pos[pi] = i
			pi--
		}
	}

	// Score the alignment.
	score := 0
	prevBonus := 0
	consecutive := 0

	for k, idx := range pos {
		var prev charClass
		if idx == 0 {
			prev = charWhite // start of string acts as whitespace boundary
		} else {
			prev = classOf(text[idx-1])
		}
		b := charBonus(prev, classOf(text[idx]))

		score += fzfScoreMatch

		// Consecutive bonus propagation: carry forward the better of the
		// ongoing-run bonus and the fixed consecutive bonus.
		if k > 0 && pos[k]-pos[k-1] == 1 {
			consecutive++
			cb := max(fzfBonusConsecutive, prevBonus)
			if b >= fzfBonusBoundary {
				cb = max(cb, b)
			}
			b = cb
		} else if k > 0 {
			gap := pos[k] - pos[k-1] - 1
			score += fzfScoreGapStart + fzfScoreGapExtension*(gap-1)
			consecutive = 0
		}

		if k == 0 {
			score += b * fzfBonusFirstCharMultiplier
		} else {
			score += b
		}
		prevBonus = b
	}

	return score
}
