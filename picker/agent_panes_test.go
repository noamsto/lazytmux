package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writePaneStateFile writes a panes/ or screen/ style state file. extra is
// appended verbatim (e.g. "session=s\nunseen=1\n" for a hook file).
func writePaneStateFile(t *testing.T, dir, id, state string, timestamp int64, extra string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("state=%s\ntimestamp=%d\n%s", state, timestamp, extra)
	if err := os.WriteFile(filepath.Join(dir, id), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCollectAgentPanesMergesHookAndScreen covers the three cases #342 asks
// for directly: a hook-written (Claude) pane, a scraper-only (codex/cursor)
// pane, and a pane with neither file (no agent at all).
func TestCollectAgentPanesMergesHookAndScreen(t *testing.T) {
	root := t.TempDir()
	hookDir := filepath.Join(root, "panes")
	screenDir := filepath.Join(root, "screen")
	issuesDir := filepath.Join(root, "issues")

	// "1": hook-written Claude pane.
	writePaneStateFile(t, hookDir, "1", "processing", 1000, "session=claude-sess\n")
	// "2": scraper-only pane (codex/cursor) — screen/ has no session field,
	// so it can only be placed via the live pane map.
	writePaneStateFile(t, screenDir, "2", "processing", 1000, "")
	// "3": a live pane running neither an agent nor a scraper — no file in
	// either directory, so it must never appear in the result.

	paneMap := map[string]paneMapping{
		"1": {session: "claude-sess", winIdx: 0},
		"2": {session: "codex-sess", winIdx: 1},
		"3": {session: "shell-sess", winIdx: 2},
	}

	panes := collectAgentPanesFrom(hookDir, screenDir, issuesDir, paneMap, 1000)

	byPaneSession := map[string]agentPaneInfo{}
	for _, p := range panes {
		byPaneSession[p.session] = p
	}

	if len(panes) != 2 {
		t.Fatalf("collectAgentPanesFrom() returned %d panes, want 2 (got %+v)", len(panes), panes)
	}
	hookPane, ok := byPaneSession["claude-sess"]
	if !ok || hookPane.state != "processing" {
		t.Errorf("hook-written pane missing or wrong state: %+v", byPaneSession)
	}
	screenPane, ok := byPaneSession["codex-sess"]
	if !ok || screenPane.state != "processing" || screenPane.winIdx != 1 {
		t.Errorf("scraper-only pane missing, wrong state, or wrong window: %+v", byPaneSession)
	}
	if _, ok := byPaneSession["shell-sess"]; ok {
		t.Errorf("non-agent pane must not appear in result: %+v", byPaneSession)
	}
}

// TestCollectAgentPanesHookFirstPrecedence pins the hook-first precedence
// read_pane_state implements in scripts/lib-claude.sh: a waiting/error/denied
// hook state is never overridden by a fresher screen reading, no matter its
// age — those states look identical to idle on screen and would be wrongly
// downgraded. A "most recent timestamp wins" or "screen always wins when
// present" implementation would flip this to "idle" and fail here.
func TestCollectAgentPanesHookFirstPrecedence(t *testing.T) {
	root := t.TempDir()
	hookDir := filepath.Join(root, "panes")
	screenDir := filepath.Join(root, "screen")
	issuesDir := filepath.Join(root, "issues")

	writePaneStateFile(t, hookDir, "1", "waiting", 1000, "session=s\n")
	writePaneStateFile(t, screenDir, "1", "idle", 999999, "")

	paneMap := map[string]paneMapping{"1": {session: "s", winIdx: 0}}

	// now is far past every staleness threshold; waiting still must win.
	panes := collectAgentPanesFrom(hookDir, screenDir, issuesDir, paneMap, 999999)

	if len(panes) != 1 || panes[0].state != "waiting" {
		t.Fatalf("collectAgentPanesFrom() = %+v, want one pane with state=waiting (hook-first, protected state)", panes)
	}
}

// TestCollectAgentPanesScreenOverridesStaleHook covers the other half of the
// same precedence rule: compacting/processing/done CAN be corrected by a live
// screen reading once the hook state is stale past its own threshold — the
// case where a completion hook was missed.
func TestCollectAgentPanesScreenOverridesStaleHook(t *testing.T) {
	root := t.TempDir()
	hookDir := filepath.Join(root, "panes")
	screenDir := filepath.Join(root, "screen")
	issuesDir := filepath.Join(root, "issues")

	// processing stale threshold is 300s; hook written at t=0, screen fresher.
	writePaneStateFile(t, hookDir, "1", "processing", 0, "session=s\nunseen=1\n")
	writePaneStateFile(t, screenDir, "1", "idle", 400, "")

	paneMap := map[string]paneMapping{"1": {session: "s", winIdx: 0}}

	panes := collectAgentPanesFrom(hookDir, screenDir, issuesDir, paneMap, 400)

	if len(panes) != 1 {
		t.Fatalf("collectAgentPanesFrom() returned %d panes, want 1", len(panes))
	}
	if panes[0].state != "idle" {
		t.Errorf("stale processing hook with a live screen reading = %q, want idle (screen overrides)", panes[0].state)
	}
	if panes[0].unseen {
		t.Error("unseen must clear when the screen reading overrides a stale hook state")
	}
}

// TestCollectAgentPanesOverrideRefreshesSessionFromPaneMap: when a stale hook
// is overridden by a live screen reading, the pane's session is re-resolved
// from the live pane map rather than trusted from the (possibly outdated)
// hook write — so the pane still surfaces under its real, current session
// instead of being silently dropped or misplaced.
func TestCollectAgentPanesOverrideRefreshesSessionFromPaneMap(t *testing.T) {
	root := t.TempDir()
	hookDir := filepath.Join(root, "panes")
	screenDir := filepath.Join(root, "screen")
	issuesDir := filepath.Join(root, "issues")

	writePaneStateFile(t, hookDir, "1", "processing", 0, "session=stale-sess\n")
	writePaneStateFile(t, screenDir, "1", "idle", 400, "")

	paneMap := map[string]paneMapping{"1": {session: "live-sess", winIdx: 2}}

	panes := collectAgentPanesFrom(hookDir, screenDir, issuesDir, paneMap, 400)

	if len(panes) != 1 {
		t.Fatalf("collectAgentPanesFrom() returned %d panes, want 1", len(panes))
	}
	if panes[0].session != "live-sess" || panes[0].winIdx != 2 {
		t.Errorf("collectAgentPanesFrom() session/winIdx = %q/%d, want live-sess/2 (from the pane map, not the stale hook)",
			panes[0].session, panes[0].winIdx)
	}
}
