package graphics

import (
	"sync"
	"testing"
)

func TestRelayFromTermFeatures(t *testing.T) {
	tests := []struct {
		name  string
		feats string
		want  bool
	}{
		{"contains sixel token", "bpaste,focus,RGB,sixel,title", true},
		{"sixel-free list", "bpaste,focus,RGB,title", false},
		{"substring only, not a whole token", "bpaste,nosixel,sixelfoo", false},
		{"empty string", "", false},
		{"whitespace around the token", "bpaste, sixel ,title", true},
		{"only whitespace", "   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelayFromTermFeatures(tt.feats).Sixel(); got != tt.want {
				t.Fatalf("Sixel() = %v, want %v for feats=%q", got, tt.want, tt.feats)
			}
		})
	}
}

func TestRelayStringGrammar(t *testing.T) {
	if got := RelayFromTermFeatures("sixel").String(); got != "sixel" {
		t.Fatalf("String() = %q, want %q", got, "sixel")
	}
	if got := RelayFromTermFeatures("bpaste").String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
	if zero := (Relay{}); zero.String() != "" || zero.Sixel() {
		t.Fatal("zero value must mean no relayable capability")
	}
}

func TestRelaySourceNilReceiverIsRelayOff(t *testing.T) {
	var s *RelaySource
	if got := s.Load(); got.Sixel() {
		t.Fatal("nil *RelaySource must Load() the zero (relay-off) Relay")
	}
}

func TestRelaySourceZeroValueIsRelayOff(t *testing.T) {
	var s RelaySource
	if got := s.Load(); got.Sixel() {
		t.Fatal("zero-value RelaySource must Load() the zero (relay-off) Relay")
	}
}

func TestRelaySourceStoreLoadRoundTripsRaw(t *testing.T) {
	s := NewRelaySource(RelayFromTermFeatures("bpaste"))

	want := RelayFromTermFeatures("bpaste,focus,sixel,title")
	s.Store(want)

	got := s.Load()
	if got.Sixel() != want.Sixel() {
		t.Fatalf("Sixel() = %v, want %v", got.Sixel(), want.Sixel())
	}
	if got.raw != want.raw {
		t.Fatalf("raw = %q, want %q — raw must round-trip for Proxy.Filter's drop diagnostic", got.raw, want.raw)
	}
}

func TestRelaySourceConcurrentLoadStore(t *testing.T) {
	s := NewRelaySource(Relay{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			feats := "bpaste"
			if i%2 == 0 {
				feats = "bpaste,sixel"
			}
			s.Store(RelayFromTermFeatures(feats))
			_ = s.Load()
		}(i)
	}
	wg.Wait()
}
