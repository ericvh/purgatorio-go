// Command mntgen serves Styx on its standard input (Inferno mntgen(4)):
// one directory, initially empty, in which a subdirectory appears when it
// is first walked to and stays while anything refers to it. It is
// mounted with mount {mntgen} dir.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/styxservers"
	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const qroot = 0xfffffff

type entry struct {
	refcount int
	path     uint64
}

var (
	refcounts = make([]entry, 10)
	tree      = styxservers.NewTree()
	uniq      uint64
)

func main() {
	sys.Detach()
	srv := styxservers.New(os.NewFile(0, "styx"), tree, qroot)
	tree.Create(qroot, dir(".", sys.DMDIR|0o555, qroot))
	for {
		t, err := srv.Next()
		if err != nil {
			return
		}
		if e := handle(t, srv); e != nil {
			srv.Error(t, e.Error())
		}
	}
}

func walk1(c *styxservers.Fid, name string) error {
	switch {
	case name == "..":
		if c.Path != qroot {
			decref(c.Path)
		}
		c.Walk(styx.Qid{Path: qroot, Type: styx.QTDIR})
	case c.Path == qroot:
		d, _ := tree.Walk(c.Path, name)
		if d == nil {
			d = addentry(name)
		} else {
			incref(d.Qid.Path)
		}
		c.Walk(d.Qid)
	default:
		return styxservers.Enotfound
	}
	return nil
}

func handle(t *styx.Fcall, srv *styxservers.Server) error {
	switch t.Type {
	case styx.Twalk:
		c := srv.GetFid(t.Fid)
		if c == nil {
			return styxservers.Ebadfid
		}
		if c.IsOpen {
			return styxservers.Eopen
		}
		if t.Newfid != t.Fid {
			nc := srv.NewFid(t.Newfid)
			if nc == nil {
				return styxservers.Einuse
			}
			c = c.Clone(nc)
			incref(c.Path)
		}
		qids := make([]styx.Qid, 0, len(t.Wname))
		oldpath, oldqtype := c.Path, c.Qtype
		incref(oldpath)
		for i, name := range t.Wname {
			if err := walk1(c, name); err != nil {
				if t.Newfid != t.Fid {
					decref(c.Path)
					srv.DelFid(c)
				}
				c.Path, c.Qtype = oldpath, oldqtype
				if i == 0 {
					return err
				}
				srv.Reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
				return nil
			}
			qids = append(qids, styx.Qid{Path: c.Path, Type: c.Qtype})
		}
		decref(oldpath)
		srv.Reply(&styx.Fcall{Type: styx.Rwalk, Tag: t.Tag, Wqid: qids})
	case styx.Tclunk:
		if c := srv.Clunk(t); c != nil && c.Path != qroot {
			decref(c.Path)
		}
	default:
		srv.Default(t)
	}
	return nil
}

func addentry(name string) *styx.Dir {
	i := 0
	for i < len(refcounts) && refcounts[i].refcount != 0 {
		i++
	}
	if i == len(refcounts) {
		refcounts = append(refcounts, make([]entry, len(refcounts))...)
	}
	d := dir(name, sys.DMDIR|0o555, uint64(i)|uniq<<32)
	uniq++
	tree.Create(qroot, d)
	refcounts[i] = entry{1, d.Qid.Path}
	return &d
}

func incref(q uint64) {
	if id := int(uint32(q)); q != qroot && id < len(refcounts) {
		refcounts[id].refcount++
	}
}

func decref(q uint64) {
	if id := int(uint32(q)); q != qroot && id < len(refcounts) {
		if refcounts[id].refcount--; refcounts[id].refcount == 0 {
			tree.Remove(refcounts[id].path)
		}
	}
}

func dir(name string, perm uint32, path uint64) styx.Dir {
	d := styx.Dir{Name: name, UID: "me", GID: "me", Mode: perm, Qid: styx.Qid{Path: path}}
	if perm&sys.DMDIR != 0 {
		d.Qid.Type = styx.QTDIR
	}
	return d
}
