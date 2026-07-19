package controlmode

import (
	"fmt"
	"strconv"
	"strings"
)

type PaneCell struct {
	ID         string
	W, H, X, Y int
}

type Layout struct {
	W, H  int
	Panes []PaneCell
}

// ParseLayout parses a tmux layout string (window_layout / %layout-change payload).
// Panes are returned depth-first in cell order — the order local panes must be
// created in, since select-layout assigns panes to cells positionally.
func ParseLayout(s string) (Layout, error) {
	// Strip the leading "<checksum>," prefix.
	_, body, ok := strings.Cut(s, ",")
	if !ok {
		return Layout{}, fmt.Errorf("layout: no checksum separator in %q", s)
	}
	p := &layoutParser{s: body}
	root, err := p.cell()
	if err != nil {
		return Layout{}, err
	}
	if p.pos != len(p.s) {
		return Layout{}, fmt.Errorf("layout: trailing data %q", p.s[p.pos:])
	}
	var out Layout
	out.W, out.H = root.w, root.h
	collectLeaves(root, &out.Panes)
	if len(out.Panes) == 0 {
		return Layout{}, fmt.Errorf("layout: no panes in %q", s)
	}
	return out, nil
}

type node struct {
	w, h, x, y int
	id         string  // set on leaves
	children   []*node // set on splits
}

type layoutParser struct {
	s   string
	pos int
}

// cell := WxH,X,Y [ , id | { children } | [ children ] ]
func (p *layoutParser) cell() (*node, error) {
	n := &node{}
	var err error
	if n.w, err = p.intUntil('x'); err != nil {
		return nil, err
	}
	if n.h, err = p.intUntil(','); err != nil {
		return nil, err
	}
	if n.x, err = p.intUntil(','); err != nil {
		return nil, err
	}
	// Y runs until one of , { [  } ]  or end.
	n.y, err = p.intUntilAny(",{[}]")
	if err != nil {
		return nil, err
	}
	if p.pos >= len(p.s) {
		return nil, fmt.Errorf("layout: unexpected end after cell")
	}
	switch p.s[p.pos] {
	case ',':
		p.pos++ // consume ','
		n.id = "%" + p.numRun()
	case '{':
		return p.split(n, '{', '}')
	case '[':
		return p.split(n, '[', ']')
	}
	return n, nil
}

func (p *layoutParser) split(n *node, open, close byte) (*node, error) {
	p.pos++ // consume open
	for {
		c, err := p.cell()
		if err != nil {
			return nil, err
		}
		n.children = append(n.children, c)
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("layout: unterminated split")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case close:
			p.pos++
			return n, nil
		default:
			return nil, fmt.Errorf("layout: bad split delimiter %q", p.s[p.pos])
		}
	}
}

func (p *layoutParser) numRun() string {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *layoutParser) intUntil(sep byte) (int, error) {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] != sep {
		p.pos++
	}
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("layout: expected %q", sep)
	}
	v, err := strconv.Atoi(p.s[start:p.pos])
	p.pos++ // consume sep
	return v, err
}

func (p *layoutParser) intUntilAny(seps string) (int, error) {
	start := p.pos
	for p.pos < len(p.s) && !strings.ContainsRune(seps, rune(p.s[p.pos])) {
		p.pos++
	}
	return strconv.Atoi(p.s[start:p.pos])
}

func collectLeaves(n *node, out *[]PaneCell) {
	if len(n.children) == 0 {
		*out = append(*out, PaneCell{ID: n.id, W: n.w, H: n.h, X: n.x, Y: n.y})
		return
	}
	for _, c := range n.children {
		collectLeaves(c, out)
	}
}
