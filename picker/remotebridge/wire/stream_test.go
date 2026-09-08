package wire

import (
	"bytes"
	"testing"
)

func TestWriteStreamAboveCapSplitsAndRoundTrips(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, maxFrameSize+(1<<20)) // 17 MiB, one buffer
	var buf bytes.Buffer
	if err := WriteStream(&buf, FrameOutput, payload); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	var got []byte
	frames := 0
	for {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", frames, err)
		}
		if f.Type != FrameOutput {
			t.Fatalf("frame[%d].Type = %d, want FrameOutput", frames, f.Type)
		}
		if len(f.Payload) >= maxFrameSize {
			t.Fatalf("frame[%d] payload len %d, want strictly below %d", frames, len(f.Payload), maxFrameSize)
		}
		got = append(got, f.Payload...)
		frames++
		if len(got) >= len(payload) {
			break
		}
	}
	if frames < 2 {
		t.Fatalf("frames = %d, want at least 2 for a payload above the cap", frames)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("concatenated frame payloads != input payload")
	}
	if buf.Len() != 0 {
		t.Fatalf("%d trailing bytes after consuming all frames, want 0", buf.Len())
	}
}

func TestWriteStreamBelowCapEmitsOneFrame(t *testing.T) {
	payload := []byte("\x1b[2J\x1b[Hhello")

	var buf bytes.Buffer
	if err := WriteStream(&buf, FrameSeed, payload); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	var want bytes.Buffer
	if err := WriteFrame(&want, FrameSeed, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), want.Bytes()) {
		t.Fatal("WriteStream(below cap) bytes != WriteFrame bytes")
	}

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Type != FrameSeed || !bytes.Equal(f.Payload, payload) {
		t.Errorf("frame = %d/%q, want %d/%q", f.Type, f.Payload, FrameSeed, payload)
	}
	if buf.Len() != 0 {
		t.Fatalf("%d trailing bytes after the single frame, want 0", buf.Len())
	}
}

func TestWriteStreamEmptyPayloadEmitsOneFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStream(&buf, FrameOutput, nil); err != nil {
		t.Fatalf("WriteStream: %v", err)
	}

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if f.Type != FrameOutput || len(f.Payload) != 0 {
		t.Errorf("frame = %d/%q, want %d/empty", f.Type, f.Payload, FrameOutput)
	}
	if buf.Len() != 0 {
		t.Fatalf("%d trailing bytes after the single frame, want 0", buf.Len())
	}
}

func TestWriteStreamRejectsStructuredFrameTypes(t *testing.T) {
	for _, ft := range []FrameType{FrameResize, FrameCtl, FrameHello, FrameInput, FrameCtlAck} {
		var buf bytes.Buffer
		if err := WriteStream(&buf, ft, []byte("payload")); err == nil {
			t.Errorf("WriteStream(%d) = nil error, want error", ft)
		}
		if buf.Len() != 0 {
			t.Errorf("WriteStream(%d) wrote %d bytes on rejection, want 0", ft, buf.Len())
		}
	}
}
