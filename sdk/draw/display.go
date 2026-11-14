package draw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const bufsize = 8000 // Displaybufsize

// Display is a connection to the draw device.
type Display struct {
	ctl, data io.ReadWriter
	closers   []io.Closer
	buf       []byte
	imageid   int
	Dirno     int
	Image     *Image // the screen, image 0
	Chan      Chan
	Depth     int

	White, Black, Opaque, Transparent *Image
	DefaultFont                       *Font
}

// Init connects to the draw device under dev (default /dev), binding #i
// there first if it is not already (initdisplay).
func Init(dev string) (*Display, error) {
	if dev == "" {
		dev = "/dev"
	}
	ctl, err := sys.Open(dev+"/draw/new", sys.ORDWR)
	if err != nil {
		if berr := sys.Bind("#i", dev, sys.MBEFORE); berr != nil {
			return nil, fmt.Errorf("initdisplay: %s/draw/new: %v", dev, err)
		}
		if ctl, err = sys.Open(dev+"/draw/new", sys.ORDWR); err != nil {
			return nil, fmt.Errorf("initdisplay: %s/draw/new: %v", dev, err)
		}
	}
	ctlf := os.NewFile(uintptr(ctl), "ctl")
	info := make([]byte, 12*12)
	n, err := io.ReadFull(ctlf, info)
	if err != nil || n < 12*12 {
		ctlf.Close()
		return nil, fmt.Errorf("initdisplay: reading ctl: %v", err)
	}
	dir := strings.TrimSpace(string(info[:12]))
	data, err := sys.Open(dev+"/draw/"+dir+"/data", sys.ORDWR)
	if err != nil {
		ctlf.Close()
		return nil, fmt.Errorf("initdisplay: data: %v", err)
	}
	dataf := os.NewFile(uintptr(data), "data")
	d, err := NewDisplay(ctlf, dataf, info)
	if err != nil {
		ctlf.Close()
		dataf.Close()
		return nil, err
	}
	d.closers = []io.Closer{dataf, ctlf}
	return d, nil
}

// NewDisplay makes a Display on open ctl and data files, given the 144
// bytes first read from ctl.
func NewDisplay(ctl, data io.ReadWriter, info []byte) (*Display, error) {
	f := strings.Fields(string(info))
	if len(f) < 12 {
		return nil, errors.New("initdisplay: short ctl info")
	}
	atoi := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	ch := ParseChan(f[2])
	if ch == 0 {
		return nil, fmt.Errorf("initdisplay: bad channel %q", f[2])
	}
	d := &Display{ctl: ctl, data: data, Dirno: atoi(0), Chan: ch, Depth: ch.Depth()}
	d.Image = &Image{Display: d, ID: 0, Chan: ch, Depth: ch.Depth(), Repl: atoi(3) != 0,
		R: Rect(atoi(4), atoi(5), atoi(6), atoi(7)), Clipr: Rect(atoi(8), atoi(9), atoi(10), atoi(11))}
	var err error
	for _, c := range []struct {
		p   **Image
		val uint32
	}{{&d.White, White}, {&d.Black, Black}, {&d.Opaque, Opaque}, {&d.Transparent, Transparent}} {
		if *c.p, err = d.AllocImage(Rect(0, 0, 1, 1), GREY1, true, c.val); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Close flushes and closes the connection.
func (d *Display) Close() error {
	err := d.Flush(true)
	for _, c := range d.closers {
		c.Close()
	}
	return err
}

// bufimage returns n bytes of the message buffer to fill.
func (d *Display) bufimage(n int) ([]byte, error) {
	if n < 0 || n > bufsize {
		return nil, errors.New("bad count in bufimage")
	}
	if len(d.buf)+n > bufsize {
		if err := d.doflush(); err != nil {
			return nil, err
		}
	}
	o := len(d.buf)
	d.buf = append(d.buf, make([]byte, n)...)
	return d.buf[o:], nil
}

func (d *Display) doflush() error {
	if len(d.buf) == 0 {
		return nil
	}
	_, err := d.data.Write(d.buf)
	d.buf = d.buf[:0]
	if err != nil {
		return fmt.Errorf("draw: %v", err)
	}
	return nil
}

// Flush writes the buffered messages; with visible it also asks for the
// screen to show the changes.
func (d *Display) Flush(visible bool) error {
	if visible {
		if b, err := d.bufimage(1); err == nil {
			b[0] = 'v'
		}
	}
	return d.doflush()
}

// message encoding helpers.
func putl(b []byte, v int)    { binary.LittleEndian.PutUint32(b, uint32(int32(v))) }
func putu(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func puts(b []byte, v int)    { binary.LittleEndian.PutUint16(b, uint16(v)) }
func putp(b []byte, p Point)  { putl(b, p.X); putl(b[4:], p.Y) }
func putr(b []byte, r Rectangle) {
	putp(b, r.Min)
	putp(b[8:], r.Max)
}

// setop sends an 'O' message for an operator other than SoverD.
func (d *Display) setop(op Op) error {
	if op == SoverD {
		return nil
	}
	b, err := d.bufimage(2)
	if err != nil {
		return err
	}
	b[0] = 'O'
	b[1] = byte(op)
	return nil
}

// AllocImage makes an image with channels ch filled with color
// (allocimage).
func (d *Display) AllocImage(r Rectangle, ch Chan, repl bool, color uint32) (*Image, error) {
	return d.allocImage(nil, r, ch, repl, color, 0, 0)
}

func (d *Display) allocImage(s *Screen, r Rectangle, ch Chan, repl bool, color uint32, screenid, refresh int) (*Image, error) {
	depth := ch.Depth()
	if depth == 0 {
		return nil, errors.New("allocimage: bad channel descriptor")
	}
	b, err := d.bufimage(1 + 4 + 4 + 1 + 4 + 1 + 4*4 + 4*4 + 4)
	if err != nil {
		return nil, err
	}
	d.imageid++
	id := d.imageid
	b[0] = 'b'
	putl(b[1:], id)
	putl(b[5:], screenid)
	b[9] = byte(refresh)
	putu(b[10:], uint32(ch))
	if repl {
		b[14] = 1
	}
	putr(b[15:], r)
	clipr := r
	if repl {
		// A huge but not infinite clip rectangle.
		clipr = Rect(-0x3FFFFFFF, -0x3FFFFFFF, 0x3FFFFFFF, 0x3FFFFFFF)
	}
	putr(b[31:], clipr)
	putu(b[47:], color)
	if err := d.doflush(); err != nil {
		return nil, err
	}
	return &Image{Display: d, ID: id, Chan: ch, Depth: depth, Repl: repl, R: r, Clipr: clipr, Screen: s}, nil
}

// Color returns a 1×1 replicated image of color, for painting.
func (d *Display) Color(color uint32) (*Image, error) {
	ch := RGB24
	if color&0xFF != 0xFF {
		ch = RGBA32
	}
	return d.AllocImage(Rect(0, 0, 1, 1), ch, true, color)
}

// CheckResize asks the draw device for the screen's rectangle and, if
// the screen has changed size (a host window the user resized), updates
// d.Image and reports true. The device also reports a resize on the
// connection's refresh file, as a record for image 0.
func (d *Display) CheckResize() (bool, error) {
	if err := d.Flush(false); err != nil {
		return false, err
	}
	var id [4]byte
	if _, err := d.ctl.Write(id[:]); err != nil {
		return false, err
	}
	info := make([]byte, 12*12)
	if _, err := io.ReadFull(d.ctl, info); err != nil {
		return false, err
	}
	f := strings.Fields(string(info))
	if len(f) < 12 {
		return false, errors.New("checkresize: short ctl info")
	}
	atoi := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	r := Rect(atoi(4), atoi(5), atoi(6), atoi(7))
	if r == d.Image.R {
		return false, nil
	}
	d.Image.R = r
	d.Image.Clipr = Rect(atoi(8), atoi(9), atoi(10), atoi(11))
	return true, nil
}

// NamedImage attaches to an image another client has named.
func (d *Display) NamedImage(name string) (*Image, error) {
	if len(name) == 0 || len(name) > 255 {
		return nil, errors.New("namedimage: bad name")
	}
	if err := d.Flush(false); err != nil {
		return nil, err
	}
	b, err := d.bufimage(1 + 4 + 1 + len(name))
	if err != nil {
		return nil, err
	}
	d.imageid++
	id := d.imageid
	b[0] = 'n'
	putl(b[1:], id)
	b[5] = byte(len(name))
	copy(b[6:], name)
	if err := d.doflush(); err != nil {
		return nil, err
	}
	info := make([]byte, 12*12)
	if _, err := io.ReadFull(d.ctl, info); err != nil {
		return nil, err
	}
	f := strings.Fields(string(info))
	if len(f) < 12 {
		return nil, errors.New("namedimage: short info")
	}
	atoi := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	ch := ParseChan(f[2])
	return &Image{Display: d, ID: id, Chan: ch, Depth: ch.Depth(), Repl: atoi(3) != 0,
		R: Rect(atoi(4), atoi(5), atoi(6), atoi(7)), Clipr: Rect(atoi(8), atoi(9), atoi(10), atoi(11))}, nil
}
