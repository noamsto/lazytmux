// tmux-picker-generate outputs ANSI-colored session/window lists for fzf.
// It replaces the bash --generate function for speed (~4ms vs ~85ms).
//
// Usage:
//
//	tmux-picker-generate              # session mode (default)
//	tmux-picker-generate --windows    # window mode
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Build-time constants injected via icons_generated.go:
//   iconMap, fallbackIcon, maxIconsPicker
//   claudeSpinnerFrames, claudeIconWaiting, claudeIconCompacting,
//   claudeIconDone, claudeIconIdle, claudeIconError
//   iconSession, iconDir, iconBranch (defaults, overridden by env/tmux)

// Staleness thresholds (seconds)
const (
	staleWaiting    = 30
	staleCompacting = 60
	staleProcessing = 300
	staleDone       = 60
	staleError      = 120
)

// Catppuccin hex colors per theme
var claudeColors = map[string]map[string]string{
	"dark": {
		"waiting": "#fab387", "compacting": "#89dceb", "processing": "#94e2d5",
		"done": "#a6e3a1", "idle": "#6c7086", "error": "#f38ba8",
	},
	"light": {
		"waiting": "#fe640b", "compacting": "#04a5e5", "processing": "#179299",
		"done": "#40a02b", "idle": "#6c6f85", "error": "#d20f39",
	},
}

type sessionData struct {
	name     string
	path     string
	activity int64
	procs    []string // unique process names
	claude   claudeCounts
}

type windowData struct {
	session  string
	index    int
	name     string
	zoomed   bool
	branch   string
	active   bool // currently active window in its session
	procs    []string
	claude   claudeCounts
}

type claudeCounts struct {
	waiting, compacting, processing, done, idle, errorCnt int
	allStale                                              bool
}

// claudePaneInfo holds parsed pane file data with window-level targeting.
type claudePaneInfo struct {
	session string
	winIdx  int
	state   string
	ts      int64
	stale   bool
}

func main() {
	windowMode := len(os.Args) > 1 && os.Args[1] == "--windows"

	// Run tmux calls + file reads in parallel
	type optsResult struct {
		opts map[string]string
	}
	optsCh := make(chan optsResult, 1)
	go func() {
		optsCh <- optsResult{readTmuxOpts()}
	}()

	claudePanes := collectClaudePanes()
	theme := detectTheme()

	or := <-optsCh
	tmuxOpts := or.opts

	if windowMode {
		renderWindows(tmuxOpts, claudePanes, theme)
	} else {
		renderSessions(tmuxOpts, claudePanes, theme)
	}
}

// ---------------------------------------------------------------------------
// Session mode
// ---------------------------------------------------------------------------

func renderSessions(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string) {
	sessions := collectSessions()
	claude := aggregateClaudeBySession(claudePanes)
	mergeClaude(sessions, claude)

	thmMauve := envOrMap("THM_MAUVE", tmuxOpts, "@thm_mauve", "#cba6f7")
	thmBlue := envOrMap("THM_BLUE", tmuxOpts, "@thm_blue", "#89b4fa")
	thmSubtext0 := envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")
	iDir := envOrMap("PICKER_ICON_DIR", tmuxOpts, "@icon_dir", iconDir)
	iSess := envOrMap("PICKER_ICON_SESSION", tmuxOpts, "@icon_session", iconSession)

	cMauve := ansiFg(thmMauve)
	cBlue := ansiFg(thmBlue)
	cDim := ansiFg(thmSubtext0)
	reset := "\033[0m"
	dim := "\033[2m"

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].activity != sessions[j].activity {
			return sessions[i].activity > sessions[j].activity
		}
		return sessions[i].name < sessions[j].name
	})

	type rendered struct {
		sess  *sessionData
		icons string
		iconDW int
	}
	rows := make([]rendered, len(sessions))
	maxName := 0
	maxIconDW := 0

	for i, s := range sessions {
		if len(s.name) > maxName {
			maxName = len(s.name)
		}
		icons, dw := buildProcIcons(s.procs, maxIconsPicker)
		icons, dw = appendClaudeIcon(icons, dw, s.claude, theme, dim, reset)
		rows[i] = rendered{sess: &sessions[i], icons: icons, iconDW: dw}
		if dw > maxIconDW {
			maxIconDW = dw
		}
	}

	iconCol := maxIconDW + 1
	if iconCol < 5 {
		iconCol = 5
	}
	for i := range rows {
		rows[i].icons = padToWidth(rows[i].icons, rows[i].iconDW, iconCol)
	}
	emptyIcons := strings.Repeat(" ", iconCol)

	// Header
	namePad := strings.Repeat(" ", max(0, maxName-7))
	procsPad := emptyIcons[min(5, len(emptyIcons)):]
	fmt.Printf("%s %s%s  %s  %s %s\n",
		cDim+iSess+reset,
		cDim+"Session"+reset,
		namePad,
		cDim+"Procs"+procsPad+reset,
		cDim+iDir+reset,
		cDim+"Path"+reset,
	)

	home := os.Getenv("HOME")
	for _, r := range rows {
		shortPath := r.sess.path
		if home != "" && strings.HasPrefix(shortPath, home) {
			shortPath = "~" + shortPath[len(home):]
		}
		pad := strings.Repeat(" ", max(0, maxName-len(r.sess.name)))
		icons := r.icons
		if icons == "" {
			icons = emptyIcons
		}
		fmt.Printf("%s %s%s  %s  %s %s\n",
			cMauve+iSess+reset,
			cMauve+r.sess.name+reset,
			pad,
			icons,
			cBlue+iDir+reset,
			cDim+shortPath+reset,
		)
	}
}

// ---------------------------------------------------------------------------
// Window mode
// ---------------------------------------------------------------------------

func renderWindows(tmuxOpts map[string]string, claudePanes []claudePaneInfo, theme string) {
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

	// Group by session
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

	// Pre-compute icons per window
	type renderedWin struct {
		win    *windowData
		icons  string
		iconDW int
	}
	winRows := make(map[string][]renderedWin) // session -> windows
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
	iconCol := maxIconDW + 1
	if iconCol < 3 {
		iconCol = 3
	}
	for _, rows := range winRows {
		for i := range rows {
			rows[i].icons = padToWidth(rows[i].icons, rows[i].iconDW, iconCol)
		}
	}
	emptyIcons := strings.Repeat(" ", iconCol)

	// Two row types:
	//   Session row: "icon SESSION (active_window)" — selectable, switches to session
	//   Window row:  "  SESSION tree N: name  icons  branch" — selectable, switches to window
	//
	// SESSION is always field 2 for extraction. Window rows have "N:" pattern.
	// Session rows have no "N:" — extraction returns just session name.

	// Header
	fmt.Printf(" %s\n", cDim+"Sessions & Windows"+reset)

	for _, g := range groups {
		rows := winRows[g.name]

		// Find active window name for session header
		activeWin := ""
		for _, r := range rows {
			if r.win.active {
				name := r.win.name
				if len(name) > 25 {
					name = name[:23] + "…"
				}
				activeWin = name
				break
			}
		}

		// Session header row
		fmt.Printf("%s %s %s%s%s\n",
			cMauve+iSess+reset,
			cMauve+g.name+reset,
			cDim+"("+reset,
			cFaint+activeWin+reset,
			cDim+")"+reset,
		)

		multiWin := len(rows) > 1

		// Window rows under this session
		for wi, r := range rows {
			w := r.win

			// Tree connector
			var tree string
			if wi == len(rows)-1 {
				tree = "╰─"
			} else {
				tree = "├─"
			}

			// Window name
			name := w.name
			if len(name) > 25 {
				name = name[:23] + "…"
			}
			winLabel := fmt.Sprintf("%d: %s", w.index, name)
			if w.zoomed {
				winLabel += " 󰁌"
			}

			// Active indicator: green dot, only in multi-window sessions
			activeMarker := " "
			if w.active && multiWin {
				activeMarker = cGreen + "·" + reset
			}

			// Icons
			icons := r.icons
			if icons == "" {
				icons = emptyIcons
			}

			// Branch
			var branchDisplay string
			if w.branch != "" {
				br := w.branch
				if len(br) > 35 {
					br = br[:33] + "…"
				}
				branchDisplay = "  " + cFaint + br + reset
			}

			fmt.Printf("  %s %s %s %s %s%s\n",
				dim+g.name+reset,
				cDim+tree+reset,
				activeMarker,
				winLabel,
				icons,
				branchDisplay,
			)
		}
	}
}

// ---------------------------------------------------------------------------
// Data collection
// ---------------------------------------------------------------------------

func collectSessions() []sessionData {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}\t#{session_path}\t#{session_last_attached}\t#{pane_current_command}").Output()
	if err != nil {
		return nil
	}

	type sessInfo struct {
		path     string
		activity int64
		seen     map[string]bool
		procs    []string
	}
	m := make(map[string]*sessInfo)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		name, path, actStr, proc := parts[0], parts[1], parts[2], parts[3]
		act, _ := strconv.ParseInt(actStr, 10, 64)

		si, ok := m[name]
		if !ok {
			si = &sessInfo{path: path, activity: act, seen: make(map[string]bool)}
			m[name] = si
		}
		if act > si.activity {
			si.activity = act
		}
		if proc != "" && !si.seen[proc] {
			si.seen[proc] = true
			si.procs = append(si.procs, proc)
		}
	}

	sessions := make([]sessionData, 0, len(m))
	for name, si := range m {
		sessions = append(sessions, sessionData{
			name:     name,
			path:     si.path,
			activity: si.activity,
			procs:    si.procs,
		})
	}
	return sessions
}

func collectSessionActivity() map[string]int64 {
	out, err := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}\t#{session_last_attached}").Output()
	if err != nil {
		return nil
	}
	m := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		act, _ := strconv.ParseInt(parts[1], 10, 64)
		m[parts[0]] = act
	}
	return m
}

// stripTmuxColors removes tmux #[...] style markup and leading/trailing spaces.
func stripTmuxColors(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '#' && s[i+1] == '[' {
			// Skip until closing ]
			j := strings.IndexByte(s[i:], ']')
			if j >= 0 {
				i += j + 1
				continue
			}
		}
		sb.WriteByte(s[i])
		i++
	}
	return strings.TrimSpace(sb.String())
}

func collectWindows() []windowData {
	// Fetch both @branch and pane path basename. The window_name contains
	// icons/colors from automatic-rename-format so we reconstruct a clean name.
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}\t#{window_index}\t#{b:pane_current_path}\t#{window_zoomed_flag}\t#{pane_current_command}\t#{window_active}\t#{@branch}").Output()
	if err != nil {
		return nil
	}

	type winKey struct {
		sess string
		idx  int
	}
	type winInfo struct {
		name   string
		zoomed bool
		active bool
		branch string
		seen   map[string]bool
		procs  []string
	}
	m := make(map[winKey]*winInfo)
	// Preserve ordering
	var order []winKey

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 7)
		if len(parts) < 6 {
			continue
		}
		sess := parts[0]
		idx, _ := strconv.Atoi(parts[1])
		wName := stripTmuxColors(parts[2])
		zoomed := parts[3] == "1"
		proc := parts[4]
		active := parts[5] == "1"
		branch := ""
		if len(parts) >= 7 {
			branch = stripTmuxColors(parts[6])
		}

		k := winKey{sess, idx}
		wi, ok := m[k]
		if !ok {
			wi = &winInfo{name: wName, zoomed: zoomed, active: active, branch: branch, seen: make(map[string]bool)}
			m[k] = wi
			order = append(order, k)
		}
		if proc != "" && !wi.seen[proc] {
			wi.seen[proc] = true
			wi.procs = append(wi.procs, proc)
		}
	}

	windows := make([]windowData, 0, len(order))
	for _, k := range order {
		wi := m[k]
		windows = append(windows, windowData{
			session: k.sess,
			index:   k.idx,
			name:    wi.name,
			zoomed:  wi.zoomed,
			active:  wi.active,
			branch:  wi.branch,
			procs:   wi.procs,
		})
	}
	return windows
}

// ---------------------------------------------------------------------------
// Claude status
// ---------------------------------------------------------------------------

func collectClaudePanes() []claudePaneInfo {
	dir := "/tmp/claude-status/panes"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Build pane_id -> (session, window_index) mapping
	paneMap := buildPaneMap()
	now := time.Now().Unix()
	var result []claudePaneInfo

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		var state, session string
		var timestamp int64
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				switch k {
				case "state":
					state = v
				case "session":
					session = v
				case "timestamp":
					timestamp, _ = strconv.ParseInt(v, 10, 64)
				}
			}
		}
		if session == "" || state == "" {
			continue
		}

		stale := isStale(state, now, timestamp)

		// Try to resolve window index from pane map
		winIdx := -1
		if pm, ok := paneMap[e.Name()]; ok {
			winIdx = pm.winIdx
		}

		result = append(result, claudePaneInfo{
			session: session,
			winIdx:  winIdx,
			state:   state,
			ts:      timestamp,
			stale:   stale,
		})
	}
	return result
}

type paneMapping struct {
	session string
	winIdx  int
}

func buildPaneMap() map[string]paneMapping {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}\t#{session_name}\t#{window_index}").Output()
	if err != nil {
		return nil
	}
	m := make(map[string]paneMapping)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		paneID := strings.TrimPrefix(parts[0], "%")
		idx, _ := strconv.Atoi(parts[2])
		m[paneID] = paneMapping{session: parts[1], winIdx: idx}
	}
	return m
}

func aggregateClaudeBySession(panes []claudePaneInfo) map[string]*claudeCounts {
	result := make(map[string]*claudeCounts)
	for _, p := range panes {
		cc, ok := result[p.session]
		if !ok {
			cc = &claudeCounts{allStale: true}
			result[p.session] = cc
		}
		if !p.stale {
			cc.allStale = false
		}
		addClaudeState(cc, p.state)
	}
	return result
}

func aggregateClaudeByWindow(panes []claudePaneInfo) map[string]*claudeCounts {
	result := make(map[string]*claudeCounts)
	for _, p := range panes {
		if p.winIdx < 0 {
			continue
		}
		key := fmt.Sprintf("%s:%d", p.session, p.winIdx)
		cc, ok := result[key]
		if !ok {
			cc = &claudeCounts{allStale: true}
			result[key] = cc
		}
		if !p.stale {
			cc.allStale = false
		}
		addClaudeState(cc, p.state)
	}
	return result
}

func addClaudeState(cc *claudeCounts, state string) {
	switch state {
	case "waiting":
		cc.waiting++
	case "compacting":
		cc.compacting++
	case "processing":
		cc.processing++
	case "done":
		cc.done++
	case "idle":
		cc.idle++
	case "error":
		cc.errorCnt++
	}
}

func isStale(state string, now, ts int64) bool {
	if ts == 0 {
		return false
	}
	age := now - ts
	switch state {
	case "waiting":
		return age > staleWaiting
	case "compacting":
		return age > staleCompacting
	case "processing":
		return age > staleProcessing
	case "done":
		return age > staleDone
	case "error":
		return age > staleError
	}
	return false
}

func mergeClaude(sessions []sessionData, claude map[string]*claudeCounts) {
	if claude == nil {
		return
	}
	for i := range sessions {
		if cc, ok := claude[sessions[i].name]; ok {
			sessions[i].claude = *cc
		}
	}
}

func mergeClaudeWindows(windows []windowData, claude map[string]*claudeCounts) {
	if claude == nil {
		return
	}
	for i := range windows {
		key := fmt.Sprintf("%s:%d", windows[i].session, windows[i].index)
		if cc, ok := claude[key]; ok {
			windows[i].claude = *cc
		}
	}
}

func claudePriority(c claudeCounts) string {
	if c.errorCnt > 0 {
		return "error"
	}
	if c.waiting > 0 {
		return "waiting"
	}
	if c.compacting > 0 {
		return "compacting"
	}
	if c.processing > 0 {
		return "processing"
	}
	if c.done > 0 {
		return "done"
	}
	if c.idle > 0 {
		return "idle"
	}
	return ""
}

func claudeStateIcon(state string) string {
	now := time.Now().Unix()
	switch state {
	case "processing":
		return claudeSpinnerFrames[int(now)%len(claudeSpinnerFrames)]
	case "waiting":
		return claudeIconWaiting
	case "compacting":
		return claudeIconCompacting
	case "done":
		return claudeIconDone
	case "idle":
		return claudeIconIdle
	case "error":
		return claudeIconError
	}
	return ""
}

func appendClaudeIcon(icons string, dw int, cc claudeCounts, theme, dim, reset string) (string, int) {
	state := claudePriority(cc)
	if state == "" {
		return icons, dw
	}
	icon := claudeStateIcon(state)
	var color string
	if cc.allStale {
		color = dim
	} else {
		colors := claudeColors[theme]
		if hex, ok := colors[state]; ok {
			color = ansiFg(hex)
		}
	}
	icons += color + icon + reset + " "
	dw += 2
	return icons, dw
}

// ---------------------------------------------------------------------------
// Icon helpers
// ---------------------------------------------------------------------------

func buildProcIcons(procs []string, maxCount int) (string, int) {
	var sb strings.Builder
	dw := 0
	count := 0
	for _, proc := range procs {
		if count >= maxCount {
			break
		}
		icon, ok := iconMap[proc]
		if !ok {
			icon = fallbackIcon
		}
		if icon == "" {
			continue
		}
		sb.WriteString(icon)
		sb.WriteByte(' ')
		dw += iconCellWidth(icon) + 1
		count++
	}
	return sb.String(), dw
}

// iconCellWidth returns the display width of an icon string.
// Nerd font PUA (E000-F8FF, F0000+) = 1 cell, emoji/other = 2 cells.
// Zero-width characters (variation selectors, ZWJ, combining marks) = 0 cells.
func iconCellWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == 0xFE0E || r == 0xFE0F: // variation selectors
			// zero width
		case r == 0x200D: // zero-width joiner
			// zero width
		case r >= 0x0300 && r <= 0x036F: // combining diacritical marks
			// zero width
		case r >= 0x20D0 && r <= 0x20FF: // combining marks for symbols
			// zero width
		case (r >= 0xE000 && r <= 0xF8FF) || r >= 0xF0000:
			w++ // nerd font PUA = 1 cell
		case r > 0x7F:
			w += 2 // emoji/other = 2 cells
		default:
			w++ // ASCII = 1 cell
		}
	}
	return w
}

func padToWidth(s string, currentWidth, targetWidth int) string {
	pad := targetWidth - currentWidth
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// ---------------------------------------------------------------------------
// Theme / tmux helpers
// ---------------------------------------------------------------------------

func ansiFg(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return ""
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
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

func readTmuxOpts() map[string]string {
	out, err := exec.Command("tmux", "show", "-g").Output()
	if err != nil {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.IndexByte(line, ' '); i > 0 {
			v := strings.TrimRight(line[i+1:], " \t\r")
			v = strings.Trim(v, "\"")
			m[line[:i]] = v
		}
	}
	return m
}

func envOrMap(envKey string, tmuxOpts map[string]string, tmuxOpt, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if v, ok := tmuxOpts[tmuxOpt]; ok && v != "" {
		return v
	}
	return fallback
}
