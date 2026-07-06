package debounce

import (
	"testing"
	"time"
)

func TestDueOnlyAfterQuietWindow(t *testing.T) {
	base := time.Unix(1000, 0)
	d := New(80*time.Millisecond, nil)
	d.Mark(base)
	if d.Due(base.Add(50 * time.Millisecond)) {
		t.Fatal("should not be due at 50ms")
	}
	if !d.Due(base.Add(80 * time.Millisecond)) {
		t.Fatal("should be due at 80ms")
	}
	if d.Due(base.Add(120 * time.Millisecond)) {
		t.Fatal("should not re-fire without a new Mark")
	}
	d.Mark(base.Add(200 * time.Millisecond))
	if !d.Due(base.Add(300 * time.Millisecond)) {
		t.Fatal("should be due again after a new Mark + window")
	}
}
