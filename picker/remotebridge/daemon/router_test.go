package daemon

import (
	"bytes"
	"testing"
)

func TestRouterRoutesByPane(t *testing.T) {
	r := NewRouter()
	var a, b bytes.Buffer
	r.Register("%1", &a)
	r.Register("%2", &b)

	r.Route("%1", []byte("one"))
	r.Route("%2", []byte("two"))
	r.Route("%1", []byte("-more"))
	r.Route("%9", []byte("dropped")) // unregistered: silently dropped

	if a.String() != "one-more" {
		t.Errorf("pane %%1 got %q, want %q", a.String(), "one-more")
	}
	if b.String() != "two" {
		t.Errorf("pane %%2 got %q, want %q", b.String(), "two")
	}

	r.Unregister("%1")
	r.Route("%1", []byte("after-unregister"))
	if a.String() != "one-more" {
		t.Errorf("pane %%1 received after unregister: %q", a.String())
	}
}
