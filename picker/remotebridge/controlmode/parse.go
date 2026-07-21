package controlmode

import (
	"bufio"
	"io"
	"strings"
)

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
	WindowPaneChanged
	Pause
	Continue
)

type Line struct {
	Kind Kind
	Pane string
	Args []string
	Data []byte
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
		return Line{Kind: Begin, Args: strings.Fields(rest)}
	case "%end":
		return Line{Kind: End, Args: strings.Fields(rest)}
	case "%error":
		return Line{Kind: Error, Args: strings.Fields(rest)}
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

type Reader struct{ sc *bufio.Scanner }

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &Reader{sc: sc}
}

func (rd *Reader) Next() (Line, bool) {
	for rd.sc.Scan() {
		l := ParseLine(rd.sc.Text())
		if l.Kind != Begin {
			return l, true
		}
		// Accumulate the reply body until %end/%error (matching id in Args[0]).
		id := ""
		if len(l.Args) > 0 {
			id = l.Args[0]
		}
		var body []string
		for rd.sc.Scan() {
			raw := rd.sc.Text()
			t := ParseLine(raw)
			if t.Kind == End || t.Kind == Error {
				return Line{Kind: t.Kind, Args: []string{id}, Data: []byte(strings.Join(body, "\n"))}, true
			}
			body = append(body, raw)
		}
		return Line{Kind: End, Args: []string{id}, Data: []byte(strings.Join(body, "\n"))}, true
	}
	return Line{}, false
}
