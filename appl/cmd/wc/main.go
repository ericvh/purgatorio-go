// Command wc counts lines, words, characters, bad UTF-8 sequences and
// bytes (Inferno wc(1)).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var pline, pword, pchar, pbadr, pbyte bool

type counts struct{ line, word, char, badr, byte int }

func main() {
	args := os.Args[1:]
	for len(args) > 0 {
		a := args[0]
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			break
		}
		for _, c := range a[1:] {
			switch c {
			case 'l':
				pline = true
			case 'w':
				pword = true
			case 'c':
				pchar = true
			case 'e':
				pbadr = true
			case 'b':
				pbyte = true
			default:
				os.Stderr.WriteString("usage: wc [-lwcbe] [file ...]\n")
				sys.Exits("fail:usage")
			}
		}
		args = args[1:]
	}
	if !pline && !pword && !pchar && !pbadr && !pbyte {
		pline, pword, pchar = true, true, true
	}
	if len(args) == 0 {
		report(count(os.Stdin, ""), "")
		return
	}
	var t counts
	for _, name := range args {
		f, err := os.Open(name)
		if err != nil {
			os.Stderr.WriteString("wc: can't open " + name + ": " + sys.Errstr() + "\n")
			continue
		}
		c := count(f, name)
		report(c, name)
		f.Close()
		t.line += c.line
		t.word += c.word
		t.char += c.char
		t.badr += c.badr
		t.byte += c.byte
	}
	if len(args) > 1 {
		report(t, "total")
	}
}

func report(c counts, name string) {
	s := ""
	for _, f := range []struct {
		p bool
		n int
	}{{pline, c.line}, {pword, c.word}, {pchar, c.char}, {pbadr, c.badr}, {pbyte, c.byte}} {
		if f.p {
			s += fmt.Sprintf(" %7d", f.n)
		}
	}
	if name != "" {
		s += " " + name
	}
	os.Stdout.WriteString(s[1:] + "\n")
}

// The states of the counting machine: between words, in a word, and
// expecting one or two more bytes of a UTF-8 sequence.
const (
	statesp = iota
	statewd
	statec2
	statec3
)

// Byte classes.
const (
	cblank = iota // tab, space
	cnl
	cascii
	ccont // 10xxxxxx
	clead2
	clead3
	chigh // f0-ff
)

func class(b byte) int {
	switch {
	case b == '\t' || b == ' ':
		return cblank
	case b == '\n':
		return cnl
	case b < 0x80:
		return cascii
	case b < 0xc0:
		return ccont
	case b < 0xe0:
		return clead2
	case b < 0xf0:
		return clead3
	}
	return chigh
}

// count runs Inferno wc's state machine over f. Characters are counted
// as bytes and decreased for each continuation byte that completes a
// rune; bad sequences count separately.
func count(f *os.File, name string) counts {
	var c counts
	state := statesp
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		c.byte += n
		c.char += n
		for _, b := range buf[:n] {
			k := class(b)
			bad := false
			switch k {
			case cblank, cnl:
				if k == cnl {
					c.line++
				}
				bad = state == statec2 || state == statec3
				state = statesp
			case cascii, chigh:
				if state == statesp {
					c.word++
				}
				bad = k == chigh || state == statec2 || state == statec3
				state = statewd
			case ccont:
				switch state {
				case statesp:
					c.word++
					bad = true
					state = statewd
				case statewd:
					bad = true
				case statec2:
					c.char--
					state = statewd
				case statec3:
					c.char--
					state = statec2
				}
			case clead2, clead3:
				if state == statesp {
					c.word++
				}
				bad = state == statec2 || state == statec3
				state = statec2
				if k == clead3 {
					state = statec3
				}
			}
			if bad {
				c.badr++
			}
		}
		if err == io.EOF || n == 0 && err == nil {
			break
		}
		if err != nil {
			os.Stderr.WriteString("wc: error reading " + name + ": " + sys.Errstr() + "\n")
			break
		}
	}
	if state != statesp && state != statewd {
		c.badr++
	}
	return c
}
