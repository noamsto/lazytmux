package render

import (
	"fmt"
	"strings"

	"github.com/noamsto/tmux-og/generator/config"
	"github.com/noamsto/tmux-og/generator/paths"
)

// pickerIcons are the status-line and session-picker column glyphs. The four
// column headers are verified present in the pinned Nerd Font: md-server,
// md-apps, md-chip, md-memory. The window-flag glyphs are not here — they are
// baked into pluginConfigs, which is their only consumer.
type pickerIcons struct {
	Session string
	Branch  string
	Dir     string
	Remote  string
	Host    string
	Procs   string
	CPU     string
	Mem     string
}

var icons = pickerIcons{
	Session: "",
	Branch:  "",
	Dir:     "",
	Remote:  "",
	Host:    "",
	Procs:   "󰀻",
	CPU:     "",
	Mem:     "",
}

// oneZero is the CC plugin's dialect for @ai_naming, which its hook tests as a
// string. The resume flags next to it are tmux on/off options, so they take
// onOff instead; the two are not interchangeable.
func oneZero(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// enrichIconsDoubled is the tmux-format dialect of the enrich glyphs: '#' is
// doubled on user-supplied keys ONLY. Doubling the merged set would give a
// default glyph carrying '#' a second one the moment one is added.
func enrichIconsDoubled(cfg *config.Config) map[string]string {
	m := make(map[string]string, len(enrichIconDefaults))
	for k, v := range enrichIconDefaults {
		m[k] = v
	}
	for k, v := range cfg.Enrich.Icons {
		m[k] = strings.ReplaceAll(v, "#", "##")
	}
	return m
}

// bridgeOptNames are the window options a mirror carries twice: its own, stamped
// against the launcher's repo, and the daemon-shipped @bridge_* copy that
// describes the remote window actually on screen.
var bridgeOptNames = []string{
	"crew_name",
	"crew_color",
	"pr_number",
	"pr_state",
	"pr_check_state",
	"pr_mergeable",
}

// bridgeOpts is built per option name, not per site: each appears more than once
// in the window format. The commas inside are deliberately not '#,'-escaped —
// format_expand resolves the conditional before format_draw parses '#[…]'.
func bridgeOpts() map[string]string {
	m := make(map[string]string, len(bridgeOptNames))
	for _, n := range bridgeOptNames {
		m[n] = fmt.Sprintf("#{?#{@bridge_win},#{@bridge_%s},#{@%s}}", n, n)
	}
	return m
}

// processIcon reads the merged process-icon map, falling back to the emoji the
// usage segment uses when no glyph is configured for that agent.
func processIcon(cfg *config.Config, name, fallback string) string {
	if v, ok := cfg.ProcessIcons[name]; ok {
		return v
	}
	return fallback
}

// agentUsageArgs is the statusline argument group the usage segment needs, empty
// when the segment is off. It is spliced mid-line, so it carries its own leading
// space rather than one the template would emit unconditionally.
func agentUsageArgs(cfg *config.Config) string {
	if !cfg.AgentUsage.Enable {
		return ""
	}
	return fmt.Sprintf(" --icon-usage-claude '%s' --icon-usage-codex '%s' --icon-usage-cursor '%s' --agent-usage-monthly-threshold '%d'",
		processIcon(cfg, "claude", "🧠"),
		processIcon(cfg, "codex", "🤖"),
		processIcon(cfg, "cursor-agent", "🧊"),
		cfg.AgentUsage.MonthlyThreshold)
}

// tickHookNames is the CLEAR list, not the set list — the four setters below
// carry their names inline, so the two lists are deliberately different lengths.
// This one also carries the four legacy @lztmux-*-tick names: a rename reloads
// the config but does not restart the tmux server, and hooks_monitor_add keys on
// the name, so without their clears four orphaned monitors keep firing every
// five seconds at garbage-collected store paths for the life of the server. A
// clear removes a monitor and never keeps one working, so this is migration
// cleanup, not an alias.
//
// Emission order is load-bearing — new names first, legacy last, so every clear
// still precedes the first setter, which tick-floor-conf-assertions measures.
//
// Removal condition: drop the four legacy entries once every host's tmux server
// has restarted past the flip.
var tickHookNames = []string{
	"@og-pr-tick",
	"@og-backfill-tick",
	"@og-usage-tick",
	"@og-sweep-tick",
	"@lztmux-pr-tick",
	"@lztmux-backfill-tick",
	"@lztmux-usage-tick",
	"@lztmux-sweep-tick",
}

// tickHookIfShell arms the monitor-hook floor for the status-tick side effects
// (#603). A -B monitor's timer belongs to the monitor, not to a client, so these
// fire on a server with zero clients — the state of a control-only bridge host,
// where the status-format[0] jobs never ran at all.
//
// The clears are unconditional and come first: hooks_monitor_add keys on the
// name, so a reload replaces a hook, but disabling a feature never re-sets one —
// without them a rebuild with enrich off leaves the previous generation's
// monitor firing at a store path GC will remove.
func tickHookIfShell(cfg *config.Config, p *paths.Paths) string {
	// The target field is deliberately EMPTY ('<name>::<format>'): upstream
	// 557967c3 turned an unrecognised non-empty target into a parse failure, and
	// '::' is the one spelling both the pinned tmux and a later bump accept.
	// Divisor 5 matches arm_agent_detect's own every-5th-tick cadence.
	tick := func(name string) string {
		return name + "::#{e|/|:#{T:@og_tick},5}"
	}
	setHook := func(name, cmd string) string {
		return fmt.Sprintf(`set-hook -g -B '%s' 'run-shell -b "%s"'`, tick(name), cmd)
	}

	var parts []string
	for _, n := range tickHookNames {
		parts = append(parts,
			fmt.Sprintf("set-hook -g -u -B '%s'", n),
			fmt.Sprintf("set -gu '%s'", n))
	}
	if cfg.Enrich.Enable {
		parts = append(parts,
			setHook("@og-pr-tick", p.Scripts["tmux-pr-enrich"]+" --tick"),
			setHook("@og-backfill-tick", p.Scripts["tmux-issue-stamp"]+" --backfill"))
	}
	if cfg.AgentUsage.Enable {
		parts = append(parts, setHook("@og-usage-tick", p.Scripts["tmux-agent-usage"]+" --tick"))
	}
	// Unconditional, and arming only. OG_TICK_SWEEP=1 rather than a --sweep
	// argv flag: $1 is a session name at every other callsite, so a session
	// literally named "--sweep" would misroute itself forever.
	parts = append(parts, setHook("@og-sweep-tick", "OG_TICK_SWEEP=1 "+p.Scripts["tmux-update-icons"]))

	body := strings.ReplaceAll(strings.Join(parts, " \\; "), `"`, `\"`)
	return fmt.Sprintf(`if-shell "tmux list-commands set-hook | grep -q -- -B" "%s" "display-message 'tmux-og: tmux predates 3.8 -B session monitors -- PR/backfill/usage polling and the agent sweep only run while a real client has this session attached'"`, body)
}
