package enrichstate

import "testing"

func TestReviewColor(t *testing.T) {
	cases := []struct {
		state, review string
		want          ColorRole
		ok            bool
	}{
		{"open", "approved", ColorSuccess, true},
		{"open", "changes_requested", ColorFailure, true},
		{"open", "review_required", ColorReviewRequired, true},
		{"open", "", 0, false},
		{"merged", "approved", 0, false},
		{"closed", "changes_requested", 0, false},
	}
	for _, c := range cases {
		got, ok := ReviewColor(c.state, c.review)
		if got != c.want || ok != c.ok {
			t.Errorf("ReviewColor(%q, %q) = %v, %v; want %v, %v", c.state, c.review, got, ok, c.want, c.ok)
		}
	}
}

func TestAutoMerge(t *testing.T) {
	if !AutoMerge("open", "1") || AutoMerge("merged", "1") || AutoMerge("open", "") {
		t.Error("AutoMerge must be true only for an open PR carrying the flag")
	}
}

func TestPie(t *testing.T) {
	cases := map[string]string{
		"0/8": PieSlices[0], "3/8": PieSlices[2], "7/8": PieSlices[6], "8/8": PieSlices[7], "1/3": PieSlices[2],
		"": "", "3": "", "0/0": "", "9/8": "", "a/b": "", "-1/8": "",
	}
	for in, want := range cases {
		if got := Pie(in); got != want {
			t.Errorf("Pie(%q) = %q, want %q", in, got, want)
		}
	}
}
