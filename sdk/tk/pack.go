package tk

import (
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// The packer (pack(9)), with Tk's algorithm: each slave in turn takes a
// parcel along one side of the master's remaining cavity.

const (
	sideTop = iota
	sideBottom
	sideLeft
	sideRight
)

var sides = []string{"top", "bottom", "left", "right"}

const (
	fillX = 1 << iota
	fillY
)

type packing struct {
	master       *Widget
	side         int
	fill         int
	expand       bool
	anchor       int
	padx, pady   int // outside, each side
	ipadx, ipady int // inside, each side
}

func cmdPack(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "configure":
		return "", t.packConfigure(args[1:])
	case "forget":
		for _, n := range args[1:] {
			w := t.widgets[n]
			if w == nil {
				return "", tkerr(errBadwp, n)
			}
			w.unpack()
		}
		t.dirty = true
		return "", nil
	case "propagate":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		w := t.widgets[args[1]]
		if w == nil {
			return "", tkerr(errBadwp, args[1])
		}
		if len(args) == 2 {
			if w.propagate {
				return "1", nil
			}
			return "0", nil
		}
		b, err := parseBool(args[2])
		if err != nil {
			return "", err
		}
		w.propagate = b
		t.dirty = true
		return "", nil
	case "slaves":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		w := t.widgets[args[1]]
		if w == nil {
			return "", tkerr(errBadwp, args[1])
		}
		var n []string
		for _, s := range w.slaves {
			n = append(n, s.name)
		}
		return strings.Join(n, " "), nil
	case "info":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		w := t.widgets[args[1]]
		if w == nil {
			return "", tkerr(errBadwp, args[1])
		}
		if w.pk == nil {
			return "", tkerr(errNotpk, args[1])
		}
		p := w.pk
		fill := map[int]string{0: "none", fillX: "x", fillY: "y", fillX | fillY: "both"}[p.fill]
		exp := "0"
		if p.expand {
			exp = "1"
		}
		return "-in " + p.master.name + " -anchor " + anchorName(p.anchor) + " -expand " + exp +
			" -fill " + fill + " -ipadx " + itoa(p.ipadx) + " -ipady " + itoa(p.ipady) +
			" -padx " + itoa(p.padx) + " -pady " + itoa(p.pady) + " -side " + sides[p.side], nil
	}
	return "", t.packConfigure(args)
}

// packConfigure packs widgets: names, then options.
func (t *Toplevel) packConfigure(args []string) error {
	var ws []*Widget
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "."); i++ {
		w := t.widgets[args[i]]
		if w == nil {
			return tkerr(errBadwp, args[i])
		}
		if w == t.root {
			return tkerr(errIstop)
		}
		ws = append(ws, w)
	}
	opts := args[i:]
	if len(opts)%2 != 0 {
		return tkerr(errOparg, opts[len(opts)-1])
	}
	if len(ws) == 0 {
		return tkerr(errBadwp)
	}
	var master, before, after *Widget
	set := map[string]string{}
	for j := 0; j < len(opts); j += 2 {
		v := opts[j+1]
		switch opts[j] {
		case "-in", "-before", "-after":
			w := t.widgets[v]
			if w == nil {
				return tkerr(errBadwp, v)
			}
			switch opts[j] {
			case "-in":
				master = w
			case "-before":
				before = w
			default:
				after = w
			}
		case "-side", "-fill", "-expand", "-anchor", "-padx", "-pady", "-ipadx", "-ipady":
			set[opts[j]] = v
		default:
			return tkerr(errBadop, opts[j])
		}
	}
	if before != nil || after != nil {
		rel := before
		if rel == nil {
			rel = after
		}
		if rel.pk == nil {
			return tkerr(errNotpk, rel.name)
		}
		master = rel.pk.master
	}
	for k, w := range ws {
		p := w.pk
		if p == nil {
			p = &packing{anchor: anCenter}
		} else {
			p = &packing{}
			*p = *w.pk
		}
		for o, v := range set {
			var err error
			switch o {
			case "-side":
				p.side, err = lookup(sides, v)
			case "-fill":
				switch v {
				case "none":
					p.fill = 0
				case "x":
					p.fill = fillX
				case "y":
					p.fill = fillY
				case "both":
					p.fill = fillX | fillY
				default:
					err = tkerr(errBadvl, v)
				}
			case "-expand":
				p.expand, err = parseBool(v)
			case "-anchor":
				var ok bool
				if p.anchor, ok = anchors[v]; !ok {
					err = tkerr(errBadvl, v)
				}
			case "-padx":
				p.padx, err = t.nndist(v)
			case "-pady":
				p.pady, err = t.nndist(v)
			case "-ipadx":
				p.ipadx, err = t.nndist(v)
			case "-ipady":
				p.ipady, err = t.nndist(v)
			}
			if err != nil {
				return err
			}
		}
		m := master
		if m == nil {
			if w.pk != nil {
				m = w.pk.master
			} else {
				m = w.parent
			}
		}
		for x := m; x != nil; x = x.parentOrMaster() {
			if x == w {
				return tkerr(errRecur, w.name)
			}
		}
		w.unpack()
		p.master = m
		w.pk = p
		w.mapped = true
		// Where in the master's list: after the previous one of this
		// command, or as -before and -after say, else at the end.
		pos := len(m.slaves)
		switch {
		case k > 0:
			pos = m.slaveIndex(ws[k-1]) + 1
		case before != nil:
			pos = m.slaveIndex(before)
		case after != nil:
			pos = m.slaveIndex(after) + 1
		}
		if pos < 0 || pos > len(m.slaves) {
			pos = len(m.slaves)
		}
		m.slaves = append(m.slaves, nil)
		copy(m.slaves[pos+1:], m.slaves[pos:])
		m.slaves[pos] = w
		w.deliver(&event{kind: evMap})
	}
	t.dirty = true
	return nil
}

func (w *Widget) parentOrMaster() *Widget {
	if w.pk != nil {
		return w.pk.master
	}
	return w.parent
}

func (w *Widget) slaveIndex(s *Widget) int {
	for i, x := range w.slaves {
		if x == s {
			return i
		}
	}
	return -1
}

// unpack takes w out of its master.
func (w *Widget) unpack() {
	if w.pk == nil {
		return
	}
	m := w.pk.master
	if i := m.slaveIndex(w); i >= 0 {
		m.slaves = append(m.slaves[:i], m.slaves[i+1:]...)
	}
	w.pk = nil
	if w.kind != "menu" {
		w.mapped = false
		w.deliver(&event{kind: evUnmap})
	}
	w.t.dirty = true
}

// packsize is the size the slaves need, inside the master's border.
func (w *Widget) packsize() draw.Point {
	width, height, maxw, maxh := 0, 0, 0, 0
	for _, s := range w.slaves {
		p := s.pk
		r := s.req()
		cw := r.X + 2*p.padx + 2*p.ipadx
		ch := r.Y + 2*p.pady + 2*p.ipady
		if p.side == sideTop || p.side == sideBottom {
			maxw = max(maxw, cw+width)
			height += ch
		} else {
			maxh = max(maxh, ch+height)
			width += cw
		}
	}
	return draw.Pt(max(maxw, width), max(maxh, height))
}

// layout recomputes every widget's geometry.
func (t *Toplevel) layout() {
	t.gen++
	t.inlayout = true
	defer func() { t.inlayout = false }()
	r := t.root
	if !r.fixed {
		sz := r.req()
		if sz.X != r.w || sz.Y != r.h {
			r.w, r.h = sz.X, sz.Y
			t.dirty = true
		}
	} else if t.Image != nil {
		// Ask the window manager for a new size when the request
		// changes.
		sz := r.req()
		if sz != t.reqsize {
			if t.reqsize != (draw.Point{}) {
				p := r.abs()
				select {
				case t.Wreq <- "!reshape . -1 " + itoa(p.X) + " " + itoa(p.Y) + " " +
					itoa(p.X+sz.X) + " " + itoa(p.Y+sz.Y):
				default:
				}
			}
			t.reqsize = sz
		}
	}
	r.arrange()
	if t.menu != nil {
		t.menu.arrange()
	}
}

// arrange places w's slaves in it (Tk's ArrangePacking).
func (w *Widget) arrange() {
	if len(w.slaves) == 0 {
		return
	}
	in := draw.Rect(0, 0, w.w, w.h).Inset(w.bd + w.hl)
	in = draw.Rect(in.Min.X+w.padx, in.Min.Y+w.pady, in.Max.X-w.padx, in.Max.Y-w.pady)
	cx, cy, cw, ch := in.Min.X, in.Min.Y, max(0, in.Dx()), max(0, in.Dy())
	for i, s := range w.slaves {
		p := s.pk
		r := s.req()
		var fx, fy, fw, fh int
		if p.side == sideTop || p.side == sideBottom {
			fw = cw
			fh = r.Y + 2*p.pady + 2*p.ipady
			if p.expand {
				fh += yexpansion(w.slaves[i:], ch)
			}
			ch -= fh
			if ch < 0 {
				fh += ch
				ch = 0
			}
			fx = cx
			if p.side == sideTop {
				fy = cy
				cy += fh
			} else {
				fy = cy + ch
			}
		} else {
			fh = ch
			fw = r.X + 2*p.padx + 2*p.ipadx
			if p.expand {
				fw += xexpansion(w.slaves[i:], cw)
			}
			cw -= fw
			if cw < 0 {
				fw += cw
				cw = 0
			}
			fy = cy
			if p.side == sideLeft {
				fx = cx
				cx += fw
			} else {
				fx = cx + cw
			}
		}
		bw, bh := 2*p.padx, 2*p.pady
		width := r.X + 2*p.ipadx
		if p.fill&fillX != 0 || width > fw-bw {
			width = fw - bw
		}
		height := r.Y + 2*p.ipady
		if p.fill&fillY != 0 || height > fh-bh {
			height = fh - bh
		}
		x, y := fx+(fw-width)/2, fy+(fh-height)/2
		switch {
		case p.anchor&anW != 0:
			x = fx + p.padx
		case p.anchor&anE != 0:
			x = fx + fw - width - p.padx
		}
		switch {
		case p.anchor&anN != 0:
			y = fy + p.pady
		case p.anchor&anS != 0:
			y = fy + fh - height - p.pady
		}
		if width <= 0 || height <= 0 {
			s.x, s.y, s.w, s.h = x, y, 0, 0
			continue
		}
		if s.x != x || s.y != y || s.w != width || s.h != height {
			s.x, s.y, s.w, s.h = x, y, width, height
			s.t.dirty = true
		}
		s.arrange()
	}
}

func xexpansion(slaves []*Widget, cavity int) int {
	nexp, minexp := 0, cavity
	for _, s := range slaves {
		p := s.pk
		cw := s.req().X + 2*p.padx + 2*p.ipadx
		if p.side == sideTop || p.side == sideBottom {
			if nexp > 0 {
				minexp = min(minexp, (cavity-cw)/nexp)
			}
		} else {
			cavity -= cw
			if p.expand {
				nexp++
			}
		}
	}
	if nexp > 0 {
		minexp = min(minexp, cavity/nexp)
	}
	return max(0, minexp)
}

func yexpansion(slaves []*Widget, cavity int) int {
	nexp, minexp := 0, cavity
	for _, s := range slaves {
		p := s.pk
		ch := s.req().Y + 2*p.pady + 2*p.ipady
		if p.side == sideLeft || p.side == sideRight {
			if nexp > 0 {
				minexp = min(minexp, (cavity-ch)/nexp)
			}
		} else {
			cavity -= ch
			if p.expand {
				nexp++
			}
		}
	}
	if nexp > 0 {
		minexp = min(minexp, cavity/nexp)
	}
	return max(0, minexp)
}
