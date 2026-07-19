// Package wire is the leaf package for the daemon<->renderer framed
// protocol. It exists separately from daemon so that render (which paints
// frames) and daemon (which now also produces seed bytes via render.Seed)
// can both depend on it without an import cycle.
package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

type FrameType byte

const (
	FrameHello  FrameType = 1 // renderer->daemon: payload = remote pane id ("%N")
	FrameSeed   FrameType = 2 // daemon->renderer: payload = initial screen bytes
	FrameOutput FrameType = 3 // daemon->renderer: payload = live pane bytes
	FrameResize FrameType = 4 // daemon->renderer: payload = 8 bytes (w,h uint32 BE)
	FrameInput  FrameType = 5 // renderer->daemon: payload = stdin bytes
)

type Frame struct {
	Type    FrameType
	Payload []byte
}

func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// maxFrameSize bounds the wire-supplied payload length so a corrupt or
// malicious header can't trigger a multi-GiB allocation. Far above any real
// seed/output frame.
const maxFrameSize = 16 << 20 // 16 MiB

func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err // io.EOF passes through on clean boundary
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrameSize {
		return Frame{}, fmt.Errorf("frame length %d exceeds max %d", n, maxFrameSize)
	}
	p := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, p); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return Frame{}, err
		}
	}
	return Frame{Type: FrameType(hdr[0]), Payload: p}, nil
}

func EncodeResize(w, h int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:], uint32(w))
	binary.BigEndian.PutUint32(b[4:], uint32(h))
	return b
}

func DecodeResize(p []byte) (int, int, error) {
	if len(p) != 8 {
		return 0, 0, fmt.Errorf("resize payload len %d, want 8", len(p))
	}
	return int(binary.BigEndian.Uint32(p[0:])), int(binary.BigEndian.Uint32(p[4:])), nil
}
