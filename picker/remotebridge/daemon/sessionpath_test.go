package daemon

import "testing"

func TestReadSessionPath(t *testing.T) {
	cases := map[string]struct {
		script string
		want   string
	}{
		"plain path":      {"%begin 1 1 1\n/home/noams/git/tmux-og\n%end 1 1 1\n", "/home/noams/git/tmux-og"},
		"space is kept":   {"%begin 1 1 1\n/home/noams/my repo\n%end 1 1 1\n", "/home/noams/my repo"},
		"pipe drops":      {"%begin 1 1 1\n/home/a|b\n%end 1 1 1\n", ""},
		"markup drops":    {"%begin 1 1 1\n/home/#[fg=red]x\n%end 1 1 1\n", ""},
		"relative drops":  {"%begin 1 1 1\nhome/noams\n%end 1 1 1\n", ""},
		"error reply":     {"%begin 1 1 1\n%error 1 1 1\n", ""},
		"connection gone": {"", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rt, _ := scriptedRT(c.script)
			if got := readSessionPath(rt, "g6-work"); got != c.want {
				t.Errorf("readSessionPath = %q, want %q", got, c.want)
			}
		})
	}
}
