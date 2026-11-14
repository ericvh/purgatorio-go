package draw

// Screen is a stack of windows on an image.
type Screen struct {
	Display *Display
	ID      int
	Image   *Image
	Fill    *Image
}

var screenid int

// AllocScreen makes a screen on image, repainted with fill where no
// window covers it; a public screen can be used by other clients.
func (i *Image) AllocScreen(fill *Image, public bool) (*Screen, error) {
	d := i.Display
	b, err := d.bufimage(1 + 4 + 4 + 4 + 1)
	if err != nil {
		return nil, err
	}
	screenid++
	id := screenid + d.Dirno*1000 // screen ids are global to the device
	b[0] = 'A'
	putl(b[1:], id)
	putl(b[5:], i.ID)
	putl(b[9:], fill.ID)
	if public {
		b[13] = 1
	}
	if err := d.doflush(); err != nil {
		return nil, err
	}
	return &Screen{Display: d, ID: id, Image: i, Fill: fill}, nil
}

// Free releases the screen.
func (s *Screen) Free() error {
	b, err := s.Display.bufimage(1 + 4)
	if err != nil {
		return err
	}
	b[0] = 'F'
	putl(b[1:], s.ID)
	return s.Display.doflush()
}

// NewWindow makes a window on the screen at r, painted with color, kept
// up to date by the given Ref method.
func (s *Screen) NewWindow(r Rectangle, refresh int, color uint32) (*Image, error) {
	return s.Display.allocImage(s, r, s.Image.Chan, false, color, s.ID, refresh)
}

func (s *Screen) topbottom(w []*Image, top bool) error {
	b, err := s.Display.bufimage(1 + 1 + 2 + 4*len(w))
	if err != nil {
		return err
	}
	b[0] = 't'
	if top {
		b[1] = 1
	}
	puts(b[2:], len(w))
	for k, i := range w {
		putl(b[4+4*k:], i.ID)
	}
	return nil
}

// Top brings the windows to the front, the first frontmost.
func (s *Screen) Top(w ...*Image) error { return s.topbottom(w, true) }

// Bottom sends the windows to the back.
func (s *Screen) Bottom(w ...*Image) error { return s.topbottom(w, false) }

// Origin moves window i: log becomes its logical top left and scr its
// position on the screen.
func (i *Image) Origin(log, scr Point) error {
	b, err := i.Display.bufimage(1 + 4 + 2*4 + 2*4)
	if err != nil {
		return err
	}
	b[0] = 'o'
	putl(b[1:], i.ID)
	putp(b[5:], log)
	putp(b[13:], scr)
	if err := i.Display.doflush(); err != nil {
		return err
	}
	delta := log.Sub(i.R.Min)
	i.R = i.R.Add(delta)
	i.Clipr = i.Clipr.Add(delta)
	return nil
}

// Name publishes the image under name, or withdraws the name.
func (i *Image) Name(name string, publish bool) error {
	b, err := i.Display.bufimage(1 + 4 + 1 + 1 + len(name))
	if err != nil {
		return err
	}
	b[0] = 'N'
	putl(b[1:], i.ID)
	if publish {
		b[5] = 1
	}
	b[6] = byte(len(name))
	copy(b[7:], name)
	return i.Display.doflush()
}
