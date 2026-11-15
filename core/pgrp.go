package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

const (
	mrepl   = abi.MREPL
	mbefore = abi.MBEFORE
	mafter  = abi.MAFTER
	mcreate = abi.MCREATE
	morder  = abi.MORDER
)

// nodevsAllowed are the devices a NODEVS name space may still attach.
const nodevsAllowed = "es|M¤"

// mount is one entry of the mount table, Inferno's Mhead. It holds the
// Chan mounted on, so the file stays referenced as long as the mount does.
// Lookups match a Chan by Key, as findmount does.
type mount struct {
	from *Chan
	// to are the union members in search order. For a union made with
	// MBEFORE or MAFTER on a directory not yet mounted on, one member is
	// from itself.
	to []Member
}

func (m *mount) isOriginal(x Member) bool { return x.Chan == m.from }

// dot is the current directory: a held Chan, not only a name. A name in or
// below it is walked from this Chan, so it reaches the same files whatever
// has since been bound over the names that led here.
type dot struct {
	name string // the cleaned absolute name chdir was given
	c    *Chan  // nil for the root
	// mtpt is the key of the directory c was mounted on, when chdir
	// crossed a mount point to reach it.
	mtpt    Key
	hasMtpt bool
}

func rootDot() dot { return dot{name: "/"} }

// Pgrp is a name space: a root, a mount table and the current directory.
//
// A Pgrp is counted, as Inferno's is: each prog sharing it holds a
// reference, and the last Release lets go of every file it holds. The
// root, the current directory and each Chan in the mount table are held
// (Chan.hold) while the name space has them, so a mount's conversation
// ends when no name space or fd refers to it any more.
type Pgrp struct {
	devtab *DevTab
	root   *Chan
	refs   atomic.Int32

	mu     sync.RWMutex
	mounts []mount

	dotmu sync.Mutex
	dot   dot

	nodevs atomic.Bool
	// denied lists device characters this name space may not attach,
	// NODEVS or not.
	deniedmu sync.Mutex
	denied   string
}

// NewPgrp returns a name space rooted at root (usually #/).
func NewPgrp(devtab *DevTab, root *Chan) *Pgrp {
	root.Name = "/"
	p := &Pgrp{devtab: devtab, root: root, dot: rootDot()}
	p.refs.Store(1)
	root.hold()
	return p
}

// IncRef adds a reference: another prog shares the name space.
func (p *Pgrp) IncRef() *Pgrp {
	p.refs.Add(1)
	return p
}

// Release drops a reference. The last one empties the name space and
// lets go of what it held, as Inferno's closepgrp does.
func (p *Pgrp) Release() {
	if p.refs.Add(-1) != 0 {
		return
	}
	p.mu.Lock()
	mounts := p.mounts
	p.mounts = nil
	p.mu.Unlock()
	p.dotmu.Lock()
	d := p.dot
	p.dot = rootDot()
	p.dotmu.Unlock()
	for _, m := range mounts {
		m.unhold()
	}
	if d.c != nil {
		d.c.unhold()
	}
	p.root.unhold()
}

// hold and unhold count the mount entry's Chans.
func (m *mount) hold() {
	m.from.hold()
	for _, x := range m.to {
		x.Chan.hold()
	}
}

func (m *mount) unhold() {
	m.from.unhold()
	for _, x := range m.to {
		x.Chan.unhold()
	}
}

func (p *Pgrp) DevTab() *DevTab { return p.devtab }

// Dot is the current directory's name.
func (p *Pgrp) Dot() string {
	p.dotmu.Lock()
	defer p.dotmu.Unlock()
	return p.dot.name
}

// Fork returns a copy of this name space (FORKNS).
func (p *Pgrp) Fork() *Pgrp {
	p.mu.RLock()
	mounts := make([]mount, len(p.mounts))
	for i, m := range p.mounts {
		mounts[i] = mount{from: m.from, to: append([]Member(nil), m.to...)}
		mounts[i].hold()
	}
	p.mu.RUnlock()
	n := &Pgrp{devtab: p.devtab, root: p.root, mounts: mounts}
	n.refs.Store(1)
	n.root.hold()
	p.dotmu.Lock()
	n.dot = p.dot
	if n.dot.c != nil {
		n.dot.c.hold()
	}
	p.dotmu.Unlock()
	n.nodevs.Store(p.nodevs.Load())
	p.deniedmu.Lock()
	n.denied = p.denied
	p.deniedmu.Unlock()
	return n
}

// NewNS returns an empty name space rooted at this one's current
// directory (NEWNS).
func (p *Pgrp) NewNS(ctx context.Context) (*Pgrp, error) {
	root, err := p.Namec(ctx, ".", p.Dot())
	if err != nil {
		return nil, err
	}
	root.Union = nil
	n := NewPgrp(p.devtab, root)
	n.nodevs.Store(p.nodevs.Load())
	p.deniedmu.Lock()
	n.denied = p.denied
	p.deniedmu.Unlock()
	return n, nil
}

// Exited returns an empty name space on the same devices, for a prog that
// is leaving, so what it held is released.
func (p *Pgrp) Exited(ctx context.Context) *Pgrp {
	root, err := p.devtab.Attach(ctx, '/', "")
	if err != nil {
		root = p.root
	}
	n := NewPgrp(p.devtab, root)
	n.nodevs.Store(true)
	return n
}

// SetNodevs forbids attaching devices other than #e #s #| #M #¤.
func (p *Pgrp) SetNodevs() { p.nodevs.Store(true) }

// DenyDevs forbids attaching the given device characters from now on.
func (p *Pgrp) DenyDevs(devs string) {
	p.deniedmu.Lock()
	defer p.deniedmu.Unlock()
	for _, c := range devs {
		if !strings.ContainsRune(p.denied, c) {
			p.denied += string(c)
		}
	}
}

func (p *Pgrp) mayAttach(dc rune) bool {
	if p.nodevs.Load() && !strings.ContainsRune(nodevsAllowed, dc) {
		return false
	}
	p.deniedmu.Lock()
	defer p.deniedmu.Unlock()
	return !strings.ContainsRune(p.denied, dc)
}

func isRelative(path string) bool {
	return !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "#")
}

// Chdir makes path, which must be a directory, the current directory.
func (p *Pgrp) Chdir(ctx context.Context, path string) error {
	d := p.Dot()
	if isRelative(path) && Absolute(path, d) == d {
		return nil
	}
	under, err := p.resolve(ctx, path, d, false)
	if err != nil {
		return err
	}
	mtpt := under.Key()
	c := p.domount(under, true)
	if !c.IsDir() {
		return Error(abi.Enotdir)
	}
	mounted := c.Union != nil
	c.Union = nil
	nd := rootDot()
	if c.Name != "/" {
		nd = dot{name: c.Name, c: c, mtpt: mtpt, hasMtpt: mounted}
		c.hold()
	}
	p.dotmu.Lock()
	old := p.dot
	p.dot = nd
	p.dotmu.Unlock()
	if old.c != nil {
		old.c.unhold()
	}
	return nil
}

// startAtDot finds where to start walking full, a name given relative to
// dot, when it is in or below the current directory: that directory's
// held Chan, its name, and the rest of full.
func (p *Pgrp) startAtDot(full, d string) (*Chan, string, string, bool) {
	p.dotmu.Lock()
	held := p.dot
	p.dotmu.Unlock()
	if held.c == nil || held.name != d {
		return nil, "", "", false
	}
	rest, ok := strings.CutPrefix(full, held.name)
	if !ok {
		return nil, "", "", false
	}
	if rest != "" {
		if rest, ok = strings.CutPrefix(rest, "/"); !ok {
			return nil, "", "", false
		}
	}
	cur := held.c.CloneUnopened()
	cur.Union = nil
	if held.hasMtpt {
		p.mu.RLock()
		for _, m := range p.mounts {
			if m.from.Key() == held.mtpt && len(m.to) > 0 && m.to[0].Chan.Key() == cur.Key() {
				cur.Union = append([]Member(nil), m.to...)
				break
			}
		}
		p.mu.RUnlock()
	}
	return cur, held.name, rest, true
}

// Bind makes new visible at old. new is resolved through the mount table:
// when it is a mount point, what is mounted there is bound.
func (p *Pgrp) Bind(ctx context.Context, new, old string, flags uint32) error {
	to, err := p.Namec(ctx, new, p.Dot())
	if err != nil {
		return err
	}
	return p.BindChan(ctx, to, old, flags)
}

var mountID atomic.Uint64

func nextMountID() uint64 { return mountID.Add(1) }

// BindChan binds an existing Chan at old; mount uses it for a #M root. The
// checks are those of Inferno's bindmount and cmount.
func (p *Pgrp) BindChan(ctx context.Context, to *Chan, old string, flags uint32) error {
	if flags&^(morder|mcreate) != 0 || flags&morder == morder {
		return Error(abi.Ebadarg)
	}
	from, err := p.namecUnmounted(ctx, old)
	if err != nil {
		return err
	}
	if to.IsDir() != from.IsDir() {
		return Error(abi.Eismount)
	}
	if flags&morder != mrepl && !from.IsDir() {
		return Error(abi.Eismount)
	}
	rest := to.Union
	to.Union = nil
	if flags&mcreate != 0 && rest != nil && (len(rest) > 1 || (len(rest) == 1 && !rest[0].CanCreate())) {
		return Error(abi.Eismount)
	}
	add := []Member{{Chan: to, Flags: flags}}
	if rest != nil {
		order := uint32(mafter)
		if flags&morder == mbefore {
			order = mbefore
		}
		for _, m := range rest[1:] {
			add = append(add, Member{Chan: m.Chan, Flags: order})
		}
	}
	// What is added is held before what it replaces is let go, so a
	// conversation mounted again in its own place carries on.
	for _, x := range add {
		x.Chan.hold()
	}
	var dropped []Member
	p.mu.Lock()
	idx := -1
	for i := range p.mounts {
		if p.mounts[i].from.Key() == from.Key() {
			idx = i
			break
		}
	}
	if idx < 0 {
		var initial []Member
		if flags&morder != mrepl {
			initial = []Member{{Chan: from, ID: nextMountID()}}
			from.hold()
		}
		from.hold()
		p.mounts = append(p.mounts, mount{from: from, to: initial})
		idx = len(p.mounts) - 1
	}
	for i := range add {
		add[i].ID = nextMountID()
	}
	m := &p.mounts[idx]
	switch flags & morder {
	case mbefore:
		m.to = append(add, m.to...)
	case mafter:
		m.to = append(m.to, add...)
	default:
		dropped = m.to
		m.to = add
	}
	p.mu.Unlock()
	for _, x := range dropped {
		x.Chan.unhold()
	}
	return nil
}

// Unmount removes name from the union at old, or everything mounted there
// when name is empty.
func (p *Pgrp) Unmount(ctx context.Context, name, old string) error {
	from, err := p.namecUnmounted(ctx, old)
	if err != nil {
		return err
	}
	var targets []Key
	if name != "" {
		a, err := p.Namec(ctx, name, p.Dot())
		if err != nil {
			return err
		}
		b, err := p.namecUnmounted(ctx, name)
		if err != nil {
			return err
		}
		targets = []Key{a.Key(), b.Key()}
	}
	var gone []mount
	defer func() {
		// Let go of what was taken out once the table is unlocked: the
		// last reference to a mount's conversation ends it.
		for _, m := range gone {
			m.unhold()
		}
	}()
	p.mu.Lock()
	defer p.mu.Unlock()
	i := -1
	for j := range p.mounts {
		if p.mounts[j].from.Key() == from.Key() {
			i = j
			break
		}
	}
	if i < 0 {
		return Error(abi.Eunmount)
	}
	if targets == nil {
		gone = append(gone, p.mounts[i])
		p.mounts = append(p.mounts[:i:i], p.mounts[i+1:]...)
		return nil
	}
	m := &p.mounts[i]
	j := -1
	for _, k := range targets {
		for x, mem := range m.to {
			if mem.Chan.Key() == k {
				j = x
				break
			}
		}
		if j >= 0 {
			break
		}
	}
	if j < 0 {
		return Error(abi.Eunmount)
	}
	gone = append(gone, mount{from: nil, to: []Member{m.to[j]}})
	m.to = append(m.to[:j:j], m.to[j+1:]...)
	if len(m.to) == 0 || (len(m.to) == 1 && m.isOriginal(m.to[0])) {
		gone = append(gone, *m)
		p.mounts = append(p.mounts[:i:i], p.mounts[i+1:]...)
	}
	return nil
}

func (p *Pgrp) mountedKey(k Key) []Member {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.mounts {
		if m.from.Key() == k {
			return append([]Member(nil), m.to...)
		}
	}
	return nil
}

// Namec resolves a path to an unopened Chan, crossing mount points. dot
// is used for relative paths.
func (p *Pgrp) Namec(ctx context.Context, path, dot string) (*Chan, error) {
	return p.resolve(ctx, path, dot, true)
}

func (p *Pgrp) namecUnmounted(ctx context.Context, path string) (*Chan, error) {
	return p.resolve(ctx, path, p.Dot(), false)
}

// CreateTarget resolves the directory path would be created in and picks
// the union member to create in: the directory itself, or the first
// MCREATE member. It returns that directory and the new file's name.
func (p *Pgrp) CreateTarget(ctx context.Context, path, d string) (*Chan, string, error) {
	full := Absolute(path, d)
	i := strings.LastIndexByte(full, '/')
	if i < 0 {
		return nil, "", Error(abi.Efilename)
	}
	parent, name := full[:i], full[i+1:]
	if parent == "" {
		parent = "/"
	}
	_, rest, _ := SplitDevice(full)
	if name == "" || name == "." || name == ".." || rest == "" {
		return nil, "", Error(abi.Efilename)
	}
	dir, err := p.resolveFull(ctx, parent, d, true, isRelative(path))
	if err != nil {
		return nil, "", err
	}
	if !dir.IsDir() {
		return nil, "", Error(abi.Enotdir)
	}
	if dir.Union == nil {
		return dir, name, nil
	}
	for _, m := range dir.Union {
		if m.CanCreate() {
			c := m.Chan.CloneUnopened()
			c.Name = dir.Name
			c.Union = nil
			return c, name, nil
		}
	}
	return nil, "", Error(abi.Enocreate)
}

func (p *Pgrp) resolve(ctx context.Context, path, d string, crossLast bool) (*Chan, error) {
	if path == "" {
		return nil, Error(abi.Enonexist)
	}
	return p.resolveFull(ctx, Absolute(path, d), d, crossLast, isRelative(path))
}

// resolveFull resolves the cleaned absolute name full. When it was given
// relative to d and does not leave the current directory, the walk starts
// at the held Chan; otherwise at the root or the named device.
func (p *Pgrp) resolveFull(ctx context.Context, full, d string, crossLast, relative bool) (*Chan, error) {
	var (
		cur   *Chan
		name  string
		elems []string
	)
	held := false
	if relative {
		var rest string
		cur, name, rest, held = p.startAtDot(full, d)
		if held {
			for _, e := range strings.Split(rest, "/") {
				if e != "" {
					elems = append(elems, e)
				}
			}
		}
	}
	if !held {
		if dev, _, isdev := SplitDevice(full); isdev {
			r := []rune(dev[1:])
			if len(r) == 0 {
				return nil, Error(abi.Efilename)
			}
			dc, spec := r[0], string(r[1:])
			if dc == '*' {
				// #*dev!spec: indirection by device name (devindir.c).
				var err error
				if dc, spec, err = p.indir(spec); err != nil {
					return nil, err
				}
			} else if !p.mayAttach(dc) {
				return nil, Error(abi.Enoattach)
			}
			start, err := p.devtab.Attach(ctx, dc, spec)
			if err != nil {
				return nil, err
			}
			cur, name = start, dev
		} else {
			cur, name = p.root.CloneUnopened(), ""
		}
		elems = Elements(full)
		cur = p.domount(cur, crossLast || len(elems) > 0)
	}
	cur, name, err := p.walkElems(ctx, cur, name, elems, crossLast)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(full, "#") || !strings.HasPrefix(full, "/"):
		cur.Name = full
	case name == "":
		cur.Name = "/"
	default:
		cur.Name = name
	}
	return cur, nil
}

// Walk1 walks from one name further, as a name in a path is walked: in
// each member of a union, and across a mount point at the name. It is the
// walk export does for a client's fid.
func (p *Pgrp) Walk1(ctx context.Context, from *Chan, elem string) (*Chan, error) {
	base := from.Name
	if base == "/" {
		base = ""
	}
	c, name, err := p.walkElems(ctx, from.CloneUnopened(), base, []string{elem}, true)
	if err != nil {
		return nil, err
	}
	c.Name = name
	return c, nil
}

func (p *Pgrp) walkElems(ctx context.Context, cur *Chan, name string, elems []string, crossLast bool) (*Chan, string, error) {
	i := 0
	for i < len(elems) {
		cands := []*Chan{cur}
		if cur.Union != nil {
			cands = cands[:0]
			for _, m := range cur.Union {
				cands = append(cands, m.Chan)
			}
		}
		var (
			found *Chan
			w     WalkedNames
			n     int
		)
		var err error = Error(abi.Enonexist)
		for _, cand := range cands {
			if !cand.IsDir() {
				err = Error(abi.Enotdir)
				continue
			}
			k, wn, e := walkNames(ctx, cand, elems[i:])
			if e != nil {
				err = e
				continue
			}
			if len(wn.Qids) == 0 {
				err = Error(abi.Enonexist)
				continue
			}
			found, w, n = cand, wn, k
			break
		}
		if found == nil {
			return nil, "", err
		}
		walked := min(len(w.Qids), n)
		passed := walked
		if w.Reached {
			passed = walked - 1
		}
		crossed := -1
		var to []Member
		for j := 0; j < passed; j++ {
			if to = p.mountedKey(Key{found.Dev.DC(), found.Devno, w.Qids[j].Path}); to != nil {
				crossed = j
				break
			}
		}
		if crossed >= 0 {
			for _, e := range elems[i : i+crossed+1] {
				name += "/" + e
			}
			first := to[0].Chan.CloneUnopened()
			first.Name = name
			first.Union = to
			cur = first
			i += crossed + 1
			continue
		}
		if !w.Reached {
			if w.Why != nil {
				return nil, "", w.Why
			}
			if w.Qids[walked-1].Type&0x80 != 0 {
				return nil, "", Error(abi.Enonexist)
			}
			return nil, "", Error(abi.Enotdir)
		}
		next := NewChan(found.Dev, found.Devno, w.Qids[walked-1], "", w.Aux)
		for _, e := range elems[i : i+walked] {
			name += "/" + e
		}
		next.Name = name
		i += walked
		cur = p.domount(next, crossLast || i < len(elems))
	}
	return cur, name, nil
}

// walkNames walks as many of names from c as its device takes in one step.
func walkNames(ctx context.Context, c *Chan, names []string) (int, WalkedNames, error) {
	if mw, ok := c.Dev.(MultiWalker); ok {
		n := max(1, min(mw.MaxWalk(), len(names)))
		w, err := mw.WalkNames(ctx, c, names[:n])
		return n, w, err
	}
	w, err := c.Dev.Walk(ctx, c, names[0])
	if err != nil {
		return 1, WalkedNames{}, err
	}
	return 1, WalkedNames{Qids: []styx.Qid{w.Qid}, Aux: w.Aux, Reached: true}, nil
}

// Namespace is the name space as bind and mount lines then cd, in the raw
// form of Inferno's /prog/n/ns: one line for each bind or mount in the
// order they were made. The underlying directory of a union is not listed.
func (p *Pgrp) Namespace() string {
	type line struct {
		m   Member
		old string
	}
	p.mu.RLock()
	var lines []line
	for _, m := range p.mounts {
		for _, mem := range m.to {
			if !m.isOriginal(mem) {
				lines = append(lines, line{mem, m.from.Name})
			}
		}
	}
	p.mu.RUnlock()
	sort.SliceStable(lines, func(a, b int) bool { return lines[a].m.ID < lines[b].m.ID })
	var s strings.Builder
	for _, l := range lines {
		c := l.m.Chan
		fl := flagText(l.m.Flags)
		if md, ok := c.Dev.(Mounted); ok {
			if src, ok := md.MountSource(c); ok {
				fmt.Fprintf(&s, "mount %s %s %s %s\n", fl, src, l.old, md.Aname(c))
				continue
			}
		}
		fmt.Fprintf(&s, "bind %s %s %s\n", fl, c.Name, l.old)
	}
	fmt.Fprintf(&s, "cd %s\n", p.Dot())
	return s.String()
}

// domount returns, when c is a mount point and cross is set, the first
// union member carrying the whole union; otherwise c.
func (p *Pgrp) domount(c *Chan, cross bool) *Chan {
	if !cross {
		return c
	}
	to := p.mountedKey(c.Key())
	if to == nil {
		return c
	}
	first := to[0].Chan.CloneUnopened()
	first.Name = c.Name
	first.Union = to
	return first
}

func flagText(flags uint32) string {
	var s string
	switch flags & morder {
	case mafter:
		s = "a"
	case mbefore:
		s = "b"
	}
	if flags&mcreate != 0 {
		s += "c"
	}
	if s == "" {
		return ""
	}
	return "-" + s
}

// indir resolves #*name!spec to a device character and spec, with the
// NODEVS rules of devindir.c.
func (p *Pgrp) indir(spec string) (rune, string, error) {
	if spec == "" {
		return 0, "", Error(abi.Ebadspec)
	}
	name, rest, _ := strings.Cut(spec, "!")
	var d Dev
	for _, x := range p.devtab.Devs() {
		if x.Name() == name {
			d = x
		}
	}
	if d == nil || d.DC() == '*' {
		return 0, "", Error("unknown device: " + name)
	}
	if p.nodevs.Load() && (!strings.ContainsRune("|esDa", d.DC()) || (d.DC() == 's' && rest != "")) {
		return 0, "", Error(abi.Enoattach)
	}
	return d.DC(), rest, nil
}
