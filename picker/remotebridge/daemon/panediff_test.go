package daemon

import (
	"reflect"
	"testing"
)

// apply replays ops against have the way the daemon does against real tmux
// panes, so each case asserts the plan actually reaches want rather than just
// matching a hand-written op list.
func apply(have []string, ops paneOps) []string {
	cur := append([]string(nil), have...)
	for _, i := range ops.Remove {
		cur = append(cur[:i], cur[i+1:]...)
	}
	cur = append(cur, ops.Append...)
	for _, s := range ops.Swaps {
		cur[s[0]], cur[s[1]] = cur[s[1]], cur[s[0]]
	}
	return cur
}

func TestPlanPaneOps(t *testing.T) {
	tests := []struct {
		name       string
		have, want []string
		reset      bool
		remove     []int
		appends    []string
	}{
		{
			name: "unchanged",
			have: []string{"%1", "%2"}, want: []string{"%1", "%2"},
		},
		{
			name: "tail append (remote split the last pane)",
			have: []string{"%1"}, want: []string{"%1", "%2"},
			appends: []string{"%2"},
		},
		{
			name: "tail removal",
			have: []string{"%1", "%2", "%3"}, want: []string{"%1"},
			remove: []int{2, 1},
		},
		{
			// Measured: a remote 3-pane window %0 %1 %2 split at %0 reports
			// %0 %3 %1 %2. The old three-case reconcile skipped this entirely.
			name: "mid-list insert (split a non-last pane)",
			have: []string{"%0", "%1", "%2"}, want: []string{"%0", "%3", "%1", "%2"},
			appends: []string{"%3"},
		},
		{
			name: "mid-list removal (kill a non-last pane)",
			have: []string{"%0", "%3", "%1", "%2"}, want: []string{"%0", "%3", "%2"},
			remove: []int{2},
		},
		{
			name: "adjacent permutation (swap-pane -U)",
			have: []string{"%0", "%1", "%2"}, want: []string{"%1", "%0", "%2"},
		},
		{
			name: "non-adjacent permutation",
			have: []string{"%0", "%1", "%2"}, want: []string{"%2", "%1", "%0"},
		},
		{
			name: "rotation",
			have: []string{"%0", "%1", "%2"}, want: []string{"%1", "%2", "%0"},
		},
		{
			name: "insert, remove and reorder at once",
			have: []string{"%0", "%1", "%2"}, want: []string{"%3", "%2", "%0"},
			remove: []int{1}, appends: []string{"%3"},
		},
		{
			// Reachable without concurrency: a remote split followed quickly by
			// killing the original pane. Killing every local pane would make
			// tmux destroy the mirror window, so this must rebuild instead.
			name: "total replacement resets",
			have: []string{"%0"}, want: []string{"%3"},
			reset: true,
		},
		{
			name: "every pane closed resets",
			have: []string{"%0", "%1"}, want: nil,
			reset: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := planPaneOps(tc.have, tc.want)
			if ops.Reset != tc.reset {
				t.Fatalf("Reset = %v, want %v", ops.Reset, tc.reset)
			}
			if tc.reset {
				return
			}
			if tc.remove != nil || ops.Remove != nil {
				if !reflect.DeepEqual(ops.Remove, tc.remove) {
					t.Errorf("Remove = %v, want %v", ops.Remove, tc.remove)
				}
			}
			if tc.appends != nil || ops.Append != nil {
				if !reflect.DeepEqual(ops.Append, tc.appends) {
					t.Errorf("Append = %v, want %v", ops.Append, tc.appends)
				}
			}
			if got := apply(tc.have, ops); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("applying ops gave %v, want %v (ops %+v)", got, tc.want, ops)
			}
		})
	}
}

// Removals must be descending: killing a low index first would shift every
// higher index down and make the rest of the list wrong.
func TestPlanPaneOpsRemovesDescending(t *testing.T) {
	ops := planPaneOps([]string{"%0", "%1", "%2", "%3"}, []string{"%1"})
	for i := 1; i < len(ops.Remove); i++ {
		if ops.Remove[i-1] <= ops.Remove[i] {
			t.Fatalf("Remove not descending: %v", ops.Remove)
		}
	}
}

// A permutation costs at most n-1 transpositions, so the plan can never
// degenerate into a long swap chain against a live tmux window.
func TestPlanPaneOpsSwapsBounded(t *testing.T) {
	have := []string{"%0", "%1", "%2", "%3", "%4"}
	want := []string{"%4", "%3", "%2", "%1", "%0"}
	ops := planPaneOps(have, want)
	if len(ops.Swaps) > len(want)-1 {
		t.Errorf("got %d swaps for %d panes, want <= %d", len(ops.Swaps), len(want), len(want)-1)
	}
	if got := apply(have, ops); !reflect.DeepEqual(got, want) {
		t.Errorf("applying ops gave %v, want %v", got, want)
	}
}
