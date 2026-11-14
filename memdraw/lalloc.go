package memdraw

// NoRefresh is the refresh function of a window whose obscured parts
// need not be restored (memlnorefresh).
func NoRefresh(*Image, Rectangle, any) {}

// Lalloc makes a window on s with screen rectangle screenr, in front of
// all others, painted with val (memlalloc). A nil refreshfn means the
// window keeps a save area for its obscured parts.
func Lalloc(s *Screen, screenr Rectangle, refreshfn Refreshfn, refreshptr any, val uint32) (*Image, error) {
	n, err := AllocData(screenr, s.Image.Chan, s.Image.Data)
	if err != nil {
		return nil, err
	}
	l := &Layer{Screen: s}
	if refreshfn == nil {
		l.Save, err = Alloc(screenr, s.Image.Chan)
		if err != nil {
			return nil, err
		}
		if val != DNofill {
			l.Save.FillColor(val)
		}
	}
	l.Refreshfn = refreshfn
	l.Screenr = screenr
	n.Data.Ref++
	n.Zero = s.Image.Zero
	n.Width = s.Image.Width
	n.Layer = l

	// Start with the new window behind all existing ones.
	l.Front = s.Rearmost
	if s.Rearmost != nil {
		s.Rearmost.Layer.Rear = n
	}
	s.Rearmost = n
	if s.Frontmost == nil {
		s.Frontmost = n
	}
	// Now pull it to the front.
	ltofront(n, nil, val != DNofill)
	lsetclear(s)
	l.Refreshptr = refreshptr

	// Paint with the requested color.
	if val != DNofill {
		paint, _ := Alloc(Rect(0, 0, 1, 1), RGBA32)
		paint.Flags |= Frepl
		paint.Clipr = Rect(-0x3FFFFFF, -0x3FFFFFF, 0x3FFFFFF, 0x3FFFFFF)
		paint.SetChan(n.Chan)
		paint.FillColor(val)
		Draw(n, n.R, paint, n.R.Min, nil, n.R.Min, S)
	}
	return n, nil
}

// Ldelete removes window i from its screen, repainting what it covered
// (memldelete).
func Ldelete(i *Image) {
	l := i.Layer
	l.Save = nil
	l.Refreshptr = nil
	Ltorear(i)
	s := l.Screen
	if s.Fill != nil {
		i.Clipr = i.R
		Draw(i, i.R, s.Fill, i.R.Min, nil, i.R.Min, S)
	}
	if l.Front != nil {
		l.Front.Layer.Rear = nil
		s.Rearmost = l.Front
	} else {
		s.Frontmost = nil
		s.Rearmost = nil
	}
	i.Free()
}

// Lfree frees window i without graphics (memlfree).
func Lfree(i *Image) {
	i.Layer.Save = nil
	i.Free()
}

func lsetclear(s *Screen) {
	for i := s.Rearmost; i != nil; i = i.Layer.Front {
		l := i.Layer
		l.Clear = l.Screenr.In(l.Screen.Image.Clipr)
		if l.Clear {
			for j := l.Front; j != nil; j = j.Layer.Front {
				if RectXRect(l.Screenr, j.Layer.Screenr) {
					l.Clear = false
					break
				}
			}
		}
	}
}

// ltofront pulls i towards the top of the screen, just behind front.
func ltofront(i, front *Image, fill bool) {
	l := i.Layer
	s := l.Screen
	for l.Front != front {
		f := l.Front
		x := l.Screenr
		overlap := RectClip(&x, f.Layer.Screenr)
		if overlap {
			Lhide(f, x)
			f.Layer.Clear = false
		}
		// Swap l and f in the screen's list.
		ff := f.Layer.Front
		rr := l.Rear
		if ff == nil {
			s.Frontmost = i
		} else {
			ff.Layer.Rear = i
		}
		if rr == nil {
			s.Rearmost = f
		} else {
			rr.Layer.Front = f
		}
		l.Front = ff
		l.Rear = f
		f.Layer.Front = i
		f.Layer.Rear = rr
		if overlap && fill {
			Lexpose(i, x)
		}
	}
}

// Ltofront brings i to the front (memltofront).
func Ltofront(i *Image) {
	ltofront(i, nil, true)
	lsetclear(i.Layer.Screen)
}

// LtofrontN brings the windows to the front, the first frontmost.
func LtofrontN(ip []*Image) {
	if len(ip) == 0 {
		return
	}
	var front *Image
	for _, i := range ip {
		ltofront(i, front, true)
		front = i
	}
	lsetclear(front.Layer.Screen)
}

func ltorear(i, rear *Image) {
	l := i.Layer
	s := l.Screen
	for l.Rear != rear {
		r := l.Rear
		x := l.Screenr
		overlap := RectClip(&x, r.Layer.Screenr)
		if overlap {
			Lhide(i, x)
			l.Clear = false
		}
		rr := r.Layer.Rear
		f := l.Front
		if rr == nil {
			s.Rearmost = i
		} else {
			rr.Layer.Front = i
		}
		if f == nil {
			s.Frontmost = r
		} else {
			f.Layer.Rear = r
		}
		l.Rear = rr
		l.Front = r
		r.Layer.Rear = i
		r.Layer.Front = f
		if overlap {
			Lexpose(r, x)
		}
	}
}

// Ltorear sends i to the back (memltorear).
func Ltorear(i *Image) {
	ltorear(i, nil)
	lsetclear(i.Layer.Screen)
}

// LtorearN sends the windows to the back, the first rearmost.
func LtorearN(ip []*Image) {
	if len(ip) == 0 {
		return
	}
	var rear *Image
	for _, i := range ip {
		ltorear(i, rear)
		rear = i
	}
	lsetclear(rear.Layer.Screen)
}

// Lsetrefresh changes how window i is refreshed (memlsetrefresh).
func Lsetrefresh(i *Image, fn Refreshfn, ptr any) error {
	l := i.Layer
	if l.Refreshfn != nil && fn != nil { // just change functions
		l.Refreshfn = fn
		l.Refreshptr = ptr
		return nil
	}
	if l.Refreshfn == nil { // is using backup image; just free it
		l.Save = nil
		l.Refreshfn = fn
		l.Refreshptr = ptr
		return nil
	}
	save, err := Alloc(i.R, i.Chan)
	if err != nil {
		return err
	}
	l.Save = save
	// Easiest way is just to update the entire save area.
	l.Refreshfn(i, i.R, l.Refreshptr)
	l.Refreshfn = nil
	l.Refreshptr = nil
	return nil
}

// Lorigin moves window i so that i.R.Min is log and its screen position
// is scr (memlorigin). It returns 1 if the window moved on the screen.
func Lorigin(i *Image, log, scr Point) (int, error) {
	l := i.Layer
	s := l.Screen
	oldr := l.Screenr
	newr := Rect(scr.X, scr.Y, scr.X+oldr.Dx(), scr.Y+oldr.Dy())
	eqscr := scr == oldr.Min
	eqlog := log == i.R.Min
	if eqscr && eqlog {
		return 0, nil
	}
	var nsave *Image
	if !eqlog && l.Save != nil {
		var err error
		nsave, err = Alloc(Rect(log.X, log.Y, log.X+oldr.Dx(), log.Y+oldr.Dy()), i.Chan)
		if err != nil {
			return -1, err
		}
	}
	// Bring it to the front and move the logical coordinate system.
	Ltofront(i)
	wasclear := l.Clear
	if nsave != nil {
		if !wasclear {
			ImageDraw(nsave, nsave.R, l.Save, l.Save.R.Min, nil, Pt(0, 0), S)
		}
		l.Save = nsave
	}
	delta := log.Sub(i.R.Min)
	i.R = i.R.Add(delta)
	i.Clipr = i.Clipr.Add(delta)
	l.Delta = l.Screenr.Min.Sub(i.R.Min)
	if eqscr {
		return 0, nil
	}
	// Make a shadow window at the old position, push it behind this one,
	// and later delete it, to restore what was behind.
	shad, err := Lalloc(s, oldr, NoRefresh, nil, DNofill)
	if err != nil {
		return -1, err
	}
	s.Frontmost = i
	if s.Rearmost == i {
		s.Rearmost = shad
	} else {
		l.Rear.Layer.Front = shad
	}
	shad.Layer.Front = i
	shad.Layer.Rear = l.Rear
	l.Rear = shad
	l.Front = nil
	shad.Layer.Clear = false

	// Move the window and hide things obscured by the new position.
	for t := l.Rear.Layer.Rear; t != nil; t = t.Layer.Rear {
		x := newr
		if RectClip(&x, t.Layer.Screenr) {
			Lhide(t, x)
			t.Layer.Clear = false
		}
	}
	l.Screenr = newr
	l.Delta = scr.Sub(i.R.Min)
	l.Clear = newr.In(l.Screen.Image.Clipr)

	// Copy to the new position and delete the shadow.
	if wasclear {
		Draw(s.Image, newr, s.Image, oldr.Min, nil, Pt(0, 0), S)
	} else {
		Lexpose(i, newr)
	}
	Ldelete(shad)
	return 1, nil
}

// Load copies data into r of i, which may be a window (memload).
func Load(dst *Image, r Rectangle, data []byte, compressed bool) (int, error) {
	loadfn := LoadImage
	if compressed {
		loadfn = CLoadImage
	}
	for {
		dl := dst.Layer
		if dl == nil {
			return loadfn(dst, r, data)
		}
		lr := r
		r = r.Add(dl.Delta)
		dx := dl.Delta.X & (7 / dst.Depth)
		if dl.Clear && dx == 0 {
			dst = dl.Screen.Image
			continue
		}
		// dst is an obscured layer or the data is unaligned.
		if dl.Save != nil && dx == 0 {
			n, err := loadfn(dl.Save, lr, data)
			if err == nil && n > 0 {
				Lexpose(dst, r)
			}
			return n, err
		}
		tmp, err := Alloc(lr, dst.Chan)
		if err != nil {
			return 0, err
		}
		n, err := loadfn(tmp, lr, data)
		Draw(dst, lr, tmp, lr.Min, nil, lr.Min, S)
		return n, err
	}
}

// Unload copies r of i, which may be a window, into data (memunload).
func Unload(src *Image, r Rectangle, data []byte) (int, error) {
	for {
		dl := src.Layer
		if dl == nil {
			return UnloadImage(src, r, data)
		}
		lr := r
		r = r.Add(dl.Delta)
		dx := dl.Delta.X & (7 / src.Depth)
		if dl.Clear && dx == 0 {
			src = dl.Screen.Image
			continue
		}
		if dl.Save != nil && dx == 0 {
			if dl.Refreshfn != nil {
				return 0, ErrLoad // can't unload window if it's not Refbackup
			}
			if len(data) > 0 {
				Lhide(src, r)
			}
			return UnloadImage(dl.Save, lr, data)
		}
		tmp, err := Alloc(lr, src.Chan)
		if err != nil {
			return 0, err
		}
		Draw(tmp, lr, src, lr.Min, nil, lr.Min, S)
		return UnloadImage(tmp, lr, data)
	}
}
