//go:build wasip1

package sys

import (
	"encoding/binary"
	"unsafe"
)

//go:wasmimport purgatorio mount
func mount(fd, afd int32, o unsafe.Pointer, ol uint32, flags int32, a unsafe.Pointer, al uint32) int32

//go:wasmimport purgatorio fauth
func fauth(fd int32, a unsafe.Pointer, al uint32) int32

//go:wasmimport purgatorio fversion
func fversion(fd int32, msize uint32, v unsafe.Pointer, vl uint32, buf unsafe.Pointer, bl uint32) int32

//go:wasmimport purgatorio export
func export(fd int32, d unsafe.Pointer, dl uint32, flags int32) int32

//go:wasmimport purgatorio file2chan
func file2chan(d unsafe.Pointer, dl uint32, n unsafe.Pointer, nl uint32, hs unsafe.Pointer) int32

//go:wasmimport purgatorio chan_new
func chanNew(capacity int32) int32

//go:wasmimport purgatorio chan_close
func chanClose(h int32) int32

//go:wasmimport purgatorio chan_send
func chanSend(h int32, b unsafe.Pointer, n uint32, caps unsafe.Pointer, nc uint32) int32

//go:wasmimport purgatorio chan_recv
func chanRecv(h int32, b unsafe.Pointer, bl uint32, caps unsafe.Pointer, maxc uint32, nc unsafe.Pointer) int32

//go:wasmimport purgatorio alt
func alt(ops unsafe.Pointer, n uint32, block int32) int32

//go:wasmimport purgatorio wait
func wait(ops unsafe.Pointer, n uint32, timeout int32) int32

// Mount is Sys->mount: attach to the 9P server on fd at old. afd is -1
// unless Fauth authenticated first.
func Mount(fd, afd int, old string, flags int, aname string) error {
	op, ol := sp(old)
	ap, al := sp(aname)
	return check(mount(int32(fd), int32(afd), op, ol, int32(flags), ap, al))
}

// Fauth starts authentication on the conversation on fd.
func Fauth(fd int, aname string) (int, error) {
	ap, al := sp(aname)
	r := fauth(int32(fd), ap, al)
	return int(r), check(r)
}

// Fversion starts the 9P conversation on fd and returns the version.
func Fversion(fd, msize int, version string) (string, error) {
	vp, vl := sp(version)
	buf := make([]byte, 64)
	n := fversion(int32(fd), uint32(msize), vp, vl, bp(buf), uint32(len(buf)))
	if n < 0 {
		return "", lastErr()
	}
	return string(buf[:min(int(n), len(buf))]), nil
}

// Export modes for Export.
const (
	EXPWAIT  = 0
	EXPASYNC = 1
)

// Export serves the name space below dir on fd, until the client hangs
// up or, with EXPASYNC, in the background.
func Export(fd int, dir string, flag int) error {
	dp, dl := sp(dir)
	return check(export(int32(fd), dp, dl, int32(flag)))
}

// Chan is a kernel channel handle.
type Chan int

// NewChan makes a kernel channel; capacity 0 is synchronous.
func NewChan(capacity int) Chan { return Chan(chanNew(int32(capacity))) }

func (c Chan) Close() error { return check(chanClose(int32(c))) }

// Send sends b and the given channels, which move to the receiver.
func (c Chan) Send(b []byte, caps ...Chan) error {
	hs := make([]int32, len(caps))
	for i, x := range caps {
		hs[i] = int32(x)
	}
	return check(chanSend(int32(c), bp(b), uint32(len(b)), ip(hs), uint32(len(hs))))
}

// Recv receives a message of at most max bytes and its channels.
func (c Chan) Recv(max int) ([]byte, []Chan, error) {
	buf := make([]byte, max)
	hs := make([]int32, 8)
	var nc uint32
	n := chanRecv(int32(c), bp(buf), uint32(len(buf)), ip(hs), uint32(len(hs)), unsafe.Pointer(&nc))
	if n < 0 {
		return nil, nil, lastErr()
	}
	caps := make([]Chan, nc)
	for i := range caps {
		caps[i] = Chan(hs[i])
	}
	return buf[:n], caps, nil
}

// AltOp is one arm of Alt.
type AltOp struct {
	C    Chan
	Send bool
	Data []byte // to send, or the buffer to receive into
	Caps []Chan // to send
	// Set by Alt on the arm that completed, for a receive.
	Got    []byte
	GotCap []Chan
	Closed bool
}

// Alt does the first arm that can proceed, as Limbo's alt, and returns its
// index; with block false, -1 when none can.
func Alt(ops []AltOp, block bool) (int, error) {
	raw := make([]int32, 6*len(ops))
	caps := make([][]int32, len(ops))
	for i := range ops {
		op := &ops[i]
		raw[6*i] = int32(op.C)
		if op.Send {
			raw[6*i+1] = 1
			for _, c := range op.Caps {
				caps[i] = append(caps[i], int32(c))
			}
		} else {
			if op.Data == nil {
				op.Data = make([]byte, 8192)
			}
			caps[i] = make([]int32, 8)
		}
		raw[6*i+2] = int32(uintptr(bp(op.Data)))
		raw[6*i+3] = int32(len(op.Data))
		raw[6*i+4] = int32(uintptr(ip(caps[i])))
		raw[6*i+5] = int32(len(caps[i]))
	}
	b := 0
	if block {
		b = 1
	}
	idx := alt(ip(raw), uint32(len(ops)), int32(b))
	if idx < 0 {
		if block {
			return -1, lastErr()
		}
		return -1, nil
	}
	op := &ops[idx]
	if !op.Send {
		n := raw[6*idx+3]
		if n < 0 {
			op.Closed = true
		} else {
			op.Got = op.Data[:n]
			for _, h := range caps[idx][:raw[6*idx+5]] {
				op.GotCap = append(op.GotCap, Chan(h))
			}
		}
	}
	return int(idx), nil
}

// WaitOp kinds.
const (
	WaitRead     = 0
	WaitWrite    = 1
	WaitChanRecv = 2
	WaitChanSend = 3
)

// Wait waits up to timeout milliseconds (forever when negative) for the
// first ready op, given as (kind, fd or channel) pairs, and returns its
// index, or -1 on timeout.
func Wait(ops [][2]int, timeout int) (int, error) {
	raw := make([]int32, 2*len(ops))
	for i, o := range ops {
		raw[2*i], raw[2*i+1] = int32(o[0]), int32(o[1])
	}
	r := wait(ip(raw), uint32(len(ops)), int32(timeout))
	if r < -1 {
		return -1, lastErr()
	}
	return int(r), nil
}

// FileIO is what File2chan returns: the channels on which reads and
// writes of the file arrive.
type FileIO struct {
	Read, Write Chan
}

// File2chan makes file in dir (a directory on #s) and returns its
// channels.
func File2chan(dir, file string) (*FileIO, error) {
	dp, dl := sp(dir)
	np, nl := sp(file)
	var hs [2]int32
	if err := check(file2chan(dp, dl, np, nl, unsafe.Pointer(&hs[0]))); err != nil {
		return nil, err
	}
	return &FileIO{Chan(hs[0]), Chan(hs[1])}, nil
}

// ReadReq is a client's read of a file2chan file. A nil Reply means the
// client closed Fid.
type ReadReq struct {
	Off   int64
	Count int
	Fid   int
	reply Chan
	has   bool
}

// WriteReq is a client's write; a nil reply means Fid was closed.
type WriteReq struct {
	Off   int64
	Data  []byte
	Fid   int
	reply Chan
	has   bool
}

// Closed reports a close notice rather than a request.
func (r *ReadReq) Closed() bool  { return !r.has }
func (w *WriteReq) Closed() bool { return !w.has }

// Reply answers a read with data, or with an error when err is not "".
func (r *ReadReq) Reply(data []byte, err string) {
	if !r.has {
		return
	}
	var m []byte
	if err != "" {
		m = append([]byte{1}, err...)
	} else {
		m = append([]byte{0}, data...)
	}
	_ = r.reply.Send(m)
	_ = r.reply.Close()
}

// Reply answers a write with the count taken, or an error.
func (w *WriteReq) Reply(n int, err string) {
	if !w.has {
		return
	}
	var m []byte
	if err != "" {
		m = append([]byte{1}, err...)
	} else {
		m = binary.LittleEndian.AppendUint32([]byte{0}, uint32(n))
	}
	_ = w.reply.Send(m)
	_ = w.reply.Close()
}

// Next waits for the next read or write request and returns it as a
// *ReadReq or *WriteReq.
func (f *FileIO) Next() (any, error) {
	ops := []AltOp{{C: f.Read, Data: make([]byte, 16)}, {C: f.Write, Data: make([]byte, 64*1024+12)}}
	i, err := Alt(ops, true)
	if err != nil {
		return nil, err
	}
	op := ops[i]
	if op.Closed {
		return nil, Error("i/o on hungup channel")
	}
	b := op.Got
	if i == 0 {
		r := &ReadReq{Off: int64(binary.LittleEndian.Uint64(b)), Count: int(binary.LittleEndian.Uint32(b[8:])),
			Fid: int(binary.LittleEndian.Uint32(b[12:]))}
		if len(op.GotCap) > 0 {
			r.reply, r.has = op.GotCap[0], true
		}
		return r, nil
	}
	w := &WriteReq{Off: int64(binary.LittleEndian.Uint64(b)), Fid: int(binary.LittleEndian.Uint32(b[8:])),
		Data: append([]byte(nil), b[12:]...)}
	if len(op.GotCap) > 0 {
		w.reply, w.has = op.GotCap[0], true
	}
	return w, nil
}

//go:wasmimport purgatorio detach
func detach() int32

// Detach lets the program's caller go on as if it had exited, while it
// carries on in the background under a new pid: what a Limbo command does
// when init returns with spawned threads still running.
func Detach() int { return int(detach()) }
