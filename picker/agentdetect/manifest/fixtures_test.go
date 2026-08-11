// Fixtures under testdata/ mirror real Claude/Codex/Cursor TUI output — the
// idle/working layouts (Claude's ❯ prompt box + trailing status lines, Codex's ›
// prompt and "Working (Ns • esc to interrupt)" line, Cursor's block-bar input and
// "Running … now" / shell-tool timing lines, or the older braille "Working" line)
// were verified against live panes, then trimmed to representative form here.
package manifest

import (
	"os"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) (title, screen string) {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(b), "\n", 2)
	if strings.HasPrefix(lines[0], "TITLE:") {
		return strings.TrimPrefix(lines[0], "TITLE:"), lines[1]
	}
	return "", string(b)
}

func TestFixtures(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ file, cmd, want string }{
		{"claude_working.txt", "claude", "processing"},
		{"claude_idle.txt", "claude", "idle"},
		{"claude_permission.txt", "claude", "waiting"},
		{"codex_working.txt", "codex", "processing"},
		{"codex_idle.txt", "codex", "idle"},
		{"cursor_working.txt", "cursor-agent", "processing"},
		{"cursor_working_braille.txt", "cursor-agent", "processing"},
		{"cursor_idle.txt", "cursor-agent", "idle"},
		{"cursor_permission.txt", "cursor-agent", "waiting"},
		{"cursor_reject_feedback.txt", "cursor-agent", "waiting"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			m, ok := ForCommand(ms, c.cmd)
			if !ok {
				t.Fatalf("no manifest for %s", c.cmd)
			}
			title, screen := loadFixture(t, c.file)
			got, _ := Match(m, screen, title, false)
			if got != c.want {
				t.Fatalf("Match(%s) = %q, want %q", c.file, got, c.want)
			}
		})
	}
}
