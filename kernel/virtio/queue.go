package virtio

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Descriptor flags.
const (
	descNext  = 1
	descWrite = 2
)

// Buf is one buffer of a request: Write means the device writes it.
type Buf struct {
	B     []byte
	Write bool
}

type request struct {
	bufs []Buf // kept reachable while the device owns them
	done func(n uint32)
}

// Queue is a split virtqueue.
type Queue struct {
	d     *Device
	index int
	n     uint16
	mem   []byte
	desc  []byte
	avail []byte
	used  []byte

	mu       sync.Mutex
	space    *sync.Cond
	free     []uint16
	availIdx uint16
	lastUsed uint16
	pending  map[uint16]*request
	wake     chan struct{}
}

func addr(b []byte) uint64 { return uint64(uintptr(unsafe.Pointer(unsafe.SliceData(b)))) }

// NewQueue sets up queue index of d with at most size entries. It is
// called between Init and Ready.
func (d *Device) NewQueue(index, size int) (*Queue, error) {
	d.write(regQueueSel, uint32(index))
	if d.read(regQueueReady) != 0 {
		return nil, errors.New("virtio: queue in use")
	}
	max := int(d.read(regQueueNumMax))
	if max == 0 {
		return nil, errors.New("virtio: no such queue")
	}
	size = min(size, max)
	n := uint16(size)
	descSize, availSize, usedSize := 16*size, 6+2*size, 6+8*size
	// Each part is placed on a 4 KiB boundary of one 4 KiB-aligned
	// allocation; the spec needs only 16, 2 and 4 bytes.
	const page = 4096
	round := func(x int) int { return (x + page - 1) &^ (page - 1) }
	total := round(descSize) + round(availSize) + round(usedSize)
	raw := make([]byte, total+page)
	off := int((page - addr(raw)%page) % page)
	mem := raw[off : off+total]
	q := &Queue{d: d, index: index, n: n, mem: raw,
		desc:    mem[:descSize],
		avail:   mem[round(descSize) : round(descSize)+availSize],
		used:    mem[round(descSize)+round(availSize):][:usedSize],
		pending: map[uint16]*request{},
		wake:    make(chan struct{}, 1),
	}
	q.space = sync.NewCond(&q.mu)
	for i := n; i > 0; i-- {
		q.free = append(q.free, i-1)
	}
	d.write(regQueueNum, uint32(n))
	set := func(lo uintptr, a uint64) {
		d.write(lo, uint32(a))
		d.write(lo+4, uint32(a>>32))
	}
	set(regQueueDescLow, addr(q.desc))
	set(regQueueDrvLow, addr(q.avail))
	set(regQueueDevLow, addr(q.used))
	d.write(regQueueReady, 1)
	d.queues = append(d.queues, q)
	go q.reaper()
	return q, nil
}

// Size is the number of descriptors.
func (q *Queue) Size() int { return int(q.n) }

// Submit gives the device a request of chained buffers; done is called
// with the number of bytes the device wrote when it hands them back. It
// waits while the queue is full.
func (q *Queue) Submit(bufs []Buf, done func(n uint32)) {
	if len(bufs) == 0 || len(bufs) > int(q.n) {
		panic("virtio: bad request")
	}
	q.mu.Lock()
	for len(q.free) < len(bufs) {
		q.space.Wait()
	}
	var head, prev uint16
	for i, b := range bufs {
		id := q.free[len(q.free)-1]
		q.free = q.free[:len(q.free)-1]
		if i == 0 {
			head = id
		} else {
			binary.LittleEndian.PutUint16(q.desc[16*int(prev)+12:], descNext|flagsOf(bufs[i-1]))
			binary.LittleEndian.PutUint16(q.desc[16*int(prev)+14:], id)
		}
		e := q.desc[16*int(id):]
		binary.LittleEndian.PutUint64(e[0:], addr(b.B))
		binary.LittleEndian.PutUint32(e[8:], uint32(len(b.B)))
		binary.LittleEndian.PutUint16(e[12:], flagsOf(b))
		binary.LittleEndian.PutUint16(e[14:], 0)
		prev = id
	}
	q.pending[head] = &request{bufs: bufs, done: done}
	binary.LittleEndian.PutUint16(q.avail[4+2*int(q.availIdx%q.n):], head)
	q.availIdx++
	// flags (0: interrupts wanted) and idx in one release store.
	atomic.StoreUint32((*uint32)(unsafe.Pointer(&q.avail[0])), uint32(q.availIdx)<<16)
	q.mu.Unlock()
	q.d.write(regQueueNotify, uint32(q.index))
}

func flagsOf(b Buf) uint16 {
	if b.Write {
		return descWrite
	}
	return 0
}

// Do submits a request and waits for it, returning the bytes written.
func (q *Queue) Do(bufs ...Buf) uint32 {
	c := make(chan uint32, 1)
	q.Submit(bufs, func(n uint32) { c <- n })
	return <-c
}

func (q *Queue) kick() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// reaper hands used buffers back to their submitters. It is woken by the
// device's interrupt and, as a safeguard against a lost one, polls while
// requests are outstanding.
func (q *Queue) reaper() {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-q.wake:
		case <-t.C:
			q.mu.Lock()
			idle := len(q.pending) == 0
			q.mu.Unlock()
			if idle {
				continue
			}
		}
		q.reap()
	}
}

func (q *Queue) reap() {
	var done []func()
	q.mu.Lock()
	for {
		usedIdx := uint16(atomic.LoadUint32((*uint32)(unsafe.Pointer(&q.used[0]))) >> 16)
		if usedIdx == q.lastUsed {
			break
		}
		e := q.used[4+8*int(q.lastUsed%q.n):]
		id := uint16(binary.LittleEndian.Uint32(e[0:]))
		n := binary.LittleEndian.Uint32(e[4:])
		q.lastUsed++
		for d := id; ; {
			q.free = append(q.free, d)
			f := binary.LittleEndian.Uint16(q.desc[16*int(d)+12:])
			if f&descNext == 0 {
				break
			}
			d = binary.LittleEndian.Uint16(q.desc[16*int(d)+14:])
		}
		if r := q.pending[id]; r != nil {
			delete(q.pending, id)
			if r.done != nil {
				done = append(done, func() { r.done(n) })
			}
		}
	}
	q.space.Broadcast()
	q.mu.Unlock()
	for _, f := range done {
		f()
	}
}
