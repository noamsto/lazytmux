package tmuxformat

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestCheckLineRejectsViolations(t *testing.T) {
	t.Parallel()
	tab := "\t"
	cases := []struct {
		name string
		line string
		want []Kind
	}{
		{
			name: "literal-tab",
			line: fmt.Sprintf("rows := tmux list-panes -F '#{pane_id}%s#{pane_pipe}'", tab),
			want: []Kind{RawControl},
		},
		{
			name: "escaped-tab",
			line: `"#{pane_id}\t#{pane_pipe}"`,
			want: []Kind{EscapedControl},
		},
		{
			name: "newline-escape",
			line: "fmt := tmux display-message -p $'#{session_windows}\\n#{client_height}'",
			want: []Kind{EscapedControl},
		},
		{
			name: "us-escape",
			line: "fmt := tmux list-panes -F $'#{pane_id}\\x1f#{pane_pipe}'",
			want: []Kind{EscapedControl},
		},
		{
			name: "hex-tab-escape",
			line: "fmt := tmux list-panes -F $'#{pane_id}\\x09#{pane_pipe}'",
			want: []Kind{EscapedControl},
		},
		{
			name: "var-indirect",
			line: fmt.Sprintf("FMT := \"#{window_index}%s#{pane_current_command}\"", tab),
			want: []Kind{RawControl},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := CheckLine(c.line)
			if !slices.Equal(got, c.want) {
				t.Fatalf("CheckLine(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

func TestCheckLineAllowsPipeDelimiterAndUTF8(t *testing.T) {
	t.Parallel()
	cases := []string{
		`"-F", "#{pane_id}|#{pane_pipe}"`,
		`prefix := "#{=/10/…:#{session_name} ├─"`,
		`parts := strings.SplitN(line, "\t", 19)`,
		`for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {`,
	}
	for _, line := range cases {
		if got := CheckLine(line); len(got) != 0 {
			t.Errorf("CheckLine(%q) = %v, want clean", line, got)
		}
	}
}

func TestPickerGoSources(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..")
	vs, err := ScanGoTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) > 0 {
		t.Fatal(FormatReport(vs))
	}
}
