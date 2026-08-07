package main

import (
	"encoding/json"
	"errors"
	"fmt"
	imgcolor "image/color"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// listItem is one row in the picker list.
type listItem struct {
	target          string // tmux target; "" = unselectable (unless remoteHost set)
	display         string // ANSI-rendered display line
	plain           string // display stripped of ANSI (cached for width)
	searchText      string // filterable text (name, branch — no paths/icons)
	isHeader        bool   // session header row
	isZoxideHeader  bool   // the "── New session ──" divider
	isRemoteHeader  bool   // the "── Remote ──" divider
	session         string // owning session name (for kill)
	hasActiveClaude bool   // used for --claude filter
	isScratch       bool   // scratch-* session
	createPath      string // zoxide suggestion: dir to create a session at ("" = normal row)
	createName      string // zoxide suggestion: derived session name
	isRemoteRow     bool   // belongs to the Remote section (set even when unselectable)
	remoteHost      string // remote bridge row: ssh host for lztmux-remote-open
	remoteSess      string // remote bridge row: optional remote session name
	displayEnd      string // remote session row: display with the closing tree glyph
	plainEnd        string // remote session row: plain with the closing tree glyph
}

// pickerMode selects which renderer draws the body. One model, three
// renderers: modeList draws renderList (+ renderPreview), modeWall renderWall.
type pickerMode int8

const (
	modeList pickerMode = iota
	modeWall
)

// tuiModel is the bubbletea model for the picker.
type tuiModel struct {
	// Data
	allItems     []listItem // unfiltered: sessionItems + remoteItems + zoxideItems
	sessionItems []listItem // session/window rows (base for recombination)
	remoteItems  []listItem // remote host/session rows, loaded async after first paint
	zoxideItems  []listItem // zoxide suggestions, loaded async after first paint
	visible      []listItem // after query + mode filter
	cursor       int

	// Modes
	mode pickerMode
	// wallLaunched records that --wall opened this popup, which is what makes ^/
	// a toggle rather than a one-way trip out of the wall.
	wallLaunched bool
	windowMode   bool
	claudeOnly   bool
	scratchOnly  bool

	// Wall
	wallPage    int
	wallContent map[string]string // target -> last capture
	wallBad     map[string]bool   // targets capture-pane refused, skipped next batch
	// focused is set by tab and cleared by esc: while true, in-scope keystrokes
	// relay to the tile under the cursor instead of navigating the grid (#316).
	focused bool

	// Search
	query string
	// querying is the wall's modal filter prompt: letters navigate the grid, so
	// they only reach the query while it is open.
	querying bool

	// Transient error shown in the hint line (e.g. session-create failure)
	statusMsg string

	// Preview
	preview        viewport.Model
	showPreview    bool
	previewFor     string // target currently loaded in preview
	previewRaw     string // unshifted content for horizontal scroll
	previewXOffset int    // horizontal scroll offset (in cells)

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

// previewTickMsg drives the preview's own reload clock, separate from the 1s
// data tick.
type previewTickMsg struct{}

// wallTickMsg drives the wall's capture clock, separate from the preview's.
type wallTickMsg struct{}

type refreshMsg struct {
	items []listItem
}

// zoxideMsg carries the suggestion rows collected off the first-paint path.
type zoxideMsg struct {
	items []listItem
}

type remoteMsg struct {
	items []listItem
}

type previewMsg struct {
	content   string
	target    string
	scrollTop bool
}

// wallMsg carries one batch's captures. content is merged, never swapped in:
// tmux aborts a batch at the first bad target, so a partial reply must not blank
// the tiles it didn't reach. bad names that target, if there was one.
type wallMsg struct {
	content map[string]string
	bad     string
}

// --- Entry point ---

// layoutShowsPreview reports whether the picker opens with the preview shown,
// from @picker_layout. Anything but "list" (including unset) means preview —
// the historical default. ^/ still toggles at runtime.
func layoutShowsPreview(opts map[string]string) bool {
	return envOrMap("PICKER_LAYOUT", opts, "@picker_layout", "preview") != "list"
}

// wallMode maps the --wall flag to the mode the picker opens in.
func wallMode(wall bool) pickerMode {
	if wall {
		return modeWall
	}
	return modeList
}

// newPickerModel assembles the picker's initial model from already-gathered
// inputs — no tmux/ssh/proc calls of its own — so the first-paint wiring
// (including the Remote section, #312) is exercised by a real unit test
// instead of only by a manual tmux check.
func newPickerModel(windowMode, claudeOnly, wall bool, opts map[string]string, theme string, items []listItem) tuiModel {
	m := tuiModel{
		mode:         wallMode(wall),
		wallLaunched: wall,
		windowMode:   windowMode,
		claudeOnly:   claudeOnly,
		showPreview:  layoutShowsPreview(opts),
		theme:        theme,
		tmuxOpts:     opts,
		sessionItems: items,
		wallContent:  map[string]string{},
		wallBad:      map[string]bool{},
	}
	if !windowMode {
		// Host rows are static config — render them now so the Remote section
		// exists from the first paint. remoteCmd's probe (kicked from Init)
		// fills in each row's annotation in place via remoteMsg (#312).
		m.remoteItems = pendingRemoteItems(opts)
	}
	m = m.recombine().withFilter()
	m.cursor = m.firstSelectable(0)
	if m.mode == modeWall {
		m = m.snapWall()
	}
	return m
}

func runTUI(windowMode, claudeOnly, wall bool) error {
	theme := detectTheme()
	opts := readTmuxOpts()
	panes := collectClaudePanes()

	var items []listItem
	if windowMode {
		items = buildWindowItems(opts, panes, theme, 0)
	} else {
		items = buildSessionItems(opts, panes, theme, false)
	}

	m := newPickerModel(windowMode, claudeOnly, wall, opts, theme, items)

	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// --- Bubbletea interface ---

func (m tuiModel) Init() tea.Cmd {
	// No wall capture here: the grid is derived from a size this model doesn't
	// have yet, and nothing paints before the WindowSizeMsg that brings it.
	cmds := []tea.Cmd{tickCmd(), previewTickCmd(), wallTickCmd(), m.loadPreviewCmd()}
	if !m.windowMode {
		// First paint skips the ps -A fork; kick a full refresh right away so
		// CPU/Mem replace the placeholder without waiting for the 1s tick.
		cmds = append(cmds, m.zoxideCmd(), m.remoteCmd(), m.refreshDataCmd())
	}
	return tea.Batch(cmds...)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		widthChanged := msg.Width != m.width
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
		// Window labels are truncated to the terminal width; when it changes,
		// rebuild so the adaptive identity cap tracks the real size. Guarded on
		// change so a fixed-size popup forks the rebuild ~once.
		if m.mode == modeWall {
			// The grid is derived from the size, so a resize changes how many
			// tiles a page holds — and which page the cursor sits on.
			m = m.snapWall()
		}
		if m.windowMode && widthChanged {
			return m, tea.Batch(m.loadPreviewCmd(), m.captureWallCmd(), m.refreshDataCmd())
		}
		return m, tea.Batch(m.loadPreviewCmd(), m.captureWallCmd())

	case tickMsg:
		return m, tea.Batch(tickCmd(), m.refreshDataCmd())

	// This is the only periodic preview load: the data handlers below deliberately
	// don't, because a list rebuild moving what sits under the cursor is picked up
	// here within one interval.
	case previewTickMsg:
		return m, tea.Batch(previewTickCmd(), m.loadPreviewCmd())

	case wallTickMsg:
		return m, tea.Batch(wallTickCmd(), m.captureWallCmd())

	case wallMsg:
		return m.mergeWall(msg), nil

	case refreshMsg:
		// Read the selection before the rebuild invalidates its index.
		keep := m.currentTarget()
		m.sessionItems = msg.items
		m = m.recombine().withFilter()
		if m.cursor >= len(m.visible) {
			m.cursor = m.firstSelectable(0)
		}
		if m.mode == modeWall {
			// A rebuild can move a header under the cursor, and reorder the rows
			// under the selection; the wall has no non-tileable selection to fall
			// back on, and follows the window rather than the row number.
			m = m.snapWallTo(keep)
		}
		return m, nil

	// Both of these rebuild visible, so the wall re-snaps for the same reason
	// refreshMsg does: an index does not survive a rebuild, and the wall has no
	// non-tileable selection to land on.
	case zoxideMsg:
		keep := m.currentTarget()
		m.zoxideItems = msg.items
		m = m.recombine().withFilter()
		if m.cursor >= len(m.visible) {
			m.cursor = m.firstSelectable(0)
		}
		if m.mode == modeWall {
			m = m.snapWallTo(keep)
		}
		return m, nil

	case remoteMsg:
		keep := m.currentTarget()
		m.remoteItems = msg.items
		m = m.recombine().withFilter()
		m = m.restoreCursor(keep)
		if m.mode == modeWall {
			m = m.snapWallTo(keep)
		}
		return m, nil

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
				if msg.scrollTop {
					m.preview.SetYOffset(0)
				} else {
					m.preview.SetYOffset(m.preview.TotalLineCount())
				}
			}
		}
		return m, nil

	// Both mouse handlers hit-test by y against the list's rows, which the wall
	// doesn't have — a stray click there would resolve to an arbitrary row and,
	// when it matched the cursor, switch to it and quit.
	case tea.MouseWheelMsg:
		if m.mode != modeList {
			return m, nil
		}
		mouse := msg.Mouse()
		if m.inPreview(mouse.X, mouse.Y) {
			var cmd tea.Cmd
			m.preview, cmd = m.preview.Update(msg)
			return m, cmd
		}
		switch mouse.Button {
		case tea.MouseWheelUp:
			m = m.moveCursor(-1)
		case tea.MouseWheelDown:
			m = m.moveCursor(1)
		}
		return m, m.loadPreviewCmd()

	case tea.MouseClickMsg:
		if m.mode != modeList {
			return m, nil
		}
		mouse := msg.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		idx, ok := m.listIndexAt(mouse.X, mouse.Y)
		if !ok {
			return m, nil
		}
		if idx == m.cursor {
			return m.activateCurrent() // click the highlighted row to switch
		}
		m.statusMsg = ""
		m.cursor = idx
		return m, m.loadPreviewCmd()

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.preview, cmd = m.preview.Update(msg)
	return m, cmd
}

func (m tuiModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.statusMsg = "" // any keypress clears a stale create-error
	key := msg.String()
	// The wall's keymap runs first: letters navigate the grid there, so a shared
	// branch must never see a key the wall (or its filter prompt) owns.
	if m.mode == modeWall {
		// esc's meaning depends on focused/querying/query together, so it is
		// resolved once here rather than duplicated in the branches below.
		if key == "esc" {
			return m.applyWallEsc()
		}
		if m.querying {
			return m.handleWallQueryKey(key)
		}
		if m.focused {
			if wm, cmd, handled := m.handleFocusedKey(key); handled {
				return wm, cmd
			}
		}
		if wm, cmd, handled := m.handleWallKey(key); handled {
			return wm, cmd
		}
	}
	switch key {
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
		return m.activateCurrent()

	case "ctrl+x":
		item, ok := m.currentItem()
		if !ok {
			return m, nil
		}
		if item.createPath != "" {
			logEvent("picker", "event", "zoxide_forget", "path", item.createPath)
			if err := zoxideForget(item.createPath); err != nil {
				m.statusMsg = err.Error()
				return m, nil
			}
			return m, m.zoxideCmd()
		}
		if item.target != "" && item.remoteHost == "" {
			if strings.Contains(item.target, ":") {
				logEvent("picker", "event", "kill_window", "target", item.target)
				exec.Command("tmux", "kill-window", "-t", item.target).Run() //nolint:errcheck
			} else {
				logEvent("picker", "event", "kill_session", "target", item.target)
				exec.Command("tmux", "kill-session", "-t", item.target).Run() //nolint:errcheck
			}
			return m, m.refreshDataCmd()
		}

	case "ctrl+a":
		m = m.toggleClaudeOnly()
		return m, m.loadPreviewCmd()

	case "ctrl+s":
		m = m.toggleScratchOnly()
		return m, m.loadPreviewCmd()

	case "ctrl+/", "ctrl+_":
		// In a wall-launched popup this is the return leg of the wall toggle, and
		// the new page needs its captures now rather than at the next tick.
		if m.wallLaunched {
			m.mode = modeWall
			m = m.snapWall()
			return m, m.captureWallCmd()
		}
		m.showPreview = !m.showPreview
		if m.ready {
			m.preview.SetWidth(m.previewWidth())
			m.preview.SetHeight(m.previewHeight())
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
		if printableKey(key) {
			m.query += key
			m = m.withFilter()
			m.cursor = m.firstSelectable(0)
			return m, m.loadPreviewCmd()
		}
	}
	return m, nil
}

// printableKey reports whether a key press is a single printable character, i.e.
// one that extends the query rather than triggering a binding.
func printableKey(key string) bool {
	return len(key) == 1 && key[0] >= 0x20 && key[0] < 0x7f
}

func (m tuiModel) toggleClaudeOnly() tuiModel {
	m.claudeOnly = !m.claudeOnly
	if m.claudeOnly {
		m.scratchOnly = false
	}
	m = m.withFilter()
	m.cursor = m.firstSelectable(0)
	return m
}

func (m tuiModel) toggleScratchOnly() tuiModel {
	m.scratchOnly = !m.scratchOnly
	if m.scratchOnly {
		m.claudeOnly = false
	}
	m = m.withFilter()
	m.cursor = m.firstSelectable(0)
	return m
}

// --- Wall keys ---

// handleWallKey runs the wall's own bindings, returning handled=false for the
// keys the shared switch already gets right (enter, ^x, ^c, …). esc is not
// among them — applyWallEsc claims it before this is ever called.
func (m tuiModel) handleWallKey(key string) (tuiModel, tea.Cmd, bool) {
	cols, _ := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems()))
	switch key {
	case "h", "left":
		wm, cmd := m.moveTile(-1)
		return wm, cmd, true
	case "l", "right":
		wm, cmd := m.moveTile(1)
		return wm, cmd, true
	case "k", "up", "ctrl+k":
		wm, cmd := m.moveTile(-max(cols, 1))
		return wm, cmd, true
	case "j", "down", "ctrl+j":
		wm, cmd := m.moveTile(max(cols, 1))
		return wm, cmd, true
	case "[", "pgup":
		wm, cmd := m.turnWallPage(-1)
		return wm, cmd, true
	case "]", "pgdown":
		wm, cmd := m.turnWallPage(1)
		return wm, cmd, true
	case "/":
		m.querying = true
		return m, nil, true
	case "tab":
		// Only a tile the relay can reach is worth focusing, and only when the
		// wall actually drew a grid to focus into (the too-small fallback draws
		// the list instead, per renderWallHints).
		gridCols, gridRows := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems()))
		if item, ok := m.currentItem(); ok && relayable(item) && gridCols > 0 && gridRows > 0 {
			m.focused = true
		}
		return m, nil, true
	case "ctrl+/", "ctrl+_":
		// Back to list+preview inside the popup the wall already has; a tmux
		// popup can't be resized, so nothing is resized here. Focus is a wall
		// concept — leaving it drops the flag so a later ^/ back in starts
		// unfocused rather than silently relaying the next keystroke.
		m.mode = modeList
		m.focused = false
		return m, m.loadPreviewCmd(), true
	case "ctrl+a":
		m = m.toggleClaudeOnly().snapWall()
		return m, m.captureWallCmd(), true
	case "ctrl+s":
		m = m.toggleScratchOnly().snapWall()
		return m, m.captureWallCmd(), true
	case "ctrl+x":
		if m.focused {
			// ctrl+x is the wall's kill binding, but also an ordinary chord to
			// type (readline cut, nano save) — must not reach the shared
			// switch's unconfirmed kill while a pane is being typed into.
			return m, nil, true
		}
		return m, nil, false
	case "q":
		if m.query == "" {
			return m, tea.Quit, true
		}
		m.query = ""
		m = m.withFilter().snapWall()
		return m, m.captureWallCmd(), true
	}
	// A half-modal wall where 4 letters move and the other 22 filter cannot
	// express a query like "lazytmux", so no printable falls through to the
	// shared query branch — / opens the prompt instead.
	return m, nil, printableKey(key) || key == "backspace"
}

// handleWallQueryKey runs the wall's filter prompt. Every key belongs to the
// prompt until esc/enter closes it; only ^c still quits. esc itself is
// resolved by applyWallEsc before this is called — never reaches here.
func (m tuiModel) handleWallQueryKey(key string) (tea.Model, tea.Cmd) {
	switch {
	case key == "ctrl+c":
		return m, tea.Quit
	case key == "enter":
		m.querying = false
		return m, nil
	case key == "backspace":
		if m.query == "" {
			return m, nil
		}
		runes := []rune(m.query)
		m.query = string(runes[:len(runes)-1])
	case printableKey(key):
		m.query += key
	default:
		return m, nil
	}
	m = m.withFilter().snapWall()
	return m, m.captureWallCmd()
}

// handleFocusedKey relays an in-scope keystroke (see relayKeyArgs) to the
// focused tile's target, returning handled=false for anything outside the
// relay's scope so it falls through to handleWallKey's own bindings — that is
// how arrows keep moving the wall selection, and ctrl+a/ctrl+s keep toggling
// filters, even while a tile is focused.
func (m tuiModel) handleFocusedKey(key string) (tuiModel, tea.Cmd, bool) {
	item, ok := m.currentItem()
	if !ok || !relayable(item) {
		return m, nil, false
	}
	args, ok := relayKeyArgs(key)
	if !ok {
		return m, nil, false
	}
	target := item.target
	return m, func() tea.Msg {
		sendKeys(target, args, nil) //nolint:errcheck
		return nil
	}, true
}

// escAction is what esc does in the wall, chosen by which of its modal states
// is active.
type escAction int8

const (
	escQuit escAction = iota
	escClearQuery
	escCloseQuery
	escUnfocus
)

// wallEscAction is esc's precedence in the wall (#316): unfocus a focused tile
// before closing the filter prompt before clearing typed filter text before
// quitting. focused and querying can never both be true — a focused "/" is a
// relayed printable key, so it can never open the prompt, and the prompt's own
// switch has no case for tab, so it can never set focused — so exactly one
// rung is ever live; the two flags are still both taken so each rung has its
// own test rather than one inferred from the others.
func wallEscAction(focused, querying bool, query string) escAction {
	switch {
	case focused:
		return escUnfocus
	case querying:
		return escCloseQuery
	case query != "":
		return escClearQuery
	default:
		return escQuit
	}
}

// applyWallEsc runs wallEscAction's verdict. Escape is also one of the relay's
// in-scope keys (relayKeyArgs), but a focused esc always unfocuses rather than
// reaching the pane — a literal Escape can never reach the target through the
// wall (see relayKeyArgs for why that's accepted).
func (m tuiModel) applyWallEsc() (tea.Model, tea.Cmd) {
	switch wallEscAction(m.focused, m.querying, m.query) {
	case escUnfocus:
		m.focused = false
		return m, nil
	case escCloseQuery:
		m.querying = false
		return m, nil
	case escClearQuery:
		m.query = ""
		m = m.withFilter().snapWall()
		return m, m.captureWallCmd()
	default:
		return m, tea.Quit
	}
}

// --- Wall model ---

// relayable reports whether item is a real local pane — not a header, a
// zoxide suggestion, or a remote bridge row — the only shape capture-pane can
// read and send-keys can write. tileItems and the keystroke relay both stop at
// exactly this boundary, so both are defined off the one predicate rather than
// two copies of the same list of exclusions drifting apart.
func relayable(item listItem) bool {
	return item.target != "" && !item.isHeader && item.createPath == "" && !item.isRemoteRow && item.remoteHost == ""
}

// tileItems returns the indices into visible that are relayable.
func (m tuiModel) tileItems() []int {
	var out []int
	for i, item := range m.visible {
		if !relayable(item) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// wallPerPage is the tile count one page holds; 1 when the terminal is too small
// to tile, so paging arithmetic stays well-defined behind the list fallback.
func (m tuiModel) wallPerPage() int {
	cols, rows := wallGeometry(m.width, m.bodyHeight(), len(m.tileItems()))
	return max(cols*rows, 1)
}

func (m tuiModel) wallPageCount() int {
	n := len(m.tileItems())
	pp := m.wallPerPage()
	return max((n+pp-1)/pp, 1)
}

// tilePos is the position in tiles of the first row at or after cursor — where
// the selection snaps to when the filter moves the list underneath it.
func tilePos(tiles []int, cursor int) int {
	for i, idx := range tiles {
		if idx >= cursor {
			return i
		}
	}
	return len(tiles) - 1
}

// snapWall pins the cursor to a tileable row and the page to the one showing it.
func (m tuiModel) snapWall() tuiModel {
	return m.snapWallTo("")
}

// snapWallTo is snapWall for a rebuilt list: it re-finds keep, the target that
// was selected before the rebuild, because an index does not survive one. The
// window list is grouped by session activity, so a session gaining focus shifts
// every index after it and snapping by number lands on a different window.
// Falls back to the index when keep is gone (its pane closed) or empty.
func (m tuiModel) snapWallTo(keep string) tuiModel {
	tiles := m.tileItems()
	if len(tiles) == 0 {
		m.wallPage = 0
		return m
	}
	pos := tilePos(tiles, m.cursor)
	if keep != "" {
		for i, idx := range tiles {
			if m.visible[idx].target == keep {
				pos = i
				break
			}
		}
	}
	m.cursor = tiles[pos]
	m.wallPage = pos / m.wallPerPage()
	return m
}

// moveTile steps the selection by delta tiles in grid order. Crossing a page
// edge turns the page — that is what pagination is in the wall, there is no
// scrolling — and the new page needs its captures now, not in 500ms.
func (m tuiModel) moveTile(delta int) (tuiModel, tea.Cmd) {
	tiles := m.tileItems()
	if len(tiles) == 0 {
		return m, nil
	}
	pos := tilePos(tiles, m.cursor) + delta
	if pos < 0 || pos >= len(tiles) {
		return m, nil
	}
	m.cursor = tiles[pos]
	page := pos / m.wallPerPage()
	if page == m.wallPage {
		return m, nil
	}
	m.wallPage = page
	return m, m.captureWallCmd()
}

// turnWallPage turns a whole page, landing the cursor on its first tile.
func (m tuiModel) turnWallPage(delta int) (tuiModel, tea.Cmd) {
	tiles := m.tileItems()
	page := m.wallPage + delta
	first := page * m.wallPerPage()
	if page < 0 || first >= len(tiles) {
		return m, nil
	}
	m.wallPage = page
	m.cursor = tiles[first]
	return m, m.captureWallCmd()
}

func (m tuiModel) mergeWall(msg wallMsg) tuiModel {
	if m.wallContent == nil {
		m.wallContent = make(map[string]string, len(msg.content))
	}
	for target, content := range msg.content {
		m.wallContent[target] = content
	}
	if msg.bad != "" {
		if m.wallBad == nil {
			m.wallBad = map[string]bool{}
		}
		m.wallBad[msg.bad] = true
	}
	// Panes come and go while the wall is open; without a prune both caches grow
	// for the life of the picker and a reused target could show dead content.
	live := make(map[string]bool, len(m.visible))
	for _, i := range m.tileItems() {
		live[m.visible[i].target] = true
	}
	for target := range m.wallContent {
		if !live[target] {
			delete(m.wallContent, target)
		}
	}
	for target := range m.wallBad {
		if !live[target] {
			delete(m.wallBad, target)
		}
	}
	return m
}

// --- View ---

func (m tuiModel) View() tea.View {
	var content string
	switch {
	case !m.ready:
		content = "Loading..."

	// No bordered wrapper: its Width() would re-measure rows renderTile already
	// sized (see there), so the wall draws its own bottom rule instead.
	case m.mode == modeWall:
		content = strings.Join([]string{
			m.renderSearch(), m.renderWall(), m.renderSeparator(), m.renderWallHints(),
		}, "\n")

	default:
		body := m.renderList()
		if m.showPreview {
			body = m.renderPreview(body)
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
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// --- Layout ---

// bodyHeight is the total height available for list + preview (excludes search/hints/borders).
//
// The search bar is measured, not assumed: it borders only its bottom, so it is
// 2 rows, and 3 when a long query wraps. The other two rows are the body's
// bottom rule and the one-line hint bar.
func (m tuiModel) bodyHeight() int {
	h := m.height - lipgloss.Height(m.renderSearch()) - 2
	if h < 5 {
		return 5
	}
	return h
}

// The bounds keep both panes usable whatever the option says: a 2-row list or a
// 2-row preview is worse than no split at all.
const (
	listRatioDefault = 50
	listRatioMin     = 20
	listRatioMax     = 80
)

// listRatio is the percentage of the body the list gets, from
// @picker_list_ratio. Preview takes the rest, so a lower number is a taller
// preview.
func (m tuiModel) listRatio() int {
	raw := envOrMap("PICKER_LIST_RATIO", m.tmuxOpts, "@picker_list_ratio", "")
	r, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return listRatioDefault
	}
	return min(max(r, listRatioMin), listRatioMax)
}

func (m tuiModel) listHeight() int {
	bh := m.bodyHeight()
	if !m.showPreview {
		return bh
	}
	// Preview gets the rest of the body, minus 1 for the separator.
	return bh * m.listRatio() / 100
}

func (m tuiModel) innerWidth() int {
	return m.width // tmux popup provides the border
}

func (m tuiModel) listWidth() int {
	return m.innerWidth()
}

func (m tuiModel) previewWidth() int {
	return m.innerWidth()
}

func (m tuiModel) previewHeight() int {
	if !m.showPreview {
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
	if item.target == "" && item.remoteHost == "" {
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

func (m tuiModel) currentItem() (listItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return listItem{}, false
	}
	return m.visible[m.cursor], true
}

// restoreCursor re-finds keep (the target selected before a rebuild) in the
// new visible list so a row growing or shrinking never moves the selection
// off it — index-based restore has regressed this list three times before
// (#173, #198, #234), most recently by aliasing onto whatever row now sits
// at a stale index.
func (m tuiModel) restoreCursor(keep string) tuiModel {
	if keep != "" {
		for i, item := range m.visible {
			if item.target == keep {
				m.cursor = i
				return m
			}
		}
		m.cursor = m.firstSelectable(0)
		return m
	}
	if m.cursor >= len(m.visible) {
		m.cursor = m.firstSelectable(0)
	}
	return m
}

// activateCurrent switches to (or creates) the highlighted target and quits.
func (m tuiModel) activateCurrent() (tea.Model, tea.Cmd) {
	item, ok := m.currentItem()
	if !ok || (item.target == "" && item.remoteHost == "") {
		return m, nil
	}
	if item.remoteHost != "" {
		if err := openRemoteBridge(item.remoteHost, item.remoteSess); err != nil {
			m.statusMsg = err.Error()
			return m, nil
		}
		return m, tea.Quit
	}
	if item.createPath != "" {
		if err := createAndSwitch(item.createName, item.createPath); err != nil {
			m.statusMsg = err.Error()
			return m, nil
		}
	} else {
		logEvent("picker", "event", "switch", "target", item.target)
		exec.Command("tmux", "switch-client", "-t", item.target).Run() //nolint:errcheck
	}
	return m, tea.Quit
}

// listRowTop is the first screen row of the list body — just below the search
// box, whose lipgloss bottom border sets its height. The list body has no top
// border, so its first row sits directly beneath.
func (m tuiModel) listRowTop() int {
	return lipgloss.Height(m.renderSearch())
}

// listIndexAt maps screen coords to the selectable visible index under them.
func (m tuiModel) listIndexAt(x, y int) (int, bool) {
	if !m.ready {
		return 0, false
	}
	h := m.listHeight()
	vy := y - m.listRowTop()
	if vy < 0 || vy >= h {
		return 0, false
	}
	idx := m.scrollStart(h) + vy
	if idx < 0 || idx >= len(m.visible) || !m.isSelectable(m.visible[idx]) {
		return 0, false
	}
	return idx, true
}

// inPreview reports whether screen coords fall in the preview pane, which always
// sits below the list (past the separator row).
func (m tuiModel) inPreview(x, y int) bool {
	if !m.showPreview || !m.ready {
		return false
	}
	return y >= m.listRowTop()+m.listHeight()+1 // past the separator row
}

// --- Filter ---

// itemVisible reports whether an item passes the current mode filters
// (scratch/claude). Headers are always visible (pruned separately).
// Zoxide suggestion rows are intentionally hidden by both modes: a dir
// has no claude activity and is never a scratch session. Remote bridge rows
// are exempt instead — they are the only way to reach a host from here, so
// neither mode may drop the section.
func (m tuiModel) itemVisible(item listItem) bool {
	if item.isRemoteRow {
		return true
	}
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
		m.visible = markRemoteTreeEnds(pruneOrphanHeaders(out))
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

	// Sort by score descending; sessions always rank above remote + zoxide
	// suggestions. Stable preserves original order for ties.
	const (
		rankSession = iota
		rankRemote
		rankZoxide
	)
	sort.SliceStable(matches, func(i, j int) bool {
		rank := func(it listItem) int {
			switch {
			case it.createPath != "":
				return rankZoxide
			case it.isRemoteRow:
				return rankRemote
			default:
				return rankSession
			}
		}
		ri, rj := rank(matches[i].item), rank(matches[j].item)
		if ri != rj {
			return ri < rj
		}
		// Remote rows keep collection order so a host's sessions stay grouped
		// under it; ranking them by score breaks the tree.
		if ri == rankRemote {
			return false
		}
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
		// Re-insert section headers before the first row of each suggestion
		// block (sessions sort first, remotes next, zoxide last).
		var remoteHeader, sugHeader *listItem
		hostRows := make(map[string]listItem)
		for i := range m.allItems {
			if m.allItems[i].isRemoteHeader {
				remoteHeader = &m.allItems[i]
			}
			if m.allItems[i].isZoxideHeader {
				sugHeader = &m.allItems[i]
			}
			if it := m.allItems[i]; it.remoteHost != "" && it.remoteSess == "" {
				hostRows[it.remoteHost] = it
			}
		}
		seenHost := make(map[string]bool)
		var out []listItem
		for _, match := range matches {
			if remoteHeader != nil && match.item.isRemoteRow {
				out = append(out, *remoteHeader)
				remoteHeader = nil
			}
			// A matching session row pulls its host row in with it, so the
			// tree prefix never dangles under a host the query dropped.
			if h := match.item.remoteHost; h != "" && !seenHost[h] {
				seenHost[h] = true
				if row, ok := hostRows[h]; ok && match.item.remoteSess != "" {
					out = append(out, row)
				}
			}
			if sugHeader != nil && match.item.createPath != "" {
				out = append(out, *sugHeader)
				sugHeader = nil
			}
			out = append(out, match.item)
		}
		m.visible = markRemoteTreeEnds(out)
	}
	return m
}

// markRemoteTreeEnds gives the last visible session row under each host the
// closing tree glyph. Which row is last depends on the filter, not on the order
// the rows were collected in.
func markRemoteTreeEnds(items []listItem) []listItem {
	for i := range items {
		if items[i].displayEnd == "" {
			continue
		}
		next := i + 1
		sibling := next < len(items) &&
			items[next].remoteSess != "" &&
			items[next].remoteHost == items[i].remoteHost
		if !sibling {
			items[i].display = items[i].displayEnd
			items[i].plain = items[i].plainEnd
		}
	}
	return items
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

// previewInterval is the preview's reload clock. Fast enough that a build or a
// Claude turn reads as moving; the fork it costs is skipped entirely when the
// preview is hidden, because loadPreviewCmd returns nil then.
const previewInterval = 400 * time.Millisecond

// The clock runs for the whole session rather than being started and stopped
// with the preview: restarting it on every ^/ would leave one live loop per
// toggle, all firing.
func previewTickCmd() tea.Cmd {
	return tea.Tick(previewInterval, func(time.Time) tea.Msg { return previewTickMsg{} })
}

// wallInterval is the wall's capture clock. Slower than the preview's 400ms
// because one batch redraws a page of panes rather than a single one; still fast
// enough that a build or a Claude turn reads as moving.
const wallInterval = 500 * time.Millisecond

// Same reasoning as previewTickCmd: one clock for the whole session, or every
// ^/ leaves another live loop behind.
func wallTickCmd() tea.Cmd {
	return tea.Tick(wallInterval, func(time.Time) tea.Msg { return wallTickMsg{} })
}

// captureWallCmd batches the current page's captures into one tmux call.
func (m tuiModel) captureWallCmd() tea.Cmd {
	if m.mode != modeWall {
		return nil
	}
	targets := m.wallPageTargets()
	if len(targets) == 0 {
		return nil
	}
	return func() tea.Msg {
		content, err := captureTargets(targets, nil)
		msg := wallMsg{content: content}
		var cErr *captureErr
		if errors.As(err, &cErr) {
			msg.bad = cErr.Target
		}
		return msg
	}
}

// wallPageTargets is the page's capture set. Targets a previous batch died on
// are left out: one gone pane would otherwise abort every batch after it.
func (m tuiModel) wallPageTargets() []string {
	tiles := m.tileItems()
	pp := m.wallPerPage()
	var out []string
	for i := m.wallPage * pp; i < len(tiles) && i < m.wallPage*pp+pp; i++ {
		target := m.visible[tiles[i]].target
		if m.wallBad[target] {
			continue
		}
		out = append(out, target)
	}
	return out
}

func (m tuiModel) refreshDataCmd() tea.Cmd {
	wm := m.windowMode
	opts := m.tmuxOpts
	theme := m.theme
	lw := m.listWidth() // capture the value; the closure runs off-thread
	return func() tea.Msg {
		panes := collectClaudePanes()
		var items []listItem
		if wm {
			items = buildWindowItems(opts, panes, theme, lw)
		} else {
			items = buildSessionItems(opts, panes, theme, true)
		}
		// Always send — spinners need to animate even without structural changes.
		return refreshMsg{items: items}
	}
}

// zoxideCmd collects directory suggestions off the first-paint path (session
// mode only). The result merges in via zoxideMsg once stat-ing every zoxide
// dir completes, so the popup paints with sessions immediately.
func (m tuiModel) zoxideCmd() tea.Cmd {
	opts := m.tmuxOpts
	return func() tea.Msg {
		return zoxideMsg{items: collectZoxideItems(opts)}
	}
}

// remoteCmd collects remote host/session rows off the first-paint path
// (session mode only). ssh probes are bounded; the result merges via remoteMsg.
func (m tuiModel) remoteCmd() tea.Cmd {
	opts := m.tmuxOpts
	return func() tea.Msg {
		local := map[string]bool{}
		for _, s := range collectSessions() {
			local[s.name] = true
		}
		return remoteMsg{items: collectRemoteItems(opts, local, nil)}
	}
}

// recombine rebuilds allItems from the session base plus the async suggestion
// rows, so a 1s refresh (sessions only) doesn't drop loaded remote/zoxide entries.
func (m tuiModel) recombine() tuiModel {
	all := make([]listItem, 0, len(m.sessionItems)+len(m.remoteItems)+len(m.zoxideItems))
	all = append(all, m.sessionItems...)
	all = append(all, m.remoteItems...)
	all = append(all, m.zoxideItems...)
	m.allItems = all
	return m
}

func (m tuiModel) loadPreviewCmd() tea.Cmd {
	// The wall draws no preview, so its clock must not pay for one on top of the
	// page it already captures.
	if !m.showPreview || m.mode == modeWall {
		return nil
	}
	item, ok := m.currentItem()
	if !ok || item.target == "" {
		// A header row or a query that matched nothing. Emitting an empty
		// preview clears the last target's content instead of leaving it on
		// screen next to a selection it doesn't belong to; the handler's
		// target gate accepts it because currentTarget() is "" here too.
		return func() tea.Msg { return previewMsg{target: "", scrollTop: true} }
	}
	t := item.target
	if cp := item.createPath; cp != "" {
		return func() tea.Msg {
			return previewMsg{content: listDir(cp), target: t, scrollTop: true}
		}
	}
	if item.remoteHost != "" {
		host, sess := item.remoteHost, item.remoteSess
		return func() tea.Msg {
			msg := "remote bridge → " + host
			if sess != "" {
				msg += "/" + sess
			}
			msg += "\n\nEnter runs lztmux-remote-open (outbound ssh)."
			return previewMsg{content: msg, target: t, scrollTop: true}
		}
	}
	return func() tea.Msg {
		out, err := exec.Command("tmux", "capture-pane", "-t", t, "-p", "-e").Output()
		if err != nil {
			return previewMsg{content: "(no preview available)", target: t}
		}
		// Same untrusted content as the wall's captures, through a different path.
		content := stripStringEscapes(strings.TrimRight(string(out), "\n "))
		// Reset background at end of each line to prevent ANSI color bleeding
		// into empty viewport padding cells (e.g. opencode's black input area).
		content = strings.ReplaceAll(content, "\n", "\033[49m\n") + "\033[49m"
		return previewMsg{content: content, target: t}
	}
}

// --- Item builders ---

// resourcePlaceholder fills the CPU/Mem columns on the first paint, before the
// async resource pass (ps -A) returns. Single-byte so the len()-based column
// padding stays aligned (multibyte glyphs measure wide in bytes, narrow in cells).
const resourcePlaceholder = "-"

func buildSessionItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, withResources bool) []listItem {
	sessions := collectSessions()
	claudeMap := aggregateClaudeBySession(claudePanes)
	mergeClaude(sessions, claudeMap)

	// Resource collection forks `ps -A`, so it stays off the first-paint path:
	// the initial render passes withResources=false and an immediate async
	// refresh fills real CPU/Mem a beat later.
	var resCh chan map[string]sessionResources
	if withResources {
		resCh = make(chan map[string]sessionResources, 1)
		go func() { resCh <- collectSessionResources(sessions) }()
	}

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmBlue := envOrMap("THM_BLUE", tmuxOpts, "@thm_blue", "#89b4fa")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	iDir := envOrMap("PICKER_ICON_DIR", tmuxOpts, "@icon_dir", iconDir)
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)

	cMauve := ansiFg(thmMauve)
	cBlue := ansiFg(thmBlue)
	cDim := ansiFg(thmSubtext0)
	cPeach := ansiFg(envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387"))
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
		icons, dw = appendIssueIDs(icons, dw, s.claude.issues, cDim, reset)
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

	// Host column: only remote-bridge mirrors carry @bridge_host, so an
	// all-local list spends no width on it at all.
	hostCol := 0
	for i := range sessions {
		hostCol = max(hostCol, len(sessions[i].bridgeHost))
	}
	if hostCol > 0 {
		hostCol = max(hostCol, len("Host"))
	}
	// color wraps the text only, so the plain variant (color "") stays escape-free.
	hostCell := func(host, color string) string {
		if hostCol == 0 {
			return ""
		}
		tail := strings.Repeat(" ", hostCol-len(host)) + "  "
		if host == "" || color == "" {
			return host + tail
		}
		return color + host + reset + tail
	}

	if withResources {
		mergeResources(sessions, <-resCh)
	}

	// Pre-compute CPU and MEM strings separately so the "/" aligns
	cpuStrs := make([]string, len(rows))
	memStrs := make([]string, len(rows))
	maxCPU, maxMem := cpuColWidth(), 0
	for i, r := range rows {
		if withResources {
			cpuStrs[i] = formatCPU(r.sess.cpuPct)
			memStrs[i] = formatMem(r.sess.memMB)
		} else {
			cpuStrs[i] = resourcePlaceholder
			memStrs[i] = resourcePlaceholder
		}
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
	hdrRes := hdrCPUPad + hdrCPU + " / " + hdrMemPad + hdrMem
	hdrDisplay := fmt.Sprintf("%s %s%s  %s%s  %s  %s %s",
		cDim+iSess+reset,
		cDim+"Session"+reset,
		strings.Repeat(" ", max(0, maxName-7)),
		hostCell("Host", cDim),
		cDim+padToWidth("Procs", 5, iconCol)+reset,
		cDim+hdrRes+reset,
		cDim+iDir+reset,
		cDim+"Path"+reset,
	)
	hdrPlain := fmt.Sprintf("%s %s%s  %s%s  %s  %s %s",
		iSess, "Session", strings.Repeat(" ", max(0, maxName-7)),
		hostCell("Host", ""), padToWidth("Procs", 5, iconCol), hdrRes, iDir, "Path",
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
		display := fmt.Sprintf("%s %s%s  %s%s  %s%s %s %s%s  %s %s",
			cMauve+iSess+reset,
			cMauve+r.sess.name+reset,
			pad,
			hostCell(r.sess.bridgeHost, cPeach),
			icons,
			cpuPad,
			rc.cpuColor(r.sess.cpuPct)+cpuStrs[i]+reset,
			cDim+"/"+reset,
			memPad,
			rc.memColor(r.sess.memMB)+memStrs[i]+reset,
			cBlue+iDir+reset,
			cDim+shortPath+reset,
		)
		resPlain := cpuPad + cpuStrs[i] + " / " + memPad + memStrs[i]
		plain := fmt.Sprintf("%s %s%s  %s%s  %s  %s %s",
			iSess, r.sess.name, pad, hostCell(r.sess.bridgeHost, ""),
			stripANSI(icons), resPlain, iDir, shortPath,
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

// collectZoxideItems builds the "New session" suggestion rows (header + zoxide
// dirs). Runs off the first-paint path so its zoxide query + per-dir stat walk
// never blocks the popup; the result merges in via zoxideMsg.
func collectZoxideItems(tmuxOpts map[string]string) []listItem {
	zoxExclude := parseExcludePatterns(envOrMap("PICKER_ZOXIDE_EXCLUDE", tmuxOpts, "@picker_zoxide_exclude", ""))
	sugs := collectZoxide(collectSessions(), zoxExclude)
	if len(sugs) == 0 {
		return nil
	}

	cBlue := ansiFg(envOrMap("THM_BLUE", tmuxOpts, "@thm_blue", "#89b4fa"))
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	iDir := envOrMap("PICKER_ICON_DIR", tmuxOpts, "@icon_dir", iconDir)
	reset := "\033[0m"
	home := os.Getenv("HOME")

	rule := "── New session " + strings.Repeat("─", 220)
	items := make([]listItem, 0, len(sugs)+1)
	items = append(items, listItem{
		display:        cDim + rule + reset,
		plain:          rule,
		isHeader:       true,
		isZoxideHeader: true,
	})
	for _, sg := range sugs {
		shortPath := sg.path
		if home != "" && strings.HasPrefix(shortPath, home) {
			shortPath = "~" + shortPath[len(home):]
		}
		display := fmt.Sprintf("%s %s  %s", cBlue+iDir+reset, sg.name, cDim+shortPath+reset)
		plain := fmt.Sprintf("%s %s  %s", iDir, sg.name, shortPath)
		items = append(items, listItem{
			target:     sg.path,
			createPath: sg.path,
			createName: sg.name,
			display:    display,
			plain:      plain,
			searchText: sg.name + " " + shortPath,
		})
	}
	return items
}

func buildWindowItems(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem {
	return renderWindowItems(collectWindows(), tmuxOpts, claudePanes, theme, width)
}

const (
	defaultIdentityCap = 32
	minIdentityCap     = 12
	maxIdentityCap     = 48
	// layoutGaps: tree(2)+marker(1) glyph cells + 3 inter-field gaps + 1 gap
	// before the PR badge = 7.
	layoutGaps = 7
)

// identityCapFor sizes the inline-identity column from the terminal width, so
// wider terminals show longer ticket titles and narrow ones shorten them while
// the icon and PR columns stay pinned. width<=0 (size unknown) uses the default.
func identityCapFor(width, leadDW, iconDW, prDW int) int {
	if width <= 0 {
		return defaultIdentityCap
	}
	identCap := width - leadDW - iconDW - prDW - layoutGaps
	if identCap < minIdentityCap {
		return minIdentityCap
	}
	if identCap > maxIdentityCap {
		return maxIdentityCap
	}
	return identCap
}

// renderWindowItems is the pure rendering half of buildWindowItems, split out so
// the enriched row layout can be unit-tested with synthetic windows. width is
// the list width in cells (0 = unknown → default identity cap).
func renderWindowItems(windows []windowData, tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string, width int) []listItem {
	claudeByWin := aggregateClaudeByWindow(claudePanes)
	mergeClaudeWindows(windows, claudeByWin)

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmGreen := envOrMap("THM_GREEN", tmuxOpts, "@thm_green", "#a6e3a1")
	thmRed := envOrMap("THM_RED", tmuxOpts, "@thm_red", "#f38ba8")
	thmPeach := envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	thmOverlay1 := envOrMap("THM_OVERLAY_1", tmuxOpts, "@thm_overlay_1", "#7f849c")
	thmOverlay0 := envOrMap("THM_OVERLAY_0", tmuxOpts, "@thm_overlay_0", "#6c7086")
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)
	iBranch := envOrMap("PICKER_ICON_BRANCH", tmuxOpts, "@icon_branch", iconBranch)

	cMauve := ansiFg(thmMauve)
	cGreen := ansiFg(thmGreen)
	cDim := ansiFg(thmSubtext0)
	cFaint := ansiFg(thmOverlay1)
	reset := "\033[0m"
	dim := "\033[2m"
	// Peach for pending mirrors the status bar (tmux-reflow-windows PRCOLOR);
	// closed = overlay0 (dim), matching the dead/superseded-PR treatment there.
	prCols := prColors{success: cGreen, failure: ansiFg(thmRed), pending: ansiFg(thmPeach), merged: cMauve, closed: ansiFg(thmOverlay0), reset: reset}

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

	// Pass A builds every fixed-width piece and the raw identity parts, and
	// tracks the column maxima (lead prefix, icons, PR). The identity cap is
	// then derived from the terminal width, and pass B truncates + aligns.
	type rawIdentity struct {
		kind      int    // 0 none/name, 1 issue, 2 branch
		id, rest  string // issue: id (accent) + rest (dim)
		text      string // branch/name plain text
		leadGlyph string // branch icon, already includes trailing space, or ""
	}
	type renderedWin struct {
		win         *windowData
		name        string // clean window name, for search
		icons       string
		iconDW      int
		leadPlain   string // "N: " + crew + " "  (uncolored, for width)
		leadColored string
		leadDW      int
		ident       rawIdentity
		identSearch string
		prBadge     string
		prPlain     string
		crewName    string
	}
	winRows := make(map[string][]renderedWin)
	maxLeadDW, maxIconDW, maxPrDW, maxZoomDW := 0, 0, 0, 0
	for _, g := range groups {
		for _, w := range g.windows {
			icons, dw := buildProcIcons(w.procs, maxIconsPicker)
			icons, dw = appendClaudeIcon(icons, dw, w.claude, theme, dim, reset)
			icons, dw = appendIssueIDs(icons, dw, w.claude.issues, cDim, reset)

			name := truncateCells(w.name, 40)

			// Lead: "N: " then the crew codename (after the index), only when
			// tagged — no reserved gap otherwise.
			leadPlain := fmt.Sprintf("%d: ", w.index)
			leadColored := leadPlain
			if w.crewName != "" {
				leadPlain += w.crewName + " "
				crew := w.crewName
				if c := ansiFgTmux(w.crewColor); c != "" {
					crew = c + w.crewName + reset
				}
				leadColored += crew + " "
			}
			leadDW := iconCellWidth(leadPlain)

			// Inline identity (the name): issue id+title → non-default branch →
			// remote bridge name → repo basename. Mirrors the status bar's
			// build_window_label priority.
			var ri rawIdentity
			var idSearch string
			if w.labelID != "" {
				ri = rawIdentity{kind: 1, id: w.labelID, rest: w.labelRest}
				idSearch = w.labelID + w.labelRest
			} else if w.branch != "" && !branchEchoesName(w.branch, w.name) && w.branch != "main" && w.branch != "master" {
				ri = rawIdentity{kind: 2, text: w.branch, leadGlyph: ""}
				if iBranch != "" {
					ri.leadGlyph = iBranch + " "
				}
				idSearch = w.branch
			} else if w.bridgeName != "" {
				ri = rawIdentity{kind: 0, text: truncateCells(w.bridgeName, 40)}
				idSearch = w.bridgeName
			} else {
				ri = rawIdentity{kind: 0, text: name}
				idSearch = name
			}

			prBadge := colorPRBadge(w.prPlain, w.prState, w.prCheck, w.prMergeable, prCols)
			prPlain := strings.TrimSpace(w.prPlain)
			prDW := iconCellWidth(prPlain)

			winRows[g.name] = append(winRows[g.name], renderedWin{
				win: w, name: name, icons: icons, iconDW: dw,
				leadPlain: leadPlain, leadColored: leadColored, leadDW: leadDW,
				ident: ri, identSearch: idSearch,
				prBadge: prBadge, prPlain: prPlain, crewName: w.crewName,
			})
			maxLeadDW = max(maxLeadDW, leadDW)
			maxIconDW = max(maxIconDW, dw)
			maxPrDW = max(maxPrDW, prDW)
			if w.zoomed {
				maxZoomDW = max(maxZoomDW, iconCellWidth(" 󰁌"))
			}
		}
	}
	iconCol := max(maxIconDW+1, 3)
	identityCap := identityCapFor(width, maxLeadDW+maxZoomDW, iconCol, maxPrDW)
	// Uniform label column so the icon column lines up across every row.
	labelCol := maxLeadDW + identityCap + maxZoomDW

	// truncID renders a rawIdentity to (colored, plain) within identityCap cells.
	truncID := func(ri rawIdentity) (string, string) {
		switch ri.kind {
		case 1: // issue: id accent + dim title
			id := ri.id
			idW := iconCellWidth(id)
			rest := ri.rest
			if idW >= identityCap {
				id = truncateCells(id, identityCap)
				rest = ""
				idW = iconCellWidth(id)
			} else if idW+iconCellWidth(rest) > identityCap {
				rest = truncateCells(rest, identityCap-idW)
			}
			plain := id + rest
			colored := cMauve + id + reset
			if rest != "" {
				colored += cDim + rest + reset
			}
			return colored, plain
		case 2: // branch: faint, optional glyph
			br := truncateCells(ri.text, max(identityCap-iconCellWidth(ri.leadGlyph), 1))
			plain := ri.leadGlyph + br
			return cFaint + plain + reset, plain
		default: // name: plain
			nm := truncateCells(ri.text, identityCap)
			return nm, nm
		}
	}

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

			activeMarker := " "
			if w.active && multiWin {
				activeMarker = cGreen + "▸" + reset
			}

			icons := r.icons
			if icons == "" {
				icons = strings.Repeat(" ", iconCol)
			} else {
				icons = padToWidth(icons, r.iconDW, iconCol)
			}

			tree := "├─"
			if wi == len(rows)-1 {
				tree = "╰─"
			}

			idColored, idPlain := truncID(r.ident)
			zoom := ""
			if w.zoomed {
				zoom = " 󰁌" // width budgeted into labelCol via maxZoomDW
			}
			lead := padToWidth(r.leadColored, r.leadDW, maxLeadDW)
			leadPlainPadded := padToWidth(r.leadPlain, r.leadDW, maxLeadDW)
			labelColored := lead + idColored + zoom
			labelPlain := leadPlainPadded + idPlain + zoom
			labelColored = padToWidth(labelColored, iconCellWidth(labelPlain), labelCol)
			labelPlain = padToWidth(labelPlain, iconCellWidth(labelPlain), labelCol)

			display := fmt.Sprintf("%s %s %s %s",
				cDim+tree+reset, activeMarker, labelColored, icons)
			plain := fmt.Sprintf("%s %s %s %s",
				tree, strings.TrimSpace(stripANSI(activeMarker)), labelPlain, stripANSI(icons))
			if r.prBadge != "" {
				display += " " + r.prBadge
				plain += " " + r.prPlain
			}
			display = strings.TrimRight(display, " ")
			plain = strings.TrimRight(plain, " ")

			search := g.name + " " + r.name
			if r.identSearch != "" {
				search += " " + r.identSearch
			}
			if r.prPlain != "" {
				search += " " + r.prPlain
			}
			if r.crewName != "" {
				search += " " + r.crewName
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

	fzfBonusConsecutive         = 4  // -(scoreGapStart + scoreGapExtension)
	fzfBonusBoundary            = 8  // scoreMatch / 2
	fzfBonusBoundaryWhite       = 10 // boundary + 2
	fzfBonusBoundaryDelimiter   = 9  // boundary + 1
	fzfBonusNonWord             = 8
	fzfBonusCamelCase           = 7 // boundary + scoreGapExtension
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

func detectTheme() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		xdg = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	data, err := os.ReadFile(filepath.Join(xdg, "theme-state.json"))
	if err != nil {
		return "dark"
	}
	var cfg struct {
		Theme string `json:"theme"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.Theme == "" {
		return "dark"
	}
	return cfg.Theme
}
