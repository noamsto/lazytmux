package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noamsto/lazytmux/generator/paths"
)

type fixture struct {
	config   string
	paths    string
	template string
	out      string
}

func newFixture(t *testing.T, configBody, templateBody string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		config:   filepath.Join(dir, "config.toml"),
		paths:    filepath.Join(dir, "paths.toml"),
		template: filepath.Join(dir, "tmux.conf.tmpl"),
		out:      filepath.Join(dir, "out"),
	}
	write(t, f.config, configBody)
	write(t, f.paths, pathsToml())
	write(t, f.template, templateBody)
	return f
}

func (f fixture) args() []string {
	return []string{"--config", f.config, "--paths", f.paths, "--template", f.template, "--out", f.out}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pathsToml() string {
	var b strings.Builder
	b.WriteString("bash = \"/store/bash/bin/bash\"\n\n[scripts]\n")
	for _, n := range paths.RequiredScripts {
		fmt.Fprintf(&b, "%q = \"/store/%s\"\n", n, n)
	}
	b.WriteString("\n[bin]\n")
	for _, n := range paths.RequiredBin {
		fmt.Fprintf(&b, "%q = \"/store/%s\"\n", n, n)
	}
	b.WriteString("\n[plugins]\n")
	for _, n := range []string{"catppuccin", "better-mouse-mode", "vim-tmux-navigator", "tmux-fzf", "fingers"} {
		fmt.Fprintf(&b, "%q = \"/store/%s.tmux\"\n", n, n)
	}
	return b.String()
}

const okConfig = "platform = \"linux\"\n\n[tmux]\nprefix = \"b\"\n"

func TestRunWritesOnlyTmuxConf(t *testing.T) {
	f := newFixture(t, okConfig, "set -g prefix {{.Config.Tmux.Prefix}}\n")
	if err := run(f.args()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "tmux.conf" {
		t.Fatalf("out dir holds %v, want exactly [tmux.conf]", entries)
	}
	got, err := os.ReadFile(filepath.Join(f.out, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "set -g prefix b\n" {
		t.Fatalf("tmux.conf = %q", got)
	}
}

func TestRunErrors(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		template     string
		args         func(f fixture) []string
		wantContains string
	}{
		{
			name:         "unknown config key",
			config:       okConfig + "not_a_key = 1\n",
			template:     "x\n",
			wantContains: "unknown key(s)",
		},
		{
			name:         "vs16 icon",
			config:       okConfig + "\n[process_icons]\nzed = \"⚡️\"\n",
			template:     "x\n",
			wantContains: "Strip the trailing ️ from: zed",
		},
		{
			name:         "template names an unknown key",
			config:       okConfig,
			template:     "{{.Nope}}\n",
			wantContains: "render:",
		},
		{
			name:     "neither paths nor prefix",
			config:   okConfig,
			template: "x\n",
			args: func(f fixture) []string {
				return []string{"--config", f.config, "--template", f.template, "--out", f.out}
			},
			wantContains: "exactly one of --paths or --prefix",
		},
		{
			name:     "both paths and prefix",
			config:   okConfig,
			template: "x\n",
			args: func(f fixture) []string {
				return append(f.args(), "--prefix", "/nowhere")
			},
			wantContains: "exactly one of --paths or --prefix",
		},
		{
			name:     "missing template flag",
			config:   okConfig,
			template: "x\n",
			args: func(f fixture) []string {
				return []string{"--config", f.config, "--paths", f.paths, "--out", f.out}
			},
			wantContains: "--template is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.config, tt.template)
			args := f.args()
			if tt.args != nil {
				args = tt.args(f)
			}
			err := run(args)
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tt.wantContains)
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantContains)
			}
			if _, statErr := os.Stat(f.out); statErr == nil {
				t.Error("out dir was created despite the failure")
			}
		})
	}
}

func TestRunPrefixMode(t *testing.T) {
	f := newFixture(t, okConfig, "sh: {{.Paths.Bash}}\n")
	prefix := t.TempDir()
	args := []string{"--config", f.config, "--prefix", prefix, "--template", f.template, "--out", f.out}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.out, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := "sh: " + filepath.Join(prefix, "bin", "bash") + "\n"
	if string(got) != want {
		t.Fatalf("tmux.conf = %q, want %q", got, want)
	}
}

// The VS16 message is shared with the Nix throw byte for byte, so main must not
// add a prefix or a second newline when it prints one.
func TestVS16MessagePrintsVerbatim(t *testing.T) {
	f := newFixture(t, okConfig+"\n[process_icons]\nzed = \"⚡️\"\n", "x\n")
	err := run(f.args())
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	want := "process-icons: VS16 emoji (U+FE0F) cause alignment bugs in the picker.\n" +
		"Strip the trailing ️ from: zed\n" +
		"See: charmbracelet/lipgloss#55\n"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}
