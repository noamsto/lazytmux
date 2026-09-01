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
