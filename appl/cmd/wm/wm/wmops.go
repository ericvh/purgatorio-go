package main

import (
	"fmt"
	"strconv"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/quote"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func (w *wm) newclient(fid uint32) *client {
	c := &client{w: w, id: w.nextid, fid: fid, cursor: "cursor"}
	w.nextid++
	w.clients = append(w.clients, c)
	c.putctl("rect " + r2s(w.d.Image.R))
	if w.allow {
		w.control = c
		c.flags |= controller
		w.allow = false
	} else {
		w.controlevent("newclient " + strconv.Itoa(c.id))
	}
	return c
}

// delclient removes a client that has gone, and its windows.
func (w *wm) delclient(c *client) {
	for _, win := range c.wins {
		if win.img != nil {
			_ = win.img.Free()
		}
	}
	c.wins = nil
	c.dead = true
	w.unlink(c)
	if c == w.ptrfocus {
		w.ptrfocus = nil
	}
	if c == w.control {
		w.control = nil
	}
	w.controlevent("delclient " + strconv.Itoa(c.id))
	if c == w.kbdfocus {
		w.kbdfocus = nil
		for _, z := range w.clients {
			if z.flags&kbdStarted != 0 && len(z.wins) > 0 {
				w.setfocus(z)
				break
			}
		}
	}
	_ = w.d.Flush(true)
}

// request carries out a client's request (wm.b's handlerequest) and
// returns an error string, empty on success.
func (w *wm) request(c *client, req string) string {
	args := quote.Unquoted(req)
	if len(args) == 0 {
		return "no request"
	}
	n := len(args)
	if req[0] == '!' && n < 3 {
		return "bad arg count"
	}
	switch args[0] {
	case "key":
		if n != 2 {
			return "bad arg count"
		}
		k, err := strconv.Atoi(args[1])
		if err != nil {
			return "bad key"
		}
		w.key(rune(k))
	case "ptr":
		if n != 3 {
			return "bad arg count"
		}
		if w.ptrfocus != c {
			return "cannot move pointer"
		}
		x, _ := strconv.Atoi(args[1])
		y, _ := strconv.Atoi(args[2])
		if fd, err := sys.Open("/dev/pointer", sys.OWRITE); err == nil {
			_, _ = syscall.Write(fd, fmt.Appendf(nil, "m%d %d", x, y))
			syscall.Close(fd)
		}
	case "cursor":
		if n != 6 && n != 1 {
			return "bad arg count"
		}
		c.cursor = req
	case "start":
		if n != 2 {
			return "bad arg count"
		}
		switch args[1] {
		case "mouse", "ptr":
			c.flags |= ptrStarted
		case "kbd":
			c.flags |= kbdStarted
			w.setfocus(c)
		case "control":
			if c.flags&controller == 0 {
				return "control not available"
			}
			c.flags |= controlStarted
		default:
			return "unknown input source"
		}
	case "!reshape":
		// !reshape tag reqid minx miny maxx maxy [how]
		if n < 7 {
			return "bad arg count"
		}
		var v [4]int
		for i := range v {
			x, err := strconv.Atoi(args[3+i])
			if err != nil {
				return "bad rectangle"
			}
			v[i] = x
		}
		r := draw.Rect(v[0], v[1], v[2], v[3])
		scr := w.d.Image.R
		if n > 7 {
			switch args[7] {
			case "onscreen":
				r = fitrect(r, scr)
			case "place":
				r = w.newrect(fitrect(r, scr), scr)
			case "exact":
			case "max":
				r = scr
			default:
				return "unkown placement method"
			}
		}
		return w.reshape(c, args[1], r)
	case "delete":
		if n < 2 {
			return "tag required"
		}
		for i, win := range c.wins {
			if win.tag == args[1] {
				if win.img != nil {
					_ = win.img.Free()
				}
				c.wins = append(c.wins[:i], c.wins[i+1:]...)
				break
			}
		}
		if len(c.wins) == 0 && c == w.kbdfocus {
			w.setfocus(nil)
		}
	case "raise":
		w.top(c)
	case "lower":
		w.bottom(c)
	case "!move", "!size":
		// !move tag reqid startx starty; !size tag reqid mindx mindy
		win := c.window(args[1])
		if win == nil {
			return "no such tag"
		}
		if n != 5 {
			return "bad arg count"
		}
		x, _ := strconv.Atoi(args[3])
		y, _ := strconv.Atoi(args[4])
		if args[0] == "!move" {
			return w.dragwin(c, win, draw.Pt(x, y).Sub(win.r.Min))
		}
		return w.sizewin(c, win, draw.Pt(x, y))
	case "fixedorigin":
		c.flags |= fixedOrigin
	case "rect":
	case "kbdfocus":
		if n != 2 {
			return "bad arg count"
		}
		if args[1] != "0" {
			w.setfocus(c)
		} else if c == w.kbdfocus {
			w.setfocus(nil)
		}
	case "exit":
		w.exit()
	case "request":
		if c.flags&controller == 0 {
			return "you are not in control"
		}
	case "ctl":
		if c.flags&controlStarted == 0 {
			return "invalid request"
		}
		if n < 3 {
			return "bad arg count"
		}
		id, _ := strconv.Atoi(args[1])
		for _, z := range w.clients {
			if z.id == id {
				msg := ""
				for i, a := range args[2:] {
					if i > 0 {
						msg += " "
					}
					msg += quote.Quote(a)
				}
				z.putctl(msg)
				return ""
			}
		}
		return "no such client"
	case "endcontrol":
		if c != w.control {
			return "invalid request"
		}
		w.control = nil
		w.allow = true
		c.flags &^= controlStarted | controller
	default:
		if c == w.control || w.control == nil || w.control.flags&controlStarted == 0 {
			return "unknown control request"
		}
		w.control.putctl("request " + strconv.Itoa(c.id) + " " + req)
	}
	return ""
}

// reshape gives window tag of client c the rectangle r: a window of
// the same size is moved; otherwise it gets a new image.
func (w *wm) reshape(c *client, tag string, r draw.Rectangle) string {
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return "bad rectangle"
	}
	win := c.window(tag)
	if win != nil && win.img != nil && win.r.Size() == r.Size() {
		if err := win.img.Origin(r.Min, r.Min); err != nil {
			return fmt.Sprintf("window move failed: %v", err)
		}
		win.r = r
	} else {
		img, err := w.screen.NewWindow(r, draw.Refbackup, draw.White)
		if err != nil {
			return fmt.Sprintf("window creation failed: %v", err)
		}
		w.imgseq++
		name := fmt.Sprintf("wm.%d.%d", c.id, w.imgseq)
		if err := img.Name(name, true); err != nil {
			_ = img.Free()
			return fmt.Sprintf("cannot name window: %v", err)
		}
		if win == nil {
			win = &window{tag: tag}
			c.wins = append(c.wins, win)
		} else if win.img != nil {
			_ = win.img.Free()
		}
		win.img, win.r, win.name = img, r, name
	}
	c.iname = win.name
	w.top(c)
	_ = w.d.Flush(true)
	return ""
}

// dragwin moves a window with the pointer while a button is held.
func (w *wm) dragwin(c *client, win *window, off draw.Point) string {
	if w.buttons == 0 {
		return "too late"
	}
	scr := w.d.Image.R
	const margin = 10
	p := w.last
	org := win.r.Min
	for p.Buttons != 0 {
		var ok bool
		if p, ok = w.readPointer(); !ok {
			break
		}
		org = p.XY.Sub(off)
		if org.Y < scr.Min.Y {
			org.Y = scr.Min.Y
		} else if org.Y > scr.Max.Y-margin {
			org.Y = scr.Max.Y - margin
		}
		if org.X < scr.Min.X && org.X+win.r.Dx() < scr.Min.X+margin {
			org.X = scr.Min.X + margin - win.r.Dx()
		} else if org.X > scr.Max.X-margin {
			org.X = scr.Max.X - margin
		}
		_ = win.img.Origin(win.img.R.Min, org)
		_ = w.d.Flush(true)
	}
	w.buttons = 0
	r := draw.Rectangle{Min: org, Max: org.Add(win.r.Size())}
	// Make the window's coordinates the screen's again.
	_ = win.img.Origin(r.Min, r.Min)
	win.r = r
	c.iname = win.name
	c.putptr(p)
	_ = w.d.Flush(true)
	return ""
}

const bdwidth = 3

// sizewin sweeps out a new rectangle for a window with the pointer.
func (w *wm) sizewin(c *client, win *window, minsize draw.Point) string {
	borders := make([]*draw.Image, 4)
	defer func() {
		for _, b := range borders {
			if b != nil {
				_ = b.Free()
			}
		}
	}()
	w.showborders(borders, win.r)
	_ = w.d.Flush(true)
	p := w.last
	for p.Buttons == 0 {
		var ok bool
		if p, ok = w.readPointer(); !ok {
			return "pointer failed"
		}
	}
	xy := p.XY
	r := win.r
	var move int
	var offset draw.Point
	const minx, miny, maxx, maxy = 1, 2, 4, 8
	if !xy.In(r) {
		r = draw.Rectangle{Min: xy, Max: xy}
		move = maxx | maxy
	} else {
		if xy.X < (r.Min.X+r.Max.X)/2 {
			move, offset.X = minx, xy.X-r.Min.X
		} else {
			move, offset.X = maxx, xy.X-r.Max.X
		}
		if xy.Y < (r.Min.Y+r.Max.Y)/2 {
			move, offset.Y = move|miny, xy.Y-r.Min.Y
		} else {
			move, offset.Y = move|maxy, xy.Y-r.Max.Y
		}
	}
	for p.Buttons != 0 {
		var ok bool
		if p, ok = w.readPointer(); !ok {
			break
		}
		q := p.XY.Sub(offset)
		if move&minx != 0 {
			r.Min.X = q.X
		}
		if move&miny != 0 {
			r.Min.Y = q.Y
		}
		if move&maxx != 0 {
			r.Max.X = q.X
		}
		if move&maxy != 0 {
			r.Max.Y = q.Y
		}
		w.showborders(borders, canon(r))
		_ = w.d.Flush(true)
	}
	w.buttons = 0
	r = canon(r)
	if r.Dx() < minsize.X {
		if move&maxx != 0 {
			r.Max.X = r.Min.X + minsize.X
		} else {
			r.Min.X = r.Max.X - minsize.X
		}
	}
	if r.Dy() < minsize.Y {
		if move&maxy != 0 {
			r.Max.Y = r.Min.Y + minsize.Y
		} else {
			r.Min.Y = r.Max.Y - minsize.Y
		}
	}
	c.putptr(p)
	return w.reshape(c, win.tag, r)
}

func canon(r draw.Rectangle) draw.Rectangle {
	if r.Min.X > r.Max.X {
		r.Min.X, r.Max.X = r.Max.X, r.Min.X
	}
	if r.Min.Y > r.Max.Y {
		r.Min.Y, r.Max.Y = r.Max.Y, r.Min.Y
	}
	return r
}

// showborders shows r's edges as red windows.
func (w *wm) showborders(b []*draw.Image, r draw.Rectangle) {
	rs := []draw.Rectangle{
		draw.Rect(r.Min.X, r.Min.Y, r.Min.X+bdwidth, r.Max.Y),
		draw.Rect(r.Min.X+bdwidth, r.Min.Y, r.Max.X-bdwidth, r.Min.Y+bdwidth),
		draw.Rect(r.Max.X-bdwidth, r.Min.Y, r.Max.X, r.Max.Y),
		draw.Rect(r.Min.X+bdwidth, r.Max.Y-bdwidth, r.Max.X-bdwidth, r.Max.Y),
	}
	for i, br := range rs {
		if b[i] != nil {
			_ = b[i].Free()
			b[i] = nil
		}
		if br.Dx() > 0 && br.Dy() > 0 {
			b[i], _ = w.screen.NewWindow(br, draw.Refbackup, draw.Red)
		}
	}
}

// fitrect fits a window rectangle w on screen r, keeping its place if
// it can (wm.b).
func fitrect(w, r draw.Rectangle) draw.Rectangle {
	if w.Dx() > r.Dx() {
		w.Max.X = w.Min.X + r.Dx()
	}
	if w.Dy() > r.Dy() {
		w.Max.Y = w.Min.Y + r.Dy()
	}
	sz := w.Size()
	if w.Max.X > r.Max.X {
		w.Min.X, w.Max.X = r.Max.X-sz.X, r.Max.X
	}
	if w.Max.Y > r.Max.Y {
		w.Min.Y, w.Max.Y = r.Max.Y-sz.Y, r.Max.Y
	}
	if w.Min.X < r.Min.X {
		w.Min.X, w.Max.X = r.Min.X, r.Min.X+sz.X
	}
	if w.Min.Y < r.Min.Y {
		w.Min.Y, w.Max.Y = r.Min.Y, r.Min.Y+sz.Y
	}
	return w
}

// newrect finds a place for a new window of w's size: down and to the
// right of the last one, back at the top left when it would fall off
// the screen.
func (w *wm) newrect(r, scr draw.Rectangle) draw.Rectangle {
	sz := r.Size()
	p := scr.Min.Add(draw.Pt(8, 8))
	if w.lastrect.Dx() > 0 {
		p = w.lastrect.Min.Add(draw.Pt(24, 24))
	}
	if p.X+sz.X > scr.Max.X || p.Y+sz.Y > scr.Max.Y {
		p = scr.Min.Add(draw.Pt(8, 8))
	}
	w.lastrect = fitrect(draw.Rectangle{Min: p, Max: p.Add(sz)}, scr)
	return w.lastrect
}
