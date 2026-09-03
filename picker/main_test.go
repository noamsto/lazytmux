package main

import (
	"strings"
	"testing"
)

func TestAgentPriority(t *testing.T) {
	cases := []struct {
		name string
		c    agentCounts
		want string
	}{
		{"error wins over everything", agentCounts{errorCnt: 1, waiting: 1, done: 1}, "error"},
		{"waiting beats denied/compacting/processing/done/idle",
			agentCounts{waiting: 1, denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "waiting"},
		{"denied beats compacting/processing/done/idle",
			agentCounts{denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "denied"},
		{"compacting beats processing/done/idle",
			agentCounts{compacting: 1, processing: 1, done: 1, idle: 1}, "compacting"},
		{"processing beats done/idle", agentCounts{processing: 1, done: 1, idle: 1}, "processing"},
		{"done beats idle", agentCounts{done: 1, idle: 1}, "done"},
		{"idle alone", agentCounts{idle: 1}, "idle"},
		{"all zero", agentCounts{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentPriority(c.c); got != c.want {
				t.Errorf("agentPriority(%+v) = %q, want %q", c.c, got, c.want)
			}
		})
	}
}

func TestAgentStateOrderMatchesPriority(t *testing.T) {
	// agentStateOrder is what groupWindowsByState (Task 2) walks to decide
	// header order; it must name every state agentPriority can return, in
	// the same order, or the two would silently diverge.
	if len(agentStateOrder) != 7 {
		t.Fatalf("agentStateOrder has %d entries, want 7 (error/waiting/denied/compacting/processing/done/idle)",
			len(agentStateOrder))
	}
	want := []string{"error", "waiting", "denied", "compacting", "processing", "done", "idle"}
	for i, s := range want {
		if agentStateOrder[i] != s {
			t.Errorf("agentStateOrder[%d] = %q, want %q", i, agentStateOrder[i], s)
		}
	}
}

// A remote window name reaches us through @window_bridge_name in the daemon's
// escaped form, because the status line collapses the doubling when it draws.
// The picker draws its own rows, so it has to undo the escape first.
func TestDecodeBridgeName(t *testing.T) {
	cases := map[string]string{
		"pr##367":               "pr#367",
		"a####b":                "a##b",
		"plain-name":            "plain-name",
		"":                      "",
		"[nix-amd-ai 󰪣 󰘭 ##46]": "[nix-amd-ai 󰪣 󰘭 #46]",
	}
	for in, want := range cases {
		if got := decodeBridgeName(in); got != want {
			t.Errorf("decodeBridgeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionHeaderLabelsAndAlignment(t *testing.T) {
	snap := panesSnapshot{
		"%1|lazytmux|0|/home/noams/git/lazytmux|1900000300||fish|1",
		"%2|tp-g6-money|0|/home/noams/src|1900000200|tp-g6|fish|1",
	}
	items := buildSessionItems(nil, snap, nil, "dark", false)
	hdr := items[0]
	if !hdr.isColumnHeader {
		t.Fatal("items[0] must be flagged as the column header for the sticky pin")
	}
	for _, word := range []string{"Session", "Host", "Procs", "CPU", "Mem", "Path"} {
		if !strings.Contains(hdr.plain, word) {
			t.Errorf("header is missing the label %q: %q", word, hdr.plain)
		}
	}
	for _, glyph := range []string{iconHost, iconProcs, iconCPU, iconMem} {
		if !strings.Contains(hdr.plain, glyph) {
			t.Errorf("header is missing glyph %q: %q", glyph, hdr.plain)
		}
	}
	// CPU and Mem must be distinguishable, not the same glyph twice.
	if iconCPU == iconMem {
		t.Error("CPU and Mem share a glyph")
	}
	// The Host column has to start at the same cell in the header and in a row,
	// which len()-based padding would get wrong: a glyph is 1 cell, 4 bytes.
	col := func(s, needle string) int { return visibleWidth(s[:strings.Index(s, needle)]) }
	if h, r := col(hdr.plain, iconHost), col(items[2].plain, "tp-g6 "); h != r {
		t.Errorf("host column starts at %d in the header but %d in the row", h, r)
	}
	// The CPU and Mem labels mirror each other around the separator: CPU ends on
	// its left, Mem starts on its right, reading as one CPU / Mem unit.
	if !strings.Contains(hdr.plain, "CPU / "+iconMem) {
		t.Errorf("CPU and Mem labels should flank the separator: %q", hdr.plain)
	}
	// Both header and rows must agree on where the resource field ends, or Path
	// drifts: the label moving left may not change the field's width.
	if h, r := col(hdr.plain, " / "), col(items[1].plain, " / "); h != r {
		t.Errorf("the ' / ' sits at %d in the header but %d in a row", h, r)
	}
	if h, r := col(hdr.plain, iconDir), col(items[1].plain, iconDir); h != r {
		t.Errorf("path column starts at %d in the header but %d in the row", h, r)
	}
}

func TestUnquoteTmuxOptValue(t *testing.T) {
	cases := map[string]string{
		`''`:              "",          // tmux's rendering of an option set to ""
		`""`:              "",          //
		`"tp-g6 mbp"`:     "tp-g6 mbp", // quoted because it holds a space
		`tp-g6`:           "tp-g6",     // bare, no quoting needed
		`'tp-g6 mbp'`:     "tp-g6 mbp",
		`"unbalanced`:     `"unbalanced`, // no matched pair: leave it alone
		`ends"`:           `ends"`,
		`"outer 'inner'"`: `outer 'inner'`, // only the one outer pair comes off
	}
	for in, want := range cases {
		if got := unquoteTmuxOptValue(in); got != want {
			t.Errorf("unquoteTmuxOptValue(%s) = %q, want %q", in, got, want)
		}
	}
}

// windowPaneRow builds one list-panes -a row in parseWindowPaneRows' field
// order (see collectWindows' -F string), for tests below.
func windowPaneRow(fields ...string) string {
	const n = 29
	row := make([]string, n)
	copy(row, fields)
	return strings.Join(row, "|")
}

func TestParseWindowPaneRowsBridgeIdentity(t *testing.T) {
	// A bridge row takes the bridge copies wholesale — including over a
	// window carrying its own (launcher-residue) label/crew/PR fields — and
	// clears branch, which is what arms and then disarms collectWindows' git
	// fallback.
	row := windowPaneRow(
		"sess", "0", "winname", "0", "fish", "1", "should-be-cleared", "/some/path",
		"LOCAL-1", " local title", " pr-local", "open", "pending", "mergeable",
		"local-crew", "colour0", "", "", "",
		"1", "raven", "colour1", "BRIDGE-1", " bridge title",
		" pr-bridge", "closed", "success", "conflicting",
	)
	order, m := parseWindowPaneRows([]string{row})
	if len(order) != 1 {
		t.Fatalf("got %d windows, want 1", len(order))
	}
	wi := m[order[0]]
	if !wi.bridgeWin {
		t.Fatal("bridgeWin = false, want true")
	}
	if wi.branch != "" {
		t.Errorf("branch = %q, want cleared", wi.branch)
	}
	if wi.crewName != "raven" || wi.crewColor != "colour1" {
		t.Errorf("crew = %q/%q, want raven/colour1", wi.crewName, wi.crewColor)
	}
	if wi.labelID != "BRIDGE-1" || wi.labelRest != " bridge title" {
		t.Errorf("label = %q/%q, want BRIDGE-1/ bridge title", wi.labelID, wi.labelRest)
	}
	if wi.prPlain != " pr-bridge" || wi.prState != "closed" || wi.prCheck != "success" || wi.prMergeable != "conflicting" {
		t.Errorf("pr = %q/%q/%q/%q, want bridge values", wi.prPlain, wi.prState, wi.prCheck, wi.prMergeable)
	}
}

func TestParseWindowPaneRowsBridgeHost(t *testing.T) {
	row := windowPaneRow(
		"sess", "0", "winname", "0", "fish", "1", "feature/x", "/some/path",
		"", "", "", "", "", "",
		"", "", "", "", "",
		"", "", "", "", "",
		"", "", "", "",
		"tp-g6",
	)
	order, m := parseWindowPaneRows([]string{row})
	wi := m[order[0]]
	if wi.bridgeHost != "tp-g6" {
		t.Errorf("bridgeHost = %q, want tp-g6", wi.bridgeHost)
	}
}

func TestParseWindowPaneRowsLocalUnchanged(t *testing.T) {
	// A non-bridge row (@bridge_win empty) must ignore the bridge fields
	// entirely, even when tmux hands back garbage in them.
	row := windowPaneRow(
		"sess", "0", "winname", "0", "fish", "1", "feature/x", "/some/path",
		"LIN-1", " local title", " pr-local", "open", "pending", "mergeable",
		"local-crew", "colour0", "", "", "",
		"", "garbage-crew", "garbage-color", "GARBAGE-1", " garbage title",
		" garbage-pr", "garbage", "garbage", "garbage",
	)
	order, m := parseWindowPaneRows([]string{row})
	wi := m[order[0]]
	if wi.bridgeWin {
		t.Fatal("bridgeWin = true, want false")
	}
	if wi.branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", wi.branch)
	}
	if wi.labelID != "LIN-1" || wi.crewName != "local-crew" {
		t.Errorf("label/crew = %q/%q, want local's own values, not the bridge fields", wi.labelID, wi.crewName)
	}
}

func TestParseWindowPaneRowsBridgeEmptyIDFallsBackToRest(t *testing.T) {
	// A remote window on a branch with no detected issue has an empty bridge
	// id but a non-empty rest — bridgeName must take that rest raw, not
	// through decodeBridgeName (which would mangle a '#' inside it).
	row := windowPaneRow(
		"sess", "0", "winname", "0", "fish", "1", "", "/some/path",
		"", "", "", "", "", "",
		"", "", "", "", "",
		"1", "raven", "colour1", "", " feature title",
		"", "", "", "",
	)
	order, m := parseWindowPaneRows([]string{row})
	wi := m[order[0]]
	if wi.labelID != "" {
		t.Fatalf("labelID = %q, want empty", wi.labelID)
	}
	if wi.bridgeName != " feature title" {
		t.Errorf("bridgeName = %q, want the raw bridge rest", wi.bridgeName)
	}
}

func TestParseWindowPaneRowsBareBridgeKeepsDecodedWindowName(t *testing.T) {
	// A bare mirror (no bridge id, no bridge rest) falls back to the decoded
	// @window_bridge_name. Reflow stamps the '#'-doubled name into
	// @window_label_rest_long on a bare mirror too, so this fixture carries
	// "feat##1" in BOTH fields — only sourcing bridgeName from the bridge
	// copy (not reflow's) catches a wrong implementation that would render
	// "feat##1" undecoded instead of "feat#1".
	row := windowPaneRow(
		"sess", "0", "winname", "0", "fish", "1", "", "/some/path",
		"", "feat##1", "", "", "", "",
		"", "", "feat##1", "", "",
		"1", "", "", "", "",
		"", "", "", "",
	)
	order, m := parseWindowPaneRows([]string{row})
	wi := m[order[0]]
	if wi.labelID != "" || wi.labelRest != "" {
		t.Fatalf("labelID/labelRest = %q/%q, want both empty", wi.labelID, wi.labelRest)
	}
	if wi.bridgeName != "feat#1" {
		t.Errorf("bridgeName = %q, want feat#1 (decoded)", wi.bridgeName)
	}
}

func TestEmptyRemoteHostsOptionYieldsNoSection(t *testing.T) {
	// The '' form reached parseRemoteHosts as a one-element list, so a user with
	// no remote hosts configured got a Remote section holding a host named ''.
	if got := parseRemoteHosts(unquoteTmuxOptValue(`''`)); got != nil {
		t.Errorf("got %q, want no hosts", got)
	}
	if got := pendingRemoteItems(map[string]string{"@remote_bridge_hosts": unquoteTmuxOptValue(`''`)}); got != nil {
		t.Errorf("got %d rows, want no Remote section", len(got))
	}
}
