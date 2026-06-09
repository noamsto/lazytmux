package main

import (
	"fmt"
	imgcolor "image/color"
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
	previewID   = 250 // kitty image id for the big preview
	stripThumbW = 12  // filmstrip thumbnail width in cells
	stripGutter = 1   // blank columns between filmstrip thumbnails
)

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// layout is the carousel geometry for a given pane size.
type layout struct {
	previewW, previewH int
	stripW, stripH     int
	stripCols          int // visible filmstrip thumbnails
}

// computeLayout splits the pane into a big preview on top and a filmstrip row
// above a one-line status bar.
func computeLayout(paneW, paneH int) layout {
	stripH := clamp(paneH/6, 3, 8)
	stripW := stripThumbW
	stripCols := clamp((paneW+stripGutter)/(stripW+stripGutter), 1, maxCellDim)
	previewW := clamp(paneW, 1, maxCellDim)
	// rows consumed: status(1) + filmstrip(stripH) + marker(1) + gap(1).
	previewH := clamp(paneH-stripH-3, 1, maxCellDim)
	return layout{previewW: previewW, previewH: previewH, stripW: stripW, stripH: stripH, stripCols: stripCols}
}

// stripStart is the first filmstrip index for a window centered on cursor.
func stripStart(cursor, stripCols, n int) int {
	if n <= stripCols {
		return 0
	}
	return clamp(cursor-stripCols/2, 0, n-stripCols)
}

// ---------------------------------------------------------------------------
// Gallery bubbletea model — carousel (big preview + filmstrip)
// ---------------------------------------------------------------------------

type galleryModel struct {
	pane    string
	images  []imageEntry
	backend gridBackend
	theme   string
	l       layout
	cursor  int      // selected image index
	width   int
	height  int
	tty     *os.File // raw graphics sink (bypasses bubbletea's stdout)
	mtime   int64    // manifest mtime at last load (for auto-refresh)
	ready   bool
}

func (m galleryModel) Init() tea.Cmd { return galleryTickCmd() }

type galleryTickMsg struct{}

// galleryTickCmd polls the manifest so the carousel auto-refreshes while the
// plugin hook appends new images.
func galleryTickCmd() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return galleryTickMsg{} })
}

// transmitView stores the preview + visible filmstrip images store-only (kitty
// backend). Writes to /dev/tty so the APC bytes never interleave with
// bubbletea's frame output.
func (m *galleryModel) transmitView() {
	if m.backend != backendKitty || m.tty == nil || len(m.images) == 0 {
		return
	}
	fmt.Fprint(m.tty, deleteAll())
	fmt.Fprint(m.tty, transmitVirtual(previewID, m.images[m.cursor].Path, m.l.previewW, m.l.previewH))
	start := stripStart(m.cursor, m.l.stripCols, len(m.images))
	for s := 0; s < m.l.stripCols; s++ {
		idx := start + s
		if idx >= len(m.images) {
			break
		}
		fmt.Fprint(m.tty, transmitVirtual(s+1, m.images[idx].Path, m.l.stripW, m.l.stripH))
	}
}

// selectIndex moves the selection (clamped) and re-transmits.
func (m *galleryModel) selectIndex(idx int) {
	idx = clamp(idx, 0, max(0, len(m.images)-1))
	if idx != m.cursor {
		m.cursor = idx
		m.transmitView()
	}
}

func (m *galleryModel) reload() {
	m.mtime = manifestMtime(m.pane)
	m.images = loadManifest(m.pane)
	m.cursor = clamp(m.cursor, 0, max(0, len(m.images)-1))
	if m.ready {
		m.l = computeLayout(m.width, m.height)
		m.transmitView()
	}
}

func (m galleryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.l = computeLayout(m.width, m.height)
		m.ready = true
		m.transmitView()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "right", "l", "down", "j":
			m.selectIndex(m.cursor + 1)
		case "left", "h", "up", "k":
			m.selectIndex(m.cursor - 1)
		case "n":
			m.selectIndex(m.cursor + m.l.stripCols)
		case "p":
			m.selectIndex(m.cursor - m.l.stripCols)
		case "g", "home":
			m.selectIndex(0)
		case "G", "end":
			m.selectIndex(len(m.images) - 1)
		case "o", "enter":
			m.openSelected("")
		case "O":
			m.openSelected("dir")
		case "r":
			m.reload()
		default:
			if d := digitKey(msg.String()); d >= 1 && d-1 < len(m.images) {
				m.selectIndex(d - 1)
			}
		}
	case galleryTickMsg:
		if mt := manifestMtime(m.pane); mt != m.mtime {
			m.reload()
		}
		return m, galleryTickCmd()
	}
	return m, nil
}

// openSelected launches the default app for the selected image (mode "") or its
// containing folder (mode "dir"), detached so it doesn't block the TUI.
func (m galleryModel) openSelected(mode string) {
	if len(m.images) == 0 {
		return
	}
	target := m.images[m.cursor].Path
	if mode == "dir" {
		target = filepath.Dir(target)
	}
	_ = exec.Command("xdg-open", target).Start()
}

func (m galleryModel) View() tea.View {
	content := "Loading..."
	if m.ready {
		content = m.renderView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m galleryModel) renderView() string {
	if len(m.images) == 0 {
		return "no images yet"
	}
	center := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center)

	// Big preview of the selected image.
	var preview string
	if m.backend == backendKitty {
		preview = placeholderBlock(previewID, m.l.previewW, m.l.previewH)
	} else {
		preview = symbolsBlock(m.images[m.cursor].Path, m.l.previewW, m.l.previewH)
	}

	// Filmstrip window + a marker under the selected thumbnail.
	start := stripStart(m.cursor, m.l.stripCols, len(m.images))
	hgap := lipgloss.NewStyle().Width(stripGutter).Height(m.l.stripH).Render("")
	var cells []string
	for s := 0; s < m.l.stripCols; s++ {
		idx := start + s
		if idx >= len(m.images) {
			break
		}
		if s > 0 {
			cells = append(cells, hgap)
		}
		if m.backend == backendKitty {
			cells = append(cells, placeholderBlock(s+1, m.l.stripW, m.l.stripH))
		} else {
			cells = append(cells, symbolsBlock(m.images[idx].Path, m.l.stripW, m.l.stripH))
		}
	}
	strip := lipgloss.JoinHorizontal(lipgloss.Top, cells...)
	selSlot := m.cursor - start
	markerColor := m.thmColor("@thm_mauve", "#cba6f7", "#8839ef")
	marker := strings.Repeat(" ", selSlot*(m.l.stripW+stripGutter)) +
		lipgloss.NewStyle().Foreground(markerColor).Render(strings.Repeat("▔", m.l.stripW))

	status := fmt.Sprintf("[%d/%d] %s · ↵/o open · O folder · h/l move · q quit",
		m.cursor+1, len(m.images), filepath.Base(m.images[m.cursor].Path))

	return lipgloss.JoinVertical(lipgloss.Left,
		center.Render(preview),
		"",
		strip,
		marker,
		status,
	)
}

// thmColor reads a tmux @thm_* color option, falling back per theme.
func (m galleryModel) thmColor(opt, dark, light string) imgcolor.Color {
	out, err := exec.Command("tmux", "show", "-gv", opt).Output()
	if err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return lipgloss.Color(v)
		}
	}
	if m.theme == "light" {
		return lipgloss.Color(light)
	}
	return lipgloss.Color(dark)
}

// runGallery is the entry point dispatched from main for `--gallery <pane>`.
func runGallery(pane string) error {
	tty, _ := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	m := galleryModel{
		pane:    pane,
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

// manifestMtime returns the manifest file's mtime in ns, or 0 if absent.
func manifestMtime(pane string) int64 {
	fi, err := os.Stat(manifestPath(pane))
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

// digitKey maps "1".."9" to 1..9, else 0.
func digitKey(s string) int {
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return int(s[0] - '0')
	}
	return 0
}
