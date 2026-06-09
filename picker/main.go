// tmux-picker-generate runs the bubbletea session/window picker TUI.
//
// Usage:
//
//	tmux-picker-generate --tui              # session picker
//	tmux-picker-generate --tui --windows    # window picker (add --claude to filter)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
)

// Build-time constants injected via icons_generated.go:
//   iconMap, fallbackIcon, maxIconsPicker
//   claudeSpinnerFrames, claudeIconWaiting, claudeIconCompacting,
//   claudeIconDone, claudeIconIdle, claudeIconError, claudeIconDenied
//   iconSession, iconDir, iconBranch (defaults, overridden by env/tmux)

// Staleness thresholds (seconds)
const (
	staleWaiting    = 30
	staleCompacting = 60
	staleProcessing = 300
	staleDone       = 60
	staleError      = 120
	staleDenied     = 60
)

// Catppuccin hex colors per theme
var claudeColors = map[string]map[string]string{
	"dark": {
		"waiting": "#fab387", "compacting": "#89dceb", "processing": "#94e2d5",
		"done": "#a6e3a1", "idle": "#6c7086", "error": "#f38ba8", "denied": "#f9e2af",
	},
	"light": {
		"waiting": "#fe640b", "compacting": "#04a5e5", "processing": "#179299",
		"done": "#40a02b", "idle": "#6c6f85", "error": "#d20f39", "denied": "#df8e1d",
	},
}

type sessionData struct {
	name     string
	path     string
	activity int64
	procs    []string // unique process names
	panePIDs []int    // shell PIDs for resource collection
	claude   claudeCounts
	cpuPct   float64 // total CPU% across all descendant processes
	memMB    float64 // total RSS in MiB across all descendant processes
}

type windowData struct {
	session string
	index   int
	name    string
	zoomed  bool
	branch  string
	active  bool // currently active window in its session
	procs   []string
	claude  claudeCounts
}

type claudeCounts struct {
	waiting, compacting, processing, done, idle, errorCnt, denied int
	allStale                                                      bool
	anyUnseen                                                     bool
	issues                                                        []string // union of self-reported issue ids
}

// claudePaneInfo holds parsed pane file data with window-level targeting.
type claudePaneInfo struct {
	session string
	winIdx  int
	state   string
	ts      int64
	stale   bool
	unseen  bool
	issues  []string
}

func main() {
	args := map[string]bool{}
	for _, a := range os.Args[1:] {
		args[a] = true
	}
	if err := runTUI(args["--windows"], args["--claude"]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Data collection
// ---------------------------------------------------------------------------

func collectSessions() []sessionData {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}\t#{session_path}\t#{session_last_attached}\t#{pane_current_command}\t#{pane_pid}").Output()
	if err != nil {
		return nil
	}

	type sessInfo struct {
		path     string
		activity int64
		seen     map[string]bool
		procs    []string
		panePIDs []int
	}
	m := make(map[string]*sessInfo)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}
		name, path, actStr, proc := parts[0], parts[1], parts[2], parts[3]
		// Expand %h (tmux may store literal %h for home dir)
		if home := os.Getenv("HOME"); home != "" {
			path = strings.Replace(path, "%h", home, 1)
		}
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
		if pid, err := strconv.Atoi(parts[4]); err == nil && pid > 0 {
			si.panePIDs = append(si.panePIDs, pid)
		}
	}

	sessions := make([]sessionData, 0, len(m))
	for name, si := range m {
		sessions = append(sessions, sessionData{
			name:     name,
			path:     si.path,
			activity: si.activity,
			procs:    si.procs,
			panePIDs: si.panePIDs,
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
		"#{session_name}\t#{window_index}\t#{b:pane_current_path}\t#{window_zoomed_flag}\t#{pane_current_command}\t#{window_active}\t#{@branch}\t#{pane_current_path}").Output()
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
		path   string // pane_current_path for git branch fallback
		seen   map[string]bool
		procs  []string
	}
	m := make(map[winKey]*winInfo)
	// Preserve ordering
	var order []winKey

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 8)
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
		panePath := ""
		if len(parts) >= 8 {
			panePath = parts[7]
		}

		k := winKey{sess, idx}
		wi, ok := m[k]
		if !ok {
			wi = &winInfo{name: wName, zoomed: zoomed, active: active, branch: branch, path: panePath, seen: make(map[string]bool)}
			m[k] = wi
			order = append(order, k)
		}
		if proc != "" && !wi.seen[proc] {
			wi.seen[proc] = true
			wi.procs = append(wi.procs, proc)
		}
	}

	// Fill missing branches by running git in each pane's working directory.
	// Parallel: one git fork per window, all in flight at once — the picker's
	// first paint waits on the slowest single call, not their sum. Each goroutine
	// writes a distinct *winInfo, so no shared-state guard is needed.
	var wg sync.WaitGroup
	for _, k := range order {
		wi := m[k]
		if wi.branch == "" && wi.path != "" {
			wg.Add(1)
			go func(wi *winInfo) {
				defer wg.Done()
				if out, err := exec.Command("git", "-C", wi.path, "branch", "--show-current").Output(); err == nil {
					wi.branch = strings.TrimSpace(string(out))
				}
			}(wi)
		}
	}
	wg.Wait()

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
// Resource usage (CPU + memory per session)
// ---------------------------------------------------------------------------

// sessionResources holds aggregated CPU and memory for a session.
type sessionResources struct {
	cpuPct float64
	memMB  float64
}

// resourceCache holds the last result to avoid re-running ps every 1s tick.
var resourceCache struct {
	sync.Mutex
	result map[string]sessionResources
	ts     time.Time
}

const resourceCacheTTL = 5 * time.Second

// collectSessionResources returns per-session CPU% and RSS by walking the
// process tree from each tmux pane PID. Runs ps once and builds a parent→children
// map to sum all descendants. Results are cached for 5s since resource data
// changes slowly relative to the 1s TUI refresh rate.
func collectSessionResources(sessions []sessionData) map[string]sessionResources {
	resourceCache.Lock()
	if time.Since(resourceCache.ts) < resourceCacheTTL && resourceCache.result != nil {
		cached := resourceCache.result
		resourceCache.Unlock()
		return cached
	}
	resourceCache.Unlock()

	sessionPIDs := make(map[string][]int, len(sessions))
	for _, s := range sessions {
		if len(s.panePIDs) > 0 {
			sessionPIDs[s.name] = s.panePIDs
		}
	}

	psOut, err := exec.Command("ps", "-eo", "pid,ppid,pcpu,rss", "--no-headers").Output()
	if err != nil {
		return nil
	}

	children := make(map[int][]int)
	type procInfo struct {
		cpu float64
		rss int64 // KiB
	}
	procs := make(map[int]*procInfo)

	for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		rss, _ := strconv.ParseInt(fields[3], 10, 64)
		if pid <= 0 {
			continue
		}
		procs[pid] = &procInfo{cpu: cpu, rss: rss}
		children[ppid] = append(children[ppid], pid)
	}

	result := make(map[string]sessionResources, len(sessionPIDs))
	for sess, pids := range sessionPIDs {
		var totalCPU float64
		var totalRSS int64
		for _, root := range pids {
			queue := []int{root}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				if p, ok := procs[cur]; ok {
					totalCPU += p.cpu
					totalRSS += p.rss
				}
				queue = append(queue, children[cur]...)
			}
		}
		result[sess] = sessionResources{
			cpuPct: totalCPU,
			memMB:  float64(totalRSS) / 1024.0,
		}
	}

	resourceCache.Lock()
	resourceCache.result = result
	resourceCache.ts = time.Now()
	resourceCache.Unlock()

	return result
}

func mergeResources(sessions []sessionData, res map[string]sessionResources) {
	for i := range sessions {
		if r, ok := res[sessions[i].name]; ok {
			sessions[i].cpuPct = r.cpuPct
			sessions[i].memMB = r.memMB
		}
	}
}

// formatCPU returns a compact CPU% string.
func formatCPU(cpuPct float64) string {
	return fmt.Sprintf("%.0f%%", cpuPct)
}

// cpuColWidth returns the reserved column width for CPU%, sized to the
// worst-case value (numCPU * 100%) so the column doesn't shift between
// renders when usage crosses a digit boundary (e.g. 99% → 300%).
func cpuColWidth() int {
	return len(formatCPU(numCPU * 100))
}

// formatMem returns a compact memory string (M or G).
func formatMem(memMB float64) string {
	if memMB < 1024 {
		return fmt.Sprintf("%.0fM", memMB)
	}
	return fmt.Sprintf("%.1fG", memMB/1024.0)
}

// resourceColors holds themed ANSI color codes for resource level coloring.
type resourceColors struct {
	low, med, high, crit string
}

func newResourceColors(tmuxOpts map[string]string) resourceColors {
	return resourceColors{
		low:  ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8")),
		med:  ansiFg(envOrMap("THM_YELLOW", tmuxOpts, "@thm_yellow", "#f9e2af")),
		high: ansiFg(envOrMap("THM_PEACH", tmuxOpts, "@thm_peach", "#fab387")),
		crit: ansiFg(envOrMap("THM_RED", tmuxOpts, "@thm_red", "#f38ba8")),
	}
}

var numCPU = float64(runtime.NumCPU())

// cpuColor thresholds scale with core count:
// low: <10% of total, med: <25%, high: <60%, crit: ≥60%
func (rc resourceColors) cpuColor(pct float64) string {
	ratio := pct / (numCPU * 100)
	switch {
	case ratio < 0.10:
		return rc.low
	case ratio < 0.25:
		return rc.med
	case ratio < 0.60:
		return rc.high
	default:
		return rc.crit
	}
}

// memColor thresholds are absolute — memory pressure is memory pressure.
func (rc resourceColors) memColor(mb float64) string {
	switch {
	case mb < 256:
		return rc.low
	case mb < 1024:
		return rc.med
	case mb < 4096:
		return rc.high
	default:
		return rc.crit
	}
}

// ---------------------------------------------------------------------------
// Claude status
// ---------------------------------------------------------------------------

func collectClaudePanes() []claudePaneInfo {
	dir := "/tmp/claude-status/panes"
	issuesDir := "/tmp/claude-status/issues"
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
		var unseen bool
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				switch k {
				case "state":
					state = v
				case "session":
					session = v
				case "timestamp":
					timestamp, _ = strconv.ParseInt(v, 10, 64)
				case "unseen":
					unseen = v == "1"
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
			unseen:  unseen,
			issues:  readPaneIssues(filepath.Join(issuesDir, e.Name())),
		})
	}
	return result
}

// readPaneIssues reads the comma-separated self-reported issue id list for a
// pane. Missing file (the common case) yields nil.
func readPaneIssues(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(line, ",") {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
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
		if p.unseen {
			cc.anyUnseen = true
		}
		addClaudeState(cc, p.state)
		addIssues(cc, p.issues)
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
		if p.unseen {
			cc.anyUnseen = true
		}
		addClaudeState(cc, p.state)
		addIssues(cc, p.issues)
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
	case "denied":
		cc.denied++
	}
}

func addIssues(cc *claudeCounts, ids []string) {
	for _, id := range ids {
		seen := false
		for _, e := range cc.issues {
			if e == id {
				seen = true
				break
			}
		}
		if !seen {
			cc.issues = append(cc.issues, id)
		}
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
	case "denied":
		return age > staleDenied
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
	if c.denied > 0 {
		return "denied"
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
	case "denied":
		return claudeIconDenied
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
	// Dim stale icons, but unseen overrides — stay bright until user looks
	if cc.allStale && !cc.anyUnseen {
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

func formatIssueIDs(ids []string, limit int) string {
	if len(ids) == 0 {
		return ""
	}
	if len(ids) <= limit {
		return strings.Join(ids, " ")
	}
	return fmt.Sprintf("%s +%d", strings.Join(ids[:limit], " "), len(ids)-limit)
}

// appendIssueIDs appends a dim "ENG-1 GH-2 +N" segment to the icons string.
// Ids are validated ASCII ([A-Za-z0-9_-]), so len() is the display width.
func appendIssueIDs(icons string, dw int, ids []string, cDim, reset string) (string, int) {
	s := formatIssueIDs(ids, 2)
	if s == "" {
		return icons, dw
	}
	return icons + cDim + s + reset + " ", dw + len(s) + 1
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

// runeCellWidth returns the display width of a single rune, handling nerd font
// PUA (1 cell) and deferring to go-runewidth for everything else.
func runeCellWidth(r rune) int {
	switch {
	case r == 0xFE0E || r == 0xFE0F: // variation selectors
		return 0
	case r == 0x200D: // zero-width joiner
		return 0
	case r >= 0x0300 && r <= 0x036F: // combining diacritical marks
		return 0
	case r >= 0x20D0 && r <= 0x20FF: // combining marks for symbols
		return 0
	case (r >= 0xE000 && r <= 0xF8FF) || r >= 0xF0000:
		return 1 // nerd font PUA = 1 cell
	default:
		return runewidth.RuneWidth(r)
	}
}

// iconCellWidth returns the display width of an icon string.
// VS16 emoji are stripped at build time (process-icons.nix) to avoid lipgloss
// width miscalculation (charmbracelet/lipgloss#55, #562).
func iconCellWidth(s string) int {
	runes := []rune(s)
	w := 0
	for _, r := range runes {
		w += runeCellWidth(r)
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

func ansiBg(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return ""
	}
	r, _ := strconv.ParseUint(hex[0:2], 16, 8)
	g, _ := strconv.ParseUint(hex[2:4], 16, 8)
	b, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
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

// logEvent fires the lazytmux-log-event CLI (best-effort, never blocks the UI).
// Bare-name exec relies on the tmux wrapper's PATH, like our tmux/zoxide calls.
// No-ops when debug is off (the CLI checks the sentinel).
func logEvent(args ...string) {
	exec.Command("lazytmux-log-event", args...).Run() //nolint:errcheck
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
