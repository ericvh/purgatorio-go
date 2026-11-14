package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// #P, the profiling device (Inferno's devprof). Inferno samples the pc
// of the running Dis prog; a wasm guest has no pc the host can read, so
// the engine builds a profiled module with a hook on every function
// entry and exit, and the sampler reads the function each profiled
// guest is in. A pc here is a wasm function index (imports first) and a
// histogram counts samples, or calls when coverage profiling, per
// function.

const (
	pfdir = iota
	pfname
	pfpath
	pfhist
	pfpctl
	pfctl
)

var proftab = []Tab{
	{"name", pfname, 0o444, 0},
	{"path", pfpath, 0o444, 0},
	{"histogram", pfhist, 0o444, 0},
	{"pctl", pfpctl, 0o222, 0},
}

const (
	pnil = iota // not profiling
	psam        // sampling
	pcov        // coverage: every function call counted
)

// Profile is the profiler's state, shared by every prog.
type Profile struct {
	mu       sync.Mutex
	profiler int
	interval int // ms
	pmods    []string
	recs     []*ProfRecord // newest first
	ids      int
	time     int64
	covpids  map[int]bool
	live     map[*ProfInstance]bool
	sampling bool
}

// ProfRecord is the profile of one module.
type ProfRecord struct {
	id     int
	name   string
	path   string
	key    [32]byte
	bucket map[uint32]uint64
}

// ProfInstance is one profiled guest instance. Enter and Leave are
// called on the guest's own goroutine.
type ProfInstance struct {
	pr     *Profile
	rec    *ProfRecord
	cov    bool
	stack  []uint32
	cur    atomic.Int64 // the function the guest is in, or -1
	inhost atomic.Int32
}

// NewProfile returns an idle profiler.
func NewProfile() *Profile {
	return &Profile{interval: 100, covpids: map[int]bool{}, live: map[*ProfInstance]bool{}}
}

// Want is called as a module with the given code starts in prog p. It
// returns the instance to report to when the module is to be profiled,
// or nil.
func (pr *Profile) Want(p *Prog, name, path string, code []byte) *ProfInstance {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	cov := false
	switch pr.profiler {
	case psam:
	case pcov:
		for q := p; q != nil && !cov; q = q.parentProg() {
			cov = pr.covpids[q.Pid]
		}
		if !cov {
			return nil
		}
	default:
		return nil
	}
	r := pr.mlook(name, path, sha256.Sum256(code))
	if r == nil {
		return nil
	}
	pi := &ProfInstance{pr: pr, rec: r, cov: cov}
	pi.cur.Store(-1)
	pr.live[pi] = true
	return pi
}

// mlook finds or makes the record for a module, if it is wanted.
// Called with pr.mu held.
func (pr *Profile) mlook(name, path string, key [32]byte) *ProfRecord {
	for _, r := range pr.recs {
		if r.key == key && r.name == name && r.path == path {
			return r
		}
	}
	if len(pr.pmods) > 0 && !slices.Contains(pr.pmods, name) && !slices.Contains(pr.pmods, path) {
		return nil
	}
	pr.ids++
	r := &ProfRecord{id: pr.ids, name: name, path: path, key: key, bucket: map[uint32]uint64{}}
	if pr.ids == 1<<8-1 {
		pr.ids = 0
	}
	pr.recs = append([]*ProfRecord{r}, pr.recs...)
	return r
}

func (p *Prog) parentProg() *Prog { p.mu.Lock(); defer p.mu.Unlock(); return p.parent }

// Enter records a call of function fn.
func (pi *ProfInstance) Enter(fn uint32) {
	pi.stack = append(pi.stack, fn)
	pi.cur.Store(int64(fn))
	if pi.cov {
		pi.pr.mu.Lock()
		if pi.pr.profiler == pcov {
			pi.rec.bucket[fn]++
		}
		pi.pr.mu.Unlock()
	}
}

// Leave records the return of the innermost function.
func (pi *ProfInstance) Leave() {
	n := len(pi.stack)
	if n == 0 {
		return
	}
	pi.stack = pi.stack[:n-1]
	if n == 1 {
		pi.cur.Store(-1)
	} else {
		pi.cur.Store(int64(pi.stack[n-2]))
	}
}

// Host marks the guest as in (true) or out of a blocking host call,
// where it is not sampled.
func (pi *ProfInstance) Host(in bool) {
	if in {
		pi.inhost.Add(1)
	} else {
		pi.inhost.Add(-1)
	}
}

// Done ends the instance.
func (pi *ProfInstance) Done() {
	pi.pr.mu.Lock()
	delete(pi.pr.live, pi)
	pi.pr.mu.Unlock()
}

// sampler counts, every interval, the function each running profiled
// guest is in, as Inferno's sampler counts the running prog's pc.
func (pr *Profile) sampler() {
	for {
		pr.mu.Lock()
		iv := pr.interval
		pr.mu.Unlock()
		time.Sleep(time.Duration(max(iv, 1)) * time.Millisecond)
		pr.mu.Lock()
		if pr.profiler != psam {
			pr.sampling = false
			pr.mu.Unlock()
			return
		}
		pr.time += int64(iv)
		for pi := range pr.live {
			if fn := pi.cur.Load(); fn >= 0 && pi.inhost.Load() == 0 {
				pi.rec.bucket[uint32(fn)]++
			}
		}
		pr.mu.Unlock()
	}
}

func (pr *Profile) getrec(id int) *ProfRecord {
	for _, r := range pr.recs {
		if r.id == id {
			return r
		}
	}
	return nil
}

// DevProf is #P.
type DevProf struct {
	DevBase
	k  *Kernel
	pr *Profile
}

// NewDevProf makes #P and gives the kernel its profiler.
func NewDevProf(k *Kernel) *DevProf {
	if k.Prof == nil {
		k.Prof = NewProfile()
	}
	return &DevProf{k: k, pr: k.Prof}
}

func (d *DevProf) DC() rune     { return 'P' }
func (d *DevProf) Name() string { return "prof" }

const pshift = 4

func pqid(q styx.Qid) uint64 { return q.Path & (1<<pshift - 1) }
func precid(q styx.Qid) int  { return int(q.Vers) }

func (d *DevProf) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: pfdir}}, nil
}

func (d *DevProf) top(c *Chan) bool { return c.Qid.Path == pfdir && c.IsDir() }

func (d *DevProf) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	if !c.IsDir() {
		return Walked{}, Error(abi.Enotdir)
	}
	if name == ".." {
		return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: pfdir}}, nil
	}
	if d.top(c) {
		if name == "ctl" {
			return Walked{Qid: styx.Qid{Path: pfctl}}, nil
		}
		d.pr.mu.Lock()
		defer d.pr.mu.Unlock()
		for _, r := range d.pr.recs {
			if fmt.Sprintf("%.8x", r.id) == name {
				return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: uint64(r.id) << pshift, Vers: uint32(r.id)}}, nil
			}
		}
		return Walked{}, Error(abi.Enonexist)
	}
	d.pr.mu.Lock()
	r := d.pr.getrec(precid(c.Qid))
	d.pr.mu.Unlock()
	if r == nil {
		return Walked{}, Error(abi.Enonexist)
	}
	w, err := TabWalk(proftab, name)
	if err != nil {
		return w, err
	}
	w.Qid.Path |= c.Qid.Path &^ (1<<pshift - 1)
	w.Qid.Vers = c.Qid.Vers
	return w, nil
}

func (d *DevProf) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	eve := d.k.Owner
	if d.top(c) {
		return styx.Dir{Type: 'P', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: "#P", UID: eve, GID: eve}, nil
	}
	if pqid(c.Qid) == pfctl {
		return styx.Dir{Type: 'P', Qid: c.Qid, Mode: 0o222, Name: "ctl", UID: eve, GID: eve}, nil
	}
	d.pr.mu.Lock()
	r := d.pr.getrec(precid(c.Qid))
	d.pr.mu.Unlock()
	if r == nil {
		return styx.Dir{}, Error(abi.Enonexist)
	}
	if c.IsDir() {
		return styx.Dir{Type: 'P', Qid: c.Qid, Mode: styx.DMDIR | 0o555, Name: fmt.Sprintf("%.8x", r.id),
			UID: eve, GID: eve}, nil
	}
	for _, t := range proftab {
		if t.Path == pqid(c.Qid) {
			dir := TabDir('P', t, eve)
			dir.Qid = c.Qid
			return dir, nil
		}
	}
	return styx.Dir{}, Error(abi.Enonexist)
}

func (d *DevProf) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	eve := d.k.Owner
	if d.top(c) {
		out := []styx.Dir{{Type: 'P', Qid: styx.Qid{Path: pfctl}, Mode: 0o222, Name: "ctl", UID: eve, GID: eve}}
		d.pr.mu.Lock()
		for _, r := range d.pr.recs {
			out = append(out, styx.Dir{Type: 'P', Mode: styx.DMDIR | 0o555, Name: fmt.Sprintf("%.8x", r.id),
				Qid: styx.Qid{Type: styx.QTDIR, Path: uint64(r.id) << pshift, Vers: uint32(r.id)}, UID: eve, GID: eve})
		}
		d.pr.mu.Unlock()
		return out, nil
	}
	d.pr.mu.Lock()
	r := d.pr.getrec(precid(c.Qid))
	d.pr.mu.Unlock()
	if r == nil {
		return nil, Error(abi.Enonexist)
	}
	var out []styx.Dir
	for _, t := range proftab {
		dir := TabDir('P', t, eve)
		dir.Qid.Path |= c.Qid.Path &^ (1<<pshift - 1)
		dir.Qid.Vers = c.Qid.Vers
		out = append(out, dir)
	}
	return out, nil
}

// histpos is an open histogram's position: the next function index.
type histpos struct{ next uint32 }

func (d *DevProf) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, Error(abi.Eisdir)
		}
		return c, nil
	}
	if mode&abi.OTRUNC != 0 {
		return nil, Error(abi.Eperm)
	}
	q := pqid(c.Qid)
	if q == pfctl || q == pfpctl {
		if mode&abi.OACCESS != abi.OWRITE {
			return nil, Error(abi.Eperm)
		}
	} else if mode&abi.OACCESS != abi.OREAD {
		return nil, Error(abi.Eperm)
	}
	if q != pfctl {
		d.pr.mu.Lock()
		r := d.pr.getrec(precid(c.Qid))
		d.pr.mu.Unlock()
		if r == nil {
			return nil, Error(abi.Ethread)
		}
	}
	if q == pfhist {
		c.State = &histpos{}
	}
	return c, nil
}

func (d *DevProf) Read(_ context.Context, c *Chan, buf []byte, off int64) (int, error) {
	if pqid(c.Qid) == pfctl {
		return 0, Error(abi.Eperm)
	}
	d.pr.mu.Lock()
	defer d.pr.mu.Unlock()
	r := d.pr.getrec(precid(c.Qid))
	if r == nil {
		return 0, Error(abi.Ethread)
	}
	switch pqid(c.Qid) {
	case pfname:
		return ReadStr([]byte(r.name), buf, off), nil
	case pfpath:
		return ReadStr([]byte(r.path), buf, off), nil
	case pfhist:
		// Each read returns the next pc with a non-zero count.
		h := c.State.(*histpos)
		var next uint32
		found := false
		for fn, n := range r.bucket {
			if n != 0 && fn >= h.next && (!found || fn < next) {
				next, found = fn, true
			}
		}
		if !found {
			return 0, nil
		}
		if len(buf) < 20 {
			return 0, Error(abi.Etoosmall)
		}
		h.next = next + 1
		return copy(buf, fmt.Sprintf("%d %d", next, r.bucket[next])), nil
	}
	return 0, nil
}

func (d *DevProf) Write(_ context.Context, c *Chan, buf []byte, _ int64) (int, error) {
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	if pqid(c.Qid) != pfctl {
		return 0, Error(abi.Eperm)
	}
	n := len(buf)
	if len(buf) > 127 {
		buf = buf[:127]
	}
	f := strings.FieldsFunc(string(buf), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	pr := d.pr
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(f) > 0 && f[0] == "module" {
		pr.pmods = append([]string(nil), f[1:]...)
		return n, nil
	}
	switch {
	case len(f) == 1 && f[0] == "start":
		if pr.profiler == pnil {
			pr.profiler = psam
			if !pr.sampling {
				pr.sampling = true
				go pr.sampler()
			}
		}
	case len(f) == 1 && strings.HasPrefix(f[0], "startmp"):
		// Memory profiling needs the guest's allocator; wasm guests
		// keep their heaps to themselves.
		return 0, Error(abi.Ebadarg)
	case len(f) == 1 && f[0] == "stop":
		pr.profiler = pnil
	case len(f) == 1 && f[0] == "end":
		pr.profiler = pnil
		pr.ids = 0
		pr.pmods = nil
		pr.recs = nil
		pr.time = 0
		pr.covpids = map[int]bool{}
		pr.interval = 100
	case len(f) == 2 && f[0] == "interval":
		v, _ := cstrtoul(f[1])
		pr.interval = int(v)
	case len(f) == 2 && f[0] == "startcp":
		v, _ := cstrtoul(f[1])
		p := d.k.Progs.Get(int(v))
		if p == nil {
			return 0, Error(abi.Ethread)
		}
		if pr.profiler == pnil {
			pr.profiler = pcov
			pr.covpids[p.Pid] = true
		}
	default:
		return 0, Error(abi.Ebadarg)
	}
	return n, nil
}
