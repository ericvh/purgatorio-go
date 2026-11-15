package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	beatquanta = 10 // seconds between heartbeats
	beatlimit  = 10 // idle heartbeats before a NOOP
	maxfdata   = styxservers.MAXFDATA
)

// fid is a client's fid: the node it refers to.
type fid struct {
	node *node
}

var (
	srv  *styxservers.Server
	fids = map[uint32]*fid{}
)

func serve(rootpath string) {
	remrootpath = rootpath
	remroot = newnode(nil, "/")
	remroot.dir.Mode = styx.DMDIR | 0o777
	remdir = remroot
	srv = styxservers.New(os.NewFile(0, "styx"), nil, 0)
	beatcount := 0
	for {
		// A read can bring in several requests; answer those already
		// buffered before waiting, or they sit unanswered until more
		// input comes (a burst of clunks, then nothing).
		if srv.Buffered() {
			if !handlenext() {
				return
			}
			continue
		}
		i, err := sys.Wait([][2]int{{sys.WaitRead, 0}}, beatquanta*1000)
		if err != nil {
			break
		}
		if i < 0 {
			if activity || transfer {
				beatcount, activity = 0, false
				continue
			}
			if beatcount++; beatcount == beatlimit {
				if sendrequest("NOOP", false) == success {
					getreply(false)
				}
				beatcount, activity = 0, false
			}
			continue
		}
		if !handlenext() {
			return
		}
	}
}

// handlenext reads one request and answers it; false means the client
// has gone.
func handlenext() bool {
	t, err := srv.Next()
	if err != nil {
		if debug > 0 {
			print("ftpfs: server: exiting\n")
		}
		return false
	}
	if debug > 0 {
		print("< " + styxservers.Text(t) + "\n")
	}
	handle(t)
	return true
}

func reply(r *styx.Fcall) {
	if debug > 0 {
		print("> " + styxservers.Text(r) + "\n")
	}
	if err := srv.Reply(r); err != nil {
		print("ftpfs: error replying: " + sys.Errstr() + "\n")
	}
}

func rerror(t *styx.Fcall, s string) {
	if debug > 0 {
		print("error: " + s + "\n")
	}
	reply(&styx.Fcall{Type: styx.Rerror, Tag: t.Tag, Ename: s})
}

func handle(t *styx.Fcall) {
	switch t.Type {
	case styx.Tversion:
		msize, version := styxservers.Compatible(t, styxservers.MAXRPC, styx.Version)
		srv.Msize = msize
		reply(&styx.Fcall{Type: styx.Rversion, Tag: t.Tag, Msize: msize, Version: version})
		return
	case styx.Tauth:
		rerror(t, eauthnotreqd)
		return
	case styx.Tflush:
		reply(&styx.Fcall{Type: styx.Rflush, Tag: t.Tag})
		return
	case styx.Tattach:
		fids[t.Fid] = &fid{node: remroot}
		reply(&styx.Fcall{Type: styx.Rattach, Tag: t.Tag, Qid: remroot.qid()})
		return
	}
	f := fids[t.Fid]
	if f == nil {
		rerror(t, string(styxservers.Ebadfid))
		return
	}
	switch t.Type {
	case styx.Twalk:
		walk(t, f)
	case styx.Topen:
		open(t, f)
	case styx.Tcreate:
		create(t, f)
	case styx.Tread:
		read(t, f)
	case styx.Twrite:
		write(t, f)
	case styx.Tclunk:
		n := f.node
		if n.fileisdirty() {
			if n.createfile() < 0 {
				print("ftpfs: could not create " + n.pathname() + ": " + errstr + "\n")
			}
			n.fileclean()
			n.uncache()
		}
		delete(fids, t.Fid)
		reply(&styx.Fcall{Type: styx.Rclunk, Tag: t.Tag})
	case styx.Tremove:
		n := f.node
		var r int
		if n.dir.Mode&styx.DMDIR != 0 {
			r = n.action("RMD")
		} else {
			r = n.action("DELE")
		}
		if r < 0 {
			rerror(t, errstr)
			return
		}
		n.parent.uncache()
		n.uncache()
		n.valid = false
		delete(fids, t.Fid)
		reply(&styx.Fcall{Type: styx.Rremove, Tag: t.Tag})
	case styx.Tstat:
		p := f.node.parent
		if !p.cached {
			p.invalidate()
			p.readdir()
			p.markcached()
		}
		if !f.node.valid {
			rerror(t, enosuchfile)
			return
		}
		reply(&styx.Fcall{Type: styx.Rstat, Tag: t.Tag, Stat: f.node.stat()})
	case styx.Twstat:
		rerror(t, enowstat)
	default:
		rerror(t, "unimp")
	}
}

func walk(t *styx.Fcall, f *fid) {
	n := f.node
	qids := make([]styx.Qid, 0, len(t.Wname))
	for i, name := range t.Wname {
		if n.dir.Mode&styx.DMDIR == 0 {
			if i == 0 {
				rerror(t, enotadir)
				return
			}
			break
		}
		switch name {
		case "..":
			n = n.parent
		case ".":
		case ".flush.ftpfs":
			n.invalidate()
			n.readdir()
		default:
			n = n.extendpath(name)
			if n.parent.cached {
				if !n.valid {
					if i == 0 {
						rerror(t, enosuchfile)
						return
					}
					reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
					return
				}
				if n.dir.Mode&chsyml != 0 {
					n.fixsymbolic()
				}
			} else if !n.valid {
				if n.changedir() == 0 {
					n.dir.Mode |= styx.DMDIR
				} else {
					n.dir.Mode &^= styx.DMDIR
				}
			}
		}
		qids = append(qids, n.qid())
	}
	if len(qids) < len(t.Wname) {
		reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
		return
	}
	if t.Newfid != t.Fid {
		if fids[t.Newfid] != nil {
			rerror(t, "fid in use")
			return
		}
		fids[t.Newfid] = &fid{node: n}
	} else {
		f.node = n
	}
	reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
}

func open(t *styx.Fcall, f *fid) {
	n := f.node
	if n.dir.Mode&styx.DMDIR != 0 && t.Mode != sys.OREAD {
		rerror(t, epermission)
		return
	}
	if t.Mode&sys.OTRUNC != 0 {
		n.uncache()
		n.parent.uncache()
		n.filedirty()
	} else if !n.cached {
		n.filefree()
		if n.dir.Mode&styx.DMDIR != 0 {
			n.invalidate()
			if n.readdir() < 0 {
				rerror(t, enosuchfile)
				return
			}
		} else if n.readfile() < 0 {
			rerror(t, errstr)
			return
		}
		n.markcached()
	}
	reply(&styx.Fcall{Type: styx.Ropen, Tag: t.Tag, Qid: n.qid(), Iounit: maxfdata})
}

func create(t *styx.Fcall, f *fid) {
	if f.node.dir.Mode&styx.DMDIR == 0 {
		rerror(t, enotadir)
		return
	}
	n := f.node.extendpath(t.Name)
	f.node = n
	n.uncache()
	if t.Perm&styx.DMDIR != 0 {
		if n.action("MKD") < 0 {
			rerror(t, epermission)
			return
		}
	} else {
		n.filedirty()
	}
	n.parent.invalidate()
	n.parent.uncache()
	reply(&styx.Fcall{Type: styx.Rcreate, Tag: t.Tag, Qid: n.qid(), Iounit: maxfdata})
}

func read(t *styx.Fcall, f *fid) {
	count := int(t.Count)
	if count > maxfdata {
		count = maxfdata
	}
	if int64(t.Offset) < 0 {
		rerror(t, ebadoffset)
		return
	}
	n := f.node
	var out []byte
	if n.dir.Mode&styx.DMDIR != 0 {
		offset := int(t.Offset)
		i := 0
		for ; offset > 0 && i < len(n.children); i++ {
			if p := n.children[i]; p.valid {
				offset -= len(p.stat())
			}
		}
		for ; len(out) < count && i < len(n.children); i++ {
			p := n.children[i]
			if !p.valid {
				continue
			}
			if p.dir.Mode&chsyml != 0 {
				p.fixsymbolic()
			}
			a := p.stat()
			if len(out)+len(a) > count {
				break
			}
			out = append(out, a...)
		}
	} else {
		if !n.cached && n.readfile() < 0 {
			rerror(t, errstr)
			return
		}
		n.markcached()
		out = n.fileread(int(t.Offset), count)
	}
	reply(&styx.Fcall{Type: styx.Rread, Tag: t.Tag, Data: out})
}

func write(t *styx.Fcall, f *fid) {
	n := f.node
	if n.dir.Mode&styx.DMDIR != 0 {
		rerror(t, eisadir)
		return
	}
	count := n.filewrite(t.Data, int(t.Offset))
	n.filedirty()
	reply(&styx.Fcall{Type: styx.Rwrite, Tag: t.Tag, Count: uint32(count)})
}
