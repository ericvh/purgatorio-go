// Command dd copies and converts data in blocks (Inferno dd(1)).
// Options are -name value pairs: -if, -of, -ibs, -obs, -bs, -cbs,
// -skip, -iseek, -seek (or -oseek), -count, -files and -conv.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const big = 2147483647

// conv flags
const (
	lcase = 1 << iota
	ucase
	swab
	nerr
	sync
)

// conversion types
const (
	cNull = iota
	cCnull
	cEbcdic
	cIbm
	cASCII
	cBlock
	cUnblock
)

var (
	cflag, ctype           int
	ibs, obs, bs, cbs      = 512, 512, 0, 0
	skip, iseekn, oseekn   int
	count                  int
	files                  = 1
	ibuf, obuf             []byte
	op, obc, cbc           int
	nifr, nipr, nofr, nopr int
	ntrunc, nspace         int
	ibf, obf               *os.File
	arg                    string
)

func main() {
	var ifile, ofile string
	args := os.Args[1:]
	for len(args) > 0 {
		key := args[0]
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "dd: arg %s needs a value\n", key)
			sys.Exits("fail:arg")
		}
		arg = args[1]
		args = args[2:]
		switch key {
		case "-ibs":
			ibs = number()
		case "-obs":
			obs = number()
		case "-cbs":
			cbs = number()
		case "-bs":
			bs = number()
		case "-if":
			ifile = arg
		case "-of":
			ofile = arg
		case "-skip":
			skip = number()
		case "-seek", "-oseek":
			oseekn = number()
		case "-iseek":
			iseekn = number()
		case "-count":
			count = number()
		case "-files":
			files = number()
		case "-conv":
			convopts()
		default:
			fmt.Fprintf(os.Stderr, "dd: bad arg: %s\n", key)
			sys.Exits("fail:arg")
		}
	}
	if ctype == cNull && cflag&(lcase|ucase) != 0 {
		ctype = cCnull
	}
	if ifile != "" {
		fd, err := sys.Open(ifile, sys.OREAD)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dd: open %s: %s\n", ifile, sys.Errstr())
			sys.Exits("fail:open")
		}
		ibf = os.NewFile(uintptr(fd), ifile)
	} else {
		fd, _ := sys.Dup(0, -1)
		ibf = os.NewFile(uintptr(fd), "stdin")
	}
	if ofile != "" {
		fd, err := sys.Create(ofile, sys.OWRITE, 0o664)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dd: create %s: %s\n", ofile, sys.Errstr())
			sys.Exits("fail:create")
		}
		obf = os.NewFile(uintptr(fd), ofile)
	} else {
		fd, err := sys.Dup(1, -1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dd: can't dup file descriptor: %s\n", sys.Errstr())
			sys.Exits("fail:dup")
		}
		obf = os.NewFile(uintptr(fd), "stdout")
	}
	if bs != 0 {
		ibs, obs = bs, bs
	}
	fflag := ibs == obs && ctype == cNull
	if ibs == 0 || obs == 0 {
		os.Stderr.WriteString("dd: counts: cannot be zero\n")
		sys.Exits("fail:counts")
	}
	ibuf = make([]byte, ibs)
	obuf = make([]byte, obs)
	if fflag {
		obuf = ibuf
	}
	sys.Seek(int(obf.Fd()), int64(obs)*int64(oseekn), 1)
	sys.Seek(int(ibf.Fd()), int64(ibs)*int64(iseekn), 1)
	for ; skip > 0; skip-- {
		read(ibuf)
	}
	for {
		ibc := 0
		if count == 0 || nifr+nipr != count {
			if cflag&(nerr|sync) != 0 {
				clear(ibuf)
			}
			ibc = read(ibuf)
		}
		if ibc == -1 {
			fmt.Fprintf(os.Stderr, "read: %s\n", sys.Errstr())
			if cflag&nerr == 0 {
				flsh()
				term()
			}
			ibc = 0
			for c := range ibuf {
				if ibuf[c] != 0 {
					ibc = c
				}
			}
			stats()
		}
		if ibc == 0 {
			files--
			if files <= 0 {
				flsh()
				term()
			}
		}
		if ibc != ibs {
			nipr++
			if cflag&sync != 0 {
				ibc = ibs
			}
		} else {
			nifr++
		}
		if cflag&swab != 0 {
			for c, ip := (ibc>>1)&^1, 0; c > 0; c, ip = c-1, ip+2 {
				ibuf[ip], ibuf[ip+1] = ibuf[ip+1], ibuf[ip]
			}
		}
		if fflag {
			obc = ibc
			flsh()
			continue
		}
		for _, c := range ibuf[:ibc] {
			conv(int(c))
		}
	}
}

// read is one read of the input: the count, 0 at the end, -1 on error.
func read(b []byte) int {
	n, err := ibf.Read(b)
	if err == io.EOF {
		return 0
	}
	if err != nil {
		return -1
	}
	return n
}

func convopts() {
	words := []struct {
		s     string
		ctype int
		flag  int
	}{
		{",", -1, 0},
		{"ebcdic", cEbcdic, 0},
		{"ibm", cIbm, 0},
		{"ascii", cASCII, 0},
		{"block", cBlock, 0},
		{"unblock", cUnblock, 0},
		{"lcase", -1, lcase},
		{"ucase", -1, ucase},
		{"swab", -1, swab},
		{"noerror", -1, nerr},
		{"sync", -1, sync},
	}
	// Words are matched as prefixes in turn; anything else stops the
	// scan, as in Inferno.
	for arg != "" {
		found := false
		for _, w := range words {
			if len(arg) >= len(w.s) && arg[:len(w.s)] == w.s {
				arg = arg[len(w.s):]
				if w.ctype >= 0 {
					ctype = w.ctype
				}
				cflag |= w.flag
				found = true
				break
			}
		}
		if !found {
			return
		}
	}
}

// number parses a count with optional multipliers: k (1024), b (512)
// and x (times the number that follows).
func number() int {
	n := 0
	i := 0
	for i < len(arg) && arg[i] >= '0' && arg[i] <= '9' {
		n = n*10 + int(arg[i]-'0')
		i++
		if n >= big {
			break
		}
	}
loop:
	for ; i < len(arg); i++ {
		switch arg[i] {
		case 'k':
			n *= 1024
		case 'b':
			n *= 512
		case 'x':
			arg = arg[i+1:]
			n *= number()
			break loop
		}
		if n >= big {
			break
		}
	}
	if n >= big || n < 0 {
		os.Stderr.WriteString("dd: argument out of range\n")
		sys.Exits("fail:range")
	}
	return n
}

func flsh() {
	if obc == 0 {
		return
	}
	if obc == obs {
		nofr++
	} else {
		nopr++
	}
	if _, err := obf.Write(obuf[:obc]); err != nil {
		fmt.Fprintf(os.Stderr, "write: %s\n", sys.Errstr())
		term()
	}
	obc = 0
}

func term() {
	stats()
	sys.Exits("")
}

func stats() {
	fmt.Fprintf(os.Stderr, "%d+%d records in\n", nifr, nipr)
	fmt.Fprintf(os.Stderr, "%d+%d records out\n", nofr, nopr)
	if ntrunc != 0 {
		fmt.Fprintf(os.Stderr, "%d truncated records\n", ntrunc)
	}
}

func casemap(c int) int {
	if cflag&ucase != 0 && c >= 'a' && c <= 'z' {
		c += 'A' - 'a'
	}
	if cflag&lcase != 0 && c >= 'A' && c <= 'Z' {
		c += 'a' - 'A'
	}
	return c
}

func conv(c int) {
	switch ctype {
	case cNull:
		null(c)
	case cCnull:
		null(casemap(c))
	case cASCII:
		unblock(int(etoa[c]))
	case cUnblock:
		unblock(c)
	case cEbcdic:
		block(c, atoe[:])
	case cIbm:
		block(c, atoibm[:])
	case cBlock:
		block(c, nil)
	}
}

func null(c int) {
	obuf[op] = byte(c)
	op++
	obc++
	if obc >= obs {
		flsh()
		op = 0
	}
}

// unblock turns cbs-byte records into lines, dropping trailing blanks.
func unblock(c int) {
	if cbs == 0 {
		null(casemap(c))
		return
	}
	if c == ' ' {
		nspace++
	} else {
		for ; nspace > 0; nspace-- {
			null(' ')
		}
		null(casemap(c))
	}
	cbc++
	if cbc >= cbs {
		null('\n')
		cbc = 0
		nspace = 0
	}
}

// block turns lines into cbs-byte records padded with blanks, mapping
// each byte through tab when it is not nil.
func block(cc int, tab []byte) {
	c := casemap(cc)
	mapc := func(c int) int {
		if tab != nil {
			return int(tab[c])
		}
		return c
	}
	if cbs == 0 {
		null(mapc(c))
		return
	}
	if cc == '\n' {
		for ; cbc < cbs; cbc++ {
			null(mapc(' '))
		}
		cbc = 0
		return
	}
	if cbc == cbs {
		ntrunc++
	}
	cbc++
	if cbc <= cbs {
		null(mapc(c))
	}
}

var atoe, atoibm [256]byte

func init() {
	for i, c := range etoa {
		atoe[c] = byte(i)
	}
	atoibm = atoe
	// IBM's variant differs for ! [ ] and |.
	atoibm['!'] = 0x5a
	atoibm['['] = 0xad
	atoibm[']'] = 0xbd
	atoibm['|'] = 0x4f
}

// etoa maps EBCDIC to ASCII; atoe is its inverse.
var etoa = [256]byte{
	0x00, 0x01, 0x02, 0x03, 0x9c, 0x09, 0x86, 0x7f, 0x97, 0x8d, 0x8e, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x9d, 0x85, 0x08, 0x87, 0x18, 0x19, 0x92, 0x8f, 0x1c, 0x1d, 0x1e, 0x1f,
	0x80, 0x81, 0x82, 0x83, 0x84, 0x0a, 0x17, 0x1b, 0x88, 0x89, 0x8a, 0x8b, 0x8c, 0x05, 0x06, 0x07,
	0x90, 0x91, 0x16, 0x93, 0x94, 0x95, 0x96, 0x04, 0x98, 0x99, 0x9a, 0x9b, 0x14, 0x15, 0x9e, 0x1a,
	0x20, 0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0x5b, 0x2e, 0x3c, 0x28, 0x2b, 0x21,
	0x26, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0x5d, 0x24, 0x2a, 0x29, 0x3b, 0x5e,
	0x2d, 0x2f, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0x7c, 0x2c, 0x25, 0x5f, 0x3e, 0x3f,
	0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf, 0xc0, 0xc1, 0xc2, 0x60, 0x3a, 0x23, 0x40, 0x27, 0x3d, 0x22,
	0xc3, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9,
	0xca, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0xcb, 0xcc, 0xcd, 0xce, 0xcf, 0xd0,
	0xd1, 0x7e, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7,
	0xd8, 0xd9, 0xda, 0xdb, 0xdc, 0xdd, 0xde, 0xdf, 0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
	0x7b, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed,
	0x7d, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50, 0x51, 0x52, 0xee, 0xef, 0xf0, 0xf1, 0xf2, 0xf3,
	0x5c, 0x9f, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9,
	0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff,
}
