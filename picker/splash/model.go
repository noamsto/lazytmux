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
		_ = msg
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

// colorizeArt applies the moving gradient to each non-space glyph.
func (m model) colorizeArt(a artGrid) string {
	var sb strings.Builder
	for y, line := range a.lines {
		x := 0
		for _, r := range line {
			if r == ' ' {
				sb.WriteRune(' ')
				x++
				continue
			}
			hex := m.gradient[paletteIndex(x, y, m.frame, len(m.gradient))]
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render(string(r)))
			x++
		}
		if y < len(a.lines)-1 {
			sb.WriteByte('\n')
		}
	}
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
		blocks = append(blocks, m.colorizeArt(art))
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
