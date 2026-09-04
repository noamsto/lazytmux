package daemon

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// emptyRemote answers with no windows, which reconcileWindows reads as a lost
// round-trip and leaves the mirror alone — so these cases assert the retire
// itself, not the rebuild that follows it.
func emptyRemote() roundTrip {
	rt, _ := scriptedRT("%begin 1 1 1\n%end 1 1 1\n")
	return rt
}

// #514: the reattach sweep asks localWindowGone once, and answers on positive
// evidence alone — so a lookup it cannot read leaves the dead entry in place.
// Nothing else re-read the local window set, so the mirror stayed a window
// short for the rest of the session while the label poll aimed a set-option at
// the dead id every second. The coarse tick has to close that.
func TestHealLostWindowsRetiresAMirrorTheSweepMissed(t *testing.T) {
	var mu sync.Mutex
	var killed []string

	cfg := Config{
		LocalSess: "host-sess",
		LocalTmux: func(argv ...string) error {
			mu.Lock()
			defer mu.Unlock()
			if argv[0] == "kill-window" {
				killed = append(killed, strings.Join(argv, " "))
			}
			return nil
		},
		// @0 survived; @143 is the window that went away unseen.
		LocalTmuxOut: func(argv ...string) (string, error) { return "@0\n", nil },
	}

	reg := newRegistry()
	reg.add("@1", "@143")

	healLostWindows(cfg, func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())

	if _, ok := reg.byRemoteID("@1"); ok {
		t.Error("the entry for the dead window survived the heal; the mirror stays short and the label poll keeps aiming at it")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(killed) != 1 {
		t.Errorf("kill-window calls = %v, want the one retiring @143", killed)
	}
}

// A window that is still listed must be left alone: the heal runs every tick,
// so a false positive would tear down and rebuild a healthy mirror on a loop.
func TestHealLostWindowsLeavesLiveMirrorsAlone(t *testing.T) {
	cfg := Config{
		LocalSess:    "host-sess",
		LocalTmux:    func(...string) error { return nil },
		LocalTmuxOut: func(...string) (string, error) { return "@0\n@143\n", nil },
	}
	reg := newRegistry()
	reg.add("@1", "@143")

	healLostWindows(cfg, func(string) {}, NewRouter(), noHellos, newCtlState(), reg, newConverger(), emptyRemote())

	if _, ok := reg.byRemoteID("@1"); !ok {
		t.Error("retired a mirror whose window is still listed")
	}
}

// The case the old reading missed: a lookup aimed at the dead window fails
// (any reason but the exit-0-empty-output one it assumed), while the session
// still lists its windows fine. Absence from that complete listing is the
// evidence; asking the dead target alone could only answer "unreadable", which
// the sweep then read as a live window (#514).
func TestLocalWindowGoneWhenOnlyTheWindowLookupFails(t *testing.T) {
	cfg := Config{
		LocalSess: "host-sess",
		LocalTmuxOut: func(argv ...string) (string, error) {
			if argv[0] == "display-message" {
				return "", errors.New("can't find window: @143")
			}
			return "@0\n", nil
		},
	}
	if !localWindowGone(cfg, "@143") {
		t.Error("localWindowGone = false; a window absent from a readable listing is gone")
	}
}

// The evidence is absence from a reply known to be complete. An unreadable
// listing is not that, and retiring on it would rebuild a healthy window.
func TestLocalWindowGoneNeedsAReadableListing(t *testing.T) {
	cfg := Config{
		LocalSess:    "host-sess",
		LocalTmuxOut: func(...string) (string, error) { return "", errors.New("lost server") },
	}
	if localWindowGone(cfg, "@143") {
		t.Error("localWindowGone = true on an unreadable listing")
	}
}
