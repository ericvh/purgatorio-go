package core

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// countSrv is a Styx server with one plain file, f, that counts the
// Treads it is sent. Its iounit is 1024 bytes.
type countSrv struct {
	mu    sync.Mutex
	data  []byte
	vers  uint32
	reads int
}

const countMsize = 1024 + styx.IOHDRSZ

func (s *countSrv) nreads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// change replaces the file's contents behind the client's back, with a
// new qid version.
func (s *countSrv) change(b []byte) {
	s.mu.Lock()
	s.data = append([]byte(nil), b...)
	s.vers++
	s.mu.Unlock()
}

func (s *countSrv) qid() styx.Qid { return styx.Qid{Type: styx.QTFILE, Vers: s.vers, Path: 1} }

var countRoot = styx.Qid{Type: styx.QTDIR, Path: 0}

// serve answers the messages arriving on fd in p until it is closed.
func (s *countSrv) serve(p *Prog, fd int) {
	fids := map[uint32]bool{} // fid -> is the file (else the root)
	var in []byte
	buf := make([]byte, 8192)
	for {
		for len(in) >= 4 && len(in) >= int(binary.LittleEndian.Uint32(in)) {
			n := int(binary.LittleEndian.Uint32(in))
			t, err := styx.Unmarshal(in[:n], 8192+styx.IOHDRSZ)
			in = in[n:]
			if err != nil {
				return
			}
			r := s.answer(t, fids)
			r.Tag = t.Tag
			b, err := styx.Marshal(nil, r)
			if err != nil {
				return
			}
			if _, err := p.Write(fd, b); err != nil {
				return
			}
		}
		n, err := p.Read(fd, buf)
		if err != nil || n == 0 {
			return
		}
		in = append(in, buf[:n]...)
	}
}

func (s *countSrv) answer(t *styx.Fcall, fids map[uint32]bool) *styx.Fcall {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(e string) *styx.Fcall { return &styx.Fcall{Type: styx.Rerror, Ename: e} }
	switch t.Type {
	case styx.Tversion:
		return &styx.Fcall{Type: styx.Rversion, Msize: min(t.Msize, countMsize), Version: styx.Version}
	case styx.Tattach:
		fids[t.Fid] = false
		return &styx.Fcall{Type: styx.Rattach, Qid: countRoot}
	case styx.Twalk:
		isf, ok := fids[t.Fid]
		if !ok {
			return fail("unknown fid")
		}
		var qids []styx.Qid
		for _, w := range t.Wname {
			if isf || w != "f" {
				break
			}
			isf = true
			qids = append(qids, s.qid())
		}
		if len(t.Wname) > 0 && len(qids) == 0 {
			return fail(abi.Enonexist)
		}
		if len(qids) == len(t.Wname) {
			fids[t.Newfid] = isf
		}
		return &styx.Fcall{Type: styx.Rwalk, Wqid: qids}
	case styx.Topen:
		q := countRoot
		if fids[t.Fid] {
			q = s.qid()
			if t.Mode&abi.OTRUNC != 0 {
				s.data = nil
				s.vers++
			}
		}
		return &styx.Fcall{Type: styx.Ropen, Qid: q}
	case styx.Tread:
		if !fids[t.Fid] {
			return &styx.Fcall{Type: styx.Rread}
		}
		s.reads++
		var d []byte
		if t.Offset < uint64(len(s.data)) {
			d = s.data[t.Offset:min(uint64(len(s.data)), t.Offset+uint64(t.Count))]
		}
		return &styx.Fcall{Type: styx.Rread, Data: append([]byte(nil), d...)}
	case styx.Twrite:
		end := int(t.Offset) + len(t.Data)
		if end > len(s.data) {
			s.data = append(s.data, make([]byte, end-len(s.data))...)
		}
		copy(s.data[t.Offset:], t.Data)
		s.vers++
		return &styx.Fcall{Type: styx.Rwrite, Count: uint32(len(t.Data))}
	case styx.Tstat:
		d := styx.Dir{Qid: countRoot, Mode: styx.DMDIR | 0o555, Name: "/", UID: "u", GID: "u", MUID: "u"}
		if fids[t.Fid] {
			d = styx.Dir{Qid: s.qid(), Mode: 0o666, Length: uint64(len(s.data)), Name: "f", UID: "u", GID: "u", MUID: "u"}
		}
		return &styx.Fcall{Type: styx.Rstat, Stat: styx.MarshalDir(nil, &d)}
	case styx.Twstat:
		return &styx.Fcall{Type: styx.Rwstat}
	case styx.Tclunk:
		delete(fids, t.Fid)
		return &styx.Fcall{Type: styx.Rclunk}
	case styx.Tflush:
		return &styx.Fcall{Type: styx.Rflush}
	}
	return fail("not supported")
}

// mountCount mounts a new countSrv holding data on /b with flags.
func mountCount(t *testing.T, p *Prog, data []byte, flags uint32) *countSrv {
	t.Helper()
	s := &countSrv{data: append([]byte(nil), data...)}
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go s.serve(p, fds[0])
	t.Cleanup(func() { p.Close(fds[0]) })
	if _, err := p.Mount(fds[1], -1, "/b", flags, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	p.Close(fds[1])
	return s
}

// readAll reads path n bytes at a time.
func readAll(t *testing.T, p *Prog, path string, n int) []byte {
	t.Helper()
	fd, err := p.Open(path, abi.OREAD)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(fd)
	var out []byte
	buf := make([]byte, n)
	for {
		k, err := p.Read(fd, buf)
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 {
			return out
		}
		out = append(out, buf[:k]...)
	}
}

func testData() []byte {
	b := make([]byte, 3000)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// On an MCACHE mount a sequential read of a plain file asks the server
// for a whole iounit at a time; without it, for what the prog asked.
func TestMcacheReadsAhead(t *testing.T) {
	data := testData()
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()

	plain := mountCount(t, p, data, abi.MREPL)
	if got := readAll(t, p, "/b/f", 100); !bytes.Equal(got, data) {
		t.Fatalf("read without MCACHE: %d bytes, wrong data", len(got))
	}
	cached := mountCount(t, p, data, abi.MREPL|abi.MCACHE)
	if got := readAll(t, p, "/b/f", 100); !bytes.Equal(got, data) {
		t.Fatalf("read with MCACHE: %d bytes, wrong data", len(got))
	}
	// 30 reads of 100 bytes and one at the end, against three blocks of
	// at most 1024 bytes and one at the end.
	if np, nc := plain.nreads(), cached.nreads(); np != 31 || nc != 4 {
		t.Errorf("Treads: %d without MCACHE, %d with; want 31 and 4", np, nc)
	}
	// Directories are never read ahead; listing still works.
	if got := names(t, p, "/b"); got != "" {
		t.Errorf("ls /b: %q", got)
	}
}

// A write through the mount drops the block read ahead, so the next read
// sees what was written; so does a stat that shows a new qid version.
func TestMcacheWriteInvalidates(t *testing.T) {
	data := testData()
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	s := mountCount(t, p, data, abi.MREPL|abi.MCACHE)

	fd, err := p.Open("/b/f", abi.ORDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(fd)
	buf := make([]byte, 100)
	if n, err := p.Pread(fd, buf, 0); err != nil || n != 100 || !bytes.Equal(buf, data[:100]) {
		t.Fatalf("first read: %d %v", n, err)
	}
	before := s.nreads()
	if n, err := p.Pread(fd, buf, 100); err != nil || n != 100 || s.nreads() != before {
		t.Fatalf("second read: %d %v, %d Treads", n, err, s.nreads()-before)
	}
	if _, err := p.Pwrite(fd, []byte("XYZ"), 10); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Pread(fd, buf, 0); err != nil || n != 100 || string(buf[10:13]) != "XYZ" {
		t.Fatalf("read after write: %d %v %q", n, err, buf[:20])
	}

	// A change the mount did not make is seen once a stat shows the new
	// version.
	s.change(bytes.Repeat([]byte("Q"), 200))
	if _, err := p.Fstat(fd); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Pread(fd, buf, 0); err != nil || n != 100 || buf[0] != 'Q' {
		t.Fatalf("read after a new version: %d %v %q", n, err, buf[:10])
	}
	// A wstat drops the block too.
	if n, err := p.Pread(fd, buf, 100); err != nil || n != 100 {
		t.Fatalf("read: %d %v", n, err)
	}
	before = s.nreads()
	if err := p.Fwstat(fd, &styx.Dir{Mode: ^uint32(0), Length: ^uint64(0), Atime: ^uint32(0), Mtime: ^uint32(0), Type: ^uint16(0), Dev: ^uint32(0), Qid: styx.Qid{Type: ^uint8(0), Vers: ^uint32(0), Path: ^uint64(0)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pread(fd, buf, 100); err != nil || s.nreads() != before+1 {
		t.Fatalf("read after wstat: %v, %d Treads", err, s.nreads()-before)
	}
}
