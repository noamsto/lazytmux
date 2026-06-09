package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// imageEntry is one line of the images/<pane>.jsonl manifest (shared format
// with the v1 viewer; only the fields the grid needs are decoded).
type imageEntry struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// parseManifest decodes JSONL bytes into entries, skipping blank/unparseable
// lines and entries with no path.
func parseManifest(data []byte) []imageEntry {
	var out []imageEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e imageEntry
		if json.Unmarshal([]byte(line), &e) != nil || e.Path == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// manifestPath is images/<pane>.jsonl (leading % stripped) under the claude
// status dir (CLAUDE_STATUS_DIR or /tmp/claude-status).
func manifestPath(pane string) string {
	dir := os.Getenv("CLAUDE_STATUS_DIR")
	if dir == "" {
		dir = "/tmp/claude-status"
	}
	return dir + "/images/" + strings.TrimPrefix(pane, "%") + ".jsonl"
}

// loadManifest reads and parses the pane's image manifest.
func loadManifest(pane string) []imageEntry {
	data, err := os.ReadFile(manifestPath(pane))
	if err != nil {
		return nil
	}
	return parseManifest(data)
}

type gridBackend int

const (
	backendKitty gridBackend = iota
	backendSymbols
)

// chooseGridBackend picks the grid renderer from the OUTER terminal name only.
// The kitty backend uses the raw graphics protocol (not `kitten icat`), so —
// unlike the v1 focus view — it does not depend on `kitten` being on PATH.
func chooseGridBackend(termname string) gridBackend {
	if strings.HasPrefix(termname, "xterm-kitty") || strings.HasPrefix(termname, "xterm-ghostty") {
		return backendKitty
	}
	return backendSymbols
}

// tmuxPassthrough wraps an escape sequence so tmux forwards it to the outer
// terminal verbatim: \ePtmux;<seq with every ESC doubled>\e\\
func tmuxPassthrough(seq string) string {
	if os.Getenv("TMUX") == "" {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// transmitVirtual stores a PNG (by file path) as a virtual placement (U=1) under
// a known id, sized to cols x rows cells, emitting NO visible cells. Placeholder
// cells in the View reference it.
func transmitVirtual(id int, path string, cols, rows int) string {
	enc := base64.StdEncoding.EncodeToString([]byte(path))
	return tmuxPassthrough(fmt.Sprintf("\x1b_Gi=%d,a=T,U=1,f=100,c=%d,r=%d,t=f;%s\x1b\\",
		id, cols, rows, enc))
}

// deleteAll removes all stored images + placements (kitty graphics a=d,d=A).
func deleteAll() string { return tmuxPassthrough("\x1b_Ga=d,d=A\x1b\\") }

// placeholderRune is kitty's Unicode image placeholder (U+10EEEE).
const placeholderRune = "\U0010EEEE"

// rowColDiacritics are kitty's rowcolumn-diacritics (combining class 230). The
// Nth entry encodes row/column index N. Source: kitty
// share/doc/kitty/.../rowcolumn-diacritics.txt (full list is 297 entries; this
// prefix covers any thumbnail cell extent we produce — block dims are clamped to
// len(rowColDiacritics)).
var rowColDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
}

// placeholderBlock builds h lines of w placeholder cells referencing image id.
// The image id is encoded as the 24-bit cell foreground color, set once per row.
// w and h are clamped to len(rowColDiacritics) by the caller (geometry).
func placeholderBlock(id, w, h int) string {
	var b strings.Builder
	for r := 0; r < h; r++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
		for c := 0; c < w; c++ {
			b.WriteString(placeholderRune)
			b.WriteRune(rowColDiacritics[r])
			b.WriteRune(rowColDiacritics[c])
		}
		b.WriteString("\x1b[39m")
		if r < h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// maxCellDim is the largest thumbnail cell extent placeholderBlock supports.
var maxCellDim = len(rowColDiacritics)

// symbolsArgs builds the chafa arg list for a block-art thumbnail of the given
// cell size. No --clear (that would clear the whole screen per file).
func symbolsArgs(path string, w, h int) []string {
	return []string{"-f", "symbols", "--size", fmt.Sprintf("%dx%d", w, h), path}
}

// symbolsBlock renders a path to chafa symbols text sized to w x h cells, or a
// textual placeholder on failure. chafa wraps output in cursor hide/show
// (\e[?25l … \e[?25h); strip them so they don't leak into the TUI's frame.
func symbolsBlock(path string, w, h int) string {
	out, err := exec.Command("chafa", symbolsArgs(path, w, h)...).Output()
	if err != nil {
		return "[img]"
	}
	s := string(out)
	s = strings.ReplaceAll(s, "\x1b[?25l", "")
	s = strings.ReplaceAll(s, "\x1b[?25h", "")
	return strings.TrimRight(s, "\n")
}
