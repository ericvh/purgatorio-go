package tk

import (
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// canvas is a drawing surface of items (canvas(9)): rectangles, ovals,
// lines, polygons and text, addressed by id or tag. Items can have
// bindings. Arcs, bitmaps, images and windows are not supported.
type canvas struct {
	items  []*citem
	next   int
	binds  map[string][]binding
	under  *citem
	scroll [4]int // -scrollregion
}

type citem struct {
	id      int
	typ     string
	pts     []draw.Point
	tags    []string
	fill    uint32
	outline uint32
	width   int
	text    string
	anchor  int
	hasFill bool
	hasOut  bool
	arrow   string
	smooth  bool
	stipple string
	justify string
}

func (c *canvas) defaults(w *Widget) {
	c.next = 1
	c.binds = map[string][]binding{}
}

func (c *canvas) option(w *Widget, name, val string) (bool, error) {
	switch name {
	case "scrollregion":
		f := strings.Fields(val)
		if len(f) == 4 {
			for i := range f {
				c.scroll[i], _ = w.t.dist(f[i], w)
			}
		}
	case "xscrollcommand", "yscrollcommand", "closeenough", "confine", "xscrollincrement",
		"yscrollincrement", "buffer":
	default:
		return false, nil
	}
	return true, nil
}

func (c *canvas) get(w *Widget, name string) (string, bool) {
	switch name {
	case "scrollregion":
		return itoa(c.scroll[0]) + " " + itoa(c.scroll[1]) + " " + itoa(c.scroll[2]) + " " + itoa(c.scroll[3]), true
	}
	return "", false
}

func (c *canvas) natural(w *Widget) draw.Point { return draw.Pt(100, 100) }

func (it *citem) has(tag string) bool {
	if tag == "all" || tag == strconv.Itoa(it.id) {
		return true
	}
	for _, t := range it.tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (c *canvas) find(tag string) []*citem {
	var out []*citem
	for _, it := range c.items {
		if it.has(tag) {
			out = append(out, it)
		}
	}
	return out
}

// coords parses coordinates: separate words or one list.
func (c *canvas) coords(w *Widget, args []string) ([]draw.Point, []string, error) {
	var nums []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 && (a[1] < '0' || a[1] > '9') && a[1] != '.' {
			break
		}
		nums = append(nums, strings.Fields(a)...)
	}
	if len(nums)%2 != 0 {
		return nil, nil, tkerr(errFewpt)
	}
	var pts []draw.Point
	for k := 0; k < len(nums); k += 2 {
		x, err := w.t.dist(nums[k], w)
		if err != nil {
			return nil, nil, err
		}
		y, err := w.t.dist(nums[k+1], w)
		if err != nil {
			return nil, nil, err
		}
		pts = append(pts, draw.Pt(x, y))
	}
	return pts, args[i:], nil
}

func (c *canvas) configure(w *Widget, it *citem, opts []string) error {
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	for k := 0; k < len(opts); k += 2 {
		v := opts[k+1]
		var err error
		switch opts[k] {
		case "-fill":
			if v == "" {
				it.hasFill = false
			} else {
				it.fill, err = parseColor(v)
				it.hasFill = err == nil
			}
		case "-outline":
			if v == "" {
				it.hasOut = false
			} else {
				it.outline, err = parseColor(v)
				it.hasOut = err == nil
			}
		case "-width":
			it.width, err = w.t.nndist(v)
		case "-text":
			it.text = v
		case "-anchor":
			var ok bool
			if it.anchor, ok = anchors[v]; !ok {
				err = tkerr(errBadvl, v)
			}
		case "-tags":
			it.tags = strings.Fields(v)
		case "-arrow":
			it.arrow = v
		case "-smooth":
			it.smooth, err = parseBool(v)
		case "-stipple":
			it.stipple = v
		case "-justify":
			it.justify = v
		case "-font", "-capstyle", "-joinstyle", "-arrowshape", "-splinesteps":
		default:
			err = tkerr(errBadop, opts[k])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *canvas) itemcget(it *citem, opt string) (string, error) {
	switch opt {
	case "-fill":
		if !it.hasFill {
			return "", nil
		}
		return colorString(it.fill), nil
	case "-outline":
		if !it.hasOut {
			return "", nil
		}
		return colorString(it.outline), nil
	case "-width":
		return itoa(it.width), nil
	case "-text":
		return it.text, nil
	case "-anchor":
		return anchorName(it.anchor), nil
	case "-tags":
		return strings.Join(it.tags, " "), nil
	}
	return "", tkerr(errBadop, opt)
}

func (c *canvas) create(w *Widget, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadit)
	}
	it := &citem{typ: args[0], width: 1}
	switch it.typ {
	case "rectangle", "oval":
		it.outline, it.hasOut = 0x000000FF, true
	case "line", "polygon", "text":
		it.fill, it.hasFill = 0x000000FF, true
	default:
		return "", tkerr(errBadit, args[0])
	}
	pts, opts, err := c.coords(w, args[1:])
	if err != nil {
		return "", err
	}
	need := map[string]int{"rectangle": 2, "oval": 2, "line": 2, "polygon": 3, "text": 1}[it.typ]
	if len(pts) < need || (it.typ == "rectangle" || it.typ == "oval" || it.typ == "text") && len(pts) != need {
		return "", tkerr(errFewpt)
	}
	it.pts = pts
	if err := c.configure(w, it, opts); err != nil {
		return "", err
	}
	it.id = c.next
	c.next++
	c.items = append(c.items, it)
	w.t.dirty = true
	return itoa(it.id), nil
}

func (c *canvas) bbox(w *Widget, it *citem) draw.Rectangle {
	if it.typ == "text" {
		sz := w.t.textsize(it.text)
		p := it.pts[0]
		r := draw.Rectangle{Min: p, Max: p.Add(sz)}
		// The anchor names the point of the text at pts[0].
		dx, dy := 0, 0
		switch {
		case it.anchor&anW != 0:
		case it.anchor&anE != 0:
			dx = -sz.X
		default:
			dx = -sz.X / 2
		}
		switch {
		case it.anchor&anN != 0:
		case it.anchor&anS != 0:
			dy = -sz.Y
		default:
			dy = -sz.Y / 2
		}
		return r.Add(draw.Pt(dx, dy))
	}
	r := draw.Rectangle{Min: it.pts[0], Max: it.pts[0]}
	for _, p := range it.pts[1:] {
		r.Min.X, r.Min.Y = min(r.Min.X, p.X), min(r.Min.Y, p.Y)
		r.Max.X, r.Max.Y = max(r.Max.X, p.X), max(r.Max.Y, p.Y)
	}
	h := (it.width + 1) / 2
	return draw.Rect(r.Min.X-h, r.Min.Y-h, r.Max.X+h+1, r.Max.Y+h+1)
}

func (c *canvas) paint(w *Widget, dst *draw.Image, r draw.Rectangle) {
	o := r.Min
	for _, it := range c.items {
		pts := make([]draw.Point, len(it.pts))
		for i, p := range it.pts {
			pts[i] = p.Add(o)
		}
		fill, out := w.col(it.fill), w.col(it.outline)
		switch it.typ {
		case "rectangle":
			rr := draw.Rect(min(pts[0].X, pts[1].X), min(pts[0].Y, pts[1].Y), max(pts[0].X, pts[1].X), max(pts[0].Y, pts[1].Y))
			if it.hasFill {
				dst.Draw(rr, fill, nil, draw.ZP)
			}
			if it.hasOut && it.width > 0 {
				dst.Border(rr, it.width, out, draw.ZP)
			}
		case "oval":
			cx, cy := (pts[0].X+pts[1].X)/2, (pts[0].Y+pts[1].Y)/2
			a, b := abs(pts[1].X-pts[0].X)/2, abs(pts[1].Y-pts[0].Y)/2
			if it.hasFill {
				dst.FillEllipse(draw.Pt(cx, cy), a, b, fill, draw.ZP)
			}
			if it.hasOut && it.width > 0 {
				dst.Ellipse(draw.Pt(cx, cy), a, b, (it.width-1)/2, out, draw.ZP)
			}
		case "line":
			if it.hasFill {
				e0, e1 := draw.Endsquare, draw.Endsquare
				if it.arrow == "first" || it.arrow == "both" {
					e0 = draw.Endarrow
				}
				if it.arrow == "last" || it.arrow == "both" {
					e1 = draw.Endarrow
				}
				dst.Poly(pts, e0, e1, (it.width-1)/2, fill, draw.ZP)
			}
		case "polygon":
			if it.hasFill {
				dst.FillPoly(pts, 0, fill, draw.ZP)
			}
			if it.hasOut && it.width > 0 {
				dst.Poly(append(pts, pts[0]), draw.Endsquare, draw.Endsquare, (it.width-1)/2, out, draw.ZP)
			}
		case "text":
			if it.hasFill {
				b := c.bbox(w, it).Add(o)
				w.drawText(dst, b, it.text, anW|anN, it.fill)
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (c *canvas) cmd(w *Widget, args []string) (string, error) {
	t := w.t
	t.dirty = true
	if len(args) < 2 && args[0] != "find" {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "create":
		return c.create(w, args[1:])
	case "delete":
		for _, tag := range args[1:] {
			var keep []*citem
			for _, it := range c.items {
				if !it.has(tag) {
					keep = append(keep, it)
				} else if it == c.under {
					c.under = nil
				}
			}
			c.items = keep
		}
		return "", nil
	case "coords":
		its := c.find(args[1])
		if len(its) == 0 {
			return "", nil
		}
		if len(args) == 2 {
			var s []string
			for _, p := range its[0].pts {
				s = append(s, itoa(p.X), itoa(p.Y))
			}
			return strings.Join(s, " "), nil
		}
		pts, _, err := c.coords(w, args[2:])
		if err != nil {
			return "", err
		}
		its[0].pts = pts
		return "", nil
	case "itemconfigure":
		for _, it := range c.find(args[1]) {
			if err := c.configure(w, it, args[2:]); err != nil {
				return "", err
			}
		}
		return "", nil
	case "itemcget":
		its := c.find(args[1])
		if len(its) == 0 || len(args) != 3 {
			return "", tkerr(errBadtg, args[1])
		}
		return c.itemcget(its[0], args[2])
	case "move":
		if len(args) != 4 {
			return "", tkerr(errBadvl)
		}
		dx, err := t.dist(args[2], w)
		if err != nil {
			return "", err
		}
		dy, err := t.dist(args[3], w)
		if err != nil {
			return "", err
		}
		for _, it := range c.find(args[1]) {
			for i := range it.pts {
				it.pts[i] = it.pts[i].Add(draw.Pt(dx, dy))
			}
		}
		return "", nil
	case "type":
		if its := c.find(args[1]); len(its) > 0 {
			return its[0].typ, nil
		}
		return "", nil
	case "gettags":
		if its := c.find(args[1]); len(its) > 0 {
			return strings.Join(its[0].tags, " "), nil
		}
		return "", nil
	case "find":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		var its []*citem
		switch args[1] {
		case "all":
			its = c.items
		case "withtag":
			if len(args) != 3 {
				return "", tkerr(errBadvl)
			}
			its = c.find(args[2])
		case "closest", "overlapping", "enclosed":
			pts, _, err := c.coords(w, args[2:])
			if err != nil || len(pts) == 0 {
				return "", tkerr(errBadvl)
			}
			for k := len(c.items) - 1; k >= 0; k-- {
				if pts[0].In(c.bbox(w, c.items[k])) {
					its = append(its, c.items[k])
					if args[1] == "closest" {
						break
					}
				}
			}
		default:
			return "", tkerr(errBadvl, args[1])
		}
		var s []string
		for _, it := range its {
			s = append(s, itoa(it.id))
		}
		return strings.Join(s, " "), nil
	case "addtag":
		if len(args) < 3 {
			return "", tkerr(errBadvl)
		}
		var its []*citem
		switch args[2] {
		case "all":
			its = c.items
		case "withtag":
			if len(args) != 4 {
				return "", tkerr(errBadvl)
			}
			its = c.find(args[3])
		default:
			return "", tkerr(errBadvl, args[2])
		}
		for _, it := range its {
			if !it.has(args[1]) {
				it.tags = append(it.tags, args[1])
			}
		}
		return "", nil
	case "dtag":
		drop := args[1]
		if len(args) > 2 {
			drop = args[2]
		}
		for _, it := range c.find(args[1]) {
			var keep []string
			for _, tg := range it.tags {
				if tg != drop {
					keep = append(keep, tg)
				}
			}
			it.tags = keep
		}
		return "", nil
	case "raise", "lower":
		its := c.find(args[1])
		var rest []*citem
		for _, it := range c.items {
			if !it.has(args[1]) {
				rest = append(rest, it)
			}
		}
		if args[0] == "raise" {
			c.items = append(rest, its...)
		} else {
			c.items = append(its, rest...)
		}
		return "", nil
	case "bbox":
		var r draw.Rectangle
		for i, tag := range args[1:] {
			for k, it := range c.find(tag) {
				b := c.bbox(w, it)
				if i == 0 && k == 0 {
					r = b
				} else {
					r.Min.X, r.Min.Y = min(r.Min.X, b.Min.X), min(r.Min.Y, b.Min.Y)
					r.Max.X, r.Max.Y = max(r.Max.X, b.Max.X), max(r.Max.Y, b.Max.Y)
				}
			}
		}
		if r.Empty() {
			return "", nil
		}
		return itoa(r.Min.X) + " " + itoa(r.Min.Y) + " " + itoa(r.Max.X) + " " + itoa(r.Max.Y), nil
	case "bind":
		if len(args) < 3 {
			return "", tkerr(errBadvl)
		}
		p, err := parseSeq(args[2])
		if err != nil {
			return "", err
		}
		bs := c.binds[args[1]]
		if len(args) == 3 {
			for _, b := range bs {
				if b.pat == p {
					return b.script, nil
				}
			}
			return "", nil
		}
		for i := range bs {
			if bs[i].pat == p {
				bs[i].script = args[3]
				return "", nil
			}
		}
		c.binds[args[1]] = append(bs, binding{p, args[3]})
		return "", nil
	case "xview", "yview":
		return "0 1", nil
	case "canvasx", "canvasy":
		return args[1], nil
	case "focus", "select", "icursor", "index", "insert", "dchars", "postscript", "scale":
		return "", nil
	}
	return "", tkerr(errBadcm, args[0])
}

// event runs item bindings for the item under the pointer.
func (c *canvas) event(w *Widget, e *event) {
	switch e.kind {
	case evPress, evRelease, evMotion, evEnter, evLeave:
	default:
		return
	}
	o := w.inside().Min
	var at *citem
	if e.kind != evLeave {
		p := e.p.Sub(o)
		for k := len(c.items) - 1; k >= 0; k-- {
			if p.In(c.bbox(w, c.items[k])) {
				at = c.items[k]
				break
			}
		}
	}
	if e.held == 0 && at != c.under {
		if c.under != nil {
			c.fire(w, c.under, &event{kind: evLeave, p: e.p})
		}
		c.under = at
		if at != nil {
			c.fire(w, at, &event{kind: evEnter, p: e.p})
		}
	}
	if e.kind == evEnter || e.kind == evLeave {
		return
	}
	target := at
	if e.held != 0 || e.kind == evRelease {
		target = c.under
	}
	if target != nil {
		c.fire(w, target, e)
	}
}

func (c *canvas) fire(w *Widget, it *citem, e *event) {
	tags := append([]string{strconv.Itoa(it.id)}, it.tags...)
	tags = append(tags, "all")
	for _, tg := range tags {
		var best *binding
		bs := 0
		for i := range c.binds[tg] {
			if s := c.binds[tg][i].pat.matches(e); s > bs {
				best, bs = &c.binds[tg][i], s
			}
		}
		if best != nil {
			_, _ = w.t.exec(w.substitute(best.script, e))
		}
	}
}
