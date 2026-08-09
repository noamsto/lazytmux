package main

import "testing"

func TestClaudePriority(t *testing.T) {
	cases := []struct {
		name string
		c    claudeCounts
		want string
	}{
		{"error wins over everything", claudeCounts{errorCnt: 1, waiting: 1, done: 1}, "error"},
		{"waiting beats denied/compacting/processing/done/idle",
			claudeCounts{waiting: 1, denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "waiting"},
		{"denied beats compacting/processing/done/idle",
			claudeCounts{denied: 1, compacting: 1, processing: 1, done: 1, idle: 1}, "denied"},
		{"compacting beats processing/done/idle",
			claudeCounts{compacting: 1, processing: 1, done: 1, idle: 1}, "compacting"},
		{"processing beats done/idle", claudeCounts{processing: 1, done: 1, idle: 1}, "processing"},
		{"done beats idle", claudeCounts{done: 1, idle: 1}, "done"},
		{"idle alone", claudeCounts{idle: 1}, "idle"},
		{"all zero", claudeCounts{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudePriority(c.c); got != c.want {
				t.Errorf("claudePriority(%+v) = %q, want %q", c.c, got, c.want)
			}
		})
	}
}

func TestClaudeStateOrderMatchesPriority(t *testing.T) {
	// claudeStateOrder is what groupWindowsByState (Task 2) walks to decide
	// header order; it must name every state claudePriority can return, in
	// the same order, or the two would silently diverge.
	if len(claudeStateOrder) != 7 {
		t.Fatalf("claudeStateOrder has %d entries, want 7 (error/waiting/denied/compacting/processing/done/idle)",
			len(claudeStateOrder))
	}
	want := []string{"error", "waiting", "denied", "compacting", "processing", "done", "idle"}
	for i, s := range want {
		if claudeStateOrder[i] != s {
			t.Errorf("claudeStateOrder[%d] = %q, want %q", i, claudeStateOrder[i], s)
		}
	}
}
