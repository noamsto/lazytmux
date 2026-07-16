package controlmode

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
