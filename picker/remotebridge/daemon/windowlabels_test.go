package daemon

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseWindowLabels(t *testing.T) {
	body := strings.Join([]string{
		"@1|nova|#89b4fa|123|open|success|mergeable| PR #123|GH #460| ship it",
		"@2|orbit|#[fg=red]|none|OPEN|success|unknown||| a #[fg=red]title | with a pipe",
		"@3|zephyr", // trailing empty fields may not survive the trip
		"",          // blank line
	}, "\n")

	got := parseWindowLabels(body)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}

	want0 := labelRow{
		id: "@1", crewName: "nova", crewColor: "#89b4fa", prNumber: "123",
		prState: "open", prCheck: "success", prMergeable: "mergeable",
		// The leading space of @window_pr_plain is load-bearing for reflow's
		// pr_colw padding, so nothing trims it.
		prPlain: " PR #123", labelID: "GH #460", labelRest: " ship it",
	}
	if got[0] != want0 {
		t.Errorf("row 0 = %+v, want %+v", got[0], want0)
	}

	// The free-form field is last, so a '|' inside it lands there and shifts
	// nothing: the enum fields either side still read correctly.
	want1 := labelRow{
		id: "@2", crewName: "orbit", prCheck: "success", prMergeable: "unknown",
		labelRest: " a title  with a pipe",
	}
	if got[1] != want1 {
		t.Errorf("row 1 = %+v, want %+v", got[1], want1)
	}

	if (got[2] != labelRow{id: "@3", crewName: "zephyr"}) {
		t.Errorf("short row = %+v", got[2])
	}
}

// oneRow parses a single-window body whose field i holds v.
func oneRow(t *testing.T, i int, v string) labelRow {
	t.Helper()
	fields := make([]string, 10)
	fields[0] = "@1"
	fields[i] = v
	rows := parseWindowLabels(strings.Join(fields, "|"))
	if len(rows) != 1 {
		t.Fatalf("parseWindowLabels(%q) = %d rows, want 1", v, len(rows))
	}
	return rows[0]
}

func TestWindowLabelValidation(t *testing.T) {
	colors := []struct{ in, want string }{
		{"#89b4fa", "#89b4fa"},
		{"#89B4FA", "#89B4FA"}, // ansiFg takes either case; lowercase-only would drop it to the fallback
		{"colour42", "colour42"},
		{"colour256", ""},
		{"red", "red"},
		{"#[fg=red]", ""},
		{"#89b4f", ""},
	}
	for _, c := range colors {
		if got := oneRow(t, 2, c.in).crewColor; got != c.want {
			t.Errorf("crewColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	numbers := []struct{ in, want string }{{"123", "123"}, {"none", ""}, {"12a", ""}}
	for _, n := range numbers {
		if got := oneRow(t, 3, n.in).prNumber; got != n.want {
			t.Errorf("prNumber(%q) = %q, want %q", n.in, got, n.want)
		}
	}

	states := []struct{ in, want string }{{"open", "open"}, {"OPEN", ""}, {"open pr", ""}, {"", ""}}
	for _, s := range states {
		if got := oneRow(t, 4, s.in).prState; got != s.want {
			t.Errorf("prState(%q) = %q, want %q", s.in, got, s.want)
		}
	}

	// Caps count runes, so one pathological remote value cannot dominate a column.
	if got := []rune(oneRow(t, 1, strings.Repeat("é", 200)).crewName); len(got) != crewNameMaxRunes {
		t.Errorf("crewName capped to %d runes, want %d", len(got), crewNameMaxRunes)
	}
	if got := []rune(oneRow(t, 9, strings.Repeat("x", 300)).labelRest); len(got) != labelTextMaxRunes {
		t.Errorf("labelRest capped to %d runes, want %d", len(got), labelTextMaxRunes)
	}

	// LocalTmux execs without a shell, so tmux's own args_parse would read this
	// as a flag.
	if got := oneRow(t, 9, "-n oops").labelRest; got != "" {
		t.Errorf("flag-shaped value = %q, want it dropped", got)
	}
}

func TestLabelShipperApply(t *testing.T) {
	reg := newRegistry()
	reg.add("@1", "@101")
	s := newLabelShipper()
	var calls [][]string
	cfg := mirrorCfg(&calls)

	row := labelRow{id: "@1", crewName: "nova", crewColor: "#89b4fa", labelID: "GH #460", labelRest: " ship it"}
	if !s.apply(cfg, reg, []labelRow{row, {id: "@9", crewName: "ghost"}}) {
		t.Fatal("first pass should report a change")
	}
	if len(calls) != 1 {
		t.Fatalf("first pass = %d tmux calls, want one argv sequence: %v", len(calls), calls)
	}
	first := strings.Join(calls[0], " ")
	if n := strings.Count(first, "set-option"); n != len(bridgeLabelOptions) {
		t.Errorf("first pass wrote %d options, want all %d: %q", n, len(bridgeLabelOptions), first)
	}
	if n := strings.Count(first, " ; "); n != len(bridgeLabelOptions)-1 {
		t.Errorf("commands are not one ';'-joined sequence: %q", first)
	}
	if !strings.Contains(first, "set-option -w -t @101 @bridge_crew_name nova") {
		t.Errorf("carried value not stamped: %q", first)
	}
	// An absent remote value unsets rather than stamping "".
	if !strings.Contains(first, "set-option -w -t @101 -u @bridge_pr_number") {
		t.Errorf("empty value not unset: %q", first)
	}
	if strings.Contains(first, "ghost") {
		t.Errorf("a row with no mirror window was stamped: %q", first)
	}

	if s.apply(cfg, reg, []labelRow{row}) {
		t.Error("an unchanged row reported a change")
	}
	if len(calls) != 1 {
		t.Errorf("an unchanged row issued tmux calls: %v", calls[1:])
	}

	row.crewName = "orbit"
	s.apply(cfg, reg, []labelRow{row})
	want := []string{"set-option", "-w", "-t", "@101", "@bridge_crew_name", "orbit"}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], want) {
		t.Errorf("changed field wrote %v, want only %v", calls[1:], want)
	}

	row.crewColor = ""
	s.apply(cfg, reg, []labelRow{row})
	want = []string{"set-option", "-w", "-t", "@101", "-u", "@bridge_crew_color"}
	if len(calls) != 3 || !reflect.DeepEqual(calls[2], want) {
		t.Errorf("emptied field wrote %v, want only %v", calls[2:], want)
	}

	// Forgetting is keyed on the registry, not on absence from the reply.
	reg.remove("@1")
	s.apply(cfg, reg, nil)
	if len(s.written) != 0 {
		t.Errorf("written = %v, want the departed window forgotten", s.written)
	}
}

// Teardown ends in kill-session, so clear is near-vacuous in production and
// this is the only place it is provable.
func TestLabelShipperClear(t *testing.T) {
	reg := newRegistry()
	reg.add("@1", "@101")
	s := newLabelShipper()
	var calls [][]string
	cfg := mirrorCfg(&calls)
	s.apply(cfg, reg, []labelRow{{id: "@1", crewName: "nova"}})

	calls = nil
	s.clear(cfg, reg)
	if len(calls) != 1 {
		t.Fatalf("clear = %d tmux calls, want one argv sequence: %v", len(calls), calls)
	}
	got := strings.Join(calls[0], " ")
	for _, o := range bridgeLabelOptions {
		if !strings.Contains(got, "-t @101 -u "+o.opt) {
			t.Errorf("clear left %s set: %q", o.opt, got)
		}
	}
	if len(s.written) != 0 {
		t.Errorf("written = %v, want empty", s.written)
	}
}

// retireMirror rebuilds a dead mirror through closeWindow + reconcileWindows,
// which re-adds the SAME remote id against a fresh local window. The row is
// unchanged across that, so only the local target tells the shipper it must
// stamp again — otherwise the replacement window renders bare forever.
func TestLabelShipperRestampsRebuiltMirror(t *testing.T) {
	reg := newRegistry()
	reg.add("@1", "@101")
	s := newLabelShipper()
	var calls [][]string
	cfg := mirrorCfg(&calls)

	row := labelRow{id: "@1", crewName: "nova", labelID: "GH #460"}
	s.apply(cfg, reg, []labelRow{row})

	reg.remove("@1")
	reg.add("@1", "@102")
	calls = nil
	if !s.apply(cfg, reg, []labelRow{row}) {
		t.Fatal("a rebuilt mirror reported no change")
	}
	if len(calls) != 1 {
		t.Fatalf("rebuilt mirror = %d tmux calls, want one argv sequence: %v", len(calls), calls)
	}
	got := strings.Join(calls[0], " ")
	if n := strings.Count(got, "set-option"); n != len(bridgeLabelOptions) {
		t.Errorf("rebuilt mirror wrote %d options, want all %d: %q", n, len(bridgeLabelOptions), got)
	}
	if strings.Contains(got, "@101") {
		t.Errorf("stamped the dead local window: %q", got)
	}
	if !strings.Contains(got, "set-option -w -t @102 @bridge_crew_name nova") {
		t.Errorf("replacement window not stamped: %q", got)
	}
}
