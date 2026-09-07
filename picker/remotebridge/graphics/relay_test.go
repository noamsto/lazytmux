package graphics

import "testing"

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
