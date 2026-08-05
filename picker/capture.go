package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

// captureBGReset ends every captured row so a pane's own background cannot
// bleed into the tile padding cells around it (same reason as loadPreviewCmd).
const captureBGReset = "\033[49m"

// captureSeq makes each batch's marker unique within a process. Atomic because
// captureTargets runs on bubbletea's command goroutines, not the update loop.
var captureSeq atomic.Uint64

type captureRunner func(args ...string) ([]byte, error)

// captureErr names the target whose capture aborted the batch.
type captureErr struct {
	Target string
	Err    error
}

func (e *captureErr) Error() string {
	return fmt.Sprintf("capture-pane -t %s: %v", e.Target, e.Err)
}

func (e *captureErr) Unwrap() error { return e.Err }

// captureTargets returns each target's visible pane content keyed by target,
// from one batched tmux call: `capture-pane … ; display-message -p <marker>`
// per target, so every capture is terminated by a marker line.
//
// tmux aborts the whole batch at the first bad target (exit 1, stdout holding
// everything up to the last marker it managed to print, nothing after). One
// terminated section per completed capture is what makes the count of parsed
// sections the index of the offender — callers keep the good tiles refreshing
// and drop that target from the next batch.
//
// The marker carries this process's pid and a per-call counter and is passed in
// argv, so a pane that prints a marker-shaped line cannot forge the real one.
func captureTargets(targets []string, run captureRunner) (map[string]string, error) {
	out := make(map[string]string, len(targets))
	if len(targets) == 0 {
		return out, nil
	}
	if run == nil {
		run = func(args ...string) ([]byte, error) {
			return exec.Command("tmux", args...).Output()
		}
	}

	// One capture per distinct target; the deduped set is the requested key
	// set, so keying the map off it covers every requested target.
	distinct := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t] {
			continue
		}
		seen[t] = true
		distinct = append(distinct, t)
	}

	marker := fmt.Sprintf("@@lztmux-wall-%d-%d@@", os.Getpid(), captureSeq.Add(1))
	args := make([]string, 0, len(distinct)*9)
	for _, t := range distinct {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "capture-pane", "-p", "-e", "-t", t, ";", "display-message", "-p", marker)
	}

	stdout, runErr := run(args...)
	parts := splitCaptures(string(stdout), marker)
	for i, p := range parts {
		if i >= len(distinct) {
			break
		}
		out[distinct[i]] = p
	}
	if runErr != nil {
		bad := ""
		if len(parts) < len(distinct) {
			bad = distinct[len(parts)]
		}
		return out, &captureErr{Target: bad, Err: runErr}
	}
	return out, nil
}

// splitCaptures cuts stdout into one section per marker-terminated capture.
// A full-line match only: a pane row merely containing the marker is content.
// Anything trailing the last marker is an unfinished capture, so it is dropped.
func splitCaptures(stdout, marker string) []string {
	var parts []string
	var rows []string
	for _, line := range strings.Split(stdout, "\n") {
		if line == marker {
			parts = append(parts, joinCaptureRows(trimBlankRows(rows)))
			rows = rows[:0]
			continue
		}
		rows = append(rows, line)
	}
	return parts
}

// trimBlankRows drops the trailing empty rows tmux pads a capture with — an
// idle pane is mostly them, and they'd render as dead space in a tile.
func trimBlankRows(rows []string) []string {
	end := len(rows)
	for end > 0 && strings.TrimSpace(stripANSI(rows[end-1])) == "" {
		end--
	}
	return rows[:end]
}

func joinCaptureRows(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, captureBGReset+"\n") + captureBGReset
}
