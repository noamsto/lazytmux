package daemon

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/noamsto/tmux-og/picker/remotebridge/controlmode"
)

// sessionPathRe excludes '|' where dirRe excludes whitespace: the picker reads
// the stamp back inside a '|'-delimited list-panes row, where a pipe shifts every
// later field, while a space in a session path is both legal and harmless there.
var sessionPathRe = regexp.MustCompile(`^/[^|]*$`)

// readSessionPath fetches the remote session's own #{session_path}. The local
// mirror session's session_path is whatever cwd og-remote-open ran from — it
// creates the session with no -c, since the remote directory need not exist
// here — so the picker would otherwise show the launcher's directory for a
// mirror. An unusable reply is "", which the picker renders as no path at all.
func readSessionPath(rt roundTrip, sess string) string {
	l, ok := one(rt, fmt.Sprintf("display-message -p -t %s -F '#{session_path}'", tmuxQuote(sess)))
	if !ok || l.Kind == controlmode.Error {
		return ""
	}
	p := strings.TrimRight(string(l.Data), "\r\n")
	return matching(cleanLabelValueExact(p, dirMaxRunes), sessionPathRe)
}
