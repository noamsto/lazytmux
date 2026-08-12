// Package screen wraps a headless VT emulator behind a small stable interface,
// so the parser and manifest matcher never depend on the concrete library.
package screen

import (
	"io"

	"github.com/charmbracelet/x/vt"
)

type Screen interface {
	Feed(b []byte)
	Text() string
	Title() string
	AltScreen() bool
	Close()
}

type vtScreen struct {
	e     *vt.Emulator
	title string
	done  chan struct{}
}

func New(cols, rows int) Screen {
	e := vt.NewEmulator(cols, rows)
	done := make(chan struct{})
	// The emulator answers terminal queries by writing to an internal
	// io.Pipe; with no reader the next query blocks Write — and with it
	// the watcher's only sample loop — forever (#251).
	go func() {
		_, _ = io.Copy(io.Discard, e)
		close(done)
	}()
	s := &vtScreen{e: e, done: done}
	e.SetCallbacks(vt.Callbacks{Title: func(t string) { s.title = t }})
	return s
}

func (s *vtScreen) Feed(b []byte)   { _, _ = s.e.Write(b) }
func (s *vtScreen) Text() string    { return s.e.String() }
func (s *vtScreen) Title() string   { return s.title }
func (s *vtScreen) AltScreen() bool { return s.e.IsAltScreen() }

// Close EOFs the emulator's reply pipe so the drain goroutine exits. vt's
// closed flag is unsynchronized against the drain goroutine's Read — benign
// here (the pipe's own CloseWithError is what actually wakes Read), but it
// means tests must not call Close under -race.
func (s *vtScreen) Close() {
	_ = s.e.Close()
	<-s.done
}
