// Package ustar reads and writes POSIX.1 tar archives, for gettar,
// puttar, lstar and tar. Blocks are 512 bytes, read and written in
// records of 20 as Inferno's gettar(1) does.
//
// Block, Parse and the Writer are the format as Inferno's commands use
// it. Archive adds what tar needs to read other systems' archives: GNU
// long names (L and K) and pax path, linkpath, size and mtime records.
// Numbers too big for their octal fields are read and written in GNU's
// base-256.
package ustar

import (
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	BlockSize = 512
	NBlock    = 20 // blocks in a record
)

// ErrBlocksize is a read that was not a whole number of blocks.
var ErrBlocksize = errors.New("blocksize error")

// Reader reads an archive a block at a time.
type Reader struct {
	r     io.Reader
	full  bool
	buf   []byte
	nblk  int
	recno int
}

// NewReader reads r a record at a time, as Inferno's gettar does: each
// read must return whole blocks.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r, buf: make([]byte, NBlock*BlockSize), nblk: NBlock, recno: NBlock}
}

// NewFullReader is NewReader, but fills each record before using it, so
// that r may return any number of bytes at a time, as a pipe from gunzip
// does.
func NewFullReader(r io.Reader) *Reader {
	br := NewReader(r)
	br.full = true
	return br
}

// Block returns the next block, or nil at the end of the input. The
// block is valid until the next call.
func (r *Reader) Block() ([]byte, error) {
	if r.recno >= r.nblk {
		var i int
		var err error
		if r.full {
			i, err = io.ReadFull(r.r, r.buf)
			if err == io.ErrUnexpectedEOF {
				err = nil
			}
		} else {
			i, err = r.r.Read(r.buf)
		}
		if i == 0 && (err == io.EOF || err == nil) {
			return nil, nil
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if i%BlockSize != 0 {
			return nil, ErrBlocksize
		}
		r.nblk = i / BlockSize
		r.recno = 0
	}
	r.recno++
	return r.buf[(r.recno-1)*BlockSize : r.recno*BlockSize], nil
}

// Header is one archive member's header.
type Header struct {
	Name         string // with the prefix field, if any
	Mode         int    // permission bits
	Size, Mtime  int64
	Type         byte // the typeflag: '0', '5', ...
	Link         string
	Uname, Gname string
}

// HeaderError is a bad header: What is "bad magic" or "directory
// checksum error", and Name the member's name.
type HeaderError struct{ What, Name string }

func (e *HeaderError) Error() string { return e.What + " " + e.Name }

// Parse reads the header in block b, checking its magic and checksum;
// the error is a *HeaderError.
func Parse(b []byte) (*Header, error) {
	name := Nullterm(b[0:100])
	if b[345] != 0 {
		name = Nullterm(b[345:500]) + "/" + name
	}
	h := &Header{Name: name}
	magic := b[257:262]
	if magic[0] != 0 && string(magic) != "ustar" {
		return h, &HeaderError{"bad magic", name}
	}
	chk := Octal(b[148:156])
	sum, ssum := 0, 0
	for i, c := range b {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int(c)
		ssum += int(int8(c))
	}
	if chk != sum && chk != ssum {
		return h, &HeaderError{"directory checksum error", name}
	}
	h.Type = b[156]
	h.Mode = 0o777 & Octal(b[100:108])
	h.Size = Number(b[124:136])
	h.Mtime = Number(b[136:148])
	h.Link = Nullterm(b[157:257])
	if string(magic) == "ustar" {
		h.Uname = Nullterm(b[265:297])
		h.Gname = Nullterm(b[297:329])
	}
	return h, nil
}

// Octal is the value of an octal field, ignoring spaces and stopping at
// a NUL, as Inferno's gettar reads it.
func Octal(b []byte) int {
	sum := 0
	for _, c := range b {
		if c == ' ' {
			continue
		}
		if c == 0 {
			break
		}
		sum = 8*sum + int(c) - '0'
	}
	return sum
}

// Number is Octal, or GNU's base-256 when the field's top bit is set.
func Number(b []byte) int64 {
	if len(b) > 0 && b[0]&0x80 != 0 {
		v := int64(b[0] & 0x7f)
		for _, c := range b[1:] {
			v = v<<8 | int64(c)
		}
		return v
	}
	return int64(Octal(b))
}

// Nullterm is b up to its first NUL.
func Nullterm(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// PutOctal writes v into b as width octal digits, without a terminator.
func PutOctal(b []byte, width int, v uint64) {
	for i := width - 1; i >= 0; i-- {
		b[i] = byte('0' + v&7)
		v >>= 3
	}
}

// PutNumber writes v into the field b as octal with a NUL when it fits,
// and otherwise in base-256.
func PutNumber(b []byte, v uint64) {
	w := len(b) - 1
	if v < 1<<(3*w) {
		PutOctal(b, w, v)
		b[w] = 0
		return
	}
	clear(b)
	for i := len(b) - 1; i > 0 && v != 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	b[0] = 0x80
}

// The errors of PackName, which puttar prints after the name.
var (
	ErrNoSplit   = errors.New("> 100 bytes")
	ErrPrefixLen = errors.New("too long")
)

// PackName puts name in the header's name field, or splits it at a
// slash between the prefix and name fields, as Inferno's puttar does.
func PackName(hdr []byte, name string) error {
	utf := []byte(name)
	n := len(utf)
	if n <= 100 {
		copy(hdr, utf)
		return nil
	}
	i := n - 101
	for i < n && utf[i] != '/' {
		i++
	}
	if i == n {
		return ErrNoSplit
	}
	if i > 155 {
		return ErrPrefixLen
	}
	copy(hdr, utf[i+1:])
	copy(hdr[345:], utf[:i]) // tar supplies the slash
	return nil
}

// Checksum fills in the header's checksum, as puttar does: seven octal
// digits and a NUL.
func Checksum(hdr []byte) {
	copy(hdr[148:156], "        ")
	sum := 0
	for _, c := range hdr[:BlockSize] {
		sum += int(c)
	}
	PutOctal(hdr[148:], 7, uint64(sum))
	hdr[155] = 0
}

// Pack makes a ustar header block for h; a directory's name gets a
// trailing slash.
func (h *Header) Pack() ([]byte, error) {
	b := make([]byte, BlockSize)
	name := h.Name
	if h.Type == '5' && !strings.HasSuffix(name, "/") {
		name += "/"
	}
	if err := PackName(b, name); err != nil {
		return nil, err
	}
	PutNumber(b[100:108], uint64(h.Mode&0o7777))
	PutNumber(b[108:116], 0)
	PutNumber(b[116:124], 0)
	PutNumber(b[124:136], uint64(h.Size))
	PutNumber(b[136:148], uint64(h.Mtime))
	b[156] = h.Type
	copy(b[157:256], h.Link)
	copy(b[257:], "ustar\x0000")
	copy(b[265:296], h.Uname)
	copy(b[297:328], h.Gname)
	PutNumber(b[329:337], 0)
	PutNumber(b[337:345], 0)
	Checksum(b)
	return b, nil
}

// Writer writes an archive a block at a time, a record per write.
type Writer struct {
	w    io.Writer
	buf  []byte
	nblk int
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, buf: make([]byte, NBlock*BlockSize)}
}

// Block adds one block; data shorter than a block is padded with zeros.
func (w *Writer) Block(data []byte) error {
	b := w.buf[w.nblk*BlockSize : (w.nblk+1)*BlockSize]
	clear(b[copy(b, data):])
	w.nblk++
	if w.nblk >= NBlock {
		return w.Flush()
	}
	return nil
}

// Flush writes the record, padded with zero blocks.
func (w *Writer) Flush() error {
	if w.nblk <= 0 {
		return nil
	}
	clear(w.buf[w.nblk*BlockSize:])
	w.nblk = 0
	_, err := w.w.Write(w.buf)
	return err
}

// Close ends the archive with two zero blocks and flushes it.
func (w *Writer) Close() error {
	if err := w.Block(nil); err != nil {
		return err
	}
	if err := w.Block(nil); err != nil {
		return err
	}
	return w.Flush()
}

// Archive reads the members of an archive.
type Archive struct {
	r    *Reader
	left int64 // data of the current member not yet read
	part []byte
}

// NewArchive reads the members of the archive r reads.
func NewArchive(r *Reader) *Archive { return &Archive{r: r} }

// Next returns the next member's header, or nil at the end. GNU long
// names and pax records are applied to the member they describe.
func (a *Archive) Next() (*Header, error) {
	var longname, longlink string
	pax := map[string]string{}
	for {
		for a.left > 0 {
			b, err := a.r.Block()
			if err != nil {
				return nil, err
			}
			if b == nil {
				return nil, io.ErrUnexpectedEOF
			}
			a.left -= BlockSize
		}
		a.left, a.part = 0, nil
		b, err := a.r.Block()
		if err != nil || b == nil || b[0] == 0 {
			return nil, err
		}
		h, err := Parse(b)
		if err != nil {
			return nil, err
		}
		switch h.Type {
		case 'L', 'K', 'x', 'g':
			a.left = h.Size
			data, err := io.ReadAll(a)
			if err != nil {
				return nil, err
			}
			switch h.Type {
			case 'L':
				longname = Nullterm(data)
			case 'K':
				longlink = Nullterm(data)
			case 'x':
				parsePax(data, pax)
			}
			continue
		}
		if longname != "" {
			h.Name = longname
		}
		if longlink != "" {
			h.Link = longlink
		}
		if v, ok := pax["path"]; ok {
			h.Name = v
		}
		if v, ok := pax["linkpath"]; ok {
			h.Link = v
		}
		if v, err := strconv.ParseInt(pax["size"], 10, 64); err == nil {
			h.Size = v
		}
		if v, ok := pax["mtime"]; ok {
			if s, _, _ := strings.Cut(v, "."); s != "" {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil {
					h.Mtime = n
				}
			}
		}
		if h.Type != '5' {
			a.left = h.Size
		}
		return h, nil
	}
}

// parsePax reads pax records, "len key=value\n".
func parsePax(data []byte, m map[string]string) {
	for len(data) > 0 {
		sp := strings.IndexByte(string(data), ' ')
		if sp < 0 {
			return
		}
		n, err := strconv.Atoi(string(data[:sp]))
		if err != nil || n <= sp || n > len(data) {
			return
		}
		rec := string(data[sp+1 : n])
		data = data[n:]
		if k, v, ok := strings.Cut(strings.TrimSuffix(rec, "\n"), "="); ok {
			m[k] = v
		}
	}
}

// Read reads the current member's data.
func (a *Archive) Read(p []byte) (int, error) {
	if len(a.part) == 0 {
		if a.left <= 0 {
			return 0, io.EOF
		}
		b, err := a.r.Block()
		if err != nil {
			return 0, err
		}
		if b == nil {
			return 0, io.ErrUnexpectedEOF
		}
		a.part = b[:min(int64(BlockSize), a.left)]
		a.left -= BlockSize
		if a.left < 0 {
			a.left = 0
		}
	}
	n := copy(p, a.part)
	a.part = a.part[n:]
	return n, nil
}
