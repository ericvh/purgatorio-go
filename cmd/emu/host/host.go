// Package host is the hosted platform: Linux and macOS. It is the only
// place allowed to use build constraints or runtime.GOOS; POSIX-common code
// goes here, OS-specific code in files named *_linux.go and *_darwin.go.
package host

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/user"
	"sync"
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

// Host implements platform.Platform on a POSIX host. In and Out are the
// console; nil means the process's standard input and output. Screen is
// the display behind #i; nil means a headless screen of Width by Height
// (640 by 480 when zero).
type Host struct {
	In            io.Reader
	Out           io.Writer
	Screen        platform.Screen
	Width, Height int
	// Eia lists the host serial devices behind #t, eia0 first; nil
	// means EiaPorts().
	Eia []string

	screenOnce sync.Once
}

var (
	_ platform.Platform  = (*Host)(nil)
	_ platform.Displayer = (*Host)(nil)
)

// Display returns the screen, making the headless one on first use.
func (h *Host) Display() platform.Screen {
	h.screenOnce.Do(func() {
		if h.Screen == nil {
			w, ht := h.Width, h.Height
			if w <= 0 || ht <= 0 {
				w, ht = 640, 480
			}
			h.Screen = platform.NewHeadless(w, ht, "x8r8g8b8")
		}
	})
	return h.Screen
}

func (*Host) Now() int64 { return time.Now().UnixNano() }

func (*Host) Entropy(b []byte) { _, _ = rand.Read(b) }

func (h *Host) Console() platform.Console {
	c := console{in: h.In, out: h.Out}
	if c.in == nil {
		c.in = os.Stdin
	}
	if c.out == nil {
		c.out = os.Stdout
	}
	return c
}

func (*Host) User() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "inferno"
}

func (*Host) Sysname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "purgatorio"
}

type console struct {
	in  io.Reader
	out io.Writer
}

func (c console) Read(b []byte) (int, error)  { return c.in.Read(b) }
func (c console) Write(b []byte) (int, error) { return c.out.Write(b) }
func (console) SetRaw(bool) error             { return errors.New("raw mode not implemented") }
