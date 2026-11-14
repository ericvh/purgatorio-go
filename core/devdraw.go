package core

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"strconv"
	"sync"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/memdraw"
	"github.com/ericvh/purgatorio-go/platform"
	"github.com/ericvh/purgatorio-go/styx"
)

// The draw device #i, after Inferno's emu/port/devdraw.c. Each client
// opens /dev/draw/new, which becomes the ctl file of a new directory
// /dev/draw/n; it writes draw messages to n/data and reads the screen's
// or an image's description from n/ctl.

// File types; the client slot (plus one) is above them in the qid path.
const (
	qdrawtop = iota
	qdrawnew
	qdraw3rd
	qdraw2nd
	qdrawcolormap
	qdrawctl
	qdrawdata
	qdrawrefresh
)

const (
	drawqshift = 4
	drawnhash  = 1 << 5
	drawiounit = 64 * 1024
)

func drawqid(q styx.Qid) uint64   { return q.Path & 0xF }
func drawslot(path uint64) int    { return int(path&0x7FFFFFF0) >> drawqshift }
func drawpath(slot, t int) uint64 { return uint64(slot)<<drawqshift | uint64(t) }

// Draw device error strings, as Inferno's.
const (
	enodrawimage  = "unknown id for draw image"
	enodrawscreen = "unknown id for draw screen"
	eshortdraw    = "short draw message"
	eshortread    = "draw read too short"
	eimageexists  = "image id in use"
	escreenexists = "screen id in use"
	edrawmem      = "out of memory: image"
	ereadoutside  = "readimage outside image"
	ewriteoutside = "writeimage outside image"
	enotfont      = "image not a font"
	eindex        = "character index out of range"
	enoclient     = "no such draw client"
	enameused     = "image name in use"
	enoname       = "no image with that name"
	eoldname      = "named image no longer valid"
	enamed        = "image already has name"
	ewrongname    = "wrong name for image"
	enofb         = "no frame buffer"
)

// Refresh methods.
const (
	refbackup = 0
	refnone   = 1
	refmesg   = 2
)

type drawClient struct {
	ref       int
	dimage    [drawnhash]*dimage
	cscreen   *cscreen
	refresh   *drawRefresh
	refrend   Rendez
	readdata  []byte
	busy      bool
	clientid  int
	slot      int
	refreshme bool
	infoid    int
	op        int
}

type drawRefresh struct {
	dimage *dimage
	r      memdraw.Rectangle
	next   *drawRefresh
}

type refx struct {
	client *drawClient
	dimage *dimage
}

type dname struct {
	name   string
	client *drawClient
	dimage *dimage
	vers   int
}

type fchar struct {
	minx, maxx int   // left and right edges of bits
	miny, maxy uint8 // first non-zero scan line, last plus one
	left       int8  // offset of baseline
	width      uint8 // width of baseline
}

// dimage is a client's image: one reference per open by its client, one
// per screen image or fill, one per image derived from it by name.
type dimage struct {
	id       int
	ref      int
	name     string
	vers     int
	image    *memdraw.Image
	ascent   int
	fchar    []fchar
	dscreen  *dscreen // nil if not a window
	fromname *dimage  // the image this one was named from
	next     *dimage
}

type cscreen struct {
	dscreen *dscreen
	next    *cscreen
}

type dscreen struct {
	id     int
	public bool
	ref    int
	dimage *dimage
	dfill  *dimage
	screen *memdraw.Screen
	owner  *drawClient
	next   *dscreen
}

// DevDraw is #i.
type DevDraw struct {
	DevBase
	k *Kernel

	mu        sync.Mutex // sdraw.q
	clientid  int
	client    []*drawClient
	name      []*dname
	vers      int
	scr       platform.Screen
	screen    *memdraw.Image
	flushrect memdraw.Rectangle
	waste     int
	dscreen   *dscreen
	watchers  []*Rendez // purgatorio.wait on refresh files
}

func NewDevDraw(k *Kernel) *DevDraw { return &DevDraw{k: k} }

func (d *DevDraw) DC() rune     { return 'i' }
func (d *DevDraw) Name() string { return "draw" }

// drawError carries an Inferno error string out of a draw message.
type drawError string

func drawerror(s string) { panic(drawError(s)) }

// catch turns a draw error, or a fault in the graphics code, into an
// error, as waserror does.
func catch(err *error) {
	r := recover()
	if r == nil {
		return
	}
	switch e := r.(type) {
	case drawError:
		*err = Error(string(e))
	case runtime.Error:
		*err = Error("draw: " + e.Error())
	default:
		panic(r)
	}
}

// initscreen makes the screen image on first attach.
func (d *DevDraw) initscreen() bool {
	if d.screen != nil {
		return true
	}
	dp, ok := d.k.Platform.(platform.Displayer)
	if !ok {
		return false
	}
	scr := dp.Display()
	if scr == nil {
		return false
	}
	w, h, chans := scr.Info()
	img, err := memdraw.Alloc(memdraw.Rect(0, 0, w, h), memdraw.ParseChan(chans))
	if err != nil {
		return false
	}
	d.scr = scr
	d.screen = img
	d.flushrect = memdraw.Rect(10000, 10000, -10000, -10000)
	if rs, ok := scr.(platform.Resizer); ok {
		if ch := rs.Resized(); ch != nil {
			go func() {
				for range ch {
					d.mu.Lock()
					d.resize()
					d.mu.Unlock()
				}
			}()
		}
	}
	return true
}

// resize makes the screen image the size the screen now has, keeping
// the pixels that fit and the windows on it (memdraw's Resize), and
// tells every client: its refresh file gives a record for image 0 with
// the new rectangle, and a read of ctl for image 0 describes the
// screen as it now is. Inferno's devdraw has no counterpart, since its
// screens never change size; this follows the Rust Purgatorio's.
func (d *DevDraw) resize() {
	w, h, _ := d.scr.Info()
	r := memdraw.Rect(0, 0, w, h)
	if w <= 0 || h <= 0 || r == d.screen.R {
		return
	}
	var ss []*memdraw.Screen
	for s := d.dscreen; s != nil; s = s.next {
		if s.screen.Image != nil && s.screen.Image.Data == d.screen.Data {
			ss = append(ss, s.screen)
		}
	}
	if d.screen.Resize(r, ss) != nil {
		return
	}
	d.flushrect = r
	d.flush()
	for _, cl := range d.client {
		if cl == nil {
			continue
		}
		di := d.lookup(cl, 0, false)
		if di == nil || di.image != d.screen {
			continue
		}
		found := false
		for ref := cl.refresh; ref != nil; ref = ref.next {
			if ref.dimage == di {
				ref.r, found = r, true
			}
		}
		if !found {
			cl.refresh = &drawRefresh{dimage: di, r: r, next: cl.refresh}
		}
	}
	d.wakeall()
}

// Screen returns the screen image, for tests and the host.
func (d *DevDraw) Screen() *memdraw.Image {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.screen
}

func (d *DevDraw) Attach(context.Context, string) (Walked, error) {
	d.mu.Lock()
	ok := d.initscreen()
	d.mu.Unlock()
	if !ok {
		return Walked{}, Error(enofb)
	}
	return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: qdrawtop}}, nil
}

func (d *DevDraw) clientofpath(path uint64) *drawClient {
	slot := drawslot(path)
	if slot == 0 || slot > len(d.client) {
		return nil
	}
	cl := d.client[slot-1]
	if cl == nil || cl.clientid == 0 {
		return nil
	}
	return cl
}

func (d *DevDraw) Walk(_ context.Context, c *Chan, name string) (Walked, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.screen == nil {
		return Walked{}, Error(enofb)
	}
	switch drawqid(c.Qid) {
	case qdrawtop:
		if name == "draw" {
			return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: qdraw2nd}}, nil
		}
	case qdraw2nd:
		if name == "new" {
			return Walked{Qid: styx.Qid{Path: qdrawnew}}, nil
		}
		for s, cl := range d.client {
			if cl != nil && strconv.Itoa(cl.clientid) == name {
				return Walked{Qid: styx.Qid{Type: styx.QTDIR, Path: drawpath(s+1, qdraw3rd)}}, nil
			}
		}
	case qdraw3rd:
		for _, t := range drawtab {
			if t.Name == name {
				return Walked{Qid: styx.Qid{Path: c.Qid.Path&^0xF | t.Path, Vers: c.Qid.Vers}}, nil
			}
		}
	default:
		return Walked{}, Error(abi.Enotdir)
	}
	return Walked{}, Error(abi.Enonexist)
}

var drawtab = []Tab{
	{"colormap", qdrawcolormap, 0o600, 0},
	{"ctl", qdrawctl, 0o600, 0},
	{"data", qdrawdata, 0o600, 0},
	{"refresh", qdrawrefresh, 0o400, 0},
}

func (d *DevDraw) dirOf(path uint64) styx.Dir {
	eve := d.k.Owner
	dir := styx.Dir{Type: 'i', UID: eve, GID: eve}
	switch t := path & 0xF; t {
	case qdrawtop:
		dir.Qid = styx.Qid{Type: styx.QTDIR, Path: path}
		dir.Mode, dir.Name = styx.DMDIR|0o555, "#i"
	case qdraw2nd:
		dir.Qid = styx.Qid{Type: styx.QTDIR, Path: path}
		dir.Mode, dir.Name = styx.DMDIR|0o555, "draw"
	case qdrawnew:
		dir.Qid = styx.Qid{Path: path}
		dir.Mode, dir.Name = 0o666, "new"
	case qdraw3rd:
		dir.Qid = styx.Qid{Type: styx.QTDIR, Path: path}
		dir.Mode, dir.Name = styx.DMDIR|0o555, "??"
		if cl := d.clientofpath(path); cl != nil {
			dir.Name = strconv.Itoa(cl.clientid)
		}
	default:
		for _, e := range drawtab {
			if e.Path == t {
				dir.Qid = styx.Qid{Path: path}
				dir.Mode, dir.Name = e.Perm, e.Name
			}
		}
	}
	return dir
}

func (d *DevDraw) Stat(_ context.Context, c *Chan) (styx.Dir, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dir := d.dirOf(c.Qid.Path)
	dir.Qid.Vers = c.Qid.Vers
	return dir, nil
}

func (d *DevDraw) DirRead(_ context.Context, c *Chan) ([]styx.Dir, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch drawqid(c.Qid) {
	case qdrawtop:
		return []styx.Dir{d.dirOf(qdraw2nd)}, nil
	case qdraw2nd:
		out := []styx.Dir{d.dirOf(qdrawnew)}
		for s, cl := range d.client {
			if cl != nil {
				out = append(out, d.dirOf(drawpath(s+1, qdraw3rd)))
			}
		}
		return out, nil
	case qdraw3rd:
		var out []styx.Dir
		for _, t := range drawtab {
			out = append(out, d.dirOf(c.Qid.Path&^0xF|t.Path))
		}
		return out, nil
	}
	return nil, Error(abi.Enotdir)
}

func (d *DevDraw) newclient() *drawClient {
	i := 0
	for ; i < len(d.client); i++ {
		if d.client[i] == nil {
			break
		}
	}
	if i == len(d.client) {
		d.client = append(d.client, nil)
	}
	d.clientid++
	cl := &drawClient{slot: i, clientid: d.clientid, op: memdraw.SoverD}
	d.client[i] = cl
	return cl
}

func (d *DevDraw) Open(_ context.Context, c *Chan, mode int) (*Chan, error) {
	if c.IsDir() {
		return c, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if drawqid(c.Qid) == qdrawnew {
		cl := d.newclient()
		c.Qid.Path = drawpath(cl.slot+1, qdrawctl)
	}
	switch drawqid(c.Qid) {
	case qdrawctl:
		cl := d.clientofpath(c.Qid.Path)
		if cl == nil {
			return nil, Error(enoclient)
		}
		if cl.busy {
			return nil, Error(abi.Einuse)
		}
		cl.busy = true
		d.flushrect = memdraw.Rect(10000, 10000, -10000, -10000)
		d.install(cl, 0, d.screen, nil)
		cl.ref++
	case qdrawcolormap, qdrawdata, qdrawrefresh:
		cl := d.clientofpath(c.Qid.Path)
		if cl == nil {
			return nil, Error(enoclient)
		}
		cl.ref++
	}
	return c, nil
}

func (d *DevDraw) Iounit(*Chan) uint32 { return drawiounit }

func (d *DevDraw) Close(c *Chan) {
	if drawqid(c.Qid) < qdrawcolormap {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cl := d.clientofpath(c.Qid.Path)
	if cl == nil {
		return
	}
	if drawqid(c.Qid) == qdrawctl {
		cl.busy = false
	}
	cl.ref--
	if cl.ref != 0 {
		return
	}
	func() {
		var err error
		defer catch(&err)
		cl.refresh = nil
		for i := 0; i < len(d.name); {
			if d.name[i].client == cl {
				d.delname(i)
			} else {
				i++
			}
		}
		for cl.cscreen != nil {
			d.uninstallscreen(cl, cl.cscreen)
		}
		// All screens are freed, so now the images can be.
		for i := range cl.dimage {
			for cl.dimage[i] != nil {
				di := cl.dimage[i]
				cl.dimage[i] = di.next
				d.freedimage(di)
			}
		}
	}()
	d.client[cl.slot] = nil
	d.flush() // to erase visible, now dead windows
}

func (d *DevDraw) Read(ctx context.Context, c *Chan, buf []byte, off int64) (n int, err error) {
	d.mu.Lock()
	cl := d.clientofpath(c.Qid.Path)
	if cl == nil {
		d.mu.Unlock()
		return 0, Error(enoclient)
	}
	if drawqid(c.Qid) == qdrawrefresh {
		d.mu.Unlock()
		return d.readrefresh(ctx, cl, buf)
	}
	defer d.mu.Unlock()
	defer catch(&err)
	switch drawqid(c.Qid) {
	case qdrawctl:
		if len(buf) < 12*12 {
			drawerror(eshortread)
		}
		if cl.infoid < 0 {
			drawerror(enodrawimage)
		}
		i := d.screen
		if cl.infoid != 0 {
			di := d.lookup(cl, cl.infoid, true)
			if di == nil {
				drawerror(enodrawimage)
			}
			i = di.image
		}
		repl := 0
		if i.Flags&memdraw.Frepl != 0 {
			repl = 1
		}
		s := fmt.Sprintf("%11d %11d %11s %11d %11d %11d %11d %11d %11d %11d %11d %11d ",
			cl.clientid, cl.infoid, i.Chan.String(), repl,
			i.R.Min.X, i.R.Min.Y, i.R.Max.X, i.R.Max.Y,
			i.Clipr.Min.X, i.Clipr.Min.Y, i.Clipr.Max.X, i.Clipr.Max.Y)
		cl.infoid = -1
		return copy(buf, s), nil
	case qdrawcolormap:
		return 0, nil
	case qdrawdata:
		if cl.readdata == nil {
			drawerror("no draw data")
		}
		if len(buf) < len(cl.readdata) {
			drawerror(eshortread)
		}
		n = copy(buf, cl.readdata)
		cl.readdata = nil
		return n, nil
	}
	return 0, Error(abi.Eperm)
}

// readrefresh blocks until a window of the client needs repainting.
func (d *DevDraw) readrefresh(ctx context.Context, cl *drawClient, buf []byte) (int, error) {
	if len(buf) < 5*4 {
		return 0, Error(abi.Ebadarg)
	}
	err := Park(ctx, &cl.refrend, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return cl.refreshme || cl.refresh != nil
	})
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := 0
	for cl.refresh != nil && len(buf)-p >= 5*4 {
		r := cl.refresh
		for k, v := range []int{r.dimage.id, r.r.Min.X, r.r.Min.Y, r.r.Max.X, r.r.Max.Y} {
			binary.LittleEndian.PutUint32(buf[p+4*k:], uint32(v))
		}
		cl.refresh = r.next
		p += 5 * 4
	}
	cl.refreshme = false
	return p, nil
}

func (d *DevDraw) wakeall() {
	any := false
	for _, cl := range d.client {
		if cl != nil && (cl.refreshme || cl.refresh != nil) {
			cl.refrend.Wakeup()
			any = true
		}
	}
	if any {
		for _, r := range d.watchers {
			r.Wakeup()
		}
	}
}

var _ Waiter = (*DevDraw)(nil)

// Ready, Watch and Unwatch let purgatorio.wait wait for a refresh
// record; the other files never block.
func (d *DevDraw) Ready(c *Chan, write bool) bool {
	if write || drawqid(c.Qid) != qdrawrefresh {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cl := d.clientofpath(c.Qid.Path)
	return cl == nil || cl.refreshme || cl.refresh != nil
}

func (d *DevDraw) Watch(c *Chan, write bool, r *Rendez) {
	d.mu.Lock()
	d.watchers = append(d.watchers, r)
	d.mu.Unlock()
}

func (d *DevDraw) Unwatch(c *Chan, r *Rendez) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.watchers {
		if x == r {
			d.watchers = append(d.watchers[:i], d.watchers[i+1:]...)
			return
		}
	}
}

func (d *DevDraw) Write(_ context.Context, c *Chan, buf []byte, _ int64) (n int, err error) {
	if c.IsDir() {
		return 0, Error(abi.Eisdir)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cl := d.clientofpath(c.Qid.Path)
	if cl == nil {
		return 0, Error(enoclient)
	}
	defer d.wakeall()
	switch drawqid(c.Qid) {
	case qdrawctl:
		if len(buf) != 4 {
			return 0, Error("unknown draw control request")
		}
		cl.infoid = int(int32(binary.LittleEndian.Uint32(buf)))
		return 4, nil
	case qdrawcolormap:
		return 0, nil
	case qdrawdata:
		if err := d.mesg(cl, buf); err != nil {
			return 0, err
		}
		return len(buf), nil
	}
	return 0, Error(abi.Ebadusefd)
}

// Flushing to the visible screen.

func (d *DevDraw) flushscreen(r memdraw.Rectangle) {
	if d.scr == nil || !memdraw.RectClip(&r, d.screen.R) {
		return
	}
	d.scr.Flush(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y, d.screen.Data.Bdata, 4*d.screen.Width)
}

func (d *DevDraw) addflush(r memdraw.Rectangle) {
	if !memdraw.RectClip(&r, d.screen.R) {
		return
	}
	if d.flushrect.Min.X >= d.flushrect.Max.X {
		d.flushrect = r
		d.waste = 0
		return
	}
	nbb := d.flushrect
	memdraw.CombineRect(&nbb, r)
	ar := r.Dx() * r.Dy()
	abb := d.flushrect.Dx() * d.flushrect.Dy()
	anbb := nbb.Dx() * nbb.Dy()
	// The new waste is the growth of the bounding box less the new
	// segment, which is assumed not to be waste.
	d.waste += anbb - abb - ar
	if d.waste < 0 {
		d.waste = 0
	}
	// Absorb if the total area is small, the waste is less than half,
	// or the rectangles touch.
	if anbb <= 1024 || d.waste*2 < anbb || memdraw.RectXRect(d.flushrect, r) {
		d.flushrect = nbb
		return
	}
	if d.flushrect.Min.X < d.flushrect.Max.X {
		d.flushscreen(d.flushrect)
	}
	d.flushrect = r
	d.waste = 0
}

func (d *DevDraw) dstflush(dst *memdraw.Image, r memdraw.Rectangle) {
	if dst == d.screen {
		memdraw.CombineRect(&d.flushrect, r)
		return
	}
	l := dst.Layer
	if l == nil {
		return
	}
	for l != nil {
		if l.Screen.Image.Data != d.screen.Data {
			return
		}
		r = r.Add(l.Delta)
		l = l.Screen.Image.Layer
	}
	d.addflush(r)
}

func (d *DevDraw) flush() {
	if d.flushrect.Min.X < d.flushrect.Max.X {
		d.flushscreen(d.flushrect)
	}
	d.flushrect = memdraw.Rect(10000, 10000, -10000, -10000)
}

// Images, screens and names.

func (d *DevDraw) lookupname(str string) *dname {
	for _, n := range d.name {
		if n.name == str {
			return n
		}
	}
	return nil
}

func (d *DevDraw) goodname(di *dimage) bool {
	// If a window, validate the screen's own images.
	if di.dscreen != nil {
		if !d.goodname(di.dscreen.dimage) || !d.goodname(di.dscreen.dfill) {
			return false
		}
	}
	if di.name == "" {
		return true
	}
	n := d.lookupname(di.name)
	return n != nil && n.vers == di.vers
}

func (d *DevDraw) lookup(cl *drawClient, id int, checkname bool) *dimage {
	for di := cl.dimage[id&(drawnhash-1)]; di != nil; di = di.next {
		if di.id == id {
			if checkname && !d.goodname(di) {
				drawerror(eoldname)
			}
			return di
		}
	}
	return nil
}

func (d *DevDraw) lookupdscreen(id int) *dscreen {
	for s := d.dscreen; s != nil; s = s.next {
		if s.id == id {
			return s
		}
	}
	return nil
}

func (d *DevDraw) lookupscreen(cl *drawClient, id int) (*dscreen, *cscreen) {
	for s := cl.cscreen; s != nil; s = s.next {
		if s.dscreen.id == id {
			return s.dscreen, s
		}
	}
	drawerror(enodrawscreen)
	return nil, nil
}

func (d *DevDraw) install(cl *drawClient, id int, i *memdraw.Image, ds *dscreen) *dimage {
	h := id & (drawnhash - 1)
	di := &dimage{id: id, ref: 1, image: i, dscreen: ds, next: cl.dimage[h]}
	cl.dimage[h] = di
	return di
}

func (d *DevDraw) installscreen(cl *drawClient, ds *dscreen, id int, di, dfill *dimage, public bool) *memdraw.Screen {
	if ds == nil {
		s := &memdraw.Screen{}
		ds = &dscreen{dimage: di, dfill: dfill, id: id, screen: s, public: public, next: d.dscreen, owner: cl}
		if di != nil {
			s.Image = di.image
			di.ref++
		}
		if dfill != nil {
			s.Fill = dfill.image
			dfill.ref++
		}
		d.dscreen = ds
	}
	cl.cscreen = &cscreen{dscreen: ds, next: cl.cscreen}
	ds.ref++
	return ds.screen
}

func (d *DevDraw) delname(i int) {
	d.name = append(d.name[:i], d.name[i+1:]...)
}

func (d *DevDraw) freedscreen(this *dscreen) {
	this.ref--
	if this.ref > 0 {
		return
	}
	if d.dscreen == this {
		d.dscreen = this.next
	} else {
		ds := d.dscreen
		for ds != nil && ds.next != this {
			ds = ds.next
		}
		if ds == nil {
			drawerror(enodrawimage)
		}
		ds.next = this.next
	}
	if this.dimage != nil {
		d.freedimage(this.dimage)
	}
	if this.dfill != nil {
		d.freedimage(this.dfill)
	}
}

func (d *DevDraw) freedimage(di *dimage) {
	di.ref--
	if di.ref > 0 {
		return
	}
	// Any names?
	for i := 0; i < len(d.name); {
		if d.name[i].dimage == di {
			d.delname(i)
		} else {
			i++
		}
	}
	if di.fromname != nil { // acquired by name; owned by someone else
		d.freedimage(di.fromname)
		return
	}
	if di.image == d.screen { // don't free the display
		return
	}
	if ds := di.dscreen; ds != nil {
		l := di.image
		if l.Data == d.screen.Data {
			d.dstflush(l.Layer.Screen.Image, l.Layer.Screenr)
		}
		l.Layer.Refreshptr = nil
		if d.goodname(di) {
			memdraw.Ldelete(l)
		} else {
			memdraw.Lfree(l)
		}
		d.freedscreen(ds)
		return
	}
	di.image.Free()
}

func (d *DevDraw) uninstallscreen(cl *drawClient, this *cscreen) {
	if cl.cscreen == this {
		cl.cscreen = this.next
		d.freedscreen(this.dscreen)
		return
	}
	for cs := cl.cscreen; cs != nil && cs.next != nil; cs = cs.next {
		if cs.next == this {
			cs.next = this.next
			d.freedscreen(this.dscreen)
			return
		}
	}
}

func (d *DevDraw) uninstall(cl *drawClient, id int) {
	h := id & (drawnhash - 1)
	di := cl.dimage[h]
	if di == nil {
		drawerror(enodrawimage)
	}
	if di.id == id {
		cl.dimage[h] = di.next
		d.freedimage(di)
		return
	}
	for ; di.next != nil; di = di.next {
		if di.next.id == id {
			next := di.next
			di.next = next.next
			d.freedimage(next)
			return
		}
	}
	drawerror(enodrawimage)
}

func (d *DevDraw) addname(cl *drawClient, di *dimage, str string) {
	if d.lookupname(str) != nil {
		drawerror(enameused)
	}
	d.vers++
	d.name = append(d.name, &dname{name: str, dimage: di, client: cl, vers: d.vers})
}

// refreshscreen tells the owner of the screen l is on to repaint.
func refreshscreen(l *dimage, cl *drawClient) {
	for l != nil && l.dscreen == nil {
		l = l.fromname
	}
	if l != nil && l.dscreen.owner != cl {
		l.dscreen.owner.refreshme = true
	}
}

// drawrefresh queues r of a Refmesg window for its client.
func drawrefresh(_ *memdraw.Image, r memdraw.Rectangle, v any) {
	x, ok := v.(*refx)
	if !ok || x == nil {
		return
	}
	c := x.client
	for ref := c.refresh; ref != nil; ref = ref.next {
		if ref.dimage == x.dimage {
			memdraw.CombineRect(&ref.r, r)
			return
		}
	}
	c.refresh = &drawRefresh{dimage: x.dimage, r: r, next: c.refresh}
}

// Draw messages.

func bglong(a []byte) int  { return int(int32(binary.LittleEndian.Uint32(a))) }
func bgshort(a []byte) int { return int(binary.LittleEndian.Uint16(a)) }

func bgrect(a []byte) memdraw.Rectangle {
	return memdraw.Rect(bglong(a), bglong(a[4:]), bglong(a[8:]), bglong(a[12:]))
}

func bgpoint(a []byte) memdraw.Point { return memdraw.Pt(bglong(a), bglong(a[4:])) }

func (d *DevDraw) image(cl *drawClient, a []byte) *memdraw.Image {
	di := d.lookup(cl, bglong(a), true)
	if di == nil {
		drawerror(enodrawimage)
	}
	return di.image
}

func clientop(cl *drawClient) int {
	op := cl.op
	cl.op = memdraw.SoverD
	return op
}

func (d *DevDraw) drawchar(dst *memdraw.Image, p memdraw.Point, src *memdraw.Image, sp *memdraw.Point, font *dimage, index, op int) memdraw.Point {
	fc := &font.fchar[index]
	var r memdraw.Rectangle
	r.Min.X = p.X + int(fc.left)
	r.Min.Y = p.Y - (font.ascent - int(fc.miny))
	r.Max.X = r.Min.X + (fc.maxx - fc.minx)
	r.Max.Y = r.Min.Y + int(fc.maxy-fc.miny)
	sp1 := memdraw.Pt(sp.X+int(fc.left), sp.Y+int(fc.miny))
	memdraw.Draw(dst, r, src, sp1, font.image, memdraw.Pt(fc.minx, int(fc.miny)), op)
	p.X += int(fc.width)
	sp.X += int(fc.width)
	return p
}

// drawcoord decodes one coordinate of a polygon, relative to oldx.
func drawcoord(a []byte, i int, oldx int) (int, int) {
	if i >= len(a) {
		drawerror(eshortdraw)
	}
	b := int(a[i])
	i++
	x := b & 0x7F
	if b&0x80 != 0 {
		if i+1 >= len(a) {
			drawerror(eshortdraw)
		}
		x |= int(a[i]) << 7
		x |= int(a[i+1]) << 15
		i += 2
		if x&(1<<22) != 0 {
			x |= ^0 << 23
		}
	} else {
		if b&0x40 != 0 {
			x |= ^0 << 7
		}
		x += oldx
	}
	return x, i
}

// mesg interprets a buffer of draw messages (drawmesg).
func (d *DevDraw) mesg(cl *drawClient, buf []byte) (err error) {
	defer catch(&err)
	for len(buf) > 0 {
		m := d.mesg1(cl, buf)
		buf = buf[m:]
	}
	return nil
}

func need(a []byte, m int) {
	if len(a) < m {
		drawerror(eshortdraw)
	}
}

// mesg1 interprets the message at the start of a and returns its length.
func (d *DevDraw) mesg1(cl *drawClient, a []byte) int {
	switch a[0] {
	default:
		drawerror("bad draw command")

	// allocate: 'b' id[4] screenid[4] refresh[1] chan[4] repl[1] R[4*4]
	// clipR[4*4] rrggbbaa[4]
	case 'b':
		m := 1 + 4 + 4 + 1 + 4 + 1 + 4*4 + 4*4 + 4
		need(a, m)
		dstid := bglong(a[1:])
		scrnid := bgshort(a[5:]) // sic: Inferno reads two bytes of the four
		refresh := a[9]
		ch := memdraw.Chan(binary.LittleEndian.Uint32(a[10:]))
		repl := a[14] != 0
		r := bgrect(a[15:])
		clipr := bgrect(a[31:])
		value := binary.LittleEndian.Uint32(a[47:])
		if d.lookup(cl, dstid, false) != nil {
			drawerror(eimageexists)
		}
		if scrnid != 0 {
			dscrn, _ := d.lookupscreen(cl, scrnid)
			scrn := dscrn.screen
			if repl || ch != scrn.Image.Chan {
				drawerror("image parameters incompatible with screen")
			}
			var reffn memdraw.Refreshfn
			switch refresh {
			case refbackup:
			case refnone:
				reffn = memdraw.NoRefresh
			case refmesg:
				reffn = drawrefresh
			default:
				drawerror("unknown refresh method")
			}
			l, err := memdraw.Lalloc(scrn, r, reffn, nil, value)
			if err != nil {
				drawerror(edrawmem)
			}
			d.dstflush(l.Layer.Screen.Image, l.Layer.Screenr)
			l.Clipr = clipr
			memdraw.RectClip(&l.Clipr, r)
			d.install(cl, dstid, l, dscrn)
			dscrn.ref++
			if reffn != nil {
				var x any
				if refresh == refmesg {
					x = &refx{client: cl, dimage: d.lookup(cl, dstid, true)}
				}
				memdraw.Lsetrefresh(l, reffn, x)
			}
			return m
		}
		if ch.Depth() == 0 {
			drawerror(edrawmem)
		}
		i, err := memdraw.Alloc(r, ch)
		if err != nil {
			drawerror(edrawmem)
		}
		if repl {
			i.Flags |= memdraw.Frepl
		}
		i.Clipr = clipr
		if !repl {
			memdraw.RectClip(&i.Clipr, r)
		}
		d.install(cl, dstid, i, nil)
		i.FillColor(value)
		return m

	// allocate screen: 'A' id[4] imageid[4] fillid[4] public[1]
	case 'A':
		m := 1 + 4 + 4 + 4 + 1
		need(a, m)
		dstid := bglong(a[1:])
		if dstid == 0 {
			drawerror(abi.Ebadarg)
		}
		if d.lookupdscreen(dstid) != nil {
			drawerror(escreenexists)
		}
		ddst := d.lookup(cl, bglong(a[5:]), true)
		dsrc := d.lookup(cl, bglong(a[9:]), true)
		if ddst == nil || dsrc == nil {
			drawerror(enodrawimage)
		}
		d.installscreen(cl, nil, dstid, ddst, dsrc, a[13] != 0)
		return m

	// set repl and clip: 'c' dstid[4] repl[1] clipR[4*4]
	case 'c':
		m := 1 + 4 + 1 + 4*4
		need(a, m)
		ddst := d.lookup(cl, bglong(a[1:]), true)
		if ddst == nil {
			drawerror(enodrawimage)
		}
		if ddst.name != "" {
			drawerror("cannot change repl/clipr of shared image")
		}
		dst := ddst.image
		if a[5] != 0 {
			dst.Flags |= memdraw.Frepl
		}
		dst.Clipr = bgrect(a[6:])
		return m

	// draw: 'd' dstid[4] srcid[4] maskid[4] R[4*4] P[2*4] P[2*4]
	case 'd':
		m := 1 + 4 + 4 + 4 + 4*4 + 2*4 + 2*4
		need(a, m)
		dst := d.image(cl, a[1:])
		src := d.image(cl, a[5:])
		mask := d.image(cl, a[9:])
		r := bgrect(a[13:])
		p := bgpoint(a[29:])
		q := bgpoint(a[37:])
		op := clientop(cl)
		memdraw.Draw(dst, r, src, p, mask, q, op)
		d.dstflush(dst, r)
		return m

	// toggle debugging: 'D' val[1]
	case 'D':
		m := 1 + 1
		need(a, m)
		return m

	// ellipse: 'e' dstid[4] srcid[4] center[2*4] a[4] b[4] thick[4]
	// sp[2*4] alpha[4] phi[4]
	case 'e', 'E':
		m := 1 + 4 + 4 + 2*4 + 4 + 4 + 4 + 2*4 + 2*4
		need(a, m)
		dst := d.image(cl, a[1:])
		src := d.image(cl, a[5:])
		p := bgpoint(a[9:])
		e0 := bglong(a[17:])
		e1 := bglong(a[21:])
		if e0 < 0 || e1 < 0 {
			drawerror("invalid ellipse semidiameter")
		}
		j := bglong(a[25:])
		if j < 0 {
			drawerror("negative ellipse thickness")
		}
		sp := bgpoint(a[29:])
		c := j
		if a[0] == 'E' {
			c = -1
		}
		ox := uint32(bglong(a[37:]))
		oy := bglong(a[41:])
		op := clientop(cl)
		// The high bit says arc angles are present.
		if ox&(1<<31) != 0 {
			if ox&(1<<30) == 0 {
				ox &^= 1 << 31
			}
			memdraw.Arc(dst, p, e0, e1, c, src, sp, int(int32(ox)), oy, op)
		} else {
			memdraw.Ellipse(dst, p, e0, e1, c, src, sp, op)
		}
		d.dstflush(dst, memdraw.Rect(p.X-e0-j, p.Y-e1-j, p.X+e0+j+1, p.Y+e1+j+1))
		return m

	// free: 'f' id[4]
	case 'f':
		m := 1 + 4
		need(a, m)
		ll := d.lookup(cl, bglong(a[1:]), false)
		if ll != nil && ll.dscreen != nil && ll.dscreen.owner != cl {
			ll.dscreen.owner.refreshme = true
		}
		d.uninstall(cl, bglong(a[1:]))
		return m

	// free screen: 'F' id[4]
	case 'F':
		m := 1 + 4
		need(a, m)
		_, cs := d.lookupscreen(cl, bglong(a[1:]))
		d.uninstallscreen(cl, cs)
		return m

	// initialize font: 'i' fontid[4] nchars[4] ascent[1]
	case 'i':
		m := 1 + 4 + 4 + 1
		need(a, m)
		dstid := bglong(a[1:])
		dst := d.image(cl, a[1:])
		if dstid == 0 || dst == d.screen {
			drawerror("cannot use display as font")
		}
		font := d.lookup(cl, dstid, true)
		if font == nil {
			drawerror(enodrawimage)
		}
		if font.image.Layer != nil {
			drawerror("cannot use window as font")
		}
		ni := bglong(a[5:])
		if ni <= 0 || ni > 4096 {
			drawerror("bad font size (4096 chars max)")
		}
		font.fchar = make([]fchar, ni)
		font.ascent = int(a[9])
		return m

	// load character: 'l' fontid[4] srcid[4] index[2] R[4*4] P[2*4]
	// left[1] width[1]
	case 'l':
		m := 1 + 4 + 4 + 2 + 4*4 + 2*4 + 1 + 1
		need(a, m)
		font := d.lookup(cl, bglong(a[1:]), true)
		if font == nil {
			drawerror(enodrawimage)
		}
		if len(font.fchar) == 0 {
			drawerror(enotfont)
		}
		src := d.image(cl, a[5:])
		ci := bgshort(a[9:])
		if ci >= len(font.fchar) {
			drawerror(eindex)
		}
		r := bgrect(a[11:])
		p := bgpoint(a[27:])
		memdraw.Draw(font.image, r, src, p, memdraw.Opaque, p, memdraw.SoverD)
		fc := &font.fchar[ci]
		fc.minx = r.Min.X
		fc.maxx = r.Max.X
		fc.miny = uint8(r.Min.Y)
		fc.maxy = uint8(r.Max.Y)
		fc.left = int8(a[35])
		fc.width = a[36]
		return m

	// line: 'L' dstid[4] p0[2*4] p1[2*4] end0[4] end1[4] radius[4]
	// srcid[4] sp[2*4]
	case 'L':
		m := 1 + 4 + 2*4 + 2*4 + 4 + 4 + 4 + 4 + 2*4
		need(a, m)
		dst := d.image(cl, a[1:])
		p := bgpoint(a[5:])
		q := bgpoint(a[13:])
		e0 := bglong(a[21:])
		e1 := bglong(a[25:])
		j := bglong(a[29:])
		if j < 0 {
			drawerror("negative line width")
		}
		src := d.image(cl, a[33:])
		sp := bgpoint(a[37:])
		op := clientop(cl)
		memdraw.Line(dst, p, q, e0, e1, j, src, sp, op)
		if dst == d.screen || dst.Layer != nil {
			r := memdraw.LineBBox(p, q, e0, e1, j)
			d.dstflush(dst, r.Inset(-(1 + 1 + j)))
		}
		return m

	// attach to a named image: 'n' dstid[4] j[1] name[j]
	case 'n':
		m := 1 + 4 + 1
		need(a, m)
		j := int(a[5])
		if j == 0 { // give me a non-empty name please
			drawerror(eshortdraw)
		}
		m += j
		need(a, m)
		dstid := bglong(a[1:])
		if d.lookup(cl, dstid, false) != nil {
			drawerror(eimageexists)
		}
		dn := d.lookupname(string(a[6 : 6+j]))
		if dn == nil {
			drawerror(enoname)
		}
		di := d.install(cl, dstid, dn.dimage.image, nil)
		di.vers = dn.vers
		di.name = string(a[6 : 6+j])
		di.fromname = dn.dimage
		di.fromname.ref++
		cl.infoid = dstid
		return m

	// name an image: 'N' dstid[4] in[1] j[1] name[j]
	case 'N':
		m := 1 + 4 + 1 + 1
		need(a, m)
		c := a[5]
		j := int(a[6])
		if j == 0 {
			drawerror(eshortdraw)
		}
		m += j
		need(a, m)
		di := d.lookup(cl, bglong(a[1:]), false)
		if di == nil {
			drawerror(enodrawimage)
		}
		if di.name != "" {
			drawerror(enamed)
		}
		name := string(a[7 : 7+j])
		if c != 0 {
			d.addname(cl, di, name)
		} else {
			dn := d.lookupname(name)
			if dn == nil {
				drawerror(enoname)
			}
			if dn.dimage != di {
				drawerror(ewrongname)
			}
			for i, n := range d.name {
				if n == dn {
					d.delname(i)
					break
				}
			}
		}
		return m

	// position window: 'o' id[4] r.min[2*4] screenr.min[2*4]
	case 'o':
		m := 1 + 4 + 2*4 + 2*4
		need(a, m)
		dst := d.image(cl, a[1:])
		if dst.Layer != nil {
			p := bgpoint(a[5:])
			q := bgpoint(a[13:])
			r := dst.Layer.Screenr
			ni, err := memdraw.Lorigin(dst, p, q)
			if err != nil || ni < 0 {
				drawerror("image origin failed")
			}
			if ni > 0 {
				d.dstflush(dst.Layer.Screen.Image, r)
				d.dstflush(dst.Layer.Screen.Image, dst.Layer.Screenr)
				refreshscreen(d.lookup(cl, bglong(a[1:]), true), cl)
			}
		}
		return m

	// compositing operator for the next draw operation: 'O' op[1]
	case 'O':
		m := 1 + 1
		need(a, m)
		cl.op = int(a[1])
		return m

	// filled polygon: 'P' dstid[4] n[2] wind[4] ignore[2*4] srcid[4]
	// sp[2*4] dp[2*2*(n+1)]
	// polygon: 'p' dstid[4] n[2] end0[4] end1[4] radius[4] srcid[4]
	// sp[2*4] dp[2*2*(n+1)]
	// The points are coded relative to the last: a byte for a small
	// delta, three for an absolute value.
	case 'p', 'P':
		m := 1 + 4 + 2 + 4 + 4 + 4 + 4 + 2*4
		need(a, m)
		dst := d.image(cl, a[1:])
		ni := bgshort(a[5:])
		e0 := bglong(a[7:])
		e1 := bglong(a[11:])
		j := 0
		if a[0] == 'p' {
			j = bglong(a[15:])
			if j < 0 {
				drawerror("negative polygon line width")
			}
		}
		src := d.image(cl, a[19:])
		sp := bgpoint(a[23:])
		var p memdraw.Point // the points follow the header
		ni++
		pp := make([]memdraw.Point, ni)
		doflush := dst == d.screen || (dst.Layer != nil && dst.Layer.Screen.Image.Data == d.screen.Data)
		ox, oy := 0, 0
		esize := 0
		u := m
		var r memdraw.Rectangle
		y := 0
		for ; y < ni; y++ {
			q := p
			oesize := esize
			p.X, u = drawcoord(a, u, ox)
			p.Y, u = drawcoord(a, u, oy)
			ox, oy = p.X, p.Y
			if doflush {
				esize = j
				if a[0] == 'p' {
					if y == 0 {
						esize = max(esize, memdraw.LineEndSize(e0))
					}
					if y == ni-1 {
						esize = max(esize, memdraw.LineEndSize(e1))
					}
				}
				if a[0] == 'P' && e0 != 1 && e0 != ^0 {
					r = dst.Clipr
				} else if y > 0 {
					r = memdraw.Rect(q.X-oesize, q.Y-oesize, q.X+oesize+1, q.Y+oesize+1)
					memdraw.CombineRect(&r, memdraw.Rect(p.X-esize, p.Y-esize, p.X+esize+1, p.Y+esize+1))
				}
				if memdraw.RectClip(&r, dst.Clipr) {
					d.dstflush(dst, r)
				}
			}
			pp[y] = p
		}
		if y == 1 {
			d.dstflush(dst, memdraw.Rect(p.X-esize, p.Y-esize, p.X+esize+1, p.Y+esize+1))
		}
		op := clientop(cl)
		if a[0] == 'p' {
			memdraw.Poly(dst, pp, e0, e1, j, src, sp, op)
		} else {
			memdraw.FillPoly(dst, pp, e0, src, sp, op)
		}
		return u

	// read: 'r' id[4] R[4*4]
	case 'r':
		m := 1 + 4 + 4*4
		need(a, m)
		i := d.image(cl, a[1:])
		r := bgrect(a[5:])
		if !r.In(i.R) {
			drawerror(ereadoutside)
		}
		c := memdraw.BytesPerLine(r, i.Depth) * r.Dy()
		if c < 0 || c > 64<<20 {
			drawerror("readimage malloc failed")
		}
		cl.readdata = make([]byte, c)
		n, err := memdraw.Unload(i, r, cl.readdata)
		if err != nil {
			cl.readdata = nil
			drawerror("bad readimage call")
		}
		cl.readdata = cl.readdata[:n]
		return m

	// string: 's' dstid[4] srcid[4] fontid[4] P[2*4] clipr[4*4] sp[2*4]
	// ni[2] ni*(index[2])
	// stringbg: 'x' dstid[4] srcid[4] fontid[4] P[2*4] clipr[4*4]
	// sp[2*4] ni[2] bgid[4] bgpt[2*4] ni*(index[2])
	case 's', 'x':
		m := 1 + 4 + 4 + 4 + 2*4 + 4*4 + 2*4 + 2
		if a[0] == 'x' {
			m += 4 + 2*4
		}
		need(a, m)
		dst := d.image(cl, a[1:])
		src := d.image(cl, a[5:])
		font := d.lookup(cl, bglong(a[9:]), true)
		if font == nil {
			drawerror(enodrawimage)
		}
		if len(font.fchar) == 0 {
			drawerror(enotfont)
		}
		p := bgpoint(a[13:])
		r := bgrect(a[21:])
		sp := bgpoint(a[37:])
		ni := bgshort(a[45:])
		u := m
		m += ni * 2
		need(a, m)
		clipr := dst.Clipr
		dst.Clipr = r
		defer func() { dst.Clipr = clipr }()
		op := clientop(cl)
		index := func(k int) int {
			ci := bgshort(a[u+2*k:])
			if ci >= len(font.fchar) {
				drawerror(eindex)
			}
			return ci
		}
		if a[0] == 'x' {
			// Paint the background.
			l := d.image(cl, a[47:])
			q := bgpoint(a[51:])
			r.Min.X = p.X
			r.Min.Y = p.Y - font.ascent
			r.Max.X = p.X
			r.Max.Y = r.Min.Y + font.image.R.Dy()
			for k := 0; k < ni; k++ {
				r.Max.X += int(font.fchar[index(k)].width)
			}
			memdraw.Draw(dst, r, l, q, memdraw.Opaque, memdraw.ZP, op)
		}
		q := p
		for k := 0; k < ni; k++ {
			q = d.drawchar(dst, q, src, &sp, font, index(k), op)
		}
		dst.Clipr = clipr
		p.Y -= font.ascent
		d.dstflush(dst, memdraw.Rect(p.X, p.Y, q.X, p.Y+font.image.R.Dy()))
		return m

	// use public screen: 'S' id[4] chan[4]
	case 'S':
		m := 1 + 4 + 4
		need(a, m)
		dstid := bglong(a[1:])
		if dstid == 0 {
			drawerror(abi.Ebadarg)
		}
		dscrn := d.lookupdscreen(dstid)
		if dscrn == nil || (!dscrn.public && dscrn.owner != cl) {
			drawerror(enodrawscreen)
		}
		if dscrn.screen.Image.Chan != memdraw.Chan(binary.LittleEndian.Uint32(a[5:])) {
			drawerror("inconsistent chan")
		}
		d.installscreen(cl, dscrn, 0, nil, nil, false)
		return m

	// top or bottom windows: 't' top[1] nw[2] n*id[4]
	case 't':
		m := 1 + 1 + 2
		need(a, m)
		nw := bgshort(a[2:])
		if nw == 0 {
			return m
		}
		m += nw * 4
		need(a, m)
		lp := make([]*memdraw.Image, nw)
		for j := range lp {
			lp[j] = d.image(cl, a[1+1+2+j*4:])
		}
		if lp[0].Layer == nil {
			drawerror("images are not windows")
		}
		for j := 1; j < nw; j++ {
			if lp[j].Layer == nil || lp[j].Layer.Screen != lp[0].Layer.Screen {
				drawerror("images not on same screen")
			}
		}
		if a[1] != 0 {
			memdraw.LtofrontN(lp)
		} else {
			memdraw.LtorearN(lp)
		}
		if lp[0].Layer.Screen.Image.Data == d.screen.Data {
			for _, l := range lp {
				d.dstflush(l.Layer.Screen.Image, l.Layer.Screenr)
			}
		}
		refreshscreen(d.lookup(cl, bglong(a[1+1+2:]), true), cl)
		return m

	// visible: 'v'
	case 'v':
		d.flush()
		return 1

	// write: 'y' id[4] R[4*4] data[x*1]
	// write from compressed data: 'Y' id[4] R[4*4] data[x*1]
	case 'y', 'Y':
		m := 1 + 4 + 4*4
		need(a, m)
		dst := d.image(cl, a[1:])
		r := bgrect(a[5:])
		if !r.In(dst.R) {
			drawerror(ewriteoutside)
		}
		y, err := memdraw.Load(dst, r, a[m:], a[0] == 'Y')
		if err != nil || y < 0 {
			drawerror("bad writeimage call")
		}
		d.dstflush(dst, r)
		return m + y
	}
	return len(a)
}
