// Package codemem is the kernel's executable memory for wazero's
// compiler: memory that is written, then run, with no mapping ever both
// writable and executable.
//
// TamaGo maps all RAM flat (virtual address = physical address) with
// 2 MiB blocks, writable and never executable outside the kernel's text.
// The region takes its memory from the Go heap in arenas of whole 2 MiB
// blocks, which it owns and keeps for good. Each block of an arena is
// remapped with a table of 4 KiB pages, so pages can change alone:
//
//   - Map hands out pages as they are, writable and not executable;
//   - Protect cleans the data cache to the point of unification over
//     them (DC CVAU), remaps them read-only and executable, invalidates
//     their TLB entries on every CPU (TLBI VAAE1IS) and the instruction
//     cache over them (IC IVAU, broadcast), and ends with DSB and ISB;
//     then every other CPU synchronizes its context (the sync function
//     given to New) before Protect returns and the code can run;
//   - Unmap makes them writable and not executable again and frees them.
//
// The pages' only mapping is the flat one, so there is no writable
// alias of code that runs. The page tables stay when pages are freed.
package codemem

import (
	"errors"
	"sync"
	"time"
	"unsafe"
)

const (
	pageSize  = 4096    // TamaGo's translation granule
	blockSize = 2 << 20 // one level 2 entry
	pagesPer  = blockSize / pageSize

	// arenaMin is the least the region takes from the heap at a time.
	arenaMin = 32 << 20
	// Max bounds the region: when it is reached, Map fails and the
	// module is not compiled.
	Max = 768 << 20

	// Descriptor bits (Arm ARM D8.3).
	descValid = 1 << 0
	descTable = 1 << 1 // with valid: a table (levels 1, 2) or a page (level 3)
	apRO      = 1 << 7 // AP[2]: read-only
	pxn       = 1 << 53
	uxn       = 1 << 54
	addrMask  = 0x0000_ffff_ffff_f000
)

// defined in codemem_arm64.s
func ttbr0() uint64
func ctr() uint64
func cleanDcache(start, end, line uintptr)
func invalidateIcache(start, end, line uintptr)
func tlbiPage(va uintptr)
func barrier()

var (
	errFull   = errors.New("code memory full")
	errNotOur = errors.New("not code memory")
	errLayout = errors.New("code memory: RAM is not mapped by level 2 tables")
)

// arena is a run of whole blocks with a page map.
type arena struct {
	mem   []byte // from the heap, kept for good
	base  uintptr
	pages int
	used  []bool
}

// Region implements wazero's experimental.CodeMemory.
type Region struct {
	mu     sync.Mutex
	arenas []*arena
	total  int
	allocs map[uintptr]int // page counts by start
	tables [][]uint64      // level 3 tables, kept for good
	dline  uintptr
	iline  uintptr
	inUse  int
	sync   func()
	syncs  int
	synced time.Duration // spent in sync
}

// New returns an empty region. Protect calls sync, if it is not nil,
// after the maintenance has completed: it must return once every other
// CPU has executed a context synchronization event (an ISB or an
// exception) since it was called.
func New(sync func()) *Region {
	c := ctr()
	return &Region{
		sync:   sync,
		allocs: map[uintptr]int{},
		dline:  4 << ((c >> 16) & 0xf),
		iline:  4 << (c & 0xf),
	}
}

// Stats returns the bytes the region holds from the heap, the bytes
// handed out, the times Protect synchronized the other CPUs and the
// time that took.
func (r *Region) Stats() (held, used, syncs int, synced time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total, r.inUse * pageSize, r.syncs, r.synced
}

// Map implements experimental.CodeMemory.
func (r *Region) Map(size int) ([]byte, error) {
	if size <= 0 {
		return nil, errNotOur
	}
	n := (size + pageSize - 1) / pageSize
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.arenas {
		if p, ok := a.find(n); ok {
			return r.take(a, p, n, size), nil
		}
	}
	a, err := r.grow(n)
	if err != nil {
		return nil, err
	}
	p, _ := a.find(n)
	return r.take(a, p, n, size), nil
}

func (r *Region) take(a *arena, p, n, size int) []byte {
	for i := p; i < p+n; i++ {
		a.used[i] = true
	}
	start := a.base + uintptr(p)*pageSize
	r.allocs[start] = n
	r.inUse += n
	return unsafe.Slice((*byte)(unsafe.Add(nil, start)), n*pageSize)[:size:size]
}

// find returns the first run of n free pages.
func (a *arena) find(n int) (int, bool) {
	run := 0
	for i, u := range a.used {
		if u {
			run = 0
			continue
		}
		run++
		if run == n {
			return i - n + 1, true
		}
	}
	return 0, false
}

// grow takes a new arena from the heap, big enough for n pages, and
// gives each of its blocks a level 3 table.
func (r *Region) grow(n int) (*arena, error) {
	size := (n*pageSize + blockSize - 1) &^ (blockSize - 1)
	if size < arenaMin {
		size = arenaMin
	}
	if r.total+size > Max {
		return nil, errFull
	}
	mem := make([]byte, size+blockSize)
	base := (uintptr(unsafe.Pointer(&mem[0])) + blockSize - 1) &^ (blockSize - 1)
	for b := base; b < base+uintptr(size); b += blockSize {
		if err := r.split(b); err != nil {
			return nil, err
		}
	}
	a := &arena{mem: mem, base: base, pages: size / pageSize, used: make([]bool, size/pageSize)}
	r.arenas = append(r.arenas, a)
	r.total += size
	return a, nil
}

func entry(addr uint64) *uint64 { return (*uint64)(unsafe.Add(nil, uintptr(addr))) }

// l2 returns the level 2 entry that maps va. The kernel's tables start
// at level 1 (a 39-bit address space).
func l2(va uintptr) (*uint64, error) {
	l1 := entry(ttbr0()&addrMask + 8*uint64(va>>30))
	if *l1&(descValid|descTable) != descValid|descTable {
		return nil, errLayout
	}
	return entry(*l1&addrMask + 8*uint64((va>>21)&511)), nil
}

// l3 returns the level 3 entry of the page at va, in a block split.
func l3(va uintptr) *uint64 {
	e, _ := l2(va)
	return entry(*e&addrMask + 8*uint64((va>>12)&511))
}

// split replaces the block descriptor that maps the 2 MiB block at b
// with a table of pages with the same attributes. Nothing else uses the
// block, so it can be unmapped while the entry changes (break before
// make). A block split before keeps its table.
func (r *Region) split(b uintptr) error {
	e, err := l2(b)
	if err != nil {
		return err
	}
	switch *e & (descValid | descTable) {
	case descValid | descTable:
		return nil // split earlier, by us
	case descValid:
	default:
		return errLayout
	}
	t := make([]uint64, pagesPer)
	if uintptr(unsafe.Pointer(&t[0]))&(pageSize-1) != 0 {
		return errors.New("code memory: page table not aligned")
	}
	attrs := *e &^ addrMask
	for i := range t {
		t[i] = uint64(b) + uint64(i)*pageSize | attrs | descTable
	}
	r.tables = append(r.tables, t)
	barrier()
	*e = 0
	barrier()
	tlbiPage(b)
	barrier()
	*e = uint64(uintptr(unsafe.Pointer(&t[0]))) | descValid | descTable
	barrier()
	return nil
}

func (r *Region) lookup(code []byte) (uintptr, int, bool) {
	if len(code) == 0 {
		return 0, 0, false
	}
	start := uintptr(unsafe.Pointer(unsafe.SliceData(code)))
	n, ok := r.allocs[start]
	return start, n, ok
}

// Protect implements experimental.CodeMemory.
func (r *Region) Protect(code []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	start, n, ok := r.lookup(code)
	if !ok {
		return errNotOur
	}
	end := start + uintptr(n)*pageSize
	cleanDcache(start, end, r.dline)
	for va := start; va < end; va += pageSize {
		e := l3(va)
		*e = (*e | apRO) &^ (pxn | uxn)
	}
	r.flush(start, end)
	invalidateIcache(start, end, r.iline)
	if r.sync != nil {
		t := time.Now()
		r.sync()
		r.synced += time.Since(t)
		r.syncs++
	}
	return nil
}

// Unmap implements experimental.CodeMemory.
func (r *Region) Unmap(code []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	start, n, ok := r.lookup(code)
	if !ok {
		return errNotOur
	}
	end := start + uintptr(n)*pageSize
	for va := start; va < end; va += pageSize {
		e := l3(va)
		*e = (*e &^ apRO) | pxn | uxn
	}
	r.flush(start, end)
	delete(r.allocs, start)
	r.inUse -= n
	for _, a := range r.arenas {
		if start >= a.base && start < a.base+uintptr(a.pages)*pageSize {
			p := int((start - a.base) / pageSize)
			for i := p; i < p+n; i++ {
				a.used[i] = false
			}
		}
	}
	return nil
}

// flush makes changed page entries visible to every CPU.
func (r *Region) flush(start, end uintptr) {
	barrier()
	for va := start; va < end; va += pageSize {
		tlbiPage(va)
	}
	barrier()
}
