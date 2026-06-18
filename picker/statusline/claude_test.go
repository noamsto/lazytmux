package main

import (
	"os"
	"testing"
)

func TestPaletteSelectsByTheme(t *testing.T) {
	dark := claudePalette("dark")
	if dark.waiting != "#fab387" {
		t.Fatalf("dark waiting = %q, want #fab387", dark.waiting)
	}
	light := claudePalette("light")
	if light.waiting != "#fe640b" {
		t.Fatalf("light waiting = %q, want #fe640b", light.waiting)
	}
}

func TestFadeHexEndpoints(t *testing.T) {
	if got := fadeHex("#000000", "#ffffff", 0); got != "#000000" {
		t.Fatalf("pct 0 = %q, want #000000", got)
	}
	if got := fadeHex("#000000", "#ffffff", 100); got != "#ffffff" {
		t.Fatalf("pct 100 = %q, want #ffffff", got)
	}
	if got := fadeHex("#000000", "#ffffff", 50); got != "#7f7f7f" {
		t.Fatalf("pct 50 = %q, want #7f7f7f", got)
	}
}

func TestFadedHueUnseenPinsBright(t *testing.T) {
	p := claudePalette("dark")
	if got := p.fadedHue("waiting", 100, true); got != "#fab387" {
		t.Fatalf("unseen waiting = %q, want bright #fab387", got)
	}
	if got := p.fadedHue("waiting", 100, false); got != p.idle {
		t.Fatalf("faded waiting = %q, want idle %q", got, p.idle)
	}
}

func TestPriorityState(t *testing.T) {
	cases := []struct {
		c    counts
		want string
	}{
		{counts{errorN: 1, waiting: 1}, "error"},
		{counts{waiting: 1, processing: 3}, "waiting"},
		{counts{denied: 1, compacting: 1}, "denied"},
		{counts{processing: 2, done: 1}, "processing"},
		{counts{idle: 1}, "idle"},
		{counts{}, ""},
	}
	for _, c := range cases {
		if got := c.c.priorityState(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestFadePct(t *testing.T) {
	now := int64(1000)
	if got := fadePct("waiting", now, now-10); got != 0 {
		t.Errorf("fresh waiting = %d, want 0", got)
	}
	if got := fadePct("waiting", now, now-30-45); got != 100 {
		t.Errorf("fully stale waiting = %d, want 100", got)
	}
	if got := fadePct("waiting", now, now-52); got < 40 || got > 55 {
		t.Errorf("mid-fade waiting = %d, want ~48", got)
	}
}

func TestFormatIssueList(t *testing.T) {
	if got := formatIssueList(3, []string{"ENG-1", "ENG-2"}); got != "ENG-1 ENG-2" {
		t.Errorf("got %q", got)
	}
	if got := formatIssueList(3, []string{"A", "B", "C", "D", "E"}); got != "A B C +2" {
		t.Errorf("got %q", got)
	}
	if got := formatIssueList(3, nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestAggregateSessionFromDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	now := int64(2000)
	os.WriteFile(dir+"/panes/1", []byte("state=waiting\ntimestamp=2000\nsession=work\n"), 0o644)
	os.WriteFile(dir+"/panes/2", []byte("state=processing\ntimestamp=2000\nsession=other\n"), 0o644)
	os.WriteFile(dir+"/issues/1", []byte("ENG-9\n"), 0o644)

	agg := aggregateSession(dir, "work", now)
	if agg.counts.total != 1 {
		t.Fatalf("total = %d, want 1", agg.counts.total)
	}
	if agg.counts.priorityState() != "waiting" {
		t.Fatalf("state = %q, want waiting", agg.counts.priorityState())
	}
	if len(agg.issues) != 1 || agg.issues[0] != "ENG-9" {
		t.Fatalf("issues = %v, want [ENG-9]", agg.issues)
	}
}

func TestClaudeSegment(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/panes", 0o755)
	os.MkdirAll(dir+"/issues", 0o755)
	now := int64(5000)
	os.WriteFile(dir+"/panes/7", []byte("state=waiting\ntimestamp=5000\nsession=s\n"), 0o644)
	os.WriteFile(dir+"/issues/7", []byte("ENG-1\n"), 0o644)

	got := claudeSegment(dir, "s", "dark", now)
	want := "#[fg=#fab387]󰔟#[fg=default] #[fg=#6c7086]ENG-1#[fg=default] "
	if got != want {
		t.Fatalf("claudeSegment\n got %q\nwant %q", got, want)
	}

	if got := claudeSegment(dir, "absent", "dark", now); got != "" {
		t.Fatalf("absent session = %q, want empty", got)
	}
}
