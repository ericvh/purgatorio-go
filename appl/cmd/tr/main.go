// Command tr translates, deletes or squeezes characters
// (Inferno tr(1)).
package main

import (
	"bufio"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const runemax = 0x10FFFF

// bitset is a set of runes.
type bitset []byte

func newset() bitset            { return make(bitset, (runemax+1)/8) }
func (b bitset) set(c int)      { b[c>>3] |= 1 << (c & 7) }
func (b bitset) clear(c int)    { b[c>>3] &^= 1 << (c & 7) }
func (b bitset) has(c int) bool { return b[c>>3]&(1<<(c&7)) != 0 }
func (b bitset) fill(v byte) {
	for i := range b {
		b[i] = v
	}
}

var (
	f, t         = newset(), newset()
	pfrom, pto   *pcb
	cflag, dflag bool
	sflag        bool
	ib           *bufio.Reader
	ob           = bufio.NewWriter(os.Stdout)
)

func usage() {
	os.Stderr.WriteString("Usage: tr [-sdc] [from-set [to-set]]\n")
	sys.Exits("fail: usage")
}

func fail(s string) {
	ob.Flush()
	os.Stderr.WriteString("tr: " + s + "\n")
	sys.Exits("fail: error")
}

func main() {
	a := arg.New(os.Args)
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			sflag = true
		case 'd':
			dflag = true
		case 'c':
			cflag = true
		default:
			usage()
		}
	}
	args := a.Argv()
	argc := len(args)
	if argc > 0 {
		pfrom = newpcb(args[0])
	}
	if argc > 1 {
		pto = newpcb(args[1])
	}
	if argc > 2 {
		usage()
	}
	ib = bufio.NewReader(os.Stdin)
	if dflag {
		if sflag && argc != 2 || !sflag && argc != 1 {
			usage()
		}
		del()
	} else {
		if argc != 2 {
			usage()
		}
		if cflag {
			complement()
		} else {
			translit()
		}
	}
	if err := ob.Flush(); err != nil {
		fail("write error: " + sys.Errstr())
	}
}

func getc() int {
	r, _, err := ib.ReadRune()
	if err != nil {
		return -1
	}
	return int(r)
}

func putc(c int) { ob.WriteRune(rune(c)) }

func del() {
	if cflag {
		f.fill(0xff)
		for c := pfrom.canon(); c >= 0; c = pfrom.canon() {
			f.clear(c)
		}
	} else {
		for c := pfrom.canon(); c >= 0; c = pfrom.canon() {
			f.set(c)
		}
	}
	if sflag {
		for c := pto.canon(); c >= 0; c = pto.canon() {
			t.set(c)
		}
	}
	last := runemax + 1
	for c := getc(); c >= 0; c = getc() {
		if !f.has(c) && (c != last || !t.has(c)) {
			last = c
			putc(c)
		}
	}
}

func complement() {
	lastc, high := 0, 0
	for from := pfrom.canon(); from >= 0; from = pfrom.canon() {
		high = max(high, from)
		f.set(from)
	}
	for cto := pto.canon(); cto >= 0; cto = pto.canon() {
		high = max(high, cto)
		t.set(cto)
	}
	pto.rewind()
	p := make([]int, high+1)
	cto := 0
	for i := 0; i <= high; i++ {
		if !f.has(i) {
			if cto = pto.canon(); cto < 0 {
				cto = lastc
			} else {
				lastc = cto
			}
			p[i] = cto
		} else {
			p[i] = i
		}
	}
	output(p, high, cto)
}

func translit() {
	lastc, high := 0, 0
	for from := pfrom.canon(); from >= 0; from = pfrom.canon() {
		high = max(high, from)
	}
	pfrom.rewind()
	p := make([]int, high+1)
	for i := range p {
		p[i] = i
	}
	for from := pfrom.canon(); from >= 0; from = pfrom.canon() {
		cto := pto.canon()
		if cto < 0 {
			cto = lastc
		} else {
			lastc = cto
		}
		if f.has(from) && p[from] != cto {
			fail("ambiguous translation")
		}
		f.set(from)
		p[from] = cto
		t.set(cto)
	}
	for cto := pto.canon(); cto >= 0; cto = pto.canon() {
		t.set(cto)
	}
	output(p, high, -1)
}

// output maps the input through p. Characters above high map to over in
// complement mode (over >= 0) and to themselves otherwise.
func output(p []int, high, over int) {
	lastc := runemax + 1
	for from := getc(); from >= 0; from = getc() {
		if from <= high {
			from = p[from]
		} else if over >= 0 {
			from = over
		}
		if !sflag || from != lastc || !t.has(from) {
			lastc = from
			putc(from)
		}
	}
}

// pcb parses a set specification: characters, \ escapes (octal, or \x
// and up to six hex digits) and a-z ranges.
type pcb struct {
	spec    []rune
	current int
	last    int
	final   int
}

func newpcb(s string) *pcb { return &pcb{spec: []rune(s), last: -1, final: -1} }

func (p *pcb) rewind() {
	p.current = 0
	p.last, p.final = -1, -1
}

func (p *pcb) getc() int {
	end := len(p.spec)
	if p.current >= end {
		return -1
	}
	s := p.current
	r := int(p.spec[s])
	s++
	if r == '\\' && s < end {
		n := 0
		if r = int(p.spec[s]); r == 'x' {
			s++
			for i := 0; i < 6 && s < end; i++ {
				p.current = s
				r = int(p.spec[s])
				s++
				switch {
				case '0' <= r && r <= '9':
					n = 16*n + r - '0'
				case 'a' <= r && r <= 'f':
					n = 16*n + r - 'a' + 10
				case 'A' <= r && r <= 'F':
					n = 16*n + r - 'A' + 10
				default:
					if i == 0 {
						return 'x'
					}
					return n
				}
			}
			r = n
		} else {
			for i := 0; i < 3 && s < end; i++ {
				p.current = s
				r = int(p.spec[s])
				s++
				if '0' <= r && r <= '7' {
					n = 8*n + r - '0'
				} else {
					if i == 0 {
						// Inferno leaves current on this
						// character and so reads it twice; Plan 9
						// tr consumes it.
						p.current = s
						return r
					}
					return n
				}
			}
			if n > 0o377 {
				fail("char>0377")
			}
			r = n
		}
	}
	p.current = s
	return r
}

func (p *pcb) canon() int {
	if p.final >= 0 {
		if p.last < p.final {
			p.last++
			return p.last
		}
		p.final = -1
	}
	if p.current >= len(p.spec) {
		return -1
	}
	if p.spec[p.current] == '-' && p.last >= 0 && p.current+1 < len(p.spec) {
		p.current++
		r := p.getc()
		if r < p.last {
			fail("Invalid range specification")
		}
		if r > p.last {
			p.final = r
			p.last++
			return p.last
		}
	}
	p.last = p.getc()
	return p.last
}
