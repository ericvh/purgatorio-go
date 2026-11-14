// Package ssl pushes the ssl device (#D, ssl(3)) onto a connection, as
// Inferno's SSL module (appl/lib/ssl.b).
package ssl

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func errorf(format string, a ...any) error {
	return sys.Error(fmt.Sprintf(format, a...))
}

func open(name string, mode int) (*os.File, error) {
	fd, err := sys.Open(name, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readstring(f *os.File) (string, bool) {
	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil && n <= 0 {
		return "", false
	}
	return string(buf[:n]), true
}

func clone() (*dial.Connection, error) {
	if _, err := sys.Stat("#D"); err != nil {
		return nil, errorf("cannot access SSL device #D: %s", sys.Errstr())
	}
	c := &dial.Connection{Dir: "#D"}
	if _, err := sys.Stat("#D/ssl"); err == nil {
		c.Dir = "#D/ssl"
	}
	cf := c.Dir + "/clone"
	f, err := open(cf, sys.ORDWR)
	if err != nil {
		return nil, errorf("cannot open %s: %s", cf, sys.Errstr())
	}
	s, ok := readstring(f)
	if !ok {
		f.Close()
		return nil, errorf("cannot read %s: %s", cf, sys.Errstr())
	}
	c.Cfd = f
	c.Dir += "/" + s
	return c, nil
}

// Connect pushes ssl onto fd, in clear mode: messages are only
// delimited. Reads and writes go through the returned Dfd.
func Connect(fd *os.File) (*dial.Connection, error) {
	c, err := clone()
	if err != nil {
		return nil, err
	}
	c.Dfd, err = open(c.Dir+"/data", sys.ORDWR)
	if err != nil {
		c.Cfd.Close()
		return nil, errorf("cannot open data: %s", sys.Errstr())
	}
	if _, err := c.Cfd.Write([]byte(fmt.Sprintf("fd %d", fd.Fd()))); err != nil {
		c.Cfd.Close()
		c.Dfd.Close()
		return nil, errorf("cannot push fd: %s", sys.Errstr())
	}
	return c, nil
}

// Secret sets the secrets for each direction; a nil secret is left
// unchanged.
func Secret(c *dial.Connection, secretin, secretout []byte) error {
	for _, s := range []struct {
		name string
		v    []byte
	}{{"secretin", secretin}, {"secretout", secretout}} {
		if s.v == nil {
			continue
		}
		name := c.Dir + "/" + s.name
		f, err := open(name, sys.ORDWR)
		if err != nil {
			return errorf("cannot open %s: %s", name, sys.Errstr())
		}
		_, err = f.Write(s.v)
		f.Close()
		if err != nil {
			return errorf("cannot write %s: %s", name, sys.Errstr())
		}
	}
	return nil
}

// Ctl writes a control message, such as "alg rc4_128 sha1".
func Ctl(c *dial.Connection, msg string) error {
	if _, err := c.Cfd.Write([]byte(msg)); err != nil {
		return sys.Error(sys.Errstr())
	}
	return nil
}

// Algs lists the encryption and digest algorithms the device supports.
func Algs() ([]string, []string) {
	c, err := clone()
	if err != nil {
		return nil, nil
	}
	defer c.Cfd.Close()
	read := func(name string) []string {
		f, err := open(c.Dir+"/"+name, sys.OREAD)
		if err != nil {
			return nil
		}
		defer f.Close()
		s, _ := readstring(f)
		return strings.Fields(s)
	}
	return read("encalgs"), read("hashalgs")
}
