// Command coffee is Inferno's wm/coffee, "a whimsical plaything": six
// coffee cups circle on an ellipse in a Tk panel, with a Stop (Go)
// button and scales for the rate, the jitter and how often they turn
// back.
//
// Inferno's draws pictures from /icons (coffee0.bit to coffee5.bit,
// their masks, and bigdelight.bit behind them). Those are Inferno's
// artwork and are not copied here: the cups are drawn with draw(2)'s
// ellipses and polygons and the background is a plain colour. Inferno
// animates in a thread of its own; this one draws a frame whenever
// waiting for input times out, about fifty times a second.
package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/tk"
	"github.com/ericvh/purgatorio-go/sdk/tkclient"
)

const (
	nc    = 6  // cups
	frame = 20 // ms between frames
)

// The colours: the background, the cups' and the coffee's.
const (
	fillcol   = 0x3B2414FF
	coffeecol = 0x6F4E37FF
	saucercol = 0xEEEEEEFF
)

var cupcols = [nc]uint32{0xFFFFFFFF, 0xC03030FF, 0x3050C0FF, 0x30A040FF, 0xE0C030FF, 0xA040C0FF}

var taskcfg = []string{
	"frame .f",
	"frame .b",
	"button .b.Stop -text Stop -command {send cmd stop}",
	"scale .b.Rate -from 1 -to 10 -orient horizontal" +
		" -showvalue 0 -command {send cmd rate}",
	"scale .b.Jitter -from 0 -to 5 -orient horizontal" +
		" -showvalue 0 -command {send cmd jitter}",
	"scale .b.Skip -from 0 -to 25 -orient horizontal" +
		" -showvalue 0 -command {send cmd skip}",
	".b.Rate set 3",
	".b.Jitter set 2",
	".b.Skip set 5",
	"pack .b.Stop .b.Rate .b.Jitter .b.Skip -side left",
	"pack .b -anchor w",
	"pack .f -side bottom -fill both -expand 1",
}

var top *tk.Toplevel

func fatal(s string) {
	fmt.Fprintf(os.Stderr, "coffee: %s\n", s)
	sys.Exits("fail:" + s)
}

func cmd(s string) string {
	r := tk.Cmd(top, s)
	if strings.HasPrefix(r, "!") {
		fmt.Printf("error executing '%s': %s\n", s, r[1:])
	}
	return r
}

func main() {
	sys.Pctl(sys.NEWPGRP, nil)
	d, err := draw.Init("")
	if err != nil {
		fatal(err.Error())
	}
	if _, err := d.GetDefaultFont(); err != nil {
		fatal(err.Error())
	}
	var wmctl chan string
	top, wmctl, err = tkclient.Toplevel(d, "", "Infernal Coffee", 0)
	if err != nil {
		fatal(err.Error())
	}
	cmdch := make(chan string, 16)
	tk.NameChan(top, cmdch, "cmd")
	for _, c := range taskcfg {
		cmd(c)
	}
	cmd("update")
	if err := tkclient.StartInput(top, "ptr", "kbd"); err != nil {
		fatal(err.Error())
	}
	if err := tkclient.Onscreen(top, ""); err != nil {
		fatal(err.Error())
	}

	a, err := newAnim(d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coffee: failed to allocate image\n")
		sys.Exits("fail:image")
	}
	next := sys.Millisec()
	for {
		wait := max(0, next-sys.Millisec())
		if a.stopped {
			wait = -1
		}
		if _, err := tkclient.Next(top, nil, wait); err != nil {
			fatal(err.Error())
		}
		for drained := false; !drained; {
			select {
			case s := <-wmctl:
				tkclient.Wmctl(top, s)
			case s := <-cmdch:
				a.control(strings.Fields(s))
			default:
				drained = true
			}
		}
		if !a.stopped && sys.Millisec() >= next {
			a.frame()
			next = sys.Millisec() + frame
		}
	}
}

// anim is the animation's state (coffee.b's animate).
type anim struct {
	buffer  *draw.Image
	fill    *draw.Image
	c, m    [nc]*draw.Image
	stopped bool
	rate    int
	jitter  int
	skip    int
	i, k    int
}

func newAnim(d *draw.Display) (*anim, error) {
	a := &anim{rate: 3, jitter: 2, skip: 5}
	var err error
	if a.fill, err = d.Color(fillcol); err != nil {
		return nil, err
	}
	for j := 0; j < nc; j++ {
		if a.c[j], a.m[j], err = cup(d, cupcols[j]); err != nil {
			return nil, err
		}
	}
	if a.buffer, err = d.AllocImage(draw.Rect(0, 0, 400, 300), top.Image.Chan, false, draw.Black); err != nil {
		return nil, err
	}
	cmd("panel .f.p -bd 3 -relief flat")
	cmd("pack .f.p -fill both -expand 1")
	cmd("update")
	tk.PutImage(top, ".f.p", a.buffer, nil)
	return a, nil
}

// cup draws a cup of colour col in an image and its mask.
func cup(d *draw.Display, col uint32) (*draw.Image, *draw.Image, error) {
	r := draw.Rect(0, 0, 48, 40)
	img, err := d.AllocImage(r, draw.RGB24, false, draw.Transparent)
	if err != nil {
		return nil, nil, err
	}
	mask, err := d.AllocImage(r, draw.GREY1, false, draw.Transparent)
	if err != nil {
		return nil, nil, err
	}
	c, err := d.Color(col)
	if err != nil {
		return nil, nil, err
	}
	saucer, err := d.Color(saucercol)
	if err != nil {
		return nil, nil, err
	}
	coffee, err := d.Color(coffeecol)
	if err != nil {
		return nil, nil, err
	}
	paint := func(dst, saucer, body, coffee *draw.Image) {
		dst.FillEllipse(draw.Pt(22, 34), 20, 5, saucer, draw.ZP)
		dst.Ellipse(draw.Pt(37, 18), 7, 7, 3, body, draw.ZP)
		dst.FillPoly([]draw.Point{{X: 6, Y: 10}, {X: 38, Y: 10}, {X: 33, Y: 33}, {X: 11, Y: 33}}, 0, body, draw.ZP)
		dst.FillEllipse(draw.Pt(22, 32), 11, 3, body, draw.ZP)
		dst.FillEllipse(draw.Pt(22, 10), 16, 4, body, draw.ZP)
		dst.FillEllipse(draw.Pt(22, 10), 13, 3, coffee, draw.ZP)
	}
	paint(img, saucer, c, coffee)
	paint(mask, d.Opaque, d.Opaque, d.Opaque)
	return img, mask, nil
}

// frame draws the next frame (the body of animate's loop).
func (a *anim) frame() {
	b := a.buffer
	a.k++
	if a.k%25 > 25-a.skip {
		a.i -= a.rate
	} else {
		a.i += a.rate
	}
	b.Draw(b.R, a.fill, nil, draw.ZP)
	center := b.R.Max.Div(2)
	for j := 0; j < nc; j++ {
		sin, cos := sincos(a.i + j*(360/nc))
		x := (sin*150)/1000 + a.jitter*(a.k%5)
		y := (cos*100)/1000 + a.jitter*(a.k%5)
		p0 := center.Add(draw.Pt(x-a.c[j].R.Dx()/2, y-a.c[j].R.Dy()/2))
		b.Draw(a.c[j].R.Add(p0), a.c[j], a.m[j], draw.ZP)
	}
	tk.Cmd(top, ".f.p dirty; update")
}

// control acts on the buttons and scales.
func (a *anim) control(w []string) {
	if len(w) == 0 {
		return
	}
	n := 0
	if len(w) > 1 {
		n, _ = strconv.Atoi(w[1])
	}
	switch w[0] {
	case "go":
		if a.stopped {
			cmd(".b.Stop configure -text Stop -command {send cmd stop}")
			cmd("update")
			a.stopped = false
		}
	case "stop":
		if !a.stopped {
			cmd(".b.Stop configure -text { Go } -command {send cmd go}")
			cmd("update")
			a.stopped = true
		}
	case "rate":
		a.rate = n
	case "jitter":
		a.jitter = n
	case "skip":
		a.skip = n
	}
}

// sincos returns the sine and cosine of a degrees, times 1000.
func sincos(a int) (int, int) {
	r := float64(a) * math.Pi / 180
	return int(math.Round(1000 * math.Sin(r))), int(math.Round(1000 * math.Cos(r)))
}
