package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeCapture replays stdout (with %s substituted by the marker read out of
// argv) plus an optional error, and records how it was called.
type fakeCapture struct {
	stdout string
	err    error
	calls  int
	argv   []string
	marker string
}

func (f *fakeCapture) run(args ...string) ([]byte, error) {
	f.calls++
	f.argv = args
	f.marker = args[len(args)-1]
	return []byte(strings.ReplaceAll(f.stdout, "%M", f.marker)), f.err
}

func TestCaptureTargetsBatch(t *testing.T) {
	f := &fakeCapture{stdout: "alpha row\n%M\nbeta row\n%M\n"}
	got, err := captureTargets([]string{"%1", "%2"}, f.run)
	if err != nil {
		t.Fatalf("captureTargets: %v", err)
	}
	if f.calls != 1 {
		t.Errorf("runner called %d times, want 1", f.calls)
	}
	want := map[string]string{
		"%1": "alpha row" + captureBGReset,
		"%2": "beta row" + captureBGReset,
	}
	for target, w := range want {
		if got[target] != w {
			t.Errorf("content[%s] = %q, want %q", target, got[target], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d (%+v)", len(got), len(want), got)
	}

	seps := 0
	for _, a := range f.argv {
		if a == ";" {
			seps++
		}
	}
	if seps != 3 {
		t.Errorf("argv has %d %q separators, want 3: %q", seps, ";", f.argv)
	}
	wantMarker := fmt.Sprintf("@@lztmux-wall-%d-", os.Getpid())
	if !strings.HasPrefix(f.marker, wantMarker) {
		t.Errorf("marker %q, want prefix %q", f.marker, wantMarker)
	}
	markers := 0
	for _, a := range f.argv {
		if a == f.marker {
			markers++
		}
	}
	if markers != 2 {
		t.Errorf("marker appears %d times in argv, want 2: %q", markers, f.argv)
	}
}

func TestCaptureTargetsContent(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		stdout  string
		want    map[string]string
	}{
		{
			name:    "empty capture keeps the key",
			targets: []string{"%1", "%2"},
			stdout:  "%M\nbeta\n%M\n",
			want: map[string]string{
				"%1": "",
				"%2": "beta" + captureBGReset,
			},
		},
		{
			name:    "marker lookalikes stay content",
			targets: []string{"%1"},
			stdout:  "@@lztmux-wall- is not a marker\nlog: %M trailing\n%M\n",
			want: map[string]string{
				"%1": "@@lztmux-wall- is not a marker" + captureBGReset + "\n" +
					"log: %M trailing" + captureBGReset,
			},
		},
		{
			name:    "trailing blank rows trimmed",
			targets: []string{"%1"},
			stdout:  "top\n\n   \n" + captureBGReset + "    \n%M\n",
			want: map[string]string{
				"%1": "top" + captureBGReset,
			},
		},
		{
			name:    "duplicate target captured once",
			targets: []string{"%1", "%1"},
			stdout:  "solo\n%M\n",
			want: map[string]string{
				"%1": "solo" + captureBGReset,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeCapture{stdout: c.stdout}
			got, err := captureTargets(c.targets, f.run)
			if err != nil {
				t.Fatalf("captureTargets: %v", err)
			}
			if len(got) != len(c.want) {
				t.Errorf("got %d keys, want %d (%+v)", len(got), len(c.want), got)
			}
			for target, w := range c.want {
				w = strings.ReplaceAll(w, "%M", f.marker)
				if got[target] != w {
					t.Errorf("content[%s] = %q, want %q", target, got[target], w)
				}
			}
		})
	}
}

func TestCaptureTargetsAbort(t *testing.T) {
	cases := []struct {
		name       string
		targets    []string
		stdout     string
		wantKeys   []string
		wantTarget string
	}{
		{
			name:       "bad target mid-batch",
			targets:    []string{"%1", "%2", "%3"},
			stdout:     "alpha\n%M\n",
			wantKeys:   []string{"%1"},
			wantTarget: "%2",
		},
		{
			name:       "bad target first",
			targets:    []string{"%9", "%1"},
			stdout:     "",
			wantKeys:   nil,
			wantTarget: "%9",
		},
	}
	exit1 := errors.New("exit status 1")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeCapture{stdout: c.stdout, err: exit1}
			got, err := captureTargets(c.targets, f.run)
			var cErr *captureErr
			if !errors.As(err, &cErr) {
				t.Fatalf("err = %v, want *captureErr", err)
			}
			if cErr.Target != c.wantTarget {
				t.Errorf("Target = %q, want %q", cErr.Target, c.wantTarget)
			}
			if !errors.Is(err, exit1) {
				t.Errorf("err does not unwrap to the runner error: %v", err)
			}
			if len(got) != len(c.wantKeys) {
				t.Errorf("got %d keys, want %d (%+v)", len(got), len(c.wantKeys), got)
			}
			for _, k := range c.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("content missing key %s (%+v)", k, got)
				}
			}
		})
	}
}

func TestCaptureTargetsNoTargets(t *testing.T) {
	f := &fakeCapture{stdout: "unused"}
	got, err := captureTargets(nil, f.run)
	if err != nil {
		t.Fatalf("captureTargets: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty map", got)
	}
	if f.calls != 0 {
		t.Errorf("runner called %d times, want 0", f.calls)
	}
}
