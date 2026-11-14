package styxservers

import "github.com/ericvh/purgatorio-go/styx"

// Tree is a Navigator over a tree of names held in memory, after
// Inferno's Nametree module (styxservers-nametree(2)). Files are known by
// Qid path; the program creates and removes them and serves their
// contents itself.
type Tree struct {
	tab map[uint64]*fholder
}

type fholder struct {
	parent  uint64
	d       styx.Dir
	child   *fholder
	sibling *fholder
}

// NewTree returns an empty tree. Create its root first, with the root's
// own path as parent.
func NewTree() *Tree { return &Tree{tab: map[uint64]*fholder{}} }

// Create adds d to the directory with path parent. A root is a file
// created with its own path as parent.
func (t *Tree) Create(parent uint64, d styx.Dir) error {
	p := t.tab[parent]
	if t.tab[d.Qid.Path] != nil {
		return Eexists
	}
	if p == nil && d.Qid.Path != parent {
		return Enotfound
	}
	fh := &fholder{parent: parent, d: d}
	if p != nil {
		if t.walk(p, d.Name) != nil {
			return Eexists
		}
		fh.sibling = p.child
		p.child = fh
	}
	t.tab[d.Qid.Path] = fh
	return nil
}

// Remove removes the file with path q and everything below it.
func (t *Tree) Remove(q uint64) error {
	fh := t.tab[q]
	if fh == nil {
		return Enotfound
	}
	delete(t.tab, q)
	if p := t.tab[fh.parent]; p != nil && p != fh {
		var prev *fholder
		for s := p.child; s != nil; s = s.sibling {
			if s == fh {
				if prev == nil {
					p.child = fh.sibling
				} else {
					prev.sibling = fh.sibling
				}
				break
			}
			prev = s
		}
	}
	fh.sibling = nil
	for s := fh.child; s != nil; {
		next := s.sibling
		s.parent = s.d.Qid.Path // detach it, so it doesn't disturb the parent
		t.Remove(s.d.Qid.Path)
		s = next
	}
	return nil
}

// Wstat changes the file with path q to d; fields of d that are null (as
// in a wstat) are left alone, and the Qid path never changes.
func (t *Tree) Wstat(q uint64, d styx.Dir) error {
	fh := t.tab[q]
	if fh == nil {
		return Enotfound
	}
	nd := applydir(&d, fh.d)
	if nd.Name != fh.d.Name {
		if p := t.tab[fh.parent]; p != nil && p != fh && t.walk(p, nd.Name) != nil {
			return Eexists
		}
	}
	fh.d = nd
	fh.d.Qid.Path = q
	return nil
}

// Getpath returns the file's name from the root, or false if it is not
// in the tree.
func (t *Tree) Getpath(q uint64) (string, bool) {
	fh := t.tab[q]
	if fh == nil {
		return "", false
	}
	s := fh.d.Name
	for fh.parent != fh.d.Qid.Path {
		if fh = t.tab[fh.parent]; fh == nil {
			return "", false
		}
		s = fh.d.Name + "/" + s
	}
	return s, true
}

func (t *Tree) walk(fh *fholder, name string) *styx.Dir {
	if name == ".." {
		if p := t.tab[fh.parent]; p != nil {
			d := p.d
			return &d
		}
		return nil
	}
	for c := fh.child; c != nil; c = c.sibling {
		if c.d.Name == name {
			d := c.d
			return &d
		}
	}
	return nil
}

// Stat is the Navigator's stat.
func (t *Tree) Stat(q uint64) (*styx.Dir, error) {
	fh := t.tab[q]
	if fh == nil {
		return nil, Enotfound
	}
	d := fh.d
	return &d, nil
}

// Walk is the Navigator's walk.
func (t *Tree) Walk(q uint64, name string) (*styx.Dir, error) {
	fh := t.tab[q]
	if fh == nil {
		return nil, Enotfound
	}
	if d := t.walk(fh, name); d != nil {
		return d, nil
	}
	return nil, Enotfound
}

// Readdir is the Navigator's readdir.
func (t *Tree) Readdir(q uint64, offset, count int) []*styx.Dir {
	fh := t.tab[q]
	if fh == nil {
		return nil
	}
	var out []*styx.Dir
	i := 0
	for c := fh.child; c != nil && i < offset+count; c = c.sibling {
		if i >= offset {
			d := c.d
			out = append(out, &d)
		}
		i++
	}
	return out
}

func applydir(d *styx.Dir, onto styx.Dir) styx.Dir {
	if d.Name != "" {
		onto.Name = d.Name
	}
	if d.UID != "" {
		onto.UID = d.UID
	}
	if d.GID != "" {
		onto.GID = d.GID
	}
	if d.MUID != "" {
		onto.MUID = d.MUID
	}
	if d.Qid.Vers != ^uint32(0) {
		onto.Qid.Vers = d.Qid.Vers
	}
	if d.Qid.Type != ^uint8(0) {
		onto.Qid.Type = d.Qid.Type
	}
	if d.Mode != ^uint32(0) {
		onto.Mode = d.Mode
	}
	if d.Atime != ^uint32(0) {
		onto.Atime = d.Atime
	}
	if d.Mtime != ^uint32(0) {
		onto.Mtime = d.Mtime
	}
	if d.Length != ^uint64(0) {
		onto.Length = d.Length
	}
	if d.Type != ^uint16(0) {
		onto.Type = d.Type
	}
	if d.Dev != ^uint32(0) {
		onto.Dev = d.Dev
	}
	return onto
}
