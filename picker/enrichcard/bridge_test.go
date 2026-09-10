package main

import "testing"

// TestResolveMirrorFullBridgeState: a mirror with bridge state populated
// resolves to the bridge values, not the local residue.
func TestResolveMirrorFullBridgeState(t *testing.T) {
	o := winOpts{
		mirror: true,
		local: winState{
			issueProvider: "linear", issueID: "LOCAL-1", branch: "local-residue-branch",
			worktree: "/local/residue/repo", task: "local task", claudeAgo: "2m", paneIcon: "",
		},
		bridge: winState{
			issueProvider: "github", issueID: "42", issueTitle: "Bridge issue",
			issueURL: "https://github.com/o/r/issues/42",
			prNumber: "7", prTitle: "Bridge PR", prState: "open", prCheck: "success",
			prURL: "https://github.com/o/r/pull/7", prMergeable: "mergeable", prDraft: "1",
			branch: "feat/remote-branch", worktree: "/home/remote/repo",
		},
	}
	w := resolve(o)

	if w.issueProvider != "github" || w.issueID != "42" || w.issueTitle != "Bridge issue" || w.issueURL != "https://github.com/o/r/issues/42" {
		t.Errorf("issue fields = %+v, want bridge values", w)
	}
	if w.prNumber != "7" || w.prTitle != "Bridge PR" || w.branch != "feat/remote-branch" || w.worktree != "/home/remote/repo" {
		t.Errorf("pr/branch/dir fields = %+v, want bridge values", w)
	}
	// Always-local fields carry over even in mirror mode.
	if w.task != "local task" || w.claudeAgo != "2m" {
		t.Errorf("task/claudeAgo = %q/%q, want local values", w.task, w.claudeAgo)
	}
}

// TestResolveBareMirrorNoLocalLeak is the assertion that matters most: a
// mirror whose bridge fields are all empty (an older remote, or one that
// hasn't reported yet) must resolve to an EMPTY winState for the
// bridge-owned fields, even though local residue from the launcher's
// after-new-window hook is present and non-empty. Falling back to that
// residue is the bug this design fixes (D3/I5) — a mirror must never show
// a different repo's issue/PR/branch just because a value happens to be
// sitting in the local option.
func TestResolveBareMirrorNoLocalLeak(t *testing.T) {
	o := winOpts{
		mirror: true,
		local: winState{
			issueProvider: "linear", issueID: "LOCAL-999", issueTitle: "Wrong repo issue",
			issueURL: "https://linear.app/wrong/issue/999",
			prNumber: "13", prTitle: "Wrong repo PR", prState: "open",
			prURL:  "https://github.com/wrong/repo/pull/13",
			branch: "wrong-branch", worktree: "/wrong/repo", gitRoot: "/wrong/repo/.git",
			task: "local task", claudeAgo: "2m",
		},
		bridge: winState{}, // nothing reported yet
	}
	w := resolve(o)

	if w.issueProvider != "" || w.issueID != "" || w.issueTitle != "" || w.issueURL != "" {
		t.Errorf("issue fields leaked local residue: %+v", w)
	}
	if w.prNumber != "" || w.prTitle != "" || w.prState != "" || w.prURL != "" {
		t.Errorf("pr fields leaked local residue: %+v", w)
	}
	if w.branch != "" || w.worktree != "" || w.gitRoot != "" {
		t.Errorf("branch/dir fields leaked local residue: %+v", w)
	}
	// Always-local fields are the one deliberate exception.
	if w.task != "local task" || w.claudeAgo != "2m" {
		t.Errorf("task/claudeAgo = %q/%q, want local values", w.task, w.claudeAgo)
	}
}

// TestResolveNonMirrorUnchanged: a non-mirror window resolves to exactly the
// local values, byte-identical to pre-bridge-aware behaviour.
func TestResolveNonMirrorUnchanged(t *testing.T) {
	o := winOpts{
		mirror: false,
		local: winState{
			issueProvider: "linear", issueID: "ENG-1", branch: "feat/local",
			worktree: "/home/noams/Data/git/noamsto/lazytmux", task: "local task", claudeAgo: "4m",
		},
		bridge: winState{issueProvider: "github", issueID: "999"}, // must be ignored
	}
	w := resolve(o)

	if w != o.local {
		t.Errorf("resolve(non-mirror) = %+v, want local unchanged %+v", w, o.local)
	}
}
