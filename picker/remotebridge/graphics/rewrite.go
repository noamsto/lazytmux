package graphics

import (
	"encoding/base64"
	"fmt"
)

// Localizer turns a path on the remote host into a path the local terminal can
// read. Injected so the policy below is testable without ssh.
type Localizer interface {
	Localize(remotePath string) (localPath string, err error)
}

// Rewrite applies the localisation policy to one sequence.
//
// The governing rule (spec D7) is that a store whose payload could not be
// localised is DROPPED, never forwarded: a stale local path renders the wrong
// image, where a missing one renders blank and self-heals on the sender's next
// repaint.
func Rewrite(q *Seq, l Localizer) (out *Seq, drop bool, err error) {
	switch q.Get("t") {
	case "f", "t":
		remote, derr := base64.StdEncoding.DecodeString(string(q.Payload))
		if derr != nil {
			return nil, true, fmt.Errorf("payload is not base64: %w", derr)
		}
		local, ferr := l.Localize(string(remote))
		if ferr != nil {
			return nil, true, fmt.Errorf("localise %s: %w", remote, ferr)
		}
		cp := *q
		cp.Payload = []byte(base64.StdEncoding.EncodeToString([]byte(local)))
		return &cp, false, nil
	case "s":
		// Shared memory is host-local by definition.
		return nil, true, nil
	default:
		// t=d carries its own bytes; a=d and friends carry no payload at all.
		return q, false, nil
	}
}

// isStore reports whether q transmits image data under an id — the sequences
// coalescing may supersede.
func isStore(q *Seq) bool {
	switch q.Get("a") {
	case "T", "t":
		return q.Get("i") != ""
	}
	return false
}

// isDelete reports whether q deletes an image by id.
func isDelete(q *Seq) bool { return q.Get("a") == "d" && q.Get("i") != "" }
