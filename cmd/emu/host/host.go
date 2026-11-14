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
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

// Host implements platform.Platform on a POSIX host. In and Out are the
// console; nil means the process's standard input and output.
type Host struct {
	In  io.Reader
	Out io.Writer
}

var _ platform.Platform = (*Host)(nil)

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
