package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noamsto/tmux-og/generator/config"
	"github.com/noamsto/tmux-og/generator/paths"
)

func writeTemplate(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tmux.conf.tmpl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCopyCommandByPlatform(t *testing.T) {
	tests := []struct {
		platform string
		want     string
	}{
		{"darwin", "pbcopy"},
		{"linux", "wl-copy"},
		{"freebsd", "wl-copy"},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			d := Build(&config.Config{Platform: tt.platform}, &paths.Paths{})
			if d.CopyCommand != tt.want {
				t.Fatalf("CopyCommand = %q, want %q", d.CopyCommand, tt.want)
			}
		})
	}
}

func TestExecute(t *testing.T) {
	cfg := &config.Config{
		Platform: "linux",
		Tmux:     config.Tmux{Prefix: "`"},
	}
	p := &paths.Paths{Bash: "/store/bash/bin/bash"}
	tmpl := writeTemplate(t, "set -g prefix {{.Config.Tmux.Prefix}}\ncopy: {{.CopyCommand}}\nsh: {{.Paths.Bash}}\n")
	out, err := Execute(tmpl, Build(cfg, p))
	if err != nil {
		t.Fatal(err)
	}
	want := "set -g prefix `\ncopy: wl-copy\nsh: /store/bash/bin/bash\n"
	if string(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestExecuteUnknownName(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown field", "{{.NotAField}}\n"},
		{"unknown map key", "{{.Config.ProcessIcons.nope}}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Build(&config.Config{ProcessIcons: map[string]string{"claude": "x"}}, &paths.Paths{})
			if _, err := Execute(writeTemplate(t, tt.body), d); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

// A '{{' inside a config value is data, not template: it is substituted after
// parsing, so it can never be re-read as an action.
func TestValueBracesRenderVerbatim(t *testing.T) {
	cfg := &config.Config{Tmux: config.Tmux{ExtraConfig: "set -g x '{{ not an action }}'"}}
	out, err := Execute(writeTemplate(t, "{{.Config.Tmux.ExtraConfig}}\n"), Build(cfg, &paths.Paths{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "{{ not an action }}") {
		t.Fatalf("output = %q", out)
	}
}

func TestExecuteMissingTemplateFile(t *testing.T) {
	_, err := Execute(filepath.Join(t.TempDir(), "absent.tmpl"), Build(&config.Config{}, &paths.Paths{}))
	if err == nil {
		t.Fatal("want an error, got nil")
	}
}
