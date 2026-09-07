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

// BatchLocalizer is the Localizer's optional batch form, used by Filter when
// the localizer provides it: every distinct path one output batch needs,
// fetched concurrently under the batch's single deadline. A carousel
// re-transmit stores the preview plus every filmstrip thumbnail in one batch,
// and fetching those serialized held the pane's stream for N round-trips
// (#556). locals and errs are indexed parallel to remotes.
type BatchLocalizer interface {
	LocalizeBatch(ctx context.Context, remotes []string) (locals []string, errs []error)
}

// Rewrite applies the localisation policy to one sequence. The returned *Seq
// is the input pointer in the pass-through case and a fresh copy in the
// localising case — callers must treat it as read-only either way.
//
// Postcondition: out is nil if and only if drop is true. Filter dereferences
// the result on every non-drop path, so a branch returning (nil, false, …)
// would panic the pump goroutine rather than fail a test.
//
// The governing rule (spec D7) is that a store whose payload could not be
// localised is DROPPED, never forwarded: a stale local path renders the wrong
// image, where a missing one renders blank and self-heals on the sender's next
// repaint. A fetch that outruns ctx is just another such failure.
func Rewrite(ctx context.Context, q *Seq, l Localizer) (out *Seq, drop bool, err error) {
	return rewrite(q, func(remote string) (string, error) { return l.Localize(ctx, remote) })
}

// rewrite is the single home of the localisation policy: Rewrite fetches live,
// the proxy's batch path answers from its prefetch map. The postcondition and
// the D7 drop rule above govern both.
func rewrite(q *Seq, localize func(remote string) (string, error)) (out *Seq, drop bool, err error) {
	switch q.Get("t") {
	case "f", "t":
		remote, derr := base64.StdEncoding.DecodeString(string(q.Payload))
		if derr != nil {
			return nil, true, fmt.Errorf("payload is not base64: %w", derr)
		}
		local, ferr := localize(string(remote))
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
