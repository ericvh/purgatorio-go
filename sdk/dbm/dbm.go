// Package dbm keeps key/content pairs in a data base of two files, in
// the format of Inferno's dbm(2), itself the Unix V7 dbm: a bit map in
// file.dir and the data in 512-byte pages in file.pag. Data bases are
// interchangeable with Inferno's.
//
// Each page holds a count and a table of offsets at its start, both
// big-endian 16-bit, and the items packed down from its end, keys and
// contents alternating. A key's page is chosen by its hash and the bit
// map, which records the pages that have been split.
package dbm

import (
	"errors"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	bytesiz  = 8 // bits
	shortsiz = 2 // bytes

	// PBLKSIZ is the page size and DBLKSIZ the bit map block size.
	PBLKSIZ = 512
	DBLKSIZ = 8192 // Inferno's; V7's was 4096
)

// File is what a data base is kept in.
type File interface {
	io.ReaderAt
	io.WriterAt
}

// Dbf is an open data base. As in Inferno, it must not be used by
// several procs at once.
type Dbf struct {
	pagf, dirf File
	rdonly     bool
	closers    []io.Closer

	maxbno int // last bit number in the dir file
	bitno  int
	hmask  int
	blkno  int // current page to read or write
	pagbno int // current page in pagbuf
	pagbuf [PBLKSIZ]byte
	dirbno int // current block in dirbuf
	dirbuf [DBLKSIZ]byte
}

// Error is the error a damaged data base or a failed read or write
// gives; Inferno's dbm raises it as an exception.
type Error string

func (e Error) Error() string { return string(e) }

// Create makes the data base file (file.pag and file.dir) with
// permissions perm, truncating an existing one, open for reading and
// writing.
func Create(file string, perm uint32) (*Dbf, error) {
	pf, err := sys.Create(file+".pag", sys.ORDWR, perm)
	if err != nil {
		return nil, err
	}
	df, err := sys.Create(file+".dir", sys.ORDWR, perm)
	if err != nil {
		sys.Close(pf)
		return nil, err
	}
	return alloc(pf, df, false)
}

// Open opens the existing data base file with mode sys.OREAD, or
// sys.ORDWR (sys.OWRITE is taken as ORDWR).
func Open(file string, mode int) (*Dbf, error) {
	if mode&3 == sys.OWRITE {
		mode = mode&^3 | sys.ORDWR
	}
	pf, err := sys.Open(file+".pag", mode)
	if err != nil {
		return nil, err
	}
	df, err := sys.Open(file+".dir", mode)
	if err != nil {
		sys.Close(pf)
		return nil, err
	}
	return alloc(pf, df, mode&3 == sys.OREAD)
}

func alloc(pf, df int, rdonly bool) (*Dbf, error) {
	var length int64
	if d, err := sys.Fstat(df); err == nil {
		length = int64(d.Length)
	}
	p, d := os.NewFile(uintptr(pf), "pag"), os.NewFile(uintptr(df), "dir")
	db := New(p, d, length, rdonly)
	db.closers = []io.Closer{p, d}
	return db, nil
}

// New makes a Dbf of files already open: pag, and dir whose length is
// dirlen.
func New(pag, dir File, dirlen int64, rdonly bool) *Dbf {
	return &Dbf{pagf: pag, dirf: dir, rdonly: rdonly, pagbno: -1, dirbno: -1,
		maxbno: int(dirlen*bytesiz - 1)}
}

// Close closes the files Open or Create opened.
func (db *Dbf) Close() error {
	var err error
	for _, c := range db.closers {
		if e := c.Close(); e != nil && err == nil {
			err = e
		}
	}
	db.closers = nil
	return err
}

// Flush drops the cached page and bit map block, so the next operation
// reads them again.
func (db *Dbf) Flush() { db.pagbno, db.dirbno = -1, -1 }

// IsRdonly says whether the data base is open only for reading.
func (db *Dbf) IsRdonly() bool { return db.rdonly }

// catch turns the panic of a dbm error into an error.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(Error)
		if !ok {
			panic(r)
		}
		*err = e
	}
}

func fail(s string) { panic(Error(s)) }

// Fetch returns the contents stored under key, or nil if there is none.
func (db *Dbf) Fetch(key []byte) (item []byte, err error) {
	defer catch(&err)
	db.access(calchash(key))
	for i := 0; ; i += 2 {
		item := makdatum(db.pagbuf[:], i)
		if item == nil {
			return nil, nil
		}
		if cmpdatum(key, item) == 0 {
			item = makdatum(db.pagbuf[:], i+1)
			if item == nil {
				fail("dbm: items not in pairs")
			}
			return clone(item), nil
		}
	}
}

// Delete removes key and its contents. It returns -1 if key is not
// there or the data base is read-only, and 0 otherwise.
func (db *Dbf) Delete(key []byte) (r int, err error) {
	defer catch(&err)
	if db.rdonly {
		return -1, nil
	}
	db.access(calchash(key))
	for i := 0; ; i += 2 {
		item := makdatum(db.pagbuf[:], i)
		if item == nil {
			return -1, nil
		}
		if cmpdatum(key, item) == 0 {
			delitem(db.pagbuf[:], i)
			delitem(db.pagbuf[:], i)
			break
		}
	}
	db.writepag(db.blkno, db.pagbuf[:])
	db.pagbno = db.blkno
	return 0, nil
}

// Store stores dat under key. If key is there already, Store replaces
// its contents if replace is set and otherwise returns 1. It returns -1
// if the data base is read-only or the pair cannot fit in a page, and 0
// when it has stored the pair.
func (db *Dbf) Store(key, dat []byte, replace bool) (r int, err error) {
	defer catch(&err)
	if db.rdonly {
		return -1, nil
	}
	for {
		db.access(calchash(key))
		for i := 0; ; i += 2 {
			item := makdatum(db.pagbuf[:], i)
			if item == nil {
				break
			}
			if cmpdatum(key, item) == 0 {
				if !replace {
					return 1, nil
				}
				delitem(db.pagbuf[:], i)
				delitem(db.pagbuf[:], i)
				break
			}
		}
		if i := additem(db.pagbuf[:], key); i >= 0 {
			if additem(db.pagbuf[:], dat) >= 0 {
				break
			}
			delitem(db.pagbuf[:], i)
		}
		if !db.split(key, dat) {
			return -1, nil
		}
	}
	db.writepag(db.blkno, db.pagbuf[:])
	db.pagbno = db.blkno
	return 0, nil
}

func (db *Dbf) split(key, dat []byte) bool {
	if len(key)+len(dat)+3*shortsiz >= PBLKSIZ {
		return false
	}
	var ovfbuf [PBLKSIZ]byte
	for i := 0; ; {
		item := makdatum(db.pagbuf[:], i)
		if item == nil {
			break
		}
		if calchash(item)&(db.hmask+1) != 0 {
			additem(ovfbuf[:], item)
			delitem(db.pagbuf[:], i)
			item = makdatum(db.pagbuf[:], i)
			if item == nil {
				fail("dbm: split not paired")
			}
			additem(ovfbuf[:], item)
			delitem(db.pagbuf[:], i)
			continue
		}
		i += 2
	}
	db.writepag(db.blkno, db.pagbuf[:])
	db.pagbno = db.blkno
	db.writepag(db.blkno+db.hmask+1, ovfbuf[:])
	db.setbit()
	return true
}

// Firstkey returns the first key of the data base, or nil if it is
// empty. Keys come in no useful order.
func (db *Dbf) Firstkey() (key []byte, err error) {
	defer catch(&err)
	return clone(db.firsthash(0)), nil
}

// Nextkey returns the key after key, or nil after the last.
func (db *Dbf) Nextkey(key []byte) (next []byte, err error) {
	defer catch(&err)
	hash := calchash(key)
	db.access(hash)
	var bitem []byte
	for i := 0; ; i += 2 {
		item := makdatum(db.pagbuf[:], i)
		if item == nil {
			break
		}
		if cmpdatum(key, item) <= 0 {
			continue
		}
		if bitem == nil || cmpdatum(bitem, item) < 0 {
			bitem = item
		}
	}
	if bitem != nil {
		return clone(bitem), nil
	}
	if hash = db.hashinc(hash); hash == 0 {
		return nil, nil
	}
	return clone(db.firsthash(hash)), nil
}

func (db *Dbf) firsthash(hash int) []byte {
	for {
		db.access(hash)
		bitem := makdatum(db.pagbuf[:], 0)
		for i := 2; ; i += 2 {
			item := makdatum(db.pagbuf[:], i)
			if item == nil {
				break
			}
			if cmpdatum(bitem, item) < 0 {
				bitem = item
			}
		}
		if bitem != nil {
			return bitem
		}
		if hash = db.hashinc(hash); hash == 0 {
			return nil
		}
	}
}

func (db *Dbf) access(hash int) {
	for db.hmask = 0; ; db.hmask = db.hmask<<1 + 1 {
		db.blkno = hash & db.hmask
		db.bitno = db.blkno + db.hmask
		if !db.getbit() {
			break
		}
	}
	if db.blkno != db.pagbno {
		read(db.pagf, db.pagbuf[:], int64(db.blkno)*PBLKSIZ)
		chkblk(db.pagbuf[:])
		db.pagbno = db.blkno
	}
}

func (db *Dbf) getbit() bool {
	if db.bitno > db.maxbno {
		return false
	}
	n := db.bitno % bytesiz
	bn := db.bitno / bytesiz
	i := bn % DBLKSIZ
	b := bn / DBLKSIZ
	if b != db.dirbno {
		read(db.dirf, db.dirbuf[:], int64(b)*DBLKSIZ)
		db.dirbno = b
	}
	return db.dirbuf[i]&(1<<n) != 0
}

func (db *Dbf) setbit() {
	if db.bitno > db.maxbno {
		db.maxbno = db.bitno
		db.getbit()
	}
	n := db.bitno % bytesiz
	bn := db.bitno / bytesiz
	i := bn % DBLKSIZ
	b := bn / DBLKSIZ
	db.dirbuf[i] |= 1 << n
	write(db.dirf, db.dirbuf[:], int64(b)*DBLKSIZ)
	db.dirbno = b
}

func (db *Dbf) writepag(blkno int, buf []byte) {
	write(db.pagf, buf, int64(blkno)*PBLKSIZ)
}

func (db *Dbf) hashinc(hash int) int {
	hash &= db.hmask
	bit := db.hmask + 1
	for {
		bit >>= 1
		if bit == 0 {
			return 0
		}
		if hash&bit == 0 {
			return hash | bit
		}
		hash &^= bit
	}
}

// makdatum returns item n of page buf, or nil if there is none.
func makdatum(buf []byte, n int) []byte {
	ne := gets(buf, 0)
	if n < 0 || n >= ne {
		return nil
	}
	t := PBLKSIZ
	if n > 0 {
		t = gets(buf, n)
	}
	v := gets(buf, n+1)
	if v > t { // chkblk has rejected such pages
		fail("dbm: bad block")
	}
	return buf[v:t:t]
}

func cmpdatum(d1, d2 []byte) int {
	n := len(d1)
	if n != len(d2) {
		return n - len(d2)
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			return int(d1[i]) - int(d2[i])
		}
	}
	return 0
}

func clone(d []byte) []byte {
	if d == nil {
		return nil
	}
	return append(make([]byte, 0, len(d)), d...)
}

var hitab = [16]int32{
	61, 57, 53, 49, 45, 41, 37, 33,
	29, 25, 21, 17, 13, 9, 5, 1,
}

var hltab = [64]int32{
	0o6100151277, 0o6106161736, 0o6452611562, 0o5001724107,
	0o2614772546, 0o4120731531, 0o4665262210, 0o7347467531,
	0o6735253126, 0o6042345173, 0o3072226605, 0o1464164730,
	0o3247435524, 0o7652510057, 0o1546775256, 0o5714532133,
	0o6173260402, 0o7517101630, 0o2431460343, 0o1743245566,
	0o0261675137, 0o2433103631, 0o3421772437, 0o4447707466,
	0o4435620103, 0o3757017115, 0o3641531772, 0o6767633246,
	0o2673230344, 0o0260612216, 0o4133454451, 0o0615531516,
	0o6137717526, 0o2574116560, 0o2304023373, 0o7061702261,
	0o5153031405, 0o5322056705, 0o7401116734, 0o6552375715,
	0o6165233473, 0o5311063631, 0o1212221723, 0o1052267235,
	0o6000615237, 0o1075222665, 0o6330216006, 0o4402355630,
	0o1451177262, 0o2000133436, 0o6025467062, 0o7121076461,
	0o3123433522, 0o1010635225, 0o1716177066, 0o5161746527,
	0o1736635071, 0o6243505026, 0o3637211610, 0o1756474365,
	0o4723077174, 0o3642763134, 0o5750130273, 0o3655541561,
}

// calchash is the V7 hash, in 32-bit arithmetic as Limbo's int.
func calchash(item []byte) int {
	var hashl, hashi int32
	for _, c := range item {
		f := int32(c)
		for j := 0; j < bytesiz; j += 4 {
			hashi += hitab[f&0xF]
			hashl += hltab[hashi&0x3F]
			f >>= 4
		}
	}
	return int(hashl)
}

func delitem(buf []byte, n int) {
	ne := gets(buf, 0)
	if n < 0 || n >= ne {
		fail("dbm: bad delitem")
	}
	i1 := gets(buf, n+1)
	i2 := PBLKSIZ
	if n > 0 {
		i2 = gets(buf, n)
	}
	i3 := gets(buf, ne)
	if i2 > i1 {
		for i1 > i3 {
			i1--
			i2--
			buf[i2] = buf[i1]
			buf[i1] = 0
		}
	}
	i2 -= i1
	for i1 = n + 1; i1 < ne; i1++ {
		puts(buf, i1, gets(buf, i1+1)+i2)
	}
	puts(buf, 0, ne-1)
	puts(buf, ne, 0)
}

func additem(buf []byte, item []byte) int {
	i1 := PBLKSIZ
	ne := gets(buf, 0)
	if ne > 0 {
		i1 = gets(buf, ne)
	}
	i1 -= len(item)
	i2 := (ne + 2) * shortsiz
	if i1 <= i2 {
		return -1
	}
	puts(buf, ne+1, i1)
	copy(buf[i1:], item)
	puts(buf, 0, ne+1)
	return ne
}

func chkblk(buf []byte) {
	t := PBLKSIZ
	ne := gets(buf, 0)
	if (ne+1)*shortsiz > PBLKSIZ {
		badblk()
	}
	for i := 0; i < ne; i++ {
		v := gets(buf, i+1)
		if v > t {
			badblk()
		}
		t = v
	}
	if t < (ne+1)*shortsiz {
		badblk()
	}
}

func badblk() { fail("dbm: bad block") }

// read fills buf from f at off; nothing there reads as zeros.
func read(f File, buf []byte, off int64) {
	n, err := f.ReadAt(buf, off)
	if n == 0 && (err == nil || errors.Is(err, io.EOF)) {
		clear(buf)
	} else if n != len(buf) {
		fail("dbm: read error: " + errstr(err))
	}
}

func write(f File, buf []byte, off int64) {
	if n, err := f.WriteAt(buf, off); n != len(buf) {
		fail("dbm: write error: " + errstr(err))
	}
}

func errstr(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "short read"
	}
	return sys.Errstr()
}

func gets(buf []byte, sh int) int {
	sh *= shortsiz
	return int(buf[sh])<<8 | int(buf[sh+1])
}

func puts(buf []byte, sh, v int) {
	sh *= shortsiz
	buf[sh] = byte(v >> 8)
	buf[sh+1] = byte(v)
}
