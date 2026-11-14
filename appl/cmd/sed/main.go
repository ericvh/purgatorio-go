// Command sed is Inferno's partial stream editor (appl/cmd/sed.b), after
// Plan 9 sed.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Address kinds.
const (
	aNone = iota
	aDollar
	aLine
	aRegex
)

type addr struct {
	kind int
	line int
	re   *regex.Re
}

type sedcom struct {
	cmd      rune // the command letter; '{' and '}' become 'b' and ':'
	ad1, ad2 addr
	negfl    bool
	active   int

	gfl  bool
	pfl  int
	re   *regex.Re
	w    *bufio.Writer // s///w and w
	rhs  []rune
	text string // a, c, i text; r file name; label
	ymap map[rune]rune
}

var (
	nflag, gflag bool
	sflag        bool
	delflag      bool
	dolflag      bool
	jflag        bool
	restart      bool

	files     []string
	fout      *bufio.Writer
	infile    *bufio.Reader
	inclose   io.Closer
	peekc     rune // 0 none, -1 end of file
	lastregex *regex.Re
	linebuf   string
	filename  string
	lnum      int

	holdsp, patsp string

	cmds       []*sedcom
	appendlist []*sedcom
	flushes    []*bufio.Writer

	depth  int
	cmdcnt [maxdepth]int
)

const maxdepth = 20

func main() {
	a := arg.New(os.Args)
	compiled := false
	for c := a.Opt(); c != 0; c = a.Opt() {
		switch c {
		case 'n':
			nflag = true
		case 'g':
			gflag = true
		case 'e':
			s := a.Arg()
			if s == "" {
				usage(a)
			}
			filename = ""
			compile(bufio.NewReader(strings.NewReader(s + "\n")))
			compiled = true
		case 'f':
			if filename = a.Arg(); filename == "" {
				usage(a)
			}
			f, err := os.Open(filename)
			if err != nil {
				fatal(fmt.Sprintf("couldn't open '%s': %s", filename, sys.Errstr()))
			}
			compile(bufio.NewReader(f))
			f.Close()
			compiled = true
		case 'd':
		default:
			usage(a)
		}
	}
	args := a.Argv()
	if !compiled {
		if len(args) == 0 {
			fatal("missing pattern")
		}
		filename = ""
		compile(bufio.NewReader(strings.NewReader(args[0] + "\n")))
		args = args[1:]
	}
	files = args
	if len(files) == 0 {
		files = []string{""}
	}
	fout = bufio.NewWriter(os.Stdout)
	flushes = append(flushes, fout)
	lnum = 0
	execute()
	exits("")
}

func usage(a *arg.Arg) {
	fmt.Fprintf(os.Stderr, "usage: %s [-ngd] [-e expr] [-f file] [expr] [file...]\n", a.Progname())
	exits("usage")
}

func fatal(s string) {
	f := filename
	if f == "" {
		f = "<stdin>"
	}
	for _, b := range flushes {
		b.Flush()
	}
	fmt.Fprintf(os.Stderr, "%s:%d %s\n", f, lnum, s)
	sys.Exits("fail:error")
}

func exits(e string) {
	for _, b := range flushes {
		b.Flush()
	}
	if e != "" {
		sys.Exits("fail:" + e)
	}
	os.Exit(0)
}

// getline reads a script line; a backslash keeps the next character,
// newline included, along with itself.
func getline(b *bufio.Reader) (string, bool) {
	lnum++
	var w []rune
	for {
		r, _, err := b.ReadRune()
		if err != nil {
			return string(w), false
		}
		if r == '\\' {
			w = append(w, r)
			if r, _, err = b.ReadRune(); err != nil {
				return string(w), false
			}
		} else if r == '\n' {
			return string(w), true
		}
		w = append(w, r)
	}
}

func drop(s, chars string) string { return strings.TrimLeft(s, chars) }

func splitl(s, chars string) (string, string) {
	if i := strings.IndexAny(s, chars); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

func compile(b *bufio.Reader) {
	lnum = 1
	for {
		line, ok := getline(b)
		if !ok {
			return
		}
		linebuf = line
		s := line
		comment := false
		for {
			s = drop(s, " \t;")
			if s == "" || s[0] == '#' {
				comment = true
				break
			}
			var c sedcom
			s, c.ad1 = address(s)
			if c.ad1.kind != aNone && s != "" && (s[0] == ',' || s[0] == ';') {
				s, c.ad2 = address(s[1:])
			}
			s = drop(s, " \t")
			if s != "" && s[0] == '!' {
				c.negfl = true
				s = drop(s, "!")
			}
			s = drop(s, " \t")
			if s == "" {
				break
			}
			r := []rune(s)
			c.cmd, s = r[0], string(r[1:])
			switch c.cmd {
			case 'a', 'c', 'q', '=', 'i':
				if c.ad2.kind != aNone {
					fatal(fmt.Sprintf("only one address allowed:  '%s'", linebuf))
				}
			}
			switch c.cmd {
			default:
				fatal(fmt.Sprintf("unrecognised command: '%s' (%c)", linebuf, c.cmd))
			case 'a', 'c', 'i':
				s = strings.TrimPrefix(s, "\\")
				if s == "" || s[0] != '\n' {
					fatal(fmt.Sprintf("unexpected characters in %s command: %s", map[rune]string{'a': "a", 'c': "c", 'i': "i"}[c.cmd], s))
				}
				c.text, s = s[1:], ""
			case 'r':
				c.text, s = drop(s, " \t"), ""
			case 'w':
				s = drop(s, " \t")
				if s == "" {
					fatal("no filename in w command: " + linebuf)
				}
				c.w, s = wfile(s), ""
			case 'd', 'D', 'p', 'P', 'q', '=', 'g', 'G', 'h', 'H', 'n', 'N', 'x', 'l':
			case 'y':
				s = ycommand(&c, s)
			case 's':
				s = scommand(&c, s)
			case ':':
				var lab string
				lab, s = splitl(drop(s, " \t"), " \t;#")
				if lab == "" {
					fatal(fmt.Sprintf("null label: '%s'", linebuf))
				}
				if findlabel(lab) {
					fatal(fmt.Sprintf("duplicate label: '%s'", lab))
				}
				c.text = lab
			case 'b', 't':
				c.text, s = splitl(drop(s, " \t"), " \t;#")
			case '{':
				// A branch past the block when the address does not match.
				c.cmd = 'b'
				c.text = mklab(depth)
				c.negfl = !c.negfl
				depth++
				s = ";" + s
			case '}':
				if c.ad1.kind != aNone {
					fatal("did not expect address:" + linebuf)
				}
				if depth--; depth < 0 {
					fatal("too many }'s: " + linebuf)
				}
				c.cmd = ':'
				c.text = mklab(depth)
				cmdcnt[depth]++
				s = ";" + s
			}
			cmds = append(cmds, &c)
			if s == "" || !strings.ContainsRune(";{}", rune(s[0])) {
				break
			}
		}
		if s != "" && !comment {
			fatal("leftover junk: " + s)
		}
	}
}

func wfile(name string) *bufio.Writer {
	f, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	}
	if err != nil {
		fatal(fmt.Sprintf("can't create output file: '%s'", name))
	}
	b := bufio.NewWriter(f)
	flushes = append(flushes, b)
	return b
}

func ycommand(c *sedcom, s string) string {
	if s == "" {
		fatal("expected args: " + linebuf)
	}
	r := []rune(s)
	seof := string(r[0])
	s = string(r[1:])
	if s == "" {
		fatal("no lhs: " + linebuf)
	}
	lhs, s2 := splitl(s, seof)
	if s2 == "" {
		fatal("no lhs terminator: " + linebuf)
	}
	rhs, s4 := splitl(s2[len(seof):], seof)
	if s4 == "" {
		fatal("no rhs: " + linebuf)
	}
	l, rr := []rune(lhs), []rune(rhs)
	if len(l) != len(rr) {
		fatal("y command needs same length sets: " + linebuf)
	}
	c.ymap = map[rune]rune{}
	for i := len(l) - 1; i >= 0; i-- {
		c.ymap[l[i]] = rr[i]
	}
	return s4[len(seof):]
}

func scommand(c *sedcom, s string) string {
	if s == "" {
		fatal("too much text: " + linebuf)
	}
	seof := []rune(s)[0]
	c.re, s = recomp(s)
	s, c.rhs = compsub(string(seof) + s)
	c.gfl = gflag
	if strings.HasPrefix(s, "g") {
		c.gfl, s = true, s[1:]
	}
	if strings.HasPrefix(s, "p") {
		c.pfl, s = 1, s[1:]
	}
	if strings.HasPrefix(s, "P") {
		c.pfl, s = 2, s[1:]
	}
	if strings.HasPrefix(s, "w") {
		s = drop(s[1:], " \t")
		if s == "" {
			fatal("no filename in s with w: " + linebuf)
		}
		c.w, s = wfile(s), ""
	}
	return s
}

func findlabel(lab string) bool {
	for _, c := range cmds {
		if c.cmd == ':' && c.text == lab {
			return true
		}
	}
	return false
}

func mklab(depth int) string {
	return "_" + strconv.Itoa(cmdcnt[depth]) + "_" + strconv.Itoa(depth)
}

func address(s string) (string, addr) {
	if s == "" {
		return s, addr{}
	}
	switch c := s[0]; {
	case c == '$':
		return s[1:], addr{kind: aDollar}
	case c == '/':
		r, s1 := recomp(s)
		if r == nil {
			r = lastregex
		}
		if r == nil {
			fatal("First RE in address may not be null")
		}
		return s1, addr{kind: aRegex, re: r}
	case c >= '0' && c <= '9':
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		n, _ := strconv.Atoi(s[:i])
		if n == 0 {
			fatal("line no 0 is illegal address")
		}
		return s[i:], addr{kind: aLine, line: n}
	}
	return s, addr{}
}

// recomp compiles the regular expression delimited by s's first
// character and returns the rest; an empty expression gives nil.
func recomp(s string) (*regex.Re, string) {
	r := []rune(s)
	seof := r[0]
	r = r[1:]
	if len(r) > 0 && r[0] == seof {
		return nil, string(r[1:])
	}
	next := func() rune {
		if len(r) == 0 {
			return 0
		}
		c := r[0]
		r = r[1:]
		return c
	}
	var exp []rune
	c := next()
	for {
		if c == 0 || c == '\n' {
			fatal("too much text: " + linebuf)
		}
		if c == '\\' {
			exp = append(exp, c)
			if c = next(); c == 'n' {
				c = '\n'
			}
		}
		exp = append(exp, c)
		if c = next(); c == seof {
			break
		}
	}
	re, err := regex.Compile(string(exp), 1)
	if re == nil {
		fatal(fmt.Sprintf("%s '%s'", err, string(exp)))
	}
	lastregex = re
	return re, string(r)
}

func compsub(s string) (string, []rune) {
	r := []rune(s)
	seof := r[0]
	var rhs []rune
	i := 1
	for ; i < len(r); i++ {
		c := r[i]
		if c == seof {
			break
		}
		if c == '\\' {
			rhs = append(rhs, c)
			if i++; i >= len(r) {
				break
			}
			c = r[i]
		}
		rhs = append(rhs, c)
	}
	if i >= len(r) {
		fatal(fmt.Sprintf("no closing %c in replacement text: %s", seof, linebuf))
	}
	return string(r[i+1:]), rhs
}

func execute() {
	for {
		if !restart {
			s, ok := gline()
			if !ok {
				return
			}
			patsp = s
		}
		restart = false
		cycle()
		if !nflag && !delflag && !restart {
			fout.WriteString(patsp + "\n")
		}
		if !restart {
			arout()
		}
		delflag = false
	}
}

// cycle runs the script once over the pattern space.
func cycle() {
	for p := 0; p < len(cmds); {
		c := cmds[p]
		if !c.executable() {
			p++
			continue
		}
		c.command()
		if delflag || restart {
			return
		}
		if !jflag {
			p++
			continue
		}
		jflag = false
		found := false
		for i, l := range cmds {
			if l.cmd == ':' && l.text == c.text {
				p, found = i, true
				break
			}
		}
		if !found {
			return // a branch to no label ends the script
		}
	}
}

func match(re *regex.Re, s string) bool {
	return re != nil && re.Execute([]rune(s)) != nil
}

func (c *sedcom) executable() bool {
	if c.active != 0 {
		if c.active == 1 {
			c.active = 2
		}
		switch c.ad2.kind {
		case aNone:
			c.active = 0
		case aDollar:
			return !c.negfl
		case aLine:
			if lnum <= c.ad2.line {
				if c.ad2.line == lnum {
					c.active = 0
				}
				return !c.negfl
			}
			c.active = 0
			return c.negfl
		case aRegex:
			if match(c.ad2.re, patsp) {
				c.active = 0
			}
			return !c.negfl
		}
	}
	switch c.ad1.kind {
	case aNone:
		return !c.negfl
	case aDollar:
		if dolflag {
			return !c.negfl
		}
	case aLine:
		if c.ad1.line == lnum {
			c.active = 1
			return !c.negfl
		}
	case aRegex:
		if match(c.ad1.re, patsp) {
			c.active = 1
			return !c.negfl
		}
	}
	return c.negfl
}

func (c *sedcom) command() {
	switch c.cmd {
	case 's':
		var m bool
		if m, patsp = substitute(c, patsp); m {
			switch c.pfl {
			case 1:
				fout.WriteString(patsp + "\n")
			case 2:
				l, rest := splitl(patsp, "\n")
				fout.WriteString(l + "\n")
				patsp = rest
			}
			if c.w != nil {
				c.w.WriteString(patsp + "\n")
			}
		}
	case 'p':
		fout.WriteString(patsp + "\n")
	case 'P':
		l, _ := splitl(patsp, "\n")
		fout.WriteString(l + "\n")
	case 'a', 'r':
		appendlist = append(appendlist, c)
	case 'c':
		delflag = true
		if c.active == 1 {
			fout.WriteString(c.text + "\n")
		}
	case 'i':
		fout.WriteString(c.text + "\n")
	case 'w':
		c.w.WriteString(patsp + "\n")
	case 'g':
		patsp = holdsp
	case 'G':
		patsp += "\n" + holdsp
	case 'h':
		holdsp = patsp
	case 'H':
		holdsp += "\n" + patsp
	case 'x':
		holdsp, patsp = patsp, holdsp
	case 'y':
		r := []rune(patsp)
		for i, ch := range r {
			if d, ok := c.ymap[ch]; ok {
				r[i] = d
			}
		}
		patsp = string(r)
	case 'd':
		delflag = true
	case 'D':
		// Delete up to the first newline and start again without reading
		// a new line; with no newline, as d.
		if i := strings.IndexByte(patsp, '\n'); i >= 0 {
			patsp = patsp[i+1:]
			restart = true
		} else {
			delflag = true
		}
	case 'q':
		if !nflag {
			fout.WriteString(patsp + "\n")
		}
		arout()
		exits("")
	case 'n':
		if !nflag {
			fout.WriteString(patsp + "\n")
		}
		arout()
		var ok bool
		if patsp, ok = gline(); !ok {
			delflag = true
		}
	case 'N':
		arout()
		ns, ok := gline()
		if !ok {
			delflag = true
		}
		patsp += "\n" + ns
	case '=':
		fmt.Fprintf(fout, "%d\n", lnum)
	case ':':
	case 'b':
		jflag = true
	case 't':
		if sflag {
			sflag = false
			jflag = true
		}
	case 'l':
		col := 0
		var cc rune
		for _, cc = range patsp {
			var s string
			if cc >= 0x20 && cc < 0x7f {
				s = string(cc)
			} else {
				s = trans(cc)
			}
			for _, ch := range s {
				fout.WriteRune(ch)
				if col > 71 {
					fout.WriteString("\\\n")
					col = 0
				} else {
					col++
				}
			}
		}
		if cc == ' ' {
			fout.WriteString("\\n")
		}
		fout.WriteByte('\n')
	}
}

func trans(ch rune) string {
	switch ch {
	case '\b':
		return "\\b"
	case '\n':
		return "\\n"
	case '\r':
		return "\\r"
	case '\t':
		return "\\t"
	case '\\':
		return "\\\\"
	}
	return fmt.Sprintf("\\u%.4x", ch)
}

// arout writes the queued a text and r files.
func arout() {
	for _, c := range appendlist {
		if c.cmd == 'a' {
			fout.WriteString(c.text + "\n")
			continue
		}
		f, err := os.Open(c.text)
		if err != nil {
			fatal(fmt.Sprintf("couldn't open '%s'", c.text))
		}
		io.Copy(fout, f)
		f.Close()
	}
	appendlist = appendlist[:0]
}

func substitute(c *sedcom, str string) (bool, string) {
	if !match(c.re, str) {
		return false, str
	}
	sflag = true
	s := []rune(str)
	start := 0
	for {
		m := c.re.ExecuteSE(s, start, len(s), start == 0, true)
		if m == nil {
			break
		}
		l, r := m[0].Beg, m[0].End
		var rep []rune
		for i := 0; i < len(c.rhs); i++ {
			ch := c.rhs[i]
			if ch != '\\' || i+1 == len(c.rhs) {
				if ch == '&' {
					rep = append(rep, s[l:r]...)
				} else {
					rep = append(rep, ch)
				}
				continue
			}
			i++
			switch ch = c.rhs[i]; {
			case ch >= '0' && ch <= '9':
				if n := int(ch - '0'); n < len(m) && m[n].Beg >= 0 {
					rep = append(rep, s[m[n].Beg:m[n].End]...)
				}
			case ch == 'n':
				rep = append(rep, '\n')
			default:
				rep = append(rep, ch)
			}
		}
		ns := append(append(append([]rune{}, s[:l]...), rep...), s[r:]...)
		s = ns
		start = l + len(rep)
		if r == l {
			start++
		}
		if !c.gfl || start > len(s) {
			break
		}
	}
	return true, string(s)
}

func readc() rune {
	r, _, err := infile.ReadRune()
	if err != nil {
		return -1
	}
	return r
}

// gline reads the next input line, moving through the files; dolflag is
// set at the end of the last one.
func gline() (string, bool) {
	if infile == nil && !opendatafile() {
		return "", false
	}
	sflag = false
	lnum++
	for {
		var s []rune
		c := peekc
		if c == 0 {
			c = readc()
		}
		for ; c != -1; c = readc() {
			if c == '\n' {
				if peekc = readc(); peekc == -1 && len(files) == 0 {
					dolflag = true
				}
				return string(s), true
			}
			s = append(s, c)
		}
		if len(s) != 0 {
			peekc = -1
			dolflag = len(files) == 0
			return string(s), true
		}
		peekc = 0
		closein()
		if !opendatafile() {
			return "", false
		}
	}
}

func closein() {
	if inclose != nil {
		inclose.Close()
		inclose = nil
	}
	infile = nil
}

func opendatafile() bool {
	if len(files) == 0 {
		return false
	}
	name := files[0]
	if name != "" {
		f, err := os.Open(name)
		if err != nil {
			fatal(fmt.Sprintf("can't open '%s'", name))
		}
		infile, inclose = bufio.NewReader(f), f
	} else {
		infile = bufio.NewReader(os.Stdin)
	}
	files = files[1:]
	return true
}
