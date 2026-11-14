package core

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
)

// Memory and prog limits (docs/abi.md, "Memory and prog limits"). A prog
// holds the wasm memory of its instances: linear memory, and tables at
// TableElemBytes an element. Writing memlimit N to its ctl limits that,
// and the progs it starts afterwards get the same limit. A prog domain
// (#pdomain) can limit the memory its members hold between them and how
// many members it has. Inferno has no such limits; its pools are
// system-wide.

// Unlimited is no limit, in a prog's memlimit and a domain's limits.
const Unlimited = ^uint64(0)

// TableElemBytes is what one wasm table element is charged.
const TableElemBytes = 8

// DomainLimits are the limits of one prog domain.
type DomainLimits struct {
	Mem   uint64 // wasm memory its members hold between them
	Progs uint64 // members
}

// domainState is the limits shared by the prog table.
type domainState struct {
	mu     sync.Mutex // limits; held while a domain charge is checked and made
	limits map[int]DomainLimits
	// admit lets one spawn at a time into a domain with a prog limit, so
	// two cannot both take the last place.
	admit sync.Mutex
}

// Domains lists the prog domains p is in, outermost first, each named by
// the process group that founded it. The slice is not changed later.
func (p *Prog) Domains() []int { p.mu.Lock(); defer p.mu.Unlock(); return p.domains }

// InDomain reports whether p is in domain id.
func (p *Prog) InDomain(id int) bool { return slices.Contains(p.Domains(), id) }

// Domain is p's innermost domain, or 0 for none.
func (p *Prog) Domain() int {
	d := p.Domains()
	if len(d) == 0 {
		return 0
	}
	return d[len(d)-1]
}

// joinDomain puts p in domain id, inside the ones it is in, unless that is
// its innermost one already.
func (p *Prog) joinDomain(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.domains); n == 0 || p.domains[n-1] != id {
		p.domains = append(slices.Clip(p.domains), id)
	}
}

// FoundDomain founds a domain for p's process group, as attaching
// #pdomain does: every prog now in the group joins it. Progs started from
// a member later are members too. It returns the domain's id.
func (t *Progs) FoundDomain(p *Prog) int {
	g := p.Group()
	for _, pid := range t.Pids() {
		if q := t.Get(pid); q != nil && q.Group() == g {
			q.joinDomain(g)
		}
	}
	return g
}

// Memlimit is p's memory limit in bytes, Unlimited for none.
func (p *Prog) Memlimit() uint64 { return p.memlimit.Load() }

// SetMemlimit lowers p's memory limit to n bytes. Raising it is
// permission denied.
func (p *Prog) SetMemlimit(n uint64) error {
	for {
		old := p.memlimit.Load()
		if n > old {
			return Error(abi.Eperm)
		}
		if p.memlimit.CompareAndSwap(old, n) {
			return nil
		}
	}
}

// Mem is the wasm memory p holds, in bytes.
func (p *Prog) Mem() uint64 { return p.mem.Load() }

// ChargeMem asks whether p may hold n more bytes of wasm memory, and
// charges it if so: p must stay within its own limit and within the
// memory limit of every domain it is in. The engine calls it for
// memory.grow and for the memory and tables a module starts with.
func (p *Prog) ChargeMem(n uint64) bool {
	if n == 0 {
		return true
	}
	if l := p.Memlimit(); l != Unlimited && p.Mem()+n > l {
		return false
	}
	doms := p.Domains()
	if len(doms) == 0 {
		p.mem.Add(n)
		return true
	}
	t := p.Kernel.Progs
	t.dom.mu.Lock()
	defer t.dom.mu.Unlock()
	for _, d := range doms {
		if l, ok := t.dom.limits[d]; ok && l.Mem != Unlimited {
			if _, held := t.DomainUsage(d); held+n > l.Mem {
				return false
			}
		}
	}
	p.mem.Add(n)
	return true
}

// UnchargeMem returns n bytes charged by ChargeMem.
func (p *Prog) UnchargeMem(n uint64) { p.mem.Add(-n) }

// DomainLimits returns the limits of domain id.
func (t *Progs) DomainLimits(id int) DomainLimits {
	t.dom.mu.Lock()
	defer t.dom.mu.Unlock()
	if l, ok := t.dom.limits[id]; ok {
		return l
	}
	return DomainLimits{Mem: Unlimited, Progs: Unlimited}
}

// SetDomainLimits lowers the limits of domain id to l. Raising either is
// permission denied and changes nothing.
func (t *Progs) SetDomainLimits(id int, l DomainLimits) error {
	t.dom.mu.Lock()
	defer t.dom.mu.Unlock()
	cur, ok := t.dom.limits[id]
	if !ok {
		cur = DomainLimits{Mem: Unlimited, Progs: Unlimited}
	}
	if l.Mem > cur.Mem || l.Progs > cur.Progs {
		return Error(abi.Eperm)
	}
	if t.dom.limits == nil {
		t.dom.limits = map[int]DomainLimits{}
	}
	t.dom.limits[id] = l
	return nil
}

// DomainUsage is how many members domain id has and the wasm memory they
// hold between them.
func (t *Progs) DomainUsage(id int) (progs, mem uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.progs {
		if p.InDomain(id) {
			progs++
			mem += p.Mem()
		}
	}
	return progs, mem
}

// admit checks that a prog in domains may start another: every one of
// them with a prog limit has fewer members than that. It fails with no
// free procs otherwise. The returned function must be called once the new
// prog is in the table.
func (t *Progs) admit(domains []int) (func(), error) {
	var limited []int
	var max []uint64
	t.dom.mu.Lock()
	for _, d := range domains {
		if l, ok := t.dom.limits[d]; ok && l.Progs != Unlimited {
			limited, max = append(limited, d), append(max, l.Progs)
		}
	}
	t.dom.mu.Unlock()
	if len(limited) == 0 {
		return func() {}, nil
	}
	t.dom.admit.Lock()
	for i, d := range limited {
		if n, _ := t.DomainUsage(d); n >= max[i] {
			t.dom.admit.Unlock()
			return nil, Error(abi.Enoproc)
		}
	}
	return t.dom.admit.Unlock, nil
}

// ParseSize reads a count of bytes or progs: digits, then k, m or g
// (either case) for 2^10, 2^20 or 2^30 times as many.
func ParseSize(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	shift := 0
	switch s[len(s)-1] {
	case 'k', 'K':
		shift = 10
	case 'm', 'M':
		shift = 20
	case 'g', 'G':
		shift = 30
	}
	if shift != 0 {
		s = s[:len(s)-1]
	}
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > Unlimited>>shift {
		return 0, false
	}
	return n << shift, true
}

// limitCtl applies memlimit N, domain memlimit N or domain proglimit N,
// written to p's ctl.
func (t *Progs) limitCtl(p *Prog, f []string) error {
	domain := f[0] == "domain"
	if domain {
		f = f[1:]
	}
	if len(f) != 2 {
		return Error(abi.Ebadarg)
	}
	n, ok := ParseSize(f[1])
	if !ok {
		return Error(abi.Ebadarg)
	}
	switch {
	case !domain && f[0] == "memlimit":
		return p.SetMemlimit(n)
	case domain && (f[0] == "memlimit" || f[0] == "proglimit"):
		d := p.Domain()
		if d == 0 {
			return Error(abi.Ebadarg)
		}
		l := t.DomainLimits(d)
		if f[0] == "memlimit" {
			l.Mem = n
		} else {
			l.Progs = n
		}
		return t.SetDomainLimits(d, l)
	}
	return Error(abi.Ebadarg)
}
