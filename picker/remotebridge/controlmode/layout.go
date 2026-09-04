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
	// Floats holds panes that are floating (tmux next-3.8's trailing
	// "<...>" layout section — see ParseLayout). Each cell is the INNER box
	// and equals the pane's usable size, so it feeds a renderer's dims
	// unconverted; only tmux's create/resize/move flags take the border
	// inset. Mirrored by the daemon as local floating panes.
	Floats []PaneCell
	// Raw is the layout string for the TILED panes only, incl. checksum
	// prefix, safe to feed to `select-layout` (tmux rejects its own
	// float-bearing layout strings when replayed — see ParseLayout).
	// Identical to the input string when the window has no floats.
	Raw string
}

// ParseLayout parses a tmux layout string (window_layout / %layout-change payload).
// Panes are returned depth-first in cell order — the order local panes must be
// created in, since select-layout assigns panes to cells positionally.
//
// tmux next-3.8 encodes a window's floating panes two ways in the same
// string: as ordinary leaves interleaved into the tiled split tree (so a
// naive walk would create phantom local panes for them and feed a
// select-layout string tmux itself rejects — verified: tmux errors
// "invalid layout" replaying its own output), and again as a trailing
// "<WxH,X,Y,id[,WxH,X,Y,id...]>" section listing exactly the float ids.
// tmux 3.7b has no such trailing section (and no floats in the tree either).
// ParseLayout uses the trailing section as the authoritative float id set,
// prunes those leaves out of the tree (collapsing any split left with a
// single child), and recomputes the checksum for the pruned body so Raw
// stays select-layout-safe.
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
	var floats []PaneCell
	if p.pos < len(p.s) && p.s[p.pos] == '<' {
		floats, err = p.floatSection()
		if err != nil {
			return Layout{}, err
		}
	}
	if p.pos != len(p.s) {
		return Layout{}, fmt.Errorf("layout: trailing data %q", p.s[p.pos:])
	}
	var out Layout
	out.W, out.H = root.w, root.h
	out.Floats = floats

	if len(floats) == 0 {
		out.Raw = s
	} else {
		floatIDs := make(map[string]bool, len(floats))
		for _, f := range floats {
			floatIDs[f.ID] = true
		}
		tiled := pruneFloats(root, floatIDs)
		if tiled == nil {
			return Layout{}, fmt.Errorf("layout: no tiled panes in %q", s)
		}
		var sb strings.Builder
		writeCell(tiled, &sb)
		out.Raw = fmt.Sprintf("%04x,%s", layoutChecksum(sb.String()), sb.String())
		root = tiled
	}

	collectLeaves(root, &out.Panes)
	if len(out.Panes) == 0 {
		return Layout{}, fmt.Errorf("layout: no panes in %q", s)
	}
	return out, nil
}

type node struct {
	w, h, x, y int
	id         string  // set on leaves
	kind       byte    // '{' (horizontal) or '[' (vertical); set on splits
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
		n.kind = '{'
		return p.split(n, '}')
	case '[':
		n.kind = '['
		return p.split(n, ']')
	}
	return n, nil
}

func (p *layoutParser) split(n *node, end byte) (*node, error) {
	p.pos++ // consume the already-matched opening delimiter
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
		case end:
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

// floatSection parses the trailing "<cell[,cell...]>" section tmux next-3.8
// appends after the tiled split tree, one cell per floating pane. p.pos must
// be positioned at the opening '<'.
func (p *layoutParser) floatSection() ([]PaneCell, error) {
	p.pos++ // consume '<'
	var floats []PaneCell
	for {
		c, err := p.floatCell()
		if err != nil {
			return nil, err
		}
		floats = append(floats, c)
		if p.pos >= len(p.s) {
			return nil, fmt.Errorf("layout: unterminated float section")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case '>':
			p.pos++
			return floats, nil
		default:
			return nil, fmt.Errorf("layout: bad float delimiter %q", p.s[p.pos])
		}
	}
}

// floatCell := WxH,X,Y,id — floats are always leaves, never splits.
func (p *layoutParser) floatCell() (PaneCell, error) {
	var c PaneCell
	var err error
	if c.W, err = p.intUntil('x'); err != nil {
		return c, err
	}
	if c.H, err = p.intUntil(','); err != nil {
		return c, err
	}
	if c.X, err = p.intUntil(','); err != nil {
		return c, err
	}
	if c.Y, err = p.intUntil(','); err != nil {
		return c, err
	}
	c.ID = "%" + p.numRun()
	return c, nil
}

// pruneFloats removes leaves whose id is in floatIDs from the tree rooted at
// n, collapsing any split left with a single surviving child (tmux layout
// splits always need >= 2 children). Returns nil if n has no surviving
// leaves.
func pruneFloats(n *node, floatIDs map[string]bool) *node {
	if len(n.children) == 0 {
		if floatIDs[n.id] {
			return nil
		}
		return n
	}
	var kept []*node
	for _, c := range n.children {
		if pc := pruneFloats(c, floatIDs); pc != nil {
			kept = append(kept, pc)
		}
	}
	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	default:
		n.children = kept
		return n
	}
}

// writeCell serializes n (the pruned, tiled-only tree) back into tmux layout
// syntax, without the checksum prefix.
func writeCell(n *node, sb *strings.Builder) {
	fmt.Fprintf(sb, "%dx%d,%d,%d", n.w, n.h, n.x, n.y)
	if len(n.children) == 0 {
		fmt.Fprintf(sb, ",%s", strings.TrimPrefix(n.id, "%"))
		return
	}
	sb.WriteByte(n.kind)
	for i, c := range n.children {
		if i > 0 {
			sb.WriteByte(',')
		}
		writeCell(c, sb)
	}
	switch n.kind {
	case '{':
		sb.WriteByte('}')
	case '[':
		sb.WriteByte(']')
	}
}

// layoutChecksum reproduces tmux's layout_checksum (layout.c): a rotate-right
// running sum over the layout body (everything after the checksum prefix).
// select-layout validates this checksum against the body it's given and
// rejects the command on mismatch, so a reconstructed Raw string must carry
// a checksum recomputed the same way tmux computes it, not a copy of the
// original (which covered the un-pruned, float-bearing body).
func layoutChecksum(body string) uint16 {
	var csum uint16
	for i := 0; i < len(body); i++ {
		csum = (csum >> 1) + ((csum & 1) << 15)
		csum += uint16(body[i])
	}
	return csum
}
