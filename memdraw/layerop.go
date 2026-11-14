package memdraw

// layerfn draws on dst, the screen image or a save area (insave), the
// part screenr of a layer, clipped also to clipr.
type layerfn func(dst *Image, screenr, clipr Rectangle, insave bool)

func layerop(fn layerfn, i *Image, r, clipr Rectangle, front *Image) {
	for {
		if front == i {
			// No one is in front of this part of the window.
			fn(i.Layer.Screen.Image, r, clipr, false)
			return
		}
		fr := front.Layer.Screenr
		if !RectXRect(r, fr) {
			front = front.Layer.Rear
			continue
		}
		break
	}
	fr := front.Layer.Screenr
	rear := front.Layer.Rear
	if fr.Max.Y < r.Max.Y {
		layerop(fn, i, Rect(r.Min.X, fr.Max.Y, r.Max.X, r.Max.Y), clipr, rear)
		r.Max.Y = fr.Max.Y
	}
	if r.Min.Y < fr.Min.Y {
		layerop(fn, i, Rect(r.Min.X, r.Min.Y, r.Max.X, fr.Min.Y), clipr, rear)
		r.Min.Y = fr.Min.Y
	}
	if fr.Max.X < r.Max.X {
		layerop(fn, i, Rect(fr.Max.X, r.Min.Y, r.Max.X, r.Max.Y), clipr, rear)
		r.Max.X = fr.Max.X
	}
	if r.Min.X < fr.Min.X {
		layerop(fn, i, Rect(r.Min.X, r.Min.Y, fr.Min.X, r.Max.Y), clipr, rear)
		r.Min.X = fr.Min.X
	}
	// r is covered by front, so put it in the save area.
	fn(i.Layer.Save, r, clipr, true)
}

// layerOp applies fn to the parts of screenr of layer i, on screen or in
// its save area (_memlayerop). screenr is already clipped to i.
func layerOp(fn layerfn, i *Image, screenr, clipr Rectangle) {
	l := i.Layer
	if !RectClip(&screenr, l.Screenr) {
		return
	}
	if l.Clear {
		fn(l.Screen.Image, screenr, clipr, false)
		return
	}
	r := screenr
	scr := l.Screen.Image.Clipr
	// The piece on the screen.
	if RectClip(&screenr, scr) {
		layerop(fn, i, screenr, clipr, l.Screen.Frontmost)
	}
	if r.In(scr) {
		return
	}
	// The piece off the screen.
	if !RectXRect(r, scr) {
		fn(l.Save, r, clipr, true)
		return
	}
	if r.Min.Y < scr.Min.Y {
		fn(l.Save, Rect(r.Min.X, r.Min.Y, r.Max.X, scr.Min.Y), clipr, true)
		r.Min.Y = scr.Min.Y
	}
	if r.Max.Y > scr.Max.Y {
		fn(l.Save, Rect(r.Min.X, scr.Max.Y, r.Max.X, r.Max.Y), clipr, true)
		r.Max.Y = scr.Max.Y
	}
	if r.Min.X < scr.Min.X {
		fn(l.Save, Rect(r.Min.X, r.Min.Y, scr.Min.X, r.Max.Y), clipr, true)
		r.Min.X = scr.Min.X
	}
	if r.Max.X > scr.Max.X {
		fn(l.Save, Rect(scr.Max.X, r.Min.Y, r.Max.X, r.Max.Y), clipr, true)
	}
}

// Draw is ImageDraw for images that may be windows (memdraw).
func Draw(dst *Image, r Rectangle, src *Image, p0 Point, mask *Image, p1 Point, op int) {
	if mask == nil {
		mask = Opaque
	}
	if mask.Layer != nil {
		return // too hard, at least for now
	}
	var srcr, mr Rectangle
	var dl *Layer
top:
	if dst.Layer == nil && src.Layer == nil {
		ImageDraw(dst, r, src, p0, mask, p1, op)
		return
	}
	if !DrawClip(dst, &r, src, &p0, mask, &p1, &srcr, &mr) {
		return
	}
	// Convert to screen coordinates.
	dl = dst.Layer
	if dl != nil {
		r = r.Add(dl.Delta)
	}
clearlayer:
	if dl != nil && dl.Clear {
		if src == dst {
			p0 = p0.Add(dl.Delta)
			src = dl.Screen.Image
		}
		dst = dl.Screen.Image
		goto top
	}
	sl := src.Layer
	if sl != nil {
		p0 = p0.Add(sl.Delta)
		srcr = srcr.Add(sl.Delta)
	}
	// If dst and src are the same layer, draw in the save area and
	// expose.
	if dl != nil && dst == src {
		if dl.Save == nil {
			return // refresh function makes this case unworkable
		}
		if RectXRect(r, srcr) {
			tr := r
			if srcr.Min.X < tr.Min.X {
				p1.X += tr.Min.X - srcr.Min.X
				tr.Min.X = srcr.Min.X
			}
			if srcr.Min.Y < tr.Min.Y {
				p1.Y += tr.Min.X - srcr.Min.X // sic
				tr.Min.Y = srcr.Min.Y
			}
			if srcr.Max.X > tr.Max.X {
				tr.Max.X = srcr.Max.X
			}
			if srcr.Max.Y > tr.Max.Y {
				tr.Max.Y = srcr.Max.Y
			}
			Lhide(dst, tr)
		} else {
			Lhide(dst, r)
			Lhide(dst, srcr)
		}
		Draw(dl.Save, r.Sub(dl.Delta), dl.Save, srcr.Min.Sub(src.Layer.Delta), mask, p1, op)
		Lexpose(dst, r)
		return
	}
	if sl != nil {
		if sl.Clear {
			src = sl.Screen.Image
			if dl != nil {
				r = r.Sub(dl.Delta)
			}
			goto top
		}
		// Relatively rare case; use the save area.
		if sl.Save == nil {
			return
		}
		Lhide(src, srcr)
		p0 = p0.Sub(sl.Delta)
		srcr = srcr.Sub(sl.Delta)
		src = sl.Save
	}
	// src is now an image; dst may be an image or a clear layer.
	if dst.Layer == nil {
		goto top
	}
	if dst.Layer.Clear {
		goto clearlayer
	}
	// dst is an obscured layer.
	deltas := p0.Sub(r.Min)
	deltam := p1.Sub(r.Min)
	layerOp(func(d *Image, screenr, clipr Rectangle, insave bool) {
		if insave && dl.Save == nil {
			return
		}
		p0 := screenr.Min.Add(deltas)
		p1 := screenr.Min.Add(deltam)
		r := screenr
		if insave {
			r = screenr.Sub(dl.Delta)
			clipr = clipr.Sub(dl.Delta)
		}
		if !r.In(clipr) {
			oclipr := d.Clipr
			d.Clipr = clipr
			var sr, mr Rectangle
			ok := DrawClip(d, &r, src, &p0, mask, &p1, &sr, &mr)
			d.Clipr = oclipr
			if !ok {
				return
			}
		}
		Draw(d, r, src, p0, mask, p1, op)
	}, dst, r, r)
}

// Lhide puts the part of screenr now on the screen into i's save area.
func Lhide(i *Image, screenr Rectangle) {
	l := i.Layer
	if l.Save == nil {
		return
	}
	if !RectClip(&screenr, l.Screen.Image.R) {
		return
	}
	layerOp(func(src *Image, screenr, _ Rectangle, _ bool) {
		if src != l.Save { // do nothing if src is already in save area
			Draw(l.Save, screenr.Sub(l.Delta), src, screenr.Min, nil, screenr.Min, S)
		}
	}, i, screenr, screenr)
}

// Lexpose puts the part of screenr now in i's save area onto the screen.
func Lexpose(i *Image, screenr Rectangle) {
	l := i.Layer
	if !RectClip(&screenr, l.Screen.Image.R) {
		return
	}
	layerOp(func(dst *Image, screenr, _ Rectangle, insave bool) {
		if insave {
			return
		}
		r := screenr.Sub(l.Delta)
		if l.Save != nil {
			Draw(dst, screenr, l.Save, r.Min, nil, r.Min, S)
		} else if l.Refreshfn != nil {
			l.Refreshfn(dst, r, l.Refreshptr)
		}
	}, i, screenr, screenr)
}
