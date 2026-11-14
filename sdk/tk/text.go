package tk

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// text is a multi-line text editor (text(9)): indices, marks, tags with
// display options and bindings, the sel tag, searching and scrolling.
// Embedded windows and images are not supported, and tab stops other
// than left ones are taken as left.
type text struct {
	lines  [][]rune
	marks  map[string]pos
	left   map[string]bool // marks with left gravity
	tags   map[string][]span
	order  []string // tag priority, lowest first
	styles map[string]*tagstyle
	tbinds map[string][]binding

	top     int // first line shown
	xoff    int // pixels scrolled left, when not wrapping
	wrap    string
	yscroll string
	xscroll string
	lastf   string // fractions last sent to yscroll
	lastx   string // and to xscroll
	shown   int    // lines shown at the last paint
	spacing [3]int
	tabs    []int
	tabstr  string
	inswid  int
	selbd   int
	scan    draw.Point // scan mark, with the top line then
	scantop int
	cur     []string // tags on the current character, for Enter and Leave
}

type pos struct{ line, col int } // line from 0; col in runes

type span struct{ a, b pos }

func (p pos) less(q pos) bool { return p.line < q.line || p.line == q.line && p.col < q.col }

func newText() *text {
	return &text{lines: [][]rune{nil}, marks: map[string]pos{"insert": {}, "current": {}},
		left: map[string]bool{}, tags: map[string][]span{"sel": nil}, order: []string{"sel"},
		styles: map[string]*tagstyle{}, tbinds: map[string][]binding{}, wrap: "char", inswid: 1}
}

func (x *text) defaults(w *Widget) {
	w.bd, w.relief, w.takefocus = 1, reliefSunken, true
}

func (x *text) option(w *Widget, name, val string) (bool, error) {
	t := w.t
	var err error
	switch name {
	case "wrap":
		if val != "none" && val != "char" && val != "word" {
			return true, tkerr(errBadvl, val)
		}
		x.wrap = val
	case "yscrollcommand":
		x.yscroll = val
	case "xscrollcommand":
		x.xscroll = val
	case "spacing1", "spacing2", "spacing3":
		x.spacing[name[7]-'1'], err = t.nndistw(val, w)
	case "tabs":
		x.tabs, err = t.parseTabs(val, w)
		if err == nil {
			x.tabstr = val
		}
	case "insertwidth":
		x.inswid, err = t.nndist(val)
	case "selectborderwidth":
		x.selbd, err = t.nndist(val)
	case "setgrid", "exportselection", "tagshare", "propagate":
	default:
		return false, nil
	}
	return true, err
}

func (x *text) get(w *Widget, name string) (string, bool) {
	switch name {
	case "wrap":
		return x.wrap, true
	case "yscrollcommand":
		return x.yscroll, true
	case "xscrollcommand":
		return x.xscroll, true
	case "spacing1", "spacing2", "spacing3":
		return itoa(x.spacing[name[7]-'1']), true
	case "tabs":
		return x.tabstr, true
	case "insertwidth":
		return itoa(x.inswid), true
	case "selectborderwidth":
		return itoa(x.selbd), true
	}
	return "", false
}

// parseTabs parses a tab list: distances, each perhaps followed by
// left, right, center or numeric.
func (t *Toplevel) parseTabs(s string, w *Widget) ([]int, error) {
	ws, err := t.words(s)
	if err != nil {
		return nil, err
	}
	var tabs []int
	for _, f := range ws {
		switch f {
		case "left", "right", "center", "numeric":
			if len(tabs) == 0 {
				return nil, tkerr(errBadvl, s)
			}
			continue
		}
		d, err := t.nndistw(f, w)
		if err != nil {
			return nil, err
		}
		tabs = append(tabs, d)
	}
	return tabs, nil
}

func (x *text) natural(w *Widget) draw.Point {
	return draw.Pt(40*w.charwidth(), 10*w.font.Height)
}

func (x *text) end() pos {
	n := len(x.lines) - 1
	return pos{n, len(x.lines[n])}
}

func (x *text) clamp(p pos) pos {
	if p.line < 0 {
		return pos{}
	}
	if p.line >= len(x.lines) {
		return x.end()
	}
	p.col = max(0, min(p.col, len(x.lines[p.line])))
	return p
}

// fwd moves p n runes on, counting line ends as one.
func (x *text) fwd(p pos, n int) pos {
	for n > 0 {
		if p.col < len(x.lines[p.line]) {
			d := min(n, len(x.lines[p.line])-p.col)
			p.col += d
			n -= d
		} else if p.line+1 < len(x.lines) {
			p = pos{p.line + 1, 0}
			n--
		} else {
			break
		}
	}
	for n < 0 {
		if p.col > 0 {
			d := min(-n, p.col)
			p.col -= d
			n += d
		} else if p.line > 0 {
			p = pos{p.line - 1, len(x.lines[p.line-1])}
			n++
		} else {
			break
		}
	}
	return p
}

// rune returns the character at p; a line's end is a newline.
func (x *text) rune(p pos) rune {
	if p.col >= len(x.lines[p.line]) {
		return '\n'
	}
	return x.lines[p.line][p.col]
}

func isword(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80
}

// index parses a text index: a base (line.char, @x,y, end, a mark,
// tag.first or tag.last) and modifiers (+n chars, -n lines,
// linestart, lineend, wordstart, wordend).
func (x *text) index(w *Widget, s string) (pos, error) {
	bad := tkerr(errBadix, s)
	s = strings.TrimSpace(s)
	i := 0
	if strings.HasPrefix(s, "@") {
		// The coordinates may be negative.
		i = 1
		for i < len(s) && s[i] != ' ' && s[i] != '+' && (s[i] != '-' || s[i-1] == '@' || s[i-1] == ',') {
			i++
		}
	}
	for i < len(s) && s[i] != ' ' && s[i] != '+' && s[i] != '-' {
		i++
	}
	base, mods := s[:i], s[i:]
	var p pos
	switch {
	case base == "":
		return p, bad
	case base == "end":
		p = x.end()
	case base[0] == '@':
		xy := strings.SplitN(base[1:], ",", 2)
		if len(xy) != 2 {
			return p, bad
		}
		px, e1 := strconv.Atoi(xy[0])
		py, e2 := strconv.Atoi(xy[1])
		if e1 != nil || e2 != nil {
			return p, bad
		}
		p = x.at(w, w.inside().Min.Add(draw.Pt(px, py)))
	case base[0] >= '0' && base[0] <= '9':
		lc := strings.SplitN(base, ".", 2)
		if len(lc) != 2 {
			return p, bad
		}
		l, err := strconv.Atoi(lc[0])
		if err != nil {
			return p, bad
		}
		p.line = l - 1
		switch {
		case p.line < 0:
			p = pos{}
		case p.line >= len(x.lines):
			p = x.end()
		case lc[1] == "end":
			p.col = len(x.lines[p.line])
		default:
			c, err := strconv.Atoi(lc[1])
			if err != nil {
				return p, bad
			}
			p.col = c
		}
		p = x.clamp(p)
	case strings.HasSuffix(base, ".first") || strings.HasSuffix(base, ".last"):
		dot := strings.LastIndexByte(base, '.')
		sp := x.tags[base[:dot]]
		if len(sp) == 0 {
			return p, tkerr(errBadtg, base)
		}
		if base[dot:] == ".first" {
			p = sp[0].a
		} else {
			p = sp[len(sp)-1].b
		}
	default:
		m, ok := x.marks[base]
		if !ok {
			return p, bad
		}
		p = m
	}
	for {
		mods = strings.TrimLeft(mods, " ")
		if mods == "" {
			return x.clamp(p), nil
		}
		if mods[0] == '+' || mods[0] == '-' {
			sign := 1
			if mods[0] == '-' {
				sign = -1
			}
			mods = strings.TrimLeft(mods[1:], " ")
			j := 0
			for j < len(mods) && mods[j] >= '0' && mods[j] <= '9' {
				j++
			}
			n, err := strconv.Atoi(mods[:j])
			if err != nil {
				return p, bad
			}
			mods = strings.TrimLeft(mods[j:], " ")
			k := 0
			for k < len(mods) && mods[k] >= 'a' && mods[k] <= 'z' {
				k++
			}
			unit := mods[:k]
			mods = mods[k:]
			switch {
			case unit != "" && strings.HasPrefix("chars", unit):
				p = x.fwd(p, sign*n)
			case unit != "" && strings.HasPrefix("lines", unit):
				p = x.clamp(pos{p.line + sign*n, p.col})
			default:
				return p, bad
			}
			continue
		}
		k := 0
		for k < len(mods) && mods[k] >= 'a' && mods[k] <= 'z' {
			k++
		}
		word := mods[:k]
		mods = mods[k:]
		line := x.lines[p.line]
		switch word {
		case "linestart":
			p.col = 0
		case "lineend":
			p.col = len(line)
		case "wordstart":
			if p.col < len(line) && isword(line[p.col]) {
				for p.col > 0 && isword(line[p.col-1]) {
					p.col--
				}
			}
		case "wordend":
			if p.col < len(line) && isword(line[p.col]) {
				for p.col < len(line) && isword(line[p.col]) {
					p.col++
				}
			} else {
				p = x.fwd(p, 1)
			}
		default:
			return p, bad
		}
	}
}

func posString(p pos) string { return itoa(p.line+1) + "." + itoa(p.col) }

// textof returns the text from a to b.
func (x *text) textof(a, b pos) string {
	if !a.less(b) {
		return ""
	}
	var sb strings.Builder
	for l := a.line; l <= b.line; l++ {
		line := x.lines[l]
		c0, c1 := 0, len(line)
		if l == a.line {
			c0 = a.col
		}
		if l == b.line {
			c1 = b.col
		}
		sb.WriteString(string(line[c0:c1]))
		if l < b.line {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// adjustIns moves p for an insertion at a that ends at e (right
// gravity); adjustDel for a deletion of a..b.
func adjustIns(p, a, e pos) pos {
	if p.less(a) {
		return p
	}
	if p.line == a.line {
		return pos{e.line, e.col + p.col - a.col}
	}
	return pos{p.line + e.line - a.line, p.col}
}

func adjustDel(p, a, b pos) pos {
	if p.less(a) {
		return p
	}
	if p.less(b) {
		return a
	}
	if p.line == b.line {
		return pos{a.line, a.col + p.col - b.col}
	}
	return pos{p.line - (b.line - a.line), p.col}
}

func (x *text) insert(p pos, s string, tags []string) pos {
	p = x.clamp(p)
	parts := strings.Split(s, "\n")
	line := x.lines[p.line]
	head := append([]rune(nil), line[:p.col]...)
	tail := append([]rune(nil), line[p.col:]...)
	var nl [][]rune
	for i, part := range parts {
		rs := []rune(part)
		if i == 0 {
			rs = append(head, rs...)
		}
		nl = append(nl, rs)
	}
	e := pos{p.line + len(parts) - 1, len(nl[len(nl)-1])}
	nl[len(nl)-1] = append(nl[len(nl)-1], tail...)
	x.lines = append(x.lines[:p.line], append(nl, x.lines[p.line+1:]...)...)
	for k, m := range x.marks {
		if m == p && x.left[k] {
			continue
		}
		x.marks[k] = adjustIns(m, p, e)
	}
	for k, sp := range x.tags {
		for i := range sp {
			sp[i].a = adjustIns(sp[i].a, p, e)
			if sp[i].b != p {
				sp[i].b = adjustIns(sp[i].b, p, e)
			}
		}
		x.tags[k] = sp
	}
	for _, tg := range tags {
		x.tagAdd(tg, p, e)
	}
	return e
}

// tagsAround returns the tags on both the character before p and the
// one at p, which text inserted at p takes.
func (x *text) tagsAround(p pos) []string {
	if p == (pos{}) {
		return nil
	}
	b := x.fwd(p, -1)
	var n []string
	for _, tg := range x.order {
		if tg != "sel" && x.hasTag(tg, b) && x.hasTag(tg, p) {
			n = append(n, tg)
		}
	}
	return n
}

func (x *text) delete(a, b pos) {
	a, b = x.clamp(a), x.clamp(b)
	if !a.less(b) {
		return
	}
	nl := append(append([]rune(nil), x.lines[a.line][:a.col]...), x.lines[b.line][b.col:]...)
	x.lines = append(x.lines[:a.line], append([][]rune{nl}, x.lines[b.line+1:]...)...)
	for k, m := range x.marks {
		x.marks[k] = adjustDel(m, a, b)
	}
	for k, sp := range x.tags {
		var out []span
		for _, s := range sp {
			s.a, s.b = adjustDel(s.a, a, b), adjustDel(s.b, a, b)
			if s.a.less(s.b) {
				out = append(out, s)
			}
		}
		x.tags[k] = out
	}
	x.top = min(x.top, len(x.lines)-1)
}

// define makes tag name known, at the highest priority.
func (x *text) define(name string) {
	if _, ok := x.tags[name]; ok {
		return
	}
	x.tags[name] = nil
	x.order = append(x.order, name)
}

func (x *text) tagAdd(name string, a, b pos) {
	x.define(name)
	if !a.less(b) {
		return
	}
	sp := append(x.tags[name], span{a, b})
	sort.SliceStable(sp, func(i, j int) bool { return sp[i].a.less(sp[j].a) })
	var out []span
	for _, s := range sp {
		if n := len(out); n > 0 && !out[n-1].b.less(s.a) {
			if out[n-1].b.less(s.b) {
				out[n-1].b = s.b
			}
			continue
		}
		out = append(out, s)
	}
	x.tags[name] = out
}

func (x *text) tagRemove(name string, a, b pos) {
	if _, ok := x.tags[name]; !ok {
		return
	}
	var out []span
	for _, s := range x.tags[name] {
		if !a.less(s.b) || !s.a.less(b) {
			out = append(out, s) // no overlap
			continue
		}
		if s.a.less(a) {
			out = append(out, span{s.a, a})
		}
		if b.less(s.b) {
			out = append(out, span{b, s.b})
		}
	}
	x.tags[name] = out
}

func (x *text) hasTag(name string, p pos) bool {
	for _, s := range x.tags[name] {
		if p.less(s.a) {
			break
		}
		if p.less(s.b) {
			return true
		}
	}
	return false
}

func (x *text) selected(p pos) bool { return x.hasTag("sel", p) }

// tagsAt returns the tags on the character at p, lowest priority first.
func (x *text) tagsAt(p pos) []string {
	var n []string
	for _, tg := range x.order {
		if x.hasTag(tg, p) {
			n = append(n, tg)
		}
	}
	return n
}

// Tag display options.
const (
	tsFg = 1 << iota
	tsBg
	tsFont
	tsBd
	tsRelief
	tsOffset
	tsLmargin1
	tsLmargin2
	tsRmargin
	tsSpacing1
	tsSpacing2
	tsSpacing3
	tsJustify
	tsUnderline
	tsOverstrike
	tsWrap
	tsTabs
	tsLineheight
)

type tagstyle struct {
	set        uint32
	fg, bg     uint32
	font       *draw.Font
	fontname   string
	bd, relief int
	offset     int
	lmargin1   int
	lmargin2   int
	rmargin    int
	spacing    [3]int
	lineheight int
	justify    string
	underline  bool
	overstrike bool
	wrap       string
	tabs       []int
	tabstr     string
}

var tagopts = map[string]uint32{
	"foreground": tsFg, "fg": tsFg, "background": tsBg, "bg": tsBg, "font": tsFont,
	"borderwidth": tsBd, "relief": tsRelief, "offset": tsOffset, "lmargin1": tsLmargin1,
	"lmargin2": tsLmargin2, "lmargin3": 0, "rmargin": tsRmargin, "spacing1": tsSpacing1,
	"spacing2": tsSpacing2, "spacing3": tsSpacing3, "justify": tsJustify, "underline": tsUnderline,
	"overstrike": tsOverstrike, "wrap": tsWrap, "tabs": tsTabs, "lineheight": tsLineheight,
}

// tagConfigure sets display options of a tag.
func (x *text) tagConfigure(w *Widget, ts *tagstyle, opts []string) error {
	t := w.t
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	for i := 0; i < len(opts); i += 2 {
		name, val := strings.TrimPrefix(opts[i], "-"), opts[i+1]
		bit, ok := tagopts[name]
		if !ok || !strings.HasPrefix(opts[i], "-") {
			return tkerr(errBadop, opts[i])
		}
		if val == "" && bit != tsFont {
			// An empty value unsets the option.
			ts.set &^= bit
			continue
		}
		var err error
		switch bit {
		case tsFg:
			ts.fg, err = parseColor(val)
		case tsBg:
			ts.bg, err = parseColor(val)
		case tsFont:
			ts.font, err = t.openFont(val)
			ts.fontname = val
		case tsBd:
			ts.bd, err = t.nndistw(val, w)
		case tsRelief:
			ts.relief, err = lookup(reliefs, val)
		case tsOffset:
			ts.offset, err = t.dist(val, w)
		case tsLmargin1:
			ts.lmargin1, err = t.dist(val, w)
		case tsLmargin2:
			ts.lmargin2, err = t.dist(val, w)
		case tsRmargin:
			ts.rmargin, err = t.dist(val, w)
		case tsSpacing1, tsSpacing2, tsSpacing3:
			ts.spacing[name[7]-'1'], err = t.nndistw(val, w)
		case tsLineheight:
			ts.lineheight, err = t.nndistw(val, w)
		case tsJustify:
			if val != "left" && val != "right" && val != "center" {
				err = tkerr(errBadvl, val)
			}
			ts.justify = val
		case tsUnderline:
			ts.underline, err = parseBool(val)
		case tsOverstrike:
			ts.overstrike, err = parseBool(val)
		case tsWrap:
			if val != "none" && val != "char" && val != "word" {
				err = tkerr(errBadvl, val)
			}
			ts.wrap = val
		case tsTabs:
			ts.tabs, err = t.parseTabs(val, w)
			ts.tabstr = val
		case 0:
			continue
		}
		if err != nil {
			return err
		}
		ts.set |= bit
	}
	return nil
}

func (x *text) tagCget(w *Widget, name string, ts *tagstyle, opt string) (string, error) {
	o := strings.TrimPrefix(opt, "-")
	bit, ok := tagopts[o]
	if !ok {
		return "", tkerr(errBadop, opt)
	}
	if ts == nil {
		ts = &tagstyle{}
	}
	if name == "sel" {
		switch {
		case bit == tsFg && ts.set&tsFg == 0:
			return colorString(w.colors[cSelectFg]), nil
		case bit == tsBg && ts.set&tsBg == 0:
			return colorString(w.colors[cSelectBg]), nil
		}
	}
	if ts.set&bit == 0 {
		return "", nil
	}
	switch bit {
	case tsFg:
		return colorString(ts.fg), nil
	case tsBg:
		return colorString(ts.bg), nil
	case tsFont:
		return ts.fontname, nil
	case tsBd:
		return itoa(ts.bd), nil
	case tsRelief:
		return reliefs[ts.relief], nil
	case tsOffset:
		return itoa(ts.offset), nil
	case tsLmargin1:
		return itoa(ts.lmargin1), nil
	case tsLmargin2:
		return itoa(ts.lmargin2), nil
	case tsRmargin:
		return itoa(ts.rmargin), nil
	case tsSpacing1, tsSpacing2, tsSpacing3:
		return itoa(ts.spacing[o[7]-'1']), nil
	case tsLineheight:
		return itoa(ts.lineheight), nil
	case tsJustify:
		return ts.justify, nil
	case tsUnderline:
		return boolString(ts.underline), nil
	case tsOverstrike:
		return boolString(ts.overstrike), nil
	case tsWrap:
		return ts.wrap, nil
	case tsTabs:
		return ts.tabstr, nil
	}
	return "", nil
}

func boolString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// cstyle is how a character is drawn: the widget's defaults overridden
// by its tags in priority order.
type cstyle struct {
	fg, bg     uint32
	hasbg      bool
	font       *draw.Font
	bd, relief int
	offset     int
	underline  bool
	overstrike bool
	// Options that apply to a display line, from its first character.
	lmargin1, lmargin2, rmargin int
	spacing                     [3]int
	lineheight                  int
	justify                     string
	wrap                        string
	tabs                        []int
}

func (x *text) basestyle(w *Widget) cstyle {
	return cstyle{fg: w.colors[cFg], font: w.font, spacing: x.spacing, wrap: x.wrap, tabs: x.tabs,
		justify: "left"}
}

// styleOf returns the style of a character with the given tags.
func (x *text) styleOf(w *Widget, tags []string, base *cstyle) cstyle {
	s := *base
	for _, tg := range tags {
		ts := x.styles[tg]
		if tg == "sel" {
			if ts == nil || ts.set&tsBg == 0 {
				s.bg, s.hasbg = w.colors[cSelectBg], true
				s.bd, s.relief = x.selbd, reliefRaised
			}
			if ts == nil || ts.set&tsFg == 0 {
				s.fg = w.colors[cSelectFg]
			}
		}
		if ts == nil {
			continue
		}
		if ts.set&tsFg != 0 {
			s.fg = ts.fg
		}
		if ts.set&tsBg != 0 {
			s.bg, s.hasbg = ts.bg, true
		}
		if ts.set&tsFont != 0 && ts.font != nil {
			s.font = ts.font
		}
		if ts.set&tsBd != 0 {
			s.bd = ts.bd
		}
		if ts.set&tsRelief != 0 {
			s.relief = ts.relief
		}
		if ts.set&tsOffset != 0 {
			s.offset = ts.offset
		}
		if ts.set&tsUnderline != 0 {
			s.underline = ts.underline
		}
		if ts.set&tsOverstrike != 0 {
			s.overstrike = ts.overstrike
		}
		if ts.set&tsLmargin1 != 0 {
			s.lmargin1 = ts.lmargin1
		}
		if ts.set&tsLmargin2 != 0 {
			s.lmargin2 = ts.lmargin2
		}
		if ts.set&tsRmargin != 0 {
			s.rmargin = ts.rmargin
		}
		for k := 0; k < 3; k++ {
			if ts.set&(tsSpacing1<<k) != 0 {
				s.spacing[k] = ts.spacing[k]
			}
		}
		if ts.set&tsLineheight != 0 {
			s.lineheight = ts.lineheight
		}
		if ts.set&tsJustify != 0 {
			s.justify = ts.justify
		}
		if ts.set&tsWrap != 0 {
			s.wrap = ts.wrap
		}
		if ts.set&tsTabs != 0 {
			s.tabs = ts.tabs
		}
	}
	return s
}

// linestyles returns the style of each character of line l, the line's
// end included, sharing styles between characters with the same tags.
func (x *text) linestyles(w *Widget, l int, base *cstyle) []*cstyle {
	n := len(x.lines[l]) + 1
	ids := make([][]int, n)
	for k, tg := range x.order {
		for _, s := range x.tags[tg] {
			if s.b.line < l || s.a.line > l {
				continue
			}
			c0, c1 := 0, n
			if s.a.line == l {
				c0 = s.a.col
			}
			if s.b.line == l {
				c1 = s.b.col
			}
			for c := c0; c < c1 && c < n; c++ {
				ids[c] = append(ids[c], k)
			}
		}
	}
	out := make([]*cstyle, n)
	cache := map[string]*cstyle{}
	for c := range ids {
		var kb strings.Builder
		for _, k := range ids[c] {
			kb.WriteString(itoa(k))
			kb.WriteByte(',')
		}
		key := kb.String()
		if s := cache[key]; s != nil {
			out[c] = s
			continue
		}
		var tags []string
		for _, k := range ids[c] {
			tags = append(tags, x.order[k])
		}
		s := x.styleOf(w, tags, base)
		cache[key] = &s
		out[c] = &s
	}
	return out
}

// dline is one row on the screen: part of a text line.
type dline struct {
	line, c0, c1 int
	y, h, asc    int // top relative to the text's area, height and ascent
	xs           []int
	sty          []*cstyle // the line's styles
	last         bool      // the last row of its line
}

// tabstop returns where a tab at x goes, with the tab list tabs.
func tabstop(x int, tabs []int, cw int) int {
	for _, t := range tabs {
		if t > x {
			return t
		}
	}
	step := 8 * cw
	last := 0
	if n := len(tabs); n > 0 {
		last = tabs[n-1]
		if n > 1 {
			step = tabs[n-1] - tabs[n-2]
		}
	}
	if step <= 0 {
		step = 8 * cw
	}
	return last + ((x-last)/step+1)*step
}

// layout lays out lines from the top line to fill height ht at width
// wd.
func (x *text) layout(w *Widget, wd, ht int) []dline {
	base := x.basestyle(w)
	cw := w.charwidth()
	var rows []dline
	y := 0
	for l := x.top; l < len(x.lines) && (y < ht || len(rows) == 0); l++ {
		line := x.lines[l]
		sty := x.linestyles(w, l, &base)
		c0 := 0
		first := true
		for {
			s0 := sty[min(c0, len(line))]
			lm := s0.lmargin1
			if !first {
				lm = s0.lmargin2
			}
			limit := wd - s0.rmargin
			px := lm
			xs := []int{px}
			c1 := c0
			for c1 < len(line) {
				st := sty[c1]
				var cwid int
				if line[c1] == '\t' {
					cwid = max(1, tabstop(px-lm, st.tabs, cw)+lm-px)
				} else {
					cwid = st.font.Width(string(line[c1]))
				}
				if s0.wrap != "none" && px+cwid > limit && c1 > c0 {
					break
				}
				px += cwid
				c1++
				xs = append(xs, px)
			}
			if s0.wrap == "word" && c1 < len(line) {
				for k := c1; k > c0+1; k-- {
					if line[k-1] == ' ' || line[k-1] == '\t' {
						xs = xs[:k-c0+1]
						c1 = k
						break
					}
				}
			}
			// Justification moves the whole row.
			if s0.justify == "right" || s0.justify == "center" {
				d := limit - xs[len(xs)-1]
				if s0.justify == "center" {
					d /= 2
				}
				if d > 0 {
					for k := range xs {
						xs[k] += d
					}
				}
			}
			asc, desc := 0, 0
			for c := c0; c < c1 || c == c0; c++ {
				st := sty[min(c, len(line))]
				f := st.font
				asc = max(asc, f.Ascent+max(0, st.offset))
				desc = max(desc, f.Height-f.Ascent+max(0, -st.offset))
				if c >= len(line) {
					break
				}
			}
			h := asc + desc
			if s0.lineheight > h {
				asc += (s0.lineheight - h) / 2
				h = s0.lineheight
			}
			last := c1 >= len(line)
			above, below := s0.spacing[1], s0.spacing[1]
			if first {
				above = s0.spacing[0]
			}
			if last {
				below = s0.spacing[2]
			}
			rows = append(rows, dline{line: l, c0: c0, c1: c1, y: y + above, h: h, asc: asc,
				xs: xs, sty: sty, last: last})
			y += above + h + below
			if last || y >= ht {
				break
			}
			c0 = c1
			first = false
		}
	}
	return rows
}

// textarea is where the text goes in the image.
func (x *text) textarea(w *Widget) draw.Rectangle {
	r := w.inside()
	return draw.Rect(r.Min.X+w.padx+1, r.Min.Y+w.pady, r.Max.X-w.padx-1, r.Max.Y-w.pady)
}

func (x *text) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	t := w.t
	area := x.textarea(w)
	rows := x.layout(w, area.Dx(), area.Dy())
	ins := x.marks["insert"]
	showcursor := t.focus == w && t.focused && w.state != stDisabled
	old := dst.Clipr
	clip, _ := r.Clip(old)
	_ = dst.ReplClipr(false, clip)
	defer func() { _ = dst.ReplClipr(false, old) }()
	for _, dl := range rows {
		y := area.Min.Y + dl.y
		if y >= area.Max.Y {
			break
		}
		line := x.lines[dl.line]
		ox := area.Min.X - x.xoff
		// Backgrounds, run by run.
		for c := dl.c0; c < dl.c1; {
			s := dl.sty[c]
			e := c + 1
			for e < dl.c1 && dl.sty[e] == s {
				e++
			}
			if s.hasbg {
				br := draw.Rect(ox+dl.xs[c-dl.c0], y, ox+dl.xs[e-dl.c0], y+dl.h)
				dst.Draw(br, w.col(s.bg), nil, draw.ZP)
				if s.bd > 0 && s.relief != reliefFlat {
					bevel(dst, t, br, s.bd, s.relief, s.bg)
				}
			}
			c = e
		}
		// A tagged newline colours the rest of its row, as when a
		// selection runs onto the next line.
		if dl.last {
			if s := dl.sty[len(line)]; s.hasbg && len(line) == dl.c1 {
				br := draw.Rect(ox+dl.xs[len(dl.xs)-1], y, area.Max.X, y+dl.h)
				dst.Draw(br, w.col(s.bg), nil, draw.ZP)
			}
		}
		for c := dl.c0; c < dl.c1; c++ {
			s := dl.sty[c]
			px := ox + dl.xs[c-dl.c0]
			nx := ox + dl.xs[c-dl.c0+1]
			if line[c] != '\t' {
				by := y + dl.asc - s.font.Ascent - s.offset
				_, _ = dst.String(draw.Pt(px, by), w.col(s.fg), draw.ZP, s.font, string(line[c]))
			}
			if s.underline {
				uy := y + dl.asc - s.offset + 1
				dst.Draw(draw.Rect(px, uy, nx, uy+1), w.col(s.fg), nil, draw.ZP)
			}
			if s.overstrike {
				oy := y + dl.asc - s.offset - s.font.Ascent/2
				dst.Draw(draw.Rect(px, oy, nx, oy+1), w.col(s.fg), nil, draw.ZP)
			}
		}
		if showcursor && ins.line == dl.line && ins.col >= dl.c0 &&
			(ins.col < dl.c1 || ins.col == dl.c1 && (dl.last || dl.c1 == dl.c0)) {
			cx := ox + dl.xs[ins.col-dl.c0]
			dst.Draw(draw.Rect(cx, y, cx+max(1, x.inswid), y+dl.h), w.col(w.colors[cFg]), nil, draw.ZP)
		}
	}
	x.shown = 0
	if n := len(rows); n > 0 {
		// Count only the lines that fit.
		k := n - 1
		for k > 0 && area.Min.Y+rows[k].y+rows[k].h > area.Max.Y {
			k--
		}
		x.shown = rows[k].line - x.top + 1
		if !rows[k].last && k > 0 {
			x.shown--
		}
		x.shown = max(1, x.shown)
	}
	x.scrolled(w, rows)
}

// scrolled tells the scroll commands where the view is.
func (x *text) scrolled(w *Widget, rows []dline) {
	if x.yscroll != "" {
		fs := x.yfractions()
		if fs != x.lastf {
			x.lastf = fs
			w.t.later(x.yscroll + " " + fs)
		}
	}
	if x.xscroll != "" {
		fs := x.xfractions(w, rows)
		if fs != x.lastx {
			x.lastx = fs
			w.t.later(x.xscroll + " " + fs)
		}
	}
}

func frac(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

func (x *text) yfractions() string {
	n := float64(len(x.lines))
	return frac(float64(x.top)/n) + " " + frac(min(1, float64(x.top+max(1, x.shown))/n))
}

func widest(rows []dline) int {
	m := 0
	for _, dl := range rows {
		m = max(m, dl.xs[len(dl.xs)-1])
	}
	return m
}

func (x *text) xfractions(w *Widget, rows []dline) string {
	wd := x.textarea(w).Dx()
	m := widest(rows)
	if x.wrap != "none" || m <= wd {
		return frac(0) + " " + frac(1)
	}
	return frac(float64(x.xoff)/float64(m)) + " " + frac(min(1, float64(x.xoff+wd)/float64(m)))
}

// at returns the position at point p of the image.
func (x *text) at(w *Widget, p draw.Point) pos {
	area := x.textarea(w)
	rows := x.layout(w, area.Dx(), area.Dy())
	if len(rows) == 0 {
		return x.end()
	}
	y := p.Y - area.Min.Y
	k := 0
	for k+1 < len(rows) && y >= rows[k].y+rows[k].h {
		k++
	}
	dl := rows[k]
	px := p.X - area.Min.X + x.xoff
	for c := dl.c0; c < dl.c1; c++ {
		x0, x1 := dl.xs[c-dl.c0], dl.xs[c-dl.c0+1]
		if px < (x0+x1)/2 {
			return pos{dl.line, c}
		}
	}
	if !dl.last && dl.c1 > dl.c0 {
		return pos{dl.line, dl.c1 - 1}
	}
	return pos{dl.line, dl.c1}
}

// visible reports whether position p is on a row that fits.
func (x *text) visible(w *Widget, p pos) bool {
	if p.line < x.top {
		return false
	}
	area := x.textarea(w)
	rows := x.layout(w, area.Dx(), area.Dy())
	for _, dl := range rows {
		if dl.line == p.line && p.col >= dl.c0 && (p.col < dl.c1 || dl.last) {
			return dl.y+dl.h <= area.Dy() || dl.y == 0
		}
	}
	return false
}

// see scrolls so that p is shown: just enough if it is near, else with
// p in the middle.
func (x *text) see(w *Widget, p pos) {
	p = x.clamp(p)
	if x.wrap == "none" {
		x.seex(w, p)
	}
	if x.visible(w, p) {
		return
	}
	rows := x.rowsPerPage(w)
	if p.line < x.top {
		if x.top-p.line > rows {
			x.top = max(0, p.line-rows/2)
		} else {
			x.top = p.line
		}
		return
	}
	if p.line-x.top > 2*rows {
		x.top = max(0, p.line-rows/2)
	}
	for x.top < p.line && !x.visible(w, p) {
		x.top++
	}
}

// seex scrolls sideways to show p when lines do not wrap.
func (x *text) seex(w *Widget, p pos) {
	area := x.textarea(w)
	save := x.top
	x.top = p.line
	rows := x.layout(w, area.Dx(), area.Dy())
	x.top = save
	if len(rows) == 0 || p.col-rows[0].c0 >= len(rows[0].xs) {
		return
	}
	px := rows[0].xs[p.col-rows[0].c0]
	switch {
	case px < x.xoff:
		x.xoff = max(0, px-area.Dx()/3)
	case px >= x.xoff+area.Dx()-2:
		x.xoff = px - area.Dx() + area.Dx()/3
	}
}

func (x *text) cmd(w *Widget, args []string) (string, error) {
	w.t.dirty = true
	idx := func(k int) (pos, error) {
		if len(args) <= k {
			return pos{}, tkerr(errBadvl)
		}
		return x.index(w, args[k])
	}
	switch args[0] {
	case "insert":
		p, err := idx(1)
		if err != nil {
			return "", err
		}
		if w.state == stDisabled {
			return "", nil
		}
		if len(args) == 3 {
			x.insert(p, args[2], x.tagsAround(p))
			return "", nil
		}
		for k := 2; k < len(args); k += 2 {
			var tags []string
			if k+1 < len(args) {
				tags = strings.Fields(args[k+1])
			}
			p = x.insert(p, args[k], tags)
		}
		return "", nil
	case "delete":
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b := x.fwd(a, 1)
		if len(args) > 2 {
			if b, err = idx(2); err != nil {
				return "", err
			}
		}
		if w.state != stDisabled {
			x.delete(a, b)
		}
		return "", nil
	case "get":
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b := x.fwd(a, 1)
		if len(args) > 2 {
			if b, err = idx(2); err != nil {
				return "", err
			}
		}
		return x.textof(a, b), nil
	case "index":
		p, err := idx(1)
		return posString(p), err
	case "compare":
		if len(args) != 4 {
			return "", tkerr(errBadvl)
		}
		a, err := idx(1)
		if err != nil {
			return "", err
		}
		b, err := idx(3)
		if err != nil {
			return "", err
		}
		var v bool
		switch args[2] {
		case "<":
			v = a.less(b)
		case "<=":
			v = !b.less(a)
		case "==":
			v = a == b
		case "!=":
			v = a != b
		case ">=":
			v = !a.less(b)
		case ">":
			v = b.less(a)
		default:
			return "", tkerr(errBadvl, args[2])
		}
		return boolString(v), nil
	case "mark":
		return x.mark(w, args[1:])
	case "tag":
		return x.tag(w, args[1:])
	case "see":
		p, err := idx(1)
		if err == nil {
			x.see(w, p)
		}
		return "", err
	case "search":
		return x.search(w, args[1:])
	case "bbox":
		p, err := idx(1)
		if err != nil {
			return "", err
		}
		return x.bbox(w, p, false), nil
	case "dlineinfo":
		p, err := idx(1)
		if err != nil {
			return "", err
		}
		return x.bbox(w, p, true), nil
	case "yview":
		return x.yview(w, args[1:])
	case "xview":
		return x.xview(w, args[1:])
	case "scan":
		return x.scancmd(w, args[1:])
	case "selection":
		if len(args) == 2 && args[1] == "clear" {
			x.tags["sel"] = nil
			return "", nil
		}
		return "", tkerr(errBadcm, strings.Join(args, " "))
	case "debug":
		if len(args) == 1 {
			return "off", nil
		}
		return "", nil
	case "window", "image":
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}

// bbox is the area of the character at p if it is shown: x y width
// height relative to the widget; or for line, the area of its display
// line with its baseline (dlineinfo).
func (x *text) bbox(w *Widget, p pos, line bool) string {
	area := x.textarea(w)
	rows := x.layout(w, area.Dx(), area.Dy())
	o := w.inside().Min
	for _, dl := range rows {
		if dl.line != p.line || p.col < dl.c0 || p.col > dl.c1 || p.col == dl.c1 && !dl.last {
			continue
		}
		if dl.y >= area.Dy() {
			return ""
		}
		ay := area.Min.Y - o.Y + dl.y
		if line {
			ax := area.Min.X - o.X + dl.xs[0] - x.xoff
			return itoa(ax) + " " + itoa(ay) + " " + itoa(dl.xs[len(dl.xs)-1]-dl.xs[0]) + " " +
				itoa(dl.h) + " " + itoa(dl.asc)
		}
		x0 := dl.xs[p.col-dl.c0]
		var wd int
		if p.col < dl.c1 {
			wd = dl.xs[p.col-dl.c0+1] - x0
		} else {
			wd = dl.sty[p.col].font.Width(" ")
		}
		ax := area.Min.X - o.X + x0 - x.xoff
		return itoa(ax) + " " + itoa(ay) + " " + itoa(wd) + " " + itoa(dl.h)
	}
	return ""
}

// search finds pattern from an index (text(9)): -backwards, -nocase
// and --; without a stop index it wraps round the text.
func (x *text) search(w *Widget, args []string) (string, error) {
	fwd, nocase := true, false
	k := 0
flags:
	for ; k < len(args) && strings.HasPrefix(args[k], "-"); k++ {
		switch args[k] {
		case "-backwards":
			fwd = false
		case "-forwards":
			fwd = true
		case "-nocase":
			nocase = true
		case "-exact":
		case "--":
			k++
			break flags
		default:
			break flags
		}
	}
	if k+1 >= len(args) {
		return "", tkerr(errOparg)
	}
	pat := []rune(args[k])
	start, err := x.index(w, args[k+1])
	if err != nil {
		return "", err
	}
	if len(pat) == 0 {
		return posString(start), nil
	}
	text := []rune(x.textof(pos{}, x.end()))
	off := func(p pos) int {
		n := 0
		for l := 0; l < p.line; l++ {
			n += len(x.lines[l]) + 1
		}
		return n + p.col
	}
	at := func(o int) pos { return x.fwd(pos{}, o) }
	match := func(o int) bool {
		if o < 0 || o+len(pat) > len(text) {
			return false
		}
		for i, c := range pat {
			d := text[o+i]
			if nocase && c < 0x80 && d < 0x80 {
				c, d = lower(c), lower(d)
			}
			if c != d {
				return false
			}
		}
		return true
	}
	n := len(text)
	s := off(start)
	if k+2 < len(args) {
		stop, err := x.index(w, args[k+2])
		if err != nil {
			return "", err
		}
		e := off(stop)
		if fwd {
			for o := s; o < e; o++ {
				if match(o) {
					return posString(at(o)), nil
				}
			}
		} else {
			for o := s - 1; o >= e; o-- {
				if match(o) {
					return posString(at(o)), nil
				}
			}
		}
		return "", nil
	}
	// Round the text, back to the start.
	for i := 0; i <= n; i++ {
		o := (s + i) % (n + 1)
		if !fwd {
			o = ((s-1-i)%(n+1) + n + 1) % (n + 1)
		}
		if match(o) {
			return posString(at(o)), nil
		}
	}
	return "", nil
}

func lower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 'a' - 'A'
	}
	return r
}

// marksInOrder returns the marks sorted by position, then name.
func (x *text) marksInOrder() []string {
	var n []string
	for m := range x.marks {
		n = append(n, m)
	}
	sort.Slice(n, func(i, j int) bool {
		a, b := x.marks[n[i]], x.marks[n[j]]
		if a != b {
			return a.less(b)
		}
		return n[i] < n[j]
	})
	return n
}

func (x *text) mark(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "set":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		p, err := x.index(w, args[2])
		if err != nil {
			return "", err
		}
		x.marks[args[1]] = p
	case "unset":
		for _, m := range args[1:] {
			if m != "insert" && m != "current" {
				delete(x.marks, m)
				delete(x.left, m)
			}
		}
	case "names":
		n := x.marksInOrder()
		sort.Strings(n)
		return strings.Join(n, " "), nil
	case "gravity":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		if _, ok := x.marks[args[1]]; !ok {
			return "", tkerr(errBadvl, args[1])
		}
		if len(args) == 2 {
			if x.left[args[1]] {
				return "left", nil
			}
			return "right", nil
		}
		switch args[2] {
		case "left":
			x.left[args[1]] = true
		case "right":
			delete(x.left, args[1])
		default:
			return "", tkerr(errBadvl, args[2])
		}
	case "next", "previous":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		ms := x.marksInOrder()
		if _, ok := x.marks[args[1]]; ok {
			// From just after (or before) the named mark.
			for i, m := range ms {
				if m != args[1] {
					continue
				}
				if args[0] == "next" && i+1 < len(ms) {
					return ms[i+1], nil
				}
				if args[0] == "previous" && i > 0 {
					return ms[i-1], nil
				}
				return "", nil
			}
		}
		p, err := x.index(w, args[1])
		if err != nil {
			return "", err
		}
		if args[0] == "next" {
			for _, m := range ms {
				if !x.marks[m].less(p) {
					return m, nil
				}
			}
			return "", nil
		}
		for i := len(ms) - 1; i >= 0; i-- {
			if x.marks[ms[i]].less(p) {
				return ms[i], nil
			}
		}
		return "", nil
	default:
		return "", tkerr(errBadcm, args[0])
	}
	return "", nil
}

func (x *text) tag(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadcm)
	}
	if args[0] == "names" {
		var n []string
		if len(args) > 1 {
			p, err := x.index(w, args[1])
			if err != nil {
				return "", err
			}
			n = x.tagsAt(p)
		} else {
			n = append(n, x.order...)
		}
		for i, j := 0, len(n)-1; i < j; i, j = i+1, j-1 {
			n[i], n[j] = n[j], n[i]
		}
		return strings.Join(n, " "), nil
	}
	if len(args) < 2 || args[1] == "" {
		return "", tkerr(errOparg)
	}
	name := args[1]
	if name[0] >= '0' && name[0] <= '9' {
		return "", tkerr(errBadtg)
	}
	switch args[0] {
	case "add", "remove":
		if len(args) < 3 {
			return "", tkerr(errBadix)
		}
		x.define(name)
		for k := 2; k < len(args); k += 2 {
			a, err := x.index(w, args[k])
			if err != nil {
				return "", err
			}
			b := x.fwd(a, 1)
			if k+1 < len(args) {
				if b, err = x.index(w, args[k+1]); err != nil {
					return "", err
				}
			}
			if args[0] == "add" {
				// There is only ever one selection (libtk).
				if name == "sel" && len(x.tags["sel"]) > 0 && a.less(b) {
					return "", tkerr(errBadsl)
				}
				x.tagAdd(name, a, b)
			} else {
				x.tagRemove(name, a, b)
			}
		}
	case "delete":
		for _, n := range args[1:] {
			if n == "sel" {
				x.tags["sel"] = nil
				continue
			}
			delete(x.tags, n)
			delete(x.styles, n)
			delete(x.tbinds, n)
			for i, o := range x.order {
				if o == n {
					x.order = append(x.order[:i], x.order[i+1:]...)
					break
				}
			}
		}
	case "ranges":
		var n []string
		for _, s := range x.tags[name] {
			n = append(n, posString(s.a), posString(s.b))
		}
		return strings.Join(n, " "), nil
	case "nextrange", "prevrange":
		if len(args) < 3 {
			return "", tkerr(errBadix)
		}
		a, err := x.index(w, args[2])
		if err != nil {
			return "", err
		}
		sp := x.tags[name]
		if args[0] == "nextrange" {
			b := x.end()
			if len(args) > 3 {
				if b, err = x.index(w, args[3]); err != nil {
					return "", err
				}
			}
			for _, s := range sp {
				if s.a.less(a) {
					continue
				}
				if !s.a.less(b) {
					break
				}
				return posString(s.a) + " " + posString(s.b), nil
			}
			return "", nil
		}
		var b pos
		if len(args) > 3 {
			if b, err = x.index(w, args[3]); err != nil {
				return "", err
			}
		}
		for i := len(sp) - 1; i >= 0; i-- {
			s := sp[i]
			if !s.a.less(a) {
				continue
			}
			if s.a.less(b) {
				break
			}
			return posString(s.a) + " " + posString(s.b), nil
		}
		return "", nil
	case "configure":
		x.define(name)
		ts := x.styles[name]
		if ts == nil {
			ts = &tagstyle{}
			x.styles[name] = ts
		}
		return "", x.tagConfigure(w, ts, args[2:])
	case "cget":
		if len(args) != 3 {
			return "", tkerr(errBadop)
		}
		x.define(name)
		return x.tagCget(w, name, x.styles[name], args[2])
	case "bind":
		x.define(name)
		if len(args) < 3 {
			var n []string
			for _, b := range x.tbinds[name] {
				n = append(n, b.seq)
			}
			return strings.Join(n, " "), nil
		}
		p, err := parseSeq(args[2])
		if err != nil {
			return "", err
		}
		if len(args) == 3 {
			for _, b := range x.tbinds[name] {
				if b.pat == p {
					return b.script, nil
				}
			}
			return "", nil
		}
		x.tbinds[name] = setbinding(x.tbinds[name], p, args[2], args[3])
	case "raise", "lower":
		if _, ok := x.tags[name]; !ok {
			return "", tkerr(errBadtg)
		}
		var o []string
		for _, t := range x.order {
			if t != name {
				o = append(o, t)
			}
		}
		at := len(o)
		if args[0] == "lower" {
			at = 0
		}
		if len(args) > 2 {
			at = -1
			for i, t := range o {
				if t == args[2] {
					at = i
					if args[0] == "raise" {
						at++
					}
				}
			}
			if at < 0 {
				return "", tkerr(errBadtg)
			}
		}
		x.order = append(o[:at:at], append([]string{name}, o[at:]...)...)
	default:
		return "", tkerr(errBadcm, args[0])
	}
	return "", nil
}

// rowsPerPage is the number of rows of the widget's font in its area.
func (x *text) rowsPerPage(w *Widget) int { return max(1, x.textarea(w).Dy()/w.font.Height) }

func (x *text) yview(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return x.yfractions(), nil
	}
	switch args[0] {
	case "moveto":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		x.top = max(0, min(len(x.lines)-1, int(f*float64(len(x.lines)))))
	case "scroll":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		var n int
		switch {
		case strings.HasPrefix(args[2], "page"):
			n = int(f * float64(x.rowsPerPage(w)))
		case strings.HasPrefix(args[2], "unit"):
			n = int(f)
		default:
			return "", tkerr(errBadvl, args[2])
		}
		if n == 0 && f != 0 {
			n = 1
			if f < 0 {
				n = -1
			}
		}
		x.top = max(0, min(len(x.lines)-1, x.top+n))
	case "-pickplace":
		if len(args) == 2 {
			p, err := x.index(w, args[1])
			if err != nil {
				return "", err
			}
			x.see(w, p)
		}
	default:
		p, err := x.index(w, args[0])
		if err != nil {
			return "", err
		}
		x.top = p.line
	}
	return "", nil
}

func (x *text) xview(w *Widget, args []string) (string, error) {
	area := x.textarea(w)
	rows := x.layout(w, area.Dx(), area.Dy())
	if len(args) == 0 {
		return x.xfractions(w, rows), nil
	}
	m := widest(rows)
	switch args[0] {
	case "moveto":
		if len(args) != 2 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		x.xoff = int(f * float64(m))
	case "scroll":
		if len(args) != 3 {
			return "", tkerr(errBadvl)
		}
		f, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", tkerr(errBadvl, args[1])
		}
		if strings.HasPrefix(args[2], "page") {
			x.xoff += int(f * float64(area.Dx()))
		} else {
			x.xoff += int(f) * w.charwidth()
		}
	default:
		return "", tkerr(errBadvl, args[0])
	}
	if x.wrap != "none" {
		x.xoff = 0
	}
	x.xoff = max(0, min(x.xoff, m-area.Dx()/2))
	return "", nil
}

func (x *text) scancmd(w *Widget, args []string) (string, error) {
	if len(args) != 3 {
		return "", tkerr(errBadvl)
	}
	px, err := atoi(args[1])
	if err != nil {
		return "", err
	}
	py, err := atoi(args[2])
	if err != nil {
		return "", err
	}
	switch args[0] {
	case "mark":
		x.scan, x.scantop = draw.Pt(px, py), x.top
	case "dragto":
		d := 10 * (x.scan.Y - py) / max(1, w.font.Height)
		x.top = max(0, min(len(x.lines)-1, x.scantop+d))
	default:
		return "", tkerr(errBadcm, args[0])
	}
	return "", nil
}

// fireTags runs the tag bindings for e, lowest priority tag first.
func (x *text) fireTags(w *Widget, e *event, tags []string) {
	for _, tg := range tags {
		var best *binding
		bs := 0
		for i := range x.tbinds[tg] {
			if s := x.tbinds[tg][i].pat.matches(e); s > bs {
				best, bs = &x.tbinds[tg][i], s
			}
		}
		if best != nil && best.script != "" {
			_, _ = w.t.exec(w.substitute(best.script, e))
		}
	}
}

// track moves the current mark to the character under p and gives the
// tags it enters and leaves their Enter and Leave events.
func (x *text) track(w *Widget, p draw.Point, inside bool) {
	var now []string
	if inside {
		c := x.at(w, p)
		x.marks["current"] = c
		now = x.tagsAt(c)
	}
	has := func(l []string, s string) bool {
		for _, t := range l {
			if t == s {
				return true
			}
		}
		return false
	}
	var left, entered []string
	for _, t := range x.cur {
		if !has(now, t) {
			left = append(left, t)
		}
	}
	for _, t := range now {
		if !has(x.cur, t) {
			entered = append(entered, t)
		}
	}
	x.cur = now
	if len(x.tbinds) > 0 {
		x.fireTags(w, &event{kind: evLeave, p: p}, left)
		x.fireTags(w, &event{kind: evEnter, p: p}, entered)
	}
}

func (x *text) event(w *Widget, ev *event) {
	t := w.t
	switch ev.kind {
	case evPress:
		switch ev.button {
		case 1:
			p := x.at(w, ev.p)
			x.tags["sel"] = nil
			if ev.double {
				a, b := x.doubleclick(p)
				x.tagAdd("sel", a, b)
				x.marks["insert"] = a
			} else {
				x.marks["insert"], x.marks["anchor"] = p, p
			}
		case 4:
			_, _ = x.yview(w, []string{"scroll", "-0.2", "page"})
		case 5:
			_, _ = x.yview(w, []string{"scroll", "0.2", "page"})
		}
		t.dirty = true
	case evMotion:
		if ev.held == 0 {
			x.track(w, ev.p, true)
		}
		if ev.held&1 != 0 {
			p := x.at(w, ev.p)
			a := x.marks["anchor"]
			x.tags["sel"] = nil
			if p.less(a) {
				x.tagAdd("sel", p, a)
			} else {
				x.tagAdd("sel", a, p)
			}
			x.marks["insert"] = p
			t.dirty = true
		}
	case evEnter:
		x.track(w, ev.p, true)
	case evLeave:
		x.track(w, ev.p, false)
	case evKey:
		if w.state != stDisabled {
			x.key(w, ev.key)
		}
		t.dirty = true
	case evFocusIn, evFocusOut:
		t.dirty = true
	}
	switch ev.kind {
	case evPress, evRelease, evKey:
		if len(x.tbinds) > 0 {
			x.fireTags(w, ev, x.tagsAt(x.marks["current"]))
		}
	}
}

// Brackets and quotes for double clicks, as libtk (and acme) have them.
var (
	lefts  = []string{"{[(<«", "\n", "'\"`"}
	rights = []string{"}])>»", "\n", "'\"`"}
)

// doubleclick returns what a double click at p selects: from a bracket
// to its match, a line, or the word around p.
func (x *text) doubleclick(p pos) (pos, pos) {
	for i := range lefts {
		l, r := []rune(lefts[i]), []rune(rights[i])
		c := '\n'
		if p != (pos{}) {
			c = x.rune(x.fwd(p, -1))
		}
		if k := runeIndex(l, c); k >= 0 {
			if q, ok := x.clickmatch(c, r[k], 1, p); ok {
				return p, q
			}
			return p, p
		}
		c = x.rune(p)
		if k := runeIndex(r, c); k >= 0 {
			if q, ok := x.clickmatch(c, l[k], -1, p); ok {
				if c == '\n' {
					p = x.fwd(p, 1)
				}
				return q, p
			}
			return p, p
		}
	}
	a, b := p, p
	for b != x.end() && isword(x.rune(b)) {
		b = x.fwd(b, 1)
	}
	for a != (pos{}) && isword(x.rune(x.fwd(a, -1))) {
		a = x.fwd(a, -1)
	}
	return a, b
}

func runeIndex(rs []rune, c rune) int {
	for i, r := range rs {
		if r == c {
			return i
		}
	}
	return -1
}

// clickmatch finds the bracket cr that closes cl, looking from p in
// direction dir, and returns the position just inside it.
func (x *text) clickmatch(cl, cr rune, dir int, p pos) (pos, bool) {
	nest := 1
	for {
		if dir > 0 {
			if p == x.end() {
				return p, cl == '\n'
			}
			c := x.rune(p)
			if c == cr {
				if nest--; nest == 0 {
					return p, true
				}
			} else if c == cl {
				nest++
			}
			p = x.fwd(p, 1)
		} else {
			if p == (pos{}) {
				return p, cl == '\n'
			}
			q := x.fwd(p, -1)
			c := x.rune(q)
			if c == cr {
				if nest--; nest == 0 {
					return p, true
				}
			} else if c == cl {
				nest++
			}
			p = q
		}
	}
}

// key is the text's built-in key bindings (libtk's tktbinds).
func (x *text) key(w *Widget, k rune) {
	ins := x.marks["insert"]
	delsel := func() bool {
		sp := x.tags["sel"]
		if len(sp) == 0 {
			return false
		}
		x.delete(sp[0].a, sp[len(sp)-1].b)
		x.tags["sel"] = nil
		return true
	}
	switch k {
	case '\b':
		if !delsel() {
			x.delete(x.fwd(ins, -1), ins)
		}
	case 0x7F, 0x04:
		if !delsel() {
			x.delete(ins, x.fwd(ins, 1))
		}
	case 0x0B: // ^K
		if !delsel() {
			x.delete(ins, pos{ins.line, len(x.lines[ins.line])})
		}
	case 0x15: // ^U
		if !delsel() {
			if ins.col == 0 {
				x.delete(x.fwd(ins, -1), ins)
			} else {
				x.delete(pos{ins.line, 0}, ins)
			}
		}
	case 0x17: // ^W
		if !delsel() {
			p := ins
			line := x.lines[p.line]
			for p.col > 0 && !isword(line[p.col-1]) {
				p.col--
			}
			for p.col > 0 && isword(line[p.col-1]) {
				p.col--
			}
			if p == ins {
				p = x.fwd(ins, -1)
			}
			x.delete(p, ins)
		}
	case 0x01, draw.KeyHome:
		x.marks["insert"] = pos{ins.line, 0}
	case 0x05, draw.KeyEnd:
		x.marks["insert"] = pos{ins.line, len(x.lines[ins.line])}
	case 0x02, draw.KeyLeft:
		x.marks["insert"] = x.fwd(ins, -1)
	case 0x06, draw.KeyRight:
		x.marks["insert"] = x.fwd(ins, 1)
	case 0x10, draw.KeyUp:
		x.marks["insert"] = x.clamp(pos{ins.line - 1, ins.col})
	case 0x0E, draw.KeyDown:
		x.marks["insert"] = x.clamp(pos{ins.line + 1, ins.col})
	case 0x0F: // ^O
		x.insert(ins, "\n", x.tagsAround(ins))
		x.marks["insert"] = ins
	case draw.KeyPgup:
		_, _ = x.yview(w, []string{"scroll", "-0.75", "page"})
		return
	case 0x16, draw.KeyPgdown:
		_, _ = x.yview(w, []string{"scroll", "0.75", "page"})
		return
	case draw.KeyEsc:
		return
	default:
		if k != '\n' && k != '\t' && (k < 0x20 || k >= draw.KeySpec && k < 0xF900) {
			return
		}
		if sp := x.tags["sel"]; len(sp) > 0 && !ins.less(sp[0].a) && !sp[len(sp)-1].b.less(ins) {
			delsel()
		}
		p := x.marks["insert"]
		x.insert(p, string(k), x.tagsAround(p))
	}
	x.see(w, x.marks["insert"])
}

func sortStrings(s []string) { sort.Strings(s) }
