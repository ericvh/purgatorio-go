// Command trfs shows a directory on a mount point with each space in a
// file name shown as a no-break space, U+00A0 (Inferno trfs(4)), so that
// programs that split at spaces see one name.
//
// trfs exports the directory on one pipe and translates the Styx
// messages between it and the mount. trfs.b translates each direction in
// its own thread; a guest is single-threaded and mount waits for the
// server, so here a second prog of trfs (hidden first argument "@serve")
// does the translation in one loop, waiting on both sides with sys.Wait.
package main

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	serveFlag  = "@serve"
	nbspace    = " "
	nbspacelen = len(nbspace)
	maxrpc     = styx.IOHDRSZ + 8192
)

func fatal(s string) {
	os.Stderr.WriteString("trfs: " + s + ": " + sys.Errstr() + "\n")
	sys.Exits("fail:error")
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == serveFlag {
		serve()
		return
	}
	if len(os.Args) != 3 {
		os.Stderr.WriteString("usage: trfs dir mountpoint\n")
		sys.Exits("fail:usage")
	}
	dir, mntpt := os.Args[1], os.Args[2]
	p, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe")
	}
	q, err := sys.Pipe()
	if err != nil {
		fatal("can't create pipe")
	}
	if err := sys.Export(q[0], dir, sys.EXPASYNC); err != nil {
		fatal("can't export " + dir)
	}
	if _, err := sys.Spawn("/bin/trfs.wasm", []string{"trfs", serveFlag}, sys.NEWFD|sys.FORKNS,
		[][2]int{{0, p[1]}, {1, q[1]}, {2, 2}}); err != nil {
		fatal("can't start translator")
	}
	sys.Close(p[1])
	sys.Close(q[1])
	sys.Close(q[0])
	if _, err := sys.Mount(p[0], -1, mntpt, sys.MREPL|sys.MCREATE, ""); err != nil {
		fatal("can't mount on " + mntpt)
	}
}

type fid struct {
	isdir bool
	aux   int // how much longer the translated directory reads have been
}

var (
	fids  = map[uint32]*fid{}
	tfids = map[uint16]*fid{} // fid of each outstanding open, create or read
	msize = uint32(maxrpc)
)

// serve translates between the mount on fd 0 and the export on fd 1.
func serve() {
	client := os.NewFile(0, "client")
	server := os.NewFile(1, "server")
	ops := [][2]int{{sys.WaitRead, 0}, {sys.WaitRead, 1}}
	var bufs [2][]byte
	rbuf := make([]byte, 64*1024)
	files := [2]*os.File{client, server}
	for {
		i, err := sys.Wait(ops, -1)
		if err != nil || i < 0 {
			return
		}
		n, rerr := files[i].Read(rbuf)
		if n <= 0 {
			if rerr == nil || errors.Is(rerr, io.EOF) || n == 0 {
				return
			}
			return
		}
		bufs[i] = append(bufs[i], rbuf[:n]...)
		for {
			sz := styx.Size(bufs[i])
			if sz == 0 || uint32(len(bufs[i])) < sz {
				break
			}
			if sz < 7 {
				return
			}
			m, err := styx.Unmarshal(bufs[i][:sz], max(sz, msize))
			bufs[i] = bufs[i][sz:]
			if err != nil {
				return
			}
			var out *os.File
			if i == 0 {
				tin(m)
				out = server
			} else {
				tout(m)
				out = client
			}
			b, err := styx.Marshal(nil, m)
			if err != nil {
				return
			}
			out.Write(b)
		}
	}
}

// tin translates a T-message on its way to the server.
func tin(m *styx.Fcall) {
	switch m.Type {
	case styx.Tclunk, styx.Tremove:
		delete(fids, m.Fid)
	case styx.Tcreate, styx.Topen:
		f := fids[m.Fid]
		if f == nil {
			f = &fid{}
			fids[m.Fid] = f
		}
		tfids[m.Tag] = f
		if m.Type == styx.Tcreate {
			m.Name = strings.ReplaceAll(m.Name, nbspace, " ")
		}
	case styx.Tread:
		f := fids[m.Fid]
		if f == nil {
			f = &fid{}
		}
		tfids[m.Tag] = f
		if f.isdir {
			m.Count /= uint32(nbspacelen) // the names may grow this much
			if m.Offset == 0 {
				f.aux = 0
			}
			m.Offset -= uint64(f.aux)
		}
	case styx.Twalk:
		for i, n := range m.Wname {
			m.Wname[i] = strings.ReplaceAll(n, nbspace, " ")
		}
	case styx.Twstat:
		if d, err := styx.UnmarshalDir(m.Stat); err == nil {
			d.Name = strings.ReplaceAll(d.Name, nbspace, " ")
			m.Stat = styx.MarshalDir(nil, d)
		}
	}
}

// tout translates an R-message on its way to the client.
func tout(m *styx.Fcall) {
	switch m.Type {
	case styx.Rversion:
		msize = m.Msize
	case styx.Rcreate, styx.Ropen:
		if f := tfids[m.Tag]; f != nil {
			f.isdir = m.Qid.Type&styx.QTDIR != 0
		}
		delete(tfids, m.Tag)
	case styx.Rread:
		f := tfids[m.Tag]
		delete(tfids, m.Tag)
		if f == nil || !f.isdir {
			break
		}
		var b []byte
		n := 0
		for n < len(m.Data) {
			if len(m.Data)-n < 2 {
				break
			}
			ds := int(m.Data[n]) | int(m.Data[n+1])<<8 + 2
			if n+ds > len(m.Data) {
				break
			}
			d, err := styx.UnmarshalDir(m.Data[n : n+ds])
			if err != nil {
				break
			}
			d.Name = strings.ReplaceAll(d.Name, " ", nbspace)
			b = styx.MarshalDir(b, d)
			n += ds
		}
		f.aux += len(b) - n
		m.Data = b
	case styx.Rstat:
		if d, err := styx.UnmarshalDir(m.Stat); err == nil {
			d.Name = strings.ReplaceAll(d.Name, " ", nbspace)
			m.Stat = styx.MarshalDir(nil, d)
		}
	case styx.Rerror:
		delete(tfids, m.Tag)
	}
}
