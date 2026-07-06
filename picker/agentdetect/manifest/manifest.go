package manifest

import (
	"embed"
	"fmt"
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

type Manifest struct {
	ID            string   `toml:"id"`
	MatchCommands []string `toml:"match_commands"`
	Rules         []Rule   `toml:"rules"`
}

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
	for _, m := range ms {
		for _, c := range m.MatchCommands {
			if c == cmd {
				return m, true
			}
		}
	}
	return Manifest{}, false
}
