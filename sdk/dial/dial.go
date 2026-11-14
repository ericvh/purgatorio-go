// Package dial makes and takes network connections through /net, as
// Inferno's Dial module (appl/lib/dial.b): pure file operations on the
// clone, ctl, data and listen files, with /net/cs translating addresses
// when it is there.
package dial

import (
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Connection is an open conversation: its data and ctl files and its
// directory (/net/tcp/N).
type Connection struct {
	Dfd *os.File
	Cfd *os.File
	Dir string
}

// Conninfo describes a connection's endpoints.
type Conninfo struct {
	Dir, Root, Spec          string
	Lsys, Lserv, Rsys, Rserv string
	Laddr, Raddr             string
}

const namemax = 256

func open(name string, mode int) (*os.File, error) {
	fd, err := sys.Open(name, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func fprint(f *os.File, s string) error {
	if _, err := f.Write([]byte(s)); err != nil {
		return sys.Error(sys.Errstr())
	}
	return nil
}

// Dial connects to addr, [/netdir/]proto!dest; local, if not empty, is
// the local address to use.
func Dial(addr, local string) (*Connection, error) {
	netdir, proto, rem := dialparse(addr)
	if netdir != "" {
		return csdial(netdir, proto, rem, local)
	}
	c, err := csdial("/net", proto, rem, local)
	if err == nil {
		return c, nil
	}
	if strings.Contains(err.Error(), "refused") {
		return nil, err
	}
	c, alterr := csdial("/net.alt", proto, rem, local)
	if alterr == nil {
		return c, nil
	}
	if strings.Contains(alterr.Error(), "translate") || strings.Contains(alterr.Error(), "does not exist") {
		sys.Werrstr(err.Error())
		return nil, err
	}
	return nil, alterr
}

func csdial(netdir, proto, rem, local string) (*Connection, error) {
	fd, err := open(netdir+"/cs", sys.ORDWR)
	if err != nil {
		// No connection server: don't translate.
		return call(netdir+"/"+proto+"/clone", rem, local)
	}
	defer fd.Close()
	if err := fprint(fd, proto+"!"+rem); err != nil {
		return nil, err
	}
	var besterr, lasterr error
	fd.Seek(0, 0)
	for {
		clonefile, a, err := csread(fd)
		if err != nil || clonefile == "" {
			if lasterr == nil {
				lasterr = err
			}
			break
		}
		c, err := call(redir(clonefile, netdir), a, local)
		if err == nil {
			return c, nil
		}
		lasterr = err
		if !strings.Contains(err.Error(), "does not exist") {
			besterr = err
		}
	}
	if besterr != nil {
		sys.Werrstr(besterr.Error())
		return nil, besterr
	}
	if lasterr == nil {
		lasterr = sys.Error("cs: no translation")
	}
	sys.Werrstr(lasterr.Error())
	return nil, lasterr
}

func call(clonefile, dest, local string) (*Connection, error) {
	cfd, convdir, err := clone(clonefile)
	if err != nil {
		return nil, err
	}
	msg := "connect " + dest
	if local != "" {
		msg += " " + local
	}
	if err := fprint(cfd, msg); err != nil {
		cfd.Close()
		return nil, err
	}
	dfd, err := open(convdir+"/data", sys.ORDWR)
	if err != nil {
		cfd.Close()
		return nil, err
	}
	return &Connection{Dfd: dfd, Cfd: cfd, Dir: convdir}, nil
}

func clone(clonefile string) (*os.File, string, error) {
	pdir := parent(clonefile)
	if pdir == "" {
		e := sys.Error("bad clone file name: " + clonefile)
		sys.Werrstr(e.Error())
		return nil, "", e
	}
	cfd, err := open(clonefile, sys.ORDWR)
	if err != nil {
		return nil, "", err
	}
	lno, err := readchan(cfd)
	if err != nil {
		cfd.Close()
		return nil, "", err
	}
	return cfd, pdir + "/" + lno, nil
}

func readchan(f *os.File) (string, error) {
	buf := make([]byte, namemax)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		if sys.Errstr() == "" {
			sys.Werrstr("empty clone file")
		}
		return "", sys.Error(sys.Errstr())
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	return strconv.Itoa(v), nil
}

// redir moves a clone file name cs gave into netdir: cs runs in another
// name space.
func redir(old, newdir string) string {
	if len(old) > 1 && old[0] == '/' {
		if p := strings.IndexByte(old[1:], '/'); p >= 0 {
			return newdir + "/" + old[1+p+1:]
		}
	}
	return newdir + "/" + old
}

// dialparse splits [[/netdir/]proto!]rem.
func dialparse(addr string) (netdir, proto, rem string) {
	p := strings.IndexByte(addr, '!')
	if p < 0 {
		return "", "net", addr
	}
	if addr[0] != '/' && addr[0] != '#' {
		return "", addr[:p], addr[p+1:]
	}
	p2 := strings.LastIndexByte(addr[:p], '/')
	if p2 <= 0 {
		return addr[:p], "net", addr[p+1:]
	}
	return addr[:p2], addr[p2+1 : p], addr[p+1:]
}

// Announce announces a service at addr.
func Announce(addr string) (*Connection, error) {
	naddr, clonefile, err := nettrans(addr)
	if err != nil {
		return nil, err
	}
	ctl, convdir, err := clone(clonefile)
	if err != nil {
		e := sys.Error("announce " + err.Error())
		sys.Werrstr(e.Error())
		return nil, e
	}
	if err := fprint(ctl, "announce "+naddr); err != nil {
		ctl.Close()
		e := sys.Error("announce writing " + clonefile + ": " + err.Error())
		sys.Werrstr(e.Error())
		return nil, e
	}
	return &Connection{Cfd: ctl, Dir: convdir}, nil
}

// Listen waits for a call on an announced connection.
func Listen(ac *Connection) (*Connection, error) {
	pdir := parent(ac.Dir)
	if pdir == "" {
		return nil, sys.Error("listen directory format: " + ac.Dir)
	}
	ctl, err := open(ac.Dir+"/listen", sys.ORDWR)
	if err != nil {
		e := sys.Error("listen opening " + ac.Dir + "/listen: " + err.Error())
		sys.Werrstr(e.Error())
		return nil, e
	}
	lno, err := readchan(ctl)
	if err != nil {
		ctl.Close()
		e := sys.Error("listen reading " + ac.Dir + "/listen: " + err.Error())
		sys.Werrstr(e.Error())
		return nil, e
	}
	return &Connection{Cfd: ctl, Dir: pdir + "/" + lno}, nil
}

// Accept accepts a call and opens its data file.
func Accept(c *Connection) (*os.File, error) {
	_ = fprint(c.Cfd, "accept "+lastname(c.Dir))
	return open(c.Dir+"/data", sys.ORDWR)
}

// Reject rejects a call with a reason.
func Reject(c *Connection, why string) error {
	return fprint(c.Cfd, "reject "+lastname(c.Dir)+" "+quote(why))
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func nettrans(addr string) (string, string, error) {
	netdir, proto, rem := dialparse(addr)
	if proto == "" || proto == "net" {
		e := sys.Error("bad dial string: " + addr)
		sys.Werrstr(e.Error())
		return "", "", e
	}
	if netdir == "" {
		netdir = "/net"
	}
	fd, err := open(netdir+"/cs", sys.ORDWR)
	if err != nil {
		if rem == "" {
			e := sys.Error("bad dial string: " + addr)
			sys.Werrstr(e.Error())
			return "", "", e
		}
		return rem, netdir + "/" + proto + "/clone", nil
	}
	defer fd.Close()
	if err := fprint(fd, proto+"!"+rem); err != nil {
		return "", "", err
	}
	fd.Seek(0, 0)
	clonefile, naddr, err := csread(fd)
	if err != nil {
		return "", "", err
	}
	return naddr, redir(clonefile, netdir), nil
}

func csread(f *os.File) (string, string, error) {
	buf := make([]byte, namemax)
	n, err := f.Read(buf)
	if n <= 0 {
		if err != nil {
			return "", "", sys.Error(sys.Errstr())
		}
		return "", "", nil
	}
	line := string(buf[:n])
	p := strings.IndexByte(line, ' ')
	if p < 0 {
		return "", "", nil
	}
	if p == 0 {
		e := sys.Error("cs: no translation")
		sys.Werrstr(e.Error())
		return "", "", e
	}
	return line[:p], line[p+1:], nil
}

func lastname(dir string) string {
	if p := strings.LastIndexByte(dir, '/'); p >= 0 {
		return dir[p+1:]
	}
	return dir
}

func parent(dir string) string {
	if p := strings.LastIndexByte(dir, '/'); p >= 0 {
		return dir[:p]
	}
	return ""
}

// Netmkaddr makes a full network address from addr, net and service.
func Netmkaddr(addr, net, svc string) string {
	if net == "" {
		net = "net"
	}
	n := len(strings.FieldsFunc(addr, func(r rune) bool { return r == '!' }))
	if n <= 1 {
		if svc == "" {
			return net + "!" + addr
		}
		return net + "!" + addr + "!" + svc
	}
	if svc == "" || n > 2 {
		return addr
	}
	return addr + "!" + svc
}

// Netinfo describes c.
func Netinfo(c *Connection) *Conninfo {
	dir := c.Dir
	if dir == "" {
		if c.Dfd == nil {
			return nil
		}
		p, err := sys.Fd2path(int(c.Dfd.Fd()))
		if err != nil {
			return nil
		}
		if dir = parent(p); dir == "" {
			return nil
		}
	}
	ci := &Conninfo{Dir: dir, Root: parent(dir)}
	for p := parent(ci.Root); p != "" && p != "/"; p = parent(ci.Root) {
		ci.Root = p
	}
	if d, err := sys.Stat(ci.Dir); err == nil {
		ci.Spec = "#" + string(rune(d.Type)) + strconv.Itoa(int(d.Dev))
	}
	ci.Lsys, ci.Lserv = endpoint(ci.Dir, "local")
	ci.Rsys, ci.Rserv = endpoint(ci.Dir, "remote")
	p := parent(ci.Dir)
	if p == "" {
		return nil
	}
	p = strings.TrimPrefix(p, "/net/")
	ci.Laddr = p + "!" + ci.Lsys + "!" + ci.Lserv
	ci.Raddr = p + "!" + ci.Rsys + "!" + ci.Rserv
	return ci
}

func endpoint(dir, file string) (string, string) {
	b, err := os.ReadFile(dir + "/" + file)
	if err != nil || len(b) == 0 {
		return "???", "???"
	}
	s := strings.TrimSuffix(string(b), "\n")
	sysname, serv, ok := strings.Cut(s, "!")
	if !ok {
		return s, "???"
	}
	return sysname, serv
}
