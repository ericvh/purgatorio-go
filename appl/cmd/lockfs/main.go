// Command lockfs serves the files of a directory with multiple-reader,
// exclusive-writer locking (Inferno lockfs(4)). Opening a file takes a
// lock on it, or waits until the lock can be had.
//
// Without -p the server runs as a second prog in a forked name space,
// serving one end of a pipe whose other end is mounted on the mount point.
// With -p it announces addr and serves each connection that arrives. A
// guest is single-threaded, so a helper prog sharing the fd group
// listens for calls and passes each new connection's fd to the server
// over a pipe; the server waits on all of its connections at once.
//
// Authentication needs Inferno's keyring, which is not here: -p needs -A.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/dial"
	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	elocked   = "file is locked"
	einuse    = "fid already in use"
	ebadfid   = "bad fid"
	enotfound = "file does not exist"
	enotdir   = "not a directory"
	eperm     = "permission denied"
	eexists   = "file already exists"

	maxconn = 20
)

var (
	stderr     = os.Stderr
	verbose    bool
	lockdir    string
	maxqidpath uint64      = 1
	locks      []*lockfile // newest first
	starttime  int
	uname      string
)

func usage() {
	fmt.Fprintf(stderr, "usage: lockfs [-A] [-a alg]... [-p addr] dir [mountpoint]\n")
	sys.Exits("fail:usage")
}

func main() {
	if _, err := os.ReadFile("/dev/time"); err != nil {
		fmt.Fprintf(stderr, "lockfs: cannot open /dev/time: %s\n", sys.Errstr())
		sys.Exits("fail:no time")
	}
	a := arg.New(os.Args)
	addr := ""
	doauth := true
	var algs []string
	srvmode, listenmode := false, false
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 'p':
			addr = a.Arg()
		case 'a':
			alg := a.Arg()
			if alg == "" {
				usage()
			}
			algs = append(algs, alg)
		case 'A':
			doauth = false
		case 'v':
			verbose = true
		case 's': // internal: serve standard input
			srvmode = true
		case 'L': // internal: listen for calls
			listenmode = true
		default:
			usage()
		}
	}
	argv := a.Argv()
	if listenmode {
		listener(argv)
		return
	}
	if len(argv) == 0 || addr != "" && len(argv) > 1 {
		usage()
	}
	lockdir = argv[0]
	mountpoint := lockdir
	if len(argv) > 1 {
		mountpoint = argv[1]
	}
	if srvmode {
		server([]int{0}, -1)
		return
	}
	if addr == "" {
		doauth = false // no authentication for a local mount
	}
	if doauth {
		_ = algs
		fmt.Fprintf(stderr, "lockfs: cannot init auth: keyring authentication is not available; use -A\n")
		sys.Exits("fail:errors")
	}
	if addr != "" {
		serveNet(addr)
		return
	}
	p, err := sys.Pipe()
	if err != nil {
		fmt.Fprintf(stderr, "lockfs: cannot mount: %s\n", sys.Errstr())
		sys.Exits("fail:cannot mount")
	}
	args := []string{"lockfs", "-s"}
	if verbose {
		args = append(args, "-v")
	}
	args = append(args, lockdir)
	if _, err := sys.Spawn("/bin/lockfs.wasm", args, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[1]}, {1, 1}, {2, 2}}); err != nil {
		fmt.Fprintf(stderr, "lockfs: cannot start server: %s\n", sys.Errstr())
		sys.Exits("fail:errors")
	}
	sys.Close(p[1])
	if err := sys.Mount(p[0], -1, mountpoint, sys.MREPL|sys.MCREATE, ""); err != nil {
		fmt.Fprintf(stderr, "lockfs: cannot mount: %s\n", sys.Errstr())
		sys.Exits("fail:cannot mount")
	}
}

// serveNet announces addr, starts the listener and becomes the server.
func serveNet(addr string) {
	addr = dial.Netmkaddr(addr, "tcp", "33234")
	c, err := dial.Announce(addr)
	if err != nil {
		fmt.Fprintf(stderr, "lockfs: failed to start listener: cannot anounce on %s: %s\n", addr, sys.Errstr())
		sys.Exits("fail:errors")
	}
	p, err := sys.Pipe()
	if err != nil {
		fmt.Fprintf(stderr, "lockfs: failed to start listener: %s\n", sys.Errstr())
		sys.Exits("fail:errors")
	}
	// The listener shares this prog's fd group, so the fds it opens are
	// valid here.
	args := []string{"lockfs", "-L", c.Dir, strconv.Itoa(int(c.Cfd.Fd())), strconv.Itoa(p[1])}
	if _, err := sys.Spawn("/bin/lockfs.wasm", args, 0, nil); err != nil {
		fmt.Fprintf(stderr, "lockfs: failed to start listener: %s\n", sys.Errstr())
		sys.Exits("fail:errors")
	}
	sys.Detach()
	sys.Pctl(sys.FORKNS, nil)
	server(nil, p[0])
}

// listener takes calls and writes each data file's fd to the server.
func listener(argv []string) {
	if len(argv) != 3 {
		usage()
	}
	cfd, _ := strconv.Atoi(argv[1])
	wfd, _ := strconv.Atoi(argv[2])
	w := os.NewFile(uintptr(wfd), "pipe")
	ac := &dial.Connection{Cfd: os.NewFile(uintptr(cfd), "ctl"), Dir: argv[0]}
	for {
		nc, err := dial.Listen(ac)
		if err != nil {
			fmt.Fprintf(w, "! listen failed: %s\n", sys.Errstr())
			return
		}
		dfd, err := sys.Open(nc.Dir+"/data", sys.ORDWR)
		nc.Cfd.Close()
		if err == nil {
			fmt.Fprintf(w, "%d\n", dfd)
		}
	}
}

// Lock state.

type openreq struct {
	cn    *conn
	tag   uint16
	omode int
	c     *chanx
}

type lockfile struct {
	waitq   []*openreq
	fd      *os.File
	readers int
	writers int
	d       *styx.Dir
}

// flush drops queued opens from cn with tag, or all of cn's for tag -1.
func (l *lockfile) flush(cn *conn, tag int) {
	q := l.waitq[:0]
	for _, r := range l.waitq {
		if r.cn != cn || tag != -1 && int(r.tag) != tag {
			q = append(q, r)
		}
	}
	l.waitq = q
}

func now() int {
	b, err := os.ReadFile("/dev/time")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return int(n / 1000000)
}

func user() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil || len(b) == 0 {
		return "unknown"
	}
	return string(b)
}

func lockinit() {
	fd, err := sys.Open(lockdir, sys.OREAD)
	if err != nil {
		return
	}
	defer sys.Close(fd)
	var all []*lockfile
	seen := map[string]bool{}
	for {
		ds, err := sys.Dirread(fd)
		if err != nil || len(ds) == 0 {
			break
		}
		for _, d := range ds {
			if seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			sd := sysdir(&d)
			sd.Qid = styx.Qid{Path: maxqidpath, Type: styx.QTFILE}
			maxqidpath++
			all = append(all, &lockfile{d: sd})
		}
	}
	// Directories are not served.
	for _, l := range all {
		if l.d.Mode&styx.DMDIR == 0 {
			locks = append(locks, l)
		}
	}
}

func sysdir(d *sys.Dir) *styx.Dir {
	return &styx.Dir{Type: d.Type, Dev: d.Dev, Qid: styx.Qid{Path: d.Qid.Path, Vers: d.Qid.Vers, Type: d.Qid.Type},
		Mode: d.Mode, Atime: d.Atime, Mtime: d.Mtime, Length: d.Length,
		Name: d.Name, UID: d.UID, GID: d.GID, MUID: d.MUID}
}

func qid2lock(q styx.Qid) *lockfile {
	for _, l := range locks {
		if l.d.Qid.Path == q.Path {
			return l
		}
	}
	return nil
}

func name2lock(name string) *lockfile {
	for _, l := range locks {
		if l.d.Name == name {
			return l
		}
	}
	return nil
}

// The Styx service.

type chanx struct {
	qid  styx.Qid
	open bool
	path string
}

type conn struct {
	f     *os.File
	fd    int
	buf   []byte
	msize uint32
	chans map[uint32]*chanx
}

func (cn *conn) reply(r *styx.Fcall) {
	if verbose {
		fmt.Printf("-> %s\n", styxservers.Text(r))
	}
	b, err := styx.Marshal(nil, r)
	if err == nil {
		cn.f.Write(b)
	}
}

func (cn *conn) error(tag uint16, e string) {
	cn.reply(&styx.Fcall{Type: styx.Rerror, Tag: tag, Ename: e})
}

// next returns the next whole message in the buffer, or nil.
func (cn *conn) next() (*styx.Fcall, error) {
	n := styx.Size(cn.buf)
	if n == 0 || int(n) > len(cn.buf) {
		if n > 1<<20 || n != 0 && n < 7 {
			return nil, fmt.Errorf("bad message size")
		}
		return nil, nil
	}
	m, err := styx.Unmarshal(cn.buf[:n], 1<<20)
	cn.buf = append(cn.buf[:0], cn.buf[n:]...)
	return m, err
}

// server serves the connections on fds, and new ones from the listener
// pipe lfd when it is not -1. It returns when the last connection goes
// and no more can come.
func server(fds []int, lfd int) {
	starttime = now()
	uname = user()
	lockinit()
	var conns []*conn
	add := func(fd int) {
		if len(conns) >= maxconn {
			fmt.Fprintf(stderr, "lockfs: too many clients\n")
			sys.Close(fd)
			return
		}
		conns = append(conns, &conn{f: os.NewFile(uintptr(fd), "styx"), fd: fd, chans: map[uint32]*chanx{}})
	}
	for _, fd := range fds {
		add(fd)
	}
	var lpipe *os.File
	var lbuf []byte
	if lfd >= 0 {
		lpipe = os.NewFile(uintptr(lfd), "listener")
	}
	rbuf := make([]byte, 64*1024)
	for len(conns) > 0 || lpipe != nil {
		var ops [][2]int
		for _, cn := range conns {
			ops = append(ops, [2]int{sys.WaitRead, cn.fd})
		}
		if lpipe != nil {
			ops = append(ops, [2]int{sys.WaitRead, lfd})
		}
		i, err := sys.Wait(ops, -1)
		if err != nil || i < 0 {
			break
		}
		if i == len(conns) {
			n, _ := lpipe.Read(rbuf)
			if n <= 0 {
				lpipe = nil
				continue
			}
			lbuf = append(lbuf, rbuf[:n]...)
			for {
				j := strings.IndexByte(string(lbuf), '\n')
				if j < 0 {
					break
				}
				line := string(lbuf[:j])
				lbuf = lbuf[j+1:]
				if strings.HasPrefix(line, "!") {
					if verbose {
						fmt.Printf("lockfs: localserver going down (reason: %s)\n", strings.TrimSpace(line[1:]))
					}
					lpipe = nil
					break
				}
				if verbose {
					fmt.Printf("lockfs: got new connection (s == '')\n")
				}
				fd, _ := strconv.Atoi(line)
				add(fd)
			}
			continue
		}
		cn := conns[i]
		n, _ := cn.f.Read(rbuf)
		alive := n > 0
		if alive {
			cn.buf = append(cn.buf, rbuf[:n]...)
			for {
				m, err := cn.next()
				if err != nil {
					alive = false
					break
				}
				if m == nil {
					break
				}
				handle(cn, m)
			}
		}
		if !alive {
			hangup(cn)
			cn.f.Close()
			conns = append(conns[:i:i], conns[i+1:]...)
		}
	}
	if verbose {
		fmt.Printf("lockfs: finished\n")
	}
}

// hangup releases the locks and queued opens of a connection that has
// gone.
func hangup(cn *conn) {
	for _, l := range locks {
		l.flush(cn, -1)
	}
	for _, c := range cn.chans {
		if l := qid2lock(c.qid); l != nil && c.open {
			unlocked(l)
		}
	}
}

func handle(cn *conn, m *styx.Fcall) {
	if verbose {
		fmt.Printf("<- %s\n", styxservers.Text(m))
	}
	fid2chan := func() *chanx {
		c := cn.chans[m.Fid]
		if c == nil {
			fmt.Fprintf(stderr, "lockfs: panic:bad fid\n")
			cn.error(m.Tag, ebadfid)
		}
		return c
	}
	switch m.Type {
	case styx.Tversion:
		if cn.msize == 0 {
			cn.msize = styxservers.MAXRPC
		}
		msize, version := styxservers.Compatible(m, cn.msize, styx.Version)
		if msize < 128 {
			cn.error(m.Tag, "unusable message size")
			return
		}
		cn.msize = msize
		cn.reply(&styx.Fcall{Type: styx.Rversion, Tag: m.Tag, Msize: msize, Version: version})
	case styx.Tauth:
		cn.error(m.Tag, "authentication not required")
	case styx.Tattach:
		if cn.chans[m.Fid] != nil {
			cn.error(m.Tag, einuse)
			return
		}
		c := &chanx{qid: styx.Qid{Type: styx.QTDIR}, path: "dev"}
		cn.chans[m.Fid] = c
		cn.reply(&styx.Fcall{Type: styx.Rattach, Tag: m.Tag, Qid: c.qid})
	case styx.Twalk:
		c := fid2chan()
		if c == nil {
			return
		}
		cc := *c
		var qids []styx.Qid
		if len(m.Wname) > 0 {
			// The tree has just one level.
			if cc.qid.Type&styx.QTDIR == 0 {
				cn.error(m.Tag, enotdir)
				return
			}
			l := name2lock(m.Wname[0])
			if l == nil {
				cn.error(m.Tag, enotfound)
				return
			}
			cc.qid = l.d.Qid
			cc.path = l.d.Name
			qids = []styx.Qid{cc.qid}
		}
		if m.Newfid != m.Fid {
			if cn.chans[m.Newfid] != nil {
				cn.error(m.Tag, einuse)
				return
			}
			cc.open = false
			cn.chans[m.Newfid] = &cc
		} else {
			c.qid, c.path = cc.qid, cc.path
		}
		cn.reply(&styx.Fcall{Type: styx.Rwalk, Tag: m.Tag, Wqid: qids})
	case styx.Topen:
		c := fid2chan()
		if c == nil {
			return
		}
		if c.qid.Type&styx.QTDIR != 0 {
			c.open = true
			cn.reply(&styx.Fcall{Type: styx.Ropen, Tag: m.Tag, Qid: c.qid, Iounit: styxservers.MAXFDATA})
			return
		}
		l := qid2lock(c.qid)
		if l == nil {
			cn.error(m.Tag, enotfound)
			return
		}
		req := &openreq{cn: cn, tag: m.Tag, omode: int(m.Mode), c: c}
		if l.fd == nil || req.omode == sys.OREAD && l.writers == 0 {
			openlockfile(l, req)
		} else {
			l.waitq = append(l.waitq, req)
		}
	case styx.Tcreate:
		c := fid2chan()
		if c == nil {
			return
		}
		if c.qid.Type&styx.QTDIR == 0 {
			cn.error(m.Tag, enotdir)
			return
		}
		if m.Perm&styx.DMDIR != 0 {
			cn.error(m.Tag, eperm)
			return
		}
		if name2lock(m.Name) != nil {
			cn.error(m.Tag, eexists)
			return
		}
		fd, err := sys.Create(lockdir+"/"+m.Name, int(m.Mode), m.Perm)
		if err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		sd, err := sys.Fstat(fd)
		if err != nil {
			e := sys.Errstr()
			sys.Close(fd)
			cn.error(m.Tag, e)
			return
		}
		l := &lockfile{fd: os.NewFile(uintptr(fd), m.Name), d: sysdir(sd)}
		l.d.Qid = styx.Qid{Path: maxqidpath, Type: styx.QTFILE}
		maxqidpath++
		t := uint32(now())
		l.d.Mtime, l.d.Atime = t, t
		if int(m.Mode) == sys.OREAD {
			l.readers = 1
		} else {
			l.writers = 1
		}
		locks = append([]*lockfile{l}, locks...)
		c.qid = l.d.Qid
		c.path = l.d.Name
		c.open = true
		cn.reply(&styx.Fcall{Type: styx.Rcreate, Tag: m.Tag, Qid: c.qid, Iounit: styxservers.MAXFDATA})
	case styx.Tread:
		c := fid2chan()
		if c == nil {
			return
		}
		if c.qid.Type&styx.QTDIR != 0 {
			cn.reply(dirread(m))
			return
		}
		l := qid2lock(c.qid)
		if l == nil {
			cn.error(m.Tag, enotfound)
			return
		}
		if !c.open || l.fd == nil {
			cn.error(m.Tag, ebadfid)
			return
		}
		d := make([]byte, min(m.Count, cn.msize))
		if _, err := sys.Seek(int(l.fd.Fd()), int64(m.Offset), 0); err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		n, err := l.fd.Read(d)
		if err != nil && err != io.EOF {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		cn.reply(&styx.Fcall{Type: styx.Rread, Tag: m.Tag, Data: d[:max(n, 0)]})
		l.d.Atime = uint32(now())
	case styx.Twrite:
		c := fid2chan()
		if c == nil {
			return
		}
		if c.qid.Type&styx.QTDIR != 0 {
			cn.error(m.Tag, eperm)
			return
		}
		l := qid2lock(c.qid)
		if l == nil {
			cn.error(m.Tag, enotfound)
			return
		}
		if !c.open || l.fd == nil {
			cn.error(m.Tag, ebadfid)
			return
		}
		if _, err := sys.Seek(int(l.fd.Fd()), int64(m.Offset), 0); err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		n, err := l.fd.Write(m.Data)
		if err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		cn.reply(&styx.Fcall{Type: styx.Rwrite, Tag: m.Tag, Count: uint32(n)})
		if nl := m.Offset + uint64(n); nl > l.d.Length {
			l.d.Length = nl
		}
		l.d.Mtime = uint32(now())
		l.d.Qid.Vers++
	case styx.Tclunk:
		c := fid2chan()
		if c == nil {
			return
		}
		delete(cn.chans, m.Fid)
		cn.reply(&styx.Fcall{Type: styx.Rclunk, Tag: m.Tag})
		if c.open {
			if l := qid2lock(c.qid); l != nil {
				unlocked(l)
			}
		}
	case styx.Tflush:
		for _, l := range locks {
			l.flush(cn, int(m.Oldtag))
		}
		cn.reply(&styx.Fcall{Type: styx.Rflush, Tag: m.Tag})
	case styx.Tstat:
		c := fid2chan()
		if c == nil {
			return
		}
		if l := qid2lock(c.qid); l != nil && c.qid.Type&styx.QTDIR == 0 {
			cn.reply(&styx.Fcall{Type: styx.Rstat, Tag: m.Tag, Stat: styx.MarshalDir(nil, l.d)})
		} else if c.qid.Type&styx.QTDIR != 0 {
			cn.reply(&styx.Fcall{Type: styx.Rstat, Tag: m.Tag, Stat: styx.MarshalDir(nil, devdir(c.qid, c.path))})
		} else {
			cn.error(m.Tag, enotfound)
		}
	case styx.Tremove:
		c := fid2chan()
		if c == nil {
			return
		}
		delete(cn.chans, m.Fid)
		if c.qid.Type&styx.QTDIR != 0 {
			cn.error(m.Tag, eperm)
			return
		}
		l := qid2lock(c.qid)
		if l == nil {
			cn.error(m.Tag, enotfound)
			return
		}
		if l.fd != nil {
			cn.error(m.Tag, elocked)
			return
		}
		if err := sys.Remove(lockdir + "/" + l.d.Name); err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		for i, x := range locks {
			if x == l {
				locks = append(locks[:i:i], locks[i+1:]...)
				break
			}
		}
		cn.reply(&styx.Fcall{Type: styx.Rremove, Tag: m.Tag})
	case styx.Twstat:
		c := fid2chan()
		if c == nil {
			return
		}
		if c.qid.Type&styx.QTDIR != 0 {
			cn.error(m.Tag, eperm)
			return
		}
		l := qid2lock(c.qid)
		if l == nil {
			cn.error(m.Tag, enotfound)
			return
		}
		wd, err := sys.UnpackDir(m.Stat)
		if err != nil {
			cn.error(m.Tag, "bad argument")
			return
		}
		if err := sys.Wstat(lockdir+"/"+l.d.Name, wd); err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		name := wd.Name
		if name == "" {
			name = l.d.Name
		}
		sd, err := sys.Stat(lockdir + "/" + name)
		if err != nil {
			cn.error(m.Tag, sys.Errstr())
			return
		}
		nd := sysdir(sd)
		nd.Qid = l.d.Qid
		l.d = nd
		cn.reply(&styx.Fcall{Type: styx.Rwstat, Tag: m.Tag})
	default:
		cn.error(m.Tag, "unknown message")
	}
}

func devdir(q styx.Qid, name string) *styx.Dir {
	return &styx.Dir{Name: name, Qid: q, Type: 'X', Mode: styx.DMDIR | 0o555,
		Atime: uint32(starttime), Mtime: uint32(starttime), UID: uname, GID: uname}
}

// dirread replies to a read of the directory with the lock files.
func dirread(m *styx.Fcall) *styx.Fcall {
	offset := int(m.Offset)
	var data []byte
	start := 0
	for _, l := range locks {
		d := styx.MarshalDir(nil, l.d)
		if start < offset {
			start += len(d)
			continue
		}
		if len(data)+len(d) > int(m.Count) {
			break
		}
		data = append(data, d...)
	}
	return &styx.Fcall{Type: styx.Rread, Tag: m.Tag, Data: data}
}

// unlocked drops one holder of l and lets waiting opens in.
func unlocked(l *lockfile) {
	if l.readers > 0 {
		l.readers--
	} else {
		l.writers--
	}
	if l.readers > 0 {
		return
	}
	if l.fd != nil {
		l.fd.Close()
		l.fd = nil
	}
	// Let in the requests at the head of the queue: one writer, or
	// readers up to the first writer.
	for len(l.waitq) > 0 && l.writers == 0 {
		r := l.waitq[0]
		if r.omode != sys.OREAD && l.readers > 0 {
			break
		}
		openlockfile(l, r)
		l.waitq = l.waitq[1:]
	}
}

func openlockfile(l *lockfile, req *openreq) int {
	fd, err := sys.Open(lockdir+"/"+l.d.Name, req.omode)
	if err != nil {
		req.cn.error(req.tag, sys.Errstr())
		return -1
	}
	if l.fd != nil {
		l.fd.Close()
	}
	l.fd = os.NewFile(uintptr(fd), l.d.Name)
	req.c.open = true
	if req.omode&sys.OTRUNC != 0 {
		l.d.Length = 0
	}
	req.cn.reply(&styx.Fcall{Type: styx.Ropen, Tag: req.tag, Qid: l.d.Qid, Iounit: styxservers.MAXFDATA})
	if req.omode == sys.OREAD {
		l.readers++
	} else {
		l.writers++
	}
	return 0
}
