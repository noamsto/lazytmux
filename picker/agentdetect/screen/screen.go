// Package screen wraps a headless VT emulator behind a small stable interface,
// so the parser and manifest matcher never depend on the concrete library.
package screen

import "github.com/charmbracelet/x/vt"

type Screen interface {
	Feed(b []byte)
	Text() string
	Title() string
	AltScreen() bool
}

type vtScreen struct {
	e     *vt.Emulator
	title string
}

func New(cols, rows int) Screen {
	e := vt.NewEmulator(cols, rows)
	s := &vtScreen{e: e}
	e.SetCallbacks(vt.Callbacks{Title: func(t string) { s.title = t }})
	return s
}

func (s *vtScreen) Feed(b []byte)   { _, _ = s.e.Write(b) }
func (s *vtScreen) Text() string    { return s.e.String() }
func (s *vtScreen) Title() string   { return s.title }
func (s *vtScreen) AltScreen() bool { return s.e.IsAltScreen() }
