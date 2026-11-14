// Command ed is the line editor (Inferno appl/cmd/ed.b, after Plan 9 ed).
//
// The buffer is held in memory rather than in a temporary file.
package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	eof    = -1
	escflg = 0xFFFF // marks an escaped character in the replacement
	lbsize = 4096
	maxsub = 9
)

// Sources of lines for appendLines.
const (
	getTTY = iota
	getSub
	getCopy
	getFile
)

// A line of the buffer. Marks and undo compare lines by identity, as
// Inferno compares temporary file offsets.
type line struct {
	text []rune
	mark bool // selected by g or v
}

type globp struct {
	s     []rune
	isnil bool
}

// savej unwinds to the command loop after an error, like Inferno's
// "savej" exception.
type savej struct{}

var (
	addr1, addr2 int
	anymarks     bool
	col          int
	count        int
	dol, dot     int
	fchange      bool
	file         string
	given        bool
	gp           = globp{isnil: true}
	in           *bufio.Reader // a file being read
	out          *bufio.Writer // a file being written
	inf          io.Closer
	lastc        rune
	linebuf      []rune
	linebp       = -1
	listf, listn int
	loc1, loc2   int
	names        [26]*line
	oflag        bool
	pattern      *regex.Re
	peekc        rune
	pflag        bool
	rhsbuf       []rune
	savedfile    string
	subnewa      *line
	subolda      *line
	subexp       []regex.Range
	wrapp        bool
	zero         []*line // zero[0] is a sentinel; lines are 1 to dol
	vflag        = true
	bpagesize    = 20
	bformat      int
	bnum         int
	obuf         []byte
	stdin        = make([]byte, 1)
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-o" {
		oflag, vflag = true, false
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "-" {
		vflag = false
		args = args[1:]
	}
	if oflag {
		savedfile = "/fd/1"
		gp = globp{s: []rune("a")}
	} else if len(args) > 0 {
		savedfile = args[0]
		gp = globp{s: []rune("r")}
	}
	initbuf()
	for {
		run(func() {
			commands()
			quit()
		})
	}
}

// run calls f, recovering from an error raised within it.
func run(f func()) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(savej); !ok {
				panic(r)
			}
		}
	}()
	f()
}

func initbuf() {
	names = [26]*line{}
	subnewa = nil
	anymarks = false
	zero = []*line{{}}
	dot, dol = 0, 0
}

func casee(c rune) {
	setnoaddr()
	if vflag && fchange {
		fchange = false
		edError("")
	}
	filename(c)
	initbuf()
	addr2 = 0
	caseread()
}

func casep() {
	newline()
	printcom()
}

func caseq() {
	setnoaddr()
	newline()
	quit()
}

func caseread() {
	f, err := os.Open(file)
	if err != nil {
		lastc = '\n'
		edError(file)
	}
	in, inf = bufio.NewReader(f), f
	setwide()
	squeeze(0)
	c := dol != 0
	appendLines(getFile, addr2)
	exfile(false)
	fchange = c
}

func commands() {
	for {
		if pflag {
			pflag = false
			addr1, addr2 = dot, dot
			printcom()
		}
		c := rune('\n')
		var a1 int
		var lastsep rune
		for addr1 = -1; ; {
			lastsep = c
			a1 = address()
			c = getchr()
			if c != ',' && c != ';' {
				break
			}
			if lastsep == ',' {
				edError("")
			}
			if a1 < 0 {
				a1 = 1
				if a1 > dol {
					a1--
				}
			}
			addr1 = a1
			if c == ';' {
				dot = a1
			}
		}
		if lastsep != '\n' && a1 < 0 {
			a1 = dol
		}
		if addr2 = a1; a1 < 0 {
			given = false
			addr2 = dot
		} else {
			given = true
		}
		if addr1 < 0 {
			addr1 = addr2
		}
		switch c {
		case 'a':
			add(0)
		case 'b':
			nonzero()
			browse()
		case 'c':
			nonzero()
			newline()
			rdelete(addr1, addr2)
			appendLines(getTTY, addr1-1)
		case 'd':
			nonzero()
			newline()
			rdelete(addr1, addr2)
		case 'E':
			fchange = false
			casee('e')
		case 'e':
			casee(c)
		case 'f':
			setnoaddr()
			filename(c)
			putst(savedfile)
		case 'g':
			global(true)
		case 'i':
			add(-1)
		case 'j':
			if !given {
				addr2++
			}
			newline()
			join()
		case 'k':
			nonzero()
			c = getchr()
			if c < 'a' || c > 'z' {
				edError("")
			}
			newline()
			names[c-'a'] = zero[addr2]
			anymarks = true
		case 'm':
			move(false)
		case 'n':
			listn++
			newline()
			printcom()
		case '\n':
			if a1 < 0 {
				a1 = dot + 1
				addr1, addr2 = a1, a1
			}
			if lastsep == ';' {
				addr1 = a1
			}
			printcom()
		case 'l':
			listf++
			casep()
		case 'p', 'P':
			casep()
		case 'Q':
			fchange = false
			caseq()
		case 'q':
			caseq()
		case 'r':
			filename(c)
			caseread()
		case 's':
			nonzero()
			substitute(!gp.isnil)
		case 't':
			move(true)
		case 'u':
			nonzero()
			newline()
			if zero[addr2] != subnewa {
				edError("")
			}
			zero[addr2] = subolda
			dot = addr2
		case 'v':
			global(false)
		case 'W', 'w':
			casew(c)
		case '=':
			setwide()
			squeeze(0)
			newline()
			count = addr2
			putd()
			putchr('\n')
		case '!':
			callunix()
		case eof:
			return
		default:
			edError("")
		}
	}
}

func casew(c rune) {
	if c == 'W' {
		wrapp = true
	}
	setwide()
	sq := 0
	if dol > 0 {
		sq = 1
	}
	squeeze(sq)
	temp := getchr()
	if temp != 'q' && temp != 'Q' {
		peekc = temp
		temp = 0
	}
	filename(c)
	var f *os.File
	var err error
	if wrapp {
		f, err = os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0)
	}
	if !wrapp || err != nil {
		fd, err := sys.Create(file, sys.OWRITE, 0o666)
		if err != nil {
			edError(file)
		}
		f = os.NewFile(uintptr(fd), file)
	}
	out, inf = bufio.NewWriter(f), f
	wrapp = false
	if dol > 0 {
		putfile()
	}
	exfile(true)
	if addr1 <= 1 && addr2 == dol || temp == 'Q' {
		fchange = false
	}
	if temp != 0 {
		quit()
	}
}

func printcom() {
	nonzero()
	a1 := addr1
	for {
		if listn != 0 {
			count = a1
			putd()
			putchr('\t')
		}
		getline(zero[a1])
		a1++
		putshst(linebuf)
		if a1 > addr2 {
			break
		}
	}
	dot = addr2
	listf, listn = 0, 0
	pflag = false
}

func address() int {
	nextopand := -1
	sign := 1
	opcnt := 0
	a := dot
	for ok := true; ok; ok = 0 <= a && a <= dol {
		c := getchr()
		for c == ' ' || c == '\t' {
			c = getchr()
		}
		switch {
		case c >= '0' && c <= '9':
			peekc = c
			if opcnt == 0 {
				a = 0
			}
			a += sign * getnum()
		case c == '$' || c == '.':
			if c == '$' {
				a = dol
			}
			if opcnt != 0 {
				edError("")
			}
		case c == '\'':
			c = getchr()
			if opcnt != 0 || c < 'a' || c > 'z' {
				edError("")
			}
			a = 0
			for {
				a++
				if a > dol || names[c-'a'] == zero[a] {
					break
				}
			}
		case c == '?' || c == '/':
			if c == '?' {
				sign = -sign
			}
			compile(c)
			b := a
			for {
				a += sign
				if a <= 0 {
					a = dol
				}
				if a > dol {
					a = 0
				}
				if match(a) {
					break
				}
				if a == b {
					edError("")
				}
			}
		default:
			if nextopand == opcnt {
				a += sign
				if a < 0 || dol < a {
					continue
				}
			}
			if c != '+' && c != '-' && c != '^' {
				peekc = c
				if opcnt == 0 {
					a = -1
				}
				return a
			}
			sign = 1
			if c != '+' {
				sign = -sign
			}
			opcnt++
			nextopand = opcnt
			continue
		}
		sign = 1
		opcnt++
	}
	edError("")
	return -1
}

func getnum() int {
	r := 0
	for {
		c := getchr()
		if c < '0' || c > '9' {
			peekc = c
			return r
		}
		r = r*10 + int(c-'0')
	}
}

func setwide() {
	if !given {
		addr1 = 0
		if dol > 0 {
			addr1 = 1
		}
		addr2 = dol
	}
}

func setnoaddr() {
	if given {
		edError("")
	}
}

func nonzero() { squeeze(1) }

func squeeze(i int) {
	if addr1 < i || addr2 > dol || addr1 > addr2 {
		edError("")
	}
}

func newline() {
	c := getchr()
	if c == '\n' || c == eof {
		return
	}
	if c == 'p' || c == 'l' || c == 'n' {
		pflag = true
		if c == 'l' {
			listf++
		} else if c == 'n' {
			listn++
		}
		if c = getchr(); c == '\n' {
			return
		}
	}
	edError("")
}

func filename(comm rune) {
	count = 0
	c := getchr()
	if c == '\n' || c == eof {
		if savedfile == "" && comm != 'f' {
			edError("")
		}
		file = savedfile
		return
	}
	if c != ' ' {
		edError("")
	}
	for c = getchr(); c == ' '; c = getchr() {
	}
	if c == '\n' {
		edError("")
	}
	var name []rune
	for c != '\n' {
		if c == ' ' || c == eof {
			edError("")
		}
		name = append(name, c)
		c = getchr()
	}
	file = string(name)
	if savedfile == "" || comm == 'e' || comm == 'f' {
		savedfile = file
	}
}

func exfile(write bool) {
	if write {
		if out.Flush() != nil {
			inf.Close()
			edError("")
		}
	}
	inf.Close()
	in, out, inf = nil, nil, nil
	if vflag {
		putd()
		putchr('\n')
	}
}

func error1(s string) {
	wrapp = false
	listf, listn = 0, 0
	count = 0
	sys.Seek(0, 0, 2)
	pflag = false
	if !gp.isnil {
		lastc = '\n'
	}
	gp = globp{isnil: true}
	peekc = lastc
	if lastc != 0 {
		for {
			if c := getchr(); c == '\n' || c == eof {
				break
			}
		}
	}
	if inf != nil {
		inf.Close()
		in, out, inf = nil, nil, nil
	}
	putchr('?')
	putst(s)
}

func edError(s string) {
	error1(s)
	panic(savej{})
}

// getchr returns the next command character: a pushed-back one, the next
// of a global command, or one read a byte at a time from standard input
// so that nothing past the command is consumed.
func getchr() rune {
	if lastc = peekc; lastc != 0 {
		peekc = 0
		return lastc
	}
	if !gp.isnil {
		if len(gp.s) > 0 {
			lastc = gp.s[0]
			gp.s = gp.s[1:]
			return lastc
		}
		gp = globp{isnil: true}
		return eof
	}
	var b []byte
	for {
		n, err := os.Stdin.Read(stdin)
		if n <= 0 || err != nil {
			lastc = eof
			return eof
		}
		b = append(b, stdin[0])
		if utf8.FullRune(b) {
			break
		}
	}
	lastc, _ = utf8.DecodeRune(b)
	return lastc
}

func gety() int {
	gf := !gp.isnil
	linebuf = linebuf[:0]
	for {
		c := getchr()
		if c == '\n' {
			return 0
		}
		if c == eof {
			if gf {
				peekc = c
			}
			return eof
		}
		if c == 0 {
			continue
		}
		linebuf = append(linebuf, c)
		if len(linebuf) >= lbsize {
			edError("")
		}
	}
}

func gettty() int {
	if rc := gety(); rc != 0 {
		return rc
	}
	if len(linebuf) == 1 && linebuf[0] == '.' {
		return eof
	}
	return 0
}

func getfile() int {
	linebuf = linebuf[:0]
	for {
		c, _, err := in.ReadRune()
		if err != nil {
			if len(linebuf) == 0 {
				return eof
			}
			putst("'\\n' appended")
			c = '\n'
		}
		if len(linebuf) >= lbsize {
			lastc = '\n'
			edError("")
		}
		count++
		if c == '\n' {
			return 0
		}
		linebuf = append(linebuf, c)
	}
}

func putfile() {
	for a1 := addr1; a1 <= addr2; a1++ {
		getline(zero[a1])
		count += len(linebuf) + 1
		out.WriteString(string(linebuf))
		out.WriteByte('\n')
	}
	if out.Flush() != nil {
		edError("")
	}
}

func appendLines(f int, a int) int {
	nline := 0
	dot = a
	for {
		var rv int
		switch f {
		case getTTY:
			rv = gettty()
		case getSub:
			rv = getsub()
		case getCopy:
			rv = getcopy()
		case getFile:
			rv = getfile()
		}
		if rv != 0 {
			break
		}
		l := putline()
		nline++
		dol++
		dot++
		zero = append(zero, nil)
		copy(zero[dot+1:], zero[dot:])
		zero[dot] = l
	}
	return nline
}

func add(i int) {
	if i != 0 && (given || dol > 0) {
		addr1--
		addr2--
	}
	squeeze(0)
	newline()
	appendLines(getTTY, addr2)
}

func browse() {
	forward := true
	peekc = getchr()
	if peekc != '\n' {
		if peekc == '-' || peekc == '+' {
			if peekc == '-' {
				forward = false
			}
			getchr()
		}
		if n := getnum(); n > 0 {
			bpagesize = n
		}
	}
	newline()
	if pflag {
		bformat, bnum = listf, listn
	} else {
		listf, listn = bformat, bnum
	}
	if forward {
		addr1 = addr2
		addr2 = min(addr2+bpagesize, dol)
	} else {
		addr1 = max(addr2-bpagesize, 1)
	}
	printcom()
}

// callunix runs the rest of the line with sh -c.
func callunix() {
	setnoaddr()
	var buf []rune
	for c := getchr(); c != eof && c != '\n'; c = getchr() {
		buf = append(buf, c)
	}
	flushout()
	me, _ := sys.Pctl(0, nil)
	w, err := sys.Open("/prog/"+strconv.Itoa(me)+"/wait", sys.OREAD)
	if err != nil {
		putst("can't load shell")
		return
	}
	wait := os.NewFile(uintptr(w), "wait")
	defer wait.Close()
	pid, err := sys.Spawn("/bin/sh.wasm", []string{"sh", "-c", string(buf)}, sys.NEWFD,
		[][2]int{{0, 0}, {1, 1}, {2, 2}})
	if err != nil {
		putst("can't load shell")
		return
	}
	msg := make([]byte, 256)
	for {
		n, err := wait.Read(msg)
		if err != nil || n == 0 {
			break
		}
		if strings.HasPrefix(string(msg[:n]), strconv.Itoa(pid)+" ") {
			break
		}
	}
	if vflag {
		putst("!")
	}
}

func quit() {
	if vflag && fchange && dol != 0 {
		fchange = false
		edError("")
	}
	flushout()
	os.Exit(0)
}

func rdelete(ad1, ad2 int) {
	zero = append(zero[:ad1], zero[ad2+1:]...)
	dol = len(zero) - 1
	dot = min(ad1, dol)
	fchange = true
}

// gdelete removes the marked lines, for g/re/d.
func gdelete() {
	a1 := 0
	for !zero[a1].mark {
		if a1 >= dol {
			return
		}
		a1++
	}
	for a2 := a1 + 1; a2 <= dol; {
		if zero[a2].mark {
			a2++
			dot = a1
		} else {
			zero[a1] = zero[a2]
			a1++
			a2++
		}
	}
	zero = zero[:a1]
	dol = a1 - 1
	dot = min(dot, dol)
	fchange = true
}

func getline(l *line) {
	linebuf = append(linebuf[:0], l.text...)
}

// putline makes a new line of linebuf up to any newline, leaving linebp
// at the rest for getsub.
func putline() *line {
	fchange = true
	t := linebuf
	for i, c := range linebuf {
		if c == '\n' {
			t = linebuf[:i]
			linebp = i + 1
			break
		}
	}
	return &line{text: append([]rune(nil), t...)}
}

func global(k bool) {
	if !gp.isnil {
		edError("")
	}
	setwide()
	sq := 0
	if dol > 0 {
		sq = 1
	}
	squeeze(sq)
	c := getchr()
	if c == '\n' {
		edError("")
	}
	compile(c)
	var globuf []rune
	for c = getchr(); c != '\n'; c = getchr() {
		if c == eof {
			edError("")
		}
		if c == '\\' {
			if c = getchr(); c != '\n' {
				globuf = append(globuf, '\\')
			}
		}
		globuf = append(globuf, c)
	}
	if len(globuf) == 0 {
		globuf = []rune("p")
	}
	globuf = append(globuf, '\n')
	for a1 := 0; a1 <= dol; a1++ {
		zero[a1].mark = false
		if a1 >= addr1 && a1 <= addr2 && match(a1) == k {
			zero[a1].mark = true
		}
	}
	if string(globuf) == "d\n" {
		gdelete()
		return
	}
	for a1 := 0; a1 <= dol; a1++ {
		if zero[a1].mark {
			zero[a1].mark = false
			dot = a1
			gp = globp{s: append([]rune(nil), globuf...)}
			commands()
			a1 = 0
		}
	}
}

func join() {
	nonzero()
	var g []rune
	for a1 := addr1; a1 <= addr2; a1++ {
		getline(zero[a1])
		g = append(g, linebuf...)
		if len(g) >= lbsize-2 {
			edError("")
		}
	}
	linebuf = append(linebuf[:0], g...)
	zero[addr1] = putline()
	if addr1 < addr2 {
		rdelete(addr1+1, addr2)
	}
	dot = addr1
}

func substitute(inglob bool) {
	n := getnum() // 0 is as 1
	gsubf := compsub()
	for a1 := addr1; a1 <= addr2; a1++ {
		if !match(a1) {
			continue
		}
		m := n
		for {
			span := loc2 - loc1
			m--
			if m <= 0 {
				dosub()
				if !gsubf {
					break
				}
				if span == 0 {
					if loc2 >= len(linebuf) {
						break
					}
					loc2++
				}
			}
			if !match(-1) {
				break
			}
		}
		if m <= 0 {
			inglob = true
			subnewa = putline()
			zero[a1].mark = false
			if anymarks {
				for i := range names {
					if names[i] == zero[a1] {
						names[i] = subnewa
					}
				}
			}
			subolda = zero[a1]
			zero[a1] = subnewa
			addr2 += appendLines(getSub, a1)
		}
	}
	if !inglob {
		edError("")
	}
}

func compsub() bool {
	seof := getchr()
	if seof == '\n' || seof == ' ' {
		edError("")
	}
	compile(seof)
	rhsbuf = rhsbuf[:0]
	for {
		c := getchr()
		if c == '\\' {
			c = getchr()
			rhsbuf = append(rhsbuf, escflg)
		} else if c == '\n' && (gp.isnil || len(gp.s) == 0) {
			peekc = c
			pflag = true
			break
		} else if c == seof {
			break
		}
		rhsbuf = append(rhsbuf, c)
		if len(rhsbuf) >= lbsize/2 {
			edError("")
		}
	}
	if peekc = getchr(); peekc == 'g' {
		peekc = 0
		newline()
		return true
	}
	newline()
	return false
}

func getsub() int {
	if linebp == -1 {
		return eof
	}
	linebuf = append([]rune(nil), linebuf[linebp:]...)
	linebp = -1
	return 0
}

func dosub() {
	g := append([]rune(nil), linebuf[:loc1]...)
	for rp := 0; rp < len(rhsbuf); rp++ {
		c := rhsbuf[rp]
		if c == '&' {
			g = append(g, linebuf[loc1:loc2]...)
			continue
		}
		if c == escflg {
			rp++
			if rp < len(rhsbuf) {
				c = rhsbuf[rp]
			} else {
				c = 0
			}
			if c >= '1' && c < maxsub+'0' {
				n := int(c - '0')
				if n < len(subexp) && subexp[n].Beg >= 0 && subexp[n].End >= 0 {
					g = append(g, linebuf[subexp[n].Beg:subexp[n].End]...)
					continue
				}
				edError("")
			}
		}
		g = append(g, c)
		if len(g) >= lbsize {
			edError("")
		}
	}
	rest := linebuf[loc2:]
	loc2 = len(g)
	g = append(g, rest...)
	if len(g) >= lbsize {
		edError("")
	}
	linebuf = g
}

func move(cflag bool) {
	nonzero()
	adt := address()
	if adt < 0 {
		edError("")
	}
	newline()
	var ad1, ad2 int
	if cflag {
		ad1 = dol
		appendLines(getCopy, ad1)
		ad1++
		ad2 = dol
	} else {
		ad2 = addr2
		for ad1 = addr1; ad1 <= ad2; ad1++ {
			zero[ad1].mark = false
		}
		ad1 = addr1
	}
	ad2++
	switch {
	case adt < ad1:
		dot = adt + (ad2 - ad1)
		if adt++; adt == ad1 {
			return
		}
		reverse(adt, ad1)
		reverse(ad1, ad2)
		reverse(adt, ad2)
	case adt >= ad2:
		dot = adt
		adt++
		reverse(ad1, ad2)
		reverse(ad2, adt)
		reverse(ad1, adt)
	default:
		edError("")
	}
	fchange = true
}

func reverse(a1, a2 int) {
	for a2--; a1 < a2; a1, a2 = a1+1, a2-1 {
		zero[a1], zero[a2] = zero[a2], zero[a1]
	}
}

func getcopy() int {
	if addr1 > addr2 {
		return eof
	}
	getline(zero[addr1])
	addr1++
	return 0
}

// compile reads a regular expression delimited by eof; an empty one
// reuses the last.
func compile(delim rune) {
	c := getchr()
	if c == '\n' {
		peekc = c
		c = delim
	}
	if c == delim {
		if pattern == nil {
			edError("")
		}
		return
	}
	pattern = nil
	var prog []rune
	for {
		if c == '\\' {
			prog = append(prog, '\\')
			if c = getchr(); c == '\n' {
				edError("")
			}
		}
		prog = append(prog, c)
		if c = getchr(); c == delim || c == '\n' {
			break
		}
	}
	if c == '\n' {
		peekc = c
	}
	pattern, _ = regex.Compile(string(prog), 1)
}

// match runs the pattern on line addr, or on the rest of linebuf after
// the last match when addr is -1.
func match(addr int) bool {
	if pattern == nil {
		return false
	}
	rsp := loc2
	if addr >= 0 {
		if addr == 0 {
			return false
		}
		getline(zero[addr])
		rsp = 0
	}
	if rsp > len(linebuf) {
		subexp = nil
	} else {
		subexp = pattern.ExecuteSE(linebuf, rsp, len(linebuf), rsp == 0, true)
	}
	if subexp != nil {
		loc1, loc2 = subexp[0].Beg, subexp[0].End
		return true
	}
	loc1, loc2 = -1, -1
	return false
}

func putd() {
	r := count % 10
	count /= 10
	if count != 0 {
		putd()
	}
	putchr('0' + rune(r))
}

func putst(s string) {
	col = 0
	for _, c := range s {
		putchr(c)
	}
	putchr('\n')
}

func putshst(s []rune) {
	col = 0
	for _, c := range s {
		putchr(c)
	}
	putchr('\n')
}

const hex = "0123456789abcdef"

// putchr collects output a line at a time, escaping it for l.
func putchr(c rune) {
	if listf != 0 {
		if c == '\n' {
			if len(obuf) > 0 && obuf[len(obuf)-1] == ' ' {
				obuf = append(obuf, '\\', 'n')
			}
		} else {
			if col > 72-6-2 {
				col = 8
				obuf = append(obuf, '\\', '\n', '\t')
			}
			col++
			switch {
			case c == '\b' || c == '\t' || c == '\\':
				obuf = append(obuf, '\\')
				if c == '\b' {
					c = 'b'
				} else if c == '\t' {
					c = 't'
				}
				col++
			case c < ' ' || c >= 0177:
				obuf = append(obuf, '\\', 'x', hex[c>>12&0xf], hex[c>>8&0xf], hex[c>>4&0xf])
				c = rune(hex[c&0xf])
				col += 5
			}
		}
	}
	obuf = utf8.AppendRune(obuf, c)
	if c == '\n' || len(obuf) >= 70-5 {
		flushout()
	}
}

func flushout() {
	if len(obuf) == 0 {
		return
	}
	if oflag {
		os.Stderr.Write(obuf)
	} else {
		os.Stdout.Write(obuf)
	}
	obuf = obuf[:0]
}
