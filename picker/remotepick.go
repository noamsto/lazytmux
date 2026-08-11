package main

import (
	"fmt"
	"os"
	"strings"
)

// emitPayload is the picker's only output in --remote-pick mode: a typed
// key=value block the wrapper (scripts/lztmux-remote-picker.sh's kv_get)
// reads back over the same ssh session. See spec "The emit payload" — the
// value is everything after the first '=', so spaces and tabs round-trip.
type emitPayload struct {
	kind string // "session" or "dir"
	name string
	path string // set only for kind == "dir"
}

// maxEmitFieldLen bounds a value against a pathological row. Transport-only:
// no charset filter, since tmux session names are already near-arbitrary and
// cross today's Remote section as argv unfiltered (picker/remote.go:307-312).
const maxEmitFieldLen = 4096

// validEmitField gates a value on what the transport itself cannot carry. The
// format is one key=value per line, so a newline in a value would forge a
// second key — kv_get takes the last match, which is how a crafted session
// name could turn a session pick into a dir pick.
func validEmitField(v string) bool {
	return v != "" && len(v) <= maxEmitFieldLen &&
		!strings.ContainsAny(v, "\x00\n\r")
}

func (p emitPayload) encode() (string, error) {
	if !validEmitField(p.kind) {
		return "", fmt.Errorf("emit payload: unusable kind %q", p.kind)
	}
	if !validEmitField(p.name) {
		return "", fmt.Errorf("emit payload: unusable session name %q", p.name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "kind=%s\n", p.kind)
	if p.kind == "dir" {
		if !validEmitField(p.path) {
			return "", fmt.Errorf("emit payload: unusable path %q", p.path)
		}
		fmt.Fprintf(&b, "path=%s\n", p.path)
	}
	fmt.Fprintf(&b, "name=%s\n", p.name)
	return b.String(), nil
}

// resolveEmitPick maps a selected row to the payload emit mode hands back to
// the wrapper. Pure — no tmux/zoxide call, unlike activateCurrent's ordinary
// path this mode replaces: selection here must be side-effect-free, since the
// picker runs in an ssh pty with no attached tmux client to switch.
func resolveEmitPick(item listItem) (emitPayload, bool) {
	if item.createPath != "" {
		return emitPayload{kind: "dir", path: item.createPath, name: item.createName}, true
	}
	if item.target != "" {
		return emitPayload{kind: "session", name: item.target}, true
	}
	return emitPayload{}, false
}

// writeEmitPayload encodes p and writes it to path (0600 — matches the
// pre-created file's own mode). Called only on a successful pick; cancel
// leaves the wrapper's pre-created file empty, which it reads as cancel.
func writeEmitPayload(path string, p emitPayload) error {
	data, err := p.encode()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(data), 0o600)
}

// noSessionRows reports whether items — the session-mode base, which always
// carries at least a header row (buildSessionItems never omits it) — has no
// actual session.
func noSessionRows(items []listItem) bool {
	for _, it := range items {
		if !it.isHeader {
			return false
		}
	}
	return true
}

// remotePickGated parses the mirror-window gate probe — tmux display-message
// evaluating `#{&&:#{@bridge_win},#{@bridge_pane}}` — into a bool. Only "1"
// means gated (spec D7); "0", empty, and any whitespace/newline padding
// display-message adds around either are not.
func remotePickGated(raw string) bool {
	return strings.TrimSpace(raw) == "1"
}

// shellQuote single-quotes s for a POSIX shell, escaping embedded single
// quotes — the Go twin of the house shell_quote() bash helper
// (scripts/lztmux-remote-open.sh, scripts/lztmux-remote-picker.sh).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remotePickNewPaneArgs builds the `new-pane` argv that floats
// lztmux-remote-pick over host, in the house float form shared by the
// bind-key y/p sites (config/tmux.conf.nix). bin is @remote_pick_bin's store
// path — reached by option rather than bare name, since the tmux server's
// PATH is frozen until a restart (#336) and a fresh script is absent from it
// until then. host is shell-quoted because tmux hands the command string on
// to the pane's own shell, not to us.
func remotePickNewPaneArgs(bin, host string) []string {
	return []string{
		"new-pane",
		"-x", "90%", "-y", "85%", "-X", "5%", "-Y", "8%", "-B", "heavy",
		bin + " " + shellQuote(host),
		";",
		"set", "-p", "@pane_label", "remote " + host,
	}
}

// emitEmptyRow is spec D3's "empty view is never blank" placeholder: an
// unselectable row shown only when emit mode has neither sessions nor zoxide
// dirs to offer.
func emitEmptyRow(tmuxOpts map[string]string) listItem {
	cDim := ansiFg(envOrMap("THM_SUBTEXT_0", tmuxOpts, "@thm_subtext_0", "#a6adc8"))
	reset := "\033[0m"
	text := "(no sessions, and no zoxide on this host)"
	return listItem{
		display:    cDim + text + reset,
		plain:      text,
		searchText: text,
	}
}
