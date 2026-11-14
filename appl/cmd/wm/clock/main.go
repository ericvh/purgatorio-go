// Command clock shows the time in a window (Inferno wm/clock): a face
// with twelve dots and hour and minute hands, drawn with sdk/draw on a
// window from sdk/wmclient.
package main

import (
	"fmt"
	"math"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

var back, hrhand, minhand, dots *draw.Image

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "clock: %v\n", err)
	sys.Exits("fail:" + err.Error())
}

func main() {
	d, err := draw.Init("")
	if err != nil {
		fatal(err)
	}
	if _, err := d.GetDefaultFont(); err != nil {
		fatal(err)
	}
	w, err := wmclient.New(d, "clock", wmclient.Appl)
	if err != nil {
		fatal(err)
	}
	back, _ = d.Color(draw.Palebluegreen)
	hrhand, _ = d.Color(draw.Darkblue)
	minhand, _ = d.Color(draw.Paleblue)
	dots, _ = d.Color(draw.Blue)
	if err := w.Reshape(draw.Rect(0, 0, 100, 100)); err != nil {
		fatal(err)
	}
	if err := w.StartInput("ptr"); err != nil {
		fatal(err)
	}
	if err := w.Onscreen(""); err != nil {
		fatal(err)
	}
	now := daytime.Now()
	drawclock(w, now)
	for {
		e, err := w.Next(nil, 1000)
		if err != nil {
			fatal(err)
		}
		switch e.Kind {
		case wmclient.Reshape:
			drawclock(w, now)
		case wmclient.Timeout:
			if t := daytime.Now(); t/60 != now/60 {
				now = t
				drawclock(w, now)
			}
		}
	}
}

func drawclock(w *wmclient.Window, t int) {
	if w.Image == nil {
		return
	}
	tm := daytime.Local(t)
	anghr := 90 - (tm.Hour*5+tm.Min/10)*6
	angmin := 90 - tm.Min*6
	scr := w.Image
	r := w.ImageR(w.R)
	c := r.Min.Add(r.Max).Div(2)
	rad := min(r.Dx(), r.Dy())/2 - 8
	scr.Draw(r, back, nil, draw.ZP)
	for i := 0; i < 12; i++ {
		scr.FillEllipse(circlept(c, rad, i*(360/12)), 2, 2, dots, draw.ZP)
	}
	scr.Line(c, circlept(c, rad*3/4, angmin), 0, 0, 1, minhand, draw.ZP)
	scr.Line(c, circlept(c, rad/2, anghr), 0, 0, 1, hrhand, draw.ZP)
	_ = w.Display.Flush(true)
}

func circlept(c draw.Point, r, degrees int) draw.Point {
	rad := float64(degrees) * math.Pi / 180
	return draw.Pt(c.X+int(math.Cos(rad)*float64(r)), c.Y-int(math.Sin(rad)*float64(r)))
}
