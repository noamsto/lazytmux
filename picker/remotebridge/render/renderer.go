package render

import (
	"io"

	"github.com/noamsto/lazytmux/picker/remotebridge/wire"
)

// Run drives one renderer over conn: sends Hello(paneID), then paints Seed/Output
// to out and forwards in -> Input frames, until conn EOF. rawSetup is injected so
// tests can skip real tty setup; production passes render.MakeRaw(fd). recordResize
// is invoked with the decoded dims of each FrameResize; size stays daemon-authoritative
// so the renderer only records them (for its painter) and never resizes anything.
func Run(conn io.ReadWriteCloser, paneID string, in io.Reader, out io.Writer, rawSetup func() (func() error, error), recordResize func(w, h int)) error {
	if err := wire.WriteFrame(conn, wire.FrameHello, []byte(paneID)); err != nil {
		return err
	}
	restore, err := rawSetup()
	if err != nil {
		return err
	}
	if restore != nil {
		defer restore()
	}

	// stdin -> Input frames
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := wire.WriteFrame(conn, wire.FrameInput, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// daemon frames -> paint
	for {
		f, err := wire.ReadFrame(conn)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch f.Type {
		case wire.FrameSeed, wire.FrameOutput:
			if _, werr := out.Write(f.Payload); werr != nil {
				return werr
			}
		case wire.FrameResize:
			// Size is daemon-authoritative — the renderer records the dims
			// for its painter but never resizes anything (no refresh-client).
			if w, h, err := wire.DecodeResize(f.Payload); err == nil && recordResize != nil {
				recordResize(w, h)
			}
		}
	}
}
