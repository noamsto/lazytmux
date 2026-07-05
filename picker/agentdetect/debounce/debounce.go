package debounce

import "time"

type Debouncer struct {
	window   time.Duration
	lastMark time.Time
	fired    bool
	dirty    bool
}

func New(window time.Duration, _ func() time.Time) *Debouncer {
	return &Debouncer{window: window}
}

func (d *Debouncer) Mark(t time.Time) {
	d.lastMark = t
	d.dirty = true
	d.fired = false
}

func (d *Debouncer) Due(now time.Time) bool {
	if !d.dirty || d.fired {
		return false
	}
	if now.Sub(d.lastMark) >= d.window {
		d.fired = true
		d.dirty = false
		return true
	}
	return false
}
