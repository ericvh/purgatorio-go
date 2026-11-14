package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// The grid geometry manager (grid(9)), with libtk's algorithm: rows and
// columns take the size of their largest single-cell slave, spanning
// slaves widen their beams by weight, and extra room in the master is
// shared by weight (or the grid is centred when all weights are zero).
// As in libtk the grid never shrinks below its requested size: a
// smaller master clips it on the bottom and right.

// grid is a master's grid.
type grid struct {
	cells      [][]gcell // [row][column]
	rows, cols []beam
	origin     draw.Point
}

// gcell is a cell; span is set only in the top left cell of a slave.
type gcell struct {
	w    *Widget
	span draw.Point
}

// beam is a row or a column.
type beam struct {
	act      int
	minsize  int
	maxsize  int
	weight   int
	pad      int
	name     string
	equalise bool
}

// gridding is how a slave sits in its cell.
type gridding struct {
	master       *Widget
	padx, pady   int // in total, both sides
	ipadx, ipady int
	sticky       int // anchor bits, and fill bits for both sides
}

const maxbeam = 0x7fffffff

func newbeams(n int) []beam {
	b := make([]beam, n)
	for i := range b {
		b[i].maxsize = maxbeam
	}
	return b
}

func (g *grid) dim() draw.Point { return draw.Pt(len(g.cols), len(g.rows)) }

// ensure grows the grid to at least d.
func (g *grid) ensure(d draw.Point) {
	od := g.dim()
	if d.X > od.X {
		g.cols = append(g.cols, newbeams(d.X-od.X)...)
		for y := range g.cells {
			g.cells[y] = append(g.cells[y], make([]gcell, d.X-od.X)...)
		}
	}
	nx := max(d.X, od.X)
	for y := od.Y; y < d.Y; y++ {
		g.cells = append(g.cells, make([]gcell, nx))
		g.rows = append(g.rows, newbeams(1)...)
	}
}

// index parses a row or column index: a number, end or a beam's name.
func gridindex(b []beam, s string) int {
	if s == "" {
		return -1
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	if s == "end" {
		return len(b)
	}
	for i := range b {
		if b[i].name != "" && b[i].name == s {
			return i
		}
	}
	return -1
}

// beamsize is the size of beams x0 to x1 without the padding at
// either end but with the padding between them.
func beamsize(b []beam, x0, x1 int) int {
	if x0 >= x1 {
		return 0
	}
	tot, fpad := b[x0].act, b[x0].pad
	for x := x0 + 1; x < x1; x++ {
		tot += b[x].act + max(b[x].pad, fpad)
		fpad = b[x].pad
	}
	return tot
}

func beamcellpos(b []beam, i int) int {
	if len(b) == 0 || i >= len(b) || i < 0 {
		return 0
	}
	x := b[0].pad + beamsize(b, 0, i)
	if i > 0 {
		x += max(b[i-1].pad, b[i].pad)
	}
	return x
}

func (g *grid) cellbbox(p draw.Point) draw.Rectangle {
	d := g.dim()
	p.X, p.Y = min(p.X, d.X), min(p.Y, d.Y)
	var r draw.Rectangle
	r.Min = draw.Pt(beamcellpos(g.cols, p.X), beamcellpos(g.rows, p.Y))
	r.Max = r.Min
	if p.X < d.X {
		r.Max.X += g.cols[p.X].act
	}
	if p.Y < d.Y {
		r.Max.Y += g.rows[p.Y].act
	}
	return r.Add(g.origin)
}

// findspan returns the top left cell of the slave covering p.
func (g *grid) findspan(p draw.Point) (draw.Point, bool) {
	d := g.dim()
	if p.X < 0 || p.Y < 0 || p.X >= d.X || p.Y >= d.Y || g.cells[p.Y][p.X].w == nil {
		return p, false
	}
	if g.cells[p.Y][p.X].span.X == 0 {
		w := g.cells[p.Y][p.X].w
		for p.Y >= 0 && g.cells[p.Y][p.X].w == w {
			p.Y--
		}
		p.Y++
		for p.X >= 0 && g.cells[p.Y][p.X].w == w {
			p.X--
		}
		p.X++
	}
	return p, true
}

// rowhasspan reports whether a slave spans across the top of row i.
func (g *grid) rowhasspan(i int) bool {
	d := g.dim()
	if i <= 0 || i >= d.Y {
		return false
	}
	for x := 0; x < d.X; x++ {
		if c := g.cells[i][x]; c.w != nil {
			if c.span.X == 0 {
				return true
			}
			x += c.span.X - 1
		}
	}
	return false
}

func (g *grid) colhasspan(i int) bool {
	d := g.dim()
	if i <= 0 || i >= d.X {
		return false
	}
	for y := 0; y < d.Y; y++ {
		if c := g.cells[y][i]; c.w != nil {
			if c.span.Y == 0 {
				return true
			}
			y += c.span.Y - 1
		}
	}
	return false
}

// where returns a slave's top left cell.
func (g *grid) where(w *Widget) (draw.Point, bool) {
	for y, row := range g.cells {
		for x, c := range row {
			if c.w == w {
				return draw.Pt(x, y), true
			}
		}
	}
	return draw.Point{}, false
}

// remove clears w's cells.
func (g *grid) remove(w *Widget) {
	p, ok := g.where(w)
	if !ok {
		return
	}
	span := g.cells[p.Y][p.X].span
	for y := p.Y; y < p.Y+max(1, span.Y) && y < len(g.rows); y++ {
		for x := p.X; x < p.X+max(1, span.X) && x < len(g.cols); x++ {
			if g.cells[y][x].w == w {
				g.cells[y][x] = gcell{}
			}
		}
	}
}

func parseSticky(s string) (int, error) {
	v := 0
	for _, c := range s {
		switch c {
		case 'n':
			v |= anN
		case 's':
			v |= anS
		case 'e':
			v |= anE
		case 'w':
			v |= anW
		case ' ', ',':
		default:
			return 0, tkerr(errBadvl, s)
		}
	}
	return v, nil
}

func cmdGrid(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	if c := args[0][0]; c >= 'a' && c <= 'z' {
		rest := args[1:]
		switch args[0] {
		case "forget":
			for _, n := range rest {
				w := t.widgets[n]
				if w == nil {
					return "", tkerr(errBadwp, n)
				}
				w.unpack()
			}
			return "", nil
		case "propagate":
			return gridPropagate(t, rest)
		case "slaves":
			return t.gridSlaves(rest)
		case "rowconfigure", "columnconfigure":
			return "", t.beamConfigure(rest, args[0] == "rowconfigure")
		case "rowinsert", "columninsert":
			return "", t.gridInsert(rest, args[0] == "rowinsert")
		case "rowdelete", "columndelete":
			return "", t.gridDelete(rest, args[0] == "rowdelete")
		case "rowindex", "columnindex":
			if len(rest) < 2 {
				return "", tkerr(errBadix)
			}
			m := t.widgets[rest[0]]
			if m == nil {
				return "", tkerr(errBadwp)
			}
			var b []beam
			if m.grid != nil {
				b = m.grid.cols
				if args[0] == "rowindex" {
					b = m.grid.rows
				}
			}
			i := gridindex(b, rest[1])
			if i < 0 {
				return "", tkerr(errBadix)
			}
			return itoa(i), nil
		case "size":
			m, err := t.gridMaster(rest)
			if err != nil {
				return "", err
			}
			if m.grid == nil {
				return "0 0", nil
			}
			d := m.grid.dim()
			return itoa(d.X) + " " + itoa(d.Y), nil
		case "bbox":
			return t.gridBbox(rest)
		case "location":
			return t.gridLocation(rest)
		case "cellinfo":
			return t.gridCellinfo(rest)
		case "info":
			if len(rest) == 0 {
				return "", tkerr(errBadwp)
			}
			w := t.widgets[rest[0]]
			if w == nil {
				return "", tkerr(errBadwp)
			}
			if w.gr == nil || w.gr.master.grid == nil {
				return "", tkerr(errNotgrid)
			}
			g := w.gr.master.grid
			p, ok := g.where(w)
			if !ok {
				return "", tkerr(errNotgrid)
			}
			sp := g.cells[p.Y][p.X].span
			return "-in " + w.gr.master.name + " -column " + itoa(p.X) + " -row " + itoa(p.Y) +
				" -columnspan " + itoa(sp.X) + " -rowspan " + itoa(sp.Y), nil
		case "debug":
			return "", nil
		}
		return "", tkerr(errBadcm, args[0])
	}
	return "", t.gridConfigure(args)
}

// gridMaster takes the master named by args[0]: it must have a grid
// or no slaves.
func (t *Toplevel) gridMaster(args []string) (*Widget, error) {
	if len(args) == 0 {
		return nil, tkerr(errBadwp)
	}
	m := t.widgets[args[0]]
	if m == nil {
		return nil, tkerr(errBadwp)
	}
	if m.grid == nil && len(m.slaves) > 0 {
		return nil, tkerr(errNotgrid)
	}
	return m, nil
}

func gridPropagate(t *Toplevel, args []string) (string, error) {
	return cmdPack(t, append([]string{"propagate"}, args...))
}

// gridConfigure puts slaves in a grid: names (and -, x, ^), then
// options for all of them.
func (t *Toplevel) gridConfigure(args []string) error {
	var names []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "-" || a == "x" || a == "^" || strings.HasPrefix(a, ".") {
			names = append(names, a)
			continue
		}
		break
	}
	opts := args[i:]
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	span := draw.Pt(1, 1)
	padx, pady, ipadx, ipady, sticky := -1, -1, -1, -1, -1
	var in *Widget
	row, col := "", ""
	rowset, colset := false, false
	for j := 0; j < len(opts); j += 2 {
		v := opts[j+1]
		var err error
		switch opts[j] {
		case "-padx":
			padx, err = t.nndist(v)
		case "-pady":
			pady, err = t.nndist(v)
		case "-ipadx":
			ipadx, err = t.nndist(v)
		case "-ipady":
			ipady, err = t.nndist(v)
		case "-in":
			if in = t.widgets[v]; in == nil {
				err = tkerr(errBadwp, v)
			}
		case "-row":
			row, rowset = v, true
		case "-column":
			col, colset = v, true
		case "-rowspan":
			span.Y, err = t.nndist(v)
		case "-columnspan":
			span.X, err = t.nndist(v)
		case "-sticky":
			sticky, err = parseSticky(v)
		default:
			err = tkerr(errBadop, opts[j])
		}
		if err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return nil
	}
	if span.X < 1 || span.Y < 1 {
		return tkerr(errBadvl)
	}
	var first *Widget
	objs := make([]*Widget, len(names))
	maxcol := 0
	for k, n := range names {
		if n == "-" || n == "x" || n == "^" {
			maxcol++
			continue
		}
		w := t.widgets[n]
		if w == nil {
			return tkerr(errBadwp, n)
		}
		if w == t.root {
			return tkerr(errIstop)
		}
		// As libtk: taking the slave out first means a slave
		// regridded in the same grid finds its old cell free.
		w.unpack()
		if first == nil {
			first = w
		}
		objs[k] = w
		maxcol += span.X
	}
	if in == nil && first != nil {
		in = first.parent
	}
	if in == nil {
		return tkerr(errNomaster)
	}
	g := in.grid
	if g == nil && len(in.slaves) > 0 {
		return tkerr(errNotgrid)
	}
	if g == nil {
		g = &grid{}
		in.grid = g
	}
	d := g.dim()
	var pos draw.Point
	if rowset {
		if pos.Y = gridindex(g.rows, row); pos.Y < 0 {
			return tkerr(errBadix)
		}
	} else {
		// The first unoccupied row.
		for pos.Y = 0; pos.Y < d.Y; pos.Y++ {
			empty := true
			for x := 0; x < d.X; x++ {
				if g.cells[pos.Y][x].w != nil {
					empty = false
					break
				}
			}
			if empty {
				break
			}
		}
	}
	if colset {
		if pos.X = gridindex(g.cols, col); pos.X < 0 {
			return tkerr(errBadix)
		}
	}
	g.ensure(draw.Pt(pos.X+maxcol, pos.Y+span.Y))
	start := pos
	// Check that the cells are free and the spans make sense.
	seen := map[*Widget]bool{}
	for k := 0; k < len(names); {
		switch names[k] {
		case "x":
			k++
			pos.X++
		case "^":
			q, ok := g.findspan(draw.Pt(pos.X, pos.Y-1))
			if !ok {
				return tkerr(errBadspan)
			}
			sp := g.cells[q.Y][q.X].span
			for i := 0; i < sp.X; i++ {
				if k >= len(names) || names[k] != "^" {
					return tkerr(errBadspan)
				}
				if g.cells[pos.Y][pos.X+i].w != nil {
					return tkerr(errBadgridcell)
				}
				k++
			}
			pos.X += sp.X
		case "-":
			return tkerr(errBadspan)
		default:
			w := objs[k]
			for m := in; m != nil; m = m.parentOrMaster() {
				if m == w {
					return tkerr(errRecur)
				}
			}
			k++
			if seen[w] {
				return tkerr(errWpack)
			}
			seen[w] = true
			sp := span
			for ; k < len(names) && names[k] == "-"; k++ {
				sp.X++
			}
			g.ensure(draw.Pt(pos.X+sp.X, pos.Y+sp.Y))
			for x := pos.X; x < pos.X+sp.X; x++ {
				for y := pos.Y; y < pos.Y+sp.Y; y++ {
					if g.cells[y][x].w != nil {
						return tkerr(errBadgridcell)
					}
				}
			}
			pos.X += sp.X
		}
	}
	// Put them in.
	pos = start
	for k := 0; k < len(names); {
		switch names[k] {
		case "x":
			k++
			pos.X++
		case "^":
			q, _ := g.findspan(draw.Pt(pos.X, pos.Y-1))
			c := &g.cells[q.Y][q.X]
			sp, w := c.span, c.w
			if q.Y+sp.Y == pos.Y {
				c.span.Y++
			}
			for i := 0; i < sp.X; i++ {
				g.cells[pos.Y][pos.X].w = w
				pos.X++
				k++
			}
		default:
			w := objs[k]
			k++
			sp := span
			for ; k < len(names) && names[k] == "-"; k++ {
				sp.X++
			}
			for x := pos.X; x < pos.X+sp.X; x++ {
				for y := pos.Y; y < pos.Y+sp.Y; y++ {
					g.cells[y][x].w = w
				}
			}
			g.cells[pos.Y][pos.X].span = sp
			gr := &gridding{master: in}
			if padx >= 0 {
				gr.padx = 2 * padx
			}
			if pady >= 0 {
				gr.pady = 2 * pady
			}
			if ipadx >= 0 {
				gr.ipadx = 2 * ipadx
			}
			if ipady >= 0 {
				gr.ipady = 2 * ipady
			}
			if sticky >= 0 {
				gr.sticky = sticky
			}
			w.gr = gr
			w.mapped = true
			in.slaves = append([]*Widget{w}, in.slaves...)
			w.deliver(&event{kind: evMap})
			pos.X += sp.X
		}
	}
	t.dirty = true
	return nil
}

func (t *Toplevel) beamConfigure(args []string, isrow bool) error {
	var names []string
	i := 0
	for ; i < len(args) && !strings.HasPrefix(args[i], "-"); i++ {
		names = append(names, args[i])
	}
	opts := args[i:]
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	minsize, maxsize, weight, pad, eq := -1, -1, -1, -1, -1
	name, nameset := "", false
	for j := 0; j < len(opts); j += 2 {
		v := opts[j+1]
		var err error
		switch opts[j] {
		case "-minsize":
			minsize, err = t.nndist(v)
		case "-maxsize":
			maxsize, err = t.nndist(v)
		case "-weight":
			weight, err = t.nndist(v)
		case "-pad":
			pad, err = t.nndist(v)
		case "-name":
			name, nameset = v, true
		case "-equalise":
			var b bool
			b, err = parseBool(v)
			eq = 0
			if b {
				eq = 1
			}
		default:
			err = tkerr(errBadop, opts[j])
		}
		if err != nil {
			return err
		}
	}
	if len(names) < 2 {
		return tkerr(errBadvl)
	}
	m := t.widgets[names[0]]
	if m == nil {
		return tkerr(errBadwp)
	}
	g := m.grid
	if g == nil {
		if len(m.slaves) > 0 {
			return tkerr(errNotgrid)
		}
		g = &grid{}
		m.grid = g
	}
	var idx []int
	for _, s := range strings.Fields(names[1]) {
		b := g.cols
		if isrow {
			b = g.rows
		}
		n := gridindex(b, s)
		if n < 0 {
			return tkerr(errBadix)
		}
		idx = append(idx, n)
	}
	for _, n := range idx {
		if isrow {
			g.ensure(draw.Pt(len(g.cols), n+1))
		} else {
			g.ensure(draw.Pt(n+1, len(g.rows)))
		}
		b := &g.cols[n]
		if isrow {
			b = &g.rows[n]
		}
		if minsize >= 0 {
			b.minsize = minsize
		}
		if maxsize >= 0 {
			b.maxsize = maxsize
		}
		if weight >= 0 {
			b.weight = weight
		}
		if pad >= 0 {
			b.pad = pad
		}
		if nameset {
			b.name = name
		}
		if eq >= 0 {
			b.equalise = eq == 1
		}
	}
	t.dirty = true
	return nil
}

func (t *Toplevel) gridSlaves(args []string) (string, error) {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return "", err
	}
	g := m.grid
	var n []string
	if len(args) < 2 {
		for _, s := range m.slaves {
			n = append(n, s.name)
		}
		return strings.Join(n, " "), nil
	}
	isrow := false
	switch args[1] {
	case "-row":
		isrow = true
	case "-column":
	default:
		return "", tkerr(errBadop)
	}
	v := ""
	if len(args) > 2 {
		v = args[2]
	}
	b := g.cols
	if isrow {
		b = g.rows
	}
	i := gridindex(b, v)
	if i < 0 {
		return "", tkerr(errBadix)
	}
	if i >= len(b) {
		return "", nil
	}
	d := g.dim()
	if isrow {
		for x := 0; x < d.X; x++ {
			if c := g.cells[i][x]; c.w != nil && c.span.X > 0 {
				n = append(n, c.w.name)
			}
		}
	} else {
		for y := 0; y < d.Y; y++ {
			if c := g.cells[y][i]; c.w != nil && c.span.X > 0 {
				n = append(n, c.w.name)
			}
		}
	}
	return strings.Join(n, " "), nil
}

func (t *Toplevel) gridInsert(args []string, isrow bool) error {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return err
	}
	g := m.grid
	d := g.dim()
	b, n := g.cols, d.X
	if isrow {
		b, n = g.rows, d.Y
	}
	if len(args) < 2 {
		return tkerr(errBadix)
	}
	at := gridindex(b, args[1])
	if at < 0 || at > n {
		return tkerr(errBadix)
	}
	count := 1
	if len(args) > 2 {
		c, err := strconv.Atoi(args[2])
		if err != nil || c < 0 {
			return tkerr(errBadvl)
		}
		count = c
	}
	if isrow {
		if g.rowhasspan(at) {
			return tkerr(errBadgridcell)
		}
		nc := make([][]gcell, count)
		for i := range nc {
			nc[i] = make([]gcell, d.X)
		}
		g.cells = append(g.cells[:at], append(nc, g.cells[at:]...)...)
		g.rows = append(g.rows[:at], append(newbeams(count), g.rows[at:]...)...)
	} else {
		if g.colhasspan(at) {
			return tkerr(errBadgridcell)
		}
		for y := range g.cells {
			r := g.cells[y]
			g.cells[y] = append(r[:at:at], append(make([]gcell, count), r[at:]...)...)
		}
		g.cols = append(g.cols[:at:at], append(newbeams(count), g.cols[at:]...)...)
	}
	t.dirty = true
	return nil
}

func (t *Toplevel) gridDelete(args []string, isrow bool) error {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return err
	}
	g := m.grid
	b := g.cols
	if isrow {
		b = g.rows
	}
	if len(args) < 2 {
		return tkerr(errBadix)
	}
	i0 := gridindex(b, args[1])
	if i0 < 0 {
		return tkerr(errBadix)
	}
	i1 := i0 + 1
	if len(args) > 2 {
		i1 = gridindex(b, args[2])
	}
	if i1 < 0 || i0 > i1 {
		return tkerr(errBadix)
	}
	if i0 > len(b) || i0 == i1 {
		return nil
	}
	i1 = min(i1, len(b))
	gone := map[*Widget]bool{}
	if isrow {
		if g.rowhasspan(i0) || g.rowhasspan(i1) {
			return tkerr(errBadgridcell)
		}
		for y := i0; y < i1; y++ {
			for _, c := range g.cells[y] {
				if c.w != nil {
					gone[c.w] = true
				}
			}
		}
		g.cells = append(g.cells[:i0], g.cells[i1:]...)
		g.rows = append(g.rows[:i0], g.rows[i1:]...)
	} else {
		if g.colhasspan(i0) || g.colhasspan(i1) {
			return tkerr(errBadgridcell)
		}
		for y := range g.cells {
			for _, c := range g.cells[y][i0:i1] {
				if c.w != nil {
					gone[c.w] = true
				}
			}
			g.cells[y] = append(g.cells[y][:i0], g.cells[y][i1:]...)
		}
		g.cols = append(g.cols[:i0], g.cols[i1:]...)
	}
	for w := range gone {
		w.unpack()
	}
	t.dirty = true
	return nil
}

// gridArgs parses "master ?x y?" style integer arguments.
func (t *Toplevel) gridPoint(args []string) (draw.Point, error) {
	if len(args) < 2 {
		return draw.Point{}, tkerr(errFewpt)
	}
	x, err := t.dist(args[0], nil)
	if err != nil {
		return draw.Point{}, err
	}
	y, err := t.dist(args[1], nil)
	return draw.Pt(x, y), err
}

func (t *Toplevel) gridBbox(args []string) (string, error) {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return "", err
	}
	t.layout()
	g := m.grid
	d := g.dim()
	var p0, p1 draw.Point
	switch {
	case len(args) == 1:
		p1 = d
	case len(args) == 2 || len(args) == 4:
		return "", tkerr(errFewpt)
	default:
		p0 = draw.Pt(gridindex(g.cols, args[1]), gridindex(g.rows, args[2]))
		p1 = p0
		if len(args) > 3 {
			p1 = draw.Pt(gridindex(g.cols, args[3]), gridindex(g.rows, args[4]))
		}
	}
	if p0.X < 0 || p0.Y < 0 || p1.X < 0 || p1.Y < 0 {
		return "", tkerr(errBadix)
	}
	r := g.cellbbox(p0)
	if p0 != p1 {
		s := g.cellbbox(p1)
		r = draw.Rect(min(r.Min.X, s.Min.X), min(r.Min.Y, s.Min.Y), max(r.Max.X, s.Max.X), max(r.Max.Y, s.Max.Y))
	}
	return itoa(r.Min.X) + " " + itoa(r.Min.Y) + " " + itoa(r.Max.X) + " " + itoa(r.Max.Y), nil
}

func findloc(b []beam, f int) int {
	if len(b) == 0 || f < 0 {
		return -1
	}
	x, fpad := 0, 0
	for i := range b {
		x += max(fpad, b[i].pad)
		if x <= f && f < x+b[i].act {
			return i
		}
		x += b[i].act
	}
	return -1
}

func (t *Toplevel) gridLocation(args []string) (string, error) {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return "", err
	}
	p, err := t.gridPoint(args[1:])
	if err != nil {
		return "", err
	}
	t.layout()
	g := m.grid
	p = p.Sub(g.origin)
	c, r := findloc(g.cols, p.X), findloc(g.rows, p.Y)
	if c < 0 || r < 0 {
		return "", nil
	}
	return itoa(c) + " " + itoa(r), nil
}

func (t *Toplevel) gridCellinfo(args []string) (string, error) {
	m, err := t.gridMaster(args)
	if err != nil || m.grid == nil {
		return "", err
	}
	p, err := t.gridPoint(args[1:])
	if err != nil {
		return "", err
	}
	g := m.grid
	q, ok := g.findspan(p)
	if !ok {
		return "", nil
	}
	c := g.cells[q.Y][q.X]
	return c.w.name + " -in " + m.name + " -column " + itoa(q.X) + " -row " + itoa(q.Y) +
		" -columnspan " + itoa(c.span.X) + " -rowspan " + itoa(c.span.Y), nil
}

// expandwidths shares totwidth among beams x0 to x1 by weight, or
// equally if none has weight and expandzero is set, and returns the
// space left over.
func expandwidths(b []beam, x0, x1, totwidth int, expandzero bool) int {
	if x0 >= x1 {
		return 0
	}
	share := 0
	for x := x0; x < x1; x++ {
		share += b[x].weight
	}
	slack := totwidth - beamsize(b, x0, x1)
	if slack <= 0 {
		return 0
	}
	equal := false
	if share == 0 && expandzero {
		share, equal = x1-x0, true
	}
	for x := x0; x < x1 && share > 0; x++ {
		w := b[x].weight
		if equal {
			w = 1
		}
		m := slack * w / share
		b[x].act += m
		slack -= m
		share -= w
	}
	return slack
}

func equalise(b []beam) {
	m := 0
	for i := range b {
		if b[i].equalise && b[i].act > m {
			m = b[i].act
		}
	}
	if m > 0 {
		for i := range b {
			if b[i].equalise {
				b[i].act = m
			}
		}
	}
}

func minmax(b []beam) {
	for i := range b {
		b[i].act = max(b[i].act, b[i].minsize)
		b[i].act = min(b[i].act, b[i].maxsize)
	}
}

// measure sets the beams to their requested sizes and returns the
// grid's size.
func (g *grid) measure() draw.Point {
	d := g.dim()
	for x := range g.cols {
		g.cols[x].act = 0
	}
	for y := 0; y < d.Y; y++ {
		g.rows[y].act = 0
		for x := 0; x < d.X; x++ {
			c := g.cells[y][x]
			if c.w == nil || c.w.gr == nil {
				continue
			}
			r := c.w.req()
			if c.span.X == 1 {
				g.cols[x].act = max(g.cols[x].act, r.X+c.w.gr.padx+c.w.gr.ipadx)
			}
			if c.span.Y == 1 {
				g.rows[y].act = max(g.rows[y].act, r.Y+c.w.gr.pady+c.w.gr.ipady)
			}
		}
	}
	minmax(g.rows)
	minmax(g.cols)
	for y := 0; y < d.Y; y++ {
		for x := 0; x < d.X; x++ {
			c := g.cells[y][x]
			if c.w == nil || c.w.gr == nil {
				continue
			}
			r := c.w.req()
			if c.span.X > 1 {
				expandwidths(g.cols, x, x+c.span.X, r.X+c.w.gr.padx+c.w.gr.ipadx, true)
			}
			if c.span.Y > 1 {
				expandwidths(g.rows, y, y+c.span.Y, r.Y+c.w.gr.pady+c.w.gr.ipady, true)
			}
		}
	}
	equalise(g.rows)
	equalise(g.cols)
	var req draw.Point
	if d.X > 0 {
		req.X = beamsize(g.cols, 0, d.X) + g.cols[0].pad + g.cols[d.X-1].pad
	}
	if d.Y > 0 {
		req.Y = beamsize(g.rows, 0, d.Y) + g.rows[0].pad + g.rows[d.Y-1].pad
	}
	return req
}

// gridsize is the size the grid needs inside the master's border.
func (w *Widget) gridsize() draw.Point { return w.grid.measure() }

// gridArrange places the slaves of w's grid.
func (w *Widget) gridArrange() {
	g := w.grid
	req := g.measure()
	d := g.dim()
	in := draw.Rect(0, 0, w.w, w.h).Inset(w.bd + w.hl)
	var org draw.Point
	if d.X > 0 && in.Dx() > req.X {
		org.X = expandwidths(g.cols, 0, d.X, in.Dx()-(g.cols[0].pad+g.cols[d.X-1].pad), false) / 2
	}
	if d.Y > 0 && in.Dy() > req.Y {
		org.Y = expandwidths(g.rows, 0, d.Y, in.Dy()-(g.rows[0].pad+g.rows[d.Y-1].pad), false) / 2
	}
	g.origin = org
	py, fpady := org.Y, 0
	for y := 0; y < d.Y; y++ {
		py += max(fpady, g.rows[y].pad)
		fpady = g.rows[y].pad
		px, fpadx := org.X, 0
		for x := 0; x < d.X; x++ {
			c := g.cells[y][x]
			px += max(fpadx, g.cols[x].pad)
			fpadx = g.cols[x].pad
			if c.w != nil && c.w.gr != nil && c.span.X > 0 {
				f := draw.Rect(px, py, px+beamsize(g.cols, x, x+c.span.X), py+beamsize(g.rows, y, y+c.span.Y))
				c.w.place(f.Add(in.Min), c.w.gr.padx, c.w.gr.pady, c.w.gr.ipadx, c.w.gr.ipady, c.w.gr.sticky)
			}
			px += g.cols[x].act
		}
		py += g.rows[y].act
	}
}

// place puts w in frame f as libtk's tksetslavereq does: padx and pady
// are the outside padding in total, sticky the anchor bits; a slave
// stuck to both sides fills that way.
func (w *Widget) place(f draw.Rectangle, padx, pady, ipadx, ipady, sticky int) {
	r := w.req()
	width := r.X + ipadx
	if sticky&(anE|anW) == anE|anW || width > f.Dx()-padx {
		width = f.Dx() - padx
	}
	height := r.Y + ipady
	if sticky&(anN|anS) == anN|anS || height > f.Dy()-pady {
		height = f.Dy() - pady
	}
	padx /= 2
	pady /= 2
	var x, y int
	switch {
	case sticky&anN != 0:
		y = f.Min.Y + pady
	case sticky&anS != 0:
		y = f.Max.Y - height - pady
	default:
		y = f.Min.Y + (f.Dy()-height)/2
	}
	switch {
	case sticky&anW != 0:
		x = f.Min.X + padx
	case sticky&anE != 0:
		x = f.Max.X - width - padx
	default:
		x = f.Min.X + (f.Dx()-width)/2
	}
	if width <= 0 || height <= 0 {
		w.x, w.y, w.w, w.h = x, y, 0, 0
		return
	}
	if w.x != x || w.y != y || w.w != width || w.h != height {
		w.x, w.y, w.w, w.h = x, y, width, height
		w.t.dirty = true
	}
	w.arrange()
}
