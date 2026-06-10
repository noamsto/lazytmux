package main

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type tip struct {
	Key   string
	Label string
}

type frameMsg struct{}
type timeoutMsg struct{}

type model struct {
	theme         string
	tips          []tip
	prefix        string
	timeoutSec    int
	gradient      []string
	frame         int
	width, height int
	full, small   artGrid
}

func newModel(theme string, tips []tip, prefix string, timeoutSec int) model {
	anchors, ok := gradientAnchors[theme]
	if !ok {
		anchors = gradientAnchors["dark"]
	}
	return model{
		theme:      theme,
		tips:       tips,
		prefix:     prefix,
		timeoutSec: timeoutSec,
		gradient:   buildGradient(anchors, 48),
		full:       parseArt(catFull),
		small:      parseArt(catSmall),
	}
}

func frameCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return frameMsg{} })
}

func timeoutCmd(sec int) tea.Cmd {
	return tea.Tick(time.Duration(sec)*time.Second, func(time.Time) tea.Msg { return timeoutMsg{} })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(frameCmd(), timeoutCmd(m.timeoutSec))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m, tea.Quit
	case timeoutMsg:
		return m, tea.Quit
	case frameMsg:
		m.frame++
		return m, frameCmd()
	}
	return m, nil
}

func (m model) accent() string {
	if m.theme == "light" {
		return "#7287fd"
	}
	return "#b4befe"
}

func (m model) subtext() string {
	if m.theme == "light" {
		return "#6c6f85"
	}
	return "#a6adc8"
}

// introFrames is the dissolve-in length (~1s at 50ms/frame): each glyph starts
// as braille static and resolves into the real art at a hash-staggered frame.
const introFrames = 20

// brailleStatic returns a random-ish braille pattern (U+2800 + 8-bit dot mask)
// for the dissolve-in, so the "assembling" noise matches the braille art.
func brailleStatic(x, y, frame int) rune {
	return rune(0x2800 + int(cellHash(x, y, frame)*256))
}

// colorizeArt renders the mascot: a dissolve-in intro (braille static settling
// into the art), then a plasma field driving both gradient color and brightness
// per glyph (organic shimmer rather than a linear color stripe).
func (m model) colorizeArt(a artGrid) string {
	t := float64(m.frame) * 0.11
	var sb strings.Builder
	for y, line := range a.lines {
		x := 0
		for _, r := range line {
			if r == ' ' {
				sb.WriteRune(' ')
				x++
				continue
			}
			v := plasma(x, y, t)
			glyph := r
			if revealAt := cellHash(x, y, 0) * introFrames; float64(m.frame) < revealAt {
				glyph = brailleStatic(x, y, m.frame)
				v *= 0.6
			}
			hex := shade(m.gradient[int(v*float64(len(m.gradient)-1))], v)
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(string(glyph)))
			x++
		}
		if y < len(a.lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// Sleep "z" overlay: a few marks drift up-and-right from above the cat's head
// and fade, staggered with a pause between cycles so they read as sporadic.
const (
	zSlots    = 3
	zHeadroom = 4   // blank rows reserved above the cat for the z's to rise into
	zPeriod   = 78  // frames for one mark's full rise (~3.9s at 50ms)
	zStagger  = 26  // frame offset between successive marks
	zActive   = 0.8 // fraction of the period a mark is visible (rest = pause)
)

var zGlyphs = []rune{'z', 'Z', 'z'}

// renderMascot stacks the animated z headroom above the dissolving/shimmering
// cat, as one block so outer centering keeps the marks aligned to the art.
func (m model) renderMascot(a artGrid) string {
	originX := a.w * 30 / 100
	grid := make([][]rune, zHeadroom)
	bright := make([][]float64, zHeadroom)
	for r := range grid {
		grid[r] = make([]rune, a.w)
		bright[r] = make([]float64, a.w)
		for c := range grid[r] {
			grid[r][c] = ' '
		}
	}
	for i := 0; i < zSlots; i++ {
		ph := float64((m.frame+i*zStagger)%zPeriod) / zPeriod
		if ph >= zActive {
			continue // pause between marks
		}
		p := ph / zActive // 0 (just above head) → 1 (top, faded out)
		row := zHeadroom - 1 - int(p*float64(zHeadroom))
		col := originX + int(p*5)
		if row < 0 || col < 0 || col >= a.w {
			continue
		}
		grid[row][col] = zGlyphs[i]
		bright[row][col] = 1 - p
	}

	var sb strings.Builder
	for r := 0; r < zHeadroom; r++ {
		for c := 0; c < a.w; c++ {
			if grid[r][c] == ' ' {
				sb.WriteRune(' ')
				continue
			}
			hex := shade(m.accent(), 0.35+0.65*bright[r][c])
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(string(grid[r][c])))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(m.colorizeArt(a))
	return sb.String()
}

func (m model) renderTips() string {
	if len(m.tips) == 0 {
		return ""
	}
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.accent())).Bold(true)
	lblStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.subtext()))

	keyOf := func(t tip) string { return strings.ReplaceAll(t.Key, "prefix", m.prefix) }
	keyW := 0
	for _, t := range m.tips {
		if n := lipgloss.Width(keyOf(t)); n > keyW {
			keyW = n
		}
	}

	half := (len(m.tips) + 1) / 2
	var rows []string
	for i := 0; i < half; i++ {
		left := m.tipCell(m.tips[i], keyOf, keyStyle, lblStyle, keyW)
		right := ""
		if j := i + half; j < len(m.tips) {
			right = m.tipCell(m.tips[j], keyOf, keyStyle, lblStyle, keyW)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m model) tipCell(t tip, keyOf func(tip) string, keyStyle, lblStyle lipgloss.Style, keyW int) string {
	key := keyStyle.Width(keyW).Render(keyOf(t))
	return lipgloss.JoinHorizontal(lipgloss.Top, key, "  ", lblStyle.Render(t.Label))
}

func (m model) render() string {
	var blocks []string
	if art, show := pickArt(m.full, m.small, m.width, m.height); show {
		blocks = append(blocks, m.renderMascot(art))
	}
	wordmark := lipgloss.NewStyle().Foreground(lipgloss.Color(m.accent())).Bold(true).Render("l a z y t m u x")
	blocks = append(blocks, wordmark, "")
	if tips := m.renderTips(); tips != "" {
		blocks = append(blocks, tips, "")
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.subtext())).Italic(true).Render("press any key to dismiss")
	blocks = append(blocks, hint)
	return lipgloss.JoinVertical(lipgloss.Center, blocks...)
}

func (m model) View() tea.View {
	w, h := m.width, m.height
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	content := lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, m.render())
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
