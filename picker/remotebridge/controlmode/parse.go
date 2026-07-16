package controlmode

import "strings"

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
	default:
		return Line{Kind: Other}
	}
}
