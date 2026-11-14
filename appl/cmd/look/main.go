// Command look finds lines in a sorted file that begin with a string, by
// binary search (Inferno look(1)).
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	filename                         = "/lib/words"
	debug, fold, direc, exact, iflag bool
	rangeflag                        bool
	tab                              = '\t'
	compare                          = acomp
	entry, word, key, orig           string
	dfile                            *dict
	out                              *bufio.Writer
	rev                              = 1
)

// latinFold maps Latin-1 letters from U+00C0 to their ASCII base letter.
var latinFold = []rune("aaaaaaaceeeeiiiidnooooo\x00ouuuuy\x00\x00aaaaaaaceeeeiiiidnooooo\x00ouuuuy\x00y")

// dict is the dictionary file, readable by line and by rune with seeks.
type dict struct {
	f   *os.File
	r   *bufio.Reader
	off int64
}

func (d *dict) seek(off int64, whence int) int64 {
	n, err := d.f.Seek(off, whence)
	if err != nil {
		n = d.off
	}
	d.off = n
	d.r.Reset(d.f)
	return n
}

func (d *dict) getc() rune {
	r, n, err := d.r.ReadRune()
	if err != nil {
		return -1
	}
	d.off += int64(n)
	return r
}

// gets returns the next line with its newline, or "" at the end.
func (d *dict) gets() string {
	s, _ := d.r.ReadString('\n')
	d.off += int64(len(s))
	return s
}

func main() {
	a := arg.New(os.Args)
	a.SetUsage("look -[dfinx] [-r lastkey] [-t c] [string] [file]")
	var lastkey string
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'D':
			debug = true
		case 'd':
			direc = true
		case 'f':
			fold = true
		case 'i':
			iflag = true
		case 'n':
			compare = ncomp
		case 't':
			t := []rune(a.EArg())
			tab = 0
			if len(t) > 0 {
				tab = t[0]
			}
		case 'x':
			exact = true
		case 'r':
			rangeflag = true
			lastkey = rcanon(a.EArg())
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	in := bufio.NewReader(os.Stdin)
	out = bufio.NewWriter(os.Stdout)
	defer out.Flush()
	if !iflag {
		if len(args) > 0 {
			orig, args = args[0], args[1:]
		} else {
			iflag = true
		}
	}
	if len(args) == 0 {
		direc, fold = true, true
	} else {
		filename = args[0]
	}
	if !iflag {
		key = rcanon(orig)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "orig %s key %s %s\n", orig, key, filename)
	}
	f, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "look: can't open %s\n", filename)
		sys.Exits("fail:no dictionary")
	}
	dfile = &dict{f: f, r: bufio.NewReader(f)}
	if !iflag && !locate() && !rangeflag {
		out.Flush()
		sys.Exits("fail:not found")
	}
	for {
		if iflag {
			out.Flush()
			s, err := in.ReadString('\n')
			if s == "" && err == io.EOF {
				return
			}
			orig = strings.TrimSuffix(s, "\n")
			key = rcanon(orig)
			if !locate() {
				continue
			}
		}
		if rangeflag {
			if compare(key, word) <= 0 && compare(word, lastkey) <= 0 {
				out.WriteString(entry)
			}
		} else if !exact || acomp(word, orig) == 0 {
			out.WriteString(entry)
		}
		matches(lastkey)
		if !iflag {
			break
		}
	}
}

// matches prints the entries that follow the located one while they
// still match.
func matches(lastkey string) {
	for {
		if entry = dfile.gets(); entry == "" {
			return
		}
		word = rcanon(entry)
		var n int
		if rangeflag {
			n = compare(word, lastkey)
		} else {
			n = compare(key, word)
		}
		if debug {
			fmt.Fprintf(out, "compare %d '%s'\n", n, word)
		}
		switch n {
		case -2:
			if !rangeflag {
				return
			}
			out.WriteString(entry)
		case -1:
			if exact {
				return
			}
			out.WriteString(entry)
		case 0:
			if !exact || acomp(word, orig) == 0 {
				out.WriteString(entry)
			}
		default:
			return
		}
	}
}

// locate leaves entry and word at the first line not before key, and
// reports whether it matches.
func locate() bool {
	bot := int64(0)
	top := dfile.seek(0, 2)
	var mid int64
	for {
		mid = (top + bot) / 2
		if debug {
			fmt.Fprintf(os.Stderr, "locate %d %d %d\n", top, mid, bot)
		}
		dfile.seek(mid, 0)
		for c := dfile.getc(); c >= 0 && c != '\n'; c = dfile.getc() {
		}
		mid = dfile.off
		if entry = dfile.gets(); entry == "" {
			break
		}
		word = rcanon(entry)
		n := compare(key, word)
		if debug {
			fmt.Fprintf(os.Stderr, "mid %d key: %s entry: %s\ncompare: %d\n", mid, key, word, n)
		}
		if n <= 0 {
			if top <= mid {
				break
			}
			top = mid
		} else {
			bot = mid
		}
	}
	dfile.seek(bot, 0)
	for {
		if entry = dfile.gets(); entry == "" {
			return false
		}
		word = rcanon(entry)
		switch compare(key, word) {
		case -2:
			return false
		case -1:
			return !exact
		case 0:
			return true
		}
	}
}

// acomp returns -2 if s sorts before t, -1 if s is a prefix of t, 0 if
// they are equal, 1 if t is a prefix of s and 2 if t sorts before s.
func acomp(s, t string) int {
	if s == t {
		return 0
	}
	rs, rt := []rune(s), []rune(t)
	l := min(len(rs), len(rt))
	i := 0
	for i < l && rs[i] == rt[i] {
		i++
	}
	switch {
	case i == len(rs):
		return -1
	case i == len(rt):
		return 1
	case rs[i] < rt[i]:
		return -2
	}
	return 2
}

// rcanon reduces a line to its key: up to the tab character, with Latin-1
// letters folded, and the -d and -f transformations.
func rcanon(s string) string {
	s = strings.TrimSuffix(s, "\n")
	var b []rune
	for _, r := range s {
		if r == tab {
			break
		}
		if r >= 0xc0 && r <= 0xff && latinFold[r-0xc0] != 0 {
			r = latinFold[r-0xc0]
		}
		if direc && !(isalnum(r) || r == ' ' || r == '\t') {
			continue
		}
		if fold && r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b = append(b, r)
	}
	return string(b)
}

func sgn(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}

func isdigit(c rune) bool { return c >= '0' && c <= '9' }
func isalnum(c rune) bool { return isdigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isspace(c rune) bool { return c == ' ' || c == '\t' || c >= 0x0a && c <= 0x0d }

// ncomp compares leading numbers: an optional minus sign, digits and an
// optional fraction.
func ncomp(s0, t0 string) int {
	s, t := []rune(s0), []rune(t0)
	for len(s) > 0 && isspace(s[0]) {
		s = s[1:]
	}
	for len(t) > 0 && isspace(t[0]) {
		t = t[1:]
	}
	ssgn, tsgn := -2*rev, -2*rev
	if len(s) > 0 && s[0] == '-' {
		s, ssgn = s[1:], -ssgn
	}
	if len(t) > 0 && t[0] == '-' {
		t, tsgn = t[1:], -tsgn
	}
	i := 0
	for i < len(s) && isdigit(s[i]) {
		i++
	}
	is, js := s[:i], s[i:]
	i = 0
	for i < len(t) && isdigit(t[i]) {
		i++
	}
	it, jt := t[:i], t[i:]
	a := 0
	i, j := len(is), len(it)
	if ssgn == tsgn {
		for j > 0 && i > 0 {
			j--
			i--
			if b := int(it[j] - is[i]); b != 0 {
				a = b
			}
		}
	}
	for i > 0 {
		i--
		if is[i] != '0' {
			return -ssgn
		}
	}
	for j > 0 {
		j--
		if it[j] != '0' {
			return tsgn
		}
	}
	if a != 0 {
		return sgn(a) * ssgn
	}
	s, t = js, jt
	if len(s) > 0 && s[0] == '.' {
		s = s[1:]
	}
	if len(t) > 0 && t[0] == '.' {
		t = t[1:]
	}
	if ssgn == tsgn {
		for len(s) > 0 && isdigit(s[0]) && len(t) > 0 && isdigit(t[0]) {
			if a = int(t[0] - s[0]); a != 0 {
				return sgn(a) * ssgn
			}
			s, t = s[1:], t[1:]
		}
	}
	for ; len(s) > 0 && isdigit(s[0]); s = s[1:] {
		if s[0] != '0' {
			return -ssgn
		}
	}
	for ; len(t) > 0 && isdigit(t[0]); t = t[1:] {
		if t[0] != '0' {
			return tsgn
		}
	}
	return 0
}
