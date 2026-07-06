package manifest

import (
	"regexp"
	"strings"
)

func Match(m Manifest, screenText, title string, altScreen bool) (string, bool) {
	if altScreen {
		return "", false
	}
	for _, r := range m.Rules {
		region := regionText(r.Region, screenText, title)
		if ruleMatches(r, region) {
			if r.State == "skip" {
				return "", false
			}
			return r.State, true
		}
	}
	return "", false
}

func regionText(sel, screenText, title string) string {
	switch {
	case sel == "title":
		return title
	case sel == "whole":
		return joinWrapped(screenText)
	case strings.HasPrefix(sel, "last_lines:"):
		n := atoiDefault(strings.TrimPrefix(sel, "last_lines:"), 1)
		return lastNonEmpty(screenText, n)
	default:
		return screenText
	}
}

func ruleMatches(r Rule, region string) bool {
	lc := strings.ToLower(region)
	for _, s := range r.Contains {
		if !strings.Contains(lc, strings.ToLower(s)) {
			return false
		}
	}
	if r.Regex != "" {
		if re, err := regexp.Compile("(?i)" + r.Regex); err != nil || !re.MatchString(region) {
			return false
		}
	}
	for _, n := range r.Not {
		if predMatches(n, lc) {
			return false
		}
	}
	return len(r.Contains) > 0 || r.Regex != "" || len(r.Not) > 0
}

func predMatches(p Predicate, lc string) bool {
	for _, s := range p.Contains {
		if !strings.Contains(lc, strings.ToLower(s)) {
			return false
		}
	}
	if p.Regex != "" {
		if re, err := regexp.Compile("(?i)" + p.Regex); err != nil || !re.MatchString(lc) {
			return false
		}
	}
	for _, n := range p.Not {
		if predMatches(n, lc) {
			return false
		}
	}
	return len(p.Contains) > 0 || p.Regex != "" || len(p.Not) > 0
}

func joinWrapped(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func lastNonEmpty(s string, n int) string {
	lines := strings.Split(s, "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append([]string{lines[i]}, kept...)
		}
	}
	return strings.Join(kept, "\n")
}

func atoiDefault(s string, d int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return d
	}
	return n
}
