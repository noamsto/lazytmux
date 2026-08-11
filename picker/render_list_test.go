package main

import (
	"strings"
	"testing"
)

func TestRenderHintsRemoteRow(t *testing.T) {
	m := tuiModel{width: 200, visible: []listItem{{remoteHost: "tp-g6"}}, cursor: 0}
	hints := stripANSI(m.renderHints())
	if !strings.Contains(hints, "^o:remote") {
		t.Errorf("hints = %q, want ^o:remote on a remote row", hints)
	}
}

func TestRenderHintsNonRemoteRow(t *testing.T) {
	m := tuiModel{width: 200, visible: []listItem{{target: "lazytmux"}}, cursor: 0}
	hints := stripANSI(m.renderHints())
	if strings.Contains(hints, "^o") {
		t.Errorf("hints = %q, must not advertise ^o off a remote row", hints)
	}
}

func TestRenderHintsEmitMode(t *testing.T) {
	m := tuiModel{width: 200, emitPath: "/tmp/emit", visible: []listItem{{target: "lazytmux"}}, cursor: 0}
	hints := stripANSI(m.renderHints())
	if !strings.Contains(hints, "enter:pick") {
		t.Errorf("hints = %q, want enter:pick in emit mode", hints)
	}
	if strings.Contains(hints, "^x") {
		t.Errorf("hints = %q, must not advertise ^x in emit mode", hints)
	}
}

func TestRenderHintsNonEmitMode(t *testing.T) {
	m := tuiModel{width: 200, visible: []listItem{{target: "lazytmux"}}, cursor: 0}
	hints := stripANSI(m.renderHints())
	if !strings.Contains(hints, "enter:open") {
		t.Errorf("hints = %q, want enter:open outside emit mode", hints)
	}
	if !strings.Contains(hints, "^x:kill") {
		t.Errorf("hints = %q, want ^x:kill outside emit mode", hints)
	}
}
