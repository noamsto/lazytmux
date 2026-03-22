// tmux-picker-generate outputs ANSI-colored session/window lists for fzf.
// It replaces the bash --generate function for speed (~4ms vs ~85ms).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Build-time constants injected via icons_generated.go:
//   iconMap, fallbackIcon, maxIconsPicker
//   claudeSpinnerFrames, claudeIconWaiting, claudeIconCompacting,
//   claudeIconDone, claudeIconIdle, claudeIconError
//   iconSession, iconDir (defaults, overridden by env/tmux)

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

type claudeCounts struct {
	waiting, compacting, processing, done, idle, errorCnt int
	allStale                                              bool
}

func main() {
	// Run tmux calls + file reads in parallel
	type sessResult struct {
		sessions []sessionData
	}
	type optsResult struct {
		opts map[string]string
	}
	sessCh := make(chan sessResult, 1)
	optsCh := make(chan optsResult, 1)

	go func() {
		sessCh <- sessResult{collectSessions()}
	}()
	go func() {
		optsCh <- optsResult{readTmuxOpts()}
	}()
	claude := collectClaude() // file reads, runs concurrently with tmux calls
	theme := detectTheme()

	sr := <-sessCh
	or := <-optsCh
	sessions := sr.sessions
	tmuxOpts := or.opts

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

	// Sort by most recently attached, then alphabetically as tiebreaker.
	// session_last_attached only changes on session switch (not every
	// keystroke like session_activity), so order is stable across refreshes.
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].activity != sessions[j].activity {
			return sessions[i].activity > sessions[j].activity
		}
		return sessions[i].name < sessions[j].name
	})

	// Build icon strings and compute column widths
	type rendered struct {
		sess *sessionData
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

		// Append claude state icon
		state := claudePriority(s.claude)
		if state != "" {
			icon := claudeStateIcon(state)
			stale := s.claude.allStale
			var cc string
			if stale {
				cc = dim
			} else {
				colors := claudeColors[theme]
				if hex, ok := colors[state]; ok {
					cc = ansiFg(hex)
				}
			}
			icons += cc + icon + reset + " "
			dw += 2
		}

		rows[i] = rendered{sess: &sessions[i], icons: icons, iconDW: dw}
		if dw > maxIconDW {
			maxIconDW = dw
		}
	}

	iconCol := maxIconDW + 1
	if iconCol < 5 { // at least as wide as "Procs" header
		iconCol = 5
	}

	// Pad icons to uniform width
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

	// Session rows
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

// collectSessions runs tmux list-panes -a and aggregates by session.
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

// collectClaude reads /tmp/claude-status/panes/* and returns per-session counts.
func collectClaude() map[string]*claudeCounts {
	dir := "/tmp/claude-status/panes"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	now := time.Now().Unix()
	result := make(map[string]*claudeCounts)

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

		cc, ok := result[session]
		if !ok {
			cc = &claudeCounts{allStale: true}
			result[session] = cc
		}

		stale := isStale(state, now, timestamp)
		if !stale {
			cc.allStale = false
		}

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
	return result
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

// buildProcIcons builds a space-separated icon string from process names.
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
func iconCellWidth(s string) int {
	w := 0
	for _, r := range s {
		if (r >= 0xE000 && r <= 0xF8FF) || r >= 0xF0000 {
			w++ // nerd font PUA = 1 cell
		} else if r > 0x7F {
			w += 2 // emoji/other = 2 cells
		} else {
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
	// Simple parse: find "theme":"light" or "theme":"dark"
	s := string(data)
	idx := strings.Index(s, `"theme"`)
	if idx < 0 {
		return "dark"
	}
	rest := s[idx+7:]
	// Skip whitespace and colon
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == ':' || rest[0] == '\n') {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == '"' {
		rest = rest[1:]
		if end := strings.IndexByte(rest, '"'); end > 0 {
			return rest[:end]
		}
	}
	return "dark"
}

// readTmuxOpts reads all global tmux user options in a single call.
func readTmuxOpts() map[string]string {
	out, err := exec.Command("tmux", "show", "-g").Output()
	if err != nil {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		// Format: "option-name value" or "@user_option \"value\""
		if i := strings.IndexByte(line, ' '); i > 0 {
			v := strings.TrimRight(line[i+1:], " \t\r")
			v = strings.Trim(v, "\"") // tmux wraps values in quotes
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

// Compat helpers (Go 1.21+ has these in stdlib, but being safe)
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure utf8 import is used (iconCellWidth iterates runes)
var _ = utf8.RuneLen
