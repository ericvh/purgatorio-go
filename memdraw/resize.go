package memdraw

import "errors"

// Resize changes the rectangle of i, a screen image, to r, as when the
// host window showing it changes size. Inferno's libmemdraw has no
// counterpart: its screens never change size.
//
// The pixels of i that are in r stay, and the rest of r is white. The
// image keeps its Data, which windows on it share, so screens holds
// every screen whose image shares i's data: those on i itself, whose
// windows are fixed up, and those on windows of i, whose windows only
// learn the new row width. A window on i keeps its rectangle; the part
// of it that goes off the screen goes into its save area, as an
// obscured part does, and the part that comes onto the screen is put
// back from its save area or refreshed. What comes onto the screen
// outside every window is painted with the screen's fill.
func (i *Image) Resize(r Rectangle, screens []*Screen) error {
	if i.Layer != nil {
		return errNotScreen
	}
	old := i.R
	if r == old {
		return nil
	}
	n, err := Alloc(r, i.Chan)
	if err != nil {
		return err
	}
	n.FillColor(DWhite)
	var roots []*Screen
	for _, s := range screens {
		if s.Image == i {
			roots = append(roots, s)
		}
	}
	// What goes off the screen goes into the save areas.
	for _, s := range roots {
		for w := s.Frontmost; w != nil; w = w.Layer.Rear {
			for _, p := range rectDiff(intersect(w.Layer.Screenr, old), r) {
				Lhide(w, p)
			}
		}
	}
	keep := intersect(old, r)
	if !keep.Empty() {
		ImageDraw(n, keep, i, keep.Min, nil, keep.Min, S)
	}
	i.Data.Bdata = n.Data.Bdata
	i.R, i.Clipr = r, r
	i.Width, i.Zero = n.Width, n.Zero
	for _, s := range screens {
		if s.Image.Data == i.Data {
			s.Image.Width, s.Image.Zero = i.Width, i.Zero
		}
		for w := s.Frontmost; w != nil; w = w.Layer.Rear {
			if w.Data == i.Data {
				w.Width, w.Zero = i.Width, i.Zero
			}
		}
	}
	// What comes onto the screen: the fill, then the windows.
	exposed := rectDiff(r, old)
	for _, s := range roots {
		lsetclear(s)
		if s.Fill != nil {
			for _, p := range exposed {
				Draw(i, p, s.Fill, p.Min, nil, p.Min, S)
			}
		}
		for w := s.Rearmost; w != nil; w = w.Layer.Front {
			for _, p := range exposed {
				if q := intersect(p, w.Layer.Screenr); !q.Empty() {
					Lexpose(w, q)
				}
			}
		}
	}
	return nil
}

var errNotScreen = errors.New("resize of a window")

// intersect returns the rectangle common to a and b, empty if none.
func intersect(a, b Rectangle) Rectangle {
	if !RectClip(&a, b) {
		return Rectangle{}
	}
	return a
}

// rectDiff returns a minus b as at most four disjoint rectangles.
func rectDiff(a, b Rectangle) []Rectangle {
	if a.Empty() {
		return nil
	}
	if !RectXRect(a, b) {
		return []Rectangle{a}
	}
	var out []Rectangle
	if a.Min.Y < b.Min.Y {
		out = append(out, Rect(a.Min.X, a.Min.Y, a.Max.X, b.Min.Y))
		a.Min.Y = b.Min.Y
	}
	if a.Max.Y > b.Max.Y {
		out = append(out, Rect(a.Min.X, b.Max.Y, a.Max.X, a.Max.Y))
		a.Max.Y = b.Max.Y
	}
	if a.Min.X < b.Min.X {
		out = append(out, Rect(a.Min.X, a.Min.Y, b.Min.X, a.Max.Y))
	}
	if a.Max.X > b.Max.X {
		out = append(out, Rect(b.Max.X, a.Min.Y, a.Max.X, a.Max.Y))
	}
	return out
}
