package main

import (
	"errors"
	"os/exec"
	"testing"
)

func TestSSHErrorDetail(t *testing.T) {
	t.Run("plain error falls back to Error()", func(t *testing.T) {
		err := errors.New("boom")
		if got := sshErrorDetail(err); got != "boom" {
			t.Fatalf("sshErrorDetail = %q, want %q", got, "boom")
		}
	})

	t.Run("ExitError with no stderr falls back to Error()", func(t *testing.T) {
		exitErr := &exec.ExitError{}
		if got := sshErrorDetail(exitErr); got != exitErr.Error() {
			t.Fatalf("sshErrorDetail = %q, want %q", got, exitErr.Error())
		}
	})

	t.Run("ExitError with stderr prefers the trimmed stderr", func(t *testing.T) {
		exitErr := &exec.ExitError{Stderr: []byte("ssh: Could not resolve hostname foo: nodename nor servname provided\n")}
		want := "ssh: Could not resolve hostname foo: nodename nor servname provided"
		if got := sshErrorDetail(exitErr); got != want {
			t.Fatalf("sshErrorDetail = %q, want %q", got, want)
		}
	})
}

func TestParseRemote(t *testing.T) {
	allPresent := func() string {
		var s string
		for _, item := range remoteChecklist {
			s += item.Name + "=1\n"
		}
		return s
	}()
	allMissing := func() string {
		var s string
		for _, item := range remoteChecklist {
			s += item.Name + "=0\n"
		}
		return s
	}()

	tests := []struct {
		name   string
		output string
		check  func(t *testing.T, results []Result)
	}{
		{
			name:   "all present",
			output: allPresent,
			check: func(t *testing.T, results []Result) {
				for _, r := range results {
					if !r.OK {
						t.Errorf("%s: OK = false, want true", r.Name)
					}
				}
			},
		},
		{
			name:   "all missing",
			output: allMissing,
			check: func(t *testing.T, results []Result) {
				for _, r := range results {
					if r.OK {
						t.Errorf("%s: OK = true, want false", r.Name)
					}
					wantDetail := "not found"
					if r.Name == "og-remote-picker" {
						wantDetail = "remote too old to probe itself"
					}
					if r.Detail != wantDetail {
						t.Errorf("%s: Detail = %q, want %q", r.Name, r.Detail, wantDetail)
					}
				}
			},
		},
		{
			name:   "partial mix",
			output: "tmux-claude-images=1\nresvg=0\n",
			check: func(t *testing.T, results []Result) {
				byName := make(map[string]Result, len(results))
				for _, r := range results {
					byName[r.Name] = r
				}
				if !byName["tmux-claude-images"].OK {
					t.Errorf("tmux-claude-images should be OK")
				}
				if byName["resvg"].OK || byName["resvg"].Detail != "not found" {
					t.Errorf("resvg = %+v", byName["resvg"])
				}
			},
		},
		{
			name:   "extra unknown line ignored",
			output: allPresent + "some-other-tool=1\n",
			check: func(t *testing.T, results []Result) {
				if len(results) != len(remoteChecklist) {
					t.Fatalf("len(results) = %d, want %d", len(results), len(remoteChecklist))
				}
				for _, r := range results {
					if r.Name == "some-other-tool" {
						t.Fatalf("unknown name %q leaked into results", r.Name)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, ParseRemote(tt.output))
		})
	}
}

func TestParseRemoteMissingLine(t *testing.T) {
	// Only report the first checklist entry; every other name has no line at
	// all, which must read as "no answer", not "not found".
	output := remoteChecklist[0].Name + "=1\n"
	results := ParseRemote(output)

	byName := make(map[string]Result, len(results))
	for _, r := range results {
		byName[r.Name] = r
	}

	if !byName[remoteChecklist[0].Name].OK {
		t.Fatalf("%s should be OK", remoteChecklist[0].Name)
	}
	for _, item := range remoteChecklist[1:] {
		r := byName[item.Name]
		if r.OK || r.Detail != "no answer" {
			t.Errorf("%s = %+v, want OK=false Detail=%q", item.Name, r, "no answer")
		}
	}
}
