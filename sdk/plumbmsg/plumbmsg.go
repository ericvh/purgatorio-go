// Package plumbmsg is Inferno's Plumbmsg module (plumbmsg(2)): the
// message format the plumber routes and the calls a client uses to send
// messages to /chan/plumb.input and receive them on its port,
// /chan/plumb.<port>.
//
// A packed message is six newline-terminated header lines (source,
// destination, working directory, kind, attributes and the length of the
// data in bytes) followed by the data.
package plumbmsg

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Msg is a plumbing message.
type Msg struct {
	Src  string
	Dst  string
	Dir  string
	Kind string
	Attr string
	Data []byte
}

// Attr is one name=value attribute.
type Attr struct {
	Name string
	Val  string
}

var (
	input       = -1
	port        = -1
	portname    string
	maxdatasize int
)

// Init connects to the plumber. A server (doinput false and no rcvport)
// connects to nothing. With doinput the client may Send; with rcvport it
// also opens that port to Recv and tells the plumber it has started.
func Init(doinput bool, rcvport string, maxdata int) error {
	if !doinput && rcvport == "" {
		return nil
	}
	fd, err := sys.Open("/chan/plumb.input", sys.OWRITE)
	if err != nil {
		return err
	}
	input = fd
	if rcvport == "" {
		return nil
	}
	pfd, err := sys.Open("/chan/plumb."+rcvport, sys.OREAD)
	if err != nil {
		closefd(&input)
		return err
	}
	port = pfd
	maxdatasize = maxdata
	portname = rcvport
	m := &Msg{Src: portname, Dst: "plumb", Kind: "text", Data: []byte("start")}
	if err := m.Send(); err != nil {
		closefd(&port)
		closefd(&input)
		return err
	}
	return nil
}

func closefd(fd *int) {
	if *fd >= 0 {
		_ = sys.Close(*fd)
		*fd = -1
	}
}

// Shutdown tells the plumber the client's port has stopped.
func Shutdown() {
	m := &Msg{Src: portname, Dst: "plumb", Kind: "text", Data: []byte("stop")}
	_ = m.Send()
}

// Send writes m to the plumber's input in one write. The error is the
// plumber's reply, such as "no matching plumb rule".
func (m *Msg) Send() error {
	b := m.Pack()
	if _, err := syscall.Write(input, b); err != nil {
		if s := sys.Errstr(); s != "" {
			return sys.Error(s)
		}
		return err
	}
	return nil
}

// Recv reads the next message from the port, or returns nil.
func Recv() *Msg {
	b := make([]byte, maxdatasize+1000)
	n, err := syscall.Read(port, b)
	if err != nil || n <= 0 {
		return nil
	}
	return Unpack(b[:n])
}

// Pack returns m in the wire format.
func (m *Msg) Pack() []byte {
	hdr := m.Src + "\n" + m.Dst + "\n" + m.Dir + "\n" + m.Kind + "\n" + m.Attr + "\n" +
		strconv.Itoa(len(m.Data)) + "\n"
	b := make([]byte, 0, len(hdr)+len(m.Data))
	b = append(b, hdr...)
	return append(b, m.Data...)
}

// Unpack parses a packed message, or returns nil if b is malformed.
func Unpack(b []byte) *Msg {
	var hdr [6]string
	i := 0
	for n := range hdr {
		j := i
		for j < len(b) && b[j] != '\n' {
			j++
		}
		if j == len(b) {
			return nil
		}
		hdr[n] = string(b[i:j])
		i = j + 1
	}
	nb, _ := strconv.Atoi(hdr[5])
	if len(b)-i != nb {
		os.Stdout.WriteString("unpack: bad message format: wrong nbytes\n")
		return nil
	}
	return &Msg{Src: hdr[0], Dst: hdr[1], Dir: hdr[2], Kind: hdr[3], Attr: hdr[4],
		Data: append([]byte{}, b[i:]...)}
}

// String2attrs splits tab-separated name=value pairs. As in Inferno, the
// list comes back in reverse order.
func String2attrs(s string) []*Attr {
	var pairs []string
	for _, f := range strings.Split(s, "\t") {
		if f != "" {
			pairs = append(pairs, f)
		}
	}
	var attrs []*Attr
	for i := len(pairs) - 1; i >= 0; i-- {
		a := &Attr{}
		if name, val, ok := strings.Cut(pairs[i], "="); ok {
			a.Name, a.Val = name, val
		}
		attrs = append(attrs, a)
	}
	return attrs
}

// Attrs2string joins attributes as tab-separated name=value pairs.
func Attrs2string(l []*Attr) string {
	var b strings.Builder
	for i, a := range l {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(a.Name + "=" + a.Val)
	}
	return b.String()
}

// Lookup returns the value of the first attribute called name.
func Lookup(attrs []*Attr, name string) (string, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a.Val, true
		}
	}
	return "", false
}
