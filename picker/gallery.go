package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	targetCellWidth = 28 // desired thumbnail width in cells
	targetCellRows  = 4  // desired rows per screenful before paging
	labelRows       = 1  // one label line per cell
	hGutter         = 2  // blank columns between cells
	vGutter         = 1  // blank rows between cells
)

// grid is the computed layout for a given pane size and image count.
type grid struct {
	cols, rows   int // visible columns / rows of cells
	cellW, cellH int // cell box in cells (cellH includes the label row)
	imgH         int // image rows inside a cell (cellH - labelRows)
	perPage      int
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// computeGrid derives the grid layout, reserving the bottom status row and
// hGutter/vGutter spacing between cells.
func computeGrid(paneW, paneH, imageCount int) grid {
	cols := clamp((paneW+hGutter)/(targetCellWidth+hGutter), 1, maxCellDim)
	if imageCount > 0 {
		cols = clamp(cols, 1, imageCount) // don't reserve more columns than images
	}
	cellW := clamp((paneW-(cols-1)*hGutter)/cols, 1, maxCellDim)

	body := paneH - 1
	rows := clamp(targetCellRows, 1, maxRowsThatFit(body))
	cellH := clamp((body-(rows-1)*vGutter)/rows, labelRows+1, maxCellDim+labelRows)
	imgH := clamp(cellH-labelRows, 1, maxCellDim)
	return grid{cols: cols, rows: rows, cellW: cellW, cellH: cellH, imgH: imgH, perPage: cols * rows}
}

// maxRowsThatFit caps rows so each cell keeps a label row + image rows + a
// gutter, fitting the body height.
func maxRowsThatFit(body int) int {
	per := labelRows + 1 + vGutter
	if body < per {
		return 1
	}
	return body / per
}

func pageOf(index, perPage int) int { return index / perPage }

func pageCount(n, perPage int) int {
	if n <= 0 {
		return 1
	}
	return (n + perPage - 1) / perPage
}

// moveCursor shifts the selected index by delta, clamped to [0, count-1].
func moveCursor(index, delta, count int) int {
	if count == 0 {
		return 0
	}
	return clamp(index+delta, 0, count-1)
}

// ---------------------------------------------------------------------------
// Gallery bubbletea model (Tasks 9–12)
// ---------------------------------------------------------------------------

type galleryModel struct {
	pane    string
	viewer  string // absolute path to the v1 navigator (drill-in); falls back to PATH
	images  []imageEntry
	backend gridBackend
	theme   string
	g       grid
	cursor  int      // selected image index (absolute)
	width   int
	height  int
	tty     *os.File // raw graphics sink (bypasses bubbletea's stdout)
	mtime   int64    // manifest mtime at last load (for auto-refresh)
	ready   bool
}

func (m galleryModel) Init() tea.Cmd { return galleryTickCmd() }

type galleryTickMsg struct{}

// galleryTickCmd polls the manifest for changes so the grid auto-refreshes
// while the plugin hook appends new images.
func galleryTickCmd() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return galleryTickMsg{} })
}

// transmitPage stores the current page's images store-only (kitty backend),
// ids 1..perPage, sized to the cell box. Writes to /dev/tty so the APC bytes
// never interleave with bubbletea's frame output.
func (m *galleryModel) transmitPage() {
	if m.backend != backendKitty || m.tty == nil {
		return
	}
	fmt.Fprint(m.tty, deleteAll())
	page := pageOf(m.cursor, m.g.perPage)
	start := page * m.g.perPage
	for slot := 0; slot < m.g.perPage; slot++ {
		idx := start + slot
		if idx >= len(m.images) {
			break
		}
		fmt.Fprint(m.tty, transmitVirtual(slot+1, m.images[idx].Path, m.g.cellW, m.g.imgH))
	}
}

func (m galleryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.g = computeGrid(m.width, m.height, len(m.images))
		m.ready = true
		m.transmitPage()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "right", "l":
			m.cursor = m.moveAndMaybeTransmit(1)
		case "left", "h":
			m.cursor = m.moveAndMaybeTransmit(-1)
		case "down", "j":
			m.cursor = m.moveAndMaybeTransmit(m.g.cols)
		case "up", "k":
			m.cursor = m.moveAndMaybeTransmit(-m.g.cols)
		case "n":
			m.cursor = m.pageJump(1)
		case "p":
			m.cursor = m.pageJump(-1)
		case "r":
			m.mtime = manifestMtime(m.pane)
			m.images = loadManifest(m.pane)
			m.cursor = clamp(m.cursor, 0, max(0, len(m.images)-1))
			m.g = computeGrid(m.width, m.height, len(m.images))
			m.transmitPage()
		case "enter":
			return m, m.drillIn()
		default:
			if n := digitKey(msg.String()); n >= 1 {
				page := pageOf(m.cursor, m.g.perPage)
				idx := page*m.g.perPage + (n - 1)
				if idx < len(m.images) {
					m.cursor = idx
				}
			}
		}
	case retransmitMsg:
		m.transmitPage()
		return m, nil
	case galleryTickMsg:
		if mt := manifestMtime(m.pane); mt != m.mtime {
			m.mtime = mt
			m.images = loadManifest(m.pane)
			m.cursor = clamp(m.cursor, 0, max(0, len(m.images)-1))
			if m.ready {
				m.g = computeGrid(m.width, m.height, len(m.images))
				m.transmitPage()
			}
		}
		return m, galleryTickCmd()
	}
	return m, nil
}

// manifestMtime returns the manifest file's mtime in ns, or 0 if absent.
func manifestMtime(pane string) int64 {
	fi, err := os.Stat(manifestPath(pane))
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

func (m galleryModel) View() tea.View {
	content := "Loading..."
	if m.ready {
		content = m.renderGrid()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m galleryModel) renderGrid() string {
	if len(m.images) == 0 {
		return "no images"
	}
	page := pageOf(m.cursor, m.g.perPage)
	start := page * m.g.perPage
	labelStyle := lipgloss.NewStyle().Width(m.g.cellW)
	selStyle := labelStyle.Reverse(true)
	hgap := lipgloss.NewStyle().Width(hGutter).Height(m.g.cellH).Render("")

	var rows []string
	for r := 0; r < m.g.rows; r++ {
		var cols []string
		for c := 0; c < m.g.cols; c++ {
			if c > 0 {
				cols = append(cols, hgap) // gutter between columns
			}
			slot := r*m.g.cols + c
			idx := start + slot
			if idx >= len(m.images) {
				cols = append(cols, lipgloss.NewStyle().Width(m.g.cellW).Height(m.g.cellH).Render(""))
				continue
			}
			label := fmt.Sprintf("[%d] %s", idx+1, filepath.Base(m.images[idx].Path))
			if len(label) > m.g.cellW {
				label = label[:m.g.cellW]
			}
			if idx == m.cursor {
				label = selStyle.Render(label)
			} else {
				label = labelStyle.Render(label)
			}
			var thumb string
			if m.backend == backendKitty {
				thumb = placeholderBlock(slot+1, m.g.cellW, m.g.imgH)
			} else {
				thumb = symbolsBlock(m.images[idx].Path, m.g.cellW, m.g.imgH)
			}
			cols = append(cols, lipgloss.JoinVertical(lipgloss.Left, label, thumb))
		}
		if r > 0 {
			rows = append(rows, strings.Repeat("\n", vGutter-1)) // gutter between rows
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cols...))
	}
	status := fmt.Sprintf("page %d/%d · %d images · ↵ open · n/p page · q quit",
		page+1, pageCount(len(m.images), m.g.perPage), len(m.images))
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, rows...), status)
}

// runGallery is the entry point dispatched from main for `--gallery <pane>`.
// viewer is the absolute path to the v1 navigator for drill-in ("" → PATH lookup).
func runGallery(pane, viewer string) error {
	if viewer == "" {
		viewer = "tmux-claude-images"
	}
	tty, _ := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	m := galleryModel{
		pane:    pane,
		viewer:  viewer,
		images:  loadManifest(pane),
		backend: chooseGridBackend(termName()),
		theme:   detectTheme(),
		tty:     tty,
		mtime:   manifestMtime(pane),
	}
	// Teardown on pane-kill (toggle-off SIGTERM/SIGHUP), not just q.
	if tty != nil {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sig
			fmt.Fprint(tty, deleteAll())
			os.Exit(0)
		}()
	}
	_, err := tea.NewProgram(m).Run()
	if tty != nil {
		fmt.Fprint(tty, deleteAll())
		_ = tty.Close()
	}
	return err
}

// termName returns the outer client terminal name (for backend selection).
func termName() string {
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_termname}").Output()
	if err != nil {
		return os.Getenv("TERM")
	}
	return strings.TrimSpace(string(out))
}

// moveAndMaybeTransmit moves the cursor and, if the move crossed a page
// boundary, re-transmits the new page (kitty).
func (m *galleryModel) moveAndMaybeTransmit(delta int) int {
	old := pageOf(m.cursor, m.g.perPage)
	c := moveCursor(m.cursor, delta, len(m.images))
	if pageOf(c, m.g.perPage) != old {
		m.cursor = c
		m.transmitPage()
	}
	return c
}

func (m *galleryModel) pageJump(dir int) int {
	pages := pageCount(len(m.images), m.g.perPage)
	page := clamp(pageOf(m.cursor, m.g.perPage)+dir, 0, pages-1)
	c := clamp(page*m.g.perPage, 0, max(0, len(m.images)-1))
	if c != m.cursor {
		m.cursor = c
		m.transmitPage()
	}
	return c
}

// digitKey maps "1".."9" to 1..9, else 0.
func digitKey(s string) int {
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return 0
}

// drillIn hands the pane to the v1 navigator opened at the selected image.
func (m galleryModel) drillIn() tea.Cmd {
	if len(m.images) == 0 {
		return nil
	}
	idx := m.cursor
	cmd := exec.Command(m.viewer, "--view", m.pane, "--start", fmt.Sprint(idx))
	return tea.ExecProcess(cmd, func(error) tea.Msg { return retransmitMsg{} })
}

type retransmitMsg struct{}
