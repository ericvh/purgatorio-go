package core

import (
	"context"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

type rootNode struct {
	name   string
	parent int
	data   []byte
	dir    bool
}

// StaticTree is a read-only tree built at boot: the emu's #/ (empty mount
// points for the devices bound at startup) and, for the kernel, an
// embedded root with file contents.
type StaticTree struct {
	DevBase
	dc    rune
	name  string
	owner string
	nodes []rootNode
}

// NewStaticTree returns an empty tree served as #<dc>.
func NewStaticTree(dc rune, name, owner string) *StaticTree {
	return &StaticTree{dc: dc, name: name, owner: owner, nodes: []rootNode{{name: "/", dir: true}}}
}

// NewRoot returns the emu's #/, with the mount points Inferno's root has.
func NewRoot(owner string) *StaticTree {
	t := NewStaticTree('/', "root", owner)
	for _, d := range []string{"chan", "dev", "env", "fd", "net", "prog", "cmd", "n", "tmp"} {
		t.AddDir(d)
	}
	return t
}

// AddDir adds a directory and any missing parents.
func (t *StaticTree) AddDir(path string) int {
	cur := 0
	for _, e := range strings.Split(path, "/") {
		if e == "" {
			continue
		}
		if i := t.child(cur, e); i >= 0 {
			cur = i
		} else {
			t.nodes = append(t.nodes, rootNode{name: e, parent: cur, dir: true})
			cur = len(t.nodes) - 1
		}
	}
	return cur
}

// AddFile adds a file with fixed contents, creating parent directories.
func (t *StaticTree) AddFile(path string, data []byte) {
	path = strings.TrimLeft(path, "/")
	dir, name := 0, path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, name = t.AddDir(path[:i]), path[i+1:]
	}
	t.nodes = append(t.nodes, rootNode{name: name, parent: dir, data: data})
}

func (t *StaticTree) child(parent int, name string) int {
	for i := 1; i < len(t.nodes); i++ {
		if t.nodes[i].parent == parent && t.nodes[i].name == name {
			return i
		}
	}
	return -1
}

func (t *StaticTree) qid(i int) styx.Qid {
	if t.nodes[i].dir {
		return styx.Qid{Type: styx.QTDIR, Path: uint64(i)}
	}
	return styx.Qid{Path: uint64(i)}
}

func (t *StaticTree) node(c *Chan) (int, error) {
	if i := int(c.Qid.Path); i < len(t.nodes) {
		return i, nil
	}
	return 0, Error(abi.Enonexist)
}

func (t *StaticTree) dir(i int) styx.Dir {
	n := t.nodes[i]
	perm, length := uint32(0o444), uint64(len(n.data))
	if n.dir {
		perm, length = styx.DMDIR|0o555, 0
	}
	return styx.Dir{Type: uint16(t.dc), Qid: t.qid(i), Mode: perm, Length: length,
		Name: n.name, UID: t.owner, GID: t.owner}
}

func (t *StaticTree) DC() rune     { return t.dc }
func (t *StaticTree) Name() string { return t.name }

func (t *StaticTree) Attach(context.Context, string) (Walked, error) {
	return Walked{Qid: t.qid(0)}, nil
}

func (t *StaticTree) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	i, err := t.node(c)
	if err != nil {
		return Walked{}, err
	}
	j := t.child(i, name)
	if j < 0 {
		return Walked{}, Error(abi.Enonexist)
	}
	return Walked{Qid: t.qid(j)}, nil
}

func (t *StaticTree) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	i, err := t.node(c)
	if err != nil {
		return styx.Dir{}, err
	}
	return t.dir(i), nil
}

func (t *StaticTree) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if a := mode & abi.OACCESS; (a != abi.OREAD && a != abi.OEXEC) || mode&abi.OTRUNC != 0 {
		return nil, Error(abi.Eperm)
	}
	return c, nil
}

func (t *StaticTree) Read(_ context.Context, c *Chan, buf []byte, off int64) (int, error) {
	i, err := t.node(c)
	if err != nil {
		return 0, err
	}
	if t.nodes[i].dir {
		return 0, Error(abi.Eisdir)
	}
	return ReadStr(t.nodes[i].data, buf, off), nil
}

func (t *StaticTree) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	i, err := t.node(c)
	if err != nil {
		return nil, err
	}
	var out []styx.Dir
	for j := 1; j < len(t.nodes); j++ {
		if t.nodes[j].parent == i {
			out = append(out, t.dir(j))
		}
	}
	return out, nil
}

func (t *StaticTree) Write(context.Context, *Chan, []byte, int64) (int, error) {
	return 0, Error(abi.Eperm)
}
