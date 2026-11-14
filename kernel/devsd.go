package kernel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/core"
	"github.com/ericvh/purgatorio-go/kernel/virtio"
	"github.com/ericvh/purgatorio-go/styx"
)

// DevSD is #S, the storage device (Inferno's sd(3), devsd.c), over the
// virtio block devices: each disk is a directory sdC0, sdC1, ... with
// ctl and its partitions, of which data, the whole disk, is always
// there. Reading ctl gives the disk's geometry and partition table;
// writing "part name start end" (in sectors) adds a partition and
// "delpart name" removes one.
type DevSD struct {
	core.DevBase
	k     *core.Kernel
	units []*sdunit
}

type sdpart struct {
	name       string
	start, end uint64
	vers       uint32
}

type sdunit struct {
	name  string
	disk  *virtio.Block
	mu    sync.Mutex // parts, and read-modify-write of partial sectors
	parts []*sdpart  // nil entries are deleted partitions
	vers  uint32
}

// NewDevSD serves disks.
func NewDevSD(k *core.Kernel, disks []*virtio.Block) *DevSD {
	d := &DevSD{k: k}
	for i, b := range disks {
		u := &sdunit{name: "sdC" + strconv.Itoa(i), disk: b}
		u.parts = []*sdpart{{name: "data", start: 0, end: b.Sectors}}
		d.units = append(d.units, u)
	}
	return d
}

func (d *DevSD) DC() rune     { return 'S' }
func (d *DevSD) Name() string { return "sd" }

// Qid paths: the top directory is 0; unit u's directory is (u+1)<<16,
// its ctl (u+1)<<16|1 and partition i (u+1)<<16|(i+2).
const (
	sdQctl   = 1
	sdQpart0 = 2
)

func sdpath(u, f int) uint64 { return uint64(u+1)<<16 | uint64(f) }

func (d *DevSD) decode(q styx.Qid) (*sdunit, int, int) {
	u := int(q.Path>>16) - 1
	if u < 0 || u >= len(d.units) {
		return nil, -1, 0
	}
	return d.units[u], u, int(q.Path & 0xffff)
}

func (d *DevSD) Attach(_ context.Context, spec string) (core.Walked, error) {
	if spec != "" {
		return core.Walked{}, core.Error(abi.Ebadarg)
	}
	return core.Walked{Qid: styx.Qid{Type: styx.QTDIR}}, nil
}

func (d *DevSD) dir(q styx.Qid) (styx.Dir, error) {
	if q.Path == 0 {
		return core.TabDir('S', core.Tab{Name: "#S", Perm: styx.DMDIR | 0o555}, d.k.Owner), nil
	}
	u, ui, f := d.decode(q)
	if u == nil {
		return styx.Dir{}, core.Error(abi.Enonexist)
	}
	switch {
	case f == 0:
		return core.TabDir('S', core.Tab{Name: u.name, Path: sdpath(ui, 0), Perm: styx.DMDIR | 0o555}, d.k.Owner), nil
	case f == sdQctl:
		u.mu.Lock()
		defer u.mu.Unlock()
		dir := core.TabDir('S', core.Tab{Name: "ctl", Path: sdpath(ui, f), Perm: 0o640}, d.k.Owner)
		dir.Qid.Vers = u.vers
		return dir, nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	i := f - sdQpart0
	if i < 0 || i >= len(u.parts) || u.parts[i] == nil {
		return styx.Dir{}, core.Error(abi.Enonexist)
	}
	p := u.parts[i]
	dir := core.TabDir('S', core.Tab{Name: p.name, Path: sdpath(ui, f), Perm: 0o640,
		Length: (p.end - p.start) * virtio.SectorSize}, d.k.Owner)
	dir.Qid.Vers = p.vers
	return dir, nil
}

func (d *DevSD) DirRead(_ context.Context, c *core.Chan) ([]styx.Dir, error) {
	var out []styx.Dir
	if c.Qid.Path == 0 {
		for i := range d.units {
			dir, _ := d.dir(styx.Qid{Type: styx.QTDIR, Path: sdpath(i, 0)})
			out = append(out, dir)
		}
		return out, nil
	}
	u, ui, _ := d.decode(c.Qid)
	if u == nil {
		return nil, core.Error(abi.Enonexist)
	}
	dir, _ := d.dir(styx.Qid{Path: sdpath(ui, sdQctl)})
	out = append(out, dir)
	u.mu.Lock()
	n := len(u.parts)
	u.mu.Unlock()
	for i := 0; i < n; i++ {
		if dir, err := d.dir(styx.Qid{Path: sdpath(ui, sdQpart0+i)}); err == nil {
			out = append(out, dir)
		}
	}
	return out, nil
}

func (d *DevSD) Walk(ctx context.Context, c *core.Chan, name string) (core.Walked, error) {
	if !c.IsDir() {
		return core.Walked{}, core.Error(abi.Enotdir)
	}
	dirs, err := d.DirRead(ctx, c)
	if err != nil {
		return core.Walked{}, err
	}
	for _, dir := range dirs {
		if dir.Name == name {
			return core.Walked{Qid: dir.Qid}, nil
		}
	}
	return core.Walked{}, core.Error(abi.Enonexist)
}

func (d *DevSD) Stat(_ context.Context, c *core.Chan) (styx.Dir, error) { return d.dir(c.Qid) }

func (d *DevSD) Open(_ context.Context, c *core.Chan, mode int) (*core.Chan, error) {
	dir, err := d.dir(c.Qid)
	if err != nil {
		return nil, err
	}
	if c.IsDir() {
		if mode != abi.OREAD {
			return nil, core.Error(abi.Eisdir)
		}
		return c, nil
	}
	if err := core.OpenPerm(dir.Mode, mode); err != nil {
		return nil, err
	}
	return c, nil
}

// part returns the partition c is open on, checking it still exists.
func (d *DevSD) part(c *core.Chan) (*sdunit, *sdpart, error) {
	u, _, f := d.decode(c.Qid)
	if u == nil {
		return nil, nil, core.Error(abi.Enonexist)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	i := f - sdQpart0
	if i < 0 || i >= len(u.parts) || u.parts[i] == nil || u.parts[i].vers != c.Qid.Vers {
		return nil, nil, core.Error("partition changed")
	}
	return u, u.parts[i], nil
}

func (d *DevSD) Read(_ context.Context, c *core.Chan, buf []byte, off int64) (int, error) {
	u, _, f := d.decode(c.Qid)
	if u == nil {
		return 0, core.Error(abi.Enonexist)
	}
	if f == sdQctl {
		return core.ReadStr([]byte(u.ctl()), buf, off), nil
	}
	u, p, err := d.part(c)
	if err != nil {
		return 0, err
	}
	return u.bio(p, buf, off, false)
}

func (d *DevSD) Write(_ context.Context, c *core.Chan, buf []byte, off int64) (int, error) {
	u, _, f := d.decode(c.Qid)
	if u == nil {
		return 0, core.Error(abi.Enonexist)
	}
	if f == sdQctl {
		if err := u.ctlwrite(string(buf)); err != nil {
			return 0, err
		}
		return len(buf), nil
	}
	u, p, err := d.part(c)
	if err != nil {
		return 0, err
	}
	return u.bio(p, buf, off, true)
}

func (u *sdunit) ctl() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var s strings.Builder
	fmt.Fprintf(&s, "inquiry virtio block device\n")
	fmt.Fprintf(&s, "geometry %d %d\n", u.disk.Sectors, virtio.SectorSize)
	for _, p := range u.parts {
		if p != nil {
			fmt.Fprintf(&s, "part %s %d %d\n", p.name, p.start, p.end)
		}
	}
	return s.String()
}

func (u *sdunit) ctlwrite(cmd string) error {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return core.Error(abi.Ebadctl)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	switch f[0] {
	case "part":
		if len(f) != 4 {
			return core.Error(abi.Ebadctl)
		}
		start, err1 := strconv.ParseUint(f[2], 0, 64)
		end, err2 := strconv.ParseUint(f[3], 0, 64)
		if err1 != nil || err2 != nil || start > end || end > u.disk.Sectors {
			return core.Error(abi.Ebadarg)
		}
		free := -1
		for i, p := range u.parts {
			switch {
			case p == nil:
				if free < 0 {
					free = i
				}
			case p.name == f[1]:
				return core.Error(abi.Einuse)
			}
		}
		u.vers++
		p := &sdpart{name: f[1], start: start, end: end, vers: u.vers}
		if free < 0 {
			u.parts = append(u.parts, p)
		} else {
			u.parts[free] = p
		}
	case "delpart":
		if len(f) != 2 {
			return core.Error(abi.Ebadctl)
		}
		for i, p := range u.parts {
			if p != nil && p.name == f[1] {
				if i == 0 {
					return core.Error(abi.Eperm)
				}
				u.parts[i] = nil
				u.vers++
				return nil
			}
		}
		return core.Error(abi.Enonexist)
	default:
		return core.Error(abi.Ebadctl)
	}
	return nil
}

// bio reads or writes buf at byte offset off in partition p. Partial
// sectors are read, changed and written back.
func (u *sdunit) bio(p *sdpart, buf []byte, off int64, write bool) (int, error) {
	const ss = virtio.SectorSize
	size := int64(p.end-p.start) * ss
	if off < 0 {
		return 0, core.Error(abi.Ebadarg)
	}
	if off >= size {
		if write && len(buf) > 0 {
			return 0, core.Error("write past end of partition")
		}
		return 0, nil
	}
	if off+int64(len(buf)) > size {
		buf = buf[:size-off]
	}
	first := off / ss
	last := (off + int64(len(buf)) + ss - 1) / ss
	tmp := make([]byte, (last-first)*ss)
	sector := p.start + uint64(first)
	skip := off - first*ss
	partial := skip != 0 || len(buf)%ss != 0
	if write && partial {
		u.mu.Lock()
		defer u.mu.Unlock()
	}
	if !write || partial {
		if err := u.disk.ReadAt(tmp, sector); err != nil {
			return 0, core.Error(abi.Eio)
		}
	}
	if !write {
		return copy(buf, tmp[skip:]), nil
	}
	copy(tmp[skip:], buf)
	if err := u.disk.WriteAt(tmp, sector); err != nil {
		return 0, core.Error(abi.Eio)
	}
	return len(buf), nil
}
