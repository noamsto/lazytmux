package controlmode

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// ClientCommandFlag is the third field of %begin/%end/%error for a block that a
// command THIS control client sent produced: tmux writes
// !!(state->flags & CMDQ_STATE_CONTROL) there (cmdq_fire_command). A hook on the
// remote runs its commands in our own command queue and so emits blocks flagged
// 0 — with lazytmux on the far side, one per after-new-window hook follows every
// new-window. Matching replies without this flag takes a hook's empty block as
// our reply and desynchronises every later round-trip (#276).
const ClientCommandFlag = 1

// Unescape decodes tmux control-mode %output data: bytes below 0x20 and the
// backslash are written as three-digit octal (\NNN); all else is literal.
// Operates on bytes — a UTF-8 rune may be split across two %output lines.
func Unescape(data string) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+3 < len(data) {
			// try three octal digits
			d0, d1, d2 := data[i+1], data[i+2], data[i+3]
			if isOctal(d0) && isOctal(d1) && isOctal(d2) {
				out = append(out, (d0-'0')<<6|(d1-'0')<<3|(d2-'0'))
				i += 3
				continue
			}
		}
		out = append(out, data[i])
	}
	return out
}

func isOctal(b byte) bool { return b >= '0' && b <= '7' }

type Kind int

const (
	Other Kind = iota
	Output
	Begin
	End
	Error
	WindowClose
	Exit
	LayoutChange
	WindowAdd
	WindowRenamed
	SessionWindowChanged
	SessionChanged
	WindowPaneChanged
	Pause
	Continue
)

type Line struct {
	Kind Kind
	Pane string
	Args []string
	Data []byte
	// Flags is the guard flags field, set on Begin/End/Error only; see
	// ClientCommandFlag.
	Flags int
}

func ParseLine(raw string) Line {
	if !strings.HasPrefix(raw, "%") {
		return Line{Kind: Other}
	}
	verb, rest, _ := strings.Cut(raw, " ")
	switch verb {
	case "%output":
		pane, data, _ := strings.Cut(rest, " ")
		return Line{Kind: Output, Pane: pane, Data: Unescape(data)}
	case "%extended-output":
		// Flow-control form of %output, emitted once pause-after is armed
		// (refresh-client -f pause-after=N): "%extended-output %pane <age-ms> :
		// <escaped-data>". Same payload escaping as %output; drop the pane's
		// pause age and treat it as ordinary output, or live output is lost.
		pane, r, _ := strings.Cut(rest, " ")
		_, data, _ := strings.Cut(r, " : ")
		return Line{Kind: Output, Pane: pane, Data: Unescape(data)}
	case "%begin":
		f := strings.Fields(rest)
		return Line{Kind: Begin, Args: f, Flags: guardFlags(f)}
	case "%end":
		f := strings.Fields(rest)
		return Line{Kind: End, Args: f, Flags: guardFlags(f)}
	case "%error":
		f := strings.Fields(rest)
		return Line{Kind: Error, Args: f, Flags: guardFlags(f)}
	case "%window-close":
		return Line{Kind: WindowClose, Args: strings.Fields(rest)}
	case "%exit":
		return Line{Kind: Exit, Args: strings.Fields(rest)}
	case "%layout-change":
		return Line{Kind: LayoutChange, Args: strings.Fields(rest)}
	case "%window-add":
		return Line{Kind: WindowAdd, Args: strings.Fields(rest)}
	case "%window-renamed":
		// name may contain spaces: id is the first token, the rest is the
		// whole name (kept in Data, not Fields-split).
		id, name, _ := strings.Cut(rest, " ")
		return Line{Kind: WindowRenamed, Args: []string{id}, Data: []byte(name)}
	case "%session-changed":
		// Emitted at attach and on every switch-client that moves this client.
		// Same shape as %window-renamed: id is the first token, the rest is the
		// whole session name, which may contain spaces.
		id, name, _ := strings.Cut(rest, " ")
		return Line{Kind: SessionChanged, Args: []string{id}, Data: []byte(name)}
	case "%session-window-changed":
		return Line{Kind: SessionWindowChanged, Args: strings.Fields(rest)}
	case "%window-pane-changed":
		return Line{Kind: WindowPaneChanged, Args: strings.Fields(rest)}
	case "%pause":
		return Line{Kind: Pause, Args: strings.Fields(rest)}
	case "%continue":
		return Line{Kind: Continue, Args: strings.Fields(rest)}
	default:
		return Line{Kind: Other}
	}
}

// guardFlags reads the flags field of a %begin/%end/%error guard line.
func guardFlags(fields []string) int {
	if len(fields) < 3 {
		return 0
	}
	n, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0
	}
	return n
}

type Reader struct {
	sc *bufio.Scanner
	// pending holds the lines a completed block yielded, in stream order, until
	// Next has handed them out one at a time.
	pending []Line
}

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &Reader{sc: sc}
}

func (rd *Reader) Next() (Line, bool) {
	for {
		if len(rd.pending) > 0 {
			l := rd.pending[0]
			rd.pending = rd.pending[1:]
			return l, true
		}
		if !rd.sc.Scan() {
			return Line{}, false
		}
		l := ParseLine(rd.sc.Text())
		if l.Kind != Begin {
			return l, true
		}
		rd.pending = rd.readBlock(l)
	}
}

// readBlock consumes a guarded block and returns, in stream order, each
// notification tmux emitted inside it followed by the block's own terminal line
// (Kind End or Error, Data = the command output alone). tmux emits the
// notifications a command causes inside that command's block, so they have to
// be lifted out — left in the body they read as that command's output.
//
// A body line is only taken for a notification when it parses as a known verb,
// so pane content that merely starts with '%' stays body.
func (rd *Reader) readBlock(begin Line) []Line {
	id := ""
	if len(begin.Args) > 0 {
		id = begin.Args[0]
	}
	var (
		out  []Line
		body []string
	)
	end := func(kind Kind, flags int) []Line {
		return append(out, Line{Kind: kind, Args: []string{id}, Flags: flags, Data: []byte(strings.Join(body, "\n"))})
	}
	for rd.sc.Scan() {
		raw := rd.sc.Text()
		t := ParseLine(raw)
		switch {
		case t.Kind == End || t.Kind == Error:
			return end(t.Kind, t.Flags)
		case t.Kind == Other || t.Kind == Begin:
			body = append(body, raw)
		default:
			out = append(out, t)
		}
	}
	return end(End, begin.Flags)
}
