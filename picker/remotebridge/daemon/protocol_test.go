package daemon

import (
	"bytes"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := []struct {
		t FrameType
		p []byte
	}{
		{FrameHello, []byte("%3")},
		{FrameSeed, []byte("\x1b[2J\x1b[Hhello")},
		{FrameOutput, []byte{0x00, 0xff, 0x1b, '\n'}}, // binary-safe
		{FrameInput, []byte("ls\r")},
	}
	for _, x := range payloads {
		if err := WriteFrame(&buf, x.t, x.p); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for i, x := range payloads {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", i, err)
		}
		if f.Type != x.t || !bytes.Equal(f.Payload, x.p) {
			t.Errorf("frame[%d] = %d/%q, want %d/%q", i, f.Type, f.Payload, x.t, x.p)
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Errorf("expected io.EOF after last frame, got %v", err)
	}
}

func TestResizeCodec(t *testing.T) {
	p := EncodeResize(210, 52)
	w, h, err := DecodeResize(p)
	if err != nil || w != 210 || h != 52 {
		t.Fatalf("DecodeResize = %d,%d,%v want 210,52,nil", w, h, err)
	}
	if _, _, err := DecodeResize([]byte{1, 2, 3}); err == nil {
		t.Error("DecodeResize(short) expected error")
	}
}
