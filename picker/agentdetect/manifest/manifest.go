package manifest

import (
	"embed"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"
)

//go:embed all:manifests
var manifestFS embed.FS

type Predicate struct {
	Contains []string    `toml:"contains"`
	Regex    string      `toml:"regex"`
	Not      []Predicate `toml:"not"`
}

type Rule struct {
	State    string      `toml:"state"`
	Priority int         `toml:"priority"`
	Region   string      `toml:"region"`
	Contains []string    `toml:"contains"`
	Regex    string      `toml:"regex"`
	Not      []Predicate `toml:"not"`
}

// Flag is an orthogonal, counted observation about a pane — a background
// shell still running, say — as opposed to Rule's single winning state. Its
// Regex must hold one capture group holding a decimal count; a flag whose
// count is zero or absent is simply not reported.
//
// Kept separate from Rule because the two answer different questions: rules
// compete (first match wins) while flags accumulate, and a pane can carry a
// flag in any state.
type Flag struct {
	Name   string `toml:"name"`
	Region string `toml:"region"`
	Regex  string `toml:"regex"`
}

type Manifest struct {
	ID            string   `toml:"id"`
	MatchCommands []string `toml:"match_commands"`
	Rules         []Rule   `toml:"rules"`
	Flags         []Flag   `toml:"flags"`
}

var wrappedRe = regexp.MustCompile(`^\.(.*)-wrapped$`)

func Load() ([]Manifest, error) {
	entries, err := manifestFS.ReadDir("manifests")
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		b, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := toml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		sort.SliceStable(m.Rules, func(i, j int) bool { return m.Rules[i].Priority > m.Rules[j].Priority })
		out = append(out, m)
	}
	return out, nil
}

func ForCommand(ms []Manifest, cmd string) (Manifest, bool) {
	if m := wrappedRe.FindStringSubmatch(cmd); m != nil {
		cmd = m[1]
	}
	for _, m := range ms {
		if slices.Contains(m.MatchCommands, cmd) {
			return m, true
		}
	}
	return Manifest{}, false
}
