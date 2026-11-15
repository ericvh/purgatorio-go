// Command riodraw is a program that draws through the SDK, for the rio
// test: it fills its window with magenta, again whenever the window has
// a new image, writes each pointer press it gets to /tmp/riodraw.ptr,
// and exits on q. It knows nothing of rio: draw.Init and wmclient find
// the window.
package main

import (
	"fmt"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/draw"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/sdk/wmclient"
)

func check(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "riodraw: %v\n", err)
		sys.Exits("fail:riodraw")
	}
}

func main() {
	d, err := draw.Init("")
	check(err)
	w, err := wmclient.New(d, "riodraw", wmclient.Plain)
	check(err)
	check(w.StartInput("kbd", "ptr"))
	check(w.Reshape(d.Image.R))
	check(w.Onscreen("max"))
	col, err := d.Color(0xDD22DDFF)
	check(err)
	for {
		e, err := w.Next(nil, -1)
		check(err)
		switch e.Kind {
		case wmclient.Reshape:
			check(w.Image.Draw(w.Image.R, col, nil, draw.ZP))
			check(d.Flush(true))
		case wmclient.Ptr:
			if e.Ptr.Buttons != 0 {
				check(os.WriteFile("/tmp/riodraw.ptr",
					fmt.Appendf(nil, "%d %d %d\n", e.Ptr.XY.X, e.Ptr.XY.Y, e.Ptr.Buttons), 0o666))
			}
		case wmclient.Kbd:
			if e.Key == 'q' {
				wmclient.Exit()
			}
		}
	}
}
