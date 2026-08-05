package graphics

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
)

// Localizer turns a path on the remote host into a path the local terminal can
// read. The context bounds the fetch: Filter holds the pane's byte stream while
// this runs, so an unbounded call would freeze the pane (spec D4). Injected so
// the policy below is testable without ssh.
type Localizer interface {
	Localize(ctx context.Context, remotePath string) (localPath string, err error)
}

// Rewrite applies the localisation policy to one sequence. The returned *Seq
// is the input pointer in the pass-through case and a fresh copy in the
// localising case — callers must treat it as read-only either way.
//
// The governing rule (spec D7) is that a store whose payload could not be
// localised is DROPPED, never forwarded: a stale local path renders the wrong
// image, where a missing one renders blank and self-heals on the sender's next
// repaint. A fetch that outruns ctx is just another such failure.
func Rewrite(ctx context.Context, q *Seq, l Localizer) (out *Seq, drop bool, err error) {
	switch q.Get("t") {
	case "f", "t":
		remote, derr := base64.StdEncoding.DecodeString(string(q.Payload))
		if derr != nil {
			return nil, true, fmt.Errorf("payload is not base64: %w", derr)
		}
		local, ferr := l.Localize(ctx, string(remote))
		if ferr != nil {
			return nil, true, fmt.Errorf("localise %s: %w", remote, ferr)
		}
		cp := *q
		cp.Payload = []byte(base64.StdEncoding.EncodeToString([]byte(local)))
		// t=t asks the terminal to delete the file once it has read it. Our
		// payload now names the LOCAL cache copy, so honouring it would have the
		// local terminal unlink what the fetcher just wrote — invalidating the
		// cache behind its back and stranding any later re-emit that still
		// references it. The delete-after-read contract was with the sender's own
		// temp file, which never crosses the bridge, so emit t=f.
		if q.Get("t") == "t" {
			cp.Keys = setKey(q.Keys, "t", "f")
		}
		return &cp, false, nil
	case "s":
		// Shared memory is host-local by definition.
		return nil, true, nil
	default:
		// t=d carries its own bytes; a=d and friends carry no payload at all.
		return q, false, nil
	}
}

// setKey returns a copy of keys with k's value replaced. It never edits in
// place: Rewrite's shallow copy aliases this slice, so an in-place write would
// reach through into the caller's Seq.
func setKey(keys []byte, k, v string) []byte {
	parts := bytes.Split(keys, []byte{','})
	out := make([][]byte, 0, len(parts))
	for _, kv := range parts {
		if i := bytes.IndexByte(kv, '='); i >= 0 && string(kv[:i]) == k {
			kv = []byte(k + "=" + v)
		}
		out = append(out, kv)
	}
	return bytes.Join(out, []byte{','})
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
