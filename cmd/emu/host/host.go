// Package host is the hosted platform: Linux and macOS. It is the only
// place allowed to use build constraints or runtime.GOOS; POSIX-common code
// goes here, OS-specific code in files named *_linux.go and *_darwin.go.
package host

import (
	"crypto/rand"
	"errors"
	"os"
	"os/user"
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

// Host implements platform.Platform on a POSIX host.
type Host struct{}

var _ platform.Platform = Host{}

func (Host) Now() int64 { return time.Now().UnixNano() }

func (Host) Entropy(b []byte) { _, _ = rand.Read(b) }

func (Host) Console() platform.Console { return console{} }

func (Host) User() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "inferno"
}

func (Host) Sysname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "purgatorio"
}

type console struct{}

func (console) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (console) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (console) SetRaw(bool) error           { return errors.New("raw mode not implemented") }
