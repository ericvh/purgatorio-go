package main

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/plumbmsg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// plumbsize is the most data a plumbing message to acme carries
// (dat.m's PLUMBSIZE).
const plumbsize = 1024

// Acme reads the plumber's edit port, as Inferno's acme does in its
// plumbproc. A port is a file2chan file, which cannot say when a read
// would not block, so a second copy of acme (acme -P) reads it and
// copies each message into a pipe that the main loop waits on.
var plumb struct {
	fd  int // the pipe's reading end, or -1
	pid int
	buf []byte
}

func init() { plumb.fd = -1 }

// startplumb starts the reader of the edit port. Without a plumber
// acme carries on without it, as Inferno's does.
func startplumb() {
	if _, err := os.Stat("/chan/plumb.edit"); err != nil {
		return
	}
	p, err := sys.Pipe()
	if err != nil {
		return
	}
	pid, err := sys.Spawn("/bin/acme.wasm", []string{"acme", "-P"}, sys.NEWFD,
		[][2]int{{1, p[1]}, {2, 2}})
	syscall.Close(p[1])
	if err != nil {
		syscall.Close(p[0])
		return
	}
	plumb.fd, plumb.pid = p[0], pid
}

// plumbproc is acme -P: it reads messages from the edit port and
// writes them, packed, to fd 1.
func plumbproc() {
	if err := plumbmsg.Init(true, "edit", plumbsize); err != nil {
		return
	}
	for {
		m := plumbmsg.Recv()
		if m == nil {
			fmt.Printf("Acme: can't read /chan/plumb.edit: %s\n", sys.Errstr())
			return
		}
		if m.Kind != "text" {
			fmt.Printf("Acme: can't interpret '%s' kind of message\n", m.Kind)
			continue
		}
		if _, err := syscall.Write(1, m.Pack()); err != nil {
			return
		}
	}
}

// stopplumb kills the reader and tells the plumber the port has
// stopped (plumbmsg->shutdown).
func stopplumb() {
	if plumb.fd < 0 {
		return
	}
	if fd, err := sys.Open("/prog/"+strconv.Itoa(plumb.pid)+"/ctl", sys.OWRITE); err == nil {
		_, _ = syscall.Write(fd, []byte("kill"))
		syscall.Close(fd)
	}
	if plumbmsg.Init(true, "", 0) == nil {
		m := &plumbmsg.Msg{Src: "edit", Dst: "plumb", Kind: "text", Data: []byte("stop")}
		_ = m.Send()
	}
	syscall.Close(plumb.fd)
	plumb.fd = -1
}

// plumbinput reads what the reader copied and acts on each whole
// message.
func plumbinput() {
	b := make([]byte, 8192)
	n, err := syscall.Read(plumb.fd, b)
	if err != nil || n <= 0 {
		syscall.Close(plumb.fd)
		plumb.fd = -1
		return
	}
	plumb.buf = append(plumb.buf, b[:n]...)
	for {
		k := msglen(plumb.buf)
		if k < 0 {
			return
		}
		m := plumbmsg.Unpack(plumb.buf[:k])
		plumb.buf = plumb.buf[k:]
		if m == nil || m.Kind != "text" {
			continue
		}
		attrs := plumbmsg.String2attrs(m.Attr)
		act, found := plumbmsg.Lookup(attrs, "action")
		switch {
		case !found || act == "" || act == "showfile":
			plumblook(m)
		case act == "showdata":
			plumbshow(m)
		}
	}
}

// msglen returns the length of the packed message at the front of b,
// or -1 if b does not hold all of it yet.
func msglen(b []byte) int {
	off := 0
	var line string
	for i := 0; i < 6; i++ {
		j := strings.IndexByte(string(b[off:]), '\n')
		if j < 0 {
			return -1
		}
		line = string(b[off : off+j])
		off += j + 1
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 0 {
		// Not a message: drop what there is.
		return len(b)
	}
	if off+n > len(b) {
		return -1
	}
	return off + n
}

// plumblook opens the file a message names (look.b's plumblook). The
// address is the addr attribute or, as the plumbing rules here give
// it, a :address after the name.
func plumblook(m *plumbmsg.Msg) {
	if len(m.Data) > plumbsize {
		warning(nil, fmt.Sprintf("plumb message too long : %s\n", m.Data))
		return
	}
	if len(m.Data) == 0 {
		return
	}
	name := string(m.Data)
	addr, found := plumbmsg.Lookup(plumbmsg.String2attrs(m.Attr), "addr")
	if !found || addr == "" {
		addr = ""
		if i := strings.LastIndexByte(name, ':'); i > 0 {
			name, addr = name[:i], name[i+1:]
		}
	}
	if name[0] != '/' && m.Dir != "" {
		name = m.Dir + "/" + name
	}
	name = path.Clean(name)
	w := openfile(g.row.col[len(g.row.col)-1], name, addr)
	if addr == "" {
		w.body.show(w.body.q0, w.body.q1, true)
	}
}

var nuntitled int

// plumbshow puts a message's data in a new window (look.b's plumbshow).
func plumbshow(m *plumbmsg.Msg) {
	w := g.row.col[len(g.row.col)-1].add(nil, nil, -1)
	name, found := plumbmsg.Lookup(plumbmsg.String2attrs(m.Attr), "filename")
	if !found || name == "" {
		nuntitled++
		name = "Untitled-" + strconv.Itoa(nuntitled)
	}
	if name[0] != '/' && m.Dir != "" {
		name = m.Dir + "/" + name
	}
	w.setname(path.Clean(name))
	w.body.insert(0, []rune(string(m.Data)), true)
	w.body.file.Clean()
	w.body.file.mod = false
	w.dirty = false
	w.settag()
	w.tag.setselect(w.tag.file.Nc(), w.tag.file.Nc())
}
