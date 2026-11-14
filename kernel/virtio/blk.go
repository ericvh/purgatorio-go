package virtio

import (
	"encoding/binary"
	"errors"
)

// Block device features and requests (section 5.2).
const (
	blkFeatureRO    = 1 << 5
	blkFeatureFlush = 1 << 9

	blkIn    = 0
	blkOut   = 1
	blkFlush = 4

	blkOK = 0
)

// SectorSize is the unit of a virtio block device's addresses.
const SectorSize = 512

// maxXfer bounds one request's data.
const maxXfer = 64 * 1024

// Block is a virtio block device.
type Block struct {
	q        *Queue
	Sectors  uint64
	ReadOnly bool
	flush    bool
	// Requests take a slot; each has its own header, status and bounce
	// buffer on the heap, which the device addresses directly.
	slots chan *blkslot
}

type blkslot struct {
	hdr    []byte
	data   []byte
	status []byte
}

// NewBlock starts the block device d.
func NewBlock(d *Device) (*Block, error) {
	if err := d.Init(blkFeatureRO | blkFeatureFlush); err != nil {
		return nil, err
	}
	q, err := d.NewQueue(0, 64)
	if err != nil {
		return nil, err
	}
	b := &Block{q: q, Sectors: d.Config64(0), ReadOnly: d.Features&blkFeatureRO != 0,
		flush: d.Features&blkFeatureFlush != 0, slots: make(chan *blkslot, 4)}
	for i := 0; i < cap(b.slots); i++ {
		b.slots <- &blkslot{hdr: make([]byte, 16), data: make([]byte, maxXfer), status: make([]byte, 1)}
	}
	d.Ready()
	return b, nil
}

var errIO = errors.New("i/o error")

// do runs one request of n bytes at sector.
func (b *Block) do(typ uint32, sector uint64, buf []byte) error {
	s := <-b.slots
	defer func() { b.slots <- s }()
	binary.LittleEndian.PutUint32(s.hdr[0:], typ)
	binary.LittleEndian.PutUint32(s.hdr[4:], 0)
	binary.LittleEndian.PutUint64(s.hdr[8:], sector)
	s.status[0] = 0xff
	bufs := []Buf{{B: s.hdr}}
	data := s.data[:len(buf)]
	switch typ {
	case blkIn:
		bufs = append(bufs, Buf{B: data, Write: true})
	case blkOut:
		copy(data, buf)
		bufs = append(bufs, Buf{B: data})
	}
	bufs = append(bufs, Buf{B: s.status, Write: true})
	b.q.Do(bufs...)
	if s.status[0] != blkOK {
		return errIO
	}
	if typ == blkIn {
		copy(buf, data)
	}
	return nil
}

// ReadAt reads whole sectors from sector; len(buf) is a multiple of
// SectorSize.
func (b *Block) ReadAt(buf []byte, sector uint64) error {
	return b.xfer(blkIn, buf, sector)
}

// WriteAt writes whole sectors at sector.
func (b *Block) WriteAt(buf []byte, sector uint64) error {
	if b.ReadOnly {
		return errors.New("read only")
	}
	return b.xfer(blkOut, buf, sector)
}

func (b *Block) xfer(typ uint32, buf []byte, sector uint64) error {
	if len(buf)%SectorSize != 0 {
		return errors.New("unaligned transfer")
	}
	for len(buf) > 0 {
		n := min(len(buf), maxXfer)
		if err := b.do(typ, sector, buf[:n]); err != nil {
			return err
		}
		buf = buf[n:]
		sector += uint64(n / SectorSize)
	}
	return nil
}

// Flush asks the device to make written data durable.
func (b *Block) Flush() error {
	if !b.flush {
		return nil
	}
	return b.do(blkFlush, 0, nil)
}
