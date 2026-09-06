package daemon

import (
	"sync"
	"testing"
	"time"
)

// countingCfg answers every listing with wins and counts the local tmux forks.
func countingCfg(wins string, n *int) Config {
	var mu sync.Mutex
	return Config{
		LocalSess: "host-sess",
		LocalTmux: func(...string) error { return nil },
		LocalTmuxOut: func(...string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			*n++
			return wins, nil
		},
	}
}

// The sweep rides the per-line maintenance block, so without the floor it forks
// a local tmux client for every line a redrawing pane emits.
func TestWindowSweeperFloorsRepeatedPasses(t *testing.T) {
	forks := 0
	cfg := countingCfg("@0\n@143\n", &forks)
	reg := newRegistry()
	reg.add("@1", "@143")

	var s windowSweeper
	for i := 0; i < 20; i++ {
		s.sweep(cfg, func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())
	}
	if forks != 1 {
		t.Errorf("local tmux forks = %d over 20 back-to-back sweeps, want 1 (the floor admits one pass)", forks)
	}
	s.lastPass = time.Now().Add(-2 * windowSweepInterval)
	s.sweep(cfg, func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())
	if forks != 2 {
		t.Errorf("local tmux forks = %d after the floor elapsed, want 2 (the sweep still runs)", forks)
	}
}

// One listing answers for the whole registry: a fork per entry made the sweep
// scale with window count.
func TestHealLostWindowsListsOnceForEveryMirror(t *testing.T) {
	forks := 0
	cfg := countingCfg("@0\n@1\n@2\n@3\n", &forks)
	reg := newRegistry()
	reg.add("@10", "@1")
	reg.add("@11", "@2")
	reg.add("@12", "@3")

	healLostWindows(cfg, func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())

	if forks != 1 {
		t.Errorf("local tmux forks = %d for 3 mirror windows, want 1", forks)
	}
}
